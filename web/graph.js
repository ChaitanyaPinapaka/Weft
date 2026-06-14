// Force-directed graph over the vault. ESM import keeps this build-free,
// matching the TipTap editor's no-bundler philosophy.
//
// This is meant to read like the user's brain, not a generic node-link chart:
//  - nodes are colored by their top-level vault folder (a stable cluster hue),
//  - sized + brightened by `activation` (recent/frequent opens) on top of
//    backlink_count, so warm/active notes pop and dormant ones recede,
//  - backlink edges are solid, semantic (embedding) edges are faint + dashed
//    with opacity scaled by cosine weight,
//  - hovering a node lights up its neighborhood and dims the rest.
//
// We animate the force settle (decay alpha smoothly) instead of a hard freeze
// pop, then idle the sim once it's cool — a static snapshot has no reason to
// keep the CPU warm.

import * as d3 from 'https://esm.sh/d3@7';

(() => {
  const svgEl       = document.getElementById('graph');
  const tooltipEl   = document.getElementById('tooltip');
  const statusEl    = document.getElementById('status');
  const pathEl      = document.getElementById('path');
  const emptyEl     = document.getElementById('empty');
  const metaEl      = document.getElementById('meta');
  const minInput    = document.getElementById('min-backlinks');
  const tagCtrl     = document.getElementById('tag-control');
  const tagSelect   = document.getElementById('tag-filter');
  const resetBtn    = document.getElementById('reset-zoom');
  const fitBtn      = document.getElementById('fit-view');
  const semToggle   = document.getElementById('semantic-toggle');
  const legendEl    = document.getElementById('legend');
  const legendItems = document.getElementById('legend-items');
  const legendClear = document.getElementById('legend-clear');
  const minimapEl   = document.getElementById('minimap');

  const svg = d3.select(svgEl);
  // Two layered groups so zoom transforms a single <g> while keeping
  // edges painted below nodes regardless of DOM order. Labels ride above
  // nodes so haloed text is never occluded by a circle.
  const root       = svg.append('g').attr('class', 'zoom-root');
  const edgeLayer  = root.append('g').attr('class', 'edges');
  const nodeLayer  = root.append('g').attr('class', 'nodes');
  const labelLayer = root.append('g').attr('class', 'labels');

  const mmCtx = minimapEl.getContext('2d');

  // --- sizing ----------------------------------------------------------------
  // Radius blends structural importance (backlink_count) with warmth
  // (activation). Activation never zeroes a node out — even never-opened notes
  // get a small floor — so the graph stays legible while active notes bloom.
  const NODE_MIN = 4;
  const NODE_MAX = 26;
  const nodeRadius = (n) => {
    const struct = 1.4 * Math.sqrt(n.backlink_count || 0);
    const warmth = 7 * (n.activation || 0);
    return Math.min(NODE_MIN + struct + warmth, NODE_MAX);
  };

  // --- folder palette --------------------------------------------------------
  // Stable categorical assignment: folders are sorted then mapped to a fixed
  // palette by index, so a given folder keeps its hue across reloads. Root
  // notes ("" folder) read neutral. We resolve to CSS-var-friendly hex so the
  // legend swatches and node fills stay in lockstep.
  const PALETTE = [
    '#6ea8fe', '#f7768e', '#9ece6a', '#e0af68',
    '#bb9af7', '#7dcfff', '#ff9e64', '#73daca',
    '#c0caf5', '#f7c948', '#ad8b73', '#41a6b5',
  ];
  const ROOT_COLOR = '#8a8f98';
  const folderColors = new Map(); // folder -> hex
  function buildFolderColors(nodes) {
    folderColors.clear();
    const set = new Set();
    for (const n of nodes) set.add(n.folder || '');
    set.delete('');
    const ordered = [...set].sort();
    folderColors.set('', ROOT_COLOR);
    ordered.forEach((f, i) => folderColors.set(f, PALETTE[i % PALETTE.length]));
  }
  const nodeColor = (n) => folderColors.get(n.folder || '') || ROOT_COLOR;

  // --- zoom ------------------------------------------------------------------
  const zoom = d3.zoom()
    .scaleExtent([0.1, 8])
    .on('zoom', (event) => {
      currentTransform = event.transform;
      root.attr('transform', event.transform);
      updateLabelVisibility();
      drawMinimap();
    });
  svg.call(zoom);
  let currentTransform = d3.zoomIdentity;

  let allNodes = [];
  let allEdges = [];
  // Adjacency for focus/hover modes — neighbors keyed by node.path.
  const neighbors = new Map();
  const degree    = new Map(); // path -> edge count (for label threshold)
  let simulation = null;
  let nodeSel  = d3.select(null);
  let edgeSel  = d3.select(null);
  let labelSel = d3.select(null);
  let isolatedFolder = null; // legend filter
  let curNodes = [];         // last-rendered sim nodes (for minimap/fit)

  function setStatus(text, offline = false) {
    statusEl.textContent = text;
    statusEl.classList.toggle('offline', offline);
  }

  function showError(msg) {
    emptyEl.textContent = msg;
    emptyEl.classList.add('error');
    emptyEl.hidden = false;
  }
  function showEmpty(msg) {
    emptyEl.textContent = msg;
    emptyEl.classList.remove('error');
    emptyEl.hidden = false;
  }
  function clearOverlay() { emptyEl.hidden = true; }

  function edgeEnds(e) {
    const s = typeof e.src === 'string' ? e.src : e.src.path;
    const d = typeof e.dst === 'string' ? e.dst : e.dst.path;
    return [s, d];
  }

  function buildNeighbors(edges) {
    neighbors.clear();
    degree.clear();
    for (const e of edges) {
      const [s, d] = edgeEnds(e);
      if (!neighbors.has(s)) neighbors.set(s, new Set());
      if (!neighbors.has(d)) neighbors.set(d, new Set());
      neighbors.get(s).add(d);
      neighbors.get(d).add(s);
      degree.set(s, (degree.get(s) || 0) + 1);
      degree.set(d, (degree.get(d) || 0) + 1);
    }
  }

  // A note earns an always-on label if it's a structural hub OR runs hot.
  // Everyone else reveals their label on hover or once you zoom in close.
  const LABEL_DEGREE   = 4;
  const LABEL_ACTIVATION = 0.55;
  const LABEL_ZOOM     = 1.6; // below this scale, only hubs/hot notes are named
  function isAlwaysLabeled(n) {
    return (degree.get(n.path) || 0) >= LABEL_DEGREE ||
           (n.activation || 0) >= LABEL_ACTIVATION;
  }

  function applyFilters() {
    const minBL = Math.max(0, parseInt(minInput.value, 10) || 0);
    const tag   = tagSelect.value;
    const showSemantic = semToggle.checked;

    // Edges in play after the semantic toggle. A hidden semantic layer must
    // not rescue/connect nodes, so we compute visibility off this set too.
    const activeEdges = allEdges.filter(
      e => showSemantic || e.kind !== 'semantic');

    // Step 1: nodes passing direct filters (backlink count, tag, folder).
    const direct = new Set();
    for (const n of allNodes) {
      if ((n.backlink_count || 0) < minBL) continue;
      if (tag && !(n.tags || []).includes(tag)) continue;
      if (isolatedFolder !== null && (n.folder || '') !== isolatedFolder) continue;
      direct.add(n.path);
    }
    // Step 2: rescue connected leaves so hubs keep their satellites. Skip the
    // rescue while a folder is isolated — there we want a clean single cluster.
    const visible = new Set(direct);
    if (isolatedFolder === null) {
      for (const e of activeEdges) {
        const [s, d] = edgeEnds(e);
        if (direct.has(s)) visible.add(d);
        if (direct.has(d)) visible.add(s);
      }
    }

    const nodes = allNodes.filter(n => visible.has(n.path));
    const edges = activeEdges
      .map(e => {
        const [s, d] = edgeEnds(e);
        return { src: s, dst: d, kind: e.kind || 'backlink', weight: e.weight };
      })
      .filter(e => visible.has(e.src) && visible.has(e.dst));

    metaEl.textContent =
      `${nodes.length} / ${allNodes.length} nodes · ${edges.length} edges`;

    // Filters can prune a non-empty vault to zero visible nodes (e.g. a high
    // min-backlinks on a graph that leans on semantic edges). Surface a hint to
    // relax the filter instead of leaving a silently blank canvas.
    if (nodes.length === 0) {
      showEmpty('no notes match these filters — try lowering min backlinks or clearing the tag/folder filter');
    } else {
      clearOverlay();
    }
    render(nodes, edges);
  }

  function render(nodes, edges) {
    if (simulation) simulation.stop();

    // Clone nodes so the force sim can mutate x/y without poisoning allNodes
    // across re-renders. Preserve fx/fy from any pinned originals.
    const nodeById = new Map(nodes.map(n => [n.path, {
      ...n,
      x: n.x, y: n.y, fx: n.fx, fy: n.fy,
    }]));
    const simNodes = [...nodeById.values()];
    const simEdges = edges
      .filter(e => nodeById.has(e.src) && nodeById.has(e.dst))
      .map(e => ({
        source: e.src, target: e.dst,
        kind: e.kind, weight: e.weight,
      }));
    curNodes = simNodes;

    const { width, height } = svgEl.getBoundingClientRect();

    // Semantic edges pull a touch looser than structural backlinks, and
    // stronger cosine pulls a little tighter — association vs. citation.
    const linkDist = (e) =>
      e.kind === 'semantic' ? 90 - 30 * (e.weight || 0.6) : 60;

    simulation = d3.forceSimulation(simNodes)
      .alphaDecay(0.028) // gentler than before → a visible settle, not a pop
      .velocityDecay(0.4)
      .force('link', d3.forceLink(simEdges).id(d => d.path).distance(linkDist)
        .strength(e => e.kind === 'semantic' ? 0.25 * (e.weight || 0.6) : 0.7))
      .force('charge', d3.forceManyBody().strength(-200))
      .force('center', d3.forceCenter(width / 2, height / 2))
      .force('collide', d3.forceCollide().radius(d => nodeRadius(d) + 2));

    // Edges: keyed by unordered-ish src->dst + kind so backlink and any
    // (deduped) semantic edge for the same pair never collide on the key.
    edgeSel = edgeLayer.selectAll('line').data(simEdges, d =>
      `${d.kind}:${d.source.path || d.source}->${d.target.path || d.target}`,
    );
    edgeSel.exit().remove();
    edgeSel = edgeSel.enter().append('line')
      .merge(edgeSel)
      .attr('class', d => `edge ${d.kind}`)
      .attr('stroke-opacity', d =>
        d.kind === 'semantic'
          ? 0.12 + 0.45 * Math.max(0, (d.weight || 0.6) - 0.6) / 0.4
          : null); // backlink opacity lives in CSS

    nodeSel = nodeLayer.selectAll('circle').data(simNodes, d => d.path);
    nodeSel.exit().remove();
    const nodeEnter = nodeSel.enter().append('circle')
      .attr('class', 'node')
      .on('mouseenter', onHover)
      .on('mousemove', moveTooltip)
      .on('mouseleave', onLeave)
      .on('click', onNodeClick)
      .call(dragBehavior());
    nodeSel = nodeEnter.merge(nodeSel)
      .attr('r', nodeRadius)
      .attr('fill', nodeColor)
      // Warmth → fill opacity: dormant notes recede, active notes glow.
      .attr('fill-opacity', d => 0.55 + 0.45 * (d.activation || 0))
      .attr('data-folder', d => d.folder || '')
      .classed('pinned', d => d.fx != null);

    // Labels parallel the node layer. Always-on for hubs/hot notes; the rest
    // get a class so hover/zoom can reveal them via CSS.
    labelSel = labelLayer.selectAll('text').data(simNodes, d => d.path);
    labelSel.exit().remove();
    labelSel = labelSel.enter().append('text')
      .attr('class', 'node-label')
      .attr('text-anchor', 'middle')
      .attr('dy', '0.32em')
      .merge(labelSel)
      .text(d => d.title || d.path)
      .classed('always', isAlwaysLabeled)
      .attr('data-folder', d => d.folder || '');

    simulation.on('tick', () => {
      edgeSel
        .attr('x1', d => d.source.x).attr('y1', d => d.source.y)
        .attr('x2', d => d.target.x).attr('y2', d => d.target.y);
      nodeSel.attr('cx', d => d.x).attr('cy', d => d.y);
      labelSel
        .attr('x', d => d.x)
        .attr('y', d => d.y - nodeRadius(d) - 4);
    });
    simulation.on('end', () => { drawMinimap(); });

    updateLabelVisibility();
    // Settle smoothly, then fit once the layout has cooled. We don't hard-stop
    // at a tick count anymore — alphaDecay carries it to rest on its own.
    simulation.alpha(0.9).restart();
    window.setTimeout(() => fitToView(600), 1200);
  }

  function dragBehavior() {
    return d3.drag()
      .on('start', (event, d) => {
        if (!event.active) simulation.alphaTarget(0.3).restart();
        d.fx = d.x; d.fy = d.y;
      })
      .on('drag', (event, d) => { d.fx = event.x; d.fy = event.y; })
      .on('end', (event, d) => {
        if (!event.active) simulation.alphaTarget(0);
        // Leave fx/fy set so the node stays where dropped (pinned).
        // Shift+click on a pinned node unpins it.
        d3.select(event.sourceEvent.target).classed('pinned', true);
      });
  }

  // --- hover: light up the neighborhood --------------------------------------
  // Deepens focus mode: hovering dims everything except the node, its direct
  // neighbors, and the edges between them. Leaving restores (unless a click
  // focus is active, which we leave intact).
  function highlightHood(d) {
    const hood = neighbors.get(d.path) || new Set();
    svgEl.classList.add('focused');
    nodeSel
      .classed('hl', n => n.path === d.path || hood.has(n.path))
      .classed('primary', n => n.path === d.path);
    edgeSel.classed('hl', e =>
      e.source.path === d.path || e.target.path === d.path);
    labelSel.classed('hl', n => n.path === d.path || hood.has(n.path));
  }

  function onHover(event, d) {
    if (!stickyFocus) highlightHood(d);
    tooltipEl.hidden = false;
    tooltipEl.innerHTML =
      `<div class="tt-title"></div>` +
      `<div class="tt-path"></div>` +
      `<div class="tt-meta"></div>`;
    tooltipEl.querySelector('.tt-title').textContent = d.title || d.path;
    tooltipEl.querySelector('.tt-path').textContent  = d.path;
    const tags = (d.tags || []).join(', ');
    const folder = (d.folder || '') ? `${d.folder}/ · ` : '';
    const warmth = Math.round((d.activation || 0) * 100);
    tooltipEl.querySelector('.tt-meta').textContent =
      `${folder}${d.backlink_count || 0} backlinks · ${warmth}% warm` +
      (tags ? ` · ${tags}` : '');
    moveTooltip(event);
  }
  function moveTooltip(event) {
    const pad = 12;
    tooltipEl.style.left = (event.clientX + pad) + 'px';
    tooltipEl.style.top  = (event.clientY + pad) + 'px';
  }
  function onLeave() {
    tooltipEl.hidden = true;
    if (!stickyFocus) clearFocus();
  }

  // Click focus persists until cleared (background click / Esc); hover focus
  // is transient. stickyFocus distinguishes them so hover-out doesn't nuke a
  // deliberate click focus.
  let stickyFocus = false;

  function onNodeClick(event, d) {
    event.stopPropagation();
    if (event.shiftKey) {
      // Unpin: clears fx/fy and lets the sim reclaim the node.
      d.fx = null; d.fy = null;
      d3.select(event.currentTarget).classed('pinned', false);
      if (simulation) simulation.alphaTarget(0.1).restart();
      window.setTimeout(() => simulation && simulation.alphaTarget(0), 400);
      return;
    }
    // Second click on the already-focused primary node navigates — matches
    // the "click to open" expectation. The macOS host intercepts this exact
    // /note/{path} navigation, so it must stay byte-for-byte as today.
    if (stickyFocus &&
        nodeLayer.select('.node.primary').datum() === d) {
      window.location.href = `/note/${encodeURIComponent(d.path)}`;
      return;
    }
    stickyFocus = true;
    highlightHood(d);
  }

  function clearFocus() {
    stickyFocus = false;
    svgEl.classList.remove('focused');
    nodeSel.classed('hl', false).classed('primary', false);
    edgeSel.classed('hl', false);
    labelSel.classed('hl', false);
  }

  // Background click clears focus. Use the underlying <svg>, not the inner
  // <g>, so panning the empty area still resets.
  svg.on('click', (event) => {
    if (event.target === svgEl) clearFocus();
  });

  // --- labels: reveal more as you zoom in ------------------------------------
  function updateLabelVisibility() {
    const zoomedIn = currentTransform.k >= LABEL_ZOOM;
    labelLayer.classed('zoomed-in', zoomedIn);
    // Counter-scale labels slightly so they don't balloon when zoomed.
    labelLayer.attr('font-size', `${(12 / Math.sqrt(currentTransform.k)).toFixed(2)}px`);
  }

  // --- zoom-to-fit + reset ---------------------------------------------------
  function fitToView(duration = 600) {
    if (!curNodes.length) return;
    const xs = curNodes.map(n => n.x), ys = curNodes.map(n => n.y);
    const minX = Math.min(...xs), maxX = Math.max(...xs);
    const minY = Math.min(...ys), maxY = Math.max(...ys);
    const { width, height } = svgEl.getBoundingClientRect();
    const w = Math.max(maxX - minX, 1), h = Math.max(maxY - minY, 1);
    const pad = 60;
    const k = Math.min(8, Math.max(0.1,
      0.92 * Math.min(width / (w + pad), height / (h + pad))));
    const tx = width / 2 - k * (minX + maxX) / 2;
    const ty = height / 2 - k * (minY + maxY) / 2;
    svg.transition().duration(duration)
      .call(zoom.transform, d3.zoomIdentity.translate(tx, ty).scale(k));
  }

  // --- minimap ---------------------------------------------------------------
  // A cheap downsampled overview painted to <canvas>, with a rectangle marking
  // the current viewport. Repaints on tick-end and on zoom/pan.
  function drawMinimap() {
    const W = minimapEl.width, H = minimapEl.height;
    mmCtx.clearRect(0, 0, W, H);
    if (!curNodes.length) return;

    const xs = curNodes.map(n => n.x), ys = curNodes.map(n => n.y);
    const minX = Math.min(...xs), maxX = Math.max(...xs);
    const minY = Math.min(...ys), maxY = Math.max(...ys);
    const w = Math.max(maxX - minX, 1), h = Math.max(maxY - minY, 1);
    const pad = 8;
    const k = Math.min((W - 2 * pad) / w, (H - 2 * pad) / h);
    const ox = pad - minX * k + (W - 2 * pad - w * k) / 2;
    const oy = pad - minY * k + (H - 2 * pad - h * k) / 2;
    const mx = (x) => ox + x * k;
    const my = (y) => oy + y * k;

    for (const n of curNodes) {
      mmCtx.beginPath();
      mmCtx.arc(mx(n.x), my(n.y), 1.4, 0, 2 * Math.PI);
      mmCtx.fillStyle = nodeColor(n);
      mmCtx.globalAlpha = 0.5 + 0.5 * (n.activation || 0);
      mmCtx.fill();
    }
    mmCtx.globalAlpha = 1;

    // Viewport rectangle: invert the screen corners through the transform,
    // then map world-space back into minimap-space.
    const { width, height } = svgEl.getBoundingClientRect();
    const t = currentTransform;
    const wx0 = (0 - t.x) / t.k, wy0 = (0 - t.y) / t.k;
    const wx1 = (width - t.x) / t.k, wy1 = (height - t.y) / t.k;
    mmCtx.strokeStyle = getComputedStyle(document.body)
      .getPropertyValue('--accent').trim() || '#60a5fa';
    mmCtx.lineWidth = 1;
    mmCtx.strokeRect(mx(wx0), my(wy0), (wx1 - wx0) * k, (wy1 - wy0) * k);
  }

  // --- legend ----------------------------------------------------------------
  function buildLegend(nodes) {
    legendItems.innerHTML = '';
    const counts = new Map();
    for (const n of nodes) {
      const f = n.folder || '';
      counts.set(f, (counts.get(f) || 0) + 1);
    }
    // Order: named folders alpha-sorted, then root ("") last as "(root)".
    const folders = [...counts.keys()]
      .filter(f => f !== '').sort();
    if (counts.has('')) folders.push('');
    if (folders.length <= 1) { legendEl.hidden = true; return; }
    legendEl.hidden = false;

    for (const f of folders) {
      const row = document.createElement('button');
      row.type = 'button';
      row.className = 'legend-item';
      row.dataset.folder = f;
      const sw = document.createElement('span');
      sw.className = 'legend-swatch';
      sw.style.background = folderColors.get(f) || ROOT_COLOR;
      const label = document.createElement('span');
      label.className = 'legend-name';
      label.textContent = f === '' ? '(root)' : f;
      const cnt = document.createElement('span');
      cnt.className = 'legend-count';
      cnt.textContent = counts.get(f);
      row.append(sw, label, cnt);
      row.addEventListener('click', () => toggleFolder(f));
      legendItems.appendChild(row);
    }
    legendClear.hidden = isolatedFolder === null;
  }

  function toggleFolder(f) {
    isolatedFolder = isolatedFolder === f ? null : f;
    legendClear.hidden = isolatedFolder === null;
    legendItems.querySelectorAll('.legend-item').forEach(el => {
      el.classList.toggle('active', isolatedFolder !== null);
      el.classList.toggle('selected', el.dataset.folder === isolatedFolder);
    });
    clearFocus();
    applyFilters();
  }

  // --- control wiring --------------------------------------------------------
  minInput.addEventListener('change', applyFilters);
  minInput.addEventListener('input',  applyFilters);
  tagSelect.addEventListener('change', applyFilters);
  semToggle.addEventListener('change', applyFilters);
  legendClear.addEventListener('click', () => { if (isolatedFolder !== null) toggleFolder(isolatedFolder); });
  fitBtn.addEventListener('click', () => fitToView(500));
  resetBtn.addEventListener('click', () => {
    svg.transition().duration(300).call(zoom.transform, d3.zoomIdentity);
    clearFocus();
  });

  // Esc clears focus mode — the only keyboard affordance worth adding here.
  window.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') clearFocus();
  });

  async function load() {
    try {
      const res = await fetch('/api/graph');
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const data = await res.json();
      allNodes = Array.isArray(data.nodes) ? data.nodes : [];
      allEdges = Array.isArray(data.edges) ? data.edges : [];
      buildNeighbors(allEdges);
      buildFolderColors(allNodes);

      // Populate tag dropdown only if any node carries tags.
      const tagSet = new Set();
      for (const n of allNodes) for (const t of (n.tags || [])) tagSet.add(t);
      if (tagSet.size > 0) {
        for (const t of [...tagSet].sort()) {
          const opt = document.createElement('option');
          opt.value = t; opt.textContent = t;
          tagSelect.appendChild(opt);
        }
        tagCtrl.hidden = false;
      }

      buildLegend(allNodes);

      pathEl.textContent = data.vault_path || '';
      if (allNodes.length === 0) {
        setStatus('empty', false);
        showEmpty('no notes in vault');
        return;
      }
      setStatus(`${allNodes.length} notes`, false);
      clearOverlay();
      applyFilters();
    } catch (err) {
      setStatus('offline', true);
      showError(`could not load graph: ${err.message}`);
    }
  }

  load();
})();
