// Package surface ranks candidate notes for the brain panel using an ACT-R
// activation model — the canonical computational model of human declarative
// memory (Anderson). This replaces the earlier ad-hoc weighted sum.
//
//	A_i = B_i + SpreadScale · Σ_k (W_k · S_ki) + ε
//	B_i = ln( Σ_j t_j^(-d) )                         base-level (recency+frequency)
//	S_ki = wBack·back + wSem·sem + wCo·co            association, source k → candidate i
//	W_k  = attention weight of source k (focus=1, earlier-session decayed)
//	ε    = transient noise (occasional surprising resurfacing is a feature)
//
// Pure scoring only — no I/O, no DB, no HTTP. The index/server packages produce
// Candidates (access history + per-source association edges); this ranks them.
//
// All times are UNIX SECONDS. d is unit-relative, so the second is locked as the
// reference unit in and out — converting to days would silently rescale the
// whole activation surface and break the base/spread balance (see SpreadScale).
package surface

import (
	"math"
	"sort"
	"time"
)

// Params is the single tuning surface. The defaults are starting points to be
// refined by dogfooding, not truths — the ACT-R formula *shape* is locked, the
// values are not.
type Params struct {
	// Base level (forgetting curve).
	Decay      float64 // d in t^-d
	MinAgeSecs float64 // floor on t_j (clamps just-now t=0 → +Inf, and clock skew t<0)
	NeverBase  float64 // finite B for zero-access notes (see DefaultParams rationale)
	SeedMtime  bool    // treat file mtime as one synthetic access when a note has no real ones

	// Association weights and gates.
	WBacklink    float64
	WSemantic    float64
	WCoAccess    float64
	SemThreshold float64 // cosine floor for a semantic edge to fire
	CoSaturation float64 // log1p saturation point for co-access count

	// SpreadScale lifts the association term onto the same dynamic range as the
	// base level so surfacing is association-driven, not a recency sort with a
	// tiebreak. Derivation: base gap over a recency horizon is ΔB = d·ln(t_old/t_new).
	// We want a strong edge (~0.4 before scaling) to let a note read ~90 days ago
	// compete with one read yesterday: ΔB = 0.5·ln(90) ≈ 2.25, so SpreadScale ≈
	// 2.25/0.4 ≈ 5.6 → 6.0 with headroom. Without this the forgetting curve
	// out-ranges the entire association term ~7× and "surfacing" collapses to
	// "most recently opened."
	SpreadScale float64

	// Attention / session.
	FocusWeight     float64       // W of the focused note (pinned, never out-weighted)
	AttnTauSecs     float64       // exp time-constant for earlier-source decay
	AttnFloor       float64       // drop sources whose decayed W falls below this
	EarlierBudget   float64       // earlier-session weights normalize to sum = this
	SessionGap      time.Duration // max inter-access gap within a session (also co-access window)
	SessionLookback time.Duration // hard cap on session reach-back (an all-day tab can't drag stale sources)

	// Noise.
	NoiseScale float64 // logistic scale s; 0 disables (deterministic)
	Gaussian   bool    // true → N(0, s²) instead of Logistic(0, s)

	// Output.
	TopN                int
	OnThisDayWindowDays int
}

// DefaultParams returns the v0.1 starting weights.
func DefaultParams() Params {
	return Params{
		Decay:      0.5,
		MinAgeSecs: 1.0,
		// B for a single access of age t is -d·ln(t). A 100-year-old single
		// access gives ≈ -0.5·ln(3.15e9) ≈ -11.0; -12 clears that so a
		// never-accessed note can never out-rank a genuinely (even ancient)
		// accessed one on base level alone — it surfaces only via association.
		NeverBase:           -12.0,
		SeedMtime:           true,
		WBacklink:           0.4,
		WSemantic:           0.4,
		WCoAccess:           0.2,
		SemThreshold:        0.55, // tuned for bge-small / all-MiniLM cosines
		CoSaturation:        10.0,
		SpreadScale:         6.0,
		FocusWeight:         1.0,
		AttnTauSecs:         900.0, // 15 min
		AttnFloor:           1e-3,
		EarlierBudget:       1.0,
		SessionGap:          30 * time.Minute,
		SessionLookback:     8 * time.Hour,
		NoiseScale:          0.25, // logistic σ ≈ 0.45 — a fraction of a scaled edge (~2.4)
		Gaussian:            false,
		TopN:                10,
		OnThisDayWindowDays: 3,
	}
}

// Candidate is a note considered for surfacing. Accesses is its real access
// history (ascending unix-sec, nil if never opened). Edges holds, per SOURCE
// path, the association inputs for this candidate.
type Candidate struct {
	Path     string
	Title    string
	ModTime  time.Time
	Accesses []int64
	Edges    map[string]EdgeSet
}

// EdgeSet is the association input from one source note to a candidate.
type EdgeSet struct {
	Backlink      bool
	Similarity    float64 // raw cosine of source vs candidate; gated at score time
	CoAccessCount int     // distinct co-access events with the source, in SessionGap
}

// Source is an activation source (the focus note, plus earlier-session notes),
// with its attention weight already computed.
type Source struct {
	Path   string
	Weight float64
}

// Scored is a Candidate annotated with its activation and a breakdown.
type Scored struct {
	Candidate
	Activation float64
	Base       float64
	Spread     float64 // already scaled by SpreadScale (so Base+Spread+noise = Activation)
	Reasons    []string
}

// BaseLevel computes B_i = ln(Σ t_j^-d) with the MinAge clamp and the
// never-accessed floor. nowUnix is now.Unix().
func BaseLevel(accesses []int64, nowUnix int64, p Params) float64 {
	var sum float64
	for _, ts := range accesses {
		t := float64(nowUnix - ts)
		if t < p.MinAgeSecs {
			t = p.MinAgeSecs // clamps t=0 (→+Inf) and t<0 (clock skew/future mtime)
		}
		sum += math.Pow(t, -p.Decay)
	}
	if sum <= 0 { // empty slice → ln(0) guard
		return p.NeverBase
	}
	return math.Log(sum)
}

// SeedMtime injects one synthetic mtime access iff a note has no real accesses,
// so brand-new / freshly-clipped notes have a finite, recency-weighted base
// before their first open. The synthetic timestamp lives only in the returned
// slice — it is never written to the access log. When real accesses exist they
// are the truth and we leave them untouched.
//
// Known trade-off: a bulk mtime rewrite (git checkout, rsync, touch *.html)
// homogenizes the base level of every never-opened note to ~now, flattening the
// cold tail's recency ordering until each note is individually opened. Set
// p.SeedMtime=false to disable, or log a creation access at note-create time
// instead. Acceptable for v0.1 — real reads dominate and noise breaks ties.
func SeedMtime(accesses []int64, mtime time.Time, p Params) []int64 {
	if !p.SeedMtime || mtime.IsZero() || len(accesses) > 0 {
		return accesses
	}
	return []int64{mtime.Unix()}
}

// Spread computes Σ_k W_k·S_ki for one candidate, returning the raw (pre-scale)
// association total plus the reasons that fired. Backlink and co-access are
// per-source additive; semantic is max-over-sources of the gated cosine (a note
// mildly similar to many sources should not beat one strongly similar to the
// focus — switch to strict additive by moving the block into the per-source sum).
//
// Self-edges (src.Path == c.Path) are skipped: a session source that is also a
// candidate has cosine 1.0 to itself and would fabricate a +WSemantic boost.
//
// learned(src, dst) is the reinforcement multiplier for the src→candidate
// association (clicking a surfaced note strengthens its edge; a dormant edge
// decays back to 1.0). It scales the whole per-source contribution — both the
// backlink/co-access term and the semantic term — so a learned edge surfaces
// across every signal that links the pair. nil means neutral (1.0 everywhere).
func Spread(c Candidate, sources []Source, p Params, learned func(src, dst string) float64) (float64, []string) {
	if learned == nil {
		learned = neutralLearned
	}
	var total, bestSem float64
	var firedBack, firedCo, firedSem bool
	for _, src := range sources {
		if src.Path == c.Path {
			continue
		}
		e, ok := c.Edges[src.Path]
		if !ok {
			continue
		}
		lw := learned(src.Path, c.Path)
		var s float64
		if e.Backlink {
			s += p.WBacklink
			firedBack = true
		}
		if e.CoAccessCount > 0 {
			co := math.Log1p(float64(e.CoAccessCount)) / math.Log1p(p.CoSaturation)
			if co > 1.0 {
				co = 1.0
			}
			s += p.WCoAccess * co
			firedCo = true
		}
		total += src.Weight * s * lw
		if e.Similarity >= p.SemThreshold {
			if cand := src.Weight * p.WSemantic * e.Similarity * lw; cand > bestSem {
				bestSem = cand
				firedSem = true
			}
		}
	}
	total += bestSem
	var reasons []string
	if firedBack {
		reasons = append(reasons, "backlink")
	}
	if firedSem {
		reasons = append(reasons, "semantic")
	}
	if firedCo {
		reasons = append(reasons, "co-access")
	}
	return total, reasons
}

// AttentionWeights pins the focus at FocusWeight and exp-decays earlier-session
// sources by recency, then normalizes the earlier sources among themselves to
// EarlierBudget so total spread is session-size-independent (a big session
// can't inflate spreading). lastTouch maps source path → its in-session ts.
func AttentionWeights(sources []Source, nowUnix int64, lastTouch map[string]int64, focusPath string, p Params) []Source {
	out := make([]Source, 0, len(sources))
	var earlier []Source
	var rawSum float64
	for _, s := range sources {
		if s.Path == focusPath {
			out = append(out, Source{Path: s.Path, Weight: p.FocusWeight})
			continue
		}
		w := math.Exp(-float64(nowUnix-lastTouch[s.Path]) / p.AttnTauSecs)
		if w < p.AttnFloor {
			continue
		}
		earlier = append(earlier, Source{Path: s.Path, Weight: w})
		rawSum += w
	}
	if rawSum > 0 {
		for _, e := range earlier {
			out = append(out, Source{Path: e.Path, Weight: e.Weight * p.EarlierBudget / rawSum})
		}
	}
	return out
}

// neutralLearned is the no-op reinforcement getter (every edge weighted 1.0),
// used when learning is disabled or unavailable (mobile/CLI/MCP, tests).
func neutralLearned(string, string) float64 { return 1.0 }

// Rank computes activation for every candidate and returns them sorted desc.
// sources[0] must be the focus (weight already set via AttentionWeights). The
// focus is excluded from results. The caller slices to TopN. For reproducible
// noise the caller must pass candidates in a stable (path-sorted) order.
// learned is the reinforcement multiplier (see Spread); nil = neutral.
func Rank(focus Candidate, sources []Source, candidates []Candidate, nowUnix int64, p Params, n Noiser, learned func(src, dst string) float64) []Scored {
	out := make([]Scored, 0, len(candidates))
	for _, c := range candidates {
		if c.Path == focus.Path {
			continue
		}
		acc := SeedMtime(c.Accesses, c.ModTime, p)
		base := BaseLevel(acc, nowUnix, p)
		raw, reasons := Spread(c, sources, p, learned)
		spread := p.SpreadScale * raw
		act := base + spread + n.Noise(c.Path)

		sc := Scored{Candidate: c, Activation: act, Base: base, Spread: spread}
		if len(c.Accesses) > 0 {
			sc.Reasons = append(sc.Reasons, "base")
		}
		sc.Reasons = append(sc.Reasons, reasons...)
		// Resurfaced: a forgotten note (cold base) pulled up by association.
		// Cosmetic label for the "why it surfaced" UI; does not affect ranking.
		if base <= p.NeverBase+4.0 && spread >= 1.0 {
			sc.Reasons = append(sc.Reasons, "resurfaced")
		}
		out = append(out, sc)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Activation > out[j].Activation })
	return out
}

// RankDefault is the production entry point: DefaultParams plus a live-seeded
// production Noiser. Callers wanting determinism use Rank with NewNoiser(0,…).
func RankDefault(focus Candidate, sources []Source, candidates []Candidate, now time.Time) []Scored {
	p := DefaultParams()
	return Rank(focus, sources, candidates, now.Unix(), p, NewNoiser(p.NoiseScale, now.UnixNano(), p.Gaussian), nil)
}

// OnThisDay returns candidates whose ModTime falls within ±p.OnThisDayWindowDays
// of `now`'s month/day in any strictly PRIOR calendar year, most recent first.
// The forgetting curve inverted: surface what the brain would have dropped but
// the calendar makes relevant again. Caller excludes the current note.
func OnThisDay(candidates []Candidate, now time.Time, p Params) []Candidate {
	out := make([]Candidate, 0)
	for _, c := range candidates {
		if c.ModTime.IsZero() || c.ModTime.Year() >= now.Year() {
			continue
		}
		if !withinAnniversaryWindow(c.ModTime, now, p.OnThisDayWindowDays) {
			continue
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })
	return out
}

// withinAnniversaryWindow reports whether mod falls within ±window days of now's
// month/day, ignoring year. Shifts mod's anchor across ±1 year to handle the
// Dec/Jan boundary wrap.
func withinAnniversaryWindow(mod, now time.Time, window int) bool {
	loc := now.Location()
	anchor := time.Date(now.Year(), mod.Month(), mod.Day(), 0, 0, 0, 0, loc)
	target := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	for _, shift := range []int{-1, 0, 1} {
		shifted := anchor.AddDate(shift, 0, 0)
		days := int(math.Round(target.Sub(shifted).Hours() / 24.0))
		if days < 0 {
			days = -days
		}
		if days <= window {
			return true
		}
	}
	return false
}
