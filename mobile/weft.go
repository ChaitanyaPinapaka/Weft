// Package mobile is the gomobile facade: a thin, iOS-friendly wrapper over the
// Weft engine so an iPhone can be a full E2EE sync peer with no daemon. It reuses
// internal/sync (convergence + crypto), internal/core (indexing + surfacing), and
// internal/{vault,index} verbatim — the phone runs the exact logic the Mac does.
//
// gomobile binds only a narrow type surface across the Go↔Swift boundary, so
// every exported signature here uses string / []byte / int / bool / error and
// bound structs only; richer results (note lists, the surface payload) are
// returned as JSON strings in the SAME shapes the HTTP daemon emits, so the
// shared Swift Codable models in WeftKit decode them unchanged.
package mobile

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"weft/internal/core"
	"weft/internal/embed"
	"weft/internal/index"
	"weft/internal/surface"
	"weft/internal/sync"
	"weft/internal/vault"
)

// On iOS there is no usable OS keychain via go-keyring (it shells out to the
// macOS-only /usr/bin/security), so force the 0600 file secret store in the app
// sandbox. iOS Data Protection encrypts that file at rest when the device locks.
func init() { _ = os.Setenv("WEFT_SECRET_STORE", "file") }

// Session is one configured vault plus its index and sync engine — the handle
// the iOS app holds for the lifetime of a vault.
type Session struct {
	v    *vault.Vault
	ix   *index.Index
	emb  embed.Embedder // nil until a native embedder is registered (semantic off)
	cfg  sync.Config
	eng  *sync.Engine
	pair *sync.Pairing // in-flight SAS device pairing, if any
}

// Configure opens (creating if needed) the vault at vaultDir, opens its local
// index, and records the bucket config. provider is "fs" (a local directory
// passed in endpoint — for tests/self-host) or an S3-style provider ("r2",
// "gcs", "aws", "b2", ...). It does NOT enroll; call RecoverWithPhrase on first
// run, or Open once enrolled.
func Configure(vaultDir, provider, endpoint, region, bucket, accessKeyID, secret string) (*Session, error) {
	if err := os.MkdirAll(vaultDir, 0o700); err != nil {
		return nil, err
	}
	v, err := vault.New(vaultDir)
	if err != nil {
		return nil, err
	}
	ix, err := index.Open(v.Root)
	if err != nil {
		return nil, err
	}
	cfg := sync.Config{
		Provider:        provider,
		Endpoint:        endpoint,
		Region:          region,
		Bucket:          bucket,
		AccessKeyID:     accessKeyID,
		SecretAccessKey: secret,
	}
	if provider == "fs" {
		cfg.FSPath = endpoint
	}
	return &Session{v: v, ix: ix, cfg: cfg}, nil
}

// Close releases the index. Call when tearing the vault down.
func (s *Session) Close() error {
	if s.ix != nil {
		return s.ix.Close()
	}
	return nil
}

// RecoverWithPhrase enrolls this device from a 24-word BIP39 recovery phrase
// (the phrase IS the vault key). It persists the config, caches the key in the
// secret store, and leaves the vault ready to Sync.
func (s *Session) RecoverWithPhrase(phrase string) error {
	vk, err := sync.VaultKeyFromMnemonic(phrase)
	if err != nil {
		return err
	}
	eng, err := sync.JoinWithKey(s.v, s.cfg, vk)
	if err != nil {
		return err
	}
	s.eng = eng
	return nil
}

// Open re-opens an already-enrolled vault from the cached vault key (the file
// secret store) — the every-launch path after the first enrollment.
func (s *Session) Open() error {
	eng, err := sync.OpenLocal(s.v)
	if err != nil {
		return err
	}
	s.eng = eng
	return nil
}

// Enrolled reports whether this device already has sync configured for the vault.
func (s *Session) Enrolled() bool { return sync.Configured(s.v) }

// Sync runs one convergence cycle against the bucket, re-indexes any notes that
// changed, and returns the engine's Result as JSON:
// {"Pushed":N,"Applied":N,"ConflictCopies":[...],"Rejected":N,"Unverifiable":N}.
func (s *Session) Sync() (string, error) {
	if s.eng == nil {
		return "", errors.New("weft: not enrolled — call RecoverWithPhrase or Open first")
	}
	res, err := s.eng.Sync()
	if err != nil {
		return "", err
	}
	// Bring the local index up to date with whatever converged (cheap: only
	// stale notes are re-read). A failure here doesn't undo the sync.
	_ = core.IndexAll(s.v, s.ix, s.emb)
	b, err := json.Marshal(res)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ListNotes returns every note in the vault as a JSON array of vault.Note
// (PascalCase Path/Name/ModTime/Size — the same shape as GET /api/notes).
func (s *Session) ListNotes() (string, error) {
	notes, err := s.v.List()
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(notes)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ReadRaw returns the raw .html bytes of a note as a string.
func (s *Session) ReadRaw(path string) (string, error) {
	b, err := s.v.Read(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Surface computes the brain-panel payload for a focus note and returns it as
// JSON in the GET /api/surface shape: snake_case top-level keys wrapping the
// PascalCase Scored/Candidate bodies.
func (s *Session) Surface(path string) (string, error) {
	res, err := core.Surface(s.v, s.ix, surface.DefaultParams(), path, time.Now())
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(map[string]any{
		"current":     res.Current,
		"backlinks":   res.Backlinks,
		"scored":      res.Scored,
		"on_this_day": res.OnThisDay,
		"trail":       res.Trail,
	})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Capture appends free text as a bullet to today's daily note (creating it if
// needed), re-indexes it, and returns the daily note's vault path.
func (s *Session) Capture(text string) (string, error) {
	rel, err := s.v.AppendCapture(time.Now(), text)
	if err != nil {
		return "", err
	}
	_ = core.ReindexNote(s.v, s.ix, s.emb, rel)
	return rel, nil
}

// Daily ensures today's daily note exists (from the template if present),
// indexes it, and returns its vault path.
func (s *Session) Daily() (string, error) {
	rel, err := s.v.EnsureDailyFromTemplate(time.Now())
	if err != nil {
		return "", err
	}
	_ = core.ReindexNote(s.v, s.ix, s.emb, rel)
	return rel, nil
}

// SearchNotes runs an FTS5 query over the local index and returns the hits as
// a JSON array of index.Hit (PascalCase Path/Title/Snippet/Score — the same
// shape as GET /api/search, including JSON null when nothing matches).
func (s *Session) SearchNotes(query string) (string, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return "", errors.New("weft: empty query")
	}
	hits, err := s.ix.Search(q, 20)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(hits)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Trash is the human-initiated soft delete, mirroring the daemon's DELETE
// /api/note/{path}: the file moves into .trash/ (vault.Trash — bytes are never
// deleted, per the never-delete invariant) and its rows leave the index, so the
// note stops listing, searching, and surfacing. Returns {"trashed": path}.
func (s *Session) Trash(path string) (string, error) {
	// Mirror vault.Trash's traversal guard so the caller gets a clear error
	// rather than its opaque permission error.
	if strings.Contains(path, "..") {
		return "", errors.New("weft: invalid path")
	}
	if !s.v.Exists(path) {
		return "", errors.New("weft: note not found: " + path)
	}
	if err := s.v.Trash(path); err != nil {
		return "", err
	}
	// The vault is authoritative: the file is already in .trash, so a failed
	// index cleanup doesn't fail the call — indexAll's orphan sweep prunes the
	// rows on the next reindex (launch or post-sync).
	_ = s.ix.Remove(path)
	b, err := json.Marshal(map[string]string{"trashed": path})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// LogAccess records a note open in the access log so session/co-access ranking
// sees it — the signal that powers surfacing. Fire-and-forget on the Swift side.
func (s *Session) LogAccess(path string) error {
	return s.ix.LogAccess(path, time.Now().Unix())
}

// --- SAS device pairing (this device is the NEW device / initiator) ---------
//
// The full handshake lives in internal/sync; these are thin wrappers. Flow:
//   reqID, _ := PairStart()            // show reqID; user runs `weft sync pair-approve <reqID>` on the Mac
//   for sas == "" { sas, _ = PairPoll() }   // poll on a timer; show sas
//   // user confirms sas matches the Mac's screen (and approves there)
//   for !done { done, _ = PairFinish() }    // poll on a timer; enrolled when true

// PairStart begins pairing as the new device: it posts a pairing request to the
// configured bucket and returns the short reqID the user carries to an already-
// enrolled device. Requires Configure to have set the bucket credentials.
func (s *Session) PairStart() (string, error) {
	p, err := sync.StartPairing(s.v, s.cfg)
	if err != nil {
		return "", err
	}
	s.pair = p
	return p.ReqID(), nil
}

// PairPoll checks once whether the enrolled device has responded. It returns
// ("", nil) while still waiting; once the responder's key arrives it reveals our
// committed nonce and returns the 8-digit SAS to compare against the other
// screen. Call on a timer until it returns a non-empty code.
func (s *Session) PairPoll() (string, error) {
	if s.pair == nil {
		return "", errors.New("weft: call PairStart first")
	}
	ready, err := s.pair.FetchResponderKey()
	if err != nil {
		return "", err
	}
	if !ready {
		return "", nil
	}
	return s.pair.SAS(), nil
}

// PairFinish checks once whether the enrolled device has sealed the vault key
// (which it does only after the human confirms the SAS matches). It returns true
// and enrolls this device when done, or false while still waiting. Call on a
// timer until it returns true.
func (s *Session) PairFinish() (bool, error) {
	if s.pair == nil {
		return false, errors.New("weft: call PairStart first")
	}
	eng, err := s.pair.Finish()
	if err != nil {
		return false, err
	}
	if eng == nil {
		return false, nil // the enrolled device hasn't sealed yet
	}
	s.eng = eng
	s.pair = nil
	return true, nil
}
