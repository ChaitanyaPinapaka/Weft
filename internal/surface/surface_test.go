package surface

import (
	"math"
	"testing"
	"time"
)

const day = 24 * 60 * 60 // seconds

func zeroNoise() Noiser { return NewNoiser(0, 0, false) }

// --- BaseLevel ---------------------------------------------------------------

func TestBaseLevelClamps(t *testing.T) {
	p := DefaultParams()
	now := int64(1_000_000_000)

	// t = 0 (accessed this very instant): clamp to MinAge, contributes 1^-d = 1.
	if b := BaseLevel([]int64{now}, now, p); math.IsInf(b, 0) || math.IsNaN(b) || b != 0 {
		t.Fatalf("t=0 should clamp to B=ln(1)=0, got %v", b)
	}
	// t < 0 (future timestamp / clock skew): clamp, no NaN.
	if b := BaseLevel([]int64{now + 500}, now, p); math.IsNaN(b) || b != 0 {
		t.Fatalf("t<0 should clamp to B=0, got %v", b)
	}
}

func TestBaseLevelNeverAccessed(t *testing.T) {
	p := DefaultParams()
	if b := BaseLevel(nil, 1_000_000_000, p); b != p.NeverBase {
		t.Fatalf("never-accessed want NeverBase=%v, got %v", p.NeverBase, b)
	}
	// NeverBase must sit below an ancient single access (100yr ≈ -11).
	ancient := BaseLevel([]int64{1}, int64(100*365*day), p)
	if p.NeverBase >= ancient {
		t.Fatalf("NeverBase=%v must be below ancient single access B=%v", p.NeverBase, ancient)
	}
}

func TestBaseLevelMonotonic(t *testing.T) {
	p := DefaultParams()
	now := int64(10 * 365 * day)
	recent := BaseLevel([]int64{now - day}, now, p)
	old := BaseLevel([]int64{now - 100*day}, now, p)
	if recent <= old {
		t.Fatalf("more recent must have higher base: recent=%v old=%v", recent, old)
	}
	// Frequency: two accesses must beat one at the same recency.
	once := BaseLevel([]int64{now - 10*day}, now, p)
	twice := BaseLevel([]int64{now - 10*day, now - 10*day}, now, p)
	if twice <= once {
		t.Fatalf("more frequent must have higher base: twice=%v once=%v", twice, once)
	}
}

// --- SeedMtime ---------------------------------------------------------------

func TestSeedMtime(t *testing.T) {
	p := DefaultParams()
	mt := time.Unix(12345, 0)
	if got := SeedMtime(nil, mt, p); len(got) != 1 || got[0] != 12345 {
		t.Fatalf("zero accesses should seed [mtime], got %v", got)
	}
	real := []int64{999}
	if got := SeedMtime(real, mt, p); len(got) != 1 || got[0] != 999 {
		t.Fatalf("real accesses must be left untouched, got %v", got)
	}
	p.SeedMtime = false
	if got := SeedMtime(nil, mt, p); got != nil {
		t.Fatalf("SeedMtime=false should not seed, got %v", got)
	}
}

// --- Spread ------------------------------------------------------------------

func TestSpreadBacklinkAndCoAccess(t *testing.T) {
	p := DefaultParams()
	src := []Source{{Path: "focus.html", Weight: 1.0}}
	c := Candidate{Path: "c.html", Edges: map[string]EdgeSet{
		"focus.html": {Backlink: true, CoAccessCount: 9},
	}}
	raw, reasons := Spread(c, src, p, nil)
	wantCo := p.WCoAccess * math.Log1p(9) / math.Log1p(p.CoSaturation)
	want := p.WBacklink + wantCo
	if math.Abs(raw-want) > 1e-9 {
		t.Fatalf("spread want %v, got %v", want, raw)
	}
	if !hasReason(reasons, "backlink") || !hasReason(reasons, "co-access") {
		t.Fatalf("reasons missing: %v", reasons)
	}
}

func TestSpreadSemanticThresholdAndMax(t *testing.T) {
	p := DefaultParams()
	src := []Source{{Path: "a.html", Weight: 1.0}, {Path: "b.html", Weight: 1.0}}
	// Below threshold from a, above from b: only b's contributes (max-over-sources).
	c := Candidate{Path: "c.html", Edges: map[string]EdgeSet{
		"a.html": {Similarity: 0.40},
		"b.html": {Similarity: 0.90},
	}}
	raw, reasons := Spread(c, src, p, nil)
	want := p.WSemantic * 0.90
	if math.Abs(raw-want) > 1e-9 {
		t.Fatalf("semantic max-over-sources want %v, got %v", want, raw)
	}
	if !hasReason(reasons, "semantic") {
		t.Fatalf("semantic reason should fire: %v", reasons)
	}
	// All below threshold → no semantic.
	c2 := Candidate{Path: "c.html", Edges: map[string]EdgeSet{"a.html": {Similarity: 0.40}}}
	if raw2, r2 := Spread(c2, src, p, nil); raw2 != 0 || hasReason(r2, "semantic") {
		t.Fatalf("sub-threshold cosine must not fire: raw=%v reasons=%v", raw2, r2)
	}
}

// TestSpreadSkipsSelfEdge is the lens-2 adversarial regression: a note that is
// both a session source and a candidate must NOT get a semantic boost from its
// self-similarity (cosine 1.0 to itself).
func TestSpreadSkipsSelfEdge(t *testing.T) {
	p := DefaultParams()
	src := []Source{{Path: "self.html", Weight: 1.0}}
	c := Candidate{Path: "self.html", Edges: map[string]EdgeSet{
		"self.html": {Similarity: 1.0, Backlink: true, CoAccessCount: 5},
	}}
	raw, reasons := Spread(c, src, p, nil)
	if raw != 0 || len(reasons) != 0 {
		t.Fatalf("self-edge must be skipped entirely, got raw=%v reasons=%v", raw, reasons)
	}
}

// TestSpreadWithLearnedWeights: a reinforced edge (learned weight > 1) scales the
// whole per-source association up; nil getter leaves the baseline untouched.
func TestSpreadWithLearnedWeights(t *testing.T) {
	p := DefaultParams()
	src := []Source{{Path: "focus.html", Weight: 1.0}}
	c := Candidate{Path: "c.html", Edges: map[string]EdgeSet{
		"focus.html": {Backlink: true, Similarity: 0.90},
	}}
	base, _ := Spread(c, src, p, nil)
	boosted, _ := Spread(c, src, p, func(s, d string) float64 {
		if s == "focus.html" && d == "c.html" {
			return 2.0
		}
		return 1.0
	})
	if math.Abs(boosted-2.0*base) > 1e-9 {
		t.Fatalf("learned weight 2.0 should double the spread: base=%v boosted=%v", base, boosted)
	}
	// An unrelated edge weight must not affect this pair.
	other, _ := Spread(c, src, p, func(s, d string) float64 {
		if s == "x" {
			return 2.0
		}
		return 1.0
	})
	if math.Abs(other-base) > 1e-9 {
		t.Fatalf("unrelated learned edge changed spread: base=%v other=%v", base, other)
	}
}

// TestSpreadNilGetter: a nil learned getter behaves exactly as neutral (1.0).
func TestSpreadNilGetter(t *testing.T) {
	p := DefaultParams()
	src := []Source{{Path: "focus.html", Weight: 1.0}}
	c := Candidate{Path: "c.html", Edges: map[string]EdgeSet{
		"focus.html": {Backlink: true, CoAccessCount: 3},
	}}
	a, _ := Spread(c, src, p, nil)
	b, _ := Spread(c, src, p, func(string, string) float64 { return 1.0 })
	if a != b {
		t.Fatalf("nil getter must equal neutral 1.0: nil=%v neutral=%v", a, b)
	}
}

// --- AttentionWeights --------------------------------------------------------

func TestAttentionWeights(t *testing.T) {
	p := DefaultParams()
	now := int64(1_000_000)
	lastTouch := map[string]int64{
		"focus.html":  now,
		"recent.html": now - 60,   // ~1 min ago
		"older.html":  now - 1800, // 30 min ago
	}
	srcs := []Source{{Path: "focus.html"}, {Path: "recent.html"}, {Path: "older.html"}}
	out := AttentionWeights(srcs, now, lastTouch, "focus.html", p)

	var focusW, recentW, olderW float64
	for _, s := range out {
		switch s.Path {
		case "focus.html":
			focusW = s.Weight
		case "recent.html":
			recentW = s.Weight
		case "older.html":
			olderW = s.Weight
		}
	}
	if focusW != p.FocusWeight {
		t.Fatalf("focus must be pinned at %v, got %v", p.FocusWeight, focusW)
	}
	if recentW <= olderW {
		t.Fatalf("more recent source must out-weight older: recent=%v older=%v", recentW, olderW)
	}
	if math.Abs((recentW+olderW)-p.EarlierBudget) > 1e-9 {
		t.Fatalf("earlier weights must normalize to EarlierBudget=%v, got %v", p.EarlierBudget, recentW+olderW)
	}
}

// --- Noiser ------------------------------------------------------------------

func TestNoiserDeterminism(t *testing.T) {
	if zeroNoise().Noise("x") != 0 {
		t.Fatal("scale 0 must yield 0")
	}
	a := NewNoiser(0.25, 42, false)
	b := NewNoiser(0.25, 42, false)
	for i := 0; i < 100; i++ {
		if a.Noise("p") != b.Noise("p") {
			t.Fatal("same seed must yield identical stream")
		}
	}
}

// --- Rank: the scale-balance contract (lens-1 regression) --------------------

// TestRankAssociationOvercomesRecency encodes the core contract the adversarial
// review demanded: a backlinked note read months ago must out-rank an edgeless
// note read more recently. Without SpreadScale this fails (recency dominates).
func TestRankAssociationOvercomesRecency(t *testing.T) {
	p := DefaultParams()
	now := int64(10 * 365 * day)

	backlinked := Candidate{
		Path: "old-but-linked.html", ModTime: time.Unix(now, 0),
		Accesses: []int64{now - 60*day}, // read ~2 months ago
		Edges:    map[string]EdgeSet{"focus.html": {Backlink: true}},
	}
	recentNoEdge := Candidate{
		Path: "recent-orphan.html", ModTime: time.Unix(now, 0),
		Accesses: []int64{now - 2*day}, // read 2 days ago, but no association
		Edges:    map[string]EdgeSet{},
	}
	sources := []Source{{Path: "focus.html", Weight: 1.0}}
	scored := Rank(Candidate{Path: "focus.html"}, sources,
		[]Candidate{recentNoEdge, backlinked}, now, p, zeroNoise(), nil)

	if len(scored) != 2 || scored[0].Path != "old-but-linked.html" {
		t.Fatalf("association must overcome a months-vs-days recency gap; got order %v",
			[]string{scored[0].Path, scored[1].Path})
	}
}

func TestRankExcludesFocusAndSortsDesc(t *testing.T) {
	now := int64(1_000_000_000)
	p := DefaultParams()
	cands := []Candidate{
		{Path: "focus.html", Accesses: []int64{now - day}},
		{Path: "a.html", Accesses: []int64{now - day}},
		{Path: "b.html", Accesses: []int64{now - 30*day}},
	}
	scored := Rank(Candidate{Path: "focus.html"}, []Source{{Path: "focus.html", Weight: 1}}, cands, now, p, zeroNoise(), nil)
	for _, s := range scored {
		if s.Path == "focus.html" {
			t.Fatal("focus must be excluded")
		}
	}
	for i := 1; i < len(scored); i++ {
		if scored[i-1].Activation < scored[i].Activation {
			t.Fatal("must be sorted descending")
		}
	}
}

func TestRankResurfacedReason(t *testing.T) {
	now := int64(10 * 365 * day)
	p := DefaultParams()
	// Cold base (read ~1yr ago) but strong association → resurfaced.
	cold := Candidate{
		Path: "forgotten.html", ModTime: time.Unix(now, 0),
		Accesses: []int64{now - 365*day},
		Edges:    map[string]EdgeSet{"focus.html": {Backlink: true, Similarity: 0.9}},
	}
	scored := Rank(Candidate{Path: "focus.html"},
		[]Source{{Path: "focus.html", Weight: 1}}, []Candidate{cold}, now, p, zeroNoise(), nil)
	if len(scored) != 1 || !hasReason(scored[0].Reasons, "resurfaced") {
		t.Fatalf("forgotten-but-associated note should be resurfaced: %v", scored)
	}
}

// --- OnThisDay (behavior unchanged) ------------------------------------------

func TestOnThisDay(t *testing.T) {
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	cands := []Candidate{
		{Path: "1yr.html", ModTime: time.Date(2025, 5, 26, 9, 0, 0, 0, time.UTC)},    // within ±3d, prior yr
		{Path: "5yr.html", ModTime: time.Date(2021, 5, 24, 9, 0, 0, 0, time.UTC)},    // within ±3d, prior yr
		{Path: "thisyr.html", ModTime: time.Date(2026, 5, 25, 9, 0, 0, 0, time.UTC)}, // same year → excluded
		{Path: "far.html", ModTime: time.Date(2024, 8, 1, 9, 0, 0, 0, time.UTC)},     // outside window
	}
	got := OnThisDay(cands, now, DefaultParams())
	if len(got) != 2 {
		t.Fatalf("want 2 anniversary notes, got %d (%v)", len(got), paths(got))
	}
	if got[0].Path != "1yr.html" { // most recent first
		t.Fatalf("want most-recent first, got %v", paths(got))
	}
}

// --- helpers -----------------------------------------------------------------

func hasReason(rs []string, want string) bool {
	for _, r := range rs {
		if r == want {
			return true
		}
	}
	return false
}

func paths(cs []Candidate) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Path
	}
	return out
}
