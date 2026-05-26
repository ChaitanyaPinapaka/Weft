// Package bookmarks imports Netscape-format bookmark exports (Chrome, Firefox,
// Safari) into the vault as individual HTML notes.
package bookmarks

import (
	"fmt"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"

	"weft/internal/vault"
)

// Options controls Import behavior.
type Options struct {
	Force bool
}

// Report summarizes an import run.
type Report struct {
	Imported []string
	Skipped  []string
	Errors   []error
}

// slugRe matches runs of characters that are not lowercase ASCII alnum.
// These runs collapse to a single '-' in the final slug.
var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// Import parses a Netscape bookmarks HTML file at src and writes each bookmark
// as a self-contained note in v under bookmarks/{folder-path}/{title}.html.
func Import(src string, v *vault.Vault, opts Options) Report {
	var rep Report

	data, err := os.ReadFile(src)
	if err != nil {
		rep.Errors = append(rep.Errors, err)
		return rep
	}

	doc, err := html.Parse(strings.NewReader(string(data)))
	if err != nil {
		rep.Errors = append(rep.Errors, err)
		return rep
	}

	today := time.Now().Format("2006-01-02")
	seen := make(map[string]bool)

	// Walk the parsed tree maintaining a folder stack. The Netscape format
	// puts a folder title in <H3> and the folder's children in the *next*
	// <DL> sibling at the same level — so we drive the stack from <DL>
	// nodes, peeking back at the <H3> that immediately precedes the <DL>'s
	// containing <DT>. The browsers ship malformed HTML (unclosed <DT>/<p>),
	// which the html package normalizes for us.
	var stack []string
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch strings.ToLower(n.Data) {
			case "dl":
				folder := folderForDL(n)
				if folder != "" {
					stack = append(stack, folder)
					defer func() { stack = stack[:len(stack)-1] }()
				}
			case "a":
				href := attr(n, "href")
				if href == "" {
					break
				}
				title := strings.TrimSpace(textOf(n))
				rep.add(writeBookmark(v, opts, stack, href, title, today, seen))
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return rep
}

// folderForDL returns the slugged folder name for a <DL> node by looking at
// the <H3> that sits in the <DT> immediately preceding this <DL>. Returns ""
// for the root <DL> (no enclosing <DT>/<H3>).
func folderForDL(dl *html.Node) string {
	// In Netscape format the structure is:
	//   <DT><H3>Tech</H3>
	//   <DL>...</DL>
	// After html package normalization the <H3> ends up as a previous
	// sibling of the <DL> (since browsers don't close <DT>).
	for s := dl.PrevSibling; s != nil; s = s.PrevSibling {
		if s.Type != html.ElementNode {
			continue
		}
		if strings.ToLower(s.Data) == "h3" {
			return slug(textOf(s))
		}
		if strings.ToLower(s.Data) == "dt" {
			if h := findChild(s, "h3"); h != nil {
				return slug(textOf(h))
			}
		}
		// Stop at the previous bookmark/folder boundary.
		if strings.ToLower(s.Data) == "dl" {
			return ""
		}
	}
	return ""
}

func findChild(n *html.Node, tag string) *html.Node {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && strings.ToLower(c.Data) == tag {
			return c
		}
	}
	return nil
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

func textOf(n *html.Node) string {
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
	return b.String()
}

func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}

// hostFallback returns the URL host for use when no title is supplied.
func hostFallback(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Host
}

// writeBookmark renders and writes one bookmark, returning the vault path
// on success, the path with a sentinel on skip, or an error.
func writeBookmark(v *vault.Vault, opts Options, stack []string, href, title, today string, seen map[string]bool) (imported, skipped string, err error) {
	if seen[href] {
		return "", href, nil
	}
	seen[href] = true

	displayTitle := strings.TrimSpace(title)
	slugTitle := slug(displayTitle)
	if slugTitle == "" {
		host := hostFallback(href)
		if host == "" {
			return "", "", fmt.Errorf("bookmark with no usable title or host: %q", href)
		}
		displayTitle = host
		slugTitle = slug(host)
	}

	parts := append([]string{"bookmarks"}, stack...)
	parts = append(parts, slugTitle+".html")
	rel := path.Join(parts...)

	if v.Exists(rel) && !opts.Force {
		return "", rel, nil
	}

	body := fmt.Sprintf(`<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>%s</title></head>
<body><article>
<h1>%s</h1>
<p><a href="%s">%s</a></p>
<p class="meta">Imported %s</p>
</article></body></html>`,
		htmlEscape(displayTitle), htmlEscape(displayTitle),
		htmlEscape(href), htmlEscape(href), today)

	if werr := v.Write(rel, []byte(body)); werr != nil {
		return "", "", werr
	}
	return rel, "", nil
}

func htmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
	)
	return r.Replace(s)
}

func (r *Report) add(imported, skipped string, err error) {
	if err != nil {
		r.Errors = append(r.Errors, err)
		return
	}
	if imported != "" {
		r.Imported = append(r.Imported, imported)
	}
	if skipped != "" {
		r.Skipped = append(r.Skipped, skipped)
	}
}
