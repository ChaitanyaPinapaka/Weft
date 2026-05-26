// Force-directed graph over the vault. ESM import keeps this build-free,
// matching the TipTap editor's no-bundler philosophy. We freeze the layout
// after ~150 ticks because for a static vault snapshot there's no reason to
// keep the CPU warm — the user pans/zooms, not simulates.

import * as d3 from 'https://esm.sh/d3@7';

(() => {
  const svgEl     = document.getElementById('graph');
  const tooltipEl = document.getElementById('tooltip');
  const statusEl  = document.getElementById('status');
  const pathEl    = document.getElementById('path');
  const emptyEl   = document.getElementById('empty');
  const metaEl    = document.getElementById('meta');
  const minInput  = document.getElementById('min-backlinks');
  const tagCtrl   = document.getElementById('tag-control');
  const tagSelect = document.getElementById('tag-filter');
  const resetBtn  = document.getElementById('reset-zoom');

  const svg = d3.select(svgEl);
  // Two layered groups so zoom transforms a single <g> while keeping
  // edges painted below nodes regardless of DOM order.
  const root      = svg.append('g').attr('class', 'zoom-root');
  const edgeLayer = root.append('g').attr('class', 'edges');
  const nodeLayer = root.append('g').attr('class', 'nodes');

  const NODE_MIN = 4;
  const NODE_MAX = 24;
  const nodeRadius = (n) => {
    const r = NODE_MIN + 1.5 * Math.sqrt(n.backlink_count || 0);
    return Math.min(r, NODE_MAX);
  };

  // Zoom on the outer <svg>; we apply the transform to the inner <g>.
  // Clicking the bare background clears focus mode (handled below).
  const zoom = d3.zoom()
    .scaleExtent([0.1, 8])
    .on('zoom', (event) => root.attr('transform', event.transform));
  svg.call(zoom);

  let allNodes = [];
  let allEdges = [];
  // Adjacency for focus mode — neighbors keyed by node.path.
  const neighbors = new Map();
  let simulation = null;
  let nodeSel = d3.select(null);
  let edgeSel = d3.select(null);

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

  function buildNeighbors(edges) {
    neighbors.clear();
    for (const e of edges) {
      const s = typeof e.src === 'string' ? e.src : e.src.path;
      const d = typeof e.dst === 'string' ? e.dst : e.dst.path;
      if (!neighbors.has(s)) neighbors.set(s, new Set());
      if (!neighbors.has(d)) neighbors.set(d, new Set());
      neighbors.get(s).add(d);
      neighbors.get(d).add(s);
    }
  }

  function applyFilters() {
    const minBL = Math.max(0, parseInt(minInput.value, 10) || 0);
    const tag   = tagSelect.value;

    // Step 1: nodes passing direct filters (backlink count, tag).
    const direct = new Set();
    for (const n of allNodes) {
      if ((n.backlink_count || 0) < minBL) continue;
      if (tag && !(n.tags || []).includes(tag)) continue;
      direct.add(n.path);
    }
    // Step 2: rescue nodes that fail the threshold but are connected to a
    // visible node — keeps small leaves attached to hubs rather than orphaning
    // them. Single hop is enough; multi-hop would re-introduce the hairball.
    const visible = new Set(direct);
    for (const e of allEdges) {
      const s = typeof e.src === 'string' ? e.src : e.src.path;
      const d = typeof e.dst === 'string' ? e.dst : e.dst.path;
      if (direct.has(s)) visible.add(d);
      if (direct.has(d)) visible.add(s);
    }

    const nodes = allNodes.filter(n => visible.has(n.path));
    const edges = allEdges
      .map(e => ({
        src: typeof e.src === 'string' ? e.src : e.src.path,
        dst: typeof e.dst === 'string' ? e.dst : e.dst.path,
      }))
      .filter(e => visible.has(e.src) && visible.has(e.dst));

    metaEl.textContent = `${nodes.length} / ${allNodes.length} nodes · ${edges.length} edges`;
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
      .map(e => ({ source: e.src, target: e.dst }));

    const { width, height } = svgEl.getBoundingClientRect();

    simulation = d3.forceSimulation(simNodes)
      .alphaDecay(0.05)
      .force('link', d3.forceLink(simEdges).id(d => d.path).distance(60))
      .force('charge', d3.forceManyBody().strength(-180))
      .force('center', d3.forceCenter(width / 2, height / 2))
      .force('collide', d3.forceCollide().radius(d => nodeRadius(d) + 2));

    edgeSel = edgeLayer.selectAll('line').data(simEdges, d =>
      `${d.source.path || d.source}->${d.target.path || d.target}`,
    );
    edgeSel.exit().remove();
    edgeSel = edgeSel.enter().append('line').attr('class', 'edge').merge(edgeSel);

    nodeSel = nodeLayer.selectAll('circle').data(simNodes, d => d.path);
    nodeSel.exit().remove();
    const nodeEnter = nodeSel.enter().append('circle')
      .attr('class', 'node')
      .attr('r', nodeRadius)
      .on('mouseenter', onHover)
      .on('mousemove', moveTooltip)
      .on('mouseleave', hideTooltip)
      .on('click', onNodeClick)
      .call(dragBehavior());
    nodeSel = nodeEnter.merge(nodeSel);
    nodeSel.classed('pinned', d => d.fx != null);

    simulation.on('tick', () => {
      edgeSel
        .attr('x1', d => d.source.x).attr('y1', d => d.source.y)
        .attr('x2', d => d.target.x).attr('y2', d => d.target.y);
      nodeSel.attr('cx', d => d.x).attr('cy', d => d.y);
    });

    // Freeze after ~150 ticks: the spec budget is 3s for 2000 nodes and the
    // layout is essentially settled by then with alphaDecay(0.05).
    let ticks = 0;
    simulation.on('tick.freeze', () => {
      if (++ticks >= 150) {
        simulation.stop();
        simulation.on('tick.freeze', null);
      }
    });
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

  function onHover(event, d) {
    tooltipEl.hidden = false;
    tooltipEl.innerHTML =
      `<div class="tt-title"></div>` +
      `<div class="tt-path"></div>` +
      `<div class="tt-meta"></div>`;
    tooltipEl.querySelector('.tt-title').textContent = d.title || d.path;
    tooltipEl.querySelector('.tt-path').textContent  = d.path;
    const tags = (d.tags || []).join(', ');
    tooltipEl.querySelector('.tt-meta').textContent =
      `${d.backlink_count || 0} backlinks` + (tags ? ` · ${tags}` : '');
    moveTooltip(event);
  }
  function moveTooltip(event) {
    const pad = 12;
    tooltipEl.style.left = (event.clientX + pad) + 'px';
    tooltipEl.style.top  = (event.clientY + pad) + 'px';
  }
  function hideTooltip() { tooltipEl.hidden = true; }

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
    // Plain click on a node: enter focus mode (1-hop highlight). A second
    // click navigates — matches the "click to open" expectation while still
    // exposing the neighborhood on first interaction.
    if (svgEl.classList.contains('focused') &&
        nodeLayer.select('.node.hl.primary').datum() === d) {
      window.location.href = `/note/${encodeURIComponent(d.path)}`;
      return;
    }
    focusOn(d);
  }

  function focusOn(d) {
    const hood = neighbors.get(d.path) || new Set();
    svgEl.classList.add('focused');
    nodeSel
      .classed('hl', n => n.path === d.path || hood.has(n.path))
      .classed('primary', n => n.path === d.path);
    edgeSel.classed('hl', e =>
      (e.source.path === d.path) || (e.target.path === d.path));
  }
  function clearFocus() {
    svgEl.classList.remove('focused');
    nodeSel.classed('hl', false).classed('primary', false);
    edgeSel.classed('hl', false);
  }

  // Background click clears focus. Use the underlying <svg>, not the inner
  // <g>, so panning the empty area still resets.
  svg.on('click', (event) => {
    if (event.target === svgEl) clearFocus();
  });

  minInput.addEventListener('change', applyFilters);
  minInput.addEventListener('input',  applyFilters);
  tagSelect.addEventListener('change', applyFilters);
  resetBtn.addEventListener('click', () => {
    svg.transition().duration(300).call(zoom.transform, d3.zoomIdentity);
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

      // Populate tag dropdown only if any node carries tags; the spec notes
      // tags are null at build time, so we hide the control by default.
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
