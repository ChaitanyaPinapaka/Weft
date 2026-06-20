// Weft open-tasks view — every unchecked task across the vault, grouped by note.
// Read-only: each task links to its note, where you check it off. The data comes
// from GET /api/tasks (server-computed, only unchecked, ordered).

(() => {
  const tasksEl  = document.getElementById('tasks');
  const statusEl = document.getElementById('status');
  const countEl  = document.getElementById('count');

  function titleFromPath(p) {
    return (p.split('/').pop() || p).replace(/\.html?$/i, '');
  }

  async function load() {
    let tasks;
    try {
      const res = await fetch('/api/tasks');
      if (!res.ok) throw new Error('http ' + res.status);
      tasks = await res.json();
    } catch (e) {
      statusEl.textContent = 'could not load tasks';
      return;
    }
    if (!tasks || tasks.length === 0) {
      statusEl.textContent = 'No open tasks. 🎉';
      return;
    }
    statusEl.hidden = true;
    countEl.textContent = '· ' + tasks.length;

    // Group by note path, preserving the server's order within each note.
    const groups = new Map();
    for (const t of tasks) {
      if (!groups.has(t.path)) {
        groups.set(t.path, { title: t.note_title || titleFromPath(t.path), items: [] });
      }
      groups.get(t.path).items.push(t);
    }

    const frag = document.createDocumentFragment();
    for (const [path, g] of groups) {
      const wrap = document.createElement('div');
      wrap.className = 'tasks-note';

      const h = document.createElement('div');
      h.className = 'tasks-note-title';
      const a = document.createElement('a');
      a.href = '/note/' + path; // matches the unencoded convention used elsewhere
      a.textContent = g.title;
      h.appendChild(a);
      wrap.appendChild(h);

      const ul = document.createElement('ul');
      ul.className = 'tasks-list';
      for (const t of g.items) {
        const li = document.createElement('li');
        li.textContent = t.text || '(untitled task)';
        ul.appendChild(li);
      }
      wrap.appendChild(ul);
      frag.appendChild(wrap);
    }
    tasksEl.appendChild(frag);
  }

  load();
})();
