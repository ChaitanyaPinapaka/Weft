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
    @State private var showTasks = false
    // Bumped each time the graph is shown so its WKWebView is rebuilt and reloads
    // /web/graph.html fresh — always reflecting the daemon's latest payload.
    @State private var graphReload = 0

    var body: some View {
        @Bindable var model = model
        NavigationSplitView {
            SidebarView()
        } detail: {
            readerPane
                .inspector(isPresented: $showBrain) {
                    BrainPanel(
                        surface: model.surface,
                        titleFor: { model.titleFor($0) },
                        onOpen: { model.followSurfaced(path: $0) }
                    )
                    .inspectorColumnWidth(min: 240, ideal: 280, max: 360)
                }
                .toolbar { toolbar }
        }
        .navigationTitle(model.doc?.title ?? "Weft")
        // Bump on every show (toolbar button or ⌘⇧G) so the graph WKWebView is
        // rebuilt and reloads /web/graph.html fresh.
        .onChange(of: model.showGraph) { _, shown in if shown { graphReload &+= 1 } }
        .sheet(isPresented: $model.showSetup) { SetupView().environment(model) }
        .sheet(isPresented: $model.showAddDevice) { AddDeviceView() }
        // Trash confirmation — same never-delete wording as the web viewer.
        .alert(
            Text("Remove \u{201C}\(model.pendingTrash ?? "")\u{201D}?"),
            isPresented: trashAlertShown,
            presenting: model.pendingTrash
        ) { path in
            Button("Move to .trash/", role: .destructive) { model.trash(path: path) }
            Button("Cancel", role: .cancel) {}
        } message: { _ in
            Text("The file moves to .trash/ in your vault — nothing is deleted — but it leaves listings, search, and surfacing.")
        }
        // Surfaced failures (e.g. a trash that couldn't reach the daemon).
        .alert(
            "Something went wrong",
            isPresented: errorAlertShown,
            presenting: model.errorMessage
        ) { _ in
            Button("OK", role: .cancel) {}
        } message: { msg in
            Text(msg)
        }
    }

    private var errorAlertShown: Binding<Bool> {
        Binding(
            get: { model.errorMessage != nil },
            set: { if !$0 { model.errorMessage = nil } }
        )
    }

    // presenting: hands the path to the buttons, so dismissal clearing
    // pendingTrash can never race the destructive action.
    private var trashAlertShown: Binding<Bool> {
        Binding(
            get: { model.pendingTrash != nil },
            set: { if !$0 { model.pendingTrash = nil } }
        )
    }

    private var readerPane: some View {
        ZStack(alignment: .bottomTrailing) {
            Weft.bg.ignoresSafeArea()
            if model.showGraph {
                // The graph is a distinct WKWebView from the reader's, so the two
                // never fight. .id forces a rebuild (fresh reload) on each show.
                GraphWebView { path in model.open(path: path) }
                    .id(graphReload)
            } else {
                ReaderView(doc: model.doc, editing: model.editing,
                           onWebView: { model.activeWebView = $0 }) { path in
                    model.open(path: path)
                }
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
        ToolbarItem(placement: .automatic) { editButton }
        ToolbarItem(placement: .automatic) { trashButton }
        ToolbarItem(placement: .automatic) { graphButton }
        ToolbarItem(placement: .automatic) { tasksButton }
        ToolbarItem(placement: .automatic) { surfaceButton }
        ToolbarItem(placement: .automatic) {
            Button { showBrain.toggle() } label: {
                Image(systemName: "sidebar.right")
            }
            .help("Toggle brain panel")
        }
    }

    private var editButton: some View {
        Button {
            if model.editing { Task { await model.finishEditing() } } else { model.beginEditing() }
        } label: {
            Image(systemName: model.editing ? "checkmark.circle" : "pencil")
        }
        .help(model.editing ? "Done — back to reading" : "Edit this note")
        .disabled(model.doc == nil)
    }

    private var trashButton: some View {
        Button {
            if let path = model.currentPath { model.requestTrash(path) }
        } label: {
            Image(systemName: "trash")
        }
        .help("Remove — moves to .trash/ in the vault; nothing is deleted")
        .disabled(model.currentPath == nil)
    }

    private var graphButton: some View {
        Button { model.toggleGraph() } label: {
            Image(systemName: "point.3.connected.trianglepath.dotted")
                .foregroundStyle(model.showGraph ? Weft.accent : Color.primary)
        }
        .help(model.showGraph ? "Back to the note" : "Vault graph")
    }

    private var tasksButton: some View {
        Button { showTasks.toggle() } label: {
            Image(systemName: "checklist")
        }
        .help("Open tasks across the vault")
        .popover(isPresented: $showTasks, arrowEdge: .bottom) {
            TasksWebView { path in
                model.open(path: path)
                showTasks = false
            }
            .frame(width: 380, height: 460)
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
