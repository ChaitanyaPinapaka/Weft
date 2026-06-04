import SwiftUI
import WeftKit

@main
struct WeftApp: App {
    @State private var model = AppModel()

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(model)
                .task { await model.boot() }
        }
    }
}
