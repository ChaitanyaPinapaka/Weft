package clip

import (
	"strings"
	"testing"
	"time"
)

func TestClean_StripsScriptsAndNoscript(t *testing.T) {
	in := []byte(`<html><head><title>X</title></head><body>
		<script>alert(1)</script>
		<noscript>fallback</noscript>
		<p>keep me</p>
	</body></html>`)
	out, _, err := Clean(in, "https://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, "<script") || strings.Contains(s, "alert(1)") {
		t.Errorf("script not stripped: %s", s)
	}
	if strings.Contains(s, "<noscript") || strings.Contains(s, "fallback") {
		t.Errorf("noscript not stripped: %s", s)
	}
	if !strings.Contains(s, "<p>keep me</p>") {
		t.Errorf("prose not preserved: %s", s)
	}
}

func TestClean_StripsEventHandlers(t *testing.T) {
	in := []byte(`<html><body><button onclick="x()" id="b" onmouseover="y()">go</button></body></html>`)
	out, _, err := Clean(in, "https://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(strings.ToLower(s), "onclick") || strings.Contains(strings.ToLower(s), "onmouseover") {
		t.Errorf("event handlers not stripped: %s", s)
	}
	if !strings.Contains(s, `id="b"`) {
		t.Errorf("non-event attrs should survive: %s", s)
	}
}

func TestClean_StripsPreloadScriptLinks(t *testing.T) {
	in := []byte(`<html><head>
		<link rel="preload" as="script" href="/a.js">
		<link rel="stylesheet" href="/a.css">
	</head><body></body></html>`)
	out, _, err := Clean(in, "https://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, "a.js") {
		t.Errorf("preload script link not stripped: %s", s)
	}
	if !strings.Contains(s, "a.css") {
		t.Errorf("stylesheet link should survive: %s", s)
	}
}

func TestClean_InjectsBaseHref(t *testing.T) {
	in := []byte(`<html><head><title>X</title></head><body></body></html>`)
	out, _, err := Clean(in, "https://example.com/path/")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, `<base href="https://example.com/path/"`) {
		t.Errorf("base href not injected: %s", s)
	}
	if strings.Count(s, "<base") != 1 {
		t.Errorf("expected exactly one <base>, got: %s", s)
	}
}

func TestClean_BaseHrefCollisionOursWins(t *testing.T) {
	// Policy: pre-existing <base> is removed; our injected one wins.
	in := []byte(`<html><head><base href="https://old.example/"><title>X</title></head><body></body></html>`)
	out, _, err := Clean(in, "https://new.example/")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, "old.example") {
		t.Errorf("pre-existing base not removed: %s", s)
	}
	if !strings.Contains(s, `<base href="https://new.example/"`) {
		t.Errorf("new base not injected: %s", s)
	}
	if strings.Count(s, "<base") != 1 {
		t.Errorf("expected exactly one <base>, got: %s", s)
	}
}

func TestClean_TitleFallbackChain(t *testing.T) {
	cases := []struct {
		name string
		in   string
		url  string
		want string
	}{
		{"title wins", `<html><head><title>Real Title</title></head><body><h1>H1</h1></body></html>`, "https://example.com/", "Real Title"},
		{"h1 fallback", `<html><head></head><body><h1>From H1</h1></body></html>`, "https://example.com/", "From H1"},
		{"host fallback", `<html><head></head><body><p>no headings</p></body></html>`, "https://example.com/page", "example.com"},
		{"empty title falls through to h1", `<html><head><title>   </title></head><body><h1>H1 wins</h1></body></html>`, "https://example.com/", "H1 wins"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, title, err := Clean([]byte(tc.in), tc.url)
			if err != nil {
				t.Fatal(err)
			}
			if title != tc.want {
				t.Errorf("got title %q, want %q", title, tc.want)
			}
		})
	}
}

func TestClean_PreservesContent(t *testing.T) {
	in := []byte(`<html><head><style>p{color:red}</style></head><body>
		<h1>Heading</h1>
		<p>Some prose.</p>
		<a href="https://example.com/x">link</a>
		<img src="/img.png" alt="pic">
	</body></html>`)
	out, _, err := Clean(in, "https://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"<style>", "<h1>Heading</h1>", "<p>Some prose.</p>", `<a href="https://example.com/x">link</a>`, `<img src="/img.png"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in output: %s", want, s)
		}
	}
}

func TestClean_MalformedHTMLNoPanic(t *testing.T) {
	// Unclosed tags, stray angle brackets — html.Parse should normalize.
	in := []byte(`<html><body><p>hello<div><span>oops`)
	out, title, err := Clean(in, "https://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Error("expected non-empty output")
	}
	if title != "example.com" {
		t.Errorf("expected host fallback, got %q", title)
	}
}

func TestClean_HostFallbackWhenNoTitleNoH1(t *testing.T) {
	in := []byte(`<html><body><p>nothing here</p></body></html>`)
	_, title, err := Clean(in, "https://news.ycombinator.com/item?id=1")
	if err != nil {
		t.Fatal(err)
	}
	if title != "news.ycombinator.com" {
		t.Errorf("got %q, want host", title)
	}
}

func TestSlug(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Hello, World!", "hello-world"},
		{"   leading spaces", "leading-spaces"},
		{"trailing   spaces   ", "trailing-spaces"},
		{"已读", "untitled"},
		{"/", "untitled"},
		{"", "untitled"},
		{"!!!", "untitled"},
		{"Already-Slugged", "already-slugged"},
		{"multiple---hyphens", "multiple-hyphens"},
		{"MixedCASE 123", "mixedcase-123"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := Slug(tc.in); got != tc.want {
				t.Errorf("Slug(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSlug_Truncation(t *testing.T) {
	long := strings.Repeat("abcdefghij ", 20) // ~220 chars, words separated by spaces
	got := Slug(long)
	if len(got) > 60 {
		t.Errorf("slug too long: %d chars: %q", len(got), got)
	}
	// Should end on a word boundary (last char not a hyphen, and the truncation
	// shouldn't have left a partial word).
	if strings.HasSuffix(got, "-") {
		t.Errorf("slug ends with hyphen: %q", got)
	}
	if !strings.HasPrefix(got, "abcdefghij") {
		t.Errorf("unexpected slug start: %q", got)
	}
}

func TestSlug_TruncationNoBoundary(t *testing.T) {
	// One giant word, no hyphens — truncate hard at 60.
	in := strings.Repeat("a", 200)
	got := Slug(in)
	if len(got) != 60 {
		t.Errorf("got len %d, want 60: %q", len(got), got)
	}
}

func TestClipPath(t *testing.T) {
	when := time.Date(2026, 5, 25, 14, 30, 0, 0, time.UTC)
	got := ClipPath(when, "hello-world")
	want := "clips/2026-05-25-hello-world.html"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
