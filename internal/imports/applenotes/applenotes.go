// Package applenotes imports Apple Notes exports (third-party HTML+assets shape)
// into a Weft vault. Apple Notes has no native HTML export; tools like
// Notes Exporter emit a folder of .html files with images/PDFs alongside.
package applenotes

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/net/html"

	"weft/internal/vault"
)

type Options struct {
	Force bool
}

type Report struct {
	Imported []string
	Skipped  []string
	Errors   []error
}

// Import walks src for .html files and copies each into the vault under
// `apple-notes/`, preserving relative subdir structure. Referenced asset
// files (images, PDFs, etc.) are copied alongside the destination note and
// references rewritten. Absolute URLs (http, https, mailto, etc.) are left
// alone. Asset collisions on disk are overwritten silently — the export's
// own asset is treated as authoritative. Existing vault notes are skipped
// unless opts.Force is set.
func Import(src string, v *vault.Vault, opts Options) Report {
	var rpt Report
	if v == nil {
		rpt.Errors = append(rpt.Errors, fmt.Errorf("nil vault"))
		return rpt
	}
	absSrc, err := filepath.Abs(src)
	if err != nil {
		rpt.Errors = append(rpt.Errors, err)
		return rpt
	}
	walkErr := filepath.WalkDir(absSrc, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			rpt.Errors = append(rpt.Errors, err)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(p), ".html") {
			return nil
		}
		if ierr := importOne(absSrc, p, v, opts, &rpt); ierr != nil {
			rpt.Errors = append(rpt.Errors, fmt.Errorf("%s: %w", p, ierr))
		}
		return nil
	})
	if walkErr != nil {
		rpt.Errors = append(rpt.Errors, walkErr)
	}
	return rpt
}

func importOne(srcRoot, notePath string, v *vault.Vault, opts Options, rpt *Report) error {
	raw, err := os.ReadFile(notePath)
	if err != nil {
		return err
	}
	doc, err := html.Parse(bytes.NewReader(raw))
	if err != nil {
		return err
	}

	title := extractTitle(doc)
	slug := slugify(title)

	rel, err := filepath.Rel(srcRoot, notePath)
	if err != nil {
		return err
	}
	subdir := strings.ToLower(filepath.ToSlash(filepath.Dir(rel)))
	if subdir == "." {
		subdir = ""
	}

	destRel := path.Join("apple-notes", subdir, slug+".html")
	if !opts.Force && v.Exists(destRel) {
		rpt.Skipped = append(rpt.Skipped, destRel)
		return nil
	}

	// Rewrite asset references in-place on the parsed tree. The source note's
	// directory is the resolution base; assets land alongside the destination.
	srcDir := filepath.Dir(notePath)
	destDirAbs := filepath.Join(v.Root, "apple-notes", subdir)
	rewriteAssets(doc, srcRoot, srcDir, destDirAbs)

	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil {
		return err
	}
	if err := v.Write(destRel, buf.Bytes()); err != nil {
		return err
	}
	rpt.Imported = append(rpt.Imported, destRel)
	return nil
}

// extractTitle walks the parsed doc once and returns the best-available title.
// Order: <title>, first <h1>, first non-empty text line, "untitled".
func extractTitle(doc *html.Node) string {
	var titleTag, h1Text, firstText string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "title":
				if titleTag == "" {
					titleTag = strings.TrimSpace(nodeText(n))
				}
			case "h1":
				if h1Text == "" {
					h1Text = strings.TrimSpace(nodeText(n))
				}
			case "script", "style":
				return
			}
		}
		if n.Type == html.TextNode && firstText == "" {
			for _, line := range strings.Split(n.Data, "\n") {
				t := strings.TrimSpace(line)
				if t != "" {
					firstText = t
					break
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	switch {
	case titleTag != "":
		return titleTag
	case h1Text != "":
		return h1Text
	case firstText != "":
		return firstText
	}
	return "untitled"
}

func nodeText(n *html.Node) string {
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

// slugify: lowercase + non-alnum→'-', collapse runs, trim, fallback "untitled".
func slugify(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
		} else {
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "untitled"
	}
	return out
}

// rewriteAssets finds <img src> and <a href> referencing local files, copies
// them to destDir, and rewrites the reference to a relative path. Absolute
// URLs (with scheme) and fragments are left alone. Missing files are left
// as-is; the broken link is preserved rather than silently dropped.
func rewriteAssets(doc *html.Node, srcRoot, srcDir, destDir string) {
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			var attr string
			switch n.Data {
			case "img":
				attr = "src"
			case "a":
				attr = "href"
			}
			if attr != "" {
				for i, a := range n.Attr {
					if a.Key != attr {
						continue
					}
					if newVal, ok := copyAssetRef(a.Val, srcRoot, srcDir, destDir); ok {
						n.Attr[i].Val = newVal
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
}

func copyAssetRef(ref, srcRoot, srcDir, destDir string) (string, bool) {
	if ref == "" {
		return "", false
	}
	if strings.HasPrefix(ref, "#") {
		return "", false
	}
	// Anything with a scheme (http, https, mailto, data, file, etc.) is left alone.
	if u, err := url.Parse(ref); err == nil && u.Scheme != "" {
		return "", false
	}
	// Strip fragment/query for filesystem lookup but preserve in output if copied.
	u, err := url.Parse(ref)
	if err != nil {
		return "", false
	}
	decodedPath, err := url.PathUnescape(u.Path)
	if err != nil {
		decodedPath = u.Path
	}
	if decodedPath == "" {
		return "", false
	}
	srcAsset := filepath.Join(srcDir, filepath.FromSlash(decodedPath))
	// Refuse anything that escapes the export root — a malicious export can carry
	// an href/src of "../../../../etc/passwd" to read arbitrary user-readable
	// files into the vault. Mirrors the markdown importer's containment check.
	srcAssetAbs, err := filepath.Abs(srcAsset)
	if err != nil {
		return "", false
	}
	rootAbs, err := filepath.Abs(srcRoot)
	if err != nil {
		return "", false
	}
	if srcAssetAbs != rootAbs && !strings.HasPrefix(srcAssetAbs, rootAbs+string(filepath.Separator)) {
		return "", false
	}
	info, err := os.Stat(srcAssetAbs)
	if err != nil || info.IsDir() {
		return "", false
	}
	base := filepath.Base(srcAsset)
	destAsset := filepath.Join(destDir, base)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", false
	}
	if err := copyFile(srcAsset, destAsset); err != nil {
		return "", false
	}
	newRef := url.PathEscape(base)
	// PathEscape over-escapes for path segments; restore safe chars.
	newRef = strings.ReplaceAll(newRef, "%2F", "/")
	if u.RawQuery != "" {
		newRef += "?" + u.RawQuery
	}
	if u.Fragment != "" {
		newRef += "#" + u.Fragment
	}
	return newRef, true
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
