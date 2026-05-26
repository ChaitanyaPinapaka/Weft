// Package notion imports a Notion HTML export into the vault.
//
// Notion exports place each page at `{Title} {32hex}.html` with a sibling
// folder `{Title} {32hex}/` holding its children. We strip the hash, slug
// the title, mirror the hierarchy under `notion/`, rewrite internal links,
// convert CSV exports to HTML tables, and copy referenced assets alongside.
package notion

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"html"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	xhtml "golang.org/x/net/html"

	"weft/internal/vault"
)

// Options controls import behavior.
type Options struct {
	Force bool
}

// Report summarizes an import run.
type Report struct {
	Imported []string
	Skipped  []string
	Errors   []error
}

// notionHash matches Notion's trailing " {32hex}" before an extension (or
// at the end of a directory name). The space is required — slugs without
// the suffix are left alone.
var notionHash = regexp.MustCompile(`[ \-]([0-9a-fA-F]{32})$`)

// slugNonAlnum collapses runs of non-alnum into a single hyphen, matching
// the clip.Slug rule. Duplicated here to avoid an import cycle / cross-
// package coupling for a one-line regex.
var slugNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// Import walks src and copies the Notion export into v under `notion/`.
func Import(src string, v *vault.Vault, opts Options) Report {
	var rep Report

	absSrc, err := filepath.Abs(src)
	if err != nil {
		rep.Errors = append(rep.Errors, err)
		return rep
	}

	// Pass 1: build a rename map from the export-relative original path
	// (forward slashes, as they appear in links after URL-decoding) to the
	// vault-relative slugged path under `notion/`.
	renames := map[string]string{}
	var files []string // export-relative paths of regular files

	err = filepath.WalkDir(absSrc, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if p == absSrc {
			return nil
		}
		rel, _ := filepath.Rel(absSrc, p)
		rel = filepath.ToSlash(rel)
		newRel := slugRelPath(rel, d.IsDir())
		renames[rel] = newRel
		if !d.IsDir() {
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		rep.Errors = append(rep.Errors, err)
		return rep
	}

	// Pass 2: process each file by extension.
	for _, rel := range files {
		newRel := path.Join("notion", renames[rel])
		ext := strings.ToLower(path.Ext(rel))
		absPath := filepath.Join(absSrc, filepath.FromSlash(rel))

		switch ext {
		case ".html", ".htm":
			data, rerr := os.ReadFile(absPath)
			if rerr != nil {
				rep.Errors = append(rep.Errors, fmt.Errorf("read %s: %w", rel, rerr))
				continue
			}
			out, rerr := rewriteHTML(data, rel, renames)
			if rerr != nil {
				rep.Errors = append(rep.Errors, fmt.Errorf("rewrite %s: %w", rel, rerr))
				continue
			}
			if write(v, newRel, out, opts, &rep) {
				rep.Imported = append(rep.Imported, newRel)
			}
		case ".csv":
			data, rerr := os.ReadFile(absPath)
			if rerr != nil {
				rep.Errors = append(rep.Errors, fmt.Errorf("read %s: %w", rel, rerr))
				continue
			}
			out, rerr := csvToHTML(data, titleFromFilename(rel))
			if rerr != nil {
				rep.Errors = append(rep.Errors, fmt.Errorf("csv %s: %w", rel, rerr))
				continue
			}
			if write(v, newRel, out, opts, &rep) {
				rep.Imported = append(rep.Imported, newRel)
			}
		default:
			// Assets: copy bytes verbatim under the slugged path.
			data, rerr := os.ReadFile(absPath)
			if rerr != nil {
				rep.Errors = append(rep.Errors, fmt.Errorf("read %s: %w", rel, rerr))
				continue
			}
			if write(v, newRel, data, opts, &rep) {
				rep.Imported = append(rep.Imported, newRel)
			}
		}
	}

	return rep
}

// write applies the overwrite policy and records skips/errors.
func write(v *vault.Vault, rel string, data []byte, opts Options, rep *Report) bool {
	if v.Exists(rel) && !opts.Force {
		rep.Skipped = append(rep.Skipped, rel)
		return false
	}
	if err := v.Write(rel, data); err != nil {
		rep.Errors = append(rep.Errors, fmt.Errorf("write %s: %w", rel, err))
		return false
	}
	return true
}

// slugRelPath strips hashes and slugs each path segment. For .html and .csv
// the extension is preserved; for directories there is no extension. For
// asset files we also strip the hash but keep the original extension and
// only slug the stem (so `image.png` stays readable).
func slugRelPath(rel string, isDir bool) string {
	parts := strings.Split(rel, "/")
	for i, seg := range parts {
		last := i == len(parts)-1
		if !last || isDir {
			parts[i] = slugDirSegment(seg)
			continue
		}
		ext := strings.ToLower(path.Ext(seg))
		switch ext {
		case ".html", ".htm":
			parts[i] = slugDirSegment(strings.TrimSuffix(seg, path.Ext(seg))) + ".html"
		case ".csv":
			parts[i] = slugDirSegment(strings.TrimSuffix(seg, path.Ext(seg))) + ".html"
		default:
			stem := strings.TrimSuffix(seg, path.Ext(seg))
			parts[i] = slugDirSegment(stem) + ext
		}
	}
	return strings.Join(parts, "/")
}

// slugDirSegment strips Notion's hash suffix from a name and slugs it.
func slugDirSegment(name string) string {
	name = notionHash.ReplaceAllString(name, "")
	s := strings.ToLower(name)
	s = slugNonAlnum.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "untitled"
	}
	return s
}

// titleFromFilename derives a human-readable title from a filename by
// stripping the hash and extension.
func titleFromFilename(rel string) string {
	base := path.Base(rel)
	base = strings.TrimSuffix(base, path.Ext(base))
	base = notionHash.ReplaceAllString(base, "")
	return strings.TrimSpace(base)
}

// rewriteHTML parses the page and rewrites <a href> / <img src> references
// that point at known files in the export.
func rewriteHTML(data []byte, pageRel string, renames map[string]string) ([]byte, error) {
	doc, err := xhtml.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	pageDir := path.Dir(pageRel) // export-relative directory of this page

	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if n.Type == xhtml.ElementNode {
			for i, a := range n.Attr {
				key := strings.ToLower(a.Key)
				if (n.Data == "a" && key == "href") || ((n.Data == "img" || n.Data == "source") && key == "src") {
					if newVal, ok := rewriteRef(a.Val, pageDir, renames); ok {
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

	var buf bytes.Buffer
	if err := xhtml.Render(&buf, doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// rewriteRef resolves href against the page's directory in the export,
// looks up the target in renames, and returns a new path relative to the
// page's new location in the vault. Returns ok=false for refs we leave alone.
func rewriteRef(ref, pageDir string, renames map[string]string) (string, bool) {
	if ref == "" {
		return "", false
	}
	// Leave absolute URLs, fragments, mailto:, etc. alone.
	if strings.HasPrefix(ref, "#") || strings.HasPrefix(ref, "mailto:") || strings.HasPrefix(ref, "tel:") {
		return "", false
	}
	if u, err := url.Parse(ref); err == nil && u.Scheme != "" {
		return "", false
	}

	// Split off a trailing fragment so we can resolve the path portion only.
	frag := ""
	if i := strings.Index(ref, "#"); i >= 0 {
		frag = ref[i:]
		ref = ref[:i]
	}

	decoded, err := url.PathUnescape(ref)
	if err != nil {
		decoded = ref
	}

	target := path.Join(pageDir, decoded)
	target = path.Clean(target)
	if target == "." {
		return "", false
	}

	newTarget, ok := renames[target]
	if !ok {
		return "", false
	}

	// Compute the link relative to this page's NEW directory so it resolves
	// once both files live under `notion/` in the vault.
	pageNewDir := "."
	if pageDir != "." && pageDir != "" {
		pageNewDir = slugRelPath(pageDir, true)
	}
	rel, err := filepath.Rel(filepath.FromSlash(pageNewDir), filepath.FromSlash(newTarget))
	if err != nil {
		return "", false
	}
	// Slugs are ASCII [a-z0-9-]; no URL escaping needed.
	return filepath.ToSlash(rel) + frag, true
}

// csvToHTML renders rows as a self-contained HTML doc with a <table>.
func csvToHTML(data []byte, title string) ([]byte, error) {
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	buf.WriteString("<!DOCTYPE html><html><head><meta charset=\"utf-8\"><title>")
	buf.WriteString(html.EscapeString(title))
	buf.WriteString("</title></head><body><article><h1>")
	buf.WriteString(html.EscapeString(title))
	buf.WriteString("</h1><table>")
	if len(rows) > 0 {
		buf.WriteString("<thead><tr>")
		for _, cell := range rows[0] {
			buf.WriteString("<th>")
			buf.WriteString(html.EscapeString(cell))
			buf.WriteString("</th>")
		}
		buf.WriteString("</tr></thead>")
	}
	if len(rows) > 1 {
		buf.WriteString("<tbody>")
		for _, row := range rows[1:] {
			buf.WriteString("<tr>")
			for _, cell := range row {
				buf.WriteString("<td>")
				buf.WriteString(html.EscapeString(cell))
				buf.WriteString("</td>")
			}
			buf.WriteString("</tr>")
		}
		buf.WriteString("</tbody>")
	}
	buf.WriteString("</table></article></body></html>")
	return buf.Bytes(), nil
}

