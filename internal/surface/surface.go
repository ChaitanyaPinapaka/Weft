// Package surface ranks candidate notes for the brain panel.
//
// Pure scoring logic only. No I/O, no DB, no HTTP. The index package is
// responsible for producing Candidates; this package only ranks them.
package surface

import (
	"fmt"
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
	Similarity   float64   // 0..1 cosine, 0 if unknown
}

// Scored is a Candidate annotated with its surfacing score and the reasons
// that contributed to it.
type Scored struct {
	Candidate
	Score   float64
	Reasons []string
}

// Scoring constants. Tuned for the v0.2 brain panel; bump as the corpus grows.
const (
	backlinkBonus      = 1.0
	onThisDayWeight    = 0.8
	recencyWeight      = 0.5
	recencyHalfLife    = 30.0 // days; exponential decay constant
	hoursPerDay        = 24.0
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
		s.Score += backlinkBonus
		s.Reasons = append(s.Reasons, "backlink")
	}

	if otd, years, ok := onThisDay(c.ModTime, now); ok {
		s.Score += otd
		s.Reasons = append(s.Reasons, fmt.Sprintf("on-this-day:%dy", years))
	}

	if r := recency(c, now); r > 0 {
		s.Score += r
		s.Reasons = append(s.Reasons, "recent")
	}

	if c.Similarity > 0 {
		s.Score += c.Similarity
		s.Reasons = append(s.Reasons, fmt.Sprintf("similar:%.2f", c.Similarity))
	}

	return s
}

// onThisDay returns the score contribution and the integer years-ago if the
// candidate's ModTime falls on the same month+day as `now` in a prior year.
func onThisDay(mod, now time.Time) (float64, int, bool) {
	if mod.IsZero() {
		return 0, 0, false
	}
	if mod.Month() != now.Month() || mod.Day() != now.Day() {
		return 0, 0, false
	}
	if mod.Year() == now.Year() {
		return 0, 0, false
	}
	years := now.Year() - mod.Year()
	if years < 1 {
		years = 1
	}
	return onThisDayWeight / float64(years), years, true
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
