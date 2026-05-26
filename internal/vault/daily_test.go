package vault_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"weft/internal/vault"
)

func TestDailyPath(t *testing.T) {
	v, _ := vault.New(t.TempDir())
	got := v.DailyPath(time.Date(2026, 5, 25, 9, 30, 0, 0, time.UTC))
	want := "daily/2026-05-25.html"
	if got != want {
		t.Fatalf("DailyPath = %q, want %q", got, want)
	}
}

func TestEnsureDailyCreates(t *testing.T) {
	dir := t.TempDir()
	v, _ := vault.New(dir)
	day := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)

	rel, err := v.EnsureDaily(day)
	if err != nil {
		t.Fatal(err)
	}
	if rel != "daily/2026-01-02.html" {
		t.Fatalf("rel = %q", rel)
	}

	got, err := os.ReadFile(filepath.Join(dir, "daily", "2026-01-02.html"))
	if err != nil {
		t.Fatal(err)
	}
	want := "<h1>2026-01-02</h1>\n"
	if string(got) != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestEnsureDailyDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	v, _ := vault.New(dir)
	day := time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC)

	const original = "<h1>2026-05-25</h1>\n<p>hard-won thoughts</p>\n"
	if err := v.Write("daily/2026-05-25.html", []byte(original)); err != nil {
		t.Fatal(err)
	}

	if _, err := v.EnsureDaily(day); err != nil {
		t.Fatal(err)
	}

	got, err := v.Read("daily/2026-05-25.html")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("EnsureDaily clobbered existing note: got %q", got)
	}
}

func TestEnsureDailyCreatesSubdir(t *testing.T) {
	dir := t.TempDir()
	v, _ := vault.New(dir)

	if _, err := os.Stat(filepath.Join(dir, "daily")); !os.IsNotExist(err) {
		t.Fatalf("daily/ should not exist yet: %v", err)
	}

	if _, err := v.EnsureDaily(time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(filepath.Join(dir, "daily"))
	if err != nil {
		t.Fatalf("daily/ not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("daily is not a directory")
	}
}
