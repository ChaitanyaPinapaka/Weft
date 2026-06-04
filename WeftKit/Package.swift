// swift-tools-version:5.9
import PackageDescription

// WeftKit — the platform-neutral core shared by the macOS and iOS surfaces:
// the Codable data models, the reader stylesheet + HTML parse, the "Weft look"
// theme tokens, and the WeftBackend protocol that abstracts the transport (HTTP
// daemon on macOS, embedded gomobile engine on iOS). No AppKit/UIKit-only code
// lives here except behind canImport seams, so both apps depend on one source.
let package = Package(
    name: "WeftKit",
    platforms: [.macOS(.v14), .iOS(.v17)],
    products: [
        .library(name: "WeftKit", targets: ["WeftKit"]),
    ],
    targets: [
        .target(name: "WeftKit"),
    ]
)
