// Weft background service worker.
// Owns: toolbar-click clip, command routing, daemon HTTP calls.
// The service worker is event-driven; no long-lived state.

const DAEMON = "http://localhost:7777";

// Toolbar icon click -> distill active tab and POST to /api/clip.
// activeTab grants temporary host access on user gesture, so no <all_urls> needed for the clipper.
chrome.action.onClicked.addListener((tab) => clipPage(tab));

// Context menus: clip just the selection, or the whole page.
chrome.runtime.onInstalled.addListener(() => {
  chrome.contextMenus.create({ id: "clip-selection", title: "Clip selection to Weft", contexts: ["selection"] });
  chrome.contextMenus.create({ id: "clip-page", title: "Clip page to Weft", contexts: ["page"] });
});
chrome.contextMenus.onClicked.addListener((info, tab) => {
  if (info.menuItemId === "clip-selection") clipSelection(tab);
  else if (info.menuItemId === "clip-page") clipPage(tab);
});

// Runs inside the page. Distill the article with Readability when the page
// looks like an article; otherwise fall back to the full DOM. The daemon
// sanitizes either way (scripts, event handlers); our job is only to drop the
// chrome — nav, ads, banners — that would pollute FTS and embeddings.
function extractPage() {
  const fallback = {
    url: location.href,
    title: document.title,
    html: document.documentElement.outerHTML,
  };
  try {
    if (typeof Readability !== "function") return fallback;
    // Readability mutates its input, so hand it a clone.
    const article = new Readability(document.cloneNode(true)).parse();
    // Short extractions usually mean the page isn't an article (dashboards,
    // index pages); keep the full DOM rather than a misleading sliver.
    if (!article || !article.content || (article.textContent || "").trim().length < 250) return fallback;
    const esc = (s) => String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
    const title = article.title || document.title;
    return {
      url: location.href,
      title,
      html:
        `<!doctype html><html><head><meta charset="utf-8"><title>${esc(title)}</title></head><body>` +
        `<h1>${esc(title)}</h1>` +
        (article.byline ? `<p><em>${esc(article.byline)}</em></p>` : "") +
        article.content +
        `</body></html>`,
    };
  } catch (_) {
    return fallback;
  }
}

// Runs inside the page. Serialize the live selection; relative URLs inside it
// are fine because the daemon injects <base href> pointing at the source.
function extractSelection() {
  const sel = window.getSelection();
  if (!sel || sel.rangeCount === 0 || sel.isCollapsed) return null;
  const div = document.createElement("div");
  for (let i = 0; i < sel.rangeCount; i++) div.appendChild(sel.getRangeAt(i).cloneContents());
  const esc = (s) => String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  return {
    url: location.href,
    title: document.title,
    html:
      `<!doctype html><html><head><meta charset="utf-8"><title>${esc(document.title)}</title></head><body>` +
      `<h1>${esc(document.title)}</h1>` +
      `<blockquote>${div.innerHTML}</blockquote>` +
      `<p><a href="${esc(location.href)}">Source</a></p>` +
      `</body></html>`,
  };
}

async function clipPage(tab) {
  if (!tab || !tab.id) return;
  try {
    // Load Readability into the isolated world first; extractPage falls back
    // to the raw DOM if this failed (e.g. page blocked the second injection).
    await chrome.scripting
      .executeScript({ target: { tabId: tab.id }, files: ["vendor/readability.js"] })
      .catch(() => {});
    const [{ result } = {}] = await chrome.scripting.executeScript({
      target: { tabId: tab.id },
      func: extractPage,
    });
    if (!result) throw new Error("could not read page");
    await postClip(result, tab.id);
  } catch (err) {
    notify("Weft clip failed", String(err));
  }
}

async function clipSelection(tab) {
  if (!tab || !tab.id) return;
  try {
    const [{ result } = {}] = await chrome.scripting.executeScript({
      target: { tabId: tab.id },
      func: extractSelection,
    });
    if (!result) throw new Error("nothing selected");
    await postClip(result, tab.id);
  } catch (err) {
    notify("Weft clip failed", String(err));
  }
}

async function postClip(payload, tabId) {
  const resp = await fetch(`${DAEMON}/api/clip`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  });
  if (!resp.ok) throw new Error(`daemon ${resp.status}`);
  const data = await resp.json().catch(() => ({}));

  notify("Clipped to Weft", data.path || payload.title);
  // Also try to show an in-page toast (best-effort; ignored if injection fails).
  chrome.tabs.sendMessage(tabId, { type: "weft:toast", text: "Clipped to Weft" }).catch(() => {});
}

// Keyboard commands.
chrome.commands.onCommand.addListener(async (command) => {
  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
  if (!tab || !tab.id) return;

  if (command === "quick-capture") {
    // Ask the content script to surface the capture overlay; it talks back via message.
    sendToContent(tab.id, { type: "weft:open-capture" });
  } else if (command === "toggle-sidebar") {
    sendToContent(tab.id, { type: "weft:toggle-sidebar" });
  }
});

// Content scripts only auto-inject at page load, so tabs opened before the
// extension was installed (or reloaded) don't have one. On first failure,
// inject it and retry — the command keypress grants activeTab. chrome:// and
// Web Store pages refuse injection; surface that instead of failing silently.
async function sendToContent(tabId, msg) {
  try {
    await chrome.tabs.sendMessage(tabId, msg);
  } catch (_) {
    try {
      await chrome.scripting.executeScript({ target: { tabId }, files: ["content_script.js"] });
      await chrome.tabs.sendMessage(tabId, msg);
    } catch (err) {
      notify("Weft", "Can't run on this page (browser-internal pages are off-limits).");
    }
  }
}

// Content script -> background bridge for daemon POSTs.
// Doing fetches here avoids CORS quirks on file:// or sandboxed pages.
chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
  if (msg && msg.type === "weft:capture") {
    fetch(`${DAEMON}/api/capture`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ text: msg.text }),
    })
      .then(async (r) => {
        if (!r.ok) throw new Error(`daemon ${r.status}`);
        return r.json().catch(() => ({}));
      })
      .then((data) => sendResponse({ ok: true, path: data.path }))
      .catch((err) => sendResponse({ ok: false, error: String(err) }));
    return true; // async
  }
  if (msg && msg.type === "weft:search") {
    const u = new URL(`${DAEMON}/api/search`);
    u.searchParams.set("q", msg.q || "");
    fetch(u.toString())
      .then(async (r) => {
        if (!r.ok) throw new Error(`daemon ${r.status}`);
        return r.json();
      })
      .then((results) => sendResponse({ ok: true, results }))
      .catch((err) => sendResponse({ ok: false, error: String(err) }));
    return true;
  }
});

function notify(title, message) {
  // notifications permission is declared; failure is non-fatal.
  try {
    chrome.notifications.create({
      type: "basic",
      iconUrl: "icons/icon-128.png",
      title,
      message: message || "",
    });
  } catch (_) {}
}
