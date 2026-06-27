// Package event is the canonical capture substrate for Weft's context graph:
// an append-only log of immutable events plus a content-addressed blob store.
//
// It is the "completeness in the store" half of the design — nothing is ever
// rewritten or deleted here. Every event is written as its OWN immutable file
// keyed by a time-sortable id, and every payload is content-addressed by its
// SHA-256. Because each canonical file is a distinct, never-reused key, the
// file-level sync engine converges them by pure union (a new file on each
// device, never a divergent write to a shared key) — so concurrent multi-device
// capture never produces a conflict-copy and never loses an event. The derived
// graph (the SQLite index) is rebuilt from this log by replay and is never the
// source of truth.
//
// Writes go through the vault's crash-safe atomic write (temp → fsync → rename →
// dir-fsync), which returns only once the bytes are durable. Append therefore
// returns only after the event survives a crash — "ack after durable": a caller
// holding the returned EID is guaranteed the event is on disk.
package event

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"weft/internal/vault"
)

// Envelope is the one canonical shape every captured event takes, regardless of
// source. The large/raw payload is stored separately as a content-addressed blob
// and referenced by PayloadRef, keeping the log line small and the same payload
// de-duplicated across re-deliveries.
type Envelope struct {
	EID        string `json:"eid"`                   // time-sortable unique id (assigned by Append)
	Source     string `json:"source"`                // adapter: "note", "gmail", "whoop", ...
	Kind       string `json:"kind"`                  // event type within the source: "note.saved", "order.shipped"
	OccurredAt int64  `json:"occurred_at"`           // unix seconds — when the thing happened (caller-supplied)
	IngestedAt int64  `json:"ingested_at"`           // unix seconds — when Weft durably recorded it (assigned)
	DedupKey   string `json:"dedup_key,omitempty"`   // source-defined idempotency key
	PayloadRef string `json:"payload_ref,omitempty"` // SHA-256 of the payload blob, if any
	Device     string `json:"device,omitempty"`      // originating device id
	Schema     string `json:"schema,omitempty"`      // payload schema/version hint
}

// Store appends events and stores blobs under a vault, reusing the vault's
// atomic, crash-safe write. device is stamped on every event; now is injectable
// for tests.
type Store struct {
	v      *vault.Vault
	device string
	now    func() time.Time
}

// NewStore returns a Store rooted at v, stamping device on captured events.
func NewStore(v *vault.Vault, device string) *Store {
	return &Store{v: v, device: device, now: time.Now}
}

// PutBlob content-addresses payload at blobs/<ab>/<sha256>.bin and returns the
// hex digest. Idempotent by construction: the address IS the content hash, so
// re-storing identical bytes is a no-op (no duplication, no rewrite) — the first
// leg of never-miss idempotency.
func (s *Store) PutBlob(content []byte) (string, error) {
	digest := Digest(content)
	rel := blobPath(digest)
	if s.v.Exists(rel) {
		return digest, nil
	}
	if err := s.v.Write(rel, content); err != nil {
		return "", err
	}
	return digest, nil
}

// GetBlob returns the bytes previously stored under digest.
func (s *Store) GetBlob(digest string) ([]byte, error) {
	return s.v.Read(blobPath(digest))
}

// Append durably records one event. It content-addresses the optional payload,
// assigns a time-sortable EID and ingested_at, defaults occurred_at to now and
// device to the store's device, then writes the envelope as its own immutable
// NDJSON file via the vault's crash-safe atomic write. It returns the completed
// envelope only AFTER the bytes are durable on disk.
func (s *Store) Append(env Envelope, payload []byte) (Envelope, error) {
	if strings.TrimSpace(env.Source) == "" || strings.TrimSpace(env.Kind) == "" {
		return Envelope{}, fmt.Errorf("event: source and kind are required")
	}
	now := s.now()
	if payload != nil {
		digest, err := s.PutBlob(payload)
		if err != nil {
			return Envelope{}, err
		}
		env.PayloadRef = digest
	}
	eid, err := newEID(now)
	if err != nil {
		return Envelope{}, err
	}
	env.EID = eid
	env.IngestedAt = now.Unix()
	if env.OccurredAt == 0 {
		env.OccurredAt = env.IngestedAt
	}
	if env.Device == "" {
		env.Device = s.device
	}
	line, err := json.Marshal(env)
	if err != nil {
		return Envelope{}, err
	}
	line = append(line, '\n')
	if err := s.v.Write(eventPath(env.Source, eid), line); err != nil {
		return Envelope{}, err
	}
	return env, nil
}

// Digest returns the lowercase hex SHA-256 of content — the content address.
func Digest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// newEID returns a 32-hex-char, lexicographically time-sortable unique id:
// 48-bit millisecond timestamp (12 hex) followed by 80 bits of crypto-random
// (20 hex). Sortable by time like a ULID, collision-safe via the random tail,
// dependency-free.
func newEID(t time.Time) (string, error) {
	var r [10]byte
	if _, err := rand.Read(r[:]); err != nil {
		return "", err
	}
	ms := t.UnixMilli() & 0xffffffffffff // low 48 bits
	return fmt.Sprintf("%012x%s", ms, hex.EncodeToString(r[:])), nil
}

func eventPath(source, eid string) string {
	return "events/" + sanitizeSource(source) + "/" + eid + ".ndjson"
}

// blobPath fans blobs out by the first two hex chars so no single directory
// grows unbounded as capture scales.
func blobPath(digest string) string {
	return "blobs/" + digest[:2] + "/" + digest + ".bin"
}

// sanitizeSource keeps a source usable as a path segment: lowercase, only
// [a-z0-9-_], everything else collapsed to '-'.
func sanitizeSource(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}
