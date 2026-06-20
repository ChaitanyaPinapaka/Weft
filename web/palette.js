// Weft command palette — ⌘K / Ctrl+K. A keyboard-first switcher: fuzzy-jump to
// any note, run a quick command, or create a note. Shared by every surface
// (self-contained classic script; depends only on the daemon API). Navigation
// stays the job — surfacing remains the primary interface, this is the escape
// hatch power users reach for.

(() => {
  const COMMANDS = [
    { label: 'Today’s daily note', href: '/daily' },
    { label: 'All open tasks', href: '/web/tasks.html' },
    { label: 'All notes', href: '/notes' },
    { label: 'Graph', href: '/graph' },
  ];

  let overlay, input, list, notes = null, items = [], sel = 0, open = false;

  function titleFromPath(p) {
    return (p.split('/').pop() || p).replace(/\.html?$/i, '');
  }

  // Subsequence fuzzy match; returns a score (lower = better) or -1 for no match.
  // Empty query matches everything at score 0.
  function fuzzy(q, s) {
    q = q.toLowerCase();
    s = s.toLowerCase();
    if (!q) return 0;
    let qi = 0, score = 0, last = -1;
    for (let i = 0; i < s.length && qi < q.length; i++) {
      if (s[i] === q[qi]) {
        if (last >= 0) score += i - last; // reward contiguous runs
        last = i;
        qi++;
      }
    }
    return qi === q.length ? score : -1;
  }

  async function ensureNotes() {
    if (notes) return notes;
    try {
      const res = await fetch('/api/notes');
      notes = res.ok ? await res.json() : [];
    } catch (e) {
      notes = [];
    }
    return notes;
  }

  function computeItems(q) {
    const out = [];
    for (const c of COMMANDS) {
      if (fuzzy(q, c.label) >= 0) out.push({ kind: 'cmd', label: c.label, href: c.href });
    }
    const matched = [];
    for (const n of notes || []) {
      const label = titleFromPath(n.path);
      const sc = fuzzy(q, label + ' ' + n.path);
      if (sc >= 0) matched.push({ kind: 'note', path: n.path, label, score: sc });
    }
    matched.sort((a, b) => a.score - b.score);
    out.push(...matched.slice(0, 8));
    if (q.trim()) out.push({ kind: 'create', label: 'Create note: “' + q.trim() + '”' });
    return out;
  }

  function highlight() {
    [...list.children].forEach((li, i) => li.classList.toggle('is-sel', i === sel));
    const cur = list.children[sel];
    if (cur && cur.scrollIntoView) cur.scrollIntoView({ block: 'nearest' });
  }

  function render() {
    items = computeItems(input.value);
    sel = 0;
    list.innerHTML = '';
    items.forEach((it, i) => {
      const li = document.createElement('li');
      li.className = 'cmdk-item' + (i === sel ? ' is-sel' : '');
      li.setAttribute('role', 'option');
      if (it.kind === 'note') {
        const t = document.createElement('span');
        t.className = 'cmdk-item-title';
        t.textContent = it.label;
        const p = document.createElement('span');
        p.className = 'cmdk-item-path';
        p.textContent = it.path;
        li.appendChild(t);
        li.appendChild(p);
      } else {
        if (it.kind === 'create') {
          const plus = document.createElement('span');
          plus.className = 'cmdk-create';
          plus.textContent = '+';
          li.appendChild(plus);
        }
        const t = document.createElement('span');
        t.className = 'cmdk-item-title';
        t.textContent = it.label;
        li.appendChild(t);
      }
      li.addEventListener('mouseenter', () => { sel = i; highlight(); });
      li.addEventListener('click', () => activate(i));
      list.appendChild(li);
    });
  }

  async function activate(i) {
    const it = items[i];
    if (!it) return;
    if (it.kind === 'note') {
      location.href = '/note/' + it.path;
    } else if (it.kind === 'cmd') {
      location.href = it.href;
    } else {
      const title = input.value.trim();
      if (!title) return;
      try {
        const res = await fetch('/api/note/new?title=' + encodeURIComponent(title), { method: 'POST' });
        if (!res.ok) return;
        const data = await res.json();
        if (data && data.path) location.href = '/edit/' + data.path;
      } catch (e) { /* offline */ }
    }
  }

  function onKey(e) {
    if (e.key === 'ArrowDown') { e.preventDefault(); sel = Math.min(sel + 1, items.length - 1); highlight(); }
    else if (e.key === 'ArrowUp') { e.preventDefault(); sel = Math.max(sel - 1, 0); highlight(); }
    else if (e.key === 'Enter') { e.preventDefault(); activate(sel); }
    else if (e.key === 'Escape') { e.preventDefault(); hide(); }
  }

  function build() {
    overlay = document.createElement('div');
    overlay.className = 'cmdk-overlay';
    overlay.hidden = true;
    const box = document.createElement('div');
    box.className = 'cmdk';
    box.setAttribute('role', 'dialog');
    box.setAttribute('aria-label', 'Command palette');
    input = document.createElement('input');
    input.className = 'cmdk-input';
    input.type = 'text';
    input.placeholder = 'Jump to a note, run a command, or create…';
    input.setAttribute('aria-label', 'Command palette');
    input.autocomplete = 'off';
    input.spellcheck = false;
    list = document.createElement('ul');
    list.className = 'cmdk-list';
    list.setAttribute('role', 'listbox');
    box.appendChild(input);
    box.appendChild(list);
    overlay.appendChild(box);
    document.body.appendChild(overlay);
    overlay.addEventListener('mousedown', (e) => { if (e.target === overlay) hide(); });
    input.addEventListener('input', render);
    input.addEventListener('keydown', onKey);
  }

  async function show() {
    if (!overlay) build();
    await ensureNotes();
    overlay.hidden = false;
    open = true;
    input.value = '';
    render();
    input.focus();
  }
  function hide() {
    if (overlay) overlay.hidden = true;
    open = false;
  }

  document.addEventListener('keydown', (e) => {
    if ((e.metaKey || e.ctrlKey) && (e.key === 'k' || e.key === 'K')) {
      e.preventDefault();
      open ? hide() : show();
    }
  });
})();
