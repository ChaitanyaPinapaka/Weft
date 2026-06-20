// Weft viewer — read-only sibling of editor.js.
// Why a separate page: opening a note for reading shouldn't bring in
// contenteditable, autosave, or beforeunload flushing. Cheap, dedicated surface.

(() => {
  const params = new URLSearchParams(location.search);
  const path   = params.get('path') || '';

  const titleEl    = document.getElementById('title');
  const proseEl    = document.getElementById('prose');
  const pathEl     = document.getElementById('path');
  const statusEl   = document.getElementById('status');
  const trailEl           = document.getElementById('brain-trail');
  const backlinksEl       = document.getElementById('brain-backlinks');
  const surfacedEl        = document.getElementById('brain-surfaced');
  const surfacedSectionEl = document.getElementById('brain-surfaced-section');
  const ontdEl            = document.getElementById('brain-onthisday');
  const ontdSectionEl     = document.getElementById('brain-onthisday-section');
  const brainEl           = document.getElementById('brain');

  pathEl.textContent = path || '(no path — append ?path=note.html)';

  // "Explain" toggle — opt-in reveal of the activation math (act/base/spread)
  // per surfaced item. Default OFF so the panel stays calm. We toggle a class on
  // the #brain root and keep the breakdown line always in the DOM but CSS-hidden
  // when the class is absent; that avoids a /api/surface re-fetch on every toggle.
  const EXPLAIN_KEY = 'weft.brain.explain';
  let explainOn = false;
  try { explainOn = localStorage.getItem(EXPLAIN_KEY) === '1'; } catch (e) {}
  const explainToggleEl = document.createElement('button');
  explainToggleEl.type = 'button';
  explainToggleEl.className = 'brain-explain-toggle';
  explainToggleEl.textContent = 'scores';
  explainToggleEl.setAttribute('aria-label', 'Toggle activation scores');
  function syncExplainToggle() {
    brainEl.classList.toggle('brain-explain', explainOn);
    explainToggleEl.classList.toggle('is-on', explainOn);
    explainToggleEl.setAttribute('aria-pressed', explainOn ? 'true' : 'false');
  }
  explainToggleEl.addEventListener('click', () => {
    explainOn = !explainOn;
    try { localStorage.setItem(EXPLAIN_KEY, explainOn ? '1' : '0'); } catch (e) {}
    syncExplainToggle();
  });
  // Mount next to the "Surfaced" heading so it reads as scoped to that section.
  {
    const surfacedHeading = surfacedSectionEl && surfacedSectionEl.querySelector('.brain-heading');
    if (surfacedHeading) surfacedHeading.appendChild(explainToggleEl);
  }
  syncExplainToggle();

  // Edit-link is hidden until we know we have a path to hand off to the editor.
  const editLinkEl = document.getElementById('edit-link');
  if (path && editLinkEl) {
    editLinkEl.href = '/edit/' + path;
    editLinkEl.hidden = false;
  }

  // Remove is a soft delete: the daemon moves the file to .trash/ in the vault
  // (never hard-deleted) and drops it from the index, then we bounce back to
  // the vault list since this note no longer has a live page.
  const removeLinkEl = document.getElementById('remove-link');
  if (path && removeLinkEl) {
    removeLinkEl.hidden = false;
    removeLinkEl.addEventListener('click', async (e) => {
      e.preventDefault();
      const ok = confirm('Remove "' + path + '"?\n\nThe file moves to .trash/ in your vault — nothing is deleted — but it leaves listings, search, and surfacing.');
      if (!ok) return;
      try {
        const res = await fetch('/api/note/' + path, { method: 'DELETE' });
        if (!res.ok) throw new Error('http ' + res.status);
        location.href = '/notes';
      } catch (err) {
        setStatus('offline', 'remove failed');
      }
    });
  }

  function setStatus(state, text) {
    statusEl.className = 'status ' + state;
    statusEl.textContent = text;
  }

  function titleFromPath(p) {
    const base = p.split('/').pop() || p;
    return base.replace(/\.html?$/i, '');
  }

  // Rewrite in-vault .html links to bounce through the viewer so navigation
  // stays inside this surface. External links and anchors pass through untouched.
  function rewriteLinks(root) {
    const anchors = root.querySelectorAll('a[href]');
    for (const a of anchors) {
      const href = a.getAttribute('href');
      if (!href) continue;
      if (/^[a-z]+:/i.test(href)) continue;             // absolute (http:, mailto:, …)
      if (href.startsWith('#')) continue;               // in-page anchor
      if (href.startsWith('/')) continue;               // site-absolute, leave alone
      if (!/\.html?($|[?#])/i.test(href)) continue;     // not an .html target
      // Strip any leading "./" and split off query/hash so we can re-attach.
      let clean = href.replace(/^\.\//, '');
      let tail = '';
      const m = clean.match(/^([^?#]+)(.*)$/);
      if (m) { clean = m[1]; tail = m[2]; }
      a.setAttribute('href', '/note/' + clean + tail);
    }
  }

  // Strip active content from an inert (DOMParser) document before we adopt it
  // into the live page. The viewer renders note bodies via innerHTML on the
  // trusted localhost:7777 origin, so anything that survived ingestion (synced
  // notes, imports authored elsewhere) must be neutralized here. Operating on
  // the parsed-but-inert DOM is reliable — no scripts have run and no resources
  // have loaded yet. Mirrors the server-side clip sanitizer.
  const DANGEROUS_TAGS = ['script', 'iframe', 'object', 'embed', 'applet',
    'form', 'frame', 'frameset', 'meta', 'link', 'base'];
  const URL_ATTRS = ['href', 'src', 'srcset', 'action', 'formaction',
    'xlink:href', 'data', 'poster', 'background'];
  function isDangerousScheme(val) {
    // Browsers ignore leading/embedded ASCII whitespace + control chars when
    // resolving a scheme; entities are already decoded in parsed attributes.
    const v = (val || '').replace(/[\x00-\x20\x7f]/g, '').toLowerCase();
    return v.startsWith('javascript:') || v.startsWith('vbscript:') ||
      (v.startsWith('data:') && !v.startsWith('data:image/'));
  }
  function sanitize(root) {
    for (const el of root.querySelectorAll(DANGEROUS_TAGS.join(','))) {
      el.remove();
    }
    for (const el of root.querySelectorAll('*')) {
      for (const attr of [...el.attributes]) {
        const name = attr.name.toLowerCase();
        if (name.startsWith('on')) { el.removeAttribute(attr.name); continue; }
        if (URL_ATTRS.includes(name) && isDangerousScheme(attr.value)) {
          el.removeAttribute(attr.name);
        }
      }
    }
  }

  function hydrate(html) {
    const doc = new DOMParser().parseFromString(html, 'text/html');
    const article = doc.querySelector('article');
    const root = article || doc.body;
    sanitize(root);

    const h1 = root.querySelector('h1');
    if (h1) {
      titleEl.textContent = h1.textContent;
      document.title = 'Weft — ' + h1.textContent;
      h1.remove();
    } else {
      const t = doc.title || titleFromPath(path);
      titleEl.textContent = t;
      document.title = 'Weft — ' + t;
    }

    // Inject the body fragment, then rewrite links in-place on the live DOM.
    proseEl.innerHTML = root.innerHTML.trim();
    rewriteLinks(proseEl);
    attachTaskListeners(proseEl);
  }

  // Task checkboxes: the read-only viewer is where you actually tick things off.
  // TipTap saves each task as <li data-type="taskItem" data-checked> wrapping a
  // <label><input type=checkbox>; we make that input live. Toggling persists by
  // POSITION (item_idx) — the n-th taskItem — never by text, so duplicate task
  // text can't cross-toggle. State round-trips through the normal save path
  // (re-fetch /raw for a fresh ETag, flip the n-th item, POST If-Match), which
  // re-indexes the open-tasks view for free.
  function attachTaskListeners(root) {
    const items = root.querySelectorAll('li[data-type="taskItem"]');
    items.forEach((li, idx) => {
      let box = li.querySelector('input[type="checkbox"]');
      if (!box) {
        // Note authored without the input wrapper — inject a minimal one.
        box = document.createElement('input');
        box.type = 'checkbox';
        const label = document.createElement('label');
        label.appendChild(box);
        li.insertBefore(label, li.firstChild);
      }
      box.checked = li.getAttribute('data-checked') === 'true';
      box.disabled = false;
      box.style.cursor = 'pointer';
      box.addEventListener('change', () => {
        const want = box.checked;
        li.setAttribute('data-checked', want ? 'true' : 'false');
        persistTaskToggle(idx, want, box);
      });
    });
  }

  async function persistTaskToggle(idx, checked, box) {
    if (!path) return;
    setStatus('', 'saving task…');
    try {
      const res = await fetch('/raw/' + path);
      if (!res.ok) throw new Error('raw');
      const tag = res.headers.get('ETag');
      const doc = new DOMParser().parseFromString(await res.text(), 'text/html');
      const items = doc.querySelectorAll('li[data-type="taskItem"]');
      if (idx >= items.length) { // structure drifted under us — resync
        setStatus('offline', 'task moved — reloading');
        location.reload();
        return;
      }
      items[idx].setAttribute('data-checked', checked ? 'true' : 'false');
      // Keep TipTap's embedded <input checked> in sync so the editor agrees.
      const inp = items[idx].querySelector('input[type="checkbox"]');
      if (inp) {
        if (checked) inp.setAttribute('checked', 'checked');
        else inp.removeAttribute('checked');
      }
      const body = '<!DOCTYPE html>' + doc.documentElement.outerHTML;
      const headers = { 'Content-Type': 'text/html' };
      if (tag) headers['If-Match'] = tag;
      const save = await fetch('/api/note/' + path, { method: 'POST', headers, body });
      if (save.status === 412 || save.status === 409) {
        setStatus('offline', 'changed elsewhere — reloading');
        location.reload();
        return;
      }
      if (!save.ok) throw new Error('save');
      setStatus('', 'read-only');
    } catch (e) {
      box.checked = !checked; // revert the optimistic toggle
      setStatus('offline', 'task save failed');
    }
  }

  async function load() {
    if (!path) {
      setStatus('offline', 'no path');
      return;
    }
    try {
      const res = await fetch('/raw/' + path);
      if (!res.ok) {
        setStatus('offline', 'not found');
        return;
      }
      const html = await res.text();
      hydrate(html);
      setStatus('', 'read-only');
    } catch (e) {
      setStatus('offline', 'offline');
    }
  }

  // ----- Brain panel ---------------------------------------------------------
  function chipLabel(reason) {
    if (reason === 'backlink')    return 'backlink';
    if (reason === 'semantic')    return 'semantic';
    if (reason === 'recent')      return 'recent';
    if (reason === 'co-accessed') return 'co-accessed';
    if (reason === 'base')        return 'base';
    if (reason === 'resurfaced')  return 'resurfaced';
    if (reason.startsWith('on-this-day:')) {
      return reason.slice('on-this-day:'.length) + ' ago';
    }
    if (reason.startsWith('similar:')) {
      // Legacy format from earlier surface code.
      return 'similar ' + reason.slice('similar:'.length);
    }
    return reason;
  }

  // Thought trail: a faint breadcrumb of the path taken this session (A › B › C).
  // trail[0] is the current focus note. Only shown when the trail has more than
  // one entry (a real path). Crumbs link to /note/{path}.
  function renderTrail(trail) {
    trailEl.innerHTML = '';
    if (!Array.isArray(trail) || trail.length <= 1) {
      trailEl.hidden = true;
      return;
    }
    trail.forEach((p, i) => {
      if (i > 0) {
        const sep = document.createElement('span');
        sep.className = 'brain-trail-sep';
        sep.textContent = '›';
        trailEl.appendChild(sep);
      }
      const a = document.createElement('a');
      a.className = 'brain-trail-crumb';
      a.href = '/note/' + p;
      a.textContent = titleFromPath(p);
      a.title = p;
      trailEl.appendChild(a);
    });
    trailEl.hidden = false;
  }

  function renderBacklinks(list) {
    backlinksEl.innerHTML = '';
    if (!list || list.length === 0) {
      backlinksEl.className = 'brain-body';
      backlinksEl.textContent = 'no backlinks yet';
      return;
    }
    const ul = document.createElement('ul');
    ul.className = 'brain-backlinks-list';
    for (const p of list) {
      const li = document.createElement('li');
      const a  = document.createElement('a');
      a.href = '/note/' + p;
      a.textContent = titleFromPath(p);
      a.title = p;
      li.appendChild(a);
      ul.appendChild(li);
    }
    backlinksEl.className = '';
    backlinksEl.appendChild(ul);
  }

  // Map a 0..1 fraction (0 = hottest/top, 1 = coldest/bottom) to an opacity in
  // [MIN_TEMP_OPACITY, 1]. Restraint over decoration: temperature is conveyed
  // by opacity + order only — no glow, no animation. Hot rises, dormant recedes
  // to grey but never vanishes (we floor at MIN_TEMP_OPACITY).
  const MIN_TEMP_OPACITY = 0.55;
  function tempOpacity(frac) {
    return (1 - frac * (1 - MIN_TEMP_OPACITY)).toFixed(3);
  }

  // Activation breakdown line, e.g. "act 2.41 · base −1.39 · spread +3.84".
  // Uses a true minus sign for negatives and a leading "+" on spread when ≥0 so
  // the numbers read as signed deltas. Rendered always-in-DOM; CSS hides it when
  // the panel isn't in explain mode.
  function signed(n) {
    const v = (n || 0).toFixed(2);
    return v.startsWith('-') ? '−' + v.slice(1) : '+' + v;
  }
  function scoresLine(it) {
    const act = (it.Activation || 0).toFixed(2);
    return 'act ' + act + ' · base ' + signed(it.Base) + ' · spread ' + signed(it.Spread);
  }

  // Dissolving card: only render notes the current context actually activates,
  // i.e. Spread > 0 (a backlink / semantic / co-access edge fired). Base-level-
  // only notes are "recently opened" filing, not recall, so they stay in the
  // quiet margin. If nothing crosses the threshold, the whole section is hidden.
  function renderSurfaced(items) {
    surfacedEl.innerHTML = '';
    const activated = (items || []).filter(it => (it.Spread || 0) > 0);
    if (activated.length === 0) {
      surfacedSectionEl.hidden = true;
      return;
    }
    surfacedSectionEl.hidden = false;
    // Already sorted desc by Activation server-side; keep that order so the
    // hottest memory sits on top. Cap mirrors the previous slice(0,12).
    const shown = activated.slice(0, 12);
    const denom = Math.max(shown.length - 1, 1); // avoid /0 when a single item
    shown.forEach((it, i) => {
      const li = document.createElement('li');
      li.className = 'brain-item';

      const a = document.createElement('a');
      a.href = '/note/' + it.Path;
      // Temperature by position: top item full strength, lower items dimmed
      // toward --muted. Order encodes rank; opacity reinforces it quietly.
      a.style.opacity = tempOpacity(i / denom);

      // "resurfaced" — forgotten-yet-relevant. Subtle accent-tinted left border
      // + a small ✦ so the moment reads as special without shouting.
      const resurfaced = Array.isArray(it.Reasons) && it.Reasons.includes('resurfaced');
      if (resurfaced) li.classList.add('brain-resurfaced');

      const title = document.createElement('span');
      title.className = 'brain-title';
      title.textContent = (resurfaced ? '✦ ' : '') + (it.Title || titleFromPath(it.Path));
      a.appendChild(title);

      if (Array.isArray(it.Reasons) && it.Reasons.length) {
        const chips = document.createElement('div');
        chips.className = 'brain-chips';
        for (const r of it.Reasons) {
          const c = document.createElement('span');
          c.className = 'brain-chip';
          c.textContent = chipLabel(r);
          chips.appendChild(c);
        }
        a.appendChild(chips);
      }

      // Breakdown line lives inside the <a> as a non-interactive span so it
      // never swallows the navigation click. Visibility is gated by the
      // .brain-explain class on the panel root (no re-render on toggle).
      const scores = document.createElement('span');
      scores.className = 'brain-scores';
      scores.textContent = scoresLine(it);
      a.appendChild(scores);

      const pathSpan = document.createElement('span');
      pathSpan.className = 'brain-path';
      pathSpan.textContent = it.Path;
      a.appendChild(pathSpan);

      li.appendChild(a);
      surfacedEl.appendChild(li);
    });
  }

  function renderOnThisDay(items) {
    ontdEl.innerHTML = '';
    if (!items || items.length === 0) {
      ontdSectionEl.hidden = true;
      return;
    }
    ontdSectionEl.hidden = false;
    for (const it of items) {
      const li = document.createElement('li');
      li.className = 'brain-item';

      const a = document.createElement('a');
      a.href = '/note/' + it.Path;

      const title = document.createElement('span');
      title.className = 'brain-title';
      title.textContent = (it.Title || titleFromPath(it.Path)) +
        ' · ' + (it.Years || 1) + (it.Years === 1 ? ' year ago' : ' years ago');
      a.appendChild(title);

      const pathSpan = document.createElement('span');
      pathSpan.className = 'brain-path';
      pathSpan.textContent = it.Path;
      a.appendChild(pathSpan);

      li.appendChild(a);
      ontdEl.appendChild(li);
    }
  }

  async function loadSurface() {
    if (!path) {
      if (trailEl) trailEl.hidden = true;
      backlinksEl.textContent = '—';
      surfacedSectionEl.hidden = true;
      ontdSectionEl.hidden = true;
      return;
    }
    try {
      const res = await fetch('/api/surface/' + path);
      if (!res.ok) throw new Error('http ' + res.status);
      const data = await res.json();
      renderTrail(data.trail);
      renderBacklinks(data.backlinks);
      renderSurfaced(data.scored);
      renderOnThisDay(data.on_this_day);
    } catch (e) {
      // Silent on failure: panel stays a quiet dash, matches editor behaviour.
      if (trailEl) trailEl.hidden = true;
      backlinksEl.className = 'brain-body';
      backlinksEl.textContent = '—';
      surfacedSectionEl.hidden = true;
      ontdSectionEl.hidden = true;
    }
  }

  load().then(loadSurface);

  // Live reload when the open note changes on disk (capture, rename, sync pull).
  // The viewer is read-only, so reloading is always safe — no unsaved state.
  (function subscribeChanges() {
    if (!path || typeof EventSource === 'undefined') return;
    let es;
    try { es = new EventSource('/api/surface/stream'); } catch (e) { return; }
    es.addEventListener('changed', (e) => {
      let d;
      try { d = JSON.parse(e.data); } catch (_) { return; }
      if (d && d.path === path) location.reload();
    });
  })();
})();
