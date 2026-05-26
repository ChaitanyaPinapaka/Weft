package surface

import (
	"sort"
	"strings"
	"testing"
	"time"
)

func TestEmptyCandidates(t *testing.T) {
	got := Rank(Candidate{Path: "a"}, nil, time.Now())
	if got == nil {
		t.Fatal("Rank returned nil; want empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("len = %d; want 0", len(got))
	}
}

func TestCurrentExcluded(t *testing.T) {
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	current := Candidate{Path: "me.html"}
	cands := []Candidate{
		{Path: "me.html", HasBacklink: true},
		{Path: "other.html", HasBacklink: true},
	}
	got := Rank(current, cands, now)
	if len(got) != 1 {
		t.Fatalf("len = %d; want 1", len(got))
	}
	if got[0].Path != "other.html" {
		t.Fatalf("got %q; want other.html", got[0].Path)
	}
}

func TestBacklinkBeatsRecency(t *testing.T) {
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	mod := now.AddDate(0, 0, -3) // 3 days ago, similar age for both
	cands := []Candidate{
		{Path: "recent.html", ModTime: mod},
		{Path: "linked.html", ModTime: mod, HasBacklink: true},
	}
	got := Rank(Candidate{Path: "cur"}, cands, now)
	if len(got) != 2 {
		t.Fatalf("len = %d; want 2", len(got))
	}
	if got[0].Path != "linked.html" {
		t.Fatalf("top = %q; want linked.html", got[0].Path)
	}
}

func TestOnThisDay1yBeats5y(t *testing.T) {
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	one := Candidate{Path: "1y.html", ModTime: now.AddDate(-1, 0, 0)}
	five := Candidate{Path: "5y.html", ModTime: now.AddDate(-5, 0, 0)}
	got := Rank(Candidate{Path: "cur"}, []Candidate{five, one}, now)
	if len(got) != 2 {
		t.Fatalf("len = %d; want 2", len(got))
	}
	if got[0].Path != "1y.html" {
		t.Fatalf("top = %q; want 1y.html", got[0].Path)
	}
	// Reason should mention on-this-day.
	if !hasReasonPrefix(got[0].Reasons, "on-this-day:") {
		t.Fatalf("reasons = %v; want on-this-day", got[0].Reasons)
	}
}

func TestSameYearSameDayNoOnThisDay(t *testing.T) {
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	// Same month+day, same year — must not fire on-this-day.
	c := Candidate{Path: "today.html", ModTime: time.Date(2026, 5, 25, 6, 0, 0, 0, time.UTC)}
	got := Rank(Candidate{Path: "cur"}, []Candidate{c}, now)
	if len(got) != 1 {
		t.Fatalf("len = %d; want 1", len(got))
	}
	for _, r := range got[0].Reasons {
		if strings.HasPrefix(r, "on-this-day") {
			t.Fatalf("unexpected on-this-day reason: %v", got[0].Reasons)
		}
	}
}

func TestSimilarityOrdering(t *testing.T) {
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	mod := now.AddDate(0, 0, -10)
	lo := Candidate{Path: "lo.html", ModTime: mod, Similarity: 0.1}
	hi := Candidate{Path: "hi.html", ModTime: mod, Similarity: 0.9}
	got := Rank(Candidate{Path: "cur"}, []Candidate{lo, hi}, now)
	if len(got) != 2 {
		t.Fatalf("len = %d; want 2", len(got))
	}
	if got[0].Path != "hi.html" {
		t.Fatalf("top = %q; want hi.html", got[0].Path)
	}
}

func TestSortedDescending(t *testing.T) {
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	cands := []Candidate{
		{Path: "a", ModTime: now.AddDate(0, 0, -1), Similarity: 0.2},
		{Path: "b", ModTime: now.AddDate(0, 0, -1), HasBacklink: true},
		{Path: "c", ModTime: now.AddDate(0, 0, -1), Similarity: 0.5},
		{Path: "d", ModTime: now.AddDate(-1, 0, 0)}, // on-this-day 1y
	}
	got := Rank(Candidate{Path: "cur"}, cands, now)
	if len(got) != 4 {
		t.Fatalf("len = %d; want 4", len(got))
	}
	scores := make([]float64, len(got))
	for i, s := range got {
		scores[i] = s.Score
	}
	if !sort.SliceIsSorted(got, func(i, j int) bool { return got[i].Score > got[j].Score }) {
		t.Fatalf("not sorted desc: %v", scores)
	}
}

func TestZeroScoreDropped(t *testing.T) {
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	// Bare candidate with zero values: no backlink, no mod time, no similarity.
	got := Rank(Candidate{Path: "cur"}, []Candidate{{Path: "empty.html"}}, now)
	if len(got) != 0 {
		t.Fatalf("len = %d; want 0", len(got))
	}
}

func TestLastAccessedDominatesModTime(t *testing.T) {
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	oldMod := now.AddDate(-2, 0, 0)
	// One stale on disk but freshly accessed; one stale and untouched.
	fresh := Candidate{Path: "fresh.html", ModTime: oldMod, LastAccessed: now.AddDate(0, 0, -1)}
	stale := Candidate{Path: "stale.html", ModTime: oldMod}
	got := Rank(Candidate{Path: "cur"}, []Candidate{stale, fresh}, now)
	if len(got) < 1 || got[0].Path != "fresh.html" {
		t.Fatalf("top = %+v; want fresh.html first", got)
	}
}

func hasReasonPrefix(reasons []string, prefix string) bool {
	for _, r := range reasons {
		if strings.HasPrefix(r, prefix) {
			return true
		}
	}
	return false
}
