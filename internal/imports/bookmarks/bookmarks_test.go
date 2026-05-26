package bookmarks_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"weft/internal/imports/bookmarks"
	"weft/internal/vault"
)

func newVault(t *testing.T) *vault.Vault {
	t.Helper()
	v, err := vault.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func writeSrc(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "bookmarks.html")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const header = `<!DOCTYPE NETSCAPE-Bookmark-file-1>
<META HTTP-EQUIV="Content-Type" CONTENT="text/html; charset=UTF-8">
<TITLE>Bookmarks</TITLE>
<H1>Bookmarks</H1>
`

func TestImportRootBookmark(t *testing.T) {
	src := writeSrc(t, header+`<DL><p>
  <DT><A HREF="https://news.ycombinator.com/">Hacker News</A>
</DL><p>`)
	v := newVault(t)
	rep := bookmarks.Import(src, v, bookmarks.Options{})
	if len(rep.Errors) != 0 {
		t.Fatalf("errors: %v", rep.Errors)
	}
	want := "bookmarks/hacker-news.html"
	if !v.Exists(want) {
		t.Fatalf("expected %s; imported=%v", want, rep.Imported)
	}
	got, _ := v.Read(want)
	if !strings.Contains(string(got), "https://news.ycombinator.com/") {
		t.Errorf("missing href in note: %s", got)
	}
	if !strings.Contains(string(got), "<h1>Hacker News</h1>") {
		t.Errorf("missing title in note: %s", got)
	}
}

func TestImportOneLevelFolder(t *testing.T) {
	src := writeSrc(t, header+`<DL><p>
  <DT><H3>Tech</H3>
  <DL><p>
    <DT><A HREF="https://golang.org/">Go</A>
  </DL><p>
</DL><p>`)
	v := newVault(t)
	rep := bookmarks.Import(src, v, bookmarks.Options{})
	if len(rep.Errors) != 0 {
		t.Fatalf("errors: %v", rep.Errors)
	}
	if !v.Exists("bookmarks/tech/go.html") {
		t.Fatalf("missing bookmarks/tech/go.html; imported=%v", rep.Imported)
	}
}

func TestImportNestedFolders(t *testing.T) {
	src := writeSrc(t, header+`<DL><p>
  <DT><H3>Tech</H3>
  <DL><p>
    <DT><H3>Languages</H3>
    <DL><p>
      <DT><A HREF="https://golang.org/">Go</A>
    </DL><p>
    <DT><A HREF="https://example.com/">Example</A>
  </DL><p>
  <DT><A HREF="https://news.ycombinator.com/">Hacker News</A>
</DL><p>`)
	v := newVault(t)
	rep := bookmarks.Import(src, v, bookmarks.Options{})
	if len(rep.Errors) != 0 {
		t.Fatalf("errors: %v", rep.Errors)
	}
	for _, p := range []string{
		"bookmarks/tech/languages/go.html",
		"bookmarks/tech/example.html",
		"bookmarks/hacker-news.html",
	} {
		if !v.Exists(p) {
			t.Errorf("missing %s; imported=%v", p, rep.Imported)
		}
	}
}

func TestImportDuplicateURLsSkipped(t *testing.T) {
	src := writeSrc(t, header+`<DL><p>
  <DT><H3>A</H3>
  <DL><p>
    <DT><A HREF="https://dup.example/">First</A>
  </DL><p>
  <DT><H3>B</H3>
  <DL><p>
    <DT><A HREF="https://dup.example/">Second</A>
  </DL><p>
</DL><p>`)
	v := newVault(t)
	rep := bookmarks.Import(src, v, bookmarks.Options{})
	if len(rep.Errors) != 0 {
		t.Fatalf("errors: %v", rep.Errors)
	}
	if len(rep.Imported) != 1 {
		t.Errorf("want 1 imported, got %d: %v", len(rep.Imported), rep.Imported)
	}
	if len(rep.Skipped) != 1 {
		t.Errorf("want 1 skipped, got %d: %v", len(rep.Skipped), rep.Skipped)
	}
}

func TestOverwriteRequiresForce(t *testing.T) {
	v := newVault(t)
	if err := v.Write("bookmarks/go.html", []byte("<p>old</p>")); err != nil {
		t.Fatal(err)
	}

	src := writeSrc(t, header+`<DL><p>
  <DT><A HREF="https://golang.org/">Go</A>
</DL><p>`)

	rep := bookmarks.Import(src, v, bookmarks.Options{})
	if len(rep.Imported) != 0 || len(rep.Skipped) != 1 {
		t.Fatalf("no-force: want skip, got imported=%v skipped=%v", rep.Imported, rep.Skipped)
	}
	got, _ := v.Read("bookmarks/go.html")
	if string(got) != "<p>old</p>" {
		t.Errorf("content overwritten without Force")
	}

	rep = bookmarks.Import(src, v, bookmarks.Options{Force: true})
	if len(rep.Imported) != 1 {
		t.Fatalf("force: want imported, got %v", rep.Imported)
	}
	got, _ = v.Read("bookmarks/go.html")
	if !strings.Contains(string(got), "https://golang.org/") {
		t.Errorf("Force did not overwrite: %s", got)
	}
}

func TestSpecialCharsInTitle(t *testing.T) {
	src := writeSrc(t, header+`<DL><p>
  <DT><H3>Tech &amp; Stuff</H3>
  <DL><p>
    <DT><A HREF="https://example.com/x">Hello, World! &amp; More</A>
  </DL><p>
</DL><p>`)
	v := newVault(t)
	rep := bookmarks.Import(src, v, bookmarks.Options{})
	if len(rep.Errors) != 0 {
		t.Fatalf("errors: %v", rep.Errors)
	}
	want := "bookmarks/tech-stuff/hello-world-more.html"
	if !v.Exists(want) {
		t.Fatalf("expected %s; imported=%v", want, rep.Imported)
	}
}

func TestNoTitleFallsBackToHost(t *testing.T) {
	src := writeSrc(t, header+`<DL><p>
  <DT><A HREF="https://example.com/some/path"></A>
</DL><p>`)
	v := newVault(t)
	rep := bookmarks.Import(src, v, bookmarks.Options{})
	if len(rep.Errors) != 0 {
		t.Fatalf("errors: %v", rep.Errors)
	}
	want := "bookmarks/example-com.html"
	if !v.Exists(want) {
		t.Fatalf("expected %s; imported=%v", want, rep.Imported)
	}
	got, _ := v.Read(want)
	if !strings.Contains(string(got), "<h1>example.com</h1>") {
		t.Errorf("title should fallback to host: %s", got)
	}
}
