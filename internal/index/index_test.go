package index

import (
	"path/filepath"
	"reflect"
	"strings"
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

func TestParseTags(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"single", `<p>hello #alpha world</p>`, []string{"alpha"}},
		{"multiple sorted dedup", `<p>#beta #alpha #beta #gamma</p>`, []string{"alpha", "beta", "gamma"}},
		{"case insensitive", `<p>#Alpha and #ALPHA and #alpha</p>`, []string{"alpha"}},
		{"hyphen and digits", `<p>#beta-1 #foo_bar #x9</p>`, []string{"beta-1", "foo_bar", "x9"}},
		{"length at cap", `<p>#` + strings.Repeat("a", 31) + ` end</p>`, []string{strings.Repeat("a", 31)}},
		{"over length rejected", `<p>#` + strings.Repeat("a", 40) + ` end</p>`, nil},
		{"href anchor ignored", `<p>see <a href="#anchor">x</a> and #real</p>`, []string{"real"}},
		{"non-letter first char", `<p>#1 #-foo #_bar nothing</p>`, nil},
		{"mid-word", `<p>foo#bar nope</p>`, nil},
		{"start of string", `#first then text`, []string{"first"}},
		{"script and style ignored", `<style>#sel { color: red }</style><script>var x = "#nope"</script><p>#yes</p>`, []string{"yes"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseTags([]byte(tc.in))
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseTags(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestUpsertTags(t *testing.T) {
	ix := newIndex(t)
	now := time.Now()
	body := `<p>tagged #alpha and #beta-1 and #ALPHA again</p>`
	if err := ix.Upsert(Note{Path: "n.html", Title: "N", Body: body, ModTime: now}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := ix.NotesWithTag("alpha")
	if err != nil {
		t.Fatalf("NotesWithTag alpha: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"n.html"}) {
		t.Fatalf("alpha paths = %v", got)
	}
	got, _ = ix.NotesWithTag("beta-1")
	if !reflect.DeepEqual(got, []string{"n.html"}) {
		t.Fatalf("beta-1 paths = %v", got)
	}

	// Verify only the expected tag set is stored for this note.
	rows, err := ix.db.Query(`SELECT tag FROM tags WHERE path = ? ORDER BY tag`, "n.html")
	if err != nil {
		t.Fatal(err)
	}
	var tags []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		tags = append(tags, s)
	}
	rows.Close()
	if !reflect.DeepEqual(tags, []string{"alpha", "beta-1"}) {
		t.Fatalf("stored tags = %v, want [alpha beta-1]", tags)
	}

	// Re-upsert drops prior tags.
	if err := ix.Upsert(Note{Path: "n.html", Title: "N", Body: `<p>only #gamma</p>`, ModTime: now}); err != nil {
		t.Fatal(err)
	}
	rows, _ = ix.db.Query(`SELECT tag FROM tags WHERE path = ? ORDER BY tag`, "n.html")
	tags = nil
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		tags = append(tags, s)
	}
	rows.Close()
	if !reflect.DeepEqual(tags, []string{"gamma"}) {
		t.Fatalf("after re-upsert tags = %v, want [gamma]", tags)
	}
}

func TestAllTags(t *testing.T) {
	ix := newIndex(t)
	now := time.Now()
	mustUpsert := func(path, body string) {
		t.Helper()
		if err := ix.Upsert(Note{Path: path, Title: path, Body: body, ModTime: now}); err != nil {
			t.Fatal(err)
		}
	}
	mustUpsert("a.html", `<p>#shared #only-a</p>`)
	mustUpsert("b.html", `<p>#shared #only-b</p>`)
	mustUpsert("c.html", `<p>#shared</p>`)

	got, err := ix.AllTags()
	if err != nil {
		t.Fatalf("AllTags: %v", err)
	}
	want := []TagCount{
		{Tag: "shared", Count: 3},
		{Tag: "only-a", Count: 1},
		{Tag: "only-b", Count: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AllTags = %v, want %v", got, want)
	}
}

func TestNotesWithTag(t *testing.T) {
	ix := newIndex(t)
	now := time.Now()
	if err := ix.Upsert(Note{Path: "b.html", Title: "B", Body: `<p>#shared</p>`, ModTime: now}); err != nil {
		t.Fatal(err)
	}
	if err := ix.Upsert(Note{Path: "a.html", Title: "A", Body: `<p>#shared</p>`, ModTime: now}); err != nil {
		t.Fatal(err)
	}
	got, err := ix.NotesWithTag("shared")
	if err != nil {
		t.Fatalf("NotesWithTag: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"a.html", "b.html"}) {
		t.Fatalf("NotesWithTag = %v", got)
	}
}

func TestDeleteRemovesTags(t *testing.T) {
	ix := newIndex(t)
	now := time.Now()
	if err := ix.Upsert(Note{Path: "d.html", Title: "D", Body: `<p>#droppable</p>`, ModTime: now}); err != nil {
		t.Fatal(err)
	}
	if err := ix.Delete("d.html"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, _ := ix.AllTags()
	for _, tc := range got {
		if tc.Tag == "droppable" {
			t.Fatalf("AllTags still contains droppable: %v", got)
		}
	}
	paths, _ := ix.NotesWithTag("droppable")
	if len(paths) != 0 {
		t.Fatalf("NotesWithTag droppable = %v, want empty", paths)
	}
}

// TestRemove verifies the trash-path cleanup: every per-note table loses its
// x.html rows — including backlinks in BOTH directions (unlike Delete, whose
// incoming rows the rename flow rewrites afterwards) — while the append-only
// access_log keeps its behavioral history.
func TestRemove(t *testing.T) {
	ix := newIndex(t)
	now := time.Now()
	xHTML := `<a href="y.html">y</a> remove me #gone`
	yHTML := `<a href="x.html">x</a> survives`
	if err := ix.Upsert(Note{Path: "x.html", Title: "X", Body: xHTML, Links: ParseLinks([]byte(xHTML)), ModTime: now}); err != nil {
		t.Fatal(err)
	}
	if err := ix.Upsert(Note{Path: "y.html", Title: "Y", Body: yHTML, Links: ParseLinks([]byte(yHTML)), ModTime: now}); err != nil {
		t.Fatal(err)
	}
	if err := ix.UpsertEmbedding("x.html", []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if err := ix.LogAccess("x.html", now.Unix()); err != nil {
		t.Fatal(err)
	}

	if err := ix.Remove("x.html"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	for _, q := range []string{
		`SELECT COUNT(*) FROM notes WHERE path = 'x.html'`,
		`SELECT COUNT(*) FROM notes_fts WHERE path = 'x.html'`,
		`SELECT COUNT(*) FROM backlinks WHERE src = 'x.html'`,
		`SELECT COUNT(*) FROM backlinks WHERE dst = 'x.html'`,
		`SELECT COUNT(*) FROM embeddings WHERE path = 'x.html'`,
		`SELECT COUNT(*) FROM tags WHERE path = 'x.html'`,
	} {
		var n int
		if err := ix.db.QueryRow(q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if n != 0 {
			t.Errorf("%s = %d, want 0", q, n)
		}
	}

	var logs int
	if err := ix.db.QueryRow(`SELECT COUNT(*) FROM access_log WHERE path = 'x.html'`).Scan(&logs); err != nil {
		t.Fatal(err)
	}
	if logs != 1 {
		t.Fatalf("access_log rows = %d, want 1 (history is kept)", logs)
	}

	var yCount int
	if err := ix.db.QueryRow(`SELECT COUNT(*) FROM notes WHERE path = 'y.html'`).Scan(&yCount); err != nil {
		t.Fatal(err)
	}
	if yCount != 1 {
		t.Fatal("y.html must survive x.html's removal")
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
