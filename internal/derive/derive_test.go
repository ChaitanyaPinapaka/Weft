package derive

import (
	"encoding/json"
	"testing"

	"weft/internal/event"
	"weft/internal/index"
	"weft/internal/vault"
)

func setup(t *testing.T) (*event.Store, *index.Index) {
	t.Helper()
	v, err := vault.New(t.TempDir())
	if err != nil {
		t.Fatalf("vault.New: %v", err)
	}
	ix, err := index.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { ix.Close() })
	return event.NewStore(v, "test-device"), ix
}

func TestCatchupFoldsCaptureIntoGraph(t *testing.T) {
	st, ix := setup(t)
	payload, _ := json.Marshal(map[string]string{"text": "ran 5k", "path": "daily/2026-06-27.html"})
	ev, err := st.Append(event.Envelope{Source: "capture", Kind: "capture.created"}, payload)
	if err != nil {
		t.Fatal(err)
	}

	n, err := Catchup(st, ix)
	if err != nil || n != 1 {
		t.Fatalf("Catchup: n=%d err=%v", n, err)
	}

	// An Event node carrying the captured text.
	events, _ := ix.NodesByKind("Event")
	if len(events) != 1 || events[0].Props["text"] != "ran 5k" {
		t.Fatalf("expected one Event node with text: %+v", events)
	}
	// A Document node for the target daily, linked from the event.
	if docs, _ := ix.NodesByKind("Document"); len(docs) != 1 {
		t.Fatalf("expected one Document node, got %d", len(docs))
	}
	edges, _ := ix.EdgesFrom("event:" + ev.EID)
	if len(edges) != 1 || edges[0].Rel != "references" || edges[0].Dst != "doc:daily/2026-06-27.html" {
		t.Fatalf("expected references edge to the daily doc: %+v", edges)
	}
	// Cursor advanced.
	if c, _ := ix.IngestCursor(); c != ev.EID {
		t.Fatalf("cursor not advanced to %s, got %s", ev.EID, c)
	}
}

func TestCatchupIsIdempotentAndIncremental(t *testing.T) {
	st, ix := setup(t)
	p1, _ := json.Marshal(map[string]string{"text": "first", "path": "daily/d1.html"})
	if _, err := st.Append(event.Envelope{Source: "capture", Kind: "capture.created"}, p1); err != nil {
		t.Fatal(err)
	}
	if n, _ := Catchup(st, ix); n != 1 {
		t.Fatalf("first catchup should fold 1, got %d", n)
	}
	// Re-running with no new events folds nothing and doesn't duplicate.
	if n, _ := Catchup(st, ix); n != 0 {
		t.Fatalf("re-catchup should fold 0, got %d", n)
	}
	if e, _ := ix.NodesByKind("Event"); len(e) != 1 {
		t.Fatalf("idempotent: expected 1 Event node, got %d", len(e))
	}

	// A new event after the cursor is folded incrementally.
	p2, _ := json.Marshal(map[string]string{"text": "second", "path": "daily/d2.html"})
	if _, err := st.Append(event.Envelope{Source: "capture", Kind: "capture.created"}, p2); err != nil {
		t.Fatal(err)
	}
	if n, _ := Catchup(st, ix); n != 1 {
		t.Fatalf("incremental catchup should fold the 1 new event, got %d", n)
	}
	if e, _ := ix.NodesByKind("Event"); len(e) != 2 {
		t.Fatalf("expected 2 Event nodes after second capture, got %d", len(e))
	}
}

func TestCatchupSkipsUnknownKinds(t *testing.T) {
	st, ix := setup(t)
	if _, err := st.Append(event.Envelope{Source: "whoop", Kind: "sleep.recorded"}, nil); err != nil {
		t.Fatal(err)
	}
	n, err := Catchup(st, ix)
	if err != nil {
		t.Fatalf("unknown kinds must not error: %v", err)
	}
	if n != 1 { // counted + cursor advanced, but no graph nodes projected yet
		t.Fatalf("expected to advance past 1 unknown event, got %d", n)
	}
	if e, _ := ix.NodesByKind("Event"); len(e) != 0 {
		t.Fatalf("unknown kind must not project a node yet, got %d", len(e))
	}
}
