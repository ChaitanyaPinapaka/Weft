import SwiftUI
import WebKit
import WeftKit

// The reading pane: a WKWebView that renders just the note's <article> body
// under the reader stylesheet. The native chrome (sidebar, brain, toolbar) lives
// outside it. Link clicks are intercepted — in-vault links navigate inside the
// app; everything else opens in the system browser.
struct ReaderView: NSViewRepresentable {
    // Render purely from the doc, which is self-consistent (its path and body
    // come from the same fetch). The current path lags the fetch, so keying on
    // it would load stale content; keying on doc.path never does.
    let doc: ReaderDoc?
    let onNavigate: (String) -> Void

    func makeCoordinator() -> Coordinator { Coordinator(onNavigate: onNavigate) }

    func makeNSView(context: Context) -> WKWebView {
        let web = WKWebView(frame: .zero, configuration: WKWebViewConfiguration())
        web.navigationDelegate = context.coordinator
        web.allowsBackForwardNavigationGestures = false
        return web
    }

    func updateNSView(_ web: WKWebView, context: Context) {
        guard let doc, context.coordinator.loadedKey != doc.path else { return }
        context.coordinator.loadedKey = doc.path
        // baseURL points at the note's directory under /raw/ so relative anchors
        // resolve to a localhost URL the coordinator can intercept.
        let base = WeftClient.base
            .appending(path: "raw")
            .appending(path: (doc.path as NSString).deletingLastPathComponent)
        web.loadHTMLString(readerDocument(bodyHTML: doc.bodyHTML), baseURL: base)
    }

    final class Coordinator: NSObject, WKNavigationDelegate {
        var loadedKey: String?
        private let onNavigate: (String) -> Void

        init(onNavigate: @escaping (String) -> Void) { self.onNavigate = onNavigate }

        func webView(_ webView: WKWebView,
                     decidePolicyFor action: WKNavigationAction,
                     decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
            guard action.navigationType == .linkActivated, let url = action.request.url else {
                decisionHandler(.allow) // the initial loadHTMLString and in-page anchors
                return
            }
            if let notePath = vaultPath(from: url) {
                decisionHandler(.cancel)
                onNavigate(notePath)
            } else {
                decisionHandler(.cancel)
                NSWorkspace.shared.open(url)
            }
        }

        // A clicked URL → vault-relative .html path, or nil if it's external.
        private func vaultPath(from url: URL) -> String? {
            guard url.host == "localhost" || url.host == "127.0.0.1" else { return nil }
            var p = url.path
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
    }
}
