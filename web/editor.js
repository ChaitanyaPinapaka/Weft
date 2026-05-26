// Weft editor — vanilla contenteditable, 300ms debounced autosave.
// Why no framework: the editor is a thin client over the daemon. A bundler
// would dwarf the actual logic. TipTap may come later (CLAUDE.md, open Qs).

(() => {
  const params = new URLSearchParams(location.search);
  const path = params.get('path') || '';

  const titleEl  = document.getElementById('title');
  const bodyEl   = document.getElementById('body');
  const pathEl   = document.getElementById('path');
  const statusEl = document.getElementById('status');
  const backlinksEl       = document.getElementById('brain-backlinks');
  const surfacedEl        = document.getElementById('brain-surfaced');
  const surfacedSectionEl = document.getElementById('brain-surfaced-section');
  const onThisDayEl        = document.getElementById('brain-onthisday');
  const onThisDaySectionEl = document.getElementById('brain-onthisday-section');
  const brainToggleEl      = document.getElementById('brain-toggle');

  // Brain panel collapse toggle. Initial state was applied in <head> to avoid
  // a flash; here we just sync the button glyph and wire the click.
  const BRAIN_KEY = 'weft.brain.collapsed';
  function syncBrainToggle() {
    const collapsed = document.documentElement.classList.contains('brain-collapsed');
    brainToggleEl.textContent = collapsed ? '‹' : '›';
    brainToggleEl.setAttribute('aria-expanded', collapsed ? 'false' : 'true');
  }
  brainToggleEl.addEventListener('click', () => {
    const collapsed = document.documentElement.classList.toggle('brain-collapsed');
    try { localStorage.setItem(BRAIN_KEY, collapsed ? '1' : '0'); } catch (e) {}
    syncBrainToggle();
  });
  syncBrainToggle();

  pathEl.textContent = path || '(no path — append ?path=note.html)';

  let saveTimer = null;
  let inflight = false;
  let pending = false;
  let dirty = false;

  function setStatus(state, text) {
    statusEl.className = 'status ' + state;
    statusEl.textContent = text;
  }

  // Strip the wrapping document/article we emit on save so we can repopulate
  // the contenteditables with just title + body fragments.
  function hydrate(html) {
    const doc = new DOMParser().parseFromString(html, 'text/html');
    const article = doc.querySelector('article');
    const root = article || doc.body;

    const h1 = root.querySelector('h1');
    if (h1) {
      titleEl.textContent = h1.textContent;
      h1.remove();
    } else {
      titleEl.textContent = doc.title || '';
    }
    bodyEl.innerHTML = root.innerHTML.trim();
  }

  function assemble() {
    const title = titleEl.textContent.trim();
    const body  = bodyEl.innerHTML.trim();
    // Title is duplicated into <title> and into the article's <h1> so the
    // rendered note is self-contained when opened directly.
    return '<!DOCTYPE html><html><head><meta charset="utf-8"><title>' +
      escapeHtml(title) + '</title></head><body><article><h1>' +
      escapeHtml(title) + '</h1>\n' + body + '</article></body></html>';
  }

  function escapeHtml(s) {
    return s.replace(/[&<>"']/g, c => ({
      '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
    }[c]));
  }

  async function load() {
    if (!path) return;
    try {
      const res = await fetch('/note/' + path);
      if (!res.ok) {
        setStatus('', 'new');
        return;
      }
      const html = await res.text();
      hydrate(html);
      setStatus('saved', 'loaded');
    } catch (e) {
      setStatus('offline', 'offline');
    }
  }

  async function save() {
    if (!path) {
      setStatus('offline', 'no path');
      return;
    }
    if (inflight) { pending = true; return; }
    inflight = true;
    dirty = false;
    setStatus('saving', 'saving…');
    try {
      const res = await fetch('/api/note/' + path, {
        method: 'POST',
        headers: { 'Content-Type': 'text/html' },
        body: assemble(),
      });
      if (!res.ok) throw new Error('http ' + res.status);
      setStatus('saved', 'saved');
      scheduleSurfaceRefresh();
    } catch (e) {
      setStatus('offline', 'offline');
      dirty = true;
    } finally {
      inflight = false;
      if (pending) { pending = false; save(); }
    }
  }

  // ----- Brain panel ---------------------------------------------------------
  // Why debounced: a rapid save burst (e.g. paste, then immediate Cmd-S) would
  // otherwise hammer the surface endpoint with redundant fetches.
  let surfaceTimer = null;
  function scheduleSurfaceRefresh() {
    clearTimeout(surfaceTimer);
    surfaceTimer = setTimeout(loadSurface, 800);
  }

  function titleFromPath(p) {
    const base = p.split('/').pop() || p;
    return base.replace(/\.html?$/i, '');
  }

  function chipLabel(reason) {
    // Translate Reasons strings into compact, human chip labels.
    if (reason === 'backlink')    return 'backlink';
    if (reason === 'semantic')    return 'semantic';
    if (reason === 'recent')      return 'recent';
    if (reason === 'co-accessed') return 'co-accessed';
    // Legacy formats kept so older daemon responses don't break the UI.
    if (reason.startsWith('on-this-day:')) {
      return reason.slice('on-this-day:'.length) + ' ago';
    }
    if (reason.startsWith('similar:')) {
      return 'similar ' + reason.slice('similar:'.length);
    }
    return reason;
  }

  // "Nyears ago" / "1year ago" — no space, per spec.
  function yearsAgoLabel(modTime) {
    if (!modTime) return '';
    const then = new Date(modTime);
    if (isNaN(then.getTime())) return '';
    const now = new Date();
    let years = now.getFullYear() - then.getFullYear();
    const m = now.getMonth() - then.getMonth();
    if (m < 0 || (m === 0 && now.getDate() < then.getDate())) years--;
    if (years < 1) return '';
    return years + (years === 1 ? 'year ago' : 'years ago');
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
      a.href = '?path=' + encodeURIComponent(p);
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
      a.href = '?path=' + encodeURIComponent(it.Path);

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
    onThisDayEl.innerHTML = '';
    if (!items || items.length === 0) {
      onThisDaySectionEl.hidden = true;
      return;
    }
    onThisDaySectionEl.hidden = false;
    for (const it of items.slice(0, 12)) {
      const li = document.createElement('li');
      li.className = 'brain-item';

      const a = document.createElement('a');
      a.href = '?path=' + encodeURIComponent(it.Path);

      const title = document.createElement('span');
      title.className = 'brain-title';
      title.textContent = it.Title || titleFromPath(it.Path);
      a.appendChild(title);

      const sub = yearsAgoLabel(it.ModTime);
      if (sub) {
        const subEl = document.createElement('span');
        subEl.className = 'brain-sub';
        subEl.textContent = sub;
        a.appendChild(subEl);
      }

      const pathSpan = document.createElement('span');
      pathSpan.className = 'brain-path';
      pathSpan.textContent = it.Path;
      a.appendChild(pathSpan);

      li.appendChild(a);
      onThisDayEl.appendChild(li);
    }
  }

  async function loadSurface() {
    if (!path) {
      backlinksEl.textContent = '—';
      surfacedSectionEl.hidden = true;
      onThisDaySectionEl.hidden = true;
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
      // Silent on failure per spec — panel stays a quiet dash.
      backlinksEl.className = 'brain-body';
      backlinksEl.textContent = '—';
      surfacedSectionEl.hidden = true;
      onThisDaySectionEl.hidden = true;
    }
  }

  function scheduleSave() {
    dirty = true;
    setStatus('saving', 'saving…');
    clearTimeout(saveTimer);
    saveTimer = setTimeout(save, 300);
  }

  titleEl.addEventListener('input', scheduleSave);
  bodyEl.addEventListener('input', scheduleSave);

  // Cmd/Ctrl-S forces immediate flush.
  document.addEventListener('keydown', e => {
    if ((e.metaKey || e.ctrlKey) && e.key === 's') {
      e.preventDefault();
      clearTimeout(saveTimer);
      save();
    }
  });

  // Best-effort flush on tab close.
  window.addEventListener('beforeunload', e => {
    if (dirty) {
      navigator.sendBeacon?.('/api/note/' + path,
        new Blob([assemble()], { type: 'text/html' }));
    }
  });

  load().then(loadSurface);
})();
