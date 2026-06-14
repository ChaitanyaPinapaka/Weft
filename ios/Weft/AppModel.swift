import SwiftUI
import Observation
import WeftKit

// The single source of UI truth for the iOS app. Owns the embedded engine
// (WeftCore) and the vault's view state. Main-actor isolated; views observe it
// via @Observable. Unlike the macOS AppModel there is no daemon/SSE — everything
// is local calls into the gomobile engine, and proactive surfacing is computed
// on device (later).
@MainActor
@Observable
final class AppModel {
    // Vault + current note.
    var notes: [NoteRef] = []
    var currentPath: String?
    var doc: ReaderDoc?
    var surface: SurfacePayload = .empty

    // Navigation: the reader stack, owned here so URL-scheme routing can push.
    var navPath: [String] = []

    // Capture sheet, shared between the toolbar button and weft://capture.
    var showCapture = false
    var captureDraft = ""

    // App state.
    var enrolled = false
    var booting = true
    var syncing = false
    var lastError: String?
    var lastSyncAt: Date?
    var lastSyncResult: SyncResult?

    private var core: WeftCore?
    private var pairingCore: WeftCore?
    private var pendingURL: URL? // a weft:// link that arrived before boot finished

    /// The vault lives in Application Support (private, Data-Protection encrypted,
    /// not user-deletable via Files).
    static var vaultDir: String {
        let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask).first!
        let dir = base.appendingPathComponent("Weft/vault", isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir.path
    }

    // MARK: - Boot

    func boot() async {
        booting = true
        do {
            let c = try WeftCore.configure(vaultDir: Self.vaultDir)
            if c.isEnrolled {
                try await c.open()
                core = c
                enrolled = true
                await openDaily()
                await drainInbox()
                Task { await self.sync() }
            } else {
                c.close() // discard; a configured core is made at enroll time
                enrolled = false
            }
        } catch {
            lastError = "Could not open the vault: \(error.localizedDescription)"
        }
        booting = false
        if let url = pendingURL {
            pendingURL = nil
            await route(url)
        }
    }

    // MARK: - Enrollment

    func enrollWithPhrase(creds: BucketCreds, phrase: String) async throws {
        let c = try makeConfigured(creds)
        try await c.recoverWithPhrase(phrase)
        await adopt(c)
    }

    /// Begin SAS pairing; returns the reqID to read to the already-enrolled Mac.
    func pairStart(creds: BucketCreds) async throws -> String {
        let c = try makeConfigured(creds)
        let reqID = try await c.pairStart()
        pairingCore = c
        return reqID
    }

    /// Poll for the responder; returns the 8-digit SAS once ready, else "".
    func pairPoll() async throws -> String {
        try await pairingCore?.pairPoll() ?? ""
    }

    /// Poll for the sealed key; returns true and finishes enrollment once the Mac confirms.
    func pairFinish() async throws -> Bool {
        guard let c = pairingCore else { return false }
        let done = try await c.pairFinish()
        if done { pairingCore = nil; await adopt(c) }
        return done
    }

    func cancelPairing() {
        pairingCore?.close()
        pairingCore = nil
    }

    private func makeConfigured(_ creds: BucketCreds) throws -> WeftCore {
        try WeftCore.configure(
            vaultDir: Self.vaultDir, provider: creds.provider, endpoint: creds.endpoint,
            region: creds.region, bucket: creds.bucket, accessKeyID: creds.accessKeyID, secret: creds.secret)
    }

    private func adopt(_ c: WeftCore) async {
        core?.close()
        core = c
        enrolled = true
        await openDaily()
        Task { await self.sync() }
    }

    // MARK: - Sync / reload / navigation / capture

    func sync() async {
        guard let core else { return }
        syncing = true
        defer { syncing = false }
        do {
            let json = try await core.sync()
            lastSyncResult = try? JSONDecoder().decode(SyncResult.self, from: Data(json.utf8))
            lastSyncAt = Date()
            await reload()
        } catch {
            lastError = "Sync failed: \(error.localizedDescription)"
        }
    }

    func reload() async {
        guard let core else { return }
        do { notes = try await core.notes().sorted { $0.modDate > $1.modDate } }
        catch { lastError = error.localizedDescription }
    }

    func open(path: String) async {
        guard let core else { return }
        currentPath = path
        await core.logAccess(path: path)
        do {
            let raw = try await core.rawHTML(path: path)
            doc = ReaderDoc.parse(rawHTML: raw, path: path, fallbackTitle: titleFor(path))
            surface = (try? await core.surface(path: path)) ?? .empty
        } catch { lastError = error.localizedDescription }
    }

    func openDaily() async {
        guard let core else { return }
        if let path = try? await core.daily() {
            await reload()
            await open(path: path)
        }
    }

    func capture(_ text: String) async {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let core, !trimmed.isEmpty else { return }
        do {
            _ = try await core.capture(trimmed)
            await reload()
            if let p = currentPath { surface = (try? await core.surface(path: p)) ?? surface }
        } catch { lastError = error.localizedDescription }
    }

    /// FTS5 hits for a query. Search is best-effort UI — syntax errors from
    /// half-typed FTS operators (a lone quote, a trailing AND) are routine, so
    /// failures read as "no results" rather than raising the error banner.
    func search(_ query: String) async -> [SearchHit] {
        guard let core else { return [] }
        return (try? await core.search(query)) ?? []
    }

    /// Soft delete: the note moves to .trash/ in the vault — nothing is ever
    /// deleted — but it leaves listings, search, and surfacing.
    func trash(path: String) async {
        guard let core else { return }
        do {
            try await core.trash(path: path)
            navPath.removeAll { $0 == path }
            if currentPath == path {
                currentPath = nil
                doc = nil
                surface = .empty
            }
            await reload()
        } catch { lastError = error.localizedDescription }
    }

    // MARK: - URL scheme (weft://)

    /// weft://capture[?text=…] opens the capture sheet, weft://daily opens
    /// today's note, weft://note?path=… opens a note. Links that arrive while
    /// boot is still running are stashed and replayed after it.
    func handleURL(_ url: URL) {
        guard url.scheme?.lowercased() == "weft" else { return }
        if booting {
            pendingURL = url
            return
        }
        Task { await self.route(url) }
    }

    private func route(_ url: URL) async {
        guard enrolled else { return }
        let query = URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems
        switch url.host?.lowercased() {
        case "capture":
            if let text = query?.first(where: { $0.name == "text" })?.value, !text.isEmpty {
                captureDraft = text
            }
            showCapture = true
        case "daily":
            await openDaily()
            if let p = currentPath { navPath = [p] }
        case "note":
            if let p = query?.first(where: { $0.name == "path" })?.value, !p.isEmpty {
                navPath = [p]
            }
        default:
            break
        }
    }

    // MARK: - Ingest queue (App Group inbox)

    static let appGroupID = "group.app.tryweft.shared"

    /// Consume pending captures queued by the extensions (share sheet, widget…):
    /// one JSON file per capture — {text, source?, ts} — in <group>/inbox/.
    /// Each is appended to the daily note, then its queue file is removed (queue
    /// files are not vault notes; deleting them is fine). No-ops cleanly when
    /// the app group container is unavailable or the inbox is empty.
    /// Guards drainInbox against reentrancy: boot() and the scenePhase handler
    /// can both call it, and it suspends at `core.capture` before deleting the
    /// queue file — without this flag the two runs ingest the same capture twice.
    private var draining = false

    func drainInbox() async {
        guard let core else { return }
        if draining { return }
        draining = true
        defer { draining = false }
        guard let container = FileManager.default
            .containerURL(forSecurityApplicationGroupIdentifier: Self.appGroupID) else { return }
        let inbox = container.appendingPathComponent("inbox", isDirectory: true)
        guard let entries = try? FileManager.default
            .contentsOfDirectory(at: inbox, includingPropertiesForKeys: nil) else { return }

        struct Queued: Decodable {
            let text: String
            let source: String?
        }
        var captured = false
        // Producers name files by timestamp; sorting keeps capture order stable.
        for file in entries.filter({ $0.pathExtension == "json" })
            .sorted(by: { $0.lastPathComponent < $1.lastPathComponent }) {
            guard let data = try? Data(contentsOf: file),
                  let item = try? JSONDecoder().decode(Queued.self, from: data),
                  !item.text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
                // Malformed or empty entries can never succeed — drop them so
                // they don't wedge the queue forever.
                try? FileManager.default.removeItem(at: file)
                continue
            }
            // Append the source URL unless the producer already baked it into
            // the text (the share extension writes "comment\nURL" + source=URL).
            let text = item.source.flatMap {
                $0.isEmpty || item.text.contains($0) ? nil : "\(item.text)\n\($0)"
            } ?? item.text
            // Leave the file in place on failure so the next foreground retries.
            guard (try? await core.capture(text)) != nil else { continue }
            try? FileManager.default.removeItem(at: file)
            captured = true
        }
        if captured {
            await reload()
            if let p = currentPath { surface = (try? await core.surface(path: p)) ?? surface }
        }
    }

    func titleFor(_ path: String) -> String {
        notes.first { $0.path == path }?.title
            ?? (path as NSString).lastPathComponent.replacingOccurrences(of: ".html", with: "")
    }

    /// Notes grouped by top-level folder, root notes last, folders alphabetical.
    var groupedNotes: [(folder: String, notes: [NoteRef])] {
        let groups = Dictionary(grouping: notes, by: \.folder)
        return groups.keys.sorted { a, b in
            if a.isEmpty != b.isEmpty { return !a.isEmpty }
            return a < b
        }.map { ($0, groups[$0]!.sorted { $0.modDate > $1.modDate }) }
    }
}

/// Bucket credentials entered once at enrollment; the Go side persists them in
/// the sandbox secret store afterward, so they aren't stored on the Swift side.
struct BucketCreds {
    var provider = "r2"
    var endpoint = ""
    var region = "auto"
    var bucket = ""
    var accessKeyID = ""
    var secret = ""
}
