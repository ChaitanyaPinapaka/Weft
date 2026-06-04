import Foundation

// WeftBackend is the transport seam that lets AppModel + the views be shared
// across surfaces. The macOS app conforms its HTTP WeftClient (talking to the
// localhost daemon); the iOS app conforms a WeftCore wrapper over the embedded
// gomobile engine (talking straight to the BYOC bucket). Both return the same
// JSON-decoded models, so everything above this protocol is platform-neutral.
public protocol WeftBackend {
    /// All notes in the vault.
    func notes() async throws -> [NoteRef]

    /// Raw .html bytes of a note (the reader's source).
    func rawHTML(path: String) async throws -> String

    /// Brain-panel payload for a focus note.
    func surface(path: String) async throws -> SurfacePayload

    /// Register a read so session/co-access ranking sees it. Fire-and-forget.
    func logAccess(path: String) async

    /// Append free text to today's daily note; returns the daily's path.
    @discardableResult
    func capture(_ text: String) async throws -> String

    /// Ensure today's daily exists and return its path.
    func daily() async throws -> String
}
