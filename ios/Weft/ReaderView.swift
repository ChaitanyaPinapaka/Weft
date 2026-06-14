import SwiftUI
import WebKit
import WeftKit

// iOS reading pane: a WKWebView rendering the note's <article> under the shared
// reader stylesheet (WeftKit.readerDocument). Notes are local, so relative links
// resolve against a weft://vault/<dir>/ baseURL; in-vault link taps are
// intercepted and routed back to the app, external links open in Safari.
struct ReaderView: UIViewRepresentable {
    let doc: ReaderDoc?
    let onNavigate: (String) -> Void

    func makeCoordinator() -> Coordinator { Coordinator(onNavigate: onNavigate) }

    func makeUIView(context: Context) -> WKWebView {
        // Static note rendering only — no scripting. Disabling JS neutralizes
        // any active content that arrived via sync from another device.
        let config = WKWebViewConfiguration()
        config.defaultWebpagePreferences.allowsContentJavaScript = false
        let web = WKWebView(frame: .zero, configuration: config)
        web.navigationDelegate = context.coordinator
        web.isOpaque = false
        web.backgroundColor = .clear
        web.scrollView.backgroundColor = .clear
        return web
    }

    func updateUIView(_ web: WKWebView, context: Context) {
        guard let doc, context.coordinator.loadedKey != doc.path else { return }
        context.coordinator.loadedKey = doc.path
        let dir = (doc.path as NSString).deletingLastPathComponent
        let base = URL(string: "weft://vault/" + (dir.isEmpty ? "" : dir + "/"))
        web.loadHTMLString(readerDocument(bodyHTML: doc.bodyHTML), baseURL: base)
    }

    final class Coordinator: NSObject, WKNavigationDelegate {
        var loadedKey: String?
        private let onNavigate: (String) -> Void
        init(onNavigate: @escaping (String) -> Void) { self.onNavigate = onNavigate }

        func webView(_ webView: WKWebView, decidePolicyFor action: WKNavigationAction,
                     decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
            guard action.navigationType == .linkActivated, let url = action.request.url else {
                decisionHandler(.allow) // initial loadHTMLString + in-page anchors
                return
            }
            if url.scheme == "weft", let path = vaultPath(from: url) {
                decisionHandler(.cancel)
                onNavigate(path)
            } else if url.scheme == "http" || url.scheme == "https" {
                decisionHandler(.cancel)
                UIApplication.shared.open(url)
            } else {
                decisionHandler(.cancel)
            }
        }

        // weft://vault/<path> → vault-relative .html path, or nil if not a note.
        private func vaultPath(from url: URL) -> String? {
            var p = url.path
            if p.hasPrefix("/") { p = String(p.dropFirst()) }
            return p.hasSuffix(".html") ? p : nil
        }
    }
}
