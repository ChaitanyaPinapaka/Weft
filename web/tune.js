// Tune — drag a weight, watch the ranking move. The whole point is the live
// loop: change a knob -> debounced POST /api/params -> re-fetch the preview
// surface -> re-render the ordered list. Plain script, no imports, no build.

(() => {
  'use strict';

  // Single spec drives both the DOM build and the change wiring. key matches
  // the API param exactly; min/max/step come from the documented ranges.
  const PARAMS = [
    { key: 'spread_scale',  label: 'spread_scale',  min: 1,    max: 12,  step: 0.1,
      hint: 'How hard association (backlinks/semantic/co-access) competes with recency.' },
    { key: 'decay',         label: 'decay',         min: 0.1,  max: 1.0, step: 0.01,
      hint: 'Forgetting-curve decay d in t^-d.' },
    { key: 'sem_threshold', label: 'sem_threshold', min: 0,    max: 1,   step: 0.01,
      hint: 'Cosine floor for a semantic edge.' },
    { key: 'co_saturation', label: 'co_saturation', min: 1,    max: 50,  step: 1,
      hint: 'Co-access count saturation point.' },
    { key: 'w_backlink',    label: 'w_backlink',    min: 0,    max: 1,   step: 0.01,
      hint: 'Association weight: backlinks.' },
    { key: 'w_semantic',    label: 'w_semantic',    min: 0,    max: 1,   step: 0.01,
      hint: 'Association weight: semantic neighbors.' },
    { key: 'w_coaccess',    label: 'w_coaccess',    min: 0,    max: 1,   step: 0.01,
      hint: 'Association weight: recently co-accessed.' },
    { key: 'noise_scale',   label: 'noise_scale',   min: 0,    max: 1,   step: 0.01,
      hint: 'Transient noise σ (0 = deterministic).' },
    { key: 'top_n',         label: 'top_n',         min: 1,    max: 30,  step: 1,
      hint: 'How many notes to return.' },
  ];
  const MAX_ROWS = 15;

  const statusEl   = document.getElementById('status');
  const knobListEl = document.getElementById('knob-list');
  const resetBtn   = document.getElementById('reset-btn');
  const selectEl   = document.getElementById('preview-select');
  const stateEl    = document.getElementById('preview-state');
  const scoredEl   = document.getElementById('scored');

  // key -> { range, num } so a programmatic populate (load/reset) can write
  // both paired controls without re-querying the DOM.
  const controls = new Map();

  function setStatus(text) { statusEl.textContent = text; }
  function setPreviewState(text) { stateEl.textContent = text; }

  // --- build the knob rows once ---
  for (const p of PARAMS) {
    const row = document.createElement('div');
    row.className = 'knob';

    const label = document.createElement('span');
    label.className = 'knob-label';
    label.textContent = p.label;

    const num = document.createElement('input');
    num.type = 'number';
    num.className = 'knob-num';
    num.min = p.min; num.max = p.max; num.step = p.step;

    const range = document.createElement('input');
    range.type = 'range';
    range.className = 'knob-range';
    range.min = p.min; range.max = p.max; range.step = p.step;

    const hint = document.createElement('span');
    hint.className = 'knob-hint';
    hint.textContent = p.hint;

    // Mirror the two controls, then push the single changed key.
    range.addEventListener('input', () => { num.value = range.value; queueChange(p, range.value); });
    num.addEventListener('input',   () => { range.value = num.value; queueChange(p, num.value); });

    row.append(label, num, range, hint);
    knobListEl.appendChild(row);
    controls.set(p.key, { range, num, spec: p });
  }

  // Write all controls from a full params object (load + reset paths).
  function populate(params) {
    for (const p of PARAMS) {
      const c = controls.get(p.key);
      if (!c || params[p.key] === undefined) continue;
      c.range.value = params[p.key];
      c.num.value = params[p.key];
    }
  }

  // --- debounced persistence ---
  // Coalesce rapid slider input into one POST, accumulating every key that
  // moved within the window so dragging two knobs fast still saves both.
  let pending = {};
  let timer = null;
  function queueChange(spec, raw) {
    const v = spec.step >= 1 ? parseInt(raw, 10) : parseFloat(raw);
    if (Number.isNaN(v)) return;
    pending[spec.key] = v;
    if (timer) clearTimeout(timer);
    timer = setTimeout(flush, 250);
  }

  async function flush() {
    timer = null;
    const body = pending;
    pending = {};
    if (!Object.keys(body).length) return;
    setStatus('saving…');
    try {
      const res = await fetch('/api/params', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
      if (!res.ok) throw new Error('http ' + res.status);
      setStatus('saved');
      loadPreview();
    } catch (e) {
      setStatus('offline');
    }
  }

  // --- right column: ranked preview ---
  function titleFromPath(path) {
    const base = (path || '').split('/').pop().replace(/\.html$/i, '');
    return base || path || '';
  }

  function renderScored(items) {
    scoredEl.replaceChildren();
    const rows = (items || []).slice(0, MAX_ROWS);
    if (!rows.length) {
      const empty = document.createElement('li');
      empty.className = 'scored-empty';
      empty.textContent = 'nothing surfaced';
      scoredEl.appendChild(empty);
      return;
    }
    for (const it of rows) {
      const li = document.createElement('li');
      li.className = 'scored-item';

      const top = document.createElement('div');
      top.className = 'scored-top';
      const title = document.createElement('span');
      title.className = 'scored-title';
      title.textContent = it.Title || titleFromPath(it.Path);
      const act = document.createElement('span');
      act.className = 'scored-act';
      act.textContent = (it.Activation || 0).toFixed(2);
      top.append(title, act);

      const brk = document.createElement('div');
      brk.className = 'scored-break';
      brk.textContent = 'base ' + (it.Base || 0).toFixed(2) + ' · spread ' + (it.Spread || 0).toFixed(2);

      li.append(top, brk);

      if (Array.isArray(it.Reasons) && it.Reasons.length) {
        const reasons = document.createElement('div');
        reasons.className = 'scored-reasons';
        for (const r of it.Reasons) {
          const chip = document.createElement('span');
          chip.className = 'reason-chip';
          chip.textContent = r;
          reasons.appendChild(chip);
        }
        li.appendChild(reasons);
      }
      scoredEl.appendChild(li);
    }
  }

  let previewSeq = 0; // guards against out-of-order responses while dragging
  async function loadPreview() {
    const path = selectEl.value;
    if (!path) { renderScored([]); setPreviewState('—'); return; }
    const seq = ++previewSeq;
    setPreviewState('updating…');
    try {
      const res = await fetch('/api/surface/' + path);
      if (!res.ok) throw new Error('http ' + res.status);
      const data = await res.json();
      if (seq !== previewSeq) return; // a newer request already won
      renderScored(data.scored);
      setPreviewState('');
    } catch (e) {
      if (seq !== previewSeq) return;
      renderScored([]);
      setPreviewState('—');
    }
  }

  selectEl.addEventListener('change', loadPreview);

  resetBtn.addEventListener('click', async () => {
    setStatus('resetting…');
    try {
      const res = await fetch('/api/params', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ reset: true }),
      });
      if (!res.ok) throw new Error('http ' + res.status);
      const params = await res.json();
      populate(params);
      setStatus('reset');
      loadPreview();
    } catch (e) {
      setStatus('offline');
    }
  });

  // --- bootstrap ---
  async function loadParams() {
    try {
      const res = await fetch('/api/params');
      if (!res.ok) throw new Error('http ' + res.status);
      populate(await res.json());
      setStatus('ready');
    } catch (e) {
      setStatus('offline');
    }
  }

  async function loadNotes() {
    try {
      const res = await fetch('/api/notes');
      if (!res.ok) throw new Error('http ' + res.status);
      const notes = await res.json();
      selectEl.replaceChildren();
      for (const n of notes || []) {
        const opt = document.createElement('option');
        opt.value = n.Path;
        opt.textContent = n.Name || n.Path;
        selectEl.appendChild(opt);
      }
      // Prefer welcome.html as the default cue if it exists.
      const welcome = (notes || []).find(n => /(^|\/)welcome\.html$/i.test(n.Path));
      if (welcome) selectEl.value = welcome.Path;
    } catch (e) {
      // Leave the picker empty; preview stays a dash. No console spam.
    }
  }

  // Params + notes are independent; load both, then fetch the first preview.
  Promise.all([loadParams(), loadNotes()]).then(loadPreview);
})();
