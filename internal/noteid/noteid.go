// Package noteid manages a note's stable WeftID — a durable identity carried
// inside the canonical HTML as <meta name="weft-id" content="...">.
//
// Identity lives in the file (no sidecar, no config — honors HTML-is-the-format)
// so it survives out-of-band moves/copies (Finder, git, rsync). Sync uses it to
// track a note across renames: the href and on-disk path can change, the WeftID
// does not.
//
// The first ID for a note is DERIVED from its content + path, not random, so two
// devices that already hold the same note (e.g. copied via git before sync was
// enabled) mint the SAME id and converge instead of forking into duplicates.
package noteid

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"regexp"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const metaName = "weft-id"

// ReadWeftID returns the note's WeftID, or "" if none is present.
func ReadWeftID(content []byte) string {
	doc, err := html.Parse(bytes.NewReader(content))
	if err != nil {
		return ""
	}
	return findMetaID(doc)
}

// Derive computes the deterministic seed-ID for a note from its vault-relative
// path and content. Same (path, content) → same id on every device, so a note
// that predates sync converges rather than forking. Used only to MINT the first
// id; once stamped, the id is read from the file and never recomputed.
//
// The content is CANONICALIZED first (parsed, any weft-id meta stripped, then
// re-rendered) before hashing, so byte-trivia that does not change the note —
// a trailing newline, CRLF vs LF, a prior pass through html.Render (<br> vs
// <br/>, entity/attribute normalization) — cannot fork two devices' ids for
// the same logical note.
func Derive(rel string, content []byte) string {
	h := sha256.New()
	h.Write([]byte(rel))
	h.Write([]byte{0})
	h.Write(canonicalize(content))
	sum := h.Sum(nil)
	return formatID(sum[:16])
}

// weftMetaRe matches the weft-id meta tag regardless of attribute order or
// self-closing slash, so a stamped copy and a bare one canonicalize identically.
var weftMetaRe = regexp.MustCompile(`(?i)<meta[^>]*\bname="weft-id"[^>]*>`)

// canonicalize normalizes away byte-trivia that doesn't change the note: the
// weft-id meta itself (so stamped == bare), CRLF vs LF (git autocrlf), and
// leading/trailing whitespace (a trailing newline). Deliberately a light string
// pass, not parse+render — round-tripping through html.Parse reparents
// post-</html> whitespace into <body> and would itself fork the hash.
func canonicalize(content []byte) []byte {
	c := weftMetaRe.ReplaceAll(content, nil)
	c = bytes.ReplaceAll(c, []byte("\r\n"), []byte("\n"))
	return bytes.TrimSpace(c)
}

// WithWeftID returns content with its weft-id meta set to id, replacing any
// existing one. Used on save to PRESERVE a note's id even when the writer (the
// TipTap editor, the clipper) rebuilds <head> and would otherwise drop it.
func WithWeftID(content []byte, id string) ([]byte, error) {
	doc, err := html.Parse(bytes.NewReader(content))
	if err != nil {
		return nil, err
	}
	head := findElement(doc, atom.Head)
	if head == nil {
		// html.Parse synthesizes <head>; this is defensive.
		head = &html.Node{Type: html.ElementNode, Data: "head", DataAtom: atom.Head}
		if htmlEl := findElement(doc, atom.Html); htmlEl != nil {
			htmlEl.InsertBefore(head, htmlEl.FirstChild)
		}
	}
	// Remove any existing weft-id meta, then append the canonical one.
	for _, m := range metaNodes(head) {
		head.RemoveChild(m)
	}
	head.AppendChild(&html.Node{
		Type: html.ElementNode, Data: "meta", DataAtom: atom.Meta,
		Attr: []html.Attribute{{Key: "name", Val: metaName}, {Key: "content", Val: id}},
	})

	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Ensure stamps a derived WeftID iff the note lacks one, returning the (possibly
// rewritten) content, the id, and whether a change was made.
func Ensure(content []byte, rel string) (out []byte, id string, changed bool) {
	if existing := ReadWeftID(content); existing != "" {
		return content, existing, false
	}
	id = Derive(rel, content)
	out, err := WithWeftID(content, id)
	if err != nil {
		return content, id, false // couldn't inject; report the id, leave bytes as-is
	}
	return out, id, true
}

func formatID(b []byte) string {
	// UUID-shaped 8-4-4-4-12 hex from the first 16 bytes — recognizable and
	// URL-safe, but not a claimed RFC-4122 UUID (no version/variant bits).
	s := hex.EncodeToString(b)
	return s[0:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:32]
}

func findMetaID(n *html.Node) string {
	if n.Type == html.ElementNode && n.DataAtom == atom.Meta {
		var name, content string
		for _, a := range n.Attr {
			switch a.Key {
			case "name":
				name = a.Val
			case "content":
				content = a.Val
			}
		}
		if name == metaName {
			return content
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if id := findMetaID(c); id != "" {
			return id
		}
	}
	return ""
}

func metaNodes(head *html.Node) []*html.Node {
	var out []*html.Node
	for c := head.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.DataAtom == atom.Meta {
			for _, a := range c.Attr {
				if a.Key == "name" && a.Val == metaName {
					out = append(out, c)
				}
			}
		}
	}
	return out
}

func findElement(n *html.Node, a atom.Atom) *html.Node {
	if n.Type == html.ElementNode && n.DataAtom == a {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if f := findElement(c, a); f != nil {
			return f
		}
	}
	return nil
}
