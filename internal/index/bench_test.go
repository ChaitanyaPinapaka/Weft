package index

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"
)

// Fixed-seed corpus generation so successive bench runs measure the same work.
// 200 unique tokens give bodies enough lexical variety for FTS5 ranking to do
// non-trivial work without bloating compile time.
var benchWordlist = buildBenchWordlist(200)

func buildBenchWordlist(n int) []string {
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = fmt.Sprintf("tok%04d", i)
	}
	return out
}

func makeBenchNotes(count int, seed int64) []Note {
	r := rand.New(rand.NewSource(seed))
	notes := make([]Note, count)
	base := time.Unix(1_700_000_000, 0)
	for i := 0; i < count; i++ {
		var body strings.Builder
		body.Grow(50 * 8)
		for j := 0; j < 50; j++ {
			if j > 0 {
				body.WriteByte(' ')
			}
			body.WriteString(benchWordlist[r.Intn(len(benchWordlist))])
		}
		notes[i] = Note{
			Path:    fmt.Sprintf("n%04d.html", i+1),
			Title:   fmt.Sprintf("Note %04d %s", i+1, benchWordlist[r.Intn(len(benchWordlist))]),
			Body:    body.String(),
			ModTime: base.Add(time.Duration(i) * time.Minute),
			Size:    int64(body.Len()),
		}
	}
	return notes
}

func BenchmarkIndexBuild1000(b *testing.B) {
	notes := makeBenchNotes(1000, 1)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ix, err := OpenMemory()
		if err != nil {
			b.Fatalf("OpenMemory: %v", err)
		}
		for _, n := range notes {
			if err := ix.Upsert(n); err != nil {
				ix.Close()
				b.Fatalf("Upsert: %v", err)
			}
		}
		ix.Close()
	}
	b.StopTimer()

	avg := b.Elapsed() / time.Duration(b.N)
	b.ReportMetric(float64(avg.Nanoseconds()), "ns/op-build")
	if avg > 5*time.Second {
		b.Fatalf("index build budget exceeded: %v > 5s", avg)
	}
}

func BenchmarkSearch1000(b *testing.B) {
	notes := makeBenchNotes(1000, 1)
	ix, err := OpenMemory()
	if err != nil {
		b.Fatalf("OpenMemory: %v", err)
	}
	defer ix.Close()
	for _, n := range notes {
		if err := ix.Upsert(n); err != nil {
			b.Fatalf("Upsert: %v", err)
		}
	}
	// "typical query": two real tokens from the corpus joined as an FTS5 AND.
	query := benchWordlist[42] + " " + benchWordlist[137]

	b.ResetTimer()
	start := time.Now()
	for i := 0; i < b.N; i++ {
		hits, err := ix.Search(query, 20)
		if err != nil {
			b.Fatalf("Search: %v", err)
		}
		_ = hits
	}
	elapsed := time.Since(start)
	b.StopTimer()

	avg := elapsed / time.Duration(b.N)
	b.ReportMetric(float64(avg.Nanoseconds()), "ns/op-search")
	if avg > 50*time.Millisecond {
		b.Fatalf("search budget exceeded: %v > 50ms", avg)
	}
}
