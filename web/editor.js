// Weft editor — TipTap over a single contenteditable div.
// Why TipTap: vanilla contenteditable produces inconsistent HTML across
// browsers (especially around lists, paste, and undo). TipTap normalizes the
// document via ProseMirror's schema, which is the price we pay for a stable
// round-trip.
// Why ESM-from-CDN: keeps "one Go binary, no build step" intact. The browser
// fetches modules from esm.sh on first load and caches them thereafter.

import { Editor, Extension } from 'https://esm.sh/@tiptap/core@2';
import StarterKit    from 'https://esm.sh/@tiptap/starter-kit@2';
import Link          from 'https://esm.sh/@tiptap/extension-link@2';
import Placeholder   from 'https://esm.sh/@tiptap/extension-placeholder@2';
import Suggestion    from 'https://esm.sh/@tiptap/suggestion@2';

const params = new URLSearchParams(location.search);
const path = params.get('path') || '';

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

const viewLinkEl = document.getElementById('view-link');
if (path && viewLinkEl) {
  viewLinkEl.href = '/note/' + path;
  viewLinkEl.hidden = false;
}

let saveTimer = null;
let inflight = false;
let pending = false;
let dirty = false;
// Suppress the initial setContent() from marking the doc dirty. TipTap fires
// `update` on programmatic setContent unless we pass `emitUpdate:false`, but
// being explicit with a guard is safer across versions.
let loading = true;

function setStatus(state, text) {
  statusEl.className = 'status ' + state;
  statusEl.textContent = text;
}

// Tab-title dirty indicator. The bullet is the v0.3 spec convention.
const BASE_TITLE = 'Weft';
function setTitleDirty(isDirty) {
  document.title = isDirty ? '• ' + BASE_TITLE : BASE_TITLE;
}
setTitleDirty(false);

// ---- Wikilink autocomplete ------------------------------------------------
// One-shot fetch of the vault note list — kept in memory for the lifetime of
// the page. The dropdown filters this in-memory; we don't re-hit /api/notes
// per keystroke.
let notesCache = null;
let notesPromise = null;
function loadNotes() {
  if (notesPromise) return notesPromise;
  notesPromise = fetch('/api/notes')
    .then(r => r.ok ? r.json() : [])
    .then(list => { notesCache = Array.isArray(list) ? list : []; return notesCache; })
    .catch(() => { notesCache = []; return notesCache; });
  return notesPromise;
}
loadNotes();

// Vanilla dropdown renderer. The Suggestion plugin owns state (range,
// selectedIndex, items); this just paints and positions DOM. Why no popper
// lib: coordsAtPos already gives us viewport-relative coords; that's enough
// for a v0.3 dropdown.
function createSuggestionUI() {
  let root = null;
  let items = [];
  let selectedIndex = 0;
  let commandFn = null;

  function ensureRoot() {
    if (root) return root;
    root = document.createElement('div');
    root.className = 'wiki-suggestions';
    root.hidden = true;
    document.body.appendChild(root);
    return root;
  }

  function render() {
    ensureRoot();
    root.innerHTML = '';
    if (!items.length) {
      const empty = document.createElement('div');
      empty.className = 'wiki-suggestion is-empty';
      empty.textContent = 'no matches';
      root.appendChild(empty);
      return;
    }
    items.forEach((it, i) => {
      const row = document.createElement('div');
      row.className = 'wiki-suggestion' + (i === selectedIndex ? ' is-selected' : '');
      const name = document.createElement('span');
      name.className = 'name';
      name.textContent = it.Name;
      const path = document.createElement('span');
      path.className = 'path';
      path.textContent = it.Path;
      row.appendChild(name);
      row.appendChild(path);
      // mousedown (not click) so the editor doesn't blur first and cancel.
      row.addEventListener('mousedown', e => {
        e.preventDefault();
        selectedIndex = i;
        if (commandFn) commandFn(items[i]);
      });
      root.appendChild(row);
    });
  }

  function position(props) {
    ensureRoot();
    const rect = props.clientRect && props.clientRect();
    if (!rect) return;
    root.style.top  = (window.scrollY + rect.bottom + 4) + 'px';
    root.style.left = (window.scrollX + rect.left) + 'px';
  }

  return {
    onStart(props) {
      items = props.items;
      selectedIndex = 0;
      commandFn = props.command;
      ensureRoot();
      root.hidden = false;
      render();
      position(props);
    },
    onUpdate(props) {
      items = props.items;
      commandFn = props.command;
      if (selectedIndex >= items.length) selectedIndex = 0;
      render();
      position(props);
    },
    onKeyDown(props) {
      const e = props.event;
      if (e.key === 'ArrowDown') {
        selectedIndex = (selectedIndex + 1) % Math.max(items.length, 1);
        render();
        return true;
      }
      if (e.key === 'ArrowUp') {
        selectedIndex = (selectedIndex - 1 + items.length) % Math.max(items.length, 1);
        render();
        return true;
      }
      if (e.key === 'Enter' || e.key === 'Tab') {
        if (items.length && commandFn) commandFn(items[selectedIndex]);
        return true;
      }
      if (e.key === 'Escape') {
        if (root) root.hidden = true;
        return true;
      }
      return false;
    },
    onExit() {
      if (root) root.hidden = true;
      items = [];
      commandFn = null;
    },
  };
}

// Wikilinks: trigger on the second '[' of '[['. We use the single-char
// trigger and validate the preceding char in `allow`, because @tiptap/suggestion
// matches char-by-char and a multi-char trigger isn't first-class.
const WikilinkSuggestion = Extension.create({
  name: 'wikilinkSuggestion',
  addOptions() {
    return {
      suggestion: {
        char: '[',
        startOfLine: false,
        allowSpaces: true,
        // Only fire when the char immediately before our trigger '[' is also
        // '[' — i.e. the user just completed '[['.
        allow: ({ state, range }) => {
          const before = state.doc.textBetween(Math.max(0, range.from - 1), range.from, '\n', '\0');
          return before === '[';
        },
        items: ({ query }) => {
          const notes = notesCache || [];
          const q = (query || '').toLowerCase();
          const filtered = q
            ? notes.filter(n => (n.Name || '').toLowerCase().includes(q))
            : notes;
          return filtered.slice(0, 10);
        },
        command: ({ editor, range, props }) => {
          // `range` covers from the trigger '[' through the typed query. The
          // first '[' of '[[' sits one position before range.from. If the
          // user has typed the closing ']]' already, swallow that too so we
          // don't leave dangling brackets after the anchor.
          const from = Math.max(0, range.from - 1);
          let to = range.to;
          const docSize = editor.state.doc.content.size;
          const after = editor.state.doc.textBetween(to, Math.min(docSize, to + 2), '\n', '\0');
          if (after.startsWith(']]')) to += 2;

          const name = props.Name;
          const href = props.Path;
          editor.chain()
            .focus()
            .insertContentAt({ from, to }, [
              {
                type: 'text',
                text: name,
                marks: [{ type: 'link', attrs: { href } }],
              },
              { type: 'text', text: ' ' },
            ])
            .run();
        },
        render: () => {
          const ui = createSuggestionUI();
          return {
            onStart:   ui.onStart,
            onUpdate:  ui.onUpdate,
            onKeyDown: ui.onKeyDown,
            onExit:    ui.onExit,
          };
        },
      },
    };
  },
  addProseMirrorPlugins() {
    return [Suggestion({ editor: this.editor, ...this.options.suggestion })];
  },
});

// ---- TipTap ----------------------------------------------------------------

const editor = new Editor({
  element: document.getElementById('editor'),
  extensions: [
    StarterKit,
    Link.configure({
      openOnClick: false,
      autolink: true,
      HTMLAttributes: { rel: 'noopener noreferrer' },
    }),
    Placeholder.configure({
      // Show "Untitled" on the first empty heading, "Write…" elsewhere — so
      // a brand-new document looks like a titled note rather than a blank slab.
      placeholder: ({ node }) =>
        node.type.name === 'heading' && node.attrs.level === 1
          ? 'Untitled'
          : 'Write…',
      showOnlyWhenEditable: true,
    }),
    WikilinkSuggestion,
  ],
  content: '',
  autofocus: false,
  onUpdate: () => {
    if (loading) return;
    scheduleSave();
  },
});

function firstH1Text() {
  // Walk the prose-mirror doc for the first level-1 heading, fallback to path.
  let title = '';
  editor.state.doc.descendants((node) => {
    if (title) return false;
    if (node.type.name === 'heading' && node.attrs.level === 1) {
      title = node.textContent.trim();
      return false;
    }
    return true;
  });
  return title || titleFromPath(path) || 'Untitled';
}

// Pull just the <article> contents out of the saved document. Falls back to
// <body> for legacy notes that were never written through this editor.
function extractArticle(html) {
  const doc = new DOMParser().parseFromString(html, 'text/html');
  const article = doc.querySelector('article');
  const root = article || doc.body;
  return root.innerHTML.trim();
}

function escapeHtml(s) {
  return s.replace(/[&<>"']/g, c => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
  }[c]));
}

function assemble() {
  const title = firstH1Text();
  const body  = editor.getHTML();
  return '<!DOCTYPE html><html><head><meta charset="utf-8"><title>' +
    escapeHtml(title) + '</title></head><body><article>' +
    body + '</article></body></html>';
}

async function load() {
  if (!path) {
    loading = false;
    return;
  }
  try {
    const res = await fetch('/raw/' + path);
    if (!res.ok) {
      setStatus('', 'new');
      loading = false;
      return;
    }
    const html = await res.text();
    const inner = extractArticle(html);
    editor.commands.setContent(inner, false); // false = don't emit update
    setStatus('saved', 'loaded');
  } catch (e) {
    setStatus('offline', 'offline');
  } finally {
    loading = false;
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
    setTitleDirty(false);
    scheduleSurfaceRefresh();
  } catch (e) {
    setStatus('offline', 'offline');
    dirty = true;
  } finally {
    inflight = false;
    if (pending) { pending = false; save(); }
  }
}

// Autosave fires 1s after the last keystroke; the spec bumped this from 300ms
// to give a slightly calmer "saving…" cadence with the richer editor.
function scheduleSave() {
  dirty = true;
  setTitleDirty(true);
  setStatus('saving', 'saving…');
  clearTimeout(saveTimer);
  saveTimer = setTimeout(save, 1000);
}

// Cmd/Ctrl-S forces immediate flush.
document.addEventListener('keydown', e => {
  if ((e.metaKey || e.ctrlKey) && e.key === 's') {
    e.preventDefault();
    clearTimeout(saveTimer);
    save();
  }
});

// Best-effort flush on tab close.
window.addEventListener('beforeunload', () => {
  if (dirty && path) {
    navigator.sendBeacon?.('/api/note/' + path,
      new Blob([assemble()], { type: 'text/html' }));
  }
});

// ----- Brain panel ---------------------------------------------------------
// Why debounced: a rapid save burst (e.g. paste, then immediate Cmd-S) would
// otherwise hammer the surface endpoint with redundant fetches.
let surfaceTimer = null;
function scheduleSurfaceRefresh() {
  clearTimeout(surfaceTimer);
  surfaceTimer = setTimeout(loadSurface, 800);
}

function titleFromPath(p) {
  const base = (p || '').split('/').pop() || p || '';
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

load().then(loadSurface);
