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
import TaskList      from 'https://esm.sh/@tiptap/extension-task-list@2';
import TaskItem      from 'https://esm.sh/@tiptap/extension-task-item@2';
import Suggestion    from 'https://esm.sh/@tiptap/suggestion@2';
import { PluginKey } from 'https://esm.sh/@tiptap/pm@2/state';

// Distinct ProseMirror plugin keys for our two Suggestion-based extensions.
// @tiptap/suggestion otherwise defaults both to its shared module-level
// SuggestionPluginKey, and ProseMirror rejects two plugins under one key with
// "Adding different instances of a keyed plugin" — which throws inside
// `new Editor(...)` and aborts the whole editor module (no load, no autosave).
// Keys come from @tiptap/pm so they're the same ProseMirror instance TipTap uses.
const WIKILINK_SUGGESTION_KEY = new PluginKey('weftWikilinkSuggestion');
const SLASH_COMMANDS_KEY      = new PluginKey('weftSlashCommands');

const params = new URLSearchParams(location.search);
const path = params.get('path') || '';

const pathEl   = document.getElementById('path');
const statusEl = document.getElementById('status');
const trailEl           = document.getElementById('brain-trail');
const backlinksEl       = document.getElementById('brain-backlinks');
const surfacedEl        = document.getElementById('brain-surfaced');
const surfacedSectionEl = document.getElementById('brain-surfaced-section');
const onThisDayEl        = document.getElementById('brain-onthisday');
const onThisDaySectionEl = document.getElementById('brain-onthisday-section');
const brainToggleEl      = document.getElementById('brain-toggle');
const brainEl            = document.getElementById('brain');

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

// "Explain" toggle — opt-in reveal of the activation math (act/base/spread) per
// surfaced item. Default OFF so the panel stays calm. We toggle a class on the
// #brain root and keep the breakdown line always in the DOM but CSS-hidden when
// the class is absent; that avoids a /api/surface re-fetch on every toggle.
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
let stopped = false; // set by the host bridge to prevent any further saves
// Suppress the initial setContent() from marking the doc dirty. TipTap fires
// `update` on programmatic setContent unless we pass `emitUpdate:false`, but
// being explicit with a guard is safer across versions.
let loading = true;
// Optimistic-concurrency baseline (R1): the ETag of the bytes we last loaded or
// saved. Sent as If-Match so the daemon rejects (412) a save when the note has
// changed underneath us — another tab/device or a capture — instead of clobbering
// it. null = no baseline (a brand-new note, or after the user chose "overwrite").
let currentETag = null;
let conflicted = false;

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

// Default row renderer for the wikilink dropdown: a note Name over its Path.
function renderWikiRow(it, row) {
  const name = document.createElement('span');
  name.className = 'name';
  name.textContent = it.Name;
  const path = document.createElement('span');
  path.className = 'path';
  path.textContent = it.Path;
  row.appendChild(name);
  row.appendChild(path);
}

// Vanilla dropdown renderer. The Suggestion plugin owns state (range,
// selectedIndex, items); this just paints and positions DOM. Why no popper
// lib: coordsAtPos already gives us viewport-relative coords; that's enough
// for a v0.3 dropdown. Parameterised by a per-row painter and empty label so
// the slash menu can reuse the exact same popup styling and keyboard handling.
function createSuggestionUI(opts) {
  const renderItem = (opts && opts.renderItem) || renderWikiRow;
  const emptyText  = (opts && opts.emptyText)  || 'no matches';
  const rootClass  = 'wiki-suggestions' + (opts && opts.extraClass ? ' ' + opts.extraClass : '');
  let root = null;
  let items = [];
  let selectedIndex = 0;
  let commandFn = null;

  function ensureRoot() {
    if (root) return root;
    root = document.createElement('div');
    root.className = rootClass;
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
      empty.textContent = emptyText;
      root.appendChild(empty);
      return;
    }
    items.forEach((it, i) => {
      const row = document.createElement('div');
      row.className = 'wiki-suggestion' + (i === selectedIndex ? ' is-selected' : '');
      renderItem(it, row);
      // mousedown (not click) so the editor doesn't blur first and cancel.
      row.addEventListener('mousedown', e => {
        e.preventDefault();
        selectedIndex = i;
        if (commandFn) commandFn(items[i]);
      });
      // Keep the hovered row in sync with keyboard selection.
      row.addEventListener('mousemove', () => {
        if (selectedIndex !== i) { selectedIndex = i; render(); }
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
        // Two-char trigger: @tiptap/suggestion's escapeForRegEx treats the
        // string literally in its match regex, so '[[' works directly. With a
        // single-char '[' trigger the second bracket gets eaten into the
        // query instead of starting a fresh match, which is why nothing
        // surfaced before the fix.
        char: '[[',
        startOfLine: false,
        allowSpaces: true,
        // Default allowedPrefixes is [' '] which means a [[ at the start of
        // a paragraph (after a block boundary, not whitespace) won't trigger.
        // Null disables the prefix check entirely.
        allowedPrefixes: null,
        items: ({ query }) => {
          const notes = notesCache || [];
          const q = (query || '').toLowerCase();
          const filtered = q
            ? notes.filter(n => (n.Name || '').toLowerCase().includes(q))
            : notes;
          return filtered.slice(0, 10);
        },
        command: ({ editor, range, props }) => {
          // `range.from` is at the first '['; range.to is the cursor. If the
          // user has typed the closing ']]' already, swallow that too so we
          // don't leave dangling brackets after the anchor.
          const from = range.from;
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
                // class:'wiki-chip' rides on the link mark so the anchor
                // serializes as <a class="wiki-chip" href> — the chip styling is
                // pure CSS and the server-side wikilink pass still sees a normal
                // <a href>, so the save/expand round-trip is unchanged.
                marks: [{ type: 'link', attrs: { href, class: 'wiki-chip' } }],
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
    // Distinct pluginKey: both this and SlashCommands build on @tiptap/suggestion,
    // which defaults to the ProseMirror plugin key "suggestion$". Two plugins
    // sharing one key makes ProseMirror throw "Adding different instances of a
    // keyed plugin" during editor construction, aborting the whole module
    // (no content load, no autosave). A unique key per plugin avoids the clash.
    return [Suggestion({ editor: this.editor, pluginKey: WIKILINK_SUGGESTION_KEY, ...this.options.suggestion })];
  },
});

// ---- Slash menu ------------------------------------------------------------
// Typing '/' at the start of an empty line opens a block-insertion menu. It
// reuses the same @tiptap/suggestion plumbing and dropdown UI as the wikilink
// autocomplete, so the keyboard handling (↑/↓/Enter/Esc) and popup styling are
// identical — one mental model, one stylesheet.
//
// Each command receives the chain pre-focused; `deleteRange(range)` first
// removes the typed "/query" so the slash text never lands in the document.
const SLASH_COMMANDS = [
  { title: 'Heading 1',     hint: '#',   keywords: 'h1 title big',
    run: c => c.toggleHeading({ level: 1 }) },
  { title: 'Heading 2',     hint: '##',  keywords: 'h2',
    run: c => c.toggleHeading({ level: 2 }) },
  { title: 'Heading 3',     hint: '###', keywords: 'h3',
    run: c => c.toggleHeading({ level: 3 }) },
  { title: 'Bullet list',   hint: '•',   keywords: 'unordered ul bullets',
    run: c => c.toggleBulletList() },
  { title: 'Numbered list', hint: '1.',  keywords: 'ordered ol numbers',
    run: c => c.toggleOrderedList() },
  { title: 'Task list',     hint: '☐',   keywords: 'todo checkbox check',
    run: c => c.toggleTaskList() },
  { title: 'Code block',    hint: '</>', keywords: 'code pre monospace',
    run: c => c.toggleCodeBlock() },
  { title: 'Quote',         hint: '"',   keywords: 'blockquote citation',
    run: c => c.toggleBlockquote() },
  { title: 'Divider',       hint: '—',   keywords: 'hr rule horizontal separator',
    run: c => c.setHorizontalRule() },
  // Wikilink: drop the literal '[[' so the existing wikilink Suggestion takes
  // over from here — no duplicated note-picker logic.
  { title: 'Wikilink',      hint: '[[',  keywords: 'link note reference wiki',
    run: c => c.insertContent('[[') },
];

function renderSlashRow(it, row) {
  const hint = document.createElement('span');
  hint.className = 'slash-hint';
  hint.textContent = it.hint;
  const name = document.createElement('span');
  name.className = 'name';
  name.textContent = it.title;
  row.appendChild(hint);
  row.appendChild(name);
}

const SlashCommands = Extension.create({
  name: 'slashCommands',
  addOptions() {
    return {
      suggestion: {
        char: '/',
        // Only fire when the '/' is the first character of a paragraph — a '/'
        // mid-word (e.g. "and/or", a URL) must stay literal. We check the
        // trigger position (range.from), NOT node emptiness, because the query
        // text ("/heading") legitimately fills the block once typing starts.
        startOfLine: true,
        allow: ({ state, range }) => {
          const $from = state.doc.resolve(range.from);
          return $from.parentOffset === 0 && $from.parent.type.name === 'paragraph';
        },
        items: ({ query }) => {
          const q = (query || '').toLowerCase();
          if (!q) return SLASH_COMMANDS;
          return SLASH_COMMANDS.filter(c =>
            c.title.toLowerCase().includes(q) ||
            (c.keywords || '').includes(q));
        },
        command: ({ editor, range, props }) => {
          // Strip the typed "/query" first, then run the block command on the
          // now-empty line in a single chain so undo treats it as one step.
          props.run(editor.chain().focus().deleteRange(range)).run();
        },
        render: () => {
          const ui = createSuggestionUI({
            renderItem: renderSlashRow,
            emptyText: 'no commands',
            extraClass: 'slash-suggestions',
          });
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
    return [Suggestion({ editor: this.editor, pluginKey: SLASH_COMMANDS_KEY, ...this.options.suggestion })];
  },
});

// ---- Bubble menu -----------------------------------------------------------
// A floating toolbar that appears over a non-empty text selection. We build the
// DOM and own its show/hide + positioning ourselves (bar._update, via
// coordsAtPos) rather than pulling in @tiptap/extension-bubble-menu — that
// extension drags in tippy, which threw during editor construction and left the
// note blank. Each button reflects the mark/node active state.
function buildBubbleMenu(getEditor) {
  const bar = document.createElement('div');
  bar.className = 'bubble-menu';

  // [label, isActive(editor) → bool, run(chain) → chain, title]
  const BUTTONS = [
    { label: 'B',  className: 'is-bold',   title: 'Bold (⌘B)',
      active: e => e.isActive('bold'),
      run:    c => c.toggleBold() },
    { label: 'I',  className: 'is-italic', title: 'Italic (⌘I)',
      active: e => e.isActive('italic'),
      run:    c => c.toggleItalic() },
    { label: '<>', className: 'is-code',   title: 'Inline code',
      active: e => e.isActive('code'),
      run:    c => c.toggleCode() },
    { label: '↗',  className: 'is-link',   title: 'Link',
      active: e => e.isActive('link'),
      // The link button prompts for a URL rather than running a plain chain.
      link: true },
    { sep: true },
    { label: 'H1', title: 'Heading 1',
      active: e => e.isActive('heading', { level: 1 }),
      run:    c => c.toggleHeading({ level: 1 }) },
    { label: 'H2', title: 'Heading 2',
      active: e => e.isActive('heading', { level: 2 }),
      run:    c => c.toggleHeading({ level: 2 }) },
    { sep: true },
    { label: '•',  title: 'Bullet list',
      active: e => e.isActive('bulletList'),
      run:    c => c.toggleBulletList() },
    { label: '1.', title: 'Numbered list',
      active: e => e.isActive('orderedList'),
      run:    c => c.toggleOrderedList() },
  ];

  const refreshers = [];
  for (const b of BUTTONS) {
    if (b.sep) {
      const sep = document.createElement('span');
      sep.className = 'bubble-sep';
      bar.appendChild(sep);
      continue;
    }
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'bubble-btn' + (b.className ? ' ' + b.className : '');
    btn.textContent = b.label;
    btn.title = b.title;
    btn.addEventListener('mousedown', e => {
      // mousedown + preventDefault so the editor selection isn't lost on click.
      e.preventDefault();
      const editor = getEditor();
      if (!editor) return;
      if (b.link) { toggleLinkPrompt(editor); return; }
      b.run(editor.chain().focus()).run();
    });
    bar.appendChild(btn);
    if (b.active) refreshers.push(() => {
      const editor = getEditor();
      btn.classList.toggle('is-active', !!(editor && b.active(editor)));
    });
  }

  bar._refresh = () => { for (const r of refreshers) r(); };

  // Show/position manually over a non-empty text selection — no popper/tippy,
  // matching the wikilink dropdown's coordsAtPos approach (the official
  // BubbleMenu extension dragged in tippy, which broke editor construction).
  bar._update = (editor) => {
    if (!editor || !editor.isEditable) { bar.style.display = 'none'; return; }
    const { from, to, empty } = editor.state.selection;
    if (empty) { bar.style.display = 'none'; return; }
    bar._refresh();
    bar.style.display = 'flex';
    const start = editor.view.coordsAtPos(from);
    const end   = editor.view.coordsAtPos(to);
    const rect  = bar.getBoundingClientRect();
    const mid   = (Math.min(start.left, end.left) + Math.max(start.right, end.right)) / 2;
    let left = Math.max(8, Math.min(mid - rect.width / 2, window.innerWidth - rect.width - 8));
    let top  = start.top - rect.height - 8;       // above the selection…
    if (top < 8) top = end.bottom + 8;            // …or below if there's no room
    bar.style.left = left + 'px';
    bar.style.top  = top + 'px';
  };

  bar.style.display = 'none';
  document.body.appendChild(bar);
  return bar;
}

// Link button: toggle off if already a link, else prompt for a URL. Kept as a
// plain window.prompt — boring beats a custom mini-form for a v0.3 affordance.
function toggleLinkPrompt(editor) {
  if (editor.isActive('link')) {
    editor.chain().focus().unsetLink().run();
    return;
  }
  const prev = editor.getAttributes('link').href || '';
  const url = window.prompt('Link URL', prev);
  if (url === null) return;            // cancelled
  if (url === '') { editor.chain().focus().unsetLink().run(); return; }
  editor.chain().focus().setLink({ href: url }).run();
}

// ---- TipTap ----------------------------------------------------------------

// Forward reference: the bubble-menu buttons need the editor, but the editor
// needs the bubble element. We build the element first with a getter closure,
// then assign `editor` below.
let editor = null;
const bubbleEl = buildBubbleMenu(() => editor);

editor = new Editor({
  element: document.getElementById('editor'),
  extensions: [
    StarterKit,
    Link.configure({
      openOnClick: false,
      autolink: true,
      HTMLAttributes: { rel: 'noopener noreferrer' },
    }),
    TaskList,
    TaskItem.configure({ nested: true }),
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
    SlashCommands,
  ],
  content: '',
  autofocus: false,
  onUpdate: () => {
    if (loading) return;
    scheduleSave();
    updateCounts();
  },
  // Position + show/hide the bubble toolbar (and refresh its active states) on
  // every selection / doc change; hide it when focus leaves the editor.
  onSelectionUpdate: () => bubbleEl._update(editor),
  onTransaction:     () => bubbleEl._update(editor),
  onBlur:            () => { bubbleEl.style.display = 'none'; },
});

// ---- Word / char count -----------------------------------------------------
// Live counter in the editor footer. We read editor.getText() rather than add
// the @tiptap/extension-character-count dependency: the doc is small, this runs
// only on update, and it keeps the import list lean.
const countEl = document.getElementById('count');
function updateCounts() {
  if (!countEl) return;
  const text = editor.getText().trim();
  const chars = text.length;
  const words = text ? text.split(/\s+/).length : 0;
  countEl.textContent = words + (words === 1 ? ' word' : ' words') + ' · ' + chars + ' chars';
}

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
    currentETag = res.headers.get('ETag'); // baseline for optimistic save
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
  if (stopped || conflicted) return;
  if (!path) {
    setStatus('offline', 'no path');
    return;
  }
  if (inflight) { pending = true; return; }
  inflight = true;
  dirty = false;
  setStatus('saving', 'saving…');
  try {
    const headers = { 'Content-Type': 'text/html' };
    if (currentETag) headers['If-Match'] = currentETag;
    const res = await fetch('/api/note/' + path, {
      method: 'POST', headers, body: assemble(),
    });
    if (res.status === 412) {
      // The note changed on disk since we loaded it; don't overwrite blindly.
      dirty = true;
      showConflict();
      return;
    }
    if (!res.ok) throw new Error('http ' + res.status);
    currentETag = res.headers.get('ETag') || currentETag; // new baseline
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

// On a 412 we stop autosaving and offer an explicit choice rather than silently
// losing either side: Reload (take the on-disk version, discard local edits) or
// Overwrite (drop the baseline so the next save wins unconditionally).
let conflictBanner = null;
function showConflict() {
  conflicted = true;
  setStatus('offline', 'changed on disk');
  if (conflictBanner) { conflictBanner.hidden = false; return; }
  conflictBanner = document.createElement('div');
  conflictBanner.className = 'conflict-banner';
  const msg = document.createElement('span');
  msg.textContent = 'This note changed elsewhere (another tab, device, or a capture).';
  const reload = document.createElement('button');
  reload.type = 'button'; reload.textContent = 'Reload';
  reload.addEventListener('click', () => location.reload());
  const keep = document.createElement('button');
  keep.type = 'button'; keep.textContent = 'Overwrite with mine';
  keep.addEventListener('click', () => {
    conflicted = false;
    currentETag = null;          // unconditional next save
    conflictBanner.hidden = true;
    save();
  });
  conflictBanner.append(msg, reload, keep);
  document.body.appendChild(conflictBanner);
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

// Best-effort flush on tab close. Use fetch(keepalive) rather than sendBeacon so
// the request can carry If-Match — the beacon must not clobber a newer on-disk
// version either. Fire on dirty OR inflight (a save in flight already cleared
// dirty but its bytes may not have landed). Conflicts are surfaced on the live
// page, not here, so a 412 on unload simply leaves the on-disk version intact.
window.addEventListener('beforeunload', () => {
  if ((dirty || inflight) && path && !stopped && !conflicted) {
    const headers = { 'Content-Type': 'text/html' };
    if (currentETag) headers['If-Match'] = currentETag;
    fetch('/api/note/' + path, {
      method: 'POST', headers, body: assemble(), keepalive: true,
    }).catch(() => {});
  }
});

// Host bridges for the native apps, which embed this page in a WKWebView.
// WKWebView never fires beforeunload on programmatic navigation, so the host
// drives the final save explicitly instead of relying on the beacon above.

// Flush any pending edit and resolve ONLY once it has fully landed, so the
// native reader can re-fetch without racing the 1s autosave timer.
window.weftFlush = async function () {
  clearTimeout(saveTimer);
  if (dirty && path) await save();
  while (inflight || pending) {
    await new Promise(r => setTimeout(r, 25));
  }
};

// Stop saving for good — the host calls this before trashing the open note so a
// queued autosave can't recreate the file in the vault after it's been removed.
window.weftStop = function () {
  stopped = true;
  clearTimeout(saveTimer);
};

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
  if (reason === 'base')        return 'base';
  if (reason === 'resurfaced')  return 'resurfaced';
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

// Thought trail: a faint breadcrumb of the path taken this session (A › B › C).
// trail[0] is the current focus note. We only show it when there is a real path
// (more than one entry) — a single-entry trail is just the current note.
// Crumbs link to /note/{path} so the editor's trail mirrors the viewer's.
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
    a.href = '?path=' + encodeURIComponent(p);
    a.textContent = titleFromPath(p);
    a.title = p;
    li.appendChild(a);
    ul.appendChild(li);
  }
  backlinksEl.className = '';
  backlinksEl.appendChild(ul);
}

// Map a 0..1 fraction (0 = hottest/top, 1 = coldest/bottom) to an opacity in
// [MIN_TEMP_OPACITY, 1]. Restraint over decoration: temperature is conveyed by
// opacity + order only — no glow, no animation. Hot rises, dormant recedes to
// grey but never vanishes (we floor at MIN_TEMP_OPACITY).
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
    a.href = '?path=' + encodeURIComponent(it.Path);
    // Temperature by position: top item full strength, lower items dimmed
    // toward --muted. Order already encodes rank; opacity reinforces it quietly.
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
    if (trailEl) trailEl.hidden = true;
    backlinksEl.textContent = '—';
    surfacedSectionEl.hidden = true;
    onThisDaySectionEl.hidden = true;
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
    // Silent on failure per spec — panel stays a quiet dash.
    if (trailEl) trailEl.hidden = true;
    backlinksEl.className = 'brain-body';
    backlinksEl.textContent = '—';
    surfacedSectionEl.hidden = true;
    onThisDaySectionEl.hidden = true;
  }
}

load().then(() => { updateCounts(); return loadSurface(); });
