import SwiftUI
import WeftKit

// The full-screen-capable main window: sidebar | reader | brain inspector, with
// a unified toolbar carrying inline capture, the ✦ surface inbox, and the brain
// toggle. A proactive push that lands while Weft is focused fades in as a toast
// over the reader.
struct RootView: View {
    @Environment(AppModel.self) private var model
    @State private var showBrain = true
    @State private var showInbox = false

    var body: some View {
        NavigationSplitView {
            SidebarView()
        } detail: {
            readerPane
                .inspector(isPresented: $showBrain) {
                    BrainPanel()
                        .inspectorColumnWidth(min: 240, ideal: 280, max: 360)
                }
                .toolbar { toolbar }
        }
        .navigationTitle(model.doc?.title ?? "Weft")
    }

    private var readerPane: some View {
        ZStack(alignment: .bottomTrailing) {
            Weft.bg.ignoresSafeArea()
            ReaderView(doc: model.doc) { path in
                model.open(path: path)
            }
            if let toast = model.toast {
                ToastView(
                    event: toast,
                    onOpen: { model.openFromInbox(toast); model.dismissToast() },
                    onDismiss: { model.dismissToast() }
                )
                .padding(20)
                .transition(.move(edge: .bottom).combined(with: .opacity))
            }
        }
        .animation(.easeInOut(duration: 0.2), value: model.toast)
    }

    @ToolbarContentBuilder
    private var toolbar: some ToolbarContent {
        ToolbarItem(placement: .principal) { CaptureField() }
        ToolbarItem(placement: .automatic) { surfaceButton }
        ToolbarItem(placement: .automatic) {
            Button { showBrain.toggle() } label: {
                Image(systemName: "sidebar.right")
            }
            .help("Toggle brain panel")
        }
    }

    private var surfaceButton: some View {
        Button { showInbox.toggle() } label: {
            ZStack(alignment: .topTrailing) {
                Image(systemName: "sparkle").font(.system(size: 13))
                if model.hasUnseen {
                    Circle().fill(Weft.accent)
                        .frame(width: 6, height: 6)
                        .offset(x: 5, y: -3)
                }
            }
        }
        .help("What Weft surfaced")
        .popover(isPresented: $showInbox, arrowEdge: .bottom) {
            SurfaceInboxView().environment(model)
        }
    }
}
