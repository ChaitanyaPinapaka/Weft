// Package derive folds the event lake into the context graph. It is the
// "derived, disposable" half of the substrate: replay every event after the
// ingest cursor and project it into nodes + edges, then advance the cursor.
//
// Everything here is idempotent — node/edge ids come from stable keys (the event
// EID, the note path), so re-running, or a crash mid-run, upserts the identical
// graph. The cursor only advances after a clean pass, so nothing is skipped on
// failure. To pick up a NEW projection rule for events already past the cursor,
// reset the graph (rm the derived tables / index.db) and replay from "" — the
// graph is rebuildable from the lake, which is the source of truth.
package derive

import (
	"encoding/json"

	"weft/internal/event"
	"weft/internal/index"
)

// Catchup folds every event after the index's ingest cursor into the graph and
// advances the cursor to the last event folded. Returns the count folded.
func Catchup(store *event.Store, ix *index.Index) (int, error) {
	cursor, err := ix.IngestCursor()
	if err != nil {
		return 0, err
	}
	n := 0
	var last string
	if err := store.Replay(cursor, func(e event.Envelope) error {
		if err := fold(store, ix, e); err != nil {
			return err
		}
		last = e.EID
		n++
		return nil
	}); err != nil {
		return n, err
	}
	if last != "" {
		if err := ix.SetIngestCursor(last); err != nil {
			return n, err
		}
	}
	return n, nil
}

// fold projects a single event into the graph. Unknown kinds are intentionally
// skipped (they stay in the lake; a future fold rule + replay-from-zero picks
// them up) so derivation never blocks on an unmodeled source.
func fold(store *event.Store, ix *index.Index, e event.Envelope) error {
	switch e.Kind {
	case "capture.created":
		var p struct {
			Text string `json:"text"`
			Path string `json:"path"`
		}
		readPayload(store, e, &p)
		ev := index.Node{ID: "event:" + e.EID, Kind: "Event", Props: map[string]any{
			"text":        p.Text,
			"source":      e.Source,
			"occurred_at": e.OccurredAt,
		}}
		if err := ix.UpsertNode(ev); err != nil {
			return err
		}
		if p.Path != "" {
			doc := index.Node{ID: "doc:" + p.Path, Kind: "Document", Props: map[string]any{"path": p.Path}}
			if err := ix.UpsertNode(doc); err != nil {
				return err
			}
			if err := ix.UpsertEdge(index.Edge{Src: ev.ID, Dst: doc.ID, Rel: "references", ValidFrom: e.OccurredAt}); err != nil {
				return err
			}
		}

	case "clip.created":
		var p struct {
			URL   string `json:"url"`
			Title string `json:"title"`
			Path  string `json:"path"`
		}
		readPayload(store, e, &p)
		if p.Path != "" {
			return ix.UpsertNode(index.Node{ID: "doc:" + p.Path, Kind: "Document", Props: map[string]any{
				"path": p.Path, "url": p.URL, "title": p.Title, "source": "clip", "created_at": e.OccurredAt,
			}})
		}

	case "note.created":
		var p struct {
			Title string `json:"title"`
			Path  string `json:"path"`
		}
		readPayload(store, e, &p)
		if p.Path != "" {
			return ix.UpsertNode(index.Node{ID: "doc:" + p.Path, Kind: "Document", Props: map[string]any{
				"path": p.Path, "title": p.Title, "source": "note", "created_at": e.OccurredAt,
			}})
		}
	}
	return nil
}

// readPayload decodes an event's content-addressed JSON payload into dst,
// best-effort (a missing/garbled payload leaves dst zero-valued rather than
// failing the fold).
func readPayload(store *event.Store, e event.Envelope, dst any) {
	if e.PayloadRef == "" {
		return
	}
	if blob, err := store.GetBlob(e.PayloadRef); err == nil {
		_ = json.Unmarshal(blob, dst)
	}
}
