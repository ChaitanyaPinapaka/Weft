import Foundation
import WeftKit

// SSE client for GET /api/surface/stream. Uses a URLSessionDataDelegate rather
// than URLSession.bytes(_:).lines: AsyncBytes buffers and does not deliver small
// SSE frames in real time, so `hello` and pushes never arrive until the buffer
// fills. The delegate's didReceive(data:) fires per flushed chunk, which is how
// EventSource-style clients consume SSE reliably. Reconnects with a fixed
// backoff so the app survives the daemon restarting.
@MainActor
final class SurfaceStream: NSObject, URLSessionDataDelegate {
    private let onEvent: (SurfaceEvent) -> Void
    private let onStatus: (Bool) -> Void

    private var session: URLSession?
    private var task: URLSessionDataTask?
    private var buffer = Data()
    private var dataLines: [String] = []
    private var reconnect: Task<Void, Never>?
    private var stopped = false

    init(onStatus: @escaping (Bool) -> Void, onEvent: @escaping (SurfaceEvent) -> Void) {
        self.onStatus = onStatus
        self.onEvent = onEvent
        super.init()
    }

    func start() {
        stopped = false
        if session == nil {
            let cfg = URLSessionConfiguration.default
            cfg.timeoutIntervalForRequest = 3600       // keepalives arrive every 25s
            cfg.requestCachePolicy = .reloadIgnoringLocalCacheData
            session = URLSession(configuration: cfg, delegate: self, delegateQueue: nil)
        }
        connect()
    }

    func stop() {
        stopped = true
        reconnect?.cancel()
        task?.cancel()
        session?.invalidateAndCancel()
        session = nil
    }

    private func connect() {
        buffer.removeAll(keepingCapacity: true)
        dataLines.removeAll(keepingCapacity: true)
        var req = URLRequest(url: WeftClient.streamURL)
        req.setValue("text/event-stream", forHTTPHeaderField: "Accept")
        req.timeoutInterval = 3600
        WeftLog.write("stream: connecting \(WeftClient.streamURL)")
        task = session?.dataTask(with: req)
        task?.resume()
    }

    private func ingest(_ data: Data) {
        buffer.append(data)
        // SSE lines are \n-delimited; a blank line terminates a frame. Partial
        // lines split across chunks stay in `buffer` until their newline arrives.
        while let nl = buffer.firstIndex(of: 0x0A) {
            let lineData = buffer[buffer.startIndex..<nl]
            buffer.removeSubrange(buffer.startIndex...nl)
            let line = String(decoding: lineData, as: UTF8.self)
                .trimmingCharacters(in: CharacterSet(charactersIn: "\r"))
            if line.isEmpty {
                if !dataLines.isEmpty, let ev = decode(dataLines.joined()) { onEvent(ev) }
                dataLines.removeAll(keepingCapacity: true)
            } else if line.hasPrefix("data:") {
                dataLines.append(String(line.dropFirst(5)).trimmingCharacters(in: .whitespaces))
            }
            // "event:" and ":"-comment (keepalive) lines are ignored.
        }
    }

    private func decode(_ json: String) -> SurfaceEvent? {
        try? JSONDecoder().decode(SurfaceEvent.self, from: Data(json.utf8))
    }

    private func scheduleReconnect() {
        guard !stopped else { return }
        reconnect?.cancel()
        reconnect = Task { @MainActor [weak self] in
            try? await Task.sleep(nanoseconds: 3_000_000_000)
            guard let self, !self.stopped else { return }
            self.connect()
        }
    }

    // MARK: - URLSessionDataDelegate (nonisolated; hop to the main actor)

    nonisolated func urlSession(_ session: URLSession, dataTask: URLSessionDataTask,
                                didReceive response: URLResponse,
                                completionHandler: @escaping (URLSession.ResponseDisposition) -> Void) {
        let code = (response as? HTTPURLResponse)?.statusCode ?? -1
        WeftLog.write("stream: HTTP \(code)")
        Task { @MainActor in self.onStatus(code == 200) }
        completionHandler(code == 200 ? .allow : .cancel)
    }

    nonisolated func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive data: Data) {
        Task { @MainActor in self.ingest(data) }
    }

    nonisolated func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
        WeftLog.write("stream: completed error=\(error.map { String(describing: $0) } ?? "nil")")
        Task { @MainActor in
            self.onStatus(false)
            self.scheduleReconnect()
        }
    }
}
