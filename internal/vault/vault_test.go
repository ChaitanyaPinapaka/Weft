package vault_test

import (
	"os"
	"path/filepath"
	"testing"

	"weft/internal/vault"
)

func TestVaultList(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.html"), "<p>A</p>")
	writeFile(t, filepath.Join(dir, "sub", "b.html"), "<p>B</p>")

	v, err := vault.New(dir)
	if err != nil {
		t.Fatal(err)
	}

	notes, err := v.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 2 {
		t.Fatalf("want 2 notes, got %d", len(notes))
	}
}

func TestVaultReadWrite(t *testing.T) {
	dir := t.TempDir()
	v, _ := vault.New(dir)

	const content = "<p>Hello Weft</p>"
	if err := v.Write("note.html", []byte(content)); err != nil {
		t.Fatal(err)
	}

	got, err := v.Read("note.html")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Fatalf("want %q, got %q", content, got)
	}
}

func TestVaultPathTraversal(t *testing.T) {
	dir := t.TempDir()
	v, _ := vault.New(dir)

	// Writing outside the vault root must fail.
	if err := v.Write("../outside.html", []byte("evil")); err == nil {
		t.Fatal("expected error writing outside vault, got nil")
	}
}

func TestVaultExists(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "exists.html"), "<p>yes</p>")
	v, _ := vault.New(dir)

	if !v.Exists("exists.html") {
		t.Error("Exists should return true for existing file")
	}
	if v.Exists("ghost.html") {
		t.Error("Exists should return false for missing file")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
