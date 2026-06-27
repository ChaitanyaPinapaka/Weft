package server

import (
	"bufio"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"weft/internal/surface"
)

// fixed clock so yearsAgo / repeat-window math is deterministic.
var ambientNow = time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)

func scored(path string, spread float64, reasons ...string) surface.Scored {
	return surface.Scored{
		Candidate: surface.Candidate{Path: path, Title: strings.TrimSuffix(path, ".html")},
		Spread:    spread,
		Reasons:   reasons,
	}
}

func TestSelectSuggestionResurfacedWins(t *testing.T) {
	res := surfaceResult{
		Current: "focus.html",
		Scored: []surface.Scored{
			scored("hot.html", 3.0, "base", "co-access"),         // strong but not resurfaced
			scored("recall.html", 1.2, "semantic", "resurfaced"), // the magic one
		},
	}
	ev, ok := selectSuggestion(res, ambientNow, nil)
	if !ok {
		t.Fatal("want a suggestion")
	}
	if ev.Path != "recall.html" || ev.Reason != "resurfaced" {
		t.Fatalf("resurfaced must win over a higher-spread non-resurfaced item, got %+v", ev)
	}
}

func TestSelectSuggestionOnThisDayBeatsPlainAssociation(t *testing.T) {
	res := surfaceResult{
		Current: "focus.html",
		Scored:  []surface.Scored{scored("link.html", 2.0, "base", "backlink")},
		OnThisDay: []surface.Candidate{
			{Path: "trip.html", Title: "trip", ModTime: ambientNow.AddDate(-3, 0, 0)},
		},
	}
	ev, ok := selectSuggestion(res, ambientNow, nil)
	if !ok || ev.Path != "trip.html" || ev.Reason != "on-this-day" {
		t.Fatalf("on-this-day should outrank a plain association, got %+v ok=%v", ev, ok)
	}
	if ev.Detail != "3 years ago" {
		t.Fatalf("years label: want %q, got %q", "3 years ago", ev.Detail)
	}
}

func TestSelectSuggestionAssociationFallback(t *testing.T) {
	res := surfaceResult{
		Current: "focus.html",
		Scored: []surface.Scored{
			scored("cold.html", 0, "base"), // Spread<=0 → skipped
			scored("link.html", 0.8, "base", "backlink"),
		},
	}
	ev, ok := selectSuggestion(res, ambientNow, nil)
	if !ok || ev.Path != "link.html" || ev.Reason != "backlink" {
		t.Fatalf("want the backlink association, got %+v ok=%v", ev, ok)
	}
}

func TestSelectSuggestionSkipsFocusAndRecent(t *testing.T) {
	res := surfaceResult{
		Current: "focus.html",
		Scored: []surface.Scored{
			scored("focus.html", 5.0, "semantic", "resurfaced"), // is the focus → skip
			scored("seen.html", 4.0, "semantic", "resurfaced"),  // pushed 1h ago → skip
			scored("fresh.html", 1.0, "semantic", "resurfaced"), // eligible
		},
	}
	recent := map[string]int64{"seen.html": ambientNow.Unix() - 3600}
	ev, ok := selectSuggestion(res, ambientNow, recent)
	if !ok || ev.Path != "fresh.html" {
		t.Fatalf("must skip focus + recently-pushed, got %+v ok=%v", ev, ok)
	}

	// Past the repeat window, the suppressed note is eligible again.
	recent["seen.html"] = ambientNow.Unix() - (repeatWindow + 1)
	ev, _ = selectSuggestion(res, ambientNow, recent)
	if ev.Path != "seen.html" {
		t.Fatalf("after the repeat window 'seen' should resurface first, got %+v", ev)
	}
}

func TestSelectSuggestionEmpty(t *testing.T) {
	if _, ok := selectSuggestion(surfaceResult{Current: "focus.html"}, ambientNow, nil); ok {
		t.Fatal("no candidates → no suggestion")
	}
}

// The event-driven push gate is stricter than the periodic nudge: a routine
// association is pushed by selectSuggestion but withheld by selectHighValue —
// the anti-fatigue contract (routine associations stay in the pull panel).
func TestSelectHighValueGatesOutRoutineAssociation(t *testing.T) {
	res := surfaceResult{
		Current: "focus.html",
		Scored:  []surface.Scored{scored("link.html", 2.0, "base", "backlink")},
	}
	if ev, ok := selectSuggestion(res, ambientNow, nil); !ok || ev.Path != "link.html" {
		t.Fatalf("periodic nudge should still push the association, got %+v ok=%v", ev, ok)
	}
	if _, ok := selectHighValue(res, ambientNow, nil); ok {
		t.Fatal("event-driven gate must NOT push a routine association")
	}
}

// Only the unambiguous wins clear the event-driven gate.
func TestSelectHighValuePushesResurfacedAndOnThisDay(t *testing.T) {
	resurfaced := surfaceResult{
		Current: "focus.html",
		Scored:  []surface.Scored{scored("recall.html", 1.2, "semantic", "resurfaced")},
	}
	if ev, ok := selectHighValue(resurfaced, ambientNow, nil); !ok || ev.Path != "recall.html" || ev.Reason != "resurfaced" {
		t.Fatalf("resurfaced must clear the gate, got %+v ok=%v", ev, ok)
	}
	onThisDay := surfaceResult{
		Current:   "focus.html",
		OnThisDay: []surface.Candidate{{Path: "trip.html", Title: "trip", ModTime: ambientNow.AddDate(-2, 0, 0)}},
	}
	if ev, ok := selectHighValue(onThisDay, ambientNow, nil); !ok || ev.Path != "trip.html" || ev.Reason != "on-this-day" {
		t.Fatalf("on-this-day must clear the gate, got %+v ok=%v", ev, ok)
	}
}

func TestAmbientHubBroadcast(t *testing.T) {
	h := newAmbientHub()
	ch := h.subscribe()
	if h.count() != 1 {
		t.Fatalf("count after subscribe: want 1, got %d", h.count())
	}
	h.broadcast(surfaceEvent{Type: "surface", Path: "x.html"})
	select {
	case ev := <-ch:
		if ev.Path != "x.html" {
			t.Fatalf("got %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive the broadcast")
	}

	h.unsubscribe(ch)
	if h.count() != 0 {
		t.Fatalf("count after unsubscribe: want 0, got %d", h.count())
	}
	if _, open := <-ch; open {
		t.Fatal("channel should be closed after unsubscribe")
	}
}

// A stalled subscriber (full buffer) must not block the broadcaster.
func TestAmbientHubBroadcastNonBlocking(t *testing.T) {
	h := newAmbientHub()
	_ = h.subscribe() // never drained
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			h.broadcast(surfaceEvent{Type: "surface"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("broadcast blocked on a full subscriber buffer")
	}
}

// The literal "stream" route must win over the {path...} wildcard, otherwise
// the SSE endpoint would be swallowed and served as note "stream.html".
func TestSurfaceStreamRoutePrecedence(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/surface/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Route", "stream")
	})
	mux.HandleFunc("GET /api/surface/{path...}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Route", "note:"+r.PathValue("path"))
	})

	cases := map[string]string{
		"/api/surface/stream":         "stream",
		"/api/surface/notes/foo.html": "note:notes/foo.html",
	}
	for path, want := range cases {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if got := rec.Header().Get("X-Route"); got != want {
			t.Fatalf("%s routed to %q, want %q", path, got, want)
		}
	}
}

// End-to-end: the stream handler sets the SSE content type, greets with a hello,
// and forwards a broadcast pushed after the client is connected.
func TestSurfaceStreamHandlerPushes(t *testing.T) {
	h := newAmbientHub()
	srv := httptest.NewServer(surfaceStreamHandler(h))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type: want text/event-stream, got %q", ct)
	}

	r := bufio.NewReader(resp.Body)
	if hello := readSSEEvent(t, r); !strings.Contains(hello, "hello") {
		t.Fatalf("first event should be hello, got %q", hello)
	}
	// Reading the hello proves we are subscribed (subscribe precedes the hello
	// write), so a broadcast now is guaranteed to reach this connection.
	h.broadcast(surfaceEvent{Type: "surface", Path: "recall.html", Reason: "resurfaced"})
	if got := readSSEEvent(t, r); !strings.Contains(got, "recall.html") {
		t.Fatalf("did not receive the pushed surface event, got %q", got)
	}
}

// readSSEEvent reads lines up to the blank-line terminator, skipping `:` comment
// (keepalive) frames, and returns the accumulated event text.
func readSSEEvent(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	var b strings.Builder
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("reading SSE: %v (so far: %q)", err, b.String())
		}
		if line == "\n" { // event terminator
			if b.Len() == 0 {
				continue // blank line after a comment frame; keep reading
			}
			return b.String()
		}
		if strings.HasPrefix(line, ":") {
			continue // keepalive comment
		}
		b.WriteString(line)
	}
}

func TestWriteSSEFraming(t *testing.T) {
	var buf bytes.Buffer
	writeSSE(&buf, surfaceEvent{Type: "surface", Path: "a.html", Reason: "resurfaced"})
	out := buf.String()
	if !strings.HasPrefix(out, "event: surface\ndata: {") {
		t.Fatalf("bad SSE framing: %q", out)
	}
	if !strings.HasSuffix(out, "}\n\n") {
		t.Fatalf("SSE message must end with a blank line: %q", out)
	}
	if !strings.Contains(out, `"reason":"resurfaced"`) {
		t.Fatalf("payload missing reason: %q", out)
	}
}
