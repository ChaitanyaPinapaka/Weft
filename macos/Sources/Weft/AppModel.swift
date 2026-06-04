import SwiftUI
import Observation
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

    // Daemon connection (driven by the SSE stream's status).
    var connected = false

    // Proactive surfacing inbox: events the daemon pushed that the user hasn't
    // opened yet. `hasUnseen` lights the ✦ toolbar badge.
    var inbox: [SurfaceEvent] = []
    var hasUnseen = false
    // Transient toast shown in-window when a push arrives while Weft is focused.
    var toast: SurfaceEvent?

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
    }

    func reloadNotes() async {
        if let list = try? await client.notes() {
            notes = list.sorted { $0.modDate > $1.modDate }
            connected = true
        }
    }

    // MARK: - Navigation

    func open(path: String) {
        guard path != currentPath || doc == nil else { return }
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
