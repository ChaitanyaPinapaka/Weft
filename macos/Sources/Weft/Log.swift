import Foundation

// Lightweight diagnostic log. `open`-launched apps detach stderr, so we append
// to a file we can tail from the shell. Temporary scaffolding for debugging the
// notification path.
enum WeftLog {
    static let path = "/tmp/weft-app.log"

    static func write(_ message: String) {
        let line = ISO8601DateFormatter().string(from: Date()) + "  " + message + "\n"
        if let data = line.data(using: .utf8) {
            if let h = FileHandle(forWritingAtPath: path) {
                h.seekToEndOfFile(); h.write(data); try? h.close()
            } else {
                try? data.write(to: URL(fileURLWithPath: path))
            }
        }
    }
}
