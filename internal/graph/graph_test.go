package graph

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"weft/internal/index"
	"weft/internal/vault"
)

// newFixture writes the given path->html map into a fresh vault dir and
// indexes each note. Returns a Vault + Index ready for Build.
func newFixture(t *testing.T, files map[string]string) (*vault.Vault, *index.Index) {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	v, err := vault.New(root)
	if err != nil {
		t.Fatalf("vault.New: %v", err)
	}
	ix, err := index.OpenMemory()
	if err != nil {
		t.Fatalf("index.OpenMemory: %v", err)
	}
	t.Cleanup(func() { ix.Close() })

	now := time.Now()
	for rel, body := range files {
		if err := ix.Upsert(index.Note{
			Path:    rel,
			Title:   strings.TrimSuffix(rel, ".html"),
			Body:    body,
			Links:   index.ParseLinks([]byte(body)),
			ModTime: now,
		}); err != nil {
			t.Fatalf("ix.Upsert %s: %v", rel, err)
		}
	}
	return v, ix
}

func TestBuildEmptyVault(t *testing.T) {
	v, ix := newFixture(t, map[string]string{})
	g, err := Build(v, ix)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if g.Nodes == nil || g.Edges == nil {
		t.Fatalf("slices must be non-nil so JSON encodes []: %+v", g)
	}
	if len(g.Nodes) != 0 || len(g.Edges) != 0 {
		t.Fatalf("expected empty graph, got %+v", g)
	}
	// Verify the JSON shape — empty arrays, not nulls.
	buf, _ := json.Marshal(g)
	if got := string(buf); !strings.Contains(got, `"nodes":[]`) || !strings.Contains(got, `"edges":[]`) {
		t.Fatalf("expected [] for both arrays, got %s", got)
	}
}

func TestBuildChain(t *testing.T) {
	v, ix := newFixture(t, map[string]string{
		"a.html": `<a href="b.html">to b</a>`,
		"b.html": `<a href="c.html">to c</a>`,
		"c.html": `<p>leaf</p>`,
	})
	g, err := Build(v, ix)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(g.Nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(g.Nodes))
	}
	if len(g.Edges) != 2 {
		t.Fatalf("expected 2 edges, got %d (%v)", len(g.Edges), g.Edges)
	}

	want := map[string]int{"a.html": 0, "b.html": 1, "c.html": 1}
	for _, n := range g.Nodes {
		exp, ok := want[n.Path]
		if !ok {
			t.Fatalf("unexpected node %q", n.Path)
		}
		if n.BacklinkCount != exp {
			t.Fatalf("BacklinkCount[%s]: got %d want %d", n.Path, n.BacklinkCount, exp)
		}
		// Title fallback is the trimmed name.
		if n.Title != strings.TrimSuffix(n.Path, ".html") {
			t.Fatalf("title fallback: got %q for %q", n.Title, n.Path)
		}
	}

	edges := map[string]bool{}
	for _, e := range g.Edges {
		edges[e.Src+"->"+e.Dst] = true
	}
	for _, want := range []string{"a.html->b.html", "b.html->c.html"} {
		if !edges[want] {
			t.Fatalf("missing edge %s in %v", want, g.Edges)
		}
	}
}

func TestBuildMutualLink(t *testing.T) {
	v, ix := newFixture(t, map[string]string{
		"a.html": `<a href="b.html">b</a>`,
		"b.html": `<a href="a.html">a</a>`,
	})
	g, err := Build(v, ix)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(g.Edges) != 2 {
		t.Fatalf("mutual link should yield two directed edges, got %d (%v)", len(g.Edges), g.Edges)
	}
	pairs := map[string]bool{}
	for _, e := range g.Edges {
		pairs[e.Src+"->"+e.Dst] = true
	}
	if !pairs["a.html->b.html"] || !pairs["b.html->a.html"] {
		t.Fatalf("expected both directions, got %v", pairs)
	}
}

func TestBuildEdgeDedup(t *testing.T) {
	// Index already dedupes within a single Upsert (sorted/unique LinksFrom).
	// Re-Upserting the same note twice exercises Build's own seen map: even if
	// LinksFrom returned a duplicate, we'd only emit one Edge.
	v, ix := newFixture(t, map[string]string{
		"a.html": `<a href="b.html">b</a><a href="B.html">b again</a><a href="b.html">b yet again</a>`,
		"b.html": `<p>leaf</p>`,
	})
	if err := ix.Upsert(index.Note{
		Path:    "a.html",
		Title:   "a",
		Body:    `<a href="b.html">b</a>`,
		Links:   []string{"b.html", "b.html"}, // force a duplicate past the index
		ModTime: time.Now(),
	}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	g, err := Build(v, ix)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	count := 0
	for _, e := range g.Edges {
		if e.Src == "a.html" && e.Dst == "b.html" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one a->b edge after dedup, got %d (%v)", count, g.Edges)
	}
}
