import AppKit
import CryptoKit
import Observation

// State + actions for the "Set up this Mac" panel. Each row is a status check
// plus one idempotent action; every action ends by re-running ALL checks so the
// panel reflects reality, never assumptions (rows also gate on each other:
// daemon and MCP need the CLI's installed path).
enum SetupStatus: Equatable {
    case checking
    case missing(String)   // red — not set up
    case attention(String) // amber — partial / a manual step remains
    case done(String)      // green
    case blocked(String)   // grey — prerequisite missing; action disabled
}

@MainActor
@Observable
final class SetupModel {
    enum Row { case cli, daemon, ext, mcp, memory }

    var cli: SetupStatus = .checking
    var daemon: SetupStatus = .checking
    var ext: SetupStatus = .checking
    var mcp: SetupStatus = .checking
    var memory: SetupStatus = .checking

    var busyRow: Row?
    var lastError: String?
    var showChromeSteps = false

    var vaultPath: String {
        didSet { UserDefaults.standard.set(vaultPath, forKey: "vaultPath") }
    }

    // Discovered by the checks; consumed by dependent actions.
    private(set) var installedCLIPath: String?
    private var claudePath: String?

    private let fm = FileManager.default
    private let home = NSHomeDirectory()

    init() {
        vaultPath = UserDefaults.standard.string(forKey: "vaultPath") ?? "~/Vault"
    }

    // MARK: - Paths

    private var resources: String { Bundle.main.resourcePath ?? "" }
    private var bundledCLI: String { resources + "/bin/weft" }
    private var bundledExtension: String { resources + "/extension" }
    private var bundledClaude: String { resources + "/claude" }

    static let agentLabel = "app.tryweft.daemon"
    static var agentPlistPath: String {
        NSHomeDirectory() + "/Library/LaunchAgents/\(agentLabel).plist"
    }
    private var extensionDest: String { home + "/Library/Application Support/Weft/extension" }
    private var claudeMDPath: String { home + "/.claude/CLAUDE.md" }
    private var claudeCommandsDir: String { home + "/.claude/commands" }
    private var expandedVault: String { (vaultPath as NSString).expandingTildeInPath }

    private static let beginMarker = "<!-- weft:memory:begin -->"
    private static let endMarker = "<!-- weft:memory:end -->"

    // MARK: - Shared probes (also used by AppModel's auto-present-at-boot check)

    /// True only when a *weft* daemon answers: /api/notes returns 200 + JSON.
    /// A generic HTTP response is not enough — any dev server squatting on
    /// :7777 would otherwise suppress first-run setup.
    nonisolated static func daemonReachable() async -> Bool {
        var req = URLRequest(url: URL(string: "http://localhost:7777/api/notes")!)
        req.timeoutInterval = 1.5
        guard let (_, resp) = try? await URLSession.shared.data(for: req),
              let http = resp as? HTTPURLResponse, http.statusCode == 200,
              (http.value(forHTTPHeaderField: "Content-Type") ?? "").contains("application/json")
        else { return false }
        return true
    }

    // MARK: - Status checks

    func refreshAll() async {
        await checkCLI()       // first: discovers installedCLIPath, which gates the rest
        await checkDaemon()
        await checkExtension()
        await checkMCP()
        await checkMemory()
    }

    private func checkCLI() async {
        installedCLIPath = await resolveCommand("weft")
        guard let installed = installedCLIPath else {
            cli = .missing("weft not on PATH")
            return
        }
        // Hash off the main actor — the binary is ~60MB.
        let bundled = bundledCLI
        let (a, b) = await Task.detached { (Self.sha256(installed), Self.sha256(bundled)) }.value
        if b == nil || a == nil {
            // No bundled binary to compare against (dev build without build.sh
            // resources) — that's not evidence an update exists.
            cli = .attention("\(installed) — can't verify against bundled copy")
        } else if a == b {
            cli = .done(installed)
        } else {
            // Differs ≠ older: the user may have built a newer weft from source.
            cli = .attention("\(installed) · differs from bundled copy")
        }
    }

    private func checkDaemon() async {
        let reachable = await Self.daemonReachable()
        // `launchctl print` exits 0 for a loaded-but-dead job (113 only when not
        // loaded), so the exit code alone can't claim "running via launchd".
        // Green requires state = running AND launchd's pid owning :7777.
        let job = await Shell.run("/bin/launchctl", ["print", "gui/\(getuid())/\(Self.agentLabel)"])
        let loaded = job.ok
        let agentPID = Self.jobPID(in: job.stdout)
        var agentServing = false
        if reachable, loaded, job.stdout.contains("state = running"), let agentPID {
            agentServing = await Self.portListenerPIDs().contains(agentPID)
        }
        if reachable {
            if !agentServing {
                daemon = .attention("running outside launchd")
            } else if let args = Self.agentProgramArguments(), args.count >= 3, args[2] != expandedVault {
                // The plist is observable reality; the vault field is intent.
                daemon = .attention("agent serves \(args[2]) — vault field differs; re-run Install Agent")
            } else {
                daemon = .done("running via launchd · localhost:7777")
            }
        } else if installedCLIPath == nil {
            daemon = .blocked("install the weft CLI first")
        } else if loaded {
            daemon = .missing("agent loaded but daemon not answering — see ~/Library/Logs/weft-daemon.log")
        } else {
            daemon = .missing("not running")
        }
    }

    /// The job's `pid = N` line from `launchctl print` output.
    private static func jobPID(in launchctlPrint: String) -> Int? {
        for line in launchctlPrint.split(separator: "\n") {
            let t = line.trimmingCharacters(in: .whitespaces)
            if t.hasPrefix("pid = ") { return Int(t.dropFirst(6)) }
        }
        return nil
    }

    /// PIDs LISTENing on :7777 via lsof. The app's own HTTP/SSE connections
    /// also hold the port, hence the LISTEN filter.
    private static func portListenerPIDs() async -> Set<Int> {
        let res = await Shell.run("/usr/sbin/lsof", ["-nP", "-ti", "tcp:7777", "-sTCP:LISTEN"], timeout: 10)
        return Set(res.stdout.split(separator: "\n").compactMap { Int($0) })
    }

    /// The installed agent plist's ProgramArguments — observable reality for
    /// "which vault does launchd actually serve".
    private static func agentProgramArguments() -> [String]? {
        guard let data = FileManager.default.contents(atPath: agentPlistPath),
              let obj = try? PropertyListSerialization.propertyList(from: data, format: nil) as? [String: Any]
        else { return nil }
        return obj["ProgramArguments"] as? [String]
    }

    private func checkExtension() async {
        let manifest = extensionDest + "/manifest.json"
        guard let data = fm.contents(atPath: manifest),
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let version = obj["version"] as? String else {
            ext = .missing("not copied")
            return
        }
        // Honest amber: Chrome won't tell us whether "Load unpacked" happened.
        ext = .attention("v\(version) copied — finish in Chrome")
    }

    private func checkMCP() async {
        claudePath = await resolveCommand("claude")
        guard let claude = claudePath else {
            mcp = .blocked("claude CLI not found — install Claude Code first")
            return
        }
        guard installedCLIPath != nil else {
            mcp = .blocked("install the weft CLI first")
            return
        }
        let res = await Shell.run("/usr/bin/env", [claude, "mcp", "get", "weft"])
        guard res.ok else {
            // 127 means claude itself failed to launch (npm shim whose node
            // isn't on our PATH) — that says nothing about registration.
            mcp = res.status == 127
                ? .attention("claude found but failed to run (exit 127) — node not on PATH?")
                : .missing("not registered")
            return
        }
        // Exit 0 only proves *an* entry exists somewhere; the printed
        // Scope/Command/Args are the observables for scope and vault drift.
        let scope = Self.field("Scope", in: res.stdout)
        let command = Self.field("Command", in: res.stdout)
        let args = Self.field("Args", in: res.stdout)
        var drift: [String] = []
        if let scope, !scope.lowercased().contains("user") { drift.append("scope: \(scope)") }
        if let command, let installed = installedCLIPath, command != installed { drift.append("cmd: \(command)") }
        if let args, args != "mcp " + expandedVault { drift.append("args: \(args)") }
        if !drift.isEmpty {
            mcp = .attention("registered but stale (\(drift.joined(separator: " · "))) — re-register")
        } else if scope == nil {
            mcp = .done("weft registered") // output format changed; claim no more than exit 0 proves
        } else {
            mcp = .done("weft registered · user scope")
        }
    }

    /// First "Name: value" line in CLI output, e.g. `claude mcp get`'s
    /// `Scope: User config` / `Command: …` / `Args: …`.
    private static func field(_ name: String, in output: String) -> String? {
        for line in output.split(separator: "\n") {
            let t = line.trimmingCharacters(in: .whitespaces)
            if t.hasPrefix(name + ":") {
                return String(t.dropFirst(name.count + 1)).trimmingCharacters(in: .whitespaces)
            }
        }
        return nil
    }

    private func checkMemory() async {
        let md = (try? String(contentsOfFile: claudeMDPath, encoding: .utf8)) ?? ""
        let commands = ["vault-log.md", "vault-context.md"]
            .allSatisfy { fm.fileExists(atPath: claudeCommandsDir + "/" + $0) }
        switch (Self.markerState(in: md), commands) {
        case (.wellFormed, true): memory = .done("CLAUDE.md section + /vault-log + /vault-context")
        case (.malformed, _): memory = .attention("weft markers in CLAUDE.md are malformed — repair by hand")
        case (.absent, false): memory = .missing("not wired")
        default: memory = .attention("partially wired")
        }
    }

    private enum MarkerState {
        case absent
        case wellFormed(Range<String.Index>) // the begin..end block, inclusive
        case malformed
    }

    /// Well-formed means exactly one begin and one end, in order. Anything else
    /// — orphaned, inverted, duplicated, or quoted in prose — must never be
    /// merged against: first-occurrence matching would pair markers across user
    /// content and eat it (or append duplicate blocks forever).
    private static func markerState(in md: String) -> MarkerState {
        let begins = occurrences(of: beginMarker, in: md)
        let ends = occurrences(of: endMarker, in: md)
        switch (begins.count, ends.count) {
        case (0, 0):
            return .absent
        case (1, 1) where begins[0].upperBound <= ends[0].lowerBound:
            return .wellFormed(begins[0].lowerBound..<ends[0].upperBound)
        default:
            return .malformed
        }
    }

    private static func occurrences(of needle: String, in s: String) -> [Range<String.Index>] {
        var found: [Range<String.Index>] = []
        var from = s.startIndex
        while let r = s.range(of: needle, range: from..<s.endIndex) {
            found.append(r)
            from = r.upperBound
        }
        return found
    }

    // MARK: - Actions

    func installCLI() { perform(.cli) { await self.doInstallCLI() } }
    func installDaemon() { perform(.daemon) { await self.doInstallDaemon() } }
    func installExtension() { perform(.ext) { await self.doInstallExtension() } }
    func installMCP() { perform(.mcp) { await self.doInstallMCP() } }
    func installMemory() { perform(.memory) { await self.doInstallMemory() } }

    private func perform(_ row: Row, _ body: @escaping () async -> Void) {
        guard busyRow == nil else { return }
        busyRow = row
        lastError = nil
        Task {
            await body()
            await refreshAll() // reflect reality, never assumptions
            busyRow = nil
        }
    }

    private func fail(_ message: String) {
        lastError = message
        WeftLog.write("setup error: \(message)")
    }

    // Copy the bundled binary onto PATH: /opt/homebrew/bin when user-writable,
    // else /usr/local/bin behind an admin prompt.
    private func doInstallCLI() async {
        guard fm.fileExists(atPath: bundledCLI) else {
            fail("bundled weft binary missing — rebuild the app with build.sh")
            return
        }
        if fm.isWritableFile(atPath: "/opt/homebrew/bin") {
            let dst = "/opt/homebrew/bin/weft"
            do {
                if fm.fileExists(atPath: dst) { try fm.removeItem(atPath: dst) }
                try fm.copyItem(atPath: bundledCLI, toPath: dst)
                try fm.setAttributes([.posixPermissions: 0o755], ofItemAtPath: dst)
            } catch {
                fail("CLI install failed: \(error.localizedDescription)")
            }
        } else {
            let line = "mkdir -p /usr/local/bin && /usr/bin/install -m 0755 "
                + Shell.quoted(bundledCLI) + " /usr/local/bin/weft"
            let res = await Shell.runAsAdmin(line)
            if !res.ok { fail("CLI install failed: \(res.stderr)") }
        }
    }

    // Write the LaunchAgent plist, then bootout (stale copies; failure is fine)
    // + bootstrap. If an unmanaged daemon holds :7777, launchd's KeepAlive will
    // keep retrying and takes over the moment the user stops that process —
    // nothing gets killed by us.
    private func doInstallDaemon() async {
        guard let cliPath = installedCLIPath else { return }
        // `weft serve` exits immediately on a missing vault, and KeepAlive=true
        // would respawn it every ~10s forever — validate before enrolling.
        var isDir: ObjCBool = false
        guard fm.fileExists(atPath: expandedVault, isDirectory: &isDir), isDir.boolValue else {
            fail("vault folder not found: \(expandedVault) — create it or fix the path first")
            return
        }
        let logPath = home + "/Library/Logs/weft-daemon.log"
        let plist: [String: Any] = [
            "Label": Self.agentLabel,
            "ProgramArguments": [cliPath, "serve", expandedVault],
            "RunAtLoad": true,
            "KeepAlive": true,
            "StandardOutPath": logPath,
            "StandardErrorPath": logPath,
        ]
        do {
            try fm.createDirectory(atPath: home + "/Library/LaunchAgents", withIntermediateDirectories: true)
            try fm.createDirectory(atPath: home + "/Library/Logs", withIntermediateDirectories: true)
            let data = try PropertyListSerialization.data(fromPropertyList: plist, format: .xml, options: 0)
            try data.write(to: URL(fileURLWithPath: Self.agentPlistPath))
        } catch {
            fail("could not write LaunchAgent: \(error.localizedDescription)")
            return
        }
        _ = await Shell.run("/bin/launchctl", ["bootout", "gui/\(getuid())/\(Self.agentLabel)"])
        let res = await Shell.run("/bin/launchctl", ["bootstrap", "gui/\(getuid())", Self.agentPlistPath])
        if !res.ok {
            fail("launchctl bootstrap failed: \(res.stderr)")
            return
        }
        // Give the daemon a beat to bind before the re-check probes it.
        try? await Task.sleep(nanoseconds: 1_500_000_000)
    }

    // Delete-and-replace is fine: the App Support copy is our artifact, never
    // user-edited. Then put the path on the clipboard and land the user on
    // chrome://extensions (the Chrome binary directly — `open` rejects chrome: URLs).
    private func doInstallExtension() async {
        do {
            if fm.fileExists(atPath: extensionDest) { try fm.removeItem(atPath: extensionDest) }
            try fm.createDirectory(
                atPath: (extensionDest as NSString).deletingLastPathComponent,
                withIntermediateDirectories: true
            )
            try fm.copyItem(atPath: bundledExtension, toPath: extensionDest)
        } catch {
            fail("extension copy failed: \(error.localizedDescription)")
            return
        }
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(extensionDest, forType: .string)

        let chromeBin = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
        if fm.isExecutableFile(atPath: chromeBin) {
            Shell.launch(chromeBin, ["chrome://extensions"])
        } else if let url = NSWorkspace.shared.urlForApplication(withBundleIdentifier: "com.google.Chrome") {
            NSWorkspace.shared.open(url) // chrome:// can't be deep-linked here; the steps popover covers it
        } else {
            fail("Google Chrome not found — open chrome://extensions yourself once it's installed")
        }
        showChromeSteps = true
    }

    // remove-then-add so re-running converges instead of erroring on a
    // pre-existing entry; the `weft` entry is ours to own.
    private func doInstallMCP() async {
        guard let claude = claudePath, let cliPath = installedCLIPath else { return }
        _ = await Shell.run("/usr/bin/env", [claude, "mcp", "remove", "-s", "user", "weft"])
        let res = await Shell.run(
            "/usr/bin/env",
            [claude, "mcp", "add", "--scope", "user", "weft", "--", cliPath, "mcp", expandedVault]
        )
        if !res.ok { fail("claude mcp add failed: \(res.stderr)") }
    }

    // ~/.claude/CLAUDE.md is user-owned: only the marker-delimited block is ever
    // touched. Command files are copied only when absent for the same reason.
    private func doInstallMemory() async {
        guard let block = try? String(contentsOfFile: bundledClaude + "/claude-md-section.md", encoding: .utf8)
        else {
            fail("bundled claude templates missing — rebuild the app with build.sh")
            return
        }
        let trimmedBlock = block.trimmingCharacters(in: .whitespacesAndNewlines)
        do {
            try fm.createDirectory(atPath: home + "/.claude", withIntermediateDirectories: true)
            if fm.fileExists(atPath: claudeMDPath) {
                // `try?` alone would conflate "no file" with "unreadable"
                // (non-UTF-8 byte, EACCES) and replace the user's entire
                // CLAUDE.md with just our block. Fail loudly instead.
                guard var content = try? String(contentsOfFile: claudeMDPath, encoding: .utf8) else {
                    fail("could not read ~/.claude/CLAUDE.md (permissions? non-UTF-8?) — fix it, then retry")
                    return
                }
                switch Self.markerState(in: content) {
                case .wellFormed(let blockRange):
                    content.replaceSubrange(blockRange, with: trimmedBlock)
                case .absent:
                    content += (content.hasSuffix("\n") ? "\n" : "\n\n") + trimmedBlock + "\n"
                case .malformed:
                    fail("weft markers in ~/.claude/CLAUDE.md are orphaned or duplicated — repair by hand, then retry")
                    return
                }
                try content.write(toFile: claudeMDPath, atomically: true, encoding: .utf8)
            } else {
                try (trimmedBlock + "\n").write(toFile: claudeMDPath, atomically: true, encoding: .utf8)
            }
            try fm.createDirectory(atPath: claudeCommandsDir, withIntermediateDirectories: true)
            for name in ["vault-log.md", "vault-context.md"] {
                let dst = claudeCommandsDir + "/" + name
                if !fm.fileExists(atPath: dst) {
                    try fm.copyItem(atPath: bundledClaude + "/" + name, toPath: dst)
                }
            }
        } catch {
            fail("memory wiring failed: \(error.localizedDescription)")
        }
    }

    // MARK: - Helpers

    /// Locate a command the way the user's terminal would: well-known dirs
    /// first, then the login shell's PATH — the only thing that knows about
    /// nvm/volta/bun/custom npm prefixes. (A `which` via Shell.run would just
    /// re-search Shell.searchPath, finding nothing new.)
    private func resolveCommand(_ name: String) async -> String? {
        let fixed = [
            "/opt/homebrew/bin/\(name)",
            "/usr/local/bin/\(name)",
            home + "/.local/bin/\(name)",
        ]
        if let hit = fixed.first(where: { fm.isExecutableFile(atPath: $0) }) { return hit }
        let shell = ProcessInfo.processInfo.environment["SHELL"] ?? "/bin/zsh"
        let res = await Shell.run(shell, ["-lc", "command -v \(name)"], timeout: 15)
        // Last line skips rc-file chatter; the prefix check skips shell
        // functions/aliases, which `command -v` can also report.
        guard res.ok,
              let path = res.stdout.split(separator: "\n").last.map(String.init),
              path.hasPrefix("/"), fm.isExecutableFile(atPath: path)
        else { return nil }
        return path
    }

    nonisolated private static func sha256(_ path: String) -> String? {
        guard let data = try? Data(contentsOf: URL(fileURLWithPath: path), options: .mappedIfSafe)
        else { return nil }
        return SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
    }
}
