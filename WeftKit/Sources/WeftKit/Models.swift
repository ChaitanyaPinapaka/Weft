import Foundation

// Codable mirrors of the daemon/engine JSON. The pull endpoints marshal Go
// structs with NO json tags, so their fields are PascalCase (Path/Title/…); the
// SSE/event contract and the top-level surface object use lowercase/snake_case.
// CodingKeys bridge both into idiomatic Swift names. Unknown keys (Accesses,
// Edges, Size…) are ignored by Codable, so we model only what the UI needs.
//
// These are shared by both surfaces because the iOS gomobile facade returns the
// exact same JSON shapes as the macOS HTTP daemon.

public struct NoteRef: Codable, Identifiable, Hashable {
    public let path: String
    public let name: String
    public let modTime: String

    public var id: String { path }

    enum CodingKeys: String, CodingKey {
        case path = "Path", name = "Name", modTime = "ModTime"
    }

    /// Top-level folder ("daily", "clips", …) or "" for vault root.
    public var folder: String {
        let parts = path.split(separator: "/")
        return parts.count > 1 ? String(parts[0]) : ""
    }

    /// Human title: last path component without the .html extension.
    public var title: String {
        (path as NSString).lastPathComponent.replacingOccurrences(of: ".html", with: "")
    }

    /// ModTime parsed to a Date for sorting. The daemon emits RFC3339 with a
    /// variable number of fractional-second digits, so try the fractional
    /// formatter first, then the plain one; .distantPast keeps unparseable
    /// values stably last.
    public var modDate: Date {
        let frac = ISO8601DateFormatter()
        frac.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let d = frac.date(from: modTime) { return d }
        let plain = ISO8601DateFormatter()
        plain.formatOptions = [.withInternetDateTime]
        return plain.date(from: modTime) ?? .distantPast
    }
}

public struct SurfacePayload: Codable {
    public let current: String?
    public let backlinks: [String]?
    public let scored: [Scored]?
    public let onThisDay: [Candidate]?
    public let trail: [String]?

    enum CodingKeys: String, CodingKey {
        case current, backlinks, scored, trail
        case onThisDay = "on_this_day"
    }

    public static let empty = SurfacePayload(current: nil, backlinks: nil, scored: nil, onThisDay: nil, trail: nil)
}

public struct Scored: Codable, Identifiable, Hashable {
    public let path: String
    public let title: String
    public let activation: Double
    public let base: Double
    public let spread: Double
    public let reasons: [String]?

    public var id: String { path }

    enum CodingKeys: String, CodingKey {
        case path = "Path", title = "Title"
        case activation = "Activation", base = "Base", spread = "Spread"
        case reasons = "Reasons"
    }

    public var isResurfaced: Bool { reasons?.contains("resurfaced") ?? false }

    public var displayTitle: String { (title as NSString).lastPathComponent }
}

public struct Candidate: Codable, Identifiable, Hashable {
    public let path: String
    public let title: String
    public let modTime: String

    public var id: String { path }

    enum CodingKeys: String, CodingKey {
        case path = "Path", title = "Title", modTime = "ModTime"
    }

    public var displayTitle: String { (title as NSString).lastPathComponent }

    /// Whole years between the note's mtime year and `now`. The daemon doesn't
    /// send a Years field, so we read the year off the RFC3339 prefix — robust
    /// against the many fractional-second digits the API emits.
    public func yearsAgo(now: Date = Date()) -> Int {
        guard let modYear = Int(modTime.prefix(4)) else { return 1 }
        let nowYear = Calendar.current.component(.year, from: now)
        return max(1, nowYear - modYear)
    }
}

// One Server-Sent / pushed surface event. type == "hello" on connect, "surface"
// otherwise. On iOS these are produced locally rather than over SSE, but the
// shape is identical so the inbox/toast UI is shared.
public struct SurfaceEvent: Codable, Identifiable, Hashable {
    public let type: String
    public let path: String?
    public let title: String?
    public let reason: String?
    public let detail: String?
    public let activation: Double?

    public init(type: String, path: String? = nil, title: String? = nil,
                reason: String? = nil, detail: String? = nil, activation: Double? = nil) {
        self.type = type
        self.path = path
        self.title = title
        self.reason = reason
        self.detail = detail
        self.activation = activation
    }

    public var id: String { (path ?? "") + "|" + (reason ?? "") }

    public var isSurface: Bool { type == "surface" && !(path ?? "").isEmpty }

    public var displayTitle: String { ((title ?? path ?? "") as NSString).lastPathComponent }
}

// Human label for a surfacing reason chip. Mirrors viewer.js chipLabel.
public func reasonLabel(_ reason: String) -> String {
    switch reason {
    case "resurfaced": return "resurfaced"
    case "semantic": return "related"
    case "backlink": return "linked"
    case "co-access": return "seen together"
    case "on-this-day": return "on this day"
    case "base": return "recent"
    default: return reason
    }
}
