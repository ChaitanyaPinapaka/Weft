import SwiftUI
import WebKit
import WeftKit

// The reading pane: a WKWebView that renders just the note's <article> body
// under the reader stylesheet. The native chrome (sidebar, brain, toolbar) lives
// outside it. Link clicks are intercepted — in-vault links navigate inside the
// app; everything else opens in the system browser.
//
// Edit mode swaps the same web view to the daemon's editor page (/edit/{path}).
// The editor owns the whole save flow (1s autosave, Cmd-S flush, beforeunload
// beacon); the app only hosts it.
struct ReaderView: NSViewRepresentable {
    // Render purely from the doc, which is self-consistent (its path and body
    // come from the same fetch). The current path lags the fetch, so keying on
    // it would load stale content; keying on doc.path never does.
    let doc: ReaderDoc?
    let editing: Bool
    var onWebView: (WKWebView) -> Void = { _ in }
    let onNavigate: (String) -> Void

    func makeCoordinator() -> Coordinator { Coordinator(onNavigate: onNavigate) }

    func makeNSView(context: Context) -> WKWebView {
        let web = WKWebView(frame: .zero, configuration: WKWebViewConfiguration())
        web.navigationDelegate = context.coordinator
        web.allowsBackForwardNavigationGestures = false
        // Hand the model a reference so it can drive the editor's save/stop
        // bridges (WKWebView doesn't fire beforeunload on programmatic nav).
        onWebView(web)
        return web
    }

    func updateNSView(_ web: WKWebView, context: Context) {
        context.coordinator.editing = editing
        guard let doc else { return }
        // The read key hashes the body so the post-edit refresh (same path, new
        // content) re-renders; the edit key deliberately doesn't — while editing,
        // the editor page owns the content and must never be reloaded under the
        // user's cursor.
        let key = editing ? "edit:\(doc.path)" : "read:\(doc.path):\(doc.bodyHTML.hashValue)"
        guard context.coordinator.loadedKey != key else { return }
        context.coordinator.loadedKey = key
        if editing {
            web.load(URLRequest(url: WeftClient.base.appending(path: "edit").appending(path: doc.path)))
        } else {
            // baseURL points at the note's directory under /raw/ so relative
            // anchors resolve to a localhost URL the coordinator can intercept.
            let base = WeftClient.base
                .appending(path: "raw")
                .appending(path: (doc.path as NSString).deletingLastPathComponent)
            web.loadHTMLString(readerDocument(bodyHTML: doc.bodyHTML), baseURL: base)
        }
    }

    final class Coordinator: NSObject, WKNavigationDelegate {
        var loadedKey: String?
        var editing = false
        private let onNavigate: (String) -> Void

        init(onNavigate: @escaping (String) -> Void) { self.onNavigate = onNavigate }

        func webView(_ webView: WKWebView,
                     decidePolicyFor action: WKNavigationAction,
                     decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
            guard action.navigationType == .linkActivated, let url = action.request.url else {
                // The initial loadHTMLString / programmatic /edit/ load, the
                // editor's 302 + asset loads, and in-page anchors.
                decisionHandler(.allow)
                return
            }
            if let notePath = weftVaultPath(from: url) {
                decisionHandler(.cancel)
                onNavigate(notePath)
            } else if editing, isWeftDaemon(url) {
                // Editor-internal navigation (home, /notes, …) stays hosted; a
                // note click on any of those pages is caught by the branch above.
                decisionHandler(.allow)
            } else {
                decisionHandler(.cancel)
                NSWorkspace.shared.open(url)
            }
        }
    }
}

/// A clicked-or-scripted URL → vault-relative .html path, or nil if it isn't a
/// note on the local daemon. Shared by the reader/editor and graph web views.
func weftVaultPath(from url: URL) -> String? {
    guard isWeftDaemon(url) else { return nil }
    var p = url.path
    // Editor pages name their note in the query (/web/index.html?path={note});
    // a /web/ URL without one is daemon UI, not a note.
    if p.hasPrefix("/web/") {
        let q = URLComponents(url: url, resolvingAgainstBaseURL: false)?
            .queryItems?.first { $0.name == "path" }?.value
        return (q?.hasSuffix(".html") == true) ? q : nil
    }
    for prefix in ["/raw/", "/note/", "/edit/"] where p.hasPrefix(prefix) {
        p = String(p.dropFirst(prefix.count))
        break
    }
    if p.hasPrefix("/") { p = String(p.dropFirst()) }
    guard p.hasSuffix(".html") else { return nil }
    // url.path is already percent-decoded by Foundation, and the client
    // re-encodes via URL.appending(path:); a second decode here would
    // corrupt any filename containing a literal '%'.
    return p
}

func isWeftDaemon(_ url: URL) -> Bool {
    url.host == "localhost" || url.host == "127.0.0.1"
}
