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

func TestVaultTrash(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "projects", "foo.html"), "<p>foo</p>")
	v, _ := vault.New(dir)

	if err := v.Trash("projects/foo.html"); err != nil {
		t.Fatal(err)
	}
	if v.Exists("projects/foo.html") {
		t.Error("source should be gone after Trash")
	}
	got, err := os.ReadFile(filepath.Join(dir, ".trash", "projects", "foo.html"))
	if err != nil {
		t.Fatalf("trashed file should keep its relative path under .trash: %v", err)
	}
	if string(got) != "<p>foo</p>" {
		t.Fatalf("trashed content = %q, want original bytes", got)
	}
	notes, err := v.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 0 {
		t.Fatalf("List must not see trashed notes, got %v", notes)
	}
}

func TestVaultTrashCollision(t *testing.T) {
	dir := t.TempDir()
	v, _ := vault.New(dir)

	writeFile(t, filepath.Join(dir, "foo.html"), "first")
	if err := v.Trash("foo.html"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "foo.html"), "second")
	if err := v.Trash("foo.html"); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(dir, ".trash", "foo.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "first" {
		t.Fatalf("first trashed note was clobbered: %q", got)
	}
	entries, err := os.ReadDir(filepath.Join(dir, ".trash"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 files in .trash after a collision, got %d", len(entries))
	}
}

func TestVaultTrashPathTraversal(t *testing.T) {
	dir := t.TempDir()
	v, _ := vault.New(dir)

	// Trashing outside the vault root must fail loudly, mirroring Write.
	if err := v.Trash("../outside.html"); err == nil {
		t.Fatal("expected error trashing outside vault, got nil")
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
