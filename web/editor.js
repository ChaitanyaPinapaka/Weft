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
    } catch (e) {
      setStatus('offline', 'offline');
      dirty = true;
    } finally {
      inflight = false;
      if (pending) { pending = false; save(); }
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

  load();
})();
