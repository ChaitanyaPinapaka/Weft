import SwiftUI
import WebKit
import WeftKit

// The cross-vault open-tasks list, hosted in a toolbar popover. A WKWebView
// renders the daemon's /web/tasks.html (the same page the web app serves);
// clicking a task navigates to /note/{path}, which we intercept and hand to
// onOpen — opening the note and closing the popover. This reuses the web surface
// rather than reimplementing the list natively, the same pattern as GraphWebView.
struct TasksWebView: NSViewRepresentable {
    let onOpen: (String) -> Void

    func makeCoordinator() -> Coordinator { Coordinator(onOpen: onOpen) }

    func makeNSView(context: Context) -> WKWebView {
        let web = WKWebView(frame: .zero, configuration: WKWebViewConfiguration())
        web.navigationDelegate = context.coordinator
        web.allowsBackForwardNavigationGestures = false
        web.load(URLRequest(url: WeftClient.base.appending(path: "web/tasks.html")))
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
            // A task row links to /note/{path}; intercept and open it natively.
            // The initial /web/tasks.html load falls through (not a vault path).
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
