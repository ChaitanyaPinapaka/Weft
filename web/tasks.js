// Weft tasks board — Now/Soon/Later kanban over a single tasks.html document.
// Why a flat IIFE: this is one screen with no shared state, no imports beyond
// DOM APIs, and the daemon does all the heavy lifting. A module wrapper would
// just add ceremony.

(function () {
  'use strict';

  // ---- Constants ----------------------------------------------------------

  const PATH = 'tasks.html';
  const COLUMNS = ['now', 'soon', 'later'];
  const LABELS = { now: 'Now', soon: 'Soon', later: 'Later' };

  // ---- DOM handles --------------------------------------------------------

  const statusEl = document.getElementById('status');
  const boardEl  = document.getElementById('board');
  const emptyEl  = document.getElementById('empty-state');
  const createBtn = document.getElementById('create-btn');
  const archiveBtn = document.getElementById('archive-btn');
  const archiveErrorEl = document.getElementById('archive-error');

  const lists = {
    now:   document.getElementById('col-now'),
    soon:  document.getElementById('col-soon'),
    later: document.getElementById('col-later'),
  };

  // Extra DOM nodes from the source document that aren't part of any
  // Now/Soon/Later column. Preserved verbatim on save so the user's hand-authored
  // notes inside tasks.html aren't silently dropped.
  // Stored as an array of HTML strings in document order, each tagged with a
  // position relative to known anchors (h1, before-now, before-soon, before-later, after-later).
  let extras = { afterH1: [], beforeSoon: [], beforeLater: [], afterLater: [] };
  let h1Text = 'Tasks';

  // ---- Status indicator ---------------------------------------------------

  function setStatus(state, text) {
    statusEl.className = 'status ' + state;
    statusEl.textContent = text;
  }
  setStatus('', 'idle');

  // ---- Parsing -----------------------------------------------------------
  // DOMParser is the right tool here: tasks.html is well-formed HTML produced
  // by TipTap, and the browser's parser handles malformed user edits more
  // gracefully than any regex. We walk h2 -> next-ul pairs by sibling order.

  function parseDoc(html) {
    const doc = new DOMParser().parseFromString(html, 'text/html');
    const article = doc.querySelector('article') || doc.body;

    // Capture h1 text if present.
    const h1 = article.querySelector('h1');
    if (h1) h1Text = h1.textContent.trim() || 'Tasks';

    extras = { afterH1: [], beforeSoon: [], beforeLater: [], afterLater: [] };
    const colNodes = { now: null, soon: null, later: null };

    // Single linear pass over article children. Each h2 whose text matches a
    // known column claims the next <ul> sibling; anything in between (or
    // before the first h2) is preserved in the appropriate extras bucket.
    let bucket = 'afterH1';
    let pendingHeading = null;
    for (const node of Array.from(article.children)) {
      if (node === h1) continue;

      if (pendingHeading) {
        if (node.tagName === 'UL') {
          colNodes[pendingHeading] = node;
          // Subsequent unrelated content belongs to the "before <next>" bucket.
          bucket = nextBucketAfter(pendingHeading);
          pendingHeading = null;
          continue;
        }
        // No ul followed the heading — treat the heading itself as extra.
        extras[bucket].push(serializeHeading(pendingHeading));
        pendingHeading = null;
      }

      if (node.tagName === 'H2') {
        const key = columnKey(node.textContent);
        if (key) {
          pendingHeading = key;
          continue;
        }
      }
      extras[bucket].push(node.outerHTML);
    }
    if (pendingHeading) {
      extras[bucket].push(serializeHeading(pendingHeading));
    }

    return colNodes;
  }

  function columnKey(text) {
    const t = (text || '').trim().toLowerCase();
    if (t === 'now')   return 'now';
    if (t === 'soon')  return 'soon';
    if (t === 'later') return 'later';
    return null;
  }

  function nextBucketAfter(col) {
    if (col === 'now')   return 'beforeSoon';
    if (col === 'soon')  return 'beforeLater';
    if (col === 'later') return 'afterLater';
    return 'afterLater';
  }

  function serializeHeading(key) {
    return '<h2>' + LABELS[key] + '</h2>';
  }

  // ---- Rendering ---------------------------------------------------------

  function render(colNodes) {
    for (const col of COLUMNS) {
      const ul = lists[col];
      ul.innerHTML = '';
      const src = colNodes[col];
      if (!src) continue;
      for (const li of Array.from(src.children)) {
        if (li.tagName !== 'LI') continue;
        ul.appendChild(buildLi(li));
      }
    }
  }

  // Build a normalized <li> from a source <li>. We rebuild rather than clone
  // so we can guarantee a known structure (.task-row with checkbox + .task-text)
  // regardless of how the source was authored.
  function buildLi(src) {
    const li = document.createElement('li');
    li.draggable = true;

    const row = document.createElement('div');
    row.className = 'task-row';

    const cb = document.createElement('input');
    cb.type = 'checkbox';
    // Walk only the source li's *direct* children for the first checkbox; deeper
    // checkboxes belong to nested subtasks.
    const srcCb = firstDirectCheckbox(src);
    if (srcCb && srcCb.checked) cb.checked = true;

    const text = document.createElement('span');
    text.className = 'task-text';
    text.textContent = extractLiText(src);

    row.appendChild(cb);
    row.appendChild(text);
    li.appendChild(row);

    if (cb.checked) li.classList.add('checked');

    // Recurse into nested <ul>s.
    for (const child of Array.from(src.children)) {
      if (child.tagName === 'UL') {
        const sub = document.createElement('ul');
        for (const subLi of Array.from(child.children)) {
          if (subLi.tagName === 'LI') sub.appendChild(buildLi(subLi));
        }
        li.appendChild(sub);
      }
    }

    return li;
  }

  function firstDirectCheckbox(li) {
    for (const child of li.children) {
      if (child.tagName === 'INPUT' && child.type === 'checkbox') return child;
      // TipTap wraps the row in a <label> or similar; check one level deep.
      if (child.tagName !== 'UL') {
        const cb = child.querySelector(':scope > input[type="checkbox"], input[type="checkbox"]');
        if (cb) return cb;
      }
    }
    return null;
  }

  // Pull the visible task text out of the source li, skipping nested <ul>s
  // and the leading checkbox.
  function extractLiText(li) {
    const clone = li.cloneNode(true);
    for (const ul of Array.from(clone.querySelectorAll('ul'))) ul.remove();
    for (const cb of Array.from(clone.querySelectorAll('input[type="checkbox"]'))) cb.remove();
    return clone.textContent.replace(/\s+/g, ' ').trim();
  }

  // ---- Serializing -------------------------------------------------------

  function serialize() {
    let body = '<h1>' + escapeHtml(h1Text) + '</h1>';
    // afterH1 holds anything between <h1> and the first recognized column
    // heading (or anywhere if no columns were found). Other extras buckets
    // capture content sandwiched between columns.
    body += extras.afterH1.join('');
    body += renderColumnHtml('now')  + extras.beforeSoon.join('');
    body += renderColumnHtml('soon') + extras.beforeLater.join('');
    body += renderColumnHtml('later') + extras.afterLater.join('');

    return '<!DOCTYPE html><html><head><meta charset="utf-8"><title>' +
      escapeHtml(h1Text) + '</title></head><body><article>' +
      body + '</article></body></html>';
  }

  function renderColumnHtml(col) {
    let out = '<h2>' + LABELS[col] + '</h2><ul>';
    for (const li of Array.from(lists[col].children)) {
      out += serializeLi(li);
    }
    out += '</ul>';
    return out;
  }

  function serializeLi(li) {
    const cb = li.querySelector(':scope > .task-row > input[type="checkbox"]');
    const text = li.querySelector(':scope > .task-row > .task-text');
    const checked = cb && cb.checked ? ' checked' : '';
    let out = '<li><input type="checkbox"' + checked + '> ' + escapeHtml(text ? text.textContent : '');
    // Nested ul (direct child only).
    const nested = li.querySelector(':scope > ul');
    if (nested && nested.children.length) {
      out += '<ul>';
      for (const sub of Array.from(nested.children)) out += serializeLi(sub);
      out += '</ul>';
    }
    out += '</li>';
    return out;
  }

  function escapeHtml(s) {
    return (s || '').replace(/[&<>"']/g, c => ({
      '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
    }[c]));
  }

  // ---- Save (debounced, mirroring editor.js's pattern) -------------------

  let saveTimer = null;
  let inflight = false;
  let pending = false;

  function scheduleSave() {
    setStatus('saving', 'saving…');
    clearTimeout(saveTimer);
    saveTimer = setTimeout(save, 1000);
  }

  async function save() {
    if (inflight) { pending = true; return; }
    inflight = true;
    setStatus('saving', 'saving…');
    try {
      const res = await fetch('/api/note/' + PATH, {
        method: 'POST',
        headers: { 'Content-Type': 'text/html' },
        body: serialize(),
      });
      if (!res.ok) throw new Error('http ' + res.status);
      setStatus('saved', 'saved');
    } catch (e) {
      setStatus('offline', 'offline');
    } finally {
      inflight = false;
      if (pending) { pending = false; save(); }
    }
  }

  // ---- Checkbox toggling -------------------------------------------------

  boardEl.addEventListener('change', (e) => {
    const target = e.target;
    if (!(target instanceof HTMLInputElement)) return;
    if (target.type !== 'checkbox') return;
    const li = target.closest('li');
    if (!li) return;
    li.classList.toggle('checked', target.checked);
    scheduleSave();
  });

  // ---- Drag and drop -----------------------------------------------------
  // HTML5 DnD over pointer events: native API gives us proper drag images, OS
  // cursor feedback, and escape-to-cancel for free. Cross-column moves are
  // gated by depth — a nested li can only land back in its origin parent ul.

  let dragging = null;        // the <li> being dragged
  let dragOriginParent = null;
  let dragOriginIsNested = false;

  function isNestedLi(li) {
    // A top-level li sits directly inside .task-list (column UL).
    return !li.parentElement.classList.contains('task-list');
  }

  boardEl.addEventListener('dragstart', (e) => {
    const li = e.target.closest('li');
    if (!li) return;
    dragging = li;
    dragOriginParent = li.parentElement;
    dragOriginIsNested = isNestedLi(li);
    li.classList.add('dragging');
    // dataTransfer payload is required for Firefox to begin a drag, even if
    // we never read it back.
    try { e.dataTransfer.setData('text/plain', 'task'); } catch (err) {}
    e.dataTransfer.effectAllowed = 'move';
  });

  boardEl.addEventListener('dragend', () => {
    if (dragging) dragging.classList.remove('dragging');
    clearDropMarkers();
    dragging = null;
    dragOriginParent = null;
    dragOriginIsNested = false;
  });

  boardEl.addEventListener('dragover', (e) => {
    if (!dragging) return;
    const targetLi = e.target.closest('li');
    const targetUl = e.target.closest('ul.task-list, ul');

    // For nested drags, only allow drops inside the origin parent ul.
    if (dragOriginIsNested) {
      if (!targetUl || targetUl !== dragOriginParent) return;
    }

    e.preventDefault();
    e.dataTransfer.dropEffect = 'move';
    clearDropMarkers();

    if (targetLi && targetLi !== dragging && !dragging.contains(targetLi)) {
      // For nested drags, target li must share the origin parent.
      if (dragOriginIsNested && targetLi.parentElement !== dragOriginParent) return;
      const rect = targetLi.getBoundingClientRect();
      const before = (e.clientY - rect.top) < rect.height / 2;
      targetLi.classList.add(before ? 'drop-before' : 'drop-after');
    } else if (targetUl && targetUl.classList.contains('task-list')) {
      // Hovering past the last li — drop at end of column.
      if (!dragOriginIsNested) targetUl.classList.add('drop-end');
    }
  });

  boardEl.addEventListener('drop', (e) => {
    if (!dragging) return;
    e.preventDefault();
    const targetLi = e.target.closest('li');
    const targetUl = e.target.closest('ul.task-list, ul');

    let placed = false;
    if (targetLi && targetLi !== dragging && !dragging.contains(targetLi)) {
      if (dragOriginIsNested && targetLi.parentElement !== dragOriginParent) {
        // Drop refused.
      } else {
        const rect = targetLi.getBoundingClientRect();
        const before = (e.clientY - rect.top) < rect.height / 2;
        targetLi.parentElement.insertBefore(dragging, before ? targetLi : targetLi.nextSibling);
        placed = true;
      }
    } else if (targetUl && targetUl.classList.contains('task-list') && !dragOriginIsNested) {
      targetUl.appendChild(dragging);
      placed = true;
    }

    clearDropMarkers();
    if (placed) scheduleSave();
  });

  function clearDropMarkers() {
    for (const el of boardEl.querySelectorAll('.drop-before, .drop-after')) {
      el.classList.remove('drop-before', 'drop-after');
    }
    for (const el of boardEl.querySelectorAll('.drop-end')) {
      el.classList.remove('drop-end');
    }
  }

  // ---- Add task ----------------------------------------------------------

  for (const wrap of document.querySelectorAll('.add-task')) {
    const col = wrap.dataset.add;
    const btn = wrap.querySelector('.add-btn');
    const form = wrap.querySelector('.add-form');
    const input = wrap.querySelector('.add-input');

    btn.addEventListener('click', () => {
      form.hidden = false;
      btn.hidden = true;
      input.value = '';
      input.focus();
    });
    form.addEventListener('submit', (e) => {
      e.preventDefault();
      const text = input.value.trim();
      if (text) {
        addTask(col, text);
        scheduleSave();
      }
      form.hidden = true;
      btn.hidden = false;
    });
    input.addEventListener('blur', () => {
      const text = input.value.trim();
      if (text) {
        addTask(col, text);
        scheduleSave();
      }
      form.hidden = true;
      btn.hidden = false;
    });
    input.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') {
        input.value = '';
        form.hidden = true;
        btn.hidden = false;
      }
    });
  }

  function addTask(col, text) {
    const li = document.createElement('li');
    li.draggable = true;
    const row = document.createElement('div');
    row.className = 'task-row';
    const cb = document.createElement('input');
    cb.type = 'checkbox';
    const span = document.createElement('span');
    span.className = 'task-text';
    span.textContent = text;
    row.appendChild(cb);
    row.appendChild(span);
    li.appendChild(row);
    lists[col].appendChild(li);
  }

  // ---- Archive -----------------------------------------------------------

  archiveBtn.addEventListener('click', async () => {
    archiveErrorEl.hidden = true;
    archiveBtn.disabled = true;
    setStatus('saving', 'archiving…');
    try {
      const res = await fetch('/api/tasks/archive', { method: 'POST' });
      if (!res.ok) throw new Error('http ' + res.status);
      setStatus('saved', 'archived');
      await load();
    } catch (e) {
      archiveErrorEl.textContent = 'Archive failed: ' + e.message;
      archiveErrorEl.hidden = false;
      setStatus('offline', 'error');
    } finally {
      archiveBtn.disabled = false;
    }
  });

  // ---- Create (empty-state) ----------------------------------------------

  createBtn.addEventListener('click', async () => {
    setStatus('saving', 'creating…');
    try {
      const seed = '<!DOCTYPE html><html><head><meta charset="utf-8"><title>Tasks</title></head>' +
        '<body><article><h1>Tasks</h1>' +
        '<h2>Now</h2><ul></ul>' +
        '<h2>Soon</h2><ul></ul>' +
        '<h2>Later</h2><ul></ul>' +
        '</article></body></html>';
      const res = await fetch('/api/note/' + PATH, {
        method: 'POST',
        headers: { 'Content-Type': 'text/html' },
        body: seed,
      });
      if (!res.ok) throw new Error('http ' + res.status);
      setStatus('saved', 'saved');
      await load();
    } catch (e) {
      setStatus('offline', 'offline');
    }
  });

  // ---- Load --------------------------------------------------------------

  async function load() {
    setStatus('saving', 'loading…');
    try {
      const res = await fetch('/raw/' + PATH);
      if (res.status === 404) {
        boardEl.hidden = true;
        emptyEl.hidden = false;
        setStatus('', 'new');
        return;
      }
      if (!res.ok) throw new Error('http ' + res.status);
      const html = await res.text();
      const colNodes = parseDoc(html);
      render(colNodes);
      boardEl.hidden = false;
      emptyEl.hidden = true;
      setStatus('saved', 'loaded');
    } catch (e) {
      setStatus('offline', 'offline');
      boardEl.hidden = true;
      emptyEl.hidden = false;
    }
  }

  load();
})();
