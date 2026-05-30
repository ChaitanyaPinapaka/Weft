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

// AppendCapture inserts a single <aside class="capture"> block into today's
// daily note, creating the note if it does not already exist. Returns the
// vault-relative path of the daily note.
//
// The capture is inserted as the last child of the note's <article> (falling
// back to <body>), NOT appended to the raw byte stream — a naive append lands
// after </article></body></html> once the note is a full HTML document, where
// no surface (editor/viewer reads the <article>) would ever render it. Any
// previously-orphaned captures are also relocated into the target, so this
// self-heals notes broken by the old append behavior.
//
// t supplies the daily-note date (in t's location) and the <time> display;
// data-ts records UTC RFC3339.
func (v *Vault) AppendCapture(t time.Time, text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", ErrEmptyCapture
	}

	rel, err := v.EnsureDaily(t)
	if err != nil {
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

// newCapture builds the <aside class="capture" data-ts="..."><time>HH:MM</time> text</aside>
// node. Text is a TextNode, so html.Render escapes it — no manual escaping.
func newCapture(t time.Time, text string) *html.Node {
	aside := &html.Node{
		Type: html.ElementNode, Data: "aside", DataAtom: atom.Aside,
		Attr: []html.Attribute{
			{Key: "class", Val: "capture"},
			{Key: "data-ts", Val: t.UTC().Format(time.RFC3339)},
		},
	}
	timeEl := &html.Node{Type: html.ElementNode, Data: "time", DataAtom: atom.Time}
	timeEl.AppendChild(&html.Node{Type: html.TextNode, Data: t.Format("15:04")})
	aside.AppendChild(timeEl)
	aside.AppendChild(&html.Node{Type: html.TextNode, Data: " " + text})
	return aside
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

// collectCaptures gathers every <aside class="capture"> node (without recursing
// into one). Collected before any mutation so detaching them later is safe.
func collectCaptures(n *html.Node, out *[]*html.Node) {
	if n.Type == html.ElementNode && n.Data == "aside" && hasClass(n, "capture") {
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
