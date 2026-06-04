// swift-tools-version:5.9
import PackageDescription

// Weft — native macOS surface for the Weft daemon (localhost:7777).
// A thin client: WKWebView renders note bodies, SwiftUI owns the chrome,
// SSE delivers proactive surfacing. swift-tools 5.9 keeps the lenient
// concurrency model so the GUI code stays simple.
let package = Package(
    name: "Weft",
    platforms: [.macOS(.v14)],
    dependencies: [
        .package(path: "../WeftKit"),
    ],
    targets: [
        .executableTarget(
            name: "Weft",
            dependencies: [.product(name: "WeftKit", package: "WeftKit")]
        )
    ]
)
