import Foundation
import WeftKit

// Thin async wrapper over the daemon's HTTP API. No auth, localhost only.
// Everything a read+capture+surface client needs: list, raw HTML, surfacing,
// access logging (so co-access ranking keeps working), capture, and daily.
struct WeftClient {
    static let base = URL(string: "http://localhost:7777")!

    private let session = URLSession.shared
    // /note/{path} 302s to the viewer; we only want its access-log side effect,
    // so this session swallows the redirect instead of fetching viewer.html.
    private let noRedirect = URLSession(configuration: .ephemeral, delegate: NoRedirectDelegate(), delegateQueue: nil)

    func notes() async throws -> [NoteRef] {
        let (data, _) = try await session.data(from: Self.base.appending(path: "api/notes"))
        return try JSONDecoder().decode([NoteRef].self, from: data)
    }

    func rawHTML(path: String) async throws -> String {
        let (data, _) = try await session.data(from: Self.base.appending(path: "raw").appending(path: path))
        return String(decoding: data, as: UTF8.self)
    }

    func surface(path: String) async throws -> SurfacePayload {
        let (data, _) = try await session.data(from: Self.base.appending(path: "api/surface").appending(path: path))
        return try JSONDecoder().decode(SurfacePayload.self, from: data)
    }

    /// Fire-and-forget: register a read so session/co-access ranking sees it.
    func logAccess(path: String) async {
        let req = URLRequest(url: Self.base.appending(path: "note").appending(path: path))
        _ = try? await noRedirect.data(for: req)
    }

    @discardableResult
    func capture(_ text: String) async throws -> String {
        var req = URLRequest(url: Self.base.appending(path: "api/capture"))
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try JSONSerialization.data(withJSONObject: ["text": text])
        let (data, _) = try await session.data(for: req)
        return try JSONDecoder().decode(PathReply.self, from: data).path
    }

    /// DELETE /api/note/{path} — the daemon's soft remove: the file moves to
    /// .trash/ in the vault (bytes are never deleted, per the never-delete
    /// invariant) and its rows leave the index.
    func trash(path: String) async throws {
        var req = URLRequest(url: Self.base.appending(path: "api/note").appending(path: path))
        req.httpMethod = "DELETE"
        let (_, resp) = try await session.data(for: req)
        guard (resp as? HTTPURLResponse)?.statusCode == 200 else {
            throw URLError(.badServerResponse)
        }
    }

    /// Ensure today's daily exists and return its path.
    func daily() async throws -> String {
        let (data, _) = try await session.data(from: Self.base.appending(path: "api/daily"))
        return try JSONDecoder().decode(PathReply.self, from: data).path
    }

    static var streamURL: URL { base.appending(path: "api/surface/stream") }

    private struct PathReply: Codable { let path: String }

    // MARK: - Device pairing (/api/sync/pairing)

    // The daemon is the responder side of SAS pairing. Approving posts our half
    // of the key exchange but seals NOTHING; only confirm — after the human has
    // compared the codes — hands the vault key to the new device.

    enum PairingState {
        case waiting
        case sas(String)     // the 8-digit code, reveal verified
        case error(String)   // dead approval; cancel to free the daemon's slot
    }

    /// Non-2xx pairing reply, carrying the daemon's JSON {"error": …} message.
    /// inFlight is the parked request id the daemon reports on an approve 409,
    /// so the caller can cancel the right (possibly orphaned) approval.
    struct PairingFailure: LocalizedError {
        let status: Int
        let message: String
        var inFlight: String?
        var errorDescription: String? { message }
    }

    /// Pending pairing request ids from the sync bucket.
    /// Throws PairingFailure(409) when this vault has no sync configured.
    func pairingRequests() async throws -> [String] {
        let (data, resp) = try await session.data(from: Self.base.appending(path: "api/sync/pairing"))
        try Self.checkPairing(resp, data)
        struct Reply: Decodable { let requests: [String] }
        return try JSONDecoder().decode(Reply.self, from: data).requests
    }

    /// Begin the approval (202). 409 when another approval is in flight or the
    /// vault key is unavailable; 404 when the request vanished from the bucket.
    func pairingApprove(reqID: String) async throws {
        let (data, resp) = try await postPairing("approve", reqID: reqID)
        try Self.checkPairing(resp, data, expect: 202)
    }

    /// One status poll; the daemon checks the bucket for the phone's reveal.
    func pairingStatus(reqID: String) async throws -> PairingState {
        let url = Self.base.appending(path: "api/sync/pairing/status")
            .appending(queryItems: [URLQueryItem(name: "req_id", value: reqID)])
        let (data, resp) = try await session.data(from: url)
        try Self.checkPairing(resp, data)
        struct Reply: Decodable { let state: String, sas: String?, error: String? }
        let reply = try JSONDecoder().decode(Reply.self, from: data)
        switch reply.state {
        case "sas": return .sas(reply.sas ?? "")
        case "error": return .error(reply.error ?? "pairing failed")
        default: return .waiting
        }
    }

    /// The human said the codes match — seal the vault key for the new device.
    /// A 500 keeps the approval at sas on the daemon, so confirm is retryable.
    func pairingConfirm(reqID: String) async throws {
        let (data, resp) = try await postPairing("confirm", reqID: reqID)
        try Self.checkPairing(resp, data)
    }

    /// Best-effort abandon of the in-flight approval. Idempotent on the daemon
    /// (cancelling a request that isn't in flight is a 200 no-op).
    func pairingCancel(reqID: String) async {
        _ = try? await postPairing("cancel", reqID: reqID)
    }

    private func postPairing(_ action: String, reqID: String) async throws -> (Data, URLResponse) {
        var req = URLRequest(url: Self.base.appending(path: "api/sync/pairing").appending(path: action))
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try JSONSerialization.data(withJSONObject: ["req_id": reqID])
        return try await session.data(for: req)
    }

    private static func checkPairing(_ resp: URLResponse, _ data: Data, expect: Int = 200) throws {
        guard let http = resp as? HTTPURLResponse else { throw URLError(.badServerResponse) }
        guard http.statusCode == expect else {
            struct Err: Decodable { let error: String?; let in_flight: String? }
            let decoded = try? JSONDecoder().decode(Err.self, from: data)
            let msg = decoded?.error ?? "HTTP \(http.statusCode)"
            throw PairingFailure(status: http.statusCode, message: msg, inFlight: decoded?.in_flight)
        }
    }
}

// The macOS surface's WeftBackend: it already speaks the daemon HTTP API, so the
// conformance is structural. The iOS surface supplies a gomobile-backed peer.
extension WeftClient: WeftBackend {}

private final class NoRedirectDelegate: NSObject, URLSessionTaskDelegate {
    func urlSession(_ session: URLSession, task: URLSessionTask,
                    willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest,
                    completionHandler: @escaping (URLRequest?) -> Void) {
        completionHandler(nil)
    }
}
