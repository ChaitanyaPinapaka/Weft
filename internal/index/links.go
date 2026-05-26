package index

import (
	"bytes"
	"path"
	"sort"
	"strings"

	"golang.org/x/net/html"
)

// ParseLinks extracts vault-relative href targets from an HTML body.
// External, fragment-only, and non-vault schemes are skipped. Results are
// lowercased, deduped, and returned in sorted order.
func ParseLinks(htmlBody []byte) []string {
	seen := map[string]struct{}{}
	z := html.NewTokenizer(bytes.NewReader(htmlBody))
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
			continue
		}
		name, hasAttr := z.TagName()
		if !hasAttr || len(name) != 1 || name[0] != 'a' {
			continue
		}
		for {
			k, v, more := z.TagAttr()
			if string(k) == "href" {
				if norm, ok := normalizeHref(string(v)); ok {
					seen[norm] = struct{}{}
				}
				break
			}
			if !more {
				break
			}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func normalizeHref(h string) (string, bool) {
	h = strings.TrimSpace(h)
	if h == "" || strings.HasPrefix(h, "#") {
		return "", false
	}
	lower := strings.ToLower(h)
	for _, p := range []string{"http://", "https://", "mailto:", "javascript:"} {
		if strings.HasPrefix(lower, p) {
			return "", false
		}
	}
	// Drop any in-page fragment before extension check.
	if i := strings.IndexByte(h, '#'); i >= 0 {
		h = h[:i]
	}
	if i := strings.IndexByte(h, '?'); i >= 0 {
		h = h[:i]
	}
	h = strings.TrimPrefix(h, "./")
	h = strings.TrimPrefix(h, "/")
	if h == "" {
		return "", false
	}
	if path.Ext(h) == "" {
		h += ".html"
	}
	return strings.ToLower(h), true
}
