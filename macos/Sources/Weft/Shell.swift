import Foundation

// The single choke-point for every shell-out the setup panel makes. A GUI app
// launched from Finder inherits a bare environment, so PATH is explicit —
// Homebrew and ~/.local/bin tools (weft, claude) aren't findable otherwise.
enum Shell {
    struct Output {
        let status: Int32
        let stdout: String
        let stderr: String
        var ok: Bool { status == 0 }
    }

    static let searchPath = [
        "/opt/homebrew/bin",
        "/usr/local/bin",
        ("~/.local/bin" as NSString).expandingTildeInPath,
        "/usr/bin", "/bin", "/usr/sbin", "/sbin",
    ].joined(separator: ":")

    /// Run an executable, wait for exit, capture everything. The child is
    /// SIGKILLed (status 124) if it outlives `timeout` — a hung command must
    /// never freeze the setup panel, which disables every button while busy.
    static func run(_ executable: String, _ arguments: [String], timeout: TimeInterval = 60) async -> Output {
        await withCheckedContinuation { cont in
            DispatchQueue.global(qos: .userInitiated).async {
                let proc = Process()
                proc.executableURL = URL(fileURLWithPath: executable)
                proc.arguments = arguments
                proc.environment = ["PATH": searchPath, "HOME": NSHomeDirectory()]
                let out = Pipe(), err = Pipe()
                proc.standardOutput = out
                proc.standardError = err
                do {
                    try proc.run()
                } catch {
                    cont.resume(returning: Output(status: 127, stdout: "", stderr: error.localizedDescription))
                    return
                }
                // Drain both pipes concurrently before waiting. Sequential reads
                // deadlock when the child fills the 64KB buffer of whichever pipe
                // is read second (e.g. a long stack trace on stderr); waiting
                // before draining deadlocks on big output on either pipe.
                var outData = Data(), errData = Data()
                let drained = DispatchGroup()
                drained.enter()
                DispatchQueue.global(qos: .userInitiated).async {
                    outData = out.fileHandleForReading.readDataToEndOfFile()
                    drained.leave()
                }
                drained.enter()
                DispatchQueue.global(qos: .userInitiated).async {
                    errData = err.fileHandleForReading.readDataToEndOfFile()
                    drained.leave()
                }
                let timedOut = drained.wait(timeout: .now() + timeout) == .timedOut
                if timedOut {
                    kill(proc.processIdentifier, SIGKILL) // closes its pipe ends → readers EOF
                    // Brief grace for the readers, then abandon the buffers — the
                    // timeout path below never touches them, so a straggling
                    // reader (grandchild holding the pipes) can't race us.
                    _ = drained.wait(timeout: .now() + 2)
                }
                proc.waitUntilExit() // SIGKILL is unmaskable, so this returns
                cont.resume(returning: Output(
                    status: timedOut ? 124 : proc.terminationStatus,
                    stdout: timedOut ? "" : String(data: outData, encoding: .utf8)?
                        .trimmingCharacters(in: .whitespacesAndNewlines) ?? "",
                    stderr: timedOut ? "timed out after \(Int(timeout))s" : String(data: errData, encoding: .utf8)?
                        .trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
                ))
            }
        }
    }

    /// Fire-and-forget launch — for handing a URL to the Chrome binary, which
    /// may stay resident; waiting on it would hang the action.
    static func launch(_ executable: String, _ arguments: [String]) {
        let proc = Process()
        proc.executableURL = URL(fileURLWithPath: executable)
        proc.arguments = arguments
        proc.environment = ["PATH": searchPath, "HOME": NSHomeDirectory()]
        proc.standardOutput = FileHandle.nullDevice
        proc.standardError = FileHandle.nullDevice
        try? proc.run()
    }

    /// Single-quote an argument for /bin/sh so paths with spaces or quotes
    /// can't break out of the command we build for `do shell script`.
    static func quoted(_ s: String) -> String {
        "'" + s.replacingOccurrences(of: "'", with: "'\\''") + "'"
    }

    /// Run a /bin/sh command line with an admin prompt (osascript). The shell
    /// line must already be defensively quoted via `quoted(_:)`; this layer
    /// only escapes it into an AppleScript string literal.
    static func runAsAdmin(_ shellLine: String) async -> Output {
        let escaped = shellLine
            .replacingOccurrences(of: "\\", with: "\\\\")
            .replacingOccurrences(of: "\"", with: "\\\"")
        // Generous timeout: the admin password prompt sits inside this call.
        return await run(
            "/usr/bin/osascript",
            ["-e", "do shell script \"\(escaped)\" with administrator privileges"],
            timeout: 300
        )
    }
}
