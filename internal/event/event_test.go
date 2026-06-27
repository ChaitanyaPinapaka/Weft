package event

import (
	"encoding/json"
	"testing"
	"time"

	"weft/internal/vault"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	v, err := vault.New(t.TempDir())
	if err != nil {
		t.Fatalf("vault.New: %v", err)
	}
	return NewStore(v, "test-device")
}

func TestPutBlobIdempotent(t *testing.T) {
	s := newStore(t)
	d1, err := s.PutBlob([]byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	d2, err := s.PutBlob([]byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 {
		t.Fatalf("same content must content-address identically: %s vs %s", d1, d2)
	}
	// Content address is the SHA-256 of the bytes.
	if d1 != Digest([]byte("hello")) {
		t.Fatalf("digest mismatch: %s", d1)
	}
	if got, err := s.GetBlob(d1); err != nil || string(got) != "hello" {
		t.Fatalf("GetBlob round-trip failed: %q %v", got, err)
	}
	// Different content → different address.
	if d3, _ := s.PutBlob([]byte("world")); d3 == d1 {
		t.Fatal("distinct content must not collide")
	}
}

func TestAppendDurableAndReadable(t *testing.T) {
	s := newStore(t)
	payload := []byte(`{"title":"Focus","body":"a note"}`)
	env, err := s.Append(Envelope{Source: "note", Kind: "note.saved", DedupKey: "focus.html@v1"}, payload)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	// Append assigns the durable metadata.
	if env.EID == "" || env.IngestedAt == 0 {
		t.Fatalf("Append must assign eid + ingested_at: %+v", env)
	}
	if env.OccurredAt == 0 {
		t.Fatal("occurred_at must default to ingested_at")
	}
	if env.Device != "test-device" {
		t.Fatalf("device must default to the store device, got %q", env.Device)
	}
	if env.PayloadRef != Digest(payload) {
		t.Fatalf("payload_ref must be the payload digest, got %q", env.PayloadRef)
	}
	// The event file is durable and parses back to the same envelope.
	raw, err := s.v.Read(eventPath(env.Source, env.EID))
	if err != nil {
		t.Fatalf("event file not durable after Append returned: %v", err)
	}
	var got Envelope
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("event line does not parse: %v (%s)", err, raw)
	}
	if got != env {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, env)
	}
	// The payload is retrievable via its ref.
	if blob, err := s.GetBlob(env.PayloadRef); err != nil || string(blob) != string(payload) {
		t.Fatalf("payload not retrievable via ref: %v", err)
	}
}

func TestEIDTimeSortable(t *testing.T) {
	s := newStore(t)
	base := time.Date(2026, 6, 20, 21, 35, 0, 0, time.UTC)
	s.now = func() time.Time { return base }
	a, err := s.Append(Envelope{Source: "note", Kind: "x"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return base.Add(time.Second) }
	b, err := s.Append(Envelope{Source: "note", Kind: "x"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !(a.EID < b.EID) {
		t.Fatalf("later event must sort after earlier: %s !< %s", a.EID, b.EID)
	}
}

func TestAppendRequiresSourceAndKind(t *testing.T) {
	s := newStore(t)
	if _, err := s.Append(Envelope{Kind: "x"}, nil); err == nil {
		t.Fatal("missing source must error")
	}
	if _, err := s.Append(Envelope{Source: "note"}, nil); err == nil {
		t.Fatal("missing kind must error")
	}
}
