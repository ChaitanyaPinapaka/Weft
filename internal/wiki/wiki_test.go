package wiki

import (
	"strings"
	"testing"
)

func TestExpandWikilinks_SimpleMatch(t *testing.T) {
	got, err := ExpandWikilinks([]byte("see [[foo]] here"), []string{"foo.html"})
	if err != nil {
		t.Fatal(err)
	}
	want := `see <a href="foo.html">foo</a> here`
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpandWikilinks_SubPathResolution(t *testing.T) {
	got, err := ExpandWikilinks([]byte("[[bar]]"), []string{"notes/bar.html"})
	if err != nil {
		t.Fatal(err)
	}
	want := `<a href="notes/bar.html">bar</a>`
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpandWikilinks_BrokenLink(t *testing.T) {
	got, err := ExpandWikilinks([]byte("[[ghost]]"), []string{"foo.html"})
	if err != nil {
		t.Fatal(err)
	}
	want := `<a href="ghost.html" class="wiki-broken">ghost</a>`
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpandWikilinks_BrokenLinkSlugsSpaces(t *testing.T) {
	got, err := ExpandWikilinks([]byte("[[Hello World]]"), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := `<a href="hello-world.html" class="wiki-broken">Hello World</a>`
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpandWikilinks_AltText(t *testing.T) {
	got, err := ExpandWikilinks([]byte("[[foo|My Foo]]"), []string{"foo.html"})
	if err != nil {
		t.Fatal(err)
	}
	want := `<a href="foo.html">My Foo</a>`
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpandWikilinks_CaseInsensitive(t *testing.T) {
	got, err := ExpandWikilinks([]byte("[[FOO]]"), []string{"foo.html"})
	if err != nil {
		t.Fatal(err)
	}
	want := `<a href="foo.html">FOO</a>`
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpandWikilinks_SpacesMatchVerbatim(t *testing.T) {
	// "hello world" should match "hello world.html" (verbatim, case-insensitive)
	// NOT "hello-world.html".
	got, err := ExpandWikilinks([]byte("[[hello world]]"), []string{"hello world.html"})
	if err != nil {
		t.Fatal(err)
	}
	want := `<a href="hello world.html">hello world</a>`
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// Same name against a hyphenated file should be broken.
	got, err = ExpandWikilinks([]byte("[[hello world]]"), []string{"hello-world.html"})
	if err != nil {
		t.Fatal(err)
	}
	want = `<a href="hello-world.html" class="wiki-broken">hello world</a>`
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpandWikilinks_SkipPre(t *testing.T) {
	in := []byte("<pre>[[foo]]</pre>")
	got, err := ExpandWikilinks(in, []string{"foo.html"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "[[foo]]") {
		t.Errorf("expected [[foo]] preserved in <pre>, got %q", got)
	}
	if strings.Contains(string(got), `href="foo.html"`) {
		t.Errorf("should not have expanded inside <pre>: %q", got)
	}
}

func TestExpandWikilinks_SkipCode(t *testing.T) {
	in := []byte("<code>[[foo]]</code>")
	got, err := ExpandWikilinks(in, []string{"foo.html"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "[[foo]]") {
		t.Errorf("expected [[foo]] preserved in <code>, got %q", got)
	}
}

func TestExpandWikilinks_SkipScript(t *testing.T) {
	in := []byte("<script>var s = '[[foo]]';</script>")
	got, err := ExpandWikilinks(in, []string{"foo.html"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "[[foo]]") {
		t.Errorf("expected [[foo]] preserved in <script>, got %q", got)
	}
}

func TestExpandWikilinks_SkipStyle(t *testing.T) {
	in := []byte("<style>/* [[foo]] */</style>")
	got, err := ExpandWikilinks(in, []string{"foo.html"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "[[foo]]") {
		t.Errorf("expected [[foo]] preserved in <style>, got %q", got)
	}
}

func TestExpandWikilinks_SkipExistingAnchor(t *testing.T) {
	in := []byte(`<a href="x.html">[[foo]]</a>`)
	got, err := ExpandWikilinks(in, []string{"foo.html"})
	if err != nil {
		t.Fatal(err)
	}
	// Must not double-wrap; the literal [[foo]] should remain as text
	// inside the existing anchor.
	if !strings.Contains(string(got), "[[foo]]") {
		t.Errorf("expected [[foo]] preserved inside <a>, got %q", got)
	}
	// And there must be no nested <a>.
	if strings.Count(string(got), "<a ") > 1 {
		t.Errorf("nested anchors not allowed: %q", got)
	}
}

func TestExpandWikilinks_NestedSkip(t *testing.T) {
	// <a><code>[[x]]</code></a> — skipped on both counts. The skipDepth
	// counter must come back to 0 on the way out.
	in := []byte(`<a href="x.html"><code>[[x]]</code></a> then [[foo]]`)
	got, err := ExpandWikilinks(in, []string{"foo.html", "x.html"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "[[x]]") {
		t.Errorf("expected [[x]] preserved, got %q", got)
	}
	if !strings.Contains(string(got), `<a href="foo.html">foo</a>`) {
		t.Errorf("expected [[foo]] expanded after nested skip, got %q", got)
	}
}

func TestExpandWikilinks_MultipleInOneTextNode(t *testing.T) {
	in := []byte("[[foo]] and [[bar]]")
	got, err := ExpandWikilinks(in, []string{"foo.html", "bar.html"})
	if err != nil {
		t.Fatal(err)
	}
	want := `<a href="foo.html">foo</a> and <a href="bar.html">bar</a>`
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpandWikilinks_EmptyInput(t *testing.T) {
	got, err := ExpandWikilinks([]byte{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty output, got %q", got)
	}
}

func TestExpandWikilinks_MalformedToken(t *testing.T) {
	// A stray "[[" with no closing pair should be left as literal text.
	in := []byte("just [[ a bracket")
	got, err := ExpandWikilinks(in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "just [[ a bracket" {
		t.Errorf("expected passthrough, got %q", got)
	}
}

func TestExpandWikilinks_DeterministicOnDuplicateBasename(t *testing.T) {
	// Two paths share the basename "foo". Sorting → "a/foo.html" wins.
	paths := []string{"z/foo.html", "a/foo.html"}
	got, err := ExpandWikilinks([]byte("[[foo]]"), paths)
	if err != nil {
		t.Fatal(err)
	}
	want := `<a href="a/foo.html">foo</a>`
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRewriteWikilinks_OnlyMatchingHrefUpdated(t *testing.T) {
	in := []byte(`<p>` +
		`<a href="old.html">First</a> ` +
		`<a href="other.html">Other</a> ` +
		`<a href="old.html" class="wiki-broken">Aliased</a>` +
		`</p>`)
	got, err := RewriteWikilinks(in, "old.html", "new.html")
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if !strings.Contains(s, `<a href="new.html">First</a>`) {
		t.Errorf("first anchor not rewritten: %q", s)
	}
	if !strings.Contains(s, `<a href="other.html">Other</a>`) {
		t.Errorf("other anchor wrongly touched: %q", s)
	}
	// Class and text must be preserved on the rewritten anchor.
	// html.Render may reorder attributes alphabetically, so check both
	// attribute and text presence rather than exact substring.
	if !strings.Contains(s, `class="wiki-broken"`) ||
		!strings.Contains(s, `>Aliased</a>`) {
		t.Errorf("class/text not preserved on rewrite: %q", s)
	}
	// And the second old-href occurrence is now new-href.
	if strings.Count(s, `href="new.html"`) != 2 {
		t.Errorf("expected both old-href anchors rewritten: %q", s)
	}
}

func TestRewriteWikilinks_EmptyInput(t *testing.T) {
	got, err := RewriteWikilinks([]byte{}, "old.html", "new.html")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty output, got %q", got)
	}
}

func TestRewriteWikilinks_NoMatch(t *testing.T) {
	in := []byte(`<a href="foo.html">foo</a>`)
	got, err := RewriteWikilinks(in, "bar.html", "baz.html")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `<a href="foo.html">foo</a>` {
		t.Errorf("non-matching anchor should be unchanged, got %q", got)
	}
}
