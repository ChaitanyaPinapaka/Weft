package vault

import (
	"bytes"
	"errors"
	"strings"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// ErrEmptyCapture is returned when AppendCapture is called with empty text.
// Capture is for thoughts; empty strings are noise, not signal.
var ErrEmptyCapture = errors.New("capture text is empty")

// AppendCapture inserts a single <blockquote class="capture"> block into
// today's daily note, creating the note if it does not already exist. Returns
// the vault-relative path of the daily note.
//
// A blockquote (not <aside>) is used so the capture survives the TipTap editor,
// whose schema preserves blockquotes but drops unknown elements like <aside> —
// a captured thought reads as a quoted line and persists through editing.
//
// The capture is inserted as the last child of the note's <article> (falling
// back to <body>), NOT appended to the raw byte stream — a naive append lands
// after </article></body></html> once the note is a full HTML document, where
// no surface (editor/viewer reads the <article>) would ever render it. Any
// previously-orphaned captures (including legacy <aside> ones) are relocated
// into the target, so this self-heals notes broken by the old append behavior.
//
// t supplies the daily-note date (in t's location) and the <time> display;
// data-ts records UTC RFC3339.
func (v *Vault) AppendCapture(t time.Time, text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", ErrEmptyCapture
	}

	// Hold the daily note's per-path lock across the whole ensure-read-parse-write
	// so a concurrent capture OR editor save can't clobber this append (and vice
	// versa). The path is deterministic from the date, so we can lock before
	// EnsureDaily creates it. Never lose a thought.
	rel := v.DailyPath(t)
	v.Lock(rel)
	defer v.Unlock(rel)

	if _, err := v.EnsureDaily(t); err != nil {
		return "", err
	}
	content, err := v.Read(rel)
	if err != nil {
		return "", err
	}

	doc, err := html.Parse(bytes.NewReader(content))
	if err != nil {
		return "", err
	}
	target := findElement(doc, "article")
	if target == nil {
		target = findElement(doc, "body")
	}
	if target == nil {
		return "", errors.New("daily note has no body to capture into")
	}

	// Relocate any existing capture blocks (including ones the old code stranded
	// after </html>, which the parser reparents into <body>) so all captures sit
	// together inside the target, then append the new one.
	var existing []*html.Node
	collectCaptures(doc, &existing)
	for _, a := range existing {
		if a.Parent != nil {
			a.Parent.RemoveChild(a)
		}
		target.AppendChild(a)
	}
	target.AppendChild(newCapture(t, text))

	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil {
		return "", err
	}
	if err := v.Write(rel, buf.Bytes()); err != nil {
		return "", err
	}
	return rel, nil
}

// newCapture builds <blockquote class="capture" data-ts="..."><p><time>HH:MM</time>
// text</p></blockquote>. The inner <p> is what TipTap's blockquote schema
// expects, so the capture round-trips cleanly through the editor. Text is a
// TextNode, so html.Render escapes it — no manual escaping.
func newCapture(t time.Time, text string) *html.Node {
	bq := &html.Node{
		Type: html.ElementNode, Data: "blockquote", DataAtom: atom.Blockquote,
		Attr: []html.Attribute{
			{Key: "class", Val: "capture"},
			{Key: "data-ts", Val: t.UTC().Format(time.RFC3339)},
		},
	}
	p := &html.Node{Type: html.ElementNode, Data: "p", DataAtom: atom.P}
	timeEl := &html.Node{Type: html.ElementNode, Data: "time", DataAtom: atom.Time}
	timeEl.AppendChild(&html.Node{Type: html.TextNode, Data: t.Format("15:04")})
	p.AppendChild(timeEl)
	p.AppendChild(&html.Node{Type: html.TextNode, Data: " " + text})
	bq.AppendChild(p)
	return bq
}

func findElement(n *html.Node, name string) *html.Node {
	if n.Type == html.ElementNode && n.Data == name {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if f := findElement(c, name); f != nil {
			return f
		}
	}
	return nil
}

// collectCaptures gathers every element carrying class "capture" — new
// <blockquote> captures and legacy <aside> ones alike — without recursing into
// one. Collected before any mutation so detaching them later is safe.
func collectCaptures(n *html.Node, out *[]*html.Node) {
	if n.Type == html.ElementNode && hasClass(n, "capture") {
		*out = append(*out, n)
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		collectCaptures(c, out)
	}
}

func hasClass(n *html.Node, want string) bool {
	for _, a := range n.Attr {
		if a.Key == "class" {
			for _, c := range strings.Fields(a.Val) {
				if c == want {
					return true
				}
			}
		}
	}
	return false
}
