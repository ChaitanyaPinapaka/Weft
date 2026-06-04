import Foundation

// The note-body stylesheet, injected into the reader WebView. Adapted from
// web/viewer.css's prose rules but tuned for a focused native reading column.
// Background is transparent so the SwiftUI window's --bg shows through;
// light/dark follow the system (WKWebView honors prefers-color-scheme).
// The viewport meta makes the same column responsive on a phone.
public let readerCSS = """
:root {
  --bg: #fafafa; --surface: #ffffff; --border: #e8e8e8;
  --text: #1a1a1a; --muted: #888; --accent: #2563eb; --radius: 6px;
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #0f0f0f; --surface: #1a1a1a; --border: #2a2a2a;
    --text: #e8e8e8; --muted: #666; --accent: #60a5fa;
  }
}
* { box-sizing: border-box; }
html, body { margin: 0; padding: 0; background: var(--bg); }
body {
  color: var(--text);
  font-family: -apple-system, system-ui, sans-serif;
  font-size: 17px; line-height: 1.7;
  -webkit-font-smoothing: antialiased;
  text-rendering: optimizeLegibility;
}
.reader { max-width: 680px; margin: 0 auto; padding: 56px 44px 140px; }
@media (max-width: 600px) { .reader { padding: 28px 20px 120px; } }
article { overflow-wrap: anywhere; }
h1 { font-size: 2rem; font-weight: 700; letter-spacing: -0.022em; line-height: 1.2; margin: 0 0 0.5em; }
h2 { font-size: 1.4rem; font-weight: 650; letter-spacing: -0.012em; margin: 1.7em 0 0.45em; }
h3 { font-size: 1.15rem; font-weight: 650; margin: 1.4em 0 0.3em; }
p { margin: 0 0 1em; }
a { color: var(--accent); text-decoration: none; }
a:hover { text-decoration: underline; }
strong { font-weight: 650; }
code {
  font-family: ui-monospace, SFMono-Regular, monospace; font-size: 0.86em;
  background: color-mix(in srgb, var(--border) 60%, transparent);
  padding: 1px 5px; border-radius: 4px;
}
pre {
  font-family: ui-monospace, SFMono-Regular, monospace; font-size: 0.85em; line-height: 1.5;
  background: var(--surface); border: 1px solid var(--border);
  border-radius: var(--radius); padding: 14px 16px; overflow-x: auto; margin: 1em 0;
}
pre code { background: none; padding: 0; }
blockquote {
  border-left: 3px solid var(--border); margin: 1em 0;
  padding: 0.15em 0 0.15em 16px; color: var(--muted);
}
blockquote.capture { border-left-color: var(--accent); }
blockquote.capture time {
  font-family: ui-monospace, monospace; font-size: 0.78em;
  color: var(--muted); margin-right: 8px;
}
ul, ol { padding-left: 1.4em; margin: 0 0 1em; }
li { margin: 0.2em 0; }
img { max-width: 100%; height: auto; border-radius: var(--radius); }
hr { border: none; border-top: 1px solid var(--border); margin: 2.4em 0; }
table { border-collapse: collapse; margin: 1em 0; }
td, th { border: 1px solid var(--border); padding: 6px 10px; text-align: left; }
::selection { background: color-mix(in srgb, var(--accent) 25%, transparent); }
a.wiki-broken { color: #b45309; border-bottom: 1px dotted; }
@media (prefers-color-scheme: dark) { a.wiki-broken { color: #fbbf24; } }
"""

// Wrap a note's <article> inner HTML in a full, styled document the WebView can
// load. baseURL on load resolves any relative anchors against the host/scheme.
public func readerDocument(bodyHTML: String) -> String {
    """
    <!DOCTYPE html>
    <html><head><meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <style>\(readerCSS)</style></head>
    <body><div class="reader"><article>\(bodyHTML)</article></div></body></html>
    """
}
