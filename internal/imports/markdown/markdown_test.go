package markdown

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"weft/internal/vault"
)

// --- helpers ---

func newVault(t *testing.T) *vault.Vault {
	t.Helper()
	dir := t.TempDir()
	v, err := vault.New(dir)
	if err != nil {
		t.Fatalf("vault.New: %v", err)
	}
	return v
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func readVault(t *testing.T, v *vault.Vault, rel string) string {
	t.Helper()
	b, err := v.Read(rel)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// --- frontmatter ---

func TestStripFrontmatter_WithTitle(t *testing.T) {
	in := []byte("---\ntitle: Hello World\ntag: foo\n---\nbody here\n")
	body, title := stripFrontmatter(in)
	if title != "Hello World" {
		t.Errorf("title = %q, want Hello World", title)
	}
	if string(body) != "body here\n" {
		t.Errorf("body = %q", body)
	}
}

func TestStripFrontmatter_NoTitle(t *testing.T) {
	in := []byte("---\ntag: foo\n---\nbody\n")
	body, title := stripFrontmatter(in)
	if title != "" {
		t.Errorf("title = %q, want empty", title)
	}
	if string(body) != "body\n" {
		t.Errorf("body = %q", body)
	}
}

func TestStripFrontmatter_QuotedTitle(t *testing.T) {
	in := []byte("---\ntitle: \"Quoted: Title\"\n---\nx\n")
	_, title := stripFrontmatter(in)
	if title != "Quoted: Title" {
		t.Errorf("title = %q", title)
	}
}

func TestStripFrontmatter_Malformed(t *testing.T) {
	// no closing fence — body left untouched
	in := []byte("---\ntitle: x\nno closing fence here\n")
	body, title := stripFrontmatter(in)
	if title != "" {
		t.Errorf("malformed title = %q", title)
	}
	if string(body) != string(in) {
		t.Errorf("malformed body changed")
	}
}

func TestStripFrontmatter_EmptyBody(t *testing.T) {
	in := []byte("---\ntitle: x\n---\n")
	body, title := stripFrontmatter(in)
	if title != "x" {
		t.Errorf("title = %q", title)
	}
	if string(body) != "" {
		t.Errorf("expected empty body, got %q", body)
	}
}

func TestStripFrontmatter_None(t *testing.T) {
	in := []byte("# heading\nbody\n")
	body, title := stripFrontmatter(in)
	if title != "" || string(body) != string(in) {
		t.Errorf("non-frontmatter input got mangled")
	}
}

// --- wikilinks ---

func TestExpandWikilinks_Simple(t *testing.T) {
	got := string(expandWikilinks([]byte("see [[My Note]] here")))
	want := "see [My Note](my-note.html) here"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestExpandWikilinks_WithDisplay(t *testing.T) {
	got := string(expandWikilinks([]byte("[[target|click me]]")))
	want := "[click me](target.html)"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestExpandWikilinks_HeadingAnchor(t *testing.T) {
	got := string(expandWikilinks([]byte("[[My Note#section]]")))
	want := "[My Note#section](my-note.html)"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestExpandWikilinks_InsideFence(t *testing.T) {
	in := "before [[X]]\n```\n[[Y]] not converted\n```\nafter [[Z]]"
	got := string(expandWikilinks([]byte(in)))
	if !strings.Contains(got, "[X](x.html)") {
		t.Errorf("X not converted: %q", got)
	}
	if strings.Contains(got, "[Y](y.html)") {
		t.Errorf("Y inside fence WAS converted: %q", got)
	}
	if !strings.Contains(got, "[[Y]] not converted") {
		t.Errorf("Y not preserved verbatim: %q", got)
	}
	if !strings.Contains(got, "[Z](z.html)") {
		t.Errorf("Z (after fence) not converted: %q", got)
	}
}

func TestExpandWikilinks_InsideInlineCode(t *testing.T) {
	in := "use `[[X]]` to link"
	got := string(expandWikilinks([]byte(in)))
	if strings.Contains(got, "[X](x.html)") {
		t.Errorf("inline code wikilink WAS converted: %q", got)
	}
	if !strings.Contains(got, "`[[X]]`") {
		t.Errorf("inline code not preserved: %q", got)
	}
}

func TestExpandWikilinks_TildeFence(t *testing.T) {
	in := "~~~\n[[X]]\n~~~\n[[Y]]"
	got := string(expandWikilinks([]byte(in)))
	if strings.Contains(got, "[X](x.html)") {
		t.Errorf("X in ~~~ fence WAS converted")
	}
	if !strings.Contains(got, "[Y](y.html)") {
		t.Errorf("Y after fence not converted")
	}
}

// --- Import end-to-end ---

func TestImport_BasicHeadingsAndLists(t *testing.T) {
	v := newVault(t)
	src := t.TempDir()
	writeFile(t, src, "note.md", "# Title One\n\n- one\n- two\n- three\n")
	rep := Import(src, v, Options{})
	if len(rep.Errors) > 0 {
		t.Fatalf("errors: %v", rep.Errors)
	}
	if len(rep.Imported) != 1 || rep.Imported[0] != "note.html" {
		t.Fatalf("imported = %v", rep.Imported)
	}
	out := readVault(t, v, "note.html")
	if !strings.Contains(out, "<title>Title One</title>") {
		t.Errorf("title not set from h1: %s", out)
	}
	if !strings.Contains(out, "<h1>Title One</h1>") {
		t.Errorf("h1 missing: %s", out)
	}
	if !strings.Contains(out, "<ul>") || !strings.Contains(out, "<li>one</li>") {
		t.Errorf("list missing: %s", out)
	}
}

func TestImport_GFMTable(t *testing.T) {
	v := newVault(t)
	src := t.TempDir()
	writeFile(t, src, "t.md", "| a | b |\n|---|---|\n| 1 | 2 |\n")
	rep := Import(src, v, Options{})
	if len(rep.Errors) > 0 {
		t.Fatalf("errors: %v", rep.Errors)
	}
	out := readVault(t, v, "t.html")
	if !strings.Contains(out, "<table>") {
		t.Errorf("table not rendered: %s", out)
	}
}

func TestImport_CodeBlock(t *testing.T) {
	v := newVault(t)
	src := t.TempDir()
	writeFile(t, src, "c.md", "```go\nfunc x() {}\n```\n")
	rep := Import(src, v, Options{})
	if len(rep.Errors) > 0 {
		t.Fatalf("errors: %v", rep.Errors)
	}
	out := readVault(t, v, "c.html")
	if !strings.Contains(out, "<code") || !strings.Contains(out, "func x()") {
		t.Errorf("code block not rendered: %s", out)
	}
}

func TestImport_TitleFromFrontmatter(t *testing.T) {
	v := newVault(t)
	src := t.TempDir()
	writeFile(t, src, "n.md", "---\ntitle: FM Wins\n---\n# Body H1\n")
	rep := Import(src, v, Options{})
	if len(rep.Errors) > 0 {
		t.Fatalf("errors: %v", rep.Errors)
	}
	out := readVault(t, v, "n.html")
	if !strings.Contains(out, "<title>FM Wins</title>") {
		t.Errorf("frontmatter title not used: %s", out)
	}
}

func TestImport_TitleFromBasenameWhenNothingElse(t *testing.T) {
	v := newVault(t)
	src := t.TempDir()
	writeFile(t, src, "my-note.md", "just text, no h1\n")
	rep := Import(src, v, Options{})
	if len(rep.Errors) > 0 {
		t.Fatalf("errors: %v", rep.Errors)
	}
	out := readVault(t, v, "my-note.html")
	if !strings.Contains(out, "<title>my-note</title>") {
		t.Errorf("basename fallback failed: %s", out)
	}
}

func TestImport_SubdirsPreserved(t *testing.T) {
	v := newVault(t)
	src := t.TempDir()
	writeFile(t, src, "a/b/deep.md", "# Deep\n")
	rep := Import(src, v, Options{})
	if len(rep.Errors) > 0 {
		t.Fatalf("errors: %v", rep.Errors)
	}
	if !v.Exists("a/b/deep.html") {
		t.Errorf("subdir not preserved; imported = %v", rep.Imported)
	}
}

func TestImport_WikilinkRendered(t *testing.T) {
	v := newVault(t)
	src := t.TempDir()
	writeFile(t, src, "x.md", "see [[Other Note]]\n")
	rep := Import(src, v, Options{})
	if len(rep.Errors) > 0 {
		t.Fatalf("errors: %v", rep.Errors)
	}
	out := readVault(t, v, "x.html")
	if !strings.Contains(out, `href="other-note.html"`) {
		t.Errorf("wikilink href missing: %s", out)
	}
	if !strings.Contains(out, ">Other Note</a>") {
		t.Errorf("wikilink display text wrong: %s", out)
	}
}

func TestImport_ImageCopiedAndRewritten(t *testing.T) {
	v := newVault(t)
	src := t.TempDir()
	writeFile(t, src, "assets/pic.png", "PNGDATA")
	writeFile(t, src, "note.md", "![alt](assets/pic.png)\n")

	rep := Import(src, v, Options{})
	if len(rep.Errors) > 0 {
		t.Fatalf("errors: %v", rep.Errors)
	}
	if !v.Exists("assets/pic.png") {
		t.Errorf("image not copied to vault")
	}
	out := readVault(t, v, "note.html")
	// note.html is at vault root, image at assets/pic.png — relative URL is the same.
	if !strings.Contains(out, `src="assets/pic.png"`) {
		t.Errorf("image src not rewritten: %s", out)
	}
}

func TestImport_ImageInSubdirRewrittenRelative(t *testing.T) {
	v := newVault(t)
	src := t.TempDir()
	writeFile(t, src, "imgs/p.png", "X")
	writeFile(t, src, "deep/note.md", "![a](../imgs/p.png)\n")

	rep := Import(src, v, Options{})
	if len(rep.Errors) > 0 {
		t.Fatalf("errors: %v", rep.Errors)
	}
	if !v.Exists("imgs/p.png") {
		t.Errorf("image not copied")
	}
	out := readVault(t, v, "deep/note.html")
	if !strings.Contains(out, `src="../imgs/p.png"`) {
		t.Errorf("image src not rewritten to relative: %s", out)
	}
}

func TestImport_RemoteImageUntouched(t *testing.T) {
	v := newVault(t)
	src := t.TempDir()
	writeFile(t, src, "n.md", "![](https://example.com/x.png)\n")
	rep := Import(src, v, Options{})
	if len(rep.Errors) > 0 {
		t.Fatalf("errors: %v", rep.Errors)
	}
	out := readVault(t, v, "n.html")
	if !strings.Contains(out, `src="https://example.com/x.png"`) {
		t.Errorf("remote image was modified: %s", out)
	}
}

func TestImport_MissingImageRecordedInErrors(t *testing.T) {
	v := newVault(t)
	src := t.TempDir()
	writeFile(t, src, "n.md", "![](nope.png)\n")
	rep := Import(src, v, Options{})
	if len(rep.Errors) == 0 {
		t.Fatalf("expected error for missing image")
	}
	// The note still imports.
	if !v.Exists("n.html") {
		t.Errorf("note should still be written even when an image is missing")
	}
}

func TestImport_OverwriteFlag(t *testing.T) {
	v := newVault(t)
	src := t.TempDir()
	writeFile(t, src, "n.md", "# v1\n")
	if r := Import(src, v, Options{}); len(r.Errors) > 0 {
		t.Fatalf("first import errors: %v", r.Errors)
	}
	v1 := readVault(t, v, "n.html")

	// Re-author the source, import again without Force — should skip.
	writeFile(t, src, "n.md", "# v2\n")
	rep := Import(src, v, Options{})
	if len(rep.Errors) > 0 {
		t.Fatalf("second import errors: %v", rep.Errors)
	}
	if len(rep.Skipped) != 1 || rep.Skipped[0] != "n.html" {
		t.Errorf("expected skip, got skipped=%v imported=%v", rep.Skipped, rep.Imported)
	}
	if readVault(t, v, "n.html") != v1 {
		t.Errorf("note overwritten without Force")
	}

	// Now with Force — should overwrite.
	rep = Import(src, v, Options{Force: true})
	if len(rep.Errors) > 0 {
		t.Fatalf("force import errors: %v", rep.Errors)
	}
	if len(rep.Imported) != 1 {
		t.Errorf("force did not re-import: %v", rep)
	}
	if !strings.Contains(readVault(t, v, "n.html"), "v2") {
		t.Errorf("force did not write new content")
	}
}

func TestImport_FencedCodeBlockWikilinkNotConverted(t *testing.T) {
	v := newVault(t)
	src := t.TempDir()
	writeFile(t, src, "n.md", "```\n[[Inside]]\n```\n[[Outside]]\n")
	rep := Import(src, v, Options{})
	if len(rep.Errors) > 0 {
		t.Fatalf("errors: %v", rep.Errors)
	}
	out := readVault(t, v, "n.html")
	if strings.Contains(out, `href="inside.html"`) {
		t.Errorf("wikilink inside fence WAS converted: %s", out)
	}
	if !strings.Contains(out, `href="outside.html"`) {
		t.Errorf("wikilink outside fence not converted: %s", out)
	}
}
