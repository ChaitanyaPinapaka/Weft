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

func TestUpsertWritesContent(t *testing.T) {
	ix := newIndex(t)
	body := "<p>hello <b>world</b></p>"
	if err := ix.Upsert(Note{Path: "n.html", Title: "N", Body: body, ModTime: time.Now(), Size: int64(len(body))}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	var got string
	if err := ix.db.QueryRow(`SELECT content FROM notes WHERE path = ?`, "n.html").Scan(&got); err != nil {
		t.Fatalf("scan content: %v", err)
	}
	if got != body {
		t.Fatalf("content = %q, want %q", got, body)
	}

	// Re-upsert should overwrite content.
	body2 := "<p>updated</p>"
	if err := ix.Upsert(Note{Path: "n.html", Title: "N", Body: body2, ModTime: time.Now(), Size: int64(len(body2))}); err != nil {
		t.Fatalf("Upsert overwrite: %v", err)
	}
	if err := ix.db.QueryRow(`SELECT content FROM notes WHERE path = ?`, "n.html").Scan(&got); err != nil {
		t.Fatalf("scan content2: %v", err)
	}
	if got != body2 {
		t.Fatalf("content after overwrite = %q, want %q", got, body2)
	}
}

func TestCoAccessed(t *testing.T) {
	ix := newIndex(t)
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC).Unix()

	// a and b accessed 5 minutes apart -> co-accessed within a 30-minute window.
	// c accessed 2 hours later -> not co-accessed.
	mustLog := func(path string, ts int64) {
		t.Helper()
		if err := ix.LogAccess(path, ts); err != nil {
			t.Fatalf("LogAccess(%s): %v", path, err)
		}
	}
	mustLog("a.html", base)
	mustLog("b.html", base+5*60)
	mustLog("c.html", base+2*60*60)

	window := 30 * time.Minute

	got, err := ix.CoAccessed("a.html", window)
	if err != nil {
		t.Fatalf("CoAccessed a: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"b.html"}) {
		t.Fatalf("CoAccessed a = %v, want [b.html]", got)
	}

	got, err = ix.CoAccessed("b.html", window)
	if err != nil {
		t.Fatalf("CoAccessed b: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"a.html"}) {
		t.Fatalf("CoAccessed b = %v, want [a.html]", got)
	}

	// Querying a's neighbors must never include a itself.
	mustLog("a.html", base+60)
	got, err = ix.CoAccessed("a.html", window)
	if err != nil {
		t.Fatalf("CoAccessed a after self re-log: %v", err)
	}
	for _, p := range got {
		if p == "a.html" {
			t.Fatalf("CoAccessed result must exclude queried path, got %v", got)
		}
	}

	// c is far outside the window from a.
	got, _ = ix.CoAccessed("c.html", window)
	if len(got) != 0 {
		t.Fatalf("CoAccessed c = %v, want empty", got)
	}
}

func TestStale(t *testing.T) {
	ix := newIndex(t)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	// Unknown path is always stale.
	stale, err := ix.Stale("missing.html", now)
	if err != nil {
		t.Fatalf("Stale missing: %v", err)
	}
	if !stale {
		t.Fatalf("unknown path must be stale")
	}

	if err := ix.Upsert(Note{Path: "k.html", Title: "K", Body: "x", ModTime: now, Size: 1}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		fs   time.Time
		want bool
	}{
		{"equal", now, false},
		{"newer-fs", now.Add(time.Minute), true},
		{"older-fs", now.Add(-time.Minute), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ix.Stale("k.html", tc.fs)
			if err != nil {
				t.Fatalf("Stale: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Stale(%v) = %v, want %v", tc.fs, got, tc.want)
			}
		})
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
