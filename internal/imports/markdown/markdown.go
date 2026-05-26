// Package markdown imports a tree of .md files (Obsidian-flavored) into the
// vault as self-contained .html notes. Obsidian/GFM is the assumed dialect:
// frontmatter, [[wikilinks]], tables, task lists, strikethrough, autolinks.
//
// The importer is deliberately a single-pass file walker — no link graph is
// built ahead of time, no two-pass resolution. Wikilinks are slugged on the
// way in; the resulting <a href> may dangle if the target note isn't imported
// in the same run, and that's fine: the vault is the source of truth and
// dangling refs are surfaced later by the brain panel.
package markdown

import (
	"bytes"
	"fmt"
	"html"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"

	"weft/internal/vault"
)

// Options controls the import run.
type Options struct {
	Force bool      // overwrite existing notes
	Now   time.Time // for any "imported-at" decisions; default = time.Now() if zero
}

// Report summarizes what happened during an Import call.
type Report struct {
	Imported []string // vault-relative paths written
	Skipped  []string // skipped because dst existed and !Force
	Errors   []error
}

// Import walks src for .md files and converts each to .html in the vault.
// See package doc and the task spec for the full contract.
func Import(src string, v *vault.Vault, opts Options) Report {
	var rep Report
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}

	absSrc, err := filepath.Abs(src)
	if err != nil {
		rep.Errors = append(rep.Errors, fmt.Errorf("resolve src: %w", err))
		return rep
	}

	md := goldmark.New(goldmark.WithExtensions(extension.GFM))

	err = filepath.WalkDir(absSrc, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			rep.Errors = append(rep.Errors, walkErr)
			return nil
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".md") {
			return nil
		}
		if err := importOne(p, absSrc, v, md, opts, &rep); err != nil {
			rep.Errors = append(rep.Errors, fmt.Errorf("%s: %w", p, err))
		}
		return nil
	})
	if err != nil {
		rep.Errors = append(rep.Errors, err)
	}
	return rep
}

func importOne(srcFile, srcRoot string, v *vault.Vault, md goldmark.Markdown, opts Options, rep *Report) error {
	raw, err := os.ReadFile(srcFile)
	if err != nil {
		return err
	}

	rel, err := filepath.Rel(srcRoot, srcFile)
	if err != nil {
		return err
	}
	relSlash := filepath.ToSlash(rel)
	dstRel := strings.TrimSuffix(relSlash, filepath.Ext(relSlash)) + ".html"

	if !opts.Force && v.Exists(dstRel) {
		rep.Skipped = append(rep.Skipped, dstRel)
		return nil
	}

	body, fmTitle := stripFrontmatter(raw)
	body = expandWikilinks(body)

	// Image rewriting needs to know the markdown's location relative to srcRoot
	// so it can copy files preserving the subdir structure (and skip refs
	// that escape srcRoot). Errors from image copies are appended to rep —
	// they don't fail the whole note.
	srcDir := filepath.Dir(srcFile)
	body = rewriteImages(body, srcDir, srcRoot, dstRel, v, rep)

	var rendered bytes.Buffer
	if err := md.Convert(body, &rendered); err != nil {
		return fmt.Errorf("render markdown: %w", err)
	}

	title := fmTitle
	if title == "" {
		title = firstH1(rendered.Bytes())
	}
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(srcFile), filepath.Ext(srcFile))
	}

	out := assemble(title, rendered.Bytes())
	if err := v.Write(dstRel, out); err != nil {
		return err
	}
	rep.Imported = append(rep.Imported, dstRel)
	return nil
}

func assemble(title string, body []byte) []byte {
	var b bytes.Buffer
	b.WriteString("<!DOCTYPE html>\n<html>\n<head><meta charset=\"utf-8\"><title>")
	b.WriteString(html.EscapeString(title))
	b.WriteString("</title></head>\n<body><article>")
	b.Write(body)
	b.WriteString("</article></body>\n</html>\n")
	return b.Bytes()
}

// --- frontmatter ---

// yamlTitle matches `title: foo`, optionally with surrounding quotes. We
// deliberately do NOT pull in a yaml parser — that's a heavy dep for one
// field. Anything fancier than a flat string is ignored.
var yamlTitle = regexp.MustCompile(`(?mi)^title\s*:\s*(.+?)\s*$`)

// stripFrontmatter removes a leading `---\n...\n---\n` block (if present)
// and returns the body plus the extracted title (empty if none). Malformed
// frontmatter (no closing fence) returns the original body untouched.
func stripFrontmatter(b []byte) (body []byte, title string) {
	if !bytes.HasPrefix(b, []byte("---\n")) && !bytes.HasPrefix(b, []byte("---\r\n")) {
		return b, ""
	}
	// Find the closing `\n---` fence. Allow either LF or CRLF terminators on
	// the fence line; also allow EOF (no trailing newline after closing ---).
	rest := b[4:]
	if bytes.HasPrefix(b, []byte("---\r\n")) {
		rest = b[5:]
	}
	end := -1
	endLen := 0
	for _, sep := range [][]byte{
		[]byte("\n---\n"),
		[]byte("\n---\r\n"),
		[]byte("\r\n---\r\n"),
		[]byte("\r\n---\n"),
	} {
		if i := bytes.Index(rest, sep); i >= 0 {
			if end < 0 || i < end {
				end = i
				endLen = len(sep)
			}
		}
	}
	// Also handle the case where `---` is the very last line with no newline.
	if end < 0 {
		if bytes.HasSuffix(rest, []byte("\n---")) {
			end = len(rest) - len("\n---")
			endLen = len("\n---")
		} else if bytes.HasSuffix(rest, []byte("\r\n---")) {
			end = len(rest) - len("\r\n---")
			endLen = len("\r\n---")
		}
	}
	if end < 0 {
		return b, ""
	}
	frontmatter := rest[:end]
	body = rest[end+endLen:]

	if m := yamlTitle.FindSubmatch(frontmatter); m != nil {
		title = unquoteYAML(string(m[1]))
	}
	return body, title
}

func unquoteYAML(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			s = s[1 : len(s)-1]
		}
	}
	return s
}

// --- wikilinks ---

// slugNonAlnum matches the same rule clip.Slug uses: anything not lowercase
// ASCII alnum becomes a hyphen. Kept private (and duplicated) so this package
// does not depend on internal/clip.
var slugNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	s = strings.ToLower(s)
	s = slugNonAlnum.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "untitled"
	}
	return s
}

// expandWikilinks rewrites [[name]] and [[name|display]] to standard
// markdown links, but never inside fenced code blocks or inline code spans.
// We do this BEFORE handing to goldmark so the link rendering goes through
// the normal renderer (and gets html-escaped, etc).
func expandWikilinks(b []byte) []byte {
	s := string(b)
	var out strings.Builder
	out.Grow(len(s))

	inFence := false
	fenceMarker := "" // "```" or "~~~"
	inInline := false
	inlineMarker := byte(0)
	inlineRun := 0 // number of backticks/tildes that opened the inline span

	i := 0
	for i < len(s) {
		// Detect start/end of fenced code blocks at line start.
		if !inInline && atLineStart(s, i) {
			if marker, n := detectFence(s, i); marker != "" {
				if inFence {
					if marker == fenceMarker {
						inFence = false
						fenceMarker = ""
					}
				} else {
					inFence = true
					fenceMarker = marker
				}
				out.WriteString(s[i : i+n])
				i += n
				// Consume rest of fence line verbatim.
				for i < len(s) && s[i] != '\n' {
					out.WriteByte(s[i])
					i++
				}
				continue
			}
		}

		if inFence {
			out.WriteByte(s[i])
			i++
			continue
		}

		// Inline code: a run of backticks opens; the same-length run closes.
		if !inInline && s[i] == '`' {
			run := 0
			for i+run < len(s) && s[i+run] == '`' {
				run++
			}
			inInline = true
			inlineMarker = '`'
			inlineRun = run
			out.WriteString(s[i : i+run])
			i += run
			continue
		}
		if inInline && s[i] == inlineMarker {
			run := 0
			for i+run < len(s) && s[i+run] == inlineMarker {
				run++
			}
			if run == inlineRun {
				inInline = false
				inlineMarker = 0
				inlineRun = 0
			}
			out.WriteString(s[i : i+run])
			i += run
			continue
		}
		if inInline {
			out.WriteByte(s[i])
			i++
			continue
		}

		// Wikilink: [[...]] or embed ![[...]]. Embeds become plain anchors
		// for now — v0.2 surfaces will upgrade them later.
		if s[i] == '[' && i+1 < len(s) && s[i+1] == '[' {
			end := strings.Index(s[i+2:], "]]")
			if end >= 0 {
				inner := s[i+2 : i+2+end]
				name, display := splitWiki(inner)
				if name != "" {
					fmt.Fprintf(&out, "[%s](%s.html)", escapeMDText(display), slug(name))
					i += 2 + end + 2
					continue
				}
			}
		}
		if s[i] == '!' && i+2 < len(s) && s[i+1] == '[' && s[i+2] == '[' {
			end := strings.Index(s[i+3:], "]]")
			if end >= 0 {
				inner := s[i+3 : i+3+end]
				name, display := splitWiki(inner)
				if name != "" {
					fmt.Fprintf(&out, "[%s](%s.html)", escapeMDText(display), slug(name))
					i += 3 + end + 2
					continue
				}
			}
		}

		out.WriteByte(s[i])
		i++
	}
	return []byte(out.String())
}

func splitWiki(s string) (name, display string) {
	if i := strings.Index(s, "|"); i >= 0 {
		name = strings.TrimSpace(s[:i])
		display = strings.TrimSpace(s[i+1:])
	} else {
		name = strings.TrimSpace(s)
		display = name
	}
	// Obsidian wikilinks can include a #heading or ^block anchor; drop them
	// from the slug target. Display text keeps the visible name as-is.
	if h := strings.IndexAny(name, "#^"); h >= 0 {
		name = strings.TrimSpace(name[:h])
	}
	return name, display
}

// escapeMDText keeps display text safe inside `[...]` — escape `]` and `\`.
func escapeMDText(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `]`, `\]`)
	return s
}

func atLineStart(s string, i int) bool {
	return i == 0 || s[i-1] == '\n'
}

// detectFence returns ("```", N) or ("~~~", N) if a fence of length >= 3
// starts at i (after optional 0–3 leading spaces), or ("", 0) otherwise.
func detectFence(s string, i int) (string, int) {
	j := i
	spaces := 0
	for j < len(s) && s[j] == ' ' && spaces < 3 {
		j++
		spaces++
	}
	if j >= len(s) {
		return "", 0
	}
	c := s[j]
	if c != '`' && c != '~' {
		return "", 0
	}
	run := 0
	for j+run < len(s) && s[j+run] == c {
		run++
	}
	if run < 3 {
		return "", 0
	}
	return strings.Repeat(string(c), run), (j - i) + run
}

// --- images ---

// imageRE captures simple inline images. Reference-style images and edge
// cases with nested brackets are not supported — Obsidian docs are
// overwhelmingly inline-style.
var imageRE = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)

func rewriteImages(body []byte, srcDir, srcRoot, dstRel string, v *vault.Vault, rep *Report) []byte {
	dstDir := path.Dir(filepath.ToSlash(dstRel))
	if dstDir == "." {
		dstDir = ""
	}

	return imageRE.ReplaceAllFunc(body, func(match []byte) []byte {
		m := imageRE.FindSubmatch(match)
		if m == nil {
			return match
		}
		alt := string(m[1])
		url := string(m[2])

		// Absolute / data URLs pass through.
		if isRemoteURL(url) {
			return match
		}

		// Resolve relative to the markdown file's directory.
		srcImg := filepath.Join(srcDir, filepath.FromSlash(url))
		srcImgAbs, err := filepath.Abs(srcImg)
		if err != nil {
			rep.Errors = append(rep.Errors, fmt.Errorf("image %q: %w", url, err))
			return match
		}
		// Refuse anything that escapes srcRoot.
		if !strings.HasPrefix(srcImgAbs, srcRoot+string(filepath.Separator)) && srcImgAbs != srcRoot {
			rep.Errors = append(rep.Errors, fmt.Errorf("image %q: outside src", url))
			return match
		}
		if _, err := os.Stat(srcImgAbs); err != nil {
			rep.Errors = append(rep.Errors, fmt.Errorf("image %q: %w", url, err))
			return match
		}

		// Preserve subpath relative to srcRoot so a single image referenced
		// by multiple notes ends up at one vault location.
		relFromRoot, err := filepath.Rel(srcRoot, srcImgAbs)
		if err != nil {
			rep.Errors = append(rep.Errors, fmt.Errorf("image %q: %w", url, err))
			return match
		}
		dstImgRel := filepath.ToSlash(relFromRoot)

		// Collision policy: overwrite. Image imports are idempotent — two
		// passes produce the same bytes — and skipping would silently leave
		// stale copies behind on re-import.
		if err := copyFileIntoVault(srcImgAbs, dstImgRel, v); err != nil {
			rep.Errors = append(rep.Errors, fmt.Errorf("image %q: %w", url, err))
			return match
		}

		// Rewrite the href to be relative to the html note's directory so
		// the file works when opened directly off disk.
		newURL := relativizeURL(dstDir, dstImgRel)
		return []byte(fmt.Sprintf("![%s](%s)", alt, newURL))
	})
}

func isRemoteURL(u string) bool {
	lu := strings.ToLower(u)
	return strings.HasPrefix(lu, "http://") ||
		strings.HasPrefix(lu, "https://") ||
		strings.HasPrefix(lu, "data:") ||
		strings.HasPrefix(lu, "//")
}

func relativizeURL(fromDir, target string) string {
	if fromDir == "" {
		return target
	}
	rel, err := filepath.Rel(filepath.FromSlash(fromDir), filepath.FromSlash(target))
	if err != nil {
		return target
	}
	return filepath.ToSlash(rel)
}

func copyFileIntoVault(srcAbs, dstRel string, v *vault.Vault) error {
	// Read once, write through Vault.Write so the path-traversal guard and
	// MkdirAll behavior apply uniformly.
	data, err := os.ReadFile(srcAbs)
	if err != nil {
		return err
	}
	return v.Write(dstRel, data)
}

// --- title extraction from rendered html ---

var h1RE = regexp.MustCompile(`(?is)<h1[^>]*>(.*?)</h1>`)
var tagRE = regexp.MustCompile(`<[^>]+>`)

func firstH1(rendered []byte) string {
	m := h1RE.FindSubmatch(rendered)
	if m == nil {
		return ""
	}
	inner := tagRE.ReplaceAll(m[1], nil)
	return strings.TrimSpace(html.UnescapeString(string(inner)))
}

