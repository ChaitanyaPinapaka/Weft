package sync

import (
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
	WeftID string `json:"weft_id"`
	Path   string `json:"path"`
	VV     VV     `json:"vv"`
	BlobID string `json:"blob_id"` // sha256(content) hex
	Size   int64  `json:"size"`
	MTime  int64  `json:"mtime"`
}

// state is the engine's local, never-synced bookkeeping.
type state struct {
	Device   DeviceID            `json:"device"`
	Gen      uint64              `json:"gen"`       // our last published generation
	LastSeen map[DeviceID]uint64 `json:"last_seen"` // peer → last folded generation
	Manifest map[string]Entry    `json:"manifest"`  // our merged view, by WeftID
}

// Engine converges one vault against one backend. All devices run identical
// logic; the backend is dumb storage.
type Engine struct {
	v   *vault.Vault
	be  Backend
	st  state
	dir string // <vault>/.weft/sync
}

// New loads (or initializes) the engine's local state under <vault>/.weft/sync.
func New(v *vault.Vault, be Backend) (*Engine, error) {
	dir := filepath.Join(v.Root, ".weft", "sync")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	e := &Engine{v: v, be: be, dir: dir}
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
	if e.st.LastSeen == nil {
		e.st.LastSeen = map[DeviceID]uint64{}
	}
	if e.st.Manifest == nil {
		e.st.Manifest = map[string]Entry{}
	}
	return e, nil
}

// Device returns this engine's device id.
func (e *Engine) Device() DeviceID { return e.st.Device }

// Result reports what a Sync did, for tests/UI.
type Result struct {
	Pushed         int
	Applied        int // remote changes written locally (fast-forwards)
	ConflictCopies []string
}

// Sync runs one full convergence cycle: capture local edits, push, pull peers,
// merge by version-vector dominance, apply remote changes (fast-forward or
// conflict-copy), and persist state. Order-independent: repeated runs and runs
// on different devices converge to the same vault.
func (e *Engine) Sync() (Result, error) {
	var res Result
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
// from what the manifest records (new note, body edit, or move).
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
		bid := blobID(content)
		cur, ok := e.st.Manifest[id]
		if ok && cur.BlobID == bid && cur.Path == n.Path {
			continue // unchanged
		}
		vv := VV{}
		if ok {
			vv = cur.VV.clone()
		}
		vv[e.st.Device]++ // a local edit bumps our component
		e.st.Manifest[id] = Entry{
			WeftID: id, Path: n.Path, VV: vv, BlobID: bid,
			Size: int64(len(content)), MTime: n.ModTime.Unix(),
		}
		if err := e.stageBlob(bid, content); err != nil {
			return err
		}
	}
	return nil
}

// stageBlob uploads a content-addressed blob (idempotent: skipped if present).
func (e *Engine) stageBlob(bid string, content []byte) error {
	_, err := e.be.PutIfAbsent("blobs/"+bid, content)
	return err
}

// push writes a new full-manifest generation and flips HEAD — but only if our
// manifest changed since the last publish (no empty generations). Blobs were
// already staged in scanLocal/apply, so a published manifest's blobs always
// exist; HEAD flips LAST, so an interrupted push is invisible to peers.
func (e *Engine) push(res *Result) error {
	snapshot, _ := json.Marshal(e.st.Manifest)
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
	if err := e.be.Put(fmt.Sprintf("manifest/%s/HEAD", e.st.Device), []byte(fmt.Sprintf("%d", e.st.Gen))); err != nil {
		return err
	}
	res.Pushed = len(e.st.Manifest)
	return nil
}

// pull folds every peer device's latest manifest into ours and applies the
// resulting changes to disk.
func (e *Engine) pull(res *Result) error {
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
		var gen uint64
		fmt.Sscanf(string(headBytes), "%d", &gen)
		if gen <= e.st.LastSeen[dev] {
			continue // already folded
		}
		manBytes, err := e.be.Get(fmt.Sprintf("manifest/%s/%d.json", dev, gen))
		if err != nil {
			continue
		}
		var peer map[string]Entry
		if json.Unmarshal(manBytes, &peer) != nil {
			continue
		}
		for id, r := range peer {
			if err := e.foldEntry(id, r, dev, res); err != nil {
				return err
			}
		}
		e.st.LastSeen[dev] = gen
	}
	return nil
}

// foldEntry merges one remote entry into our manifest + disk, per the four
// version-vector cases. Never overwrites a divergent local edit; never deletes.
func (e *Engine) foldEntry(id string, r Entry, peer DeviceID, res *Result) error {
	l, have := e.st.Manifest[id]
	if !have {
		// New to us: adopt and write it.
		return e.applyRemote(id, r, res)
	}
	aGE, bGE := cmp(l.VV, r.VV)
	switch {
	case aGE && bGE: // equal VV
		if l.BlobID != r.BlobID {
			return e.conflict(id, l, r, res) // same VV, different bytes — anomaly, treat as conflict
		}
		return nil
	case aGE && !bGE: // local dominates — we're ahead, nothing to apply
		return nil
	case !aGE && bGE: // remote dominates — fast-forward
		return e.applyRemote(id, r, res)
	default: // concurrent — true conflict
		return e.conflict(id, l, r, res)
	}
}

// applyRemote writes the remote version to disk (fast-forward) and adopts its
// entry. Same content (blob already present) is a metadata-only update.
func (e *Engine) applyRemote(id string, r Entry, res *Result) error {
	content, err := e.be.Get("blobs/" + r.BlobID)
	if err != nil {
		return nil // blob not yet uploaded by peer; skip this round, retry next pull
	}
	if err := e.v.Write(r.Path, content); err != nil {
		return err
	}
	e.st.Manifest[id] = r
	res.Applied++
	return nil
}

// conflict keeps the local file untouched and materializes the remote version
// as a sibling conflict-copy with its own new WeftID. The local entry's VV is
// merged to the component-wise max so the divergence is recorded as resolved and
// does not re-spawn copies on every future sync. No data is ever lost: both
// versions live as first-class notes.
func (e *Engine) conflict(id string, l, r Entry, res *Result) error {
	content, err := e.be.Get("blobs/" + r.BlobID)
	if err != nil {
		return nil // remote blob not present yet; retry next pull
	}
	cpath := conflictPath(l.Path, r)
	// Give the copy its own identity so it's a distinct, surfacing note.
	cid := noteid.Derive(cpath, content)
	stamped, err := noteid.WithWeftID(content, cid)
	if err != nil {
		stamped = content
	}
	if err := e.v.Write(cpath, stamped); err != nil {
		return err
	}
	// Record the conflict-copy as a brand-new local note so it propagates.
	e.st.Manifest[cid] = Entry{
		WeftID: cid, Path: cpath, VV: VV{e.st.Device: 1},
		BlobID: blobID(stamped), Size: int64(len(stamped)), MTime: time.Now().Unix(),
	}
	if err := e.stageBlob(blobID(stamped), stamped); err != nil {
		return err
	}
	// Resolve the original's divergence: local file stays, VV jumps to max.
	l.VV = maxVV(l.VV, r.VV)
	e.st.Manifest[id] = l
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

// conflictPath builds a sibling path for a conflict copy. P1 uses the peer
// device + date; P4 makes the copy's identity deterministic across devices.
func conflictPath(base string, r Entry) string {
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	short := r.WeftID
	if len(short) > 8 {
		short = short[:8]
	}
	return fmt.Sprintf("%s.conflict-%s%s", stem, short, ext)
}
