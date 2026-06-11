import Foundation
import WeftKit
import WeftMobile

// WeftCore is the iOS WeftBackend: a thin async wrapper over the embedded Go
// engine (the gomobile `WeftMobile` framework). Every engine call is blocking
// (disk + bucket I/O) and the underlying sqlite index is single-connection, so
// all calls run on one private serial queue — off the main actor and never
// concurrent — and are bridged to async via continuations.
//
// gomobile maps Go's (string, error) returns to a _Nonnull String plus an
// explicit NSError out-param (NOT a Swift `throws`), so the String calls go
// through `run`; the (… error) → BOOL methods (open/recoverWithPhrase/logAccess)
// DO import as throwing.
final class WeftCore: WeftBackend, @unchecked Sendable {
    private let session: MobileSession
    private let queue = DispatchQueue(label: "app.tryweft.core")

    private init(session: MobileSession) { self.session = session }

    enum WeftError: LocalizedError {
        case configure
        var errorDescription: String? { "Could not open the vault." }
    }

    // MARK: - Lifecycle

    /// Open (creating if needed) the vault + index and record the bucket config.
    /// Pass empty strings for the bucket fields on relaunch of an already-enrolled
    /// vault — Open() reads the persisted config. Does NOT enroll.
    static func configure(vaultDir: String, provider: String = "", endpoint: String = "",
                          region: String = "", bucket: String = "", accessKeyID: String = "",
                          secret: String = "") throws -> WeftCore {
        var err: NSError?
        let session = MobileConfigure(vaultDir, provider, endpoint, region, bucket, accessKeyID, secret, &err)
        if let err { throw err }
        guard let session else { throw WeftError.configure }
        return WeftCore(session: session)
    }

    /// True once this device has sync configured for the vault (config.json on disk).
    var isEnrolled: Bool { session.enrolled() }

    /// Re-open an already-enrolled vault from the cached key (every-launch path).
    func open() async throws { try await runVoid { try self.session.open() } }

    func close() { try? session.close() }

    // MARK: - Enrollment

    /// Enroll from a 24-word BIP39 recovery phrase.
    func recoverWithPhrase(_ phrase: String) async throws {
        try await runVoid { try self.session.recover(withPhrase: phrase) }
    }

    /// Begin SAS pairing as the new device; returns the reqID to read aloud to the Mac.
    func pairStart() async throws -> String { try await run { self.session.pairStart($0) } }

    /// Poll for the responder's key; returns the 8-digit SAS once ready, else "".
    func pairPoll() async throws -> String { try await run { self.session.pairPoll($0) } }

    /// Poll for the sealed key; returns true and enrolls once the Mac confirms.
    func pairFinish() async throws -> Bool {
        try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Bool, Error>) in
            queue.async {
                var done: ObjCBool = false
                do { try self.session.pairFinish(&done); cont.resume(returning: done.boolValue) }
                catch { cont.resume(throwing: error) }
            }
        }
    }

    /// One convergence cycle; returns the engine Result JSON.
    @discardableResult
    func sync() async throws -> String { try await run { self.session.sync($0) } }

    // MARK: - WeftBackend (read / capture / surface)

    func notes() async throws -> [NoteRef] {
        try decode([NoteRef].self, from: await run { self.session.listNotes($0) })
    }

    func rawHTML(path: String) async throws -> String {
        try await run { self.session.readRaw(path, error: $0) }
    }

    func surface(path: String) async throws -> SurfacePayload {
        try decode(SurfacePayload.self, from: await run { self.session.surface(path, error: $0) })
    }

    func logAccess(path: String) async {
        try? await runVoid { try self.session.logAccess(path) }
    }

    @discardableResult
    func capture(_ text: String) async throws -> String {
        try await run { self.session.capture(text, error: $0) }
    }

    func daily() async throws -> String {
        try await run { self.session.daily($0) }
    }

    /// FTS5 search over the local index. The engine marshals []index.Hit
    /// directly, so an empty result set arrives as JSON `null` (the daemon's
    /// encoder does the same for a nil slice) — coalesce that to [].
    func search(_ query: String) async throws -> [SearchHit] {
        let json = try await run { self.session.searchNotes(query, error: $0) }
        let trimmed = json.trimmingCharacters(in: .whitespacesAndNewlines)
        if trimmed.isEmpty || trimmed == "null" { return [] }
        return try decode([SearchHit].self, from: json)
    }

    /// Soft delete: the engine moves the note into .trash/ in the vault (bytes
    /// are never deleted) and drops it from the index. Returns {"trashed":path},
    /// which the UI doesn't need.
    func trash(path: String) async throws {
        _ = try await run { self.session.trash(path, error: $0) }
    }

    // MARK: - Plumbing

    private func decode<T: Decodable>(_ type: T.Type, from json: String) throws -> T {
        try JSONDecoder().decode(T.self, from: Data(json.utf8))
    }

    /// Bridge a blocking, NSError-out-param gomobile String call to async/throws.
    private func run(_ body: @escaping (NSErrorPointer) -> String) async throws -> String {
        try await withCheckedThrowingContinuation { cont in
            queue.async {
                var err: NSError?
                let result = body(&err)
                if let err { cont.resume(throwing: err) } else { cont.resume(returning: result) }
            }
        }
    }

    /// Bridge a blocking, throwing (BOOL-returning) gomobile call to async/throws.
    private func runVoid(_ body: @escaping () throws -> Void) async throws {
        try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Void, Error>) in
            queue.async {
                do { try body(); cont.resume() } catch { cont.resume(throwing: error) }
            }
        }
    }
}
