//go:build ORT

package embed

import (
	"os"
	"testing"
)

// TestLocalSmoke loads the real local model, embeds three strings, and asserts
// the cat/feline pair is more similar than cat/earnings.
// Gated on WEFT_EMBED_TEST=1 because it needs network + ~22 MB download.
func TestLocalSmoke(t *testing.T) {
	if os.Getenv("WEFT_EMBED_TEST") != "1" {
		t.Skip("set WEFT_EMBED_TEST=1 to run (downloads embedding model)")
	}
	dir := t.TempDir()
	e, err := NewLocal(dir)
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	defer e.Close()

	if e.Dim() != EmbeddingDim {
		t.Fatalf("Dim = %d, want %d", e.Dim(), EmbeddingDim)
	}

	v1, err := e.Embed("the cat sat on the mat")
	if err != nil {
		t.Fatalf("Embed v1: %v", err)
	}
	v2, err := e.Embed("a feline rested on a rug")
	if err != nil {
		t.Fatalf("Embed v2: %v", err)
	}
	v3, err := e.Embed("quarterly earnings exceeded expectations")
	if err != nil {
		t.Fatalf("Embed v3: %v", err)
	}

	sim12 := CosineSimilarity(v1, v2)
	sim13 := CosineSimilarity(v1, v3)
	if sim12 <= sim13 {
		t.Errorf("expected sim(cat,feline)=%v > sim(cat,earnings)=%v", sim12, sim13)
	}
}
