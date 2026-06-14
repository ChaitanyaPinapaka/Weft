import SwiftUI
import Observation
import WebKit
import WeftKit

// The single source of UI truth. Owns the client, the SSE stream, and the
// proactive-surface inbox. Main-actor isolated; views observe it via @Observable.
@MainActor
@Observable
final class AppModel {
    static let shared = AppModel()

    // Vault + current note.
    var notes: [NoteRef] = []
    var currentPath: String?
    var doc: ReaderDoc?
    var surface: SurfacePayload = .empty
    var loading = false

    // Reader/editor toggle. While true the reader pane hosts the daemon's
    // editor page for the current note; the editor owns the whole save flow
    // (autosave + unload beacon), the app only hosts it. Sticky across
    // navigation: following a link while editing edits the target.
    var editing = false

    // Reader/graph toggle. While true the detail pane swaps the reader for the
    // vault graph (a WKWebView on /web/graph.html) in the SAME window; the
    // reader/sidebar/brain state is preserved underneath. Clicking a node opens
    // that note, which drops back to the reader (open(path:) clears this).
    var showGraph = false

    // Path staged for trashing; RootView's confirm alert completes or cancels.
    var pendingTrash: String?

    // A user-facing error (trash failed, etc.); RootView shows it in an alert.
    var errorMessage: String?

    // The reader pane's live web view, registered by ReaderView. Used to drive
    // the embedded editor's save/stop bridges when leaving or trashing a note.
    weak var activeWebView: WKWebView?

    // Daemon connection (driven by the SSE stream's status).
    var connected = false

    // Proactive surfacing inbox: events the daemon pushed that the user hasn't
    // opened yet. `hasUnseen` lights the ✦ toolbar badge.
    var inbox: [SurfaceEvent] = []
    var hasUnseen = false
    // Transient toast shown in-window when a push arrives while Weft is focused.
    var toast: SurfaceEvent?

    // "Set up this Mac" sheet (RootView presents it; the Note menu toggles it).
    var showSetup = false

    // "Add a Device" pairing sheet (Note menu, plus the setup panel's footer).
    var showAddDevice = false

    private let client = WeftClient()
    private var stream: SurfaceStream?
    private var toastDismiss: Task<Void, Never>?

    // MARK: - Lifecycle

    func bootstrap() {
        stream = SurfaceStream(
            onStatus: { [weak self] up in self?.connected = up },
            onEvent: { [weak self] ev in self?.receive(ev) }
        )
        stream?.start()
        Task { await reloadNotes() }
        Task { await openDaily() }
        Task { await maybeOfferSetup() }
    }

    // Auto-open the setup panel when no weft daemon answers. The agent plist's
    // existence proves nothing — the job may be dead or crash-looping (vault
    // moved), which needs the panel just as much as a fresh Mac does.
    private func maybeOfferSetup() async {
        for _ in 0..<2 { // second try bridges a daemon (re)start racing app launch
            if await SetupModel.daemonReachable() { return }
            try? await Task.sleep(nanoseconds: 1_000_000_000)
        }
        showSetup = true
    }

    func reloadNotes() async {
        if let list = try? await client.notes() {
            notes = list.sorted { $0.modDate > $1.modDate }
            connected = true
        }
    }

    // MARK: - Navigation

    /// Toggle the vault graph. Showing it tears down the reader's editor
    /// WKWebView, which never fires the editor's beforeunload/sendBeacon on
    /// programmatic teardown — so when editing we must flush the pending save
    /// first (mirroring finishEditing's Done path) or the last ~1s of edits are
    /// lost. Hiding the graph is a plain toggle.
    func toggleGraph() {
        // Switching INTO the graph while editing: flush first, then show. If the
        // flush fails, stay in the editor so edits aren't lost behind the graph.
        if !showGraph && editing {
            Task {
                guard await flushEditor() else {
                    errorMessage = "Couldn’t save before opening the graph; staying in the editor."
                    return
                }
                showGraph = true
            }
            return
        }
        showGraph.toggle()
    }

    func open(path: String) {
        // Opening a note always drops back to the reader — a graph node click
        // lands you on that note in the same window.
        showGraph = false
        guard path != currentPath || doc == nil else { return }

        // If we're editing a different note, flush its pending save before tearing
        // down the editor WKWebView (which fires no beforeunload), or the last ~1s
        // of edits are lost on a link-hop / sidebar / inbox navigation. If the
        // flush fails (daemon down), stay put and surface it rather than lose work.
        if editing, let cur = currentPath, cur != path {
            Task {
                let saved = await flushEditor()
                if !saved {
                    errorMessage = "Couldn’t save \(titleFor(cur)). Staying in the editor so you don’t lose changes."
                    return
                }
                editing = false
                loadInto(path: path)
            }
            return
        }
        loadInto(path: path)
    }

    /// Tear down to the reader and load `path`. Assumes any prior edit was already
    /// flushed by the caller.
    private func loadInto(path: String) {
        currentPath = path
        loading = true
        // Clear any inbox/toast entry for this note — opening it IS attending to it.
        inbox.removeAll { $0.path == path }
        if toast?.path == path { toast = nil }
        refreshUnseen()

        Task {
            await client.logAccess(path: path)
            async let rawTask = try? await client.rawHTML(path: path)
            async let surfTask = try? await client.surface(path: path)
            let raw = await rawTask
            if let raw {
                doc = ReaderDoc.parse(rawHTML: raw, path: path, fallbackTitle: titleFor(path))
                connected = true
            }
            surface = (await surfTask) ?? .empty
            loading = false
        }
    }

    func openDaily() async {
        if let path = try? await client.daily() {
            await reloadNotes()
            open(path: path)
        }
    }

    /// Re-pull surfacing for the current note (e.g. after a capture lands).
    func refreshSurface() {
        guard let path = currentPath else { return }
        Task { surface = (try? await client.surface(path: path)) ?? surface }
    }

    func titleFor(_ path: String) -> String {
        notes.first { $0.path == path }?.title
            ?? (path as NSString).lastPathComponent.replacingOccurrences(of: ".html", with: "")
    }

    // MARK: - Editing

    func beginEditing() {
        guard doc != nil else { return }
        editing = true
    }

    /// Done: flush the editor's pending save, wait for it to land, then swap
    /// back to the reader and re-read the note. WKWebView never fires the
    /// editor's beforeunload beacon on programmatic navigation, so we drive the
    /// save explicitly via the weftFlush bridge instead of racing a fixed sleep.
    func finishEditing() async {
        guard editing else { return }
        guard let path = currentPath else { editing = false; return }
        let saved = await flushEditor() // returns only once the final save has landed
        if !saved {
            // Don't drop the user into a stale reader that hides unsaved edits —
            // keep editing and surface the failure (mirrors trash()'s handling).
            errorMessage = "Couldn’t save \(titleFor(path)). Check the daemon is running; your edits are still here."
            return
        }
        editing = false
        // Bail if the user already moved on (or re-entered the editor).
        guard currentPath == path, !editing else { return }
        if let raw = try? await client.rawHTML(path: path) {
            doc = ReaderDoc.parse(rawHTML: raw, path: path, fallbackTitle: titleFor(path))
        }
        surface = (try? await client.surface(path: path)) ?? surface
        await reloadNotes() // sidebar title / mod-date may have changed
    }

    /// Drive the embedded editor's final save and wait for it to land. Returns
    /// false if the editor reports it could not save (offline / unresolved
    /// conflict), so callers can keep the user in the editor instead of losing work.
    @discardableResult
    private func flushEditor() async -> Bool {
        guard let web = activeWebView else { return true }
        let result = try? await web.callAsyncJavaScript(
            "return await window.weftFlush?.()", arguments: [:], in: nil, contentWorld: .page)
        // weftFlush returns a Bool; treat a null/throw as "landed" so a missing
        // bridge (non-editor page) doesn't wedge navigation.
        if let ok = result as? Bool { return ok }
        return true
    }

    /// Best-effort flush before the app exits (Cmd-Q / Quit). Public so the app
    /// delegate can await it from applicationShouldTerminate.
    func flushOnExit() async {
        guard editing else { return }
        _ = await flushEditor()
    }

    /// Stop the embedded editor from issuing any further saves — used before
    /// trashing the open note so a queued autosave can't recreate it.
    private func stopEditor() async {
        guard let web = activeWebView else { return }
        _ = try? await web.callAsyncJavaScript(
            "window.weftStop?.()", arguments: [:], in: nil, contentWorld: .page)
    }

    // MARK: - Trash

    /// Stage a trash request; the confirm alert calls trash() or clears this.
    func requestTrash(_ path: String) { pendingTrash = path }

    /// Soft remove: the daemon moves the file to .trash/ in the vault — nothing
    /// is deleted — and drops it from the index, so it leaves listings and
    /// surfacing. If the trashed note was open, fall to the freshest remaining
    /// note (or recreate today's daily in an emptied vault).
    func trash(path: String) {
        pendingTrash = nil
        Task {
            // If we're editing the very note being trashed, silence its autosave
            // first — otherwise a queued save could recreate the file in the
            // vault right after the daemon moves it to .trash/.
            if editing, currentPath == path {
                await stopEditor()
            }
            do {
                try await client.trash(path: path)
            } catch {
                WeftLog.write("trash failed path=\(path): \(error)")
                errorMessage = "Couldn’t remove \(titleFor(path)). \(error.localizedDescription)"
                return
            }
            inbox.removeAll { $0.path == path }
            if toast?.path == path { dismissToast() }
            refreshUnseen()
            await reloadNotes()
            if currentPath == path {
                editing = false
                currentPath = nil
                doc = nil
                surface = .empty
                if let next = notes.first?.path {
                    open(path: next)
                } else {
                    await openDaily()
                }
            }
        }
    }

    // MARK: - Capture

    func capture(_ text: String) {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        Task {
            _ = try? await client.capture(trimmed)
            await reloadNotes()
            refreshSurface()
        }
    }

    // MARK: - Proactive surfacing

    private func receive(_ ev: SurfaceEvent) {
        WeftLog.write("AppModel.receive type=\(ev.type) path=\(ev.path ?? "nil") current=\(currentPath ?? "nil")")
        // A note changed on disk (capture/rename/sync). Refresh the native reader
        // if it's the open note. While editing, the embedded editor page handles
        // its own reload/conflict via its EventSource, so leave it untouched.
        if ev.isChanged {
            if let p = ev.path, p == currentPath, !editing {
                Task {
                    if let raw = try? await client.rawHTML(path: p) {
                        doc = ReaderDoc.parse(rawHTML: raw, path: p, fallbackTitle: titleFor(p))
                    }
                    surface = (try? await client.surface(path: p)) ?? surface
                }
            }
            return
        }
        guard ev.isSurface, let path = ev.path else { return }
        // Don't surface the note already on screen, and de-dupe the inbox.
        guard path != currentPath else { return }
        inbox.removeAll { $0.path == path }
        inbox.insert(ev, at: 0)
        if inbox.count > 12 { inbox.removeLast(inbox.count - 12) }
        hasUnseen = true

        // If Weft is the active app, show a quiet in-window toast; otherwise let
        // the OS notification (posted below) carry it.
        if NSApp.isActive {
            showToast(ev)
        }
        Notifier.shared.post(ev)
    }

    func openFromInbox(_ ev: SurfaceEvent) {
        if let path = ev.path { open(path: path) }
    }

    func markInboxSeen() {
        hasUnseen = false
    }

    private func showToast(_ ev: SurfaceEvent) {
        toast = ev
        toastDismiss?.cancel()
        toastDismiss = Task {
            try? await Task.sleep(nanoseconds: 6_000_000_000)
            if !Task.isCancelled { toast = nil }
        }
    }

    func dismissToast() {
        toastDismiss?.cancel()
        toast = nil
    }

    // MARK: - Helpers

    private func refreshUnseen() {
        if inbox.isEmpty { hasUnseen = false }
    }

    /// Notes grouped by top-level folder, root notes last, folders alphabetical.
    var groupedNotes: [(folder: String, notes: [NoteRef])] {
        let groups = Dictionary(grouping: notes, by: \.folder)
        return groups.keys.sorted { a, b in
            if a.isEmpty != b.isEmpty { return !a.isEmpty } // foldered first, root last
            return a < b
        }.map { ($0, groups[$0]!.sorted { $0.modDate > $1.modDate }) }
    }
}
