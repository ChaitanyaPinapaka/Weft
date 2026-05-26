package surface

import (
	"math"
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

func TestOnThisDayEmpty(t *testing.T) {
	got := OnThisDay(nil, time.Now())
	if got == nil {
		t.Fatal("OnThisDay returned nil; want empty slice")
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

func TestBacklinkPlusSemanticBeatsRecency(t *testing.T) {
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	mod := now.AddDate(0, 0, -3)
	// recency-only candidate (very fresh) vs backlink + 0.5 similarity
	hot := Candidate{Path: "hot.html", ModTime: now} // recency ~ 0.2
	mix := Candidate{Path: "mix.html", ModTime: mod, HasBacklink: true, Similarity: 0.5}
	got := Rank(Candidate{Path: "cur"}, []Candidate{hot, mix}, now)
	if len(got) != 2 {
		t.Fatalf("len = %d; want 2", len(got))
	}
	if got[0].Path != "mix.html" {
		t.Fatalf("top = %q; want mix.html; scores=%v/%v", got[0].Path, got[0].Score, got[1].Score)
	}
	// 0.4 (backlink) + 0.2 (0.4 * 0.5 semantic) + recency component (~0.18) ≈ 0.78
	if got[0].Score < 0.6 {
		t.Fatalf("mix score = %v; want >= 0.6", got[0].Score)
	}
}

func TestCoAccessedAddsBonus(t *testing.T) {
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	mod := now.AddDate(0, 0, -3)
	plain := Candidate{Path: "plain.html", ModTime: mod, HasBacklink: true}
	withCo := Candidate{Path: "co.html", ModTime: mod, HasBacklink: true, CoAccessed: true}
	got := Rank(Candidate{Path: "cur"}, []Candidate{plain, withCo}, now)
	if len(got) != 2 {
		t.Fatalf("len = %d; want 2", len(got))
	}
	if got[0].Path != "co.html" {
		t.Fatalf("top = %q; want co.html", got[0].Path)
	}
	diff := got[0].Score - got[1].Score
	if math.Abs(diff-coAccessBonus) > 1e-9 {
		t.Fatalf("score diff = %v; want %v", diff, coAccessBonus)
	}
	if !hasReason(got[0].Reasons, "co-accessed") {
		t.Fatalf("reasons = %v; want co-accessed", got[0].Reasons)
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
		{Path: "d", ModTime: now.AddDate(0, 0, -1), HasBacklink: true, CoAccessed: true},
	}
	got := Rank(Candidate{Path: "cur"}, cands, now)
	if len(got) != 4 {
		t.Fatalf("len = %d; want 4", len(got))
	}
	if !sort.SliceIsSorted(got, func(i, j int) bool { return got[i].Score > got[j].Score }) {
		scores := make([]float64, len(got))
		for i, s := range got {
			scores[i] = s.Score
		}
		t.Fatalf("not sorted desc: %v", scores)
	}
}

func TestZeroScoreDropped(t *testing.T) {
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	// Bare candidate with zero values: no backlink, no mod time, no similarity, no co-access.
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

func TestRankNoOnThisDayReason(t *testing.T) {
	// On-this-day logic must no longer live inside Rank.
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	c := Candidate{Path: "old.html", ModTime: now.AddDate(-1, 0, 0)}
	got := Rank(Candidate{Path: "cur"}, []Candidate{c}, now)
	for _, s := range got {
		for _, r := range s.Reasons {
			if strings.HasPrefix(r, "on-this-day") {
				t.Fatalf("Rank still emits on-this-day reason: %v", s.Reasons)
			}
		}
	}
}

func TestOnThisDayPriorYearExactMatch(t *testing.T) {
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	c := Candidate{Path: "p.html", ModTime: time.Date(2024, 5, 25, 9, 0, 0, 0, time.UTC)}
	got := OnThisDay([]Candidate{c}, now)
	if len(got) != 1 || got[0].Path != "p.html" {
		t.Fatalf("got %+v; want one hit for p.html", got)
	}
}

func TestOnThisDayWindowIncludesPlusMinusThree(t *testing.T) {
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	cands := []Candidate{
		{Path: "m3.html", ModTime: time.Date(2024, 5, 22, 0, 0, 0, 0, time.UTC)},
		{Path: "p3.html", ModTime: time.Date(2024, 5, 28, 0, 0, 0, 0, time.UTC)},
		{Path: "m4.html", ModTime: time.Date(2024, 5, 21, 0, 0, 0, 0, time.UTC)}, // outside
		{Path: "p4.html", ModTime: time.Date(2024, 5, 29, 0, 0, 0, 0, time.UTC)}, // outside
	}
	got := OnThisDay(cands, now)
	if len(got) != 2 {
		t.Fatalf("len = %d; want 2; got=%+v", len(got), got)
	}
	paths := map[string]bool{}
	for _, c := range got {
		paths[c.Path] = true
	}
	if !paths["m3.html"] || !paths["p3.html"] {
		t.Fatalf("missing ±3 hits: %v", paths)
	}
}

func TestOnThisDayExcludesSameYear(t *testing.T) {
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	cands := []Candidate{
		{Path: "today.html", ModTime: time.Date(2026, 5, 25, 6, 0, 0, 0, time.UTC)},
		{Path: "near.html", ModTime: time.Date(2026, 5, 23, 0, 0, 0, 0, time.UTC)},
	}
	got := OnThisDay(cands, now)
	if len(got) != 0 {
		t.Fatalf("len = %d; want 0 (same year excluded)", len(got))
	}
}

func TestOnThisDaySortedByModTimeDesc(t *testing.T) {
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	cands := []Candidate{
		{Path: "old.html", ModTime: time.Date(2020, 5, 25, 0, 0, 0, 0, time.UTC)},
		{Path: "newer.html", ModTime: time.Date(2024, 5, 26, 0, 0, 0, 0, time.UTC)},
		{Path: "mid.html", ModTime: time.Date(2022, 5, 24, 0, 0, 0, 0, time.UTC)},
	}
	got := OnThisDay(cands, now)
	if len(got) != 3 {
		t.Fatalf("len = %d; want 3", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].ModTime.Before(got[i].ModTime) {
			t.Fatalf("not sorted desc by ModTime: %+v", got)
		}
	}
	if got[0].Path != "newer.html" {
		t.Fatalf("first = %q; want newer.html", got[0].Path)
	}
}

func TestOnThisDayIgnoresZeroModTime(t *testing.T) {
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	got := OnThisDay([]Candidate{{Path: "z.html"}}, now)
	if len(got) != 0 {
		t.Fatalf("len = %d; want 0", len(got))
	}
}

func hasReason(reasons []string, want string) bool {
	for _, r := range reasons {
		if r == want {
			return true
		}
	}
	return false
}
