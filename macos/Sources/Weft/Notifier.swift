import UserNotifications
import AppKit
import WeftKit

// Native notifications for proactive surfacing. Guarded behind a bundle check:
// UNUserNotificationCenter traps when the process isn't a signed .app bundle
// (e.g. `swift run`), so unbundled runs simply skip it — the in-window toast and
// the ✦ inbox still convey every push.
@MainActor
final class Notifier: NSObject, UNUserNotificationCenterDelegate {
    static let shared = Notifier()

    private var available: Bool { Bundle.main.bundleIdentifier != nil }

    func configure() {
        WeftLog.write("Notifier.configure available=\(available) bundleId=\(Bundle.main.bundleIdentifier ?? "nil")")
        guard available else { return }
        let center = UNUserNotificationCenter.current()
        center.delegate = self
        center.getNotificationSettings { s in
            WeftLog.write("auth status before request = \(s.authorizationStatus.rawValue)")
        }
        center.requestAuthorization(options: [.alert, .sound, .badge]) { granted, error in
            WeftLog.write("requestAuthorization granted=\(granted) error=\(error.map { String(describing: $0) } ?? "nil")")
        }
    }

    func post(_ ev: SurfaceEvent) {
        WeftLog.write("Notifier.post available=\(available) path=\(ev.path ?? "nil")")
        guard available, let path = ev.path else { return }
        let content = UNMutableNotificationContent()
        content.title = "Weft · " + reasonLabel(ev.reason ?? "surfaced")
        content.body = ev.displayTitle + (ev.detail.map { " — \($0)" } ?? "")
        content.userInfo = ["path": path]
        let request = UNNotificationRequest(identifier: path, content: content, trigger: nil)
        UNUserNotificationCenter.current().add(request) { error in
            WeftLog.write("post add error = \(error.map { String(describing: $0) } ?? "nil")")
        }
    }

    // Tapping a notification activates Weft and opens the surfaced note.
    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        didReceive response: UNNotificationResponse,
        withCompletionHandler completionHandler: @escaping () -> Void
    ) {
        let path = response.notification.request.content.userInfo["path"] as? String
        Task { @MainActor in
            NSApp.activate(ignoringOtherApps: true)
            if let path { AppModel.shared.open(path: path) }
            completionHandler()
        }
    }

    // Show the banner even when Weft is frontmost.
    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        willPresent notification: UNNotification,
        withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void
    ) {
        completionHandler([.banner])
    }
}
