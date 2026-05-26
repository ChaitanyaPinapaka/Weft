// Weft background service worker.
// Owns: toolbar-click clip, command routing, daemon HTTP calls.
// The service worker is event-driven; no long-lived state.

const DAEMON = "http://localhost:7777";

// Toolbar icon click -> capture active tab's HTML and POST to /api/clip.
// activeTab grants temporary host access on user gesture, so no <all_urls> needed for the clipper.
chrome.action.onClicked.addListener(async (tab) => {
  if (!tab || !tab.id) return;
  try {
    const [{ result } = {}] = await chrome.scripting.executeScript({
      target: { tabId: tab.id },
      func: () => ({
        url: location.href,
        title: document.title,
        html: document.documentElement.outerHTML,
      }),
    });
    if (!result) throw new Error("could not read page");

    const resp = await fetch(`${DAEMON}/api/clip`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(result),
    });
    if (!resp.ok) throw new Error(`daemon ${resp.status}`);
    const data = await resp.json().catch(() => ({}));

    notify("Clipped to Weft", data.path || result.title);
    // Also try to show an in-page toast (best-effort; ignored if injection fails).
    chrome.tabs.sendMessage(tab.id, { type: "weft:toast", text: "Clipped to Weft" }).catch(() => {});
  } catch (err) {
    notify("Weft clip failed", String(err));
  }
});

// Keyboard commands.
chrome.commands.onCommand.addListener(async (command) => {
  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
  if (!tab || !tab.id) return;

  if (command === "quick-capture") {
    // Ask the content script to surface the capture overlay; it talks back via message.
    chrome.tabs.sendMessage(tab.id, { type: "weft:open-capture" }).catch(() => {});
  } else if (command === "toggle-sidebar") {
    chrome.tabs.sendMessage(tab.id, { type: "weft:toggle-sidebar" }).catch(() => {});
  }
});

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
      iconUrl: "icons/icon.png",
      title,
      message: message || "",
    });
  } catch (_) {}
}
