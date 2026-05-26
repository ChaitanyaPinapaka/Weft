// Package surface ranks candidate notes for the brain panel.
//
// Pure scoring logic only. No I/O, no DB, no HTTP. The index package is
// responsible for producing Candidates; this package only ranks them.
package surface

import (
	"math"
	"sort"
	"time"
)

// Candidate is a note considered for surfacing alongside the current note.
type Candidate struct {
	Path         string
	Title        string
	ModTime      time.Time
	LastAccessed time.Time // zero if never accessed
	HasBacklink  bool      // true if Candidate.Path links to Current, OR vice versa
	CoAccessed   bool      // true if recently co-opened with the current note
	Similarity   float64   // 0..1 cosine, 0 if unknown
}

// Scored is a Candidate annotated with its surfacing score and the reasons
// that contributed to it.
type Scored struct {
	Candidate
	Score   float64
	Reasons []string
}

// Scoring weights per v0.2 spec. Sum of the three primary weights is 1.0;
// co-access is an additive bonus on top.
const (
	backlinkWeight  = 0.4
	semanticWeight  = 0.4
	recencyWeight   = 0.2
	coAccessBonus   = 0.1
	recencyHalfLife = 30.0 // days; exponential decay constant
	hoursPerDay     = 24.0

	// onThisDayWindowDays is the ±N day tolerance for OnThisDay matching.
	// Wide enough to absorb travel/holiday note clusters that drift a few days.
	onThisDayWindowDays = 3
)

// Rank scores candidates against current at time `now`, sorted desc by Score.
// Excludes current.Path. Drops candidates with Score <= 0.
func Rank(current Candidate, candidates []Candidate, now time.Time) []Scored {
	out := make([]Scored, 0, len(candidates))
	for _, c := range candidates {
		if c.Path == current.Path {
			continue
		}
		s := score(c, now)
		if s.Score <= 0 {
			continue
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Score > out[j].Score
	})
	return out
}

func score(c Candidate, now time.Time) Scored {
	s := Scored{Candidate: c}

	if c.HasBacklink {
		s.Score += backlinkWeight
		s.Reasons = append(s.Reasons, "backlink")
	}

	if c.Similarity > 0 {
		s.Score += semanticWeight * c.Similarity
		s.Reasons = append(s.Reasons, "semantic")
	}

	if r := recency(c, now); r > 0 {
		s.Score += r
		s.Reasons = append(s.Reasons, "recent")
	}

	if c.CoAccessed {
		s.Score += coAccessBonus
		s.Reasons = append(s.Reasons, "co-accessed")
	}

	return s
}

// recency decays exponentially from the most recent of ModTime / LastAccessed.
// A zero LastAccessed is ignored; ModTime alone is used in that case.
func recency(c Candidate, now time.Time) float64 {
	ref := c.ModTime
	if !c.LastAccessed.IsZero() && c.LastAccessed.After(ref) {
		ref = c.LastAccessed
	}
	if ref.IsZero() {
		return 0
	}
	days := now.Sub(ref).Hours() / hoursPerDay
	if days < 0 {
		days = 0
	}
	return recencyWeight * math.Exp(-days/recencyHalfLife)
}

// OnThisDay returns candidates whose ModTime falls within ±3 days of `now`'s
// month/day in any PRIOR year (different calendar year). Sorted by most recent
// modification first. Caller is responsible for excluding `current.Path`.
func OnThisDay(candidates []Candidate, now time.Time) []Candidate {
	out := make([]Candidate, 0)
	for _, c := range candidates {
		if c.ModTime.IsZero() {
			continue
		}
		if c.ModTime.Year() >= now.Year() {
			// Must be a strictly prior calendar year.
			continue
		}
		if !withinAnniversaryWindow(c.ModTime, now, onThisDayWindowDays) {
			continue
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].ModTime.After(out[j].ModTime)
	})
	return out
}

// withinAnniversaryWindow reports whether mod falls within ±window days of
// now's month/day, ignoring year. Implemented by shifting mod's year to now's
// year (and the year before, to handle Dec/Jan wrap) and comparing absolute
// day deltas.
func withinAnniversaryWindow(mod, now time.Time, window int) bool {
	loc := now.Location()
	anchor := time.Date(now.Year(), mod.Month(), mod.Day(), 0, 0, 0, 0, loc)
	target := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	// Check current-year anchor and ±1 year anchors to handle wrap near year boundary.
	for _, shift := range []int{-1, 0, 1} {
		shifted := anchor.AddDate(shift, 0, 0)
		days := int(math.Round(target.Sub(shifted).Hours() / hoursPerDay))
		if days < 0 {
			days = -days
		}
		if days <= window {
			return true
		}
	}
	return false
}
