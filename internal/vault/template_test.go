package vault_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"weft/internal/vault"
)

func TestIsTemplate(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"templates/x.html", true},
		{"/templates/x.html", true},
		{"templates/sub/y.html", true},
		{"notes/x.html", false},
		{"templates", false}, // no trailing slash — not under the directory
		{"templatesx/y.html", false},
		{"", false},
	}
	for _, c := range cases {
		if got := vault.IsTemplate(c.in); got != c.want {
			t.Errorf("IsTemplate(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestListTemplatesEmpty(t *testing.T) {
	dir := t.TempDir()
	v, _ := vault.New(dir)

	notes, err := v.ListTemplates()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("want 0 notes, got %d", len(notes))
	}
}

func TestListTemplatesReturnsBoth(t *testing.T) {
	dir := t.TempDir()
	writeTplFile(t, filepath.Join(dir, "templates", "a.html"), "<p>A</p>")
	writeTplFile(t, filepath.Join(dir, "templates", "b.html"), "<p>B</p>")
	// Non-template html should not appear.
	writeTplFile(t, filepath.Join(dir, "other.html"), "<p>nope</p>")

	v, _ := vault.New(dir)
	notes, err := v.ListTemplates()
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 2 {
		t.Fatalf("want 2 templates, got %d", len(notes))
	}
	if notes[0].Path != "templates/a.html" || notes[1].Path != "templates/b.html" {
		t.Errorf("unexpected sort order or paths: %+v", notes)
	}
}

func TestNewFromTemplateWritesContent(t *testing.T) {
	dir := t.TempDir()
	v, _ := vault.New(dir)
	const body = "<h1>Meeting</h1><p>Agenda</p>"
	if err := v.Write("templates/meeting.html", []byte(body)); err != nil {
		t.Fatal(err)
	}

	if err := v.NewFromTemplate("templates/meeting.html", "meetings/2026-05-25.html"); err != nil {
		t.Fatal(err)
	}
	got, err := v.Read("meetings/2026-05-25.html")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Fatalf("want %q, got %q", body, got)
	}
}

func TestNewFromTemplateRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	v, _ := vault.New(dir)
	if err := v.Write("templates/t.html", []byte("<p>T</p>")); err != nil {
		t.Fatal(err)
	}
	if err := v.Write("dst.html", []byte("<p>existing</p>")); err != nil {
		t.Fatal(err)
	}

	err := v.NewFromTemplate("templates/t.html", "dst.html")
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("want os.ErrExist, got %v", err)
	}
	// And the existing content must be untouched.
	got, _ := v.Read("dst.html")
	if string(got) != "<p>existing</p>" {
		t.Fatalf("destination was overwritten: %q", got)
	}
}

func TestNewFromTemplateRefusesDstInTemplates(t *testing.T) {
	dir := t.TempDir()
	v, _ := vault.New(dir)
	if err := v.Write("templates/src.html", []byte("<p>S</p>")); err != nil {
		t.Fatal(err)
	}

	if err := v.NewFromTemplate("templates/src.html", "templates/copy.html"); err == nil {
		t.Fatal("expected error when dst is inside templates/, got nil")
	}
	if v.Exists("templates/copy.html") {
		t.Fatal("template copy should not have been written")
	}
}

func TestEnsureDailyFromTemplateNoTemplate(t *testing.T) {
	dir := t.TempDir()
	v, _ := vault.New(dir)
	when := time.Date(2026, 5, 25, 10, 0, 0, 0, time.UTC)

	rel, err := v.EnsureDailyFromTemplate(when)
	if err != nil {
		t.Fatal(err)
	}
	if rel != "daily/2026-05-25.html" {
		t.Fatalf("unexpected path: %q", rel)
	}
	got, _ := v.Read(rel)
	want := "<h1>2026-05-25</h1>\n"
	if string(got) != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}

func TestEnsureDailyFromTemplateUsesTemplate(t *testing.T) {
	dir := t.TempDir()
	v, _ := vault.New(dir)
	const tpl = "<h1>Today</h1><section class=\"plan\"></section>"
	if err := v.Write(vault.DailyTemplatePath, []byte(tpl)); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 5, 25, 10, 0, 0, 0, time.UTC)

	rel, err := v.EnsureDailyFromTemplate(when)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := v.Read(rel)
	if string(got) != tpl {
		t.Fatalf("want template body, got %q", got)
	}
}

func TestEnsureDailyFromTemplateDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	v, _ := vault.New(dir)
	when := time.Date(2026, 5, 25, 10, 0, 0, 0, time.UTC)
	const existing = "<h1>my own daily</h1>\n<p>do not touch</p>"
	if err := v.Write(v.DailyPath(when), []byte(existing)); err != nil {
		t.Fatal(err)
	}
	// Even with a template present, the existing daily must survive.
	if err := v.Write(vault.DailyTemplatePath, []byte("<h1>tpl</h1>")); err != nil {
		t.Fatal(err)
	}

	rel, err := v.EnsureDailyFromTemplate(when)
	if err != nil {
		t.Fatal(err)
	}
	if rel != v.DailyPath(when) {
		t.Fatalf("want %q, got %q", v.DailyPath(when), rel)
	}
	got, _ := v.Read(rel)
	if string(got) != existing {
		t.Fatalf("existing daily was overwritten: %q", got)
	}
}

func writeTplFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
