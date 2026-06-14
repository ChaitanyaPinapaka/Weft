package graph

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"weft/internal/embed"
	"weft/internal/index"
	"weft/internal/vault"
)

// BenchmarkBuildSemantic stresses the O(n^2) cosine path at vault scale to
// confirm the snapshot builds quickly for a few thousand notes. Run with:
//
//	go test ./internal/graph -run xxx -bench BuildSemantic -benchtime 1x
func BenchmarkBuildSemantic(b *testing.B) {
	const n = 3000
	root := b.TempDir()
	ix, err := index.OpenMemory()
	if err != nil {
		b.Fatalf("OpenMemory: %v", err)
	}
	b.Cleanup(func() { ix.Close() })

	rng := rand.New(rand.NewSource(1))
	now := time.Now()
	for i := 0; i < n; i++ {
		rel := fmt.Sprintf("n%04d.html", i)
		body := "<p>note</p>"
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644); err != nil {
			b.Fatalf("write: %v", err)
		}
		if err := ix.Upsert(index.Note{Path: rel, Title: rel, Body: body, ModTime: now}); err != nil {
			b.Fatalf("Upsert: %v", err)
		}
		// Random dense vector so cosines are realistically spread (most pairs
		// land below 0.60, exercising the threshold cull on the hot path).
		v := make([]float32, embed.EmbeddingDim)
		for j := range v {
			v[j] = rng.Float32() - 0.5
		}
		if err := ix.UpsertEmbedding(rel, embed.Encode(v)); err != nil {
			b.Fatalf("UpsertEmbedding: %v", err)
		}
	}
	vlt, err := vault.New(root)
	if err != nil {
		b.Fatalf("vault.New: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Build(vlt, ix); err != nil {
			b.Fatal(err)
		}
	}
}
