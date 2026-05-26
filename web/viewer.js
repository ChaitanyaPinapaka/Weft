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
  const backlinksEl       = document.getElementById('brain-backlinks');
  const surfacedEl        = document.getElementById('brain-surfaced');
  const surfacedSectionEl = document.getElementById('brain-surfaced-section');
  const ontdEl            = document.getElementById('brain-onthisday');
  const ontdSectionEl     = document.getElementById('brain-onthisday-section');

  pathEl.textContent = path || '(no path — append ?path=note.html)';

  // Edit-link is hidden until we know we have a path to hand off to the editor.
  const editLinkEl = document.getElementById('edit-link');
  if (path && editLinkEl) {
    editLinkEl.href = '/web/index.html?path=' + encodeURIComponent(path);
    editLinkEl.hidden = false;
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
      a.setAttribute('href', '/web/viewer.html?path=' + encodeURIComponent(clean) + tail);
    }
  }

  function hydrate(html) {
    const doc = new DOMParser().parseFromString(html, 'text/html');
    const article = doc.querySelector('article');
    const root = article || doc.body;

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
  }

  async function load() {
    if (!path) {
      setStatus('offline', 'no path');
      return;
    }
    try {
      const res = await fetch('/note/' + path);
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
    if (reason.startsWith('on-this-day:')) {
      return reason.slice('on-this-day:'.length) + ' ago';
    }
    if (reason.startsWith('similar:')) {
      // Legacy format from earlier surface code.
      return 'similar ' + reason.slice('similar:'.length);
    }
    return reason;
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
      a.href = '/web/viewer.html?path=' + encodeURIComponent(p);
      a.textContent = titleFromPath(p);
      a.title = p;
      li.appendChild(a);
      ul.appendChild(li);
    }
    backlinksEl.className = '';
    backlinksEl.appendChild(ul);
  }

  function renderSurfaced(items) {
    surfacedEl.innerHTML = '';
    if (!items || items.length === 0) {
      surfacedSectionEl.hidden = true;
      return;
    }
    surfacedSectionEl.hidden = false;
    for (const it of items.slice(0, 12)) {
      const li = document.createElement('li');
      li.className = 'brain-item';

      const a = document.createElement('a');
      a.href = '/web/viewer.html?path=' + encodeURIComponent(it.Path);

      const title = document.createElement('span');
      title.className = 'brain-title';
      title.textContent = it.Title || titleFromPath(it.Path);
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

      const pathSpan = document.createElement('span');
      pathSpan.className = 'brain-path';
      pathSpan.textContent = it.Path;
      a.appendChild(pathSpan);

      li.appendChild(a);
      surfacedEl.appendChild(li);
    }
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
      a.href = '/web/viewer.html?path=' + encodeURIComponent(it.Path);

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
      backlinksEl.textContent = '—';
      surfacedSectionEl.hidden = true;
      ontdSectionEl.hidden = true;
      return;
    }
    try {
      const res = await fetch('/api/surface/' + path);
      if (!res.ok) throw new Error('http ' + res.status);
      const data = await res.json();
      renderBacklinks(data.backlinks);
      renderSurfaced(data.scored);
      renderOnThisDay(data.on_this_day);
    } catch (e) {
      // Silent on failure: panel stays a quiet dash, matches editor behaviour.
      backlinksEl.className = 'brain-body';
      backlinksEl.textContent = '—';
      surfacedSectionEl.hidden = true;
      ontdSectionEl.hidden = true;
    }
  }

  load().then(loadSurface);
})();
