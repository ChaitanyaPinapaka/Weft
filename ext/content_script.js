// Weft content script.
// Hosts (a) brain-panel sidebar, (b) quick-capture overlay, (c) clip-success toast.
// Isolation strategy: Shadow DOM.
// Why Shadow DOM over iframe: an iframe inherits no page styles which is great,
// but it costs an extra document, fights with CSP frame-src on some sites, and
// makes keyboard focus harder to reason about. A closed shadow root gives us
// total style isolation (page CSS cannot leak in or out) without those costs.
// All sidebar/popup CSS is inlined below so we never depend on a stylesheet URL
// that a strict page CSP could block.

(() => {
  if (window.__weftInjected) return;
  window.__weftInjected = true;

  // After an extension reload, the previous content script's isolated world is
  // gone but its host element survives in the DOM as a dead husk. Replace it.
  document.getElementById("weft-host")?.remove();

  const host = document.createElement("div");
  host.id = "weft-host";
  // The host element itself is the only thing the page sees; nothing inside leaks.
  host.style.cssText = "all: initial; position: fixed; top: 0; right: 0; z-index: 2147483647;";
  const root = host.attachShadow({ mode: "closed" });

  const style = document.createElement("style");
  style.textContent = `
    :host, * { box-sizing: border-box; }
    .sidebar {
      position: fixed; top: 0; right: 0; height: 100vh; width: 360px;
      background: #fafaf7; color: #1a1a1a; font: 14px/1.4 -apple-system, system-ui, sans-serif;
      border-left: 1px solid #d8d4c8; box-shadow: -2px 0 12px rgba(0,0,0,0.06);
      transform: translateX(100%); transition: transform 160ms ease;
      display: flex; flex-direction: column;
    }
    .sidebar.open { transform: translateX(0); }
    .sidebar header {
      padding: 12px 14px; border-bottom: 1px solid #e6e2d6;
      display: flex; align-items: center; justify-content: space-between;
    }
    .sidebar header h1 { margin: 0; font-size: 13px; letter-spacing: 0.04em; text-transform: uppercase; color: #6a6657; }
    .sidebar header button {
      background: none; border: none; cursor: pointer; font-size: 16px; color: #6a6657;
    }
    .sidebar .query {
      padding: 10px 14px; font-size: 12px; color: #6a6657;
      border-bottom: 1px solid #efece2; word-break: break-word;
    }
    .results { overflow-y: auto; flex: 1; }
    .result {
      display: block; padding: 10px 14px; border-bottom: 1px solid #efece2;
      text-decoration: none; color: inherit;
    }
    .result:hover { background: #f3efe2; }
    .result .title { font-weight: 600; font-size: 13px; }
    .result .path { font-size: 11px; color: #908a78; margin-top: 2px; }
    .result .snippet { font-size: 12px; color: #4a4a45; margin-top: 4px; }
    .empty, .error { padding: 14px; color: #6a6657; font-size: 12px; }
    .error { color: #a3431f; }

    .toast {
      position: fixed; top: 16px; right: 16px;
      background: #1a1a1a; color: #fafaf7; padding: 10px 14px;
      border-radius: 6px; font: 13px -apple-system, system-ui, sans-serif;
      box-shadow: 0 4px 18px rgba(0,0,0,0.25);
      opacity: 0; transform: translateY(-6px); transition: opacity 140ms, transform 140ms;
      pointer-events: none;
    }
    .toast.show { opacity: 1; transform: translateY(0); }

    .capture-backdrop {
      position: fixed; inset: 0; background: rgba(20,20,20,0.35);
      display: none; align-items: flex-start; justify-content: center; padding-top: 18vh;
    }
    .capture-backdrop.open { display: flex; }
    .capture {
      background: #fafaf7; border-radius: 8px; padding: 14px; width: min(520px, 90vw);
      box-shadow: 0 12px 40px rgba(0,0,0,0.25);
      font: 14px -apple-system, system-ui, sans-serif;
    }
    .capture label { display: block; font-size: 11px; color: #6a6657; text-transform: uppercase; letter-spacing: 0.04em; margin-bottom: 6px; }
    .capture input {
      width: 100%; padding: 10px 12px; font: inherit; border: 1px solid #d8d4c8;
      border-radius: 6px; background: #fff; color: #1a1a1a; outline: none;
    }
    .capture input:focus { border-color: #8a8470; }
    .capture .hint { margin-top: 8px; font-size: 11px; color: #908a78; }
    .capture .err { margin-top: 8px; font-size: 12px; color: #a3431f; }
  `;
  root.appendChild(style);

  // --- Sidebar ---------------------------------------------------------
  const sidebar = document.createElement("aside");
  sidebar.className = "sidebar";
  sidebar.innerHTML = `
    <header>
      <h1>Weft brain</h1>
      <button class="close" title="Close (Cmd/Ctrl+Shift+B)">x</button>
    </header>
    <div class="query"></div>
    <div class="results"><div class="empty">Loading...</div></div>
  `;
  root.appendChild(sidebar);

  const queryEl = sidebar.querySelector(".query");
  const resultsEl = sidebar.querySelector(".results");
  sidebar.querySelector(".close").addEventListener("click", () => toggleSidebar(false));

  let sidebarLoaded = false;

  async function openSidebar() {
    sidebar.classList.add("open");
    chrome.storage?.local.set({ weftSidebarOpen: true }).catch(() => {});
    if (!sidebarLoaded) await loadResults();
  }
  function closeSidebar() {
    sidebar.classList.remove("open");
    chrome.storage?.local.set({ weftSidebarOpen: false }).catch(() => {});
  }
  function toggleSidebar(force) {
    const next = typeof force === "boolean" ? force : !sidebar.classList.contains("open");
    if (next) openSidebar(); else closeSidebar();
  }

  async function loadResults() {
    sidebarLoaded = true;
    const q = (document.title || "").trim();
    queryEl.textContent = q ? `Matches for: ${q}` : "No page title to query";
    if (!q) { resultsEl.innerHTML = `<div class="empty">No page title.</div>`; return; }
    try {
      const resp = await chrome.runtime.sendMessage({ type: "weft:search", q });
      if (!resp || !resp.ok) throw new Error(resp?.error || "search failed");
      const items = Array.isArray(resp.results) ? resp.results.slice(0, 10) : [];
      if (items.length === 0) { resultsEl.innerHTML = `<div class="empty">No related notes yet.</div>`; return; }
      resultsEl.innerHTML = "";
      for (const r of items) {
        const a = document.createElement("a");
        a.className = "result";
        a.href = `http://localhost:7777/note/${encodeURI(r.Path || "")}`;
        a.target = "_blank";
        a.rel = "noopener noreferrer";
        a.innerHTML = `
          <div class="title"></div>
          <div class="path"></div>
          <div class="snippet"></div>
        `;
        a.querySelector(".title").textContent = r.Title || r.Path || "(untitled)";
        a.querySelector(".path").textContent = r.Path || "";
        a.querySelector(".snippet").textContent = r.Snippet || "";
        resultsEl.appendChild(a);
      }
    } catch (err) {
      resultsEl.innerHTML = `<div class="error"></div>`;
      resultsEl.querySelector(".error").textContent = `Could not reach daemon: ${err.message || err}`;
    }
  }

  // Restore last-known sidebar state (per-tab persistence).
  chrome.storage?.local.get("weftSidebarOpen").then((v) => {
    if (v && v.weftSidebarOpen) openSidebar();
  }).catch(() => {});

  // --- Toast -----------------------------------------------------------
  const toast = document.createElement("div");
  toast.className = "toast";
  root.appendChild(toast);
  let toastTimer = 0;
  function showToast(text) {
    toast.textContent = text;
    toast.classList.add("show");
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => toast.classList.remove("show"), 1800);
  }

  // --- Quick-capture overlay ------------------------------------------
  const backdrop = document.createElement("div");
  backdrop.className = "capture-backdrop";
  backdrop.innerHTML = `
    <div class="capture">
      <label>Capture to today</label>
      <input type="text" placeholder="One line; Enter to save, Esc to cancel" autocomplete="off" />
      <div class="hint">Posts to /api/capture on localhost:7777</div>
      <div class="err" hidden></div>
    </div>
  `;
  root.appendChild(backdrop);

  const captureInput = backdrop.querySelector("input");
  const captureErr = backdrop.querySelector(".err");

  function openCapture() {
    captureErr.hidden = true;
    captureInput.value = "";
    backdrop.classList.add("open");
    // Focus after the transition so the page's focus handlers don't fight us.
    setTimeout(() => captureInput.focus(), 30);
  }
  function closeCapture() { backdrop.classList.remove("open"); }

  backdrop.addEventListener("click", (e) => { if (e.target === backdrop) closeCapture(); });
  captureInput.addEventListener("keydown", async (e) => {
    if (e.key === "Escape") { e.preventDefault(); closeCapture(); return; }
    if (e.key === "Enter") {
      e.preventDefault();
      const text = captureInput.value.trim();
      if (!text) return;
      captureErr.hidden = true;
      try {
        const resp = await chrome.runtime.sendMessage({ type: "weft:capture", text });
        if (!resp || !resp.ok) throw new Error(resp?.error || "capture failed");
        closeCapture();
        showToast("Captured");
      } catch (err) {
        captureErr.hidden = false;
        captureErr.textContent = String(err.message || err);
      }
    }
  });

  // --- Background message handlers ------------------------------------
  chrome.runtime.onMessage.addListener((msg) => {
    if (!msg || !msg.type) return;
    if (msg.type === "weft:toggle-sidebar") toggleSidebar();
    else if (msg.type === "weft:open-capture") openCapture();
    else if (msg.type === "weft:toast") showToast(msg.text || "");
  });

  document.documentElement.appendChild(host);
})();
