import AppIntents

/// Siri / Shortcuts / Action-Button capture. Lives in the main app target (no
/// intents extension): the system launches the app in the background to run
/// it, and the intent writes straight to the App-Group inbox — the same
/// producer contract as the share extension — so capture works even when the
/// app is closed. The next launch/foreground drains the queue into the daily
/// note (AppModel.drainInbox).
struct CaptureToWeft: AppIntent {
    static var title: LocalizedStringResource = "Capture to Weft"
    static var description = IntentDescription("Save a quick thought to your Weft vault.")

    @Parameter(title: "Text", requestValueDialog: "What should I capture?")
    var text: String

    static var parameterSummary: some ParameterSummary {
        Summary("Capture \(\.$text) to Weft")
    }

    func perform() async throws -> some IntentResult & ProvidesDialog {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else {
            return .result(dialog: "Nothing to capture.")
        }
        try InboxQueue.enqueue(text: trimmed)
        return .result(dialog: "Captured to Weft.")
    }
}

struct WeftAppShortcuts: AppShortcutsProvider {
    static var appShortcuts: [AppShortcut] {
        AppShortcut(
            intent: CaptureToWeft(),
            phrases: [
                "Capture to \(.applicationName)",
                "Capture a thought in \(.applicationName)",
            ],
            shortTitle: "Capture",
            systemImageName: "square.and.pencil")
    }
}
