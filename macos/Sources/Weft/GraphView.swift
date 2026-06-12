import SwiftUI
import WebKit
import WeftKit

// The vault graph: a separate window hosting the daemon's /graph page (the D3
// force layout) in a WKWebView. A node's second click navigates the page to
// /note/{path}; we intercept that and open the note in the app's reader instead
// of letting the viewer load inside the webview.
struct GraphWindow: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        GraphWebView { path in
            model.open(path: path)
            raiseReaderWindow()
        }
        .navigationTitle("Graph")
        .background(Weft.bg.ignoresSafeArea())
        .frame(minWidth: 640, minHeight: 480)
    }

    // The note opens in the main window's reader; bring that window forward so
    // the navigation is visible (mirrors MenuBarView's "Open Weft").
    private func raiseReaderWindow() {
        NSApp.activate(ignoringOtherApps: true)
        NSApp.windows
            .first { $0.canBecomeMain && !($0 is NSPanel) && $0.title != "Graph" }?
            .makeKeyAndOrderFront(nil)
    }
}

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
