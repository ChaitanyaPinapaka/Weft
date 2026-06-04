package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"weft/internal/index"
	"weft/internal/surface"
	"weft/internal/vault"
)

// ambient.go is the "Weft wants to surface something" push channel. Surfacing
// is otherwise pure pull (GET /api/surface/{path} is computed on demand); this
// adds the only proactive path. A background ticker periodically asks the same
// engine "given the session so far, what's worth resurfacing right now?" and
// pushes a single suggestion over Server-Sent Events to any connected surface
// (the macOS app). It does NO work when nobody is listening, so the daemon's
// idle cost is unchanged.

// surfaceEvent is one SSE message. type="hello" on connect; type="surface" for
// a suggestion. JSON tags are lowercase (unlike the pull endpoint's PascalCase
// structs) because this is a fresh, native-client-facing contract.
type surfaceEvent struct {
	Type       string  `json:"type"`             // "hello" | "surface"
	Path       string  `json:"path,omitempty"`   // vault-relative .html path
	Title      string  `json:"title,omitempty"`  // note name (sans extension)
	Reason     string  `json:"reason,omitempty"` // resurfaced|on-this-day|semantic|backlink|co-access
	Detail     string  `json:"detail,omitempty"` // human hint, e.g. "3 years ago" or "act 2.4"
	Activation float64 `json:"activation,omitempty"`
}

// ambientHub fans surface events out to every connected SSE subscriber. Safe
// for concurrent use: the ticker broadcasts while handlers subscribe/leave.
type ambientHub struct {
	mu   sync.Mutex
	subs map[chan surfaceEvent]struct{}
}

func newAmbientHub() *ambientHub {
	return &ambientHub{subs: make(map[chan surfaceEvent]struct{})}
}

func (h *ambientHub) subscribe() chan surfaceEvent {
	ch := make(chan surfaceEvent, 8)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *ambientHub) unsubscribe(ch chan surfaceEvent) {
	h.mu.Lock()
	if _, ok := h.subs[ch]; ok {
		delete(h.subs, ch)
		close(ch)
	}
	h.mu.Unlock()
}

func (h *ambientHub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// broadcast delivers ev to all subscribers without blocking: a subscriber whose
// buffer is full (a stalled client) drops the event rather than wedging the
// ticker. unsubscribe is the only closer of a channel and runs under the same
// lock, so we never send on a closed channel.
func (h *ambientHub) broadcast(ev surfaceEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// surfaceStreamHandler serves GET /api/surface/stream as text/event-stream.
// It registers a subscriber, sends a "hello", then forwards pushed suggestions
// until the client disconnects, with a periodic keepalive comment so idle
// connections stay open.
func surfaceStreamHandler(h *ambientHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		ch := h.subscribe()
		defer h.unsubscribe(ch)

		writeSSE(w, surfaceEvent{Type: "hello"})
		fl.Flush()

		keepalive := time.NewTicker(25 * time.Second)
		defer keepalive.Stop()
		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				writeSSE(w, ev)
				fl.Flush()
			case <-keepalive.C:
				fmt.Fprint(w, ": keepalive\n\n")
				fl.Flush()
			}
		}
	}
}

func writeSSE(w io.Writer, ev surfaceEvent) {
	b, _ := json.Marshal(ev)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, b)
}

// startAmbientSurfacer runs the proactive-surfacing ticker. It seeds the engine
// from the most recent access (the note the user is effectively "on"), picks at
// most one suggestion per tick, suppresses a path it pushed within the last day,
// and only computes when at least one client is connected. Interval defaults to
// 30m — a calm ambient nudge, not a feed — overridable via WEFT_SURFACE_INTERVAL
// (a Go duration, e.g. "1h" or "20s" for demos).
func startAmbientSurfacer(h *ambientHub, v *vault.Vault, ix *index.Index, ps *paramStore) {
	interval := 30 * time.Minute
	if s := os.Getenv("WEFT_SURFACE_INTERVAL"); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			interval = d
		}
	}
	fmt.Printf("Surface  ambient push every %s\n", interval)

	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		recent := make(map[string]int64) // path -> last-pushed unix
		for range t.C {
			if h.count() == 0 {
				continue // nobody listening — don't touch the engine
			}
			now := time.Now()
			ev, ok := pickSurfaceEvent(v, ix, ps.get(), now, recent)
			if !ok {
				continue
			}
			recent[ev.Path] = now.Unix()
			h.broadcast(ev)
		}
	}()
}

// pickSurfaceEvent seeds the focus from the latest access and runs the engine,
// then delegates the choice to selectSuggestion. Returns ok=false when there is
// no session yet or nothing worth surfacing.
func pickSurfaceEvent(v *vault.Vault, ix *index.Index, p surface.Params, now time.Time, recent map[string]int64) (surfaceEvent, bool) {
	since := now.Unix() - int64(p.SessionLookback.Seconds())
	ra, err := ix.RecentAccesses(since)
	if err != nil || len(ra) == 0 {
		return surfaceEvent{}, false
	}
	focus := ra[0].Path // DESC by ts — the most recent open
	res, err := computeSurface(v, ix, p, focus, now)
	if err != nil {
		return surfaceEvent{}, false
	}
	return selectSuggestion(res, now, recent)
}

// repeatWindow is how long a surfaced path stays suppressed from being pushed
// again — one day, so the same note doesn't nag across a work session.
const repeatWindow = 24 * 60 * 60

// selectSuggestion is the pure choice: from a computed surface, return the one
// note most worth pushing. Priority: a resurfaced (forgotten-yet-relevant) note
// first — that's the magic moment — then an on-this-day anniversary, then the
// strongest live association. Skips the focus itself and anything pushed within
// repeatWindow. Pure (no I/O) so it is unit-testable.
func selectSuggestion(res surfaceResult, now time.Time, recent map[string]int64) (surfaceEvent, bool) {
	fresh := func(path string) bool {
		if path == "" || path == res.Current {
			return false
		}
		last, seen := recent[path]
		return !seen || now.Unix()-last > repeatWindow
	}

	// 1) Resurfaced: a cold-base note pulled up by association.
	for _, s := range res.Scored {
		if s.Spread <= 0 || !fresh(s.Path) {
			continue
		}
		if hasReason(s.Reasons, "resurfaced") {
			return surfaceEvent{
				Type: "surface", Path: s.Path, Title: s.Title,
				Reason: "resurfaced", Detail: fmt.Sprintf("act %.1f", s.Activation),
				Activation: s.Activation,
			}, true
		}
	}

	// 2) On this day: a calendar anniversary in a prior year.
	for _, c := range res.OnThisDay {
		if !fresh(c.Path) {
			continue
		}
		return surfaceEvent{
			Type: "surface", Path: c.Path, Title: c.Title,
			Reason: "on-this-day", Detail: yearsAgo(now.Year() - c.ModTime.Year()),
		}, true
	}

	// 3) Strongest live association from the current session.
	for _, s := range res.Scored {
		if s.Spread <= 0 || !fresh(s.Path) {
			continue
		}
		return surfaceEvent{
			Type: "surface", Path: s.Path, Title: s.Title,
			Reason: primaryReason(s.Reasons), Detail: fmt.Sprintf("act %.1f", s.Activation),
			Activation: s.Activation,
		}, true
	}

	return surfaceEvent{}, false
}

func hasReason(reasons []string, want string) bool {
	for _, r := range reasons {
		if r == want {
			return true
		}
	}
	return false
}

// primaryReason picks the most meaningful association tag for the chip, ignoring
// the always-present "base". Order reflects salience: a semantic neighbor reads
// as more interesting than a co-access.
func primaryReason(reasons []string) string {
	for _, want := range []string{"semantic", "backlink", "co-access"} {
		if hasReason(reasons, want) {
			return want
		}
	}
	return "related"
}

func yearsAgo(n int) string {
	if n <= 1 {
		return "1 year ago"
	}
	return fmt.Sprintf("%d years ago", n)
}
