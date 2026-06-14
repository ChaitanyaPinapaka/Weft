package sync

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"weft/internal/noteid"
	"weft/internal/vault"
)

// DeviceID identifies one device's manifest namespace in the bucket.
type DeviceID string

// VV is a version vector: deviceID → monotonically-bumped edit counter. A
// missing component reads as 0. Version vectors detect concurrency structurally,
// independent of wall clocks, so clock skew can at worst cause an extra
// conflict-copy — never silent data loss (which mtime last-writer-wins would).
type VV map[DeviceID]uint64

// cmp reports whether a ≥ b and b ≥ a (component-wise). From the pair:
// equal (both true), a-dominates (aGE && !bGE), b-dominates, or concurrent (neither).
func cmp(a, b VV) (aGE, bGE bool) {
	aGE, bGE = true, true
	for d, bc := range b {
		if a[d] < bc {
			aGE = false
		}
	}
	for d, ac := range a {
		if b[d] < ac {
			bGE = false
		}
	}
	return
}

func maxVV(a, b VV) VV {
	out := VV{}
	for d, c := range a {
		out[d] = c
	}
	for d, c := range b {
		if c > out[d] {
			out[d] = c
		}
	}
	return out
}

func (v VV) clone() VV {
	out := make(VV, len(v))
	for d, c := range v {
		out[d] = c
	}
	return out
}

// Entry is one note's sync metadata, keyed by its WeftID.
type Entry struct {
	WeftID  string `json:"weft_id"`
	Path    string `json:"path"`
	VV      VV     `json:"vv"`
	BlobID  string `json:"blob_id"` // sha256(content) hex
	Size    int64  `json:"size"`
	MTime   int64  `json:"mtime"`
	Deleted bool   `json:"deleted,omitempty"` // tombstone — bytes still live in .trash + the bucket
}

// state is the engine's local, never-synced bookkeeping.
type state struct {
	Device   DeviceID            `json:"device"`
	Gen      uint64              `json:"gen"`       // our last published generation
	LastSeen map[DeviceID]uint64 `json:"last_seen"` // peer → last folded generation
	Manifest map[string]Entry    `json:"manifest"`  // our merged view, by WeftID
	// MaxSelf is a monotonic high-water mark for THIS device's VV component. All
	// local edits bump to MaxSelf+1 (not a per-note ++), so restoring an older
	// state.json (a backup) can't re-issue a counter at or below one already
	// published — which would make a genuine new edit look causally old and be
	// silently dropped. It's also raised from any higher self-counter observed
	// in a peer's manifest (proof we published more than this restored state knows).
	MaxSelf uint64 `json:"max_self"`
	// SignKey is this device's Ed25519 private key, used to sign our HEADs. It
	// never leaves the device; the matching public key is published (sealed) in
	// the device registry. (P5 moves the private key to the OS keychain.)
	SignKey []byte `json:"sign_key,omitempty"`
}

// Engine converges one vault against one backend. All devices run identical
// logic; the backend is dumb storage. blobC/manC seal content + manifests; with
// the nop ciphers (New) the bucket holds plaintext, with real ciphers
// (NewEncrypted) it holds only ciphertext — the convergence code is identical.
type Engine struct {
	v     *vault.Vault
	be    Backend
	st    state
	dir   string // <vault>/.weft/sync
	blobC Cipher
	manC  Cipher
	regC  Cipher // seals device-registry records under the vault key

	signPriv   ed25519.PrivateKey
	registry   map[DeviceID]ed25519.PublicKey // peers' verifying keys, refreshed each pull
	registered bool                           // wrote our device record this process
}

// New loads (or initializes) a PLAINTEXT engine (nop ciphers) — P1 behavior,
// used by tests and the filesystem self-host before keys are set up.
func New(v *vault.Vault, be Backend) (*Engine, error) {
	return newEngine(v, be, nopCipher{}, nopCipher{}, nopCipher{})
}

// NewEncrypted loads an E2EE engine: blobs, manifests, and the device registry
// are sealed under subkeys of vk, so the backend (the user's cloud) sees only
// ciphertext and ciphertext-derived names. Every device with the same vk
// converges; the cloud can decrypt nothing.
func NewEncrypted(v *vault.Vault, be Backend, vk VaultKey) (*Engine, error) {
	return newEngine(v, be,
		newCipher(vk, "weft/v1 blob"),
		newCipher(vk, "weft/v1 manifest"),
		newCipher(vk, "weft/v1 registry"))
}

func newEngine(v *vault.Vault, be Backend, blobC, manC, regC Cipher) (*Engine, error) {
	dir := filepath.Join(v.Root, ".weft", "sync")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	e := &Engine{v: v, be: be, dir: dir, blobC: blobC, manC: manC, regC: regC,
		registry: map[DeviceID]ed25519.PublicKey{}}
	if data, err := os.ReadFile(filepath.Join(dir, "state.json")); err == nil {
		if err := json.Unmarshal(data, &e.st); err != nil {
			return nil, fmt.Errorf("sync: corrupt state: %w", err)
		}
	}
	if e.st.Device == "" {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, err
		}
		e.st.Device = DeviceID(hex.EncodeToString(b[:]))
	}
	// Mint this device's Ed25519 signing key once; it persists in state.json.
	if len(e.st.SignKey) == ed25519.PrivateKeySize {
		e.signPriv = ed25519.PrivateKey(e.st.SignKey)
	} else {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		e.signPriv = priv
		e.st.SignKey = priv
	}
	if e.st.LastSeen == nil {
		e.st.LastSeen = map[DeviceID]uint64{}
	}
	if e.st.Manifest == nil {
		e.st.Manifest = map[string]Entry{}
	}
	// Recover the self high-water mark from existing entries (covers an upgrade
	// from state without MaxSelf, and a restored-but-not-empty backup).
	for _, en := range e.st.Manifest {
		if c := en.VV[e.st.Device]; c > e.st.MaxSelf {
			e.st.MaxSelf = c
		}
	}
	return e, nil
}

// nextSelf returns the next monotonic counter for this device's VV component.
func (e *Engine) nextSelf() uint64 {
	e.st.MaxSelf++
	return e.st.MaxSelf
}

// recoverSelf heals this device's high-water marks (Gen + MaxSelf) from its OWN
// published manifests in the backend, BEFORE scanLocal mints any new counter or
// push picks a generation. Restoring an older state.json backup rolls both back;
// without this, scanLocal would issue a self-counter at or below one already
// published (which a peer reads as causally old and silently drops), and push
// would regress over newer generations. The peer-fold raise in foldEntry runs too
// late (after push) and never consults our own manifest (pull skips self).
//
// It must NOT trust the HEAD pointer: HEAD is a single mutable object the
// untrusted cloud can delete or garble with one write, and a low-but-validly-
// signed old HEAD could be replayed. Instead it heals from the self-MANIFESTS,
// which the cloud cannot forge (sealed under the vault key) — the max self-counter
// across every self-manifest that decrypts is a tamper-resistant high-water mark.
// Scanning content (not key numbers) also defeats relabeling a low generation onto
// a high key. Fast path: one List; the manifests are fetched only when the backend
// shows a generation beyond our local view (a restore, or tampering).
//
// Residual limit: if the cloud DELETES every self-manifest beyond our restored
// view, the high-water mark is genuinely gone from the bucket — pure data
// destruction, recoverable only from a peer that still holds it.
func (e *Engine) recoverSelf() error {
	prefix := fmt.Sprintf("manifest/%s/", e.st.Device)
	keys, err := e.be.List(prefix)
	if err != nil {
		return nil // transient backend error — heal next cycle
	}
	var gMax uint64
	for _, k := range keys {
		if g, ok := parseGen(strings.TrimPrefix(k, prefix)); ok && g > gMax {
			gMax = g
		}
	}
	if gMax <= e.st.Gen {
		return nil // fast path: nothing published beyond our local view
	}
	for _, k := range keys {
		g, ok := parseGen(strings.TrimPrefix(k, prefix))
		if !ok {
			continue
		}
		manBytes, err := e.be.Get(k)
		if err != nil {
			continue
		}
		plain, err := e.manC.Open(manBytes)
		if err != nil {
			continue // garbled or foreign — can't be one we published
		}
		var mine map[string]Entry
		if json.Unmarshal(plain, &mine) != nil {
			continue
		}
		if g > e.st.Gen {
			e.st.Gen = g // don't reissue generations we've already published
		}
		for _, en := range mine {
			if c := en.VV[e.st.Device]; c > e.st.MaxSelf {
				e.st.MaxSelf = c
			}
		}
	}
	return nil
}

// parseGen extracts N from a "<N>.json" manifest object name.
func parseGen(name string) (uint64, bool) {
	if !strings.HasSuffix(name, ".json") {
		return 0, false
	}
	var g uint64
	if _, err := fmt.Sscanf(strings.TrimSuffix(name, ".json"), "%d", &g); err != nil {
		return 0, false
	}
	return g, true
}

// Device returns this engine's device id.
func (e *Engine) Device() DeviceID { return e.st.Device }

// Result reports what a Sync did, for tests/UI.
type Result struct {
	Pushed         int
	Applied        int // remote changes written locally (fast-forwards)
	ConflictCopies []string
	Rejected       int // peer HEADs dropped: bad signature or HEAD/manifest mismatch
	Unverifiable   int // peer HEADs with no verifying key in the registry (e.g. a withheld device record)
}

// Sync runs one full convergence cycle: capture local edits, push, pull peers,
// merge by version-vector dominance, apply remote changes (fast-forward or
// conflict-copy), and persist state. Order-independent: repeated runs and runs
// on different devices converge to the same vault.
func (e *Engine) Sync() (Result, error) {
	var res Result
	if err := e.recoverSelf(); err != nil {
		return res, err
	}
	if err := e.scanLocal(); err != nil {
		return res, err
	}
	if err := e.push(&res); err != nil {
		return res, err
	}
	if err := e.pull(&res); err != nil {
		return res, err
	}
	if err := e.save(); err != nil {
		return res, err
	}
	return res, nil
}

// scanLocal walks the vault and folds genuine local edits into our manifest,
// bumping our own VV component. A note is "edited" when its content hash differs
// from what the manifest records (new note, body edit, or move keyed by WeftID).
// After the walk, any non-deleted entry whose note has vanished from disk is
// tombstoned (a user deletion) — the bytes survive in the bucket + .trash.
func (e *Engine) scanLocal() error {
	notes, err := e.v.List()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, n := range notes {
		content, err := e.v.Read(n.Path)
		if err != nil {
			continue
		}
		id := noteid.ReadWeftID(content)
		if id == "" {
			continue // un-stamped notes are picked up after the indexAll backfill stamps them
		}
		seen[id] = true
		sealed := e.blobC.Seal(content)
		bid := blobID(sealed) // address by hash-of-CIPHERTEXT — leaks nothing
		cur, ok := e.st.Manifest[id]
		if ok && !cur.Deleted && cur.BlobID == bid && cur.Path == n.Path {
			continue // unchanged
		}
		vv := VV{}
		if ok {
			vv = cur.VV.clone()
		}
		vv[e.st.Device] = e.nextSelf() // monotonic, rollback-safe
		e.st.Manifest[id] = Entry{
			WeftID: id, Path: n.Path, VV: vv, BlobID: bid,
			Size: int64(len(content)), MTime: n.ModTime.Unix(),
		}
		if _, err := e.be.PutIfAbsent("blobs/"+bid, sealed); err != nil {
			return err
		}
	}

	// Deletion detection: an entry we believe live but whose file is gone (and
	// not merely moved — a move keeps the same WeftID, so it'd be in `seen`) is
	// a user deletion → tombstone it. The v.Exists guard avoids a transient read
	// error masquerading as a delete.
	for id, en := range e.st.Manifest {
		if en.Deleted || seen[id] {
			continue
		}
		if e.v.Exists(en.Path) {
			continue
		}
		en.Deleted = true
		en.VV = en.VV.clone()
		en.VV[e.st.Device] = e.nextSelf()
		e.st.Manifest[id] = en
	}
	return nil
}

// putBlob seals content, content-addresses it by hash-of-ciphertext, and
// uploads idempotently. Returns the blob id to record in the manifest.
func (e *Engine) putBlob(content []byte) (string, error) {
	sealed := e.blobC.Seal(content)
	bid := blobID(sealed)
	if _, err := e.be.PutIfAbsent("blobs/"+bid, sealed); err != nil {
		return "", err
	}
	return bid, nil
}

// getBlob fetches and decrypts a blob by id.
func (e *Engine) getBlob(bid string) ([]byte, error) {
	sealed, err := e.be.Get("blobs/" + bid)
	if err != nil {
		return nil, err
	}
	return e.blobC.Open(sealed)
}

// push writes a new full-manifest generation and flips HEAD — but only if our
// manifest changed since the last publish (no empty generations). Blobs were
// already staged in scanLocal/apply, so a published manifest's blobs always
// exist; HEAD flips LAST, so an interrupted push is invisible to peers.
func (e *Engine) push(res *Result) error {
	plain, _ := json.Marshal(e.st.Manifest)
	snapshot := e.manC.Seal(plain) // deterministic seal: equal manifests → equal bytes
	prevKey := fmt.Sprintf("manifest/%s/%d.json", e.st.Device, e.st.Gen)
	if e.st.Gen > 0 {
		if prev, err := e.be.Get(prevKey); err == nil && string(prev) == string(snapshot) {
			return nil // nothing changed
		}
	} else if len(e.st.Manifest) == 0 {
		return nil
	}
	e.st.Gen++
	if err := e.be.Put(fmt.Sprintf("manifest/%s/%d.json", e.st.Device, e.st.Gen), snapshot); err != nil {
		return err
	}
	if err := e.register(); err != nil { // publish our verifying key before the HEAD that needs it
		return err
	}
	// HEAD is signed and bound to the exact manifest bytes, and flipped LAST.
	hd := signHead(e.signPriv, e.st.Device, e.st.Gen, hashBytes(snapshot))
	hb, err := json.Marshal(hd)
	if err != nil {
		return err
	}
	if err := e.be.Put(fmt.Sprintf("manifest/%s/HEAD", e.st.Device), hb); err != nil {
		return err
	}
	res.Pushed = len(e.st.Manifest)
	return nil
}

// register publishes this device's signing pubkey (sealed) so peers can verify
// our HEADs. Idempotent within a process; the per-device key means no write race.
func (e *Engine) register() error {
	if e.registered {
		return nil
	}
	rec := deviceRecord{
		DeviceID: e.st.Device,
		SignPub:  e.signPriv.Public().(ed25519.PublicKey),
		Created:  time.Now().Unix(),
	}
	plain, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := e.be.Put(deviceKey(e.st.Device), e.regC.Seal(plain)); err != nil {
		return err
	}
	e.registered = true
	return nil
}

// refreshRegistry loads every device's sealed record into the verifying-key map.
// Records that don't decrypt with our key (cloud-injected or corrupt) are ignored.
func (e *Engine) refreshRegistry() error {
	keys, err := e.be.List("meta/devices/")
	if err != nil {
		return err
	}
	for _, k := range keys {
		sealed, err := e.be.Get(k)
		if err != nil {
			continue
		}
		plain, err := e.regC.Open(sealed)
		if err != nil {
			continue // not sealed under our vault key → not a legitimate device
		}
		var rec deviceRecord
		if json.Unmarshal(plain, &rec) != nil {
			continue
		}
		// The record's claimed id must match its object key, so a sealed record
		// relocated to another device's key can't (re)bind a pubkey under an id it
		// wasn't written for. Belt-and-suspenders over the AEAD: the cloud can't
		// forge a record, but this also holds under a future plaintext registry.
		if string(rec.DeviceID) != strings.TrimPrefix(k, "meta/devices/") {
			continue
		}
		if len(rec.SignPub) == ed25519.PublicKeySize {
			e.registry[rec.DeviceID] = ed25519.PublicKey(rec.SignPub)
		}
	}
	return nil
}

// pull folds every peer device's latest manifest into ours and applies the
// resulting changes to disk.
func (e *Engine) pull(res *Result) error {
	if err := e.refreshRegistry(); err != nil {
		return err
	}
	keys, err := e.be.List("manifest/")
	if err != nil {
		return err
	}
	devices := map[DeviceID]bool{}
	for _, k := range keys {
		// manifest/<dev>/HEAD
		parts := strings.Split(k, "/")
		if len(parts) == 3 && parts[2] == "HEAD" {
			devices[DeviceID(parts[1])] = true
		}
	}
	for dev := range devices {
		if dev == e.st.Device {
			continue
		}
		headBytes, err := e.be.Get(fmt.Sprintf("manifest/%s/HEAD", dev))
		if err != nil {
			continue
		}
		var hd signedHead
		if json.Unmarshal(headBytes, &hd) != nil {
			res.Rejected++
			continue // unparseable HEAD
		}
		pub, ok := e.registry[dev]
		if !ok {
			// No verifying key yet (device not registered) or its record was
			// withheld/dropped by the cloud — surface it instead of looking like
			// an up-to-date peer. Folds once the record (re)appears.
			res.Unverifiable++
			continue
		}
		if !hd.verify(pub, dev) {
			res.Rejected++ // forged or tampered HEAD
			continue
		}
		if hd.Gen <= e.st.LastSeen[dev] {
			continue // already folded, or a rolled-back HEAD below our high-water mark
		}
		manBytes, err := e.be.Get(fmt.Sprintf("manifest/%s/%d.json", dev, hd.Gen))
		if err != nil {
			continue
		}
		if hashBytes(manBytes) != hd.ManifestHash {
			res.Rejected++ // HEAD points at a manifest it didn't sign
			continue
		}
		plain, err := e.manC.Open(manBytes)
		if err != nil {
			continue // not decryptable with our key — skip
		}
		var peer map[string]Entry
		if json.Unmarshal(plain, &peer) != nil {
			continue
		}
		for id, r := range peer {
			if err := e.foldEntry(id, r, dev, res); err != nil {
				return err
			}
		}
		e.st.LastSeen[dev] = hd.Gen
	}
	return nil
}

// foldEntry merges one remote entry into our manifest + disk, per the four
// version-vector cases. Raises the self high-water mark from the peer's view of
// us (rollback safety). Honors delete-loses-to-edit. Never silently overwrites a
// divergent local edit; never hard-deletes (tombstones move bytes to .trash).
func (e *Engine) foldEntry(id string, r Entry, peer DeviceID, res *Result) error {
	if c := r.VV[e.st.Device]; c > e.st.MaxSelf {
		e.st.MaxSelf = c // the peer saw us further along than our (maybe restored) state knows
	}
	l, have := e.st.Manifest[id]
	if !have {
		return e.applyRemote(id, r, res)
	}
	aGE, bGE := cmp(l.VV, r.VV)
	concurrent := !aGE && !bGE

	// delete-loses-to-edit: a concurrent tombstone vs edit always resolves in
	// favor of the EDIT (a never-delete tool should resurrect, not vanish).
	if concurrent && (l.Deleted != r.Deleted) {
		merged := maxVV(l.VV, r.VV)
		if r.Deleted { // remote tombstoned, we edited → keep our edit, swallow the tombstone
			l.VV = merged
			e.st.Manifest[id] = l
			return nil
		}
		// we tombstoned, remote edited → resurrect the remote edit at its path.
		r.VV = merged
		return e.applyRemote(id, r, res)
	}

	switch {
	case aGE && bGE: // equal VV
		if l.BlobID != r.BlobID || l.Deleted != r.Deleted {
			return e.conflict(id, l, r, res)
		}
		return nil
	case aGE && !bGE: // local dominates — we're ahead
		return nil
	case !aGE && bGE: // remote dominates — fast-forward (incl. a dominating tombstone)
		return e.applyRemote(id, r, res)
	default: // concurrent, same deleted-state — content conflict
		return e.conflict(id, l, r, res)
	}
}

// applyRemote brings local state to the remote entry. A tombstone moves our copy
// to .trash (never a hard delete); a path change is completed as a move (old
// file trashed); otherwise the remote content is written at its path. Same
// content (blob present) is a metadata-only update.
// lockedWrite / lockedTrash mutate a single note while holding its vault
// per-path lock, so a concurrent editor save / capture to the same file can't
// interleave with sync's apply and lose data. Each call is a discrete write
// (sync already holds the decrypted content), so per-call locking is enough —
// the engine never nests vault locks.
func (e *Engine) lockedWrite(rel string, content []byte) error {
	e.v.Lock(rel)
	defer e.v.Unlock(rel)
	return e.v.Write(rel, content)
}

func (e *Engine) lockedTrash(rel string) error {
	e.v.Lock(rel)
	defer e.v.Unlock(rel)
	return e.v.Trash(rel)
}

func (e *Engine) applyRemote(id string, r Entry, res *Result) error {
	prev, had := e.st.Manifest[id]

	if r.Deleted {
		if had && !prev.Deleted {
			_ = e.lockedTrash(prev.Path) // preserve bytes in .trash
			res.Applied++
		}
		e.st.Manifest[id] = r
		return nil
	}

	content, err := e.getBlob(r.BlobID)
	if err != nil {
		return nil // peer's blob not uploaded yet; retry next pull
	}
	if err := e.lockedWrite(r.Path, content); err != nil {
		return err
	}
	// Rename/move: if the note used to live elsewhere, trash the stale old file.
	if had && prev.Path != "" && prev.Path != r.Path {
		_ = e.lockedTrash(prev.Path)
	}
	e.st.Manifest[id] = r
	res.Applied++
	return nil
}

// conflict resolves a concurrent divergence DETERMINISTICALLY so that two
// devices which both detect the same conflict converge to EXACTLY the same vault
// (the P1 limitation). The winner is a pure function of the two entries — smaller
// blob id wins, ties broken by smaller path — and the winner brings ITS OWN path
// as canonical. Sourcing the canonical path from the local entry would never
// converge when a rename was concurrent with the edit (each device would keep its
// own filename). The loser becomes a conflict-copy whose WeftID + path are a pure
// function of (originalWeftID, sorted blob pair, winner path), identical on every
// device. No data is lost: both versions always exist as first-class notes.
func (e *Engine) conflict(id string, l, r Entry, res *Result) error {
	merged := maxVV(l.VV, r.VV)
	if l.Deleted && r.Deleted { // both tombstoned — stay deleted, just merge VVs
		l.VV = merged
		e.st.Manifest[id] = l
		return nil
	}

	// Deterministic winner, independent of which side is local: smaller blob id,
	// then smaller path. Both devices pick the same winner and the same winPath.
	win, lose := l, r
	if r.BlobID < l.BlobID || (r.BlobID == l.BlobID && r.Path < l.Path) {
		win, lose = r, l
	}

	// Identical content, same deleted-state — not a content divergence. But the
	// paths may differ (a concurrent rename to two names): converge to the winner's
	// canonical path and trash our stale local file. No conflict-copy needed.
	if win.BlobID == lose.BlobID && l.Deleted == r.Deleted {
		if l.Path != win.Path {
			content, err := e.getBlob(win.BlobID)
			if err != nil {
				return nil // blob not ready; retry next pull, manifest path unchanged
			}
			if err := e.lockedWrite(win.Path, content); err != nil {
				return err
			}
			_ = e.lockedTrash(l.Path)
			res.Applied++
		}
		l.Path = win.Path
		l.VV = merged
		e.st.Manifest[id] = l
		return nil
	}

	winContent, err := e.getBlob(win.BlobID)
	if err != nil {
		return nil // a blob isn't uploaded yet; retry next pull
	}
	loseContent, err := e.getBlob(lose.BlobID)
	if err != nil {
		return nil
	}

	// Winner takes the canonical path (idempotent write). If our local copy lived
	// at a different path (a concurrent rename), trash the stale file so the winner
	// isn't left behind as a duplicate.
	if err := e.lockedWrite(win.Path, winContent); err != nil {
		return err
	}
	if l.Path != "" && l.Path != win.Path {
		_ = e.lockedTrash(l.Path)
	}
	e.st.Manifest[id] = Entry{
		WeftID: id, Path: win.Path, VV: merged, BlobID: win.BlobID,
		Size: int64(len(winContent)), MTime: win.MTime,
	}

	// The loser becomes a conflict-copy with a deterministic id + path, stamped so
	// it's a distinct surfacing note. Both devices compute the same cid + path +
	// stamped bytes (all pure functions of the diverging pair), so the copy dedups
	// to exactly one across the vault.
	cid := deriveConflictID(id, win.BlobID, lose.BlobID)
	cpath := conflictPath(win.Path, cid)
	stamped, err := noteid.WithWeftID(loseContent, cid)
	if err != nil {
		stamped = loseContent
	}
	if err := e.lockedWrite(cpath, stamped); err != nil {
		return err
	}
	bid, err := e.putBlob(stamped)
	if err != nil {
		return err
	}
	e.st.Manifest[cid] = Entry{
		WeftID: cid, Path: cpath, VV: merged.clone(), BlobID: bid,
		Size: int64(len(stamped)), MTime: lose.MTime,
	}
	res.ConflictCopies = append(res.ConflictCopies, cpath)
	return nil
}

func (e *Engine) save() error {
	data, err := json.MarshalIndent(e.st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(e.dir, "state.json"), data, 0o644)
}

func blobID(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// deriveConflictID is a deterministic id for the conflict copy of `orig`,
// computed from the sorted diverging blob pair so every device that sees the
// same two versions produces the same id (and therefore the same single copy).
func deriveConflictID(orig, winBlob, loseBlob string) string {
	sum := sha256.Sum256([]byte("weft/conflict\x00" + orig + "\x00" + winBlob + "\x00" + loseBlob))
	s := hex.EncodeToString(sum[:16])
	return s[0:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:32]
}

// conflictPath is the deterministic sibling path for a conflict copy with id cid.
func conflictPath(base, cid string) string {
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	return fmt.Sprintf("%s.conflict-%s%s", stem, cid[:8], ext)
}
