import SwiftUI
import WeftKit

@main
struct WeftApp: App {
    @State private var model = AppModel()
    @Environment(\.scenePhase) private var scenePhase

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(model)
                .task { await model.boot() }
                // weft://capture, weft://daily, weft://note?path=…
                .onOpenURL { model.handleURL($0) }
        }
        .onChange(of: scenePhase) { _, phase in
            // Consume captures queued by the extensions while we were away.
            // (Also runs once at launch — boot() drains too, but drainInbox is
            // a no-op until the engine is open, so the overlap is harmless.)
            if phase == .active {
                Task { await model.drainInbox() }
            }
        }
    }
}
