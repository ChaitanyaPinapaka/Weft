import Foundation

// A parsed note ready to render: the <article> inner HTML plus the lifted H1
// title. Notes are editor-produced, well-formed HTML wrapped in <article>, so a
// light substring scan is enough — no need to pull a DOM parser into the app.
public struct ReaderDoc {
    public let title: String
    public let bodyHTML: String
    /// The note's vault path, carried alongside the body so the reader keys on a
    /// value that is always consistent with the rendered content (the current
    /// path can lag the fetch).
    public let path: String

    public init(title: String, bodyHTML: String, path: String) {
        self.title = title
        self.bodyHTML = bodyHTML
        self.path = path
    }

    public static func parse(rawHTML: String, path: String, fallbackTitle: String) -> ReaderDoc {
        let body = innerHTML(of: "article", in: rawHTML)
            ?? innerHTML(of: "body", in: rawHTML)
            ?? rawHTML
        let title = firstH1Text(in: body) ?? fallbackTitle
        return ReaderDoc(title: title, bodyHTML: body, path: path)
    }

    /// Inner HTML of the first <tag …>…</tag>, case-insensitive. nil if absent.
    private static func innerHTML(of tag: String, in html: String) -> String? {
        let lower = html.lowercased()
        guard let openStart = lower.range(of: "<\(tag)") else { return nil }
        // end of the opening tag (handles attributes)
        guard let openEnd = html.range(of: ">", range: openStart.upperBound..<html.endIndex) else { return nil }
        guard let close = lower.range(of: "</\(tag)>", range: openEnd.upperBound..<lower.endIndex) else { return nil }
        return String(html[openEnd.upperBound..<close.lowerBound]).trimmingCharacters(in: .whitespacesAndNewlines)
    }

    /// Visible text of the first <h1>…</h1>, tags stripped, entities un-escaped.
    private static func firstH1Text(in html: String) -> String? {
        let lower = html.lowercased()
        guard let openStart = lower.range(of: "<h1"),
              let openEnd = html.range(of: ">", range: openStart.upperBound..<html.endIndex),
              let close = lower.range(of: "</h1>", range: openEnd.upperBound..<lower.endIndex)
        else { return nil }
        let inner = String(html[openEnd.upperBound..<close.lowerBound])
        let text = inner.replacingOccurrences(of: "<[^>]+>", with: "", options: .regularExpression)
            .replacingOccurrences(of: "&amp;", with: "&")
            .replacingOccurrences(of: "&lt;", with: "<")
            .replacingOccurrences(of: "&gt;", with: ">")
            .trimmingCharacters(in: .whitespacesAndNewlines)
        return text.isEmpty ? nil : text
    }
}
