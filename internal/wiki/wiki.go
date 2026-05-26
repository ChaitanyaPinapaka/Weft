// Package wiki provides pure functions for expanding [[wikilink]] tokens
// in HTML bodies into anchor tags, and for rewriting links across the
// vault when a note is renamed.
//
// All functions operate on body fragments (not full HTML documents): the
// caller passes the inner-body HTML and gets inner-body HTML back.
package wiki

import (
	"bytes"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// wikilinkRe matches [[name]] and [[name|display]]. The character classes
// forbid [, ], and | inside name (and [, ] inside display) so adjacent
// tokens like [[a]][[b]] don't collapse into a single greedy match, and
// stray brackets in prose don't get half-swallowed.
var wikilinkRe = regexp.MustCompile(`\[\[([^\[\]\|]+)(?:\|([^\[\]]+))?\]\]`)

// skipElements are tags whose descendant text must not have wikilink
// tokens expanded. <a> is included to avoid nesting anchors; the others
// are verbatim/code contexts where [[ is meaningful as literal text.
var skipElements = map[string]bool{
	"a":      true,
	"pre":    true,
	"code":   true,
	"script": true,
	"style":  true,
}

// ExpandWikilinks converts [[name]] tokens in htmlBody into <a> tags.
//
// vaultPaths is the slice of vault-relative paths (e.g. "notes/foo.html",
// "daily/2026-05-25.html"). Matching is name-based: the link's "name" is
// compared case-insensitively against the basename of each path (minus
// the .html suffix). The slice is sorted before lookup so the first
// match is deterministic.
//
// Match found → <a href="path/to/file.html">name</a>
// No match    → <a href="<slug>.html" class="wiki-broken">name</a>
// Alt text    → [[name|display]] uses display as link text.
//
// Tokens inside <pre>, <code>, <script>, <style>, or <a> are left alone.
// Skip-element nesting is tracked by depth, so <a><code>[[x]]</code></a>
// is correctly skipped on the way back up.
func ExpandWikilinks(htmlBody []byte, vaultPaths []string) ([]byte, error) {
	if len(htmlBody) == 0 {
		return []byte{}, nil
	}

	lookup := buildLookup(vaultPaths)

	body := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragment(bytes.NewReader(htmlBody), body)
	if err != nil {
		return nil, err
	}
	for _, n := range nodes {
		body.AppendChild(n)
	}

	expandWalk(body, lookup, 0)

	var buf bytes.Buffer
	for c := body.FirstChild; c != nil; c = c.NextSibling {
		if err := html.Render(&buf, c); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// RewriteWikilinks updates every anchor in htmlBody whose href equals
// oldPath so it points at newPath instead. Used during rename. Link text
// and other attributes (including class="wiki-broken") are preserved —
// the broken-link class is left in place because it's a stale-data
// concern best resolved by re-running ExpandWikilinks, not by guessing
// here.
func RewriteWikilinks(htmlBody []byte, oldPath, newPath string) ([]byte, error) {
	if len(htmlBody) == 0 {
		return []byte{}, nil
	}

	body := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragment(bytes.NewReader(htmlBody), body)
	if err != nil {
		return nil, err
	}
	for _, n := range nodes {
		body.AppendChild(n)
	}

	rewriteWalk(body, oldPath, newPath)

	var buf bytes.Buffer
	for c := body.FirstChild; c != nil; c = c.NextSibling {
		if err := html.Render(&buf, c); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// buildLookup returns a lowercase-basename → vault-path map. Paths are
// sorted alphabetically so that if two files share a basename, the first
// in sort order wins deterministically.
func buildLookup(paths []string) map[string]string {
	sorted := make([]string, len(paths))
	copy(sorted, paths)
	sort.Strings(sorted)

	m := make(map[string]string, len(sorted))
	for _, p := range sorted {
		key := strings.ToLower(strings.TrimSuffix(filepath.Base(p), ".html"))
		if _, exists := m[key]; !exists {
			m[key] = p
		}
	}
	return m
}

// expandWalk recurses through the parsed tree, expanding tokens in text
// nodes whenever skipDepth == 0. skipDepth is incremented when entering
// a skip element and decremented on the way out, so that arbitrarily
// nested skip contexts are handled correctly.
func expandWalk(n *html.Node, lookup map[string]string, skipDepth int) {
	if n.Type == html.ElementNode && skipElements[n.Data] {
		skipDepth++
	}

	if n.Type == html.TextNode && skipDepth == 0 && wikilinkRe.MatchString(n.Data) {
		replaceTextNode(n, lookup)
		return
	}

	// Snapshot children: expandWalk may mutate the sibling list when it
	// replaces a text node with multiple new nodes.
	var children []*html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		children = append(children, c)
	}
	for _, c := range children {
		expandWalk(c, lookup, skipDepth)
	}
}

// replaceTextNode splits n.Data on wikilink matches and replaces n with
// a sequence of text + anchor nodes in its parent.
func replaceTextNode(n *html.Node, lookup map[string]string) {
	matches := wikilinkRe.FindAllStringSubmatchIndex(n.Data, -1)
	if len(matches) == 0 {
		return
	}

	parent := n.Parent
	if parent == nil {
		return
	}

	text := n.Data
	var fragments []*html.Node
	cursor := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		nameStart, nameEnd := m[2], m[3]
		displayStart, displayEnd := m[4], m[5]

		if start > cursor {
			fragments = append(fragments, &html.Node{
				Type: html.TextNode,
				Data: text[cursor:start],
			})
		}

		name := text[nameStart:nameEnd]
		display := name
		if displayStart != -1 {
			display = text[displayStart:displayEnd]
		}

		fragments = append(fragments, buildAnchor(name, display, lookup))
		cursor = end
	}
	if cursor < len(text) {
		fragments = append(fragments, &html.Node{
			Type: html.TextNode,
			Data: text[cursor:],
		})
	}

	for _, f := range fragments {
		parent.InsertBefore(f, n)
	}
	parent.RemoveChild(n)
}

// buildAnchor constructs <a href="..." [class="wiki-broken"]>display</a>.
// Match key is the lowercased trimmed name (no slug transformation —
// "hello world" matches "hello world.html", not "hello-world.html").
// The broken-link slug DOES use hyphenation, so the user has a stable
// URL to create the missing note at.
func buildAnchor(name, display string, lookup map[string]string) *html.Node {
	key := strings.ToLower(strings.TrimSpace(name))
	a := &html.Node{
		Type:     html.ElementNode,
		Data:     "a",
		DataAtom: atom.A,
		Attr:     []html.Attribute{{Key: "href"}},
	}

	if path, ok := lookup[key]; ok {
		a.Attr[0].Val = path
	} else {
		slug := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(name), " ", "-"))
		a.Attr[0].Val = slug + ".html"
		a.Attr = append(a.Attr, html.Attribute{Key: "class", Val: "wiki-broken"})
	}

	a.AppendChild(&html.Node{Type: html.TextNode, Data: display})
	return a
}

// rewriteWalk visits every <a> in the tree and rewrites href when it
// matches oldPath. Non-anchor descendants are still traversed.
func rewriteWalk(n *html.Node, oldPath, newPath string) {
	if n.Type == html.ElementNode && n.Data == "a" {
		for i, attr := range n.Attr {
			if attr.Key == "href" && attr.Val == oldPath {
				n.Attr[i].Val = newPath
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		rewriteWalk(c, oldPath, newPath)
	}
}
