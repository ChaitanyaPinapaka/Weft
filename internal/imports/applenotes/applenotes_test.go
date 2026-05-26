package applenotes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"weft/internal/vault"
)

func newVault(t *testing.T) *vault.Vault {
	t.Helper()
	dir := t.TempDir()
	v, err := vault.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestImport_UsesTitleTag(t *testing.T) {
	src := t.TempDir()
	writeFile(t, src, "note.html", `<html><head><title>My Title</title></head><body><h1>Other</h1></body></html>`)
	v := newVault(t)

	rpt := Import(src, v, Options{})
	if len(rpt.Errors) != 0 {
		t.Fatalf("errors: %v", rpt.Errors)
	}
	if !v.Exists("apple-notes/my-title.html") {
		t.Fatalf("expected apple-notes/my-title.html, got imported=%v", rpt.Imported)
	}
}

func TestImport_FallsBackToH1(t *testing.T) {
	src := t.TempDir()
	writeFile(t, src, "note.html", `<html><body><h1>Header Wins</h1><p>body</p></body></html>`)
	v := newVault(t)

	rpt := Import(src, v, Options{})
	if len(rpt.Errors) != 0 {
		t.Fatalf("errors: %v", rpt.Errors)
	}
	if !v.Exists("apple-notes/header-wins.html") {
		t.Fatalf("expected apple-notes/header-wins.html, imported=%v", rpt.Imported)
	}
}

func TestImport_FallsBackToFirstText(t *testing.T) {
	src := t.TempDir()
	writeFile(t, src, "note.html", `<html><body><div>   </div><p>First real line</p><p>second</p></body></html>`)
	v := newVault(t)

	rpt := Import(src, v, Options{})
	if len(rpt.Errors) != 0 {
		t.Fatalf("errors: %v", rpt.Errors)
	}
	if !v.Exists("apple-notes/first-real-line.html") {
		t.Fatalf("expected apple-notes/first-real-line.html, imported=%v", rpt.Imported)
	}
}

func TestImport_NoTextUntitled(t *testing.T) {
	src := t.TempDir()
	writeFile(t, src, "blank.html", `<html><body></body></html>`)
	v := newVault(t)

	rpt := Import(src, v, Options{})
	if len(rpt.Errors) != 0 {
		t.Fatalf("errors: %v", rpt.Errors)
	}
	if !v.Exists("apple-notes/untitled.html") {
		t.Fatalf("expected apple-notes/untitled.html, imported=%v", rpt.Imported)
	}
}

func TestImport_CopiesImageAndRewritesSrc(t *testing.T) {
	src := t.TempDir()
	writeFile(t, src, "pic.png", "PNGDATA")
	writeFile(t, src, "note.html",
		`<html><head><title>Pic Note</title></head><body><img src="pic.png" alt="x"></body></html>`)
	v := newVault(t)

	rpt := Import(src, v, Options{})
	if len(rpt.Errors) != 0 {
		t.Fatalf("errors: %v", rpt.Errors)
	}
	destNote := filepath.Join(v.Root, "apple-notes", "pic-note.html")
	body, err := os.ReadFile(destNote)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `src="pic.png"`) {
		t.Fatalf("expected src=\"pic.png\" in output, got:\n%s", body)
	}
	copied, err := os.ReadFile(filepath.Join(v.Root, "apple-notes", "pic.png"))
	if err != nil {
		t.Fatalf("image not copied: %v", err)
	}
	if string(copied) != "PNGDATA" {
		t.Fatalf("image content mismatch: %q", copied)
	}
}

func TestImport_AbsoluteAndMailtoLeftAlone(t *testing.T) {
	src := t.TempDir()
	writeFile(t, src, "note.html",
		`<html><head><title>Links</title></head><body>`+
			`<a href="https://example.com/x">w</a>`+
			`<a href="mailto:me@example.com">m</a>`+
			`<img src="http://cdn.example.com/i.png">`+
			`</body></html>`)
	v := newVault(t)

	rpt := Import(src, v, Options{})
	if len(rpt.Errors) != 0 {
		t.Fatalf("errors: %v", rpt.Errors)
	}
	body, err := os.ReadFile(filepath.Join(v.Root, "apple-notes", "links.html"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	for _, want := range []string{"https://example.com/x", "mailto:me@example.com", "http://cdn.example.com/i.png"} {
		if !strings.Contains(s, want) {
			t.Errorf("expected %q preserved, body:\n%s", want, s)
		}
	}
}

func TestImport_MissingAssetPreservesRef(t *testing.T) {
	src := t.TempDir()
	writeFile(t, src, "note.html",
		`<html><head><title>Ghost</title></head><body><img src="gone.png"></body></html>`)
	v := newVault(t)

	rpt := Import(src, v, Options{})
	if len(rpt.Errors) != 0 {
		t.Fatalf("errors: %v", rpt.Errors)
	}
	body, err := os.ReadFile(filepath.Join(v.Root, "apple-notes", "ghost.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `src="gone.png"`) {
		t.Fatalf("missing asset ref should be preserved, body:\n%s", body)
	}
}

func TestImport_PreservesSubdir(t *testing.T) {
	src := t.TempDir()
	writeFile(t, src, "Travel/trip.html",
		`<html><head><title>Trip</title></head><body>x</body></html>`)
	v := newVault(t)

	rpt := Import(src, v, Options{})
	if len(rpt.Errors) != 0 {
		t.Fatalf("errors: %v", rpt.Errors)
	}
	if !v.Exists("apple-notes/travel/trip.html") {
		t.Fatalf("expected apple-notes/travel/trip.html, imported=%v", rpt.Imported)
	}
}

func TestImport_RefusesOverwriteWithoutForce(t *testing.T) {
	src := t.TempDir()
	writeFile(t, src, "note.html",
		`<html><head><title>Dup</title></head><body>new</body></html>`)
	v := newVault(t)
	if err := v.Write("apple-notes/dup.html", []byte("OLD")); err != nil {
		t.Fatal(err)
	}

	rpt := Import(src, v, Options{})
	if len(rpt.Errors) != 0 {
		t.Fatalf("errors: %v", rpt.Errors)
	}
	if len(rpt.Skipped) != 1 || rpt.Skipped[0] != "apple-notes/dup.html" {
		t.Fatalf("expected skipped=[apple-notes/dup.html], got %v", rpt.Skipped)
	}
	body, _ := v.Read("apple-notes/dup.html")
	if string(body) != "OLD" {
		t.Fatalf("expected unchanged OLD, got %q", body)
	}

	rpt = Import(src, v, Options{Force: true})
	if len(rpt.Errors) != 0 {
		t.Fatalf("force errors: %v", rpt.Errors)
	}
	body, _ = v.Read("apple-notes/dup.html")
	if !strings.Contains(string(body), "new") {
		t.Fatalf("force should have written new content, got %q", body)
	}
}
