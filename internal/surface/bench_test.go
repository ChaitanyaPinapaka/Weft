package surface

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

// Deterministic candidate corpus so per-op variance reflects Rank, not inputs.
func makeBenchCandidates(count int, seed int64, now time.Time) []Candidate {
	r := rand.New(rand.NewSource(seed))
	out := make([]Candidate, count)
	sixMonths := 180 * 24 * time.Hour
	for i := 0; i < count; i++ {
		offset := time.Duration(r.Int63n(int64(sixMonths)))
		out[i] = Candidate{
			Path:        fmt.Sprintf("n%04d.html", i+1),
			Title:       fmt.Sprintf("Note %04d", i+1),
			ModTime:     now.Add(-offset),
			HasBacklink: r.Intn(2) == 0,
			Similarity:  r.Float64(),
		}
	}
	return out
}

func BenchmarkRank1000(b *testing.B) {
	now := time.Unix(1_750_000_000, 0)
	candidates := makeBenchCandidates(1000, 1, now)
	current := Candidate{Path: "current.html", Title: "Current", ModTime: now}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out := Rank(current, candidates, now)
		_ = out
	}
	b.StopTimer()

	avg := b.Elapsed() / time.Duration(b.N)
	b.ReportMetric(float64(avg.Nanoseconds()), "ns/op-rank")
	if avg > 200*time.Millisecond {
		b.Fatalf("rank budget exceeded: %v > 200ms", avg)
	}
}
