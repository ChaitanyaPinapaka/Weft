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

    /// Ensure today's daily exists and return its path.
    func daily() async throws -> String {
        let (data, _) = try await session.data(from: Self.base.appending(path: "api/daily"))
        return try JSONDecoder().decode(PathReply.self, from: data).path
    }

    static var streamURL: URL { base.appending(path: "api/surface/stream") }

    private struct PathReply: Codable { let path: String }
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
