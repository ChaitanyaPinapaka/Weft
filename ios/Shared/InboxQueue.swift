import Foundation

/// Producer side of the App-Group capture queue, compiled into the share
/// extension and the main app (for App Intents). One JSON file per capture —
/// {text, source?, ts} — in <group>/inbox/; AppModel.drainInbox consumes them
/// on launch/foreground and appends each to the daily note.
///
/// Producers are write-only by design: extensions must never link the gomobile
/// engine (extension memory limits, and the engine's vault/index state must
/// have exactly one owner — the app process).
enum InboxQueue {
    static let appGroupID = "group.app.tryweft.shared"

    struct Item: Encodable {
        let text: String
        let source: String?
        let ts: String
    }

    /// Queue one capture. No-op for blank text (the consumer drops empty
    /// entries anyway). File names lead with a fixed-width epoch-millisecond
    /// timestamp so the consumer's lexicographic sort preserves capture order.
    static func enqueue(text: String, source: String? = nil) throws {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        guard let container = FileManager.default
            .containerURL(forSecurityApplicationGroupIdentifier: appGroupID) else {
            throw NSError(
                domain: "app.tryweft.inbox", code: 1,
                userInfo: [NSLocalizedDescriptionKey: "App group container unavailable."])
        }
        let inbox = container.appendingPathComponent("inbox", isDirectory: true)
        try FileManager.default.createDirectory(at: inbox, withIntermediateDirectories: true)

        let now = Date()
        let ms = UInt64(now.timeIntervalSince1970 * 1000)
        let name = String(format: "%013llu-%@.json", ms, UUID().uuidString)
        let item = Item(text: trimmed, source: source, ts: ISO8601DateFormatter().string(from: now))
        try JSONEncoder().encode(item).write(to: inbox.appendingPathComponent(name), options: .atomic)
    }
}
