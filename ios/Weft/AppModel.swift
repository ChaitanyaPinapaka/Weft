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

    // App state.
    var enrolled = false
    var booting = true
    var syncing = false
    var lastError: String?

    private var core: WeftCore?
    private var pairingCore: WeftCore?

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
        defer { booting = false }
        do {
            let c = try WeftCore.configure(vaultDir: Self.vaultDir)
            if c.isEnrolled {
                try await c.open()
                core = c
                enrolled = true
                await openDaily()
                Task { await self.sync() }
            } else {
                c.close() // discard; a configured core is made at enroll time
                enrolled = false
            }
        } catch {
            lastError = error.localizedDescription
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
        do { _ = try await core.sync(); await reload() } catch { lastError = error.localizedDescription }
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
