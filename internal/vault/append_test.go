package vault_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"weft/internal/vault"
)

func TestAppendCaptureCreatesDaily(t *testing.T) {
	dir := t.TempDir()
	v, _ := vault.New(dir)
	day := time.Date(2026, 5, 25, 19, 48, 49, 0, time.UTC)

	rel, err := v.AppendCapture(day, "first idea")
	if err != nil {
		t.Fatal(err)
	}
	if rel != "daily/2026-05-25.html" {
		t.Fatalf("rel = %q", rel)
	}

	got, err := v.Read(rel)
	if err != nil {
		t.Fatal(err)
	}
	body := string(got)
	if !strings.HasPrefix(body, "<h1>2026-05-25</h1>\n") {
		t.Fatalf("missing daily stub: %q", body)
	}
	if !strings.Contains(body, `<aside class="capture" data-ts="2026-05-25T19:48:49Z">`) {
		t.Fatalf("missing aside: %q", body)
	}
	if !strings.Contains(body, "<time>19:48</time> first idea") {
		t.Fatalf("missing time/text: %q", body)
	}

	// Daily file should live where DailyPath says.
	if _, err := v.Read(filepath.ToSlash(rel)); err != nil {
		t.Fatal(err)
	}
}

func TestAppendCapturePreservesExisting(t *testing.T) {
	dir := t.TempDir()
	v, _ := vault.New(dir)
	day := time.Date(2026, 5, 25, 9, 0, 0, 0, time.UTC)

	const original = "<h1>2026-05-25</h1>\n<p>hard-won thoughts</p>\n"
	if err := v.Write("daily/2026-05-25.html", []byte(original)); err != nil {
		t.Fatal(err)
	}

	if _, err := v.AppendCapture(day, "new note"); err != nil {
		t.Fatal(err)
	}

	got, _ := v.Read("daily/2026-05-25.html")
	if !strings.HasPrefix(string(got), original) {
		t.Fatalf("clobbered existing content: %q", got)
	}
	if !strings.Contains(string(got), "new note") {
		t.Fatalf("missing appended content: %q", got)
	}
}

func TestAppendCaptureEscapesHTML(t *testing.T) {
	dir := t.TempDir()
	v, _ := vault.New(dir)
	day := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)

	if _, err := v.AppendCapture(day, `<script>alert("x")</script> & friends`); err != nil {
		t.Fatal(err)
	}

	got, _ := v.Read("daily/2026-05-25.html")
	body := string(got)
	if strings.Contains(body, "<script>") {
		t.Fatalf("unescaped <script>: %q", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Fatalf("missing escaped <script>: %q", body)
	}
	if !strings.Contains(body, "&amp; friends") {
		t.Fatalf("missing escaped &: %q", body)
	}
}

func TestAppendCaptureTwiceProducesTwoAsides(t *testing.T) {
	dir := t.TempDir()
	v, _ := vault.New(dir)
	day := time.Date(2026, 5, 25, 8, 0, 0, 0, time.UTC)

	if _, err := v.AppendCapture(day, "one"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.AppendCapture(day.Add(time.Hour), "two"); err != nil {
		t.Fatal(err)
	}

	got, _ := v.Read("daily/2026-05-25.html")
	body := string(got)
	if n := strings.Count(body, `<aside class="capture"`); n != 2 {
		t.Fatalf("aside count = %d, want 2; body = %q", n, body)
	}
	if !strings.Contains(body, " one</aside>") {
		t.Fatalf("missing first capture: %q", body)
	}
	if !strings.Contains(body, " two</aside>") {
		t.Fatalf("missing second capture: %q", body)
	}
	// Order matters: first capture must precede the second on disk.
	if strings.Index(body, "one") > strings.Index(body, "two") {
		t.Fatalf("captures out of order: %q", body)
	}
}

func TestAppendCaptureRejectsEmpty(t *testing.T) {
	dir := t.TempDir()
	v, _ := vault.New(dir)
	day := time.Date(2026, 5, 25, 8, 0, 0, 0, time.UTC)

	if _, err := v.AppendCapture(day, ""); !errors.Is(err, vault.ErrEmptyCapture) {
		t.Fatalf("err = %v, want ErrEmptyCapture", err)
	}
	if _, err := v.AppendCapture(day, "   \t\n"); !errors.Is(err, vault.ErrEmptyCapture) {
		t.Fatalf("err = %v, want ErrEmptyCapture", err)
	}

	// Daily file should not be created when capture is rejected.
	if v.Exists("daily/2026-05-25.html") {
		t.Fatalf("daily note created for empty capture")
	}
}
