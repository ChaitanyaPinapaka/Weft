package index

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func newIndex(t *testing.T) *Index {
	t.Helper()
	ix, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { ix.Close() })
	return ix
}

func TestOpenCreatesDir(t *testing.T) {
	root := t.TempDir()
	ix, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer ix.Close()
	if _, err := filepath.Glob(filepath.Join(root, ".weft", "index.db")); err != nil {
		t.Fatalf("expected .weft/index.db: %v", err)
	}
}

func TestUpsertAndSearch(t *testing.T) {
	ix := newIndex(t)
	now := time.Now()
	notes := []Note{
		{Path: "a.html", Title: "Apples", Body: "apples are red and crunchy", ModTime: now, Size: 10},
		{Path: "b.html", Title: "Bananas", Body: "bananas are yellow fruit", ModTime: now, Size: 20},
		{Path: "c.html", Title: "Mixed", Body: "apples and bananas together", ModTime: now, Size: 30},
	}
	for _, n := range notes {
		if err := ix.Upsert(n); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
	}

	hits, err := ix.Search("apples", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits, got %d (%v)", len(hits), hits)
	}

	// Upsert overwrite: bump body + title, expect old terms gone.
	if err := ix.Upsert(Note{Path: "a.html", Title: "Oranges", Body: "oranges now", ModTime: now, Size: 5}); err != nil {
		t.Fatal(err)
	}
	hits, _ = ix.Search("apples", 10)
	if len(hits) != 1 {
		t.Fatalf("after overwrite expected 1 apples hit, got %d", len(hits))
	}
	hits, _ = ix.Search("oranges", 10)
	if len(hits) != 1 || hits[0].Path != "a.html" {
		t.Fatalf("expected oranges hit on a.html, got %v", hits)
	}
}

func TestBacklinksRoundTrip(t *testing.T) {
	ix := newIndex(t)
	now := time.Now()
	srcHTML := `<p>see <a href="target.html">t</a> and <a href="other.html">o</a></p>`
	if err := ix.Upsert(Note{
		Path: "src.html", Title: "Src",
		Body:    srcHTML,
		Links:   ParseLinks([]byte(srcHTML)),
		ModTime: now,
	}); err != nil {
		t.Fatal(err)
	}
	src2HTML := `<a href="target.html">t</a>`
	if err := ix.Upsert(Note{
		Path: "src2.html", Title: "Src2",
		Body:    src2HTML,
		Links:   ParseLinks([]byte(src2HTML)),
		ModTime: now,
	}); err != nil {
		t.Fatal(err)
	}

	from, err := ix.LinksFrom("src.html")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(from, []string{"other.html", "target.html"}) {
		t.Fatalf("LinksFrom = %v", from)
	}

	to, err := ix.BacklinksTo("target.html")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(to, []string{"src.html", "src2.html"}) {
		t.Fatalf("BacklinksTo = %v", to)
	}

	// Re-upserting src with no link to target should remove that backlink.
	if err := ix.Upsert(Note{Path: "src.html", Title: "Src", Body: `<p>nothing</p>`, Links: nil, ModTime: now}); err != nil {
		t.Fatal(err)
	}
	to, _ = ix.BacklinksTo("target.html")
	if !reflect.DeepEqual(to, []string{"src2.html"}) {
		t.Fatalf("after re-upsert BacklinksTo = %v", to)
	}
}

func TestDelete(t *testing.T) {
	ix := newIndex(t)
	now := time.Now()
	xHTML := `<a href="y.html">y</a> hello world`
	_ = ix.Upsert(Note{Path: "x.html", Title: "X", Body: xHTML, Links: ParseLinks([]byte(xHTML)), ModTime: now})

	if err := ix.Delete("x.html"); err != nil {
		t.Fatal(err)
	}
	hits, _ := ix.Search("hello", 10)
	if len(hits) != 0 {
		t.Fatalf("expected no hits after delete, got %v", hits)
	}
	from, _ := ix.LinksFrom("x.html")
	if len(from) != 0 {
		t.Fatalf("expected no links from deleted note, got %v", from)
	}
}
