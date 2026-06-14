import SwiftUI
import WebKit
import WeftKit

// The vault graph, hosted in the main window's detail pane (RootView swaps it in
// for the reader when model.showGraph is true). A WKWebView renders the daemon's
// /web/graph.html — the D3 force layout. A node's click navigates the page to
// /note/{path}; we intercept that and call model.open(path:), which opens the
// note and clears showGraph, dropping you onto that note in the same reader.
//
// This is a distinct view instance from the reader's WKWebView, so the two never
// fight over a shared web view. The graph reloads fresh each time it appears, so
// it always reflects the daemon's latest (enriched) payload.
struct GraphWebView: NSViewRepresentable {
    let onOpen: (String) -> Void

    func makeCoordinator() -> Coordinator { Coordinator(onOpen: onOpen) }

    func makeNSView(context: Context) -> WKWebView {
        let web = WKWebView(frame: .zero, configuration: WKWebViewConfiguration())
        web.navigationDelegate = context.coordinator
        web.allowsBackForwardNavigationGestures = false
        web.load(URLRequest(url: WeftClient.base.appending(path: "graph")))
        return web
    }

    func updateNSView(_ web: WKWebView, context: Context) {}

    final class Coordinator: NSObject, WKNavigationDelegate {
        private let onOpen: (String) -> Void

        init(onOpen: @escaping (String) -> Void) { self.onOpen = onOpen }

        func webView(_ webView: WKWebView,
                     decidePolicyFor action: WKNavigationAction,
                     decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
            guard let url = action.request.url else {
                decisionHandler(.allow)
                return
            }
            // Node clicks navigate via window.location (type .other, not
            // .linkActivated), so match on the URL itself. The initial /graph
            // load and its 302 to /web/graph.html both fall through: neither
            // resolves to a vault path.
            if let notePath = weftVaultPath(from: url) {
                decisionHandler(.cancel)
                onOpen(notePath)
            } else if action.navigationType == .linkActivated, !isWeftDaemon(url) {
                decisionHandler(.cancel)
                NSWorkspace.shared.open(url)
            } else {
                decisionHandler(.allow)
            }
        }
    }
}
