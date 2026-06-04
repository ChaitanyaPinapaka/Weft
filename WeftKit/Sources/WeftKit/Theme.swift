import SwiftUI
#if canImport(UIKit)
import UIKit
#elseif canImport(AppKit)
import AppKit
#endif

// The "Weft look", ported from web/style.css verbatim: near-monochrome neutrals,
// a single blue accent, automatic light/dark. The hex token table and the type
// scale are identical on both platforms; only the dynamic-color provider differs
// (UIColor traitCollection on iOS, NSColor appearance on macOS).
public enum Weft {
    public static let radius: CGFloat = 6

    public static let bg      = dynamic(light: "fafafa", dark: "0f0f0f")
    public static let surface = dynamic(light: "ffffff", dark: "1a1a1a")
    public static let border  = dynamic(light: "e8e8e8", dark: "2a2a2a")
    public static let text    = dynamic(light: "1a1a1a", dark: "e8e8e8")
    public static let muted   = dynamic(light: "888888", dark: "666666")
    public static let accent  = dynamic(light: "2563eb", dark: "60a5fa")
    public static let danger  = dynamic(light: "c0392b", dark: "ff7a6e")

    static func dynamic(light: String, dark: String) -> Color {
        #if canImport(UIKit)
        return Color(uiColor: UIColor { trait in
            UIColor(hex: trait.userInterfaceStyle == .dark ? dark : light)
        })
        #else
        return Color(nsColor: NSColor(name: nil) { appearance in
            let isDark = appearance.bestMatch(from: [.aqua, .darkAqua]) == .darkAqua
            return NSColor(hex: isDark ? dark : light)
        })
        #endif
    }
}

#if canImport(UIKit)
extension UIColor {
    convenience init(hex: String) {
        var v: UInt64 = 0
        Scanner(string: hex).scanHexInt64(&v)
        self.init(
            red: CGFloat((v >> 16) & 0xff) / 255,
            green: CGFloat((v >> 8) & 0xff) / 255,
            blue: CGFloat(v & 0xff) / 255,
            alpha: 1
        )
    }
}
#elseif canImport(AppKit)
extension NSColor {
    convenience init(hex: String) {
        var v: UInt64 = 0
        Scanner(string: hex).scanHexInt64(&v)
        self.init(
            srgbRed: CGFloat((v >> 16) & 0xff) / 255,
            green: CGFloat((v >> 8) & 0xff) / 255,
            blue: CGFloat(v & 0xff) / 255,
            alpha: 1
        )
    }
}
#endif

extension Font {
    // Small-caps section label — the signature .brain-heading treatment.
    public static let weftLabel = Font.system(size: 11, weight: .semibold).width(.standard)
    public static let weftMono = Font.system(size: 11, design: .monospaced)
}
