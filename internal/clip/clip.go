// Package clip provides shared helpers for turning a captured web page into
// a self-contained HTML note in the vault. Used by both the daemon's
// POST /api/clip endpoint and the `weft clip` CLI subcommand.
package clip

import (
	"bytes"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Clean takes the raw HTML of a captured page and its source URL, returning
// a cleaned, self-contained HTML document suitable for the vault. See the
// package doc for the full contract.
func Clean(rawHTML []byte, sourceURL string) ([]byte, string, error) {
	// html.Parse never returns an error for malformed input; it wraps everything
	// into a well-formed tree with implicit <html>/<head>/<body> as needed.
	doc, err := html.Parse(bytes.NewReader(rawHTML))
	if err != nil {
		return nil, "", err
	}

	stripDangerous(doc)

	head := findFirst(doc, atom.Head)
	if head == nil {
		// html.Parse synthesizes <head>, but guard against pathological inputs.
		head = &html.Node{Type: html.ElementNode, Data: "head", DataAtom: atom.Head}
		if htmlEl := findFirst(doc, atom.Html); htmlEl != nil {
			htmlEl.InsertBefore(head, htmlEl.FirstChild)
		}
	}

	// Collision policy: our injected <base> wins. We remove any pre-existing
	// <base> so the saved file resolves relative URLs against the original
	// origin regardless of what the source page declared.
	if sourceURL != "" {
		removeAll(head, atom.Base)
		base := &html.Node{
			Type:     html.ElementNode,
			Data:     "base",
			DataAtom: atom.Base,
			Attr:     []html.Attribute{{Key: "href", Val: sourceURL}},
		}
		head.InsertBefore(base, head.FirstChild)
	}

	title := extractTitle(doc, sourceURL)

	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), title, nil
}

// slugNonAlnum matches runs of characters that are not lowercase ASCII
// alphanumerics; we collapse those into a single hyphen. Non-ASCII is
// intentionally excluded — filenames stay portable.
var slugNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// Slug produces a filename-safe slug. See package doc for the contract.
func Slug(title string) string {
	s := strings.ToLower(title)
	s = slugNonAlnum.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "untitled"
	}
	if len(s) > 60 {
		// Truncate on a word boundary (hyphen) if one exists in the tail.
		cut := s[:60]
		if i := strings.LastIndex(cut, "-"); i > 0 {
			cut = cut[:i]
		}
		s = strings.Trim(cut, "-")
		if s == "" {
			return "untitled"
		}
	}
	return s
}

// ClipPath returns "clips/YYYY-MM-DD-{slug}.html" for the given time and slug.
// Caller is responsible for disk-collision suffixing.
func ClipPath(t time.Time, slug string) string {
	return fmt.Sprintf("clips/%s-%s.html", t.Format("2006-01-02"), slug)
}

// --- internal helpers ---

// dangerousAttrPrefix flags inline event handlers (onclick, onload, etc.)
// which would re-execute when the saved file is opened locally.
const dangerousAttrPrefix = "on"

func stripDangerous(n *html.Node) {
	// Walk depth-first, collecting nodes to remove so we don't mutate the
	// sibling list while iterating it.
	var toRemove []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.DataAtom {
			case atom.Script, atom.Noscript:
				toRemove = append(toRemove, n)
				return
			case atom.Link:
				if isPreloadScript(n) {
					toRemove = append(toRemove, n)
					return
				}
			}
			n.Attr = stripEventHandlers(n.Attr)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	for _, node := range toRemove {
		if node.Parent != nil {
			node.Parent.RemoveChild(node)
		}
	}
}

func isPreloadScript(n *html.Node) bool {
	var rel, as string
	for _, a := range n.Attr {
		switch strings.ToLower(a.Key) {
		case "rel":
			rel = strings.ToLower(a.Val)
		case "as":
			as = strings.ToLower(a.Val)
		}
	}
	return rel == "preload" && as == "script"
}

func stripEventHandlers(attrs []html.Attribute) []html.Attribute {
	out := attrs[:0]
	for _, a := range attrs {
		if strings.HasPrefix(strings.ToLower(a.Key), dangerousAttrPrefix) && len(a.Key) > 2 {
			continue
		}
		out = append(out, a)
	}
	return out
}

func findFirst(n *html.Node, a atom.Atom) *html.Node {
	if n.Type == html.ElementNode && n.DataAtom == a {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findFirst(c, a); found != nil {
			return found
		}
	}
	return nil
}

func removeAll(parent *html.Node, a atom.Atom) {
	var toRemove []*html.Node
	for c := parent.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.DataAtom == a {
			toRemove = append(toRemove, c)
		}
	}
	for _, n := range toRemove {
		parent.RemoveChild(n)
	}
}

func textContent(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(b.String())
}

func extractTitle(doc *html.Node, sourceURL string) string {
	if t := findFirst(doc, atom.Title); t != nil {
		if s := textContent(t); s != "" {
			return s
		}
	}
	if h := findFirst(doc, atom.H1); h != nil {
		if s := textContent(h); s != "" {
			return s
		}
	}
	if sourceURL != "" {
		if u, err := url.Parse(sourceURL); err == nil && u.Host != "" {
			return u.Host
		}
	}
	return ""
}
