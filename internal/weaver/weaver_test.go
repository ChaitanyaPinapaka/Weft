package weaver

import (
	"strings"
	"testing"
	"time"

	"weft/internal/embed"
	"weft/internal/index"
	"weft/internal/vault"
)

func newVaultIndex(t *testing.T) (*vault.Vault, *index.Index) {
	t.Helper()
	v, err := vault.New(t.TempDir())
	if err != nil {
		t.Fatalf("vault.New: %v", err)
	}
	ix, err := index.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { ix.Close() })
	return v, ix
}

func TestUnlinkedSemanticPairs(t *testing.T) {
	vecs := map[string][]float32{
		"a.html": {1, 0, 0},
		"b.html": {1, 0, 0}, // identical to a → cosine 1.0
		"c.html": {0, 1, 0}, // orthogonal to a/b → cosine 0
	}
	// No links: only a↔b clears the 0.55 threshold.
	pairs := UnlinkedSemanticPairs(vecs, nil, 0.55, 0)
	if len(pairs) != 1 {
		t.Fatalf("want 1 pair, got %d (%+v)", len(pairs), pairs)
	}
	if pairs[0].A != "a.html" || pairs[0].B != "b.html" {
		t.Fatalf("want canonical a.html<b.html, got %+v", pairs[0])
	}
	if pairs[0].Similarity < 0.99 {
		t.Fatalf("identical vectors should be ~1.0, got %v", pairs[0].Similarity)
	}

	// Marking a↔b already linked drops the only candidate.
	linked := func(x, y string) bool {
		return canonKey(x, y) == canonKey("a.html", "b.html")
	}
	if got := UnlinkedSemanticPairs(vecs, linked, 0.55, 0); len(got) != 0 {
		t.Fatalf("linked pair must be excluded, got %+v", got)
	}
}

func TestGenerateDigestNoEmbeddings(t *testing.T) {
	v, ix := newVaultIndex(t)
	now := time.Date(2026, 6, 19, 9, 0, 0, 0, time.UTC)
	path, n, wrote, err := GenerateDigest(v, ix, now)
	if err != nil {
		t.Fatal(err)
	}
	if wrote || n != 0 {
		t.Fatalf("empty index should propose nothing, got wrote=%v n=%d", wrote, n)
	}
	if v.Exists(path) {
		t.Fatal("no digest file should be written with zero proposals")
	}
}

func TestGenerateDigestWritesDigest(t *testing.T) {
	v, ix := newVaultIndex(t)
	if err := ix.UpsertEmbedding("a.html", embed.Encode([]float32{1, 0, 0})); err != nil {
		t.Fatal(err)
	}
	if err := ix.UpsertEmbedding("b.html", embed.Encode([]float32{1, 0, 0})); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 19, 9, 0, 0, 0, time.UTC)
	path, n, wrote, err := GenerateDigest(v, ix, now)
	if err != nil {
		t.Fatal(err)
	}
	if !wrote || n != 1 || path != "digests/2026-06-19.html" {
		t.Fatalf("want wrote=true n=1 path=digests/2026-06-19.html, got wrote=%v n=%d path=%q", wrote, n, path)
	}
	body, err := v.Read(path)
	if err != nil {
		t.Fatalf("digest not readable: %v", err)
	}
	for _, want := range []string{"/note/a.html", "/note/b.html", "Weave digest"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("digest missing %q:\n%s", want, body)
		}
	}
}

func TestGenerateDigestAllLinked(t *testing.T) {
	v, ix := newVaultIndex(t)
	// a and b are similar but already linked (a → b), so nothing to propose.
	if err := ix.Upsert(index.Note{Path: "a.html", Title: "A", Body: "x", Links: []string{"b.html"}, ModTime: time.Now(), Size: 1}); err != nil {
		t.Fatal(err)
	}
	if err := ix.UpsertEmbedding("a.html", embed.Encode([]float32{1, 0, 0})); err != nil {
		t.Fatal(err)
	}
	if err := ix.UpsertEmbedding("b.html", embed.Encode([]float32{1, 0, 0})); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 19, 9, 0, 0, 0, time.UTC)
	path, n, wrote, err := GenerateDigest(v, ix, now)
	if err != nil {
		t.Fatal(err)
	}
	if wrote || n != 0 {
		t.Fatalf("all-linked should propose nothing, got wrote=%v n=%d", wrote, n)
	}
	if v.Exists(path) {
		t.Fatal("no digest should be written when there are no proposals")
	}
}

func TestGenerateDigestIdempotent(t *testing.T) {
	v, ix := newVaultIndex(t)
	if err := ix.UpsertEmbedding("a.html", embed.Encode([]float32{1, 0, 0})); err != nil {
		t.Fatal(err)
	}
	if err := ix.UpsertEmbedding("b.html", embed.Encode([]float32{1, 0, 0})); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 19, 9, 0, 0, 0, time.UTC)
	path, _, wrote, err := GenerateDigest(v, ix, now)
	if err != nil || !wrote {
		t.Fatalf("first run should write: wrote=%v err=%v", wrote, err)
	}
	first, _ := v.Read(path)

	// Second run for the same date must not overwrite.
	_, n2, wrote2, err := GenerateDigest(v, ix, now)
	if err != nil {
		t.Fatal(err)
	}
	if wrote2 || n2 != 0 {
		t.Fatalf("second run must be a no-op, got wrote=%v n=%d", wrote2, n2)
	}
	second, _ := v.Read(path)
	if string(first) != string(second) {
		t.Fatal("existing digest must be left untouched")
	}
}
