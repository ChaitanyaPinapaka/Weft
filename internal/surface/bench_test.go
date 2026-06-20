package surface

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

// Deterministic candidate corpus so per-op variance reflects Rank, not inputs.
// Each candidate carries an access history and a single focus-source edge, the
// shape the production surfaceHandler builds.
func makeBenchCandidates(count int, seed int64, now time.Time) []Candidate {
	r := rand.New(rand.NewSource(seed))
	out := make([]Candidate, count)
	sixMonths := int64(180 * 24 * 60 * 60) // seconds
	nowUnix := now.Unix()
	for i := 0; i < count; i++ {
		// 1–3 accesses spread over the last six months.
		n := 1 + r.Intn(3)
		acc := make([]int64, n)
		for j := range acc {
			acc[j] = nowUnix - r.Int63n(sixMonths)
		}
		out[i] = Candidate{
			Path:     fmt.Sprintf("n%04d.html", i+1),
			Title:    fmt.Sprintf("Note %04d", i+1),
			ModTime:  now.Add(-time.Duration(r.Int63n(sixMonths)) * time.Second),
			Accesses: acc,
			Edges: map[string]EdgeSet{
				"focus.html": {
					Backlink:      r.Intn(2) == 0,
					Similarity:    r.Float64(),
					CoAccessCount: r.Intn(4),
				},
			},
		}
	}
	return out
}

func BenchmarkRank1000(b *testing.B) {
	now := time.Unix(1_750_000_000, 0)
	candidates := makeBenchCandidates(1000, 1, now)
	focus := Candidate{Path: "focus.html", Title: "Focus", ModTime: now}
	sources := []Source{{Path: "focus.html", Weight: 1.0}}
	p := DefaultParams()
	noiser := NewNoiser(0, 0, false) // deterministic; measures Rank, not the RNG

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Rank(focus, sources, candidates, now.Unix(), p, noiser, nil)
	}
	b.StopTimer()

	avg := b.Elapsed() / time.Duration(b.N)
	b.ReportMetric(float64(avg.Nanoseconds()), "ns/op-rank")
	if avg > 200*time.Millisecond {
		b.Fatalf("rank budget exceeded: %v > 200ms", avg)
	}
}
