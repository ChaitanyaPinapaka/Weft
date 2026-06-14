package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	stdsync "sync"
	"time"

	"weft/internal/embed"
	"weft/internal/index"
	syncpkg "weft/internal/sync"
	"weft/internal/vault"
)

// startAutoSync turns `weft serve` into a live sync node: if the vault has sync
// configured (weft sync init/join), it converges through the E2EE bucket on an
// interval and re-derives the index for anything pulled — so edits made on this
// device propagate and edits from other devices appear automatically. A no-op
// (with a one-line notice) when sync isn't configured or the passphrase isn't
// available, so the daemon always serves regardless.
//
// The vault key is unwrapped from the local keyfile using WEFT_PASSPHRASE; the
// key is held only in memory, never re-persisted. Interval defaults to 30s,
// overridable via WEFT_SYNC_INTERVAL (a Go duration, e.g. "10s").
func startAutoSync(v *vault.Vault, ix *index.Index, emb embed.Embedder, hub *ambientHub) {
	if !syncpkg.Configured(v) {
		return
	}
	// Prefer the vault key cached in the OS keychain (init/join/pair stored it), so
	// the daemon needs no passphrase on a desktop. Fall back to WEFT_PASSPHRASE for
	// headless hosts using the on-disk keyfile.
	eng, err := syncpkg.OpenLocal(v)
	if errors.Is(err, syncpkg.ErrNeedPassphrase) {
		pass := os.Getenv("WEFT_PASSPHRASE")
		if pass == "" {
			fmt.Println("Sync  configured — unlock the keychain or set WEFT_PASSPHRASE to enable auto-sync")
			return
		}
		eng, err = syncpkg.Open(v, pass)
	}
	if err != nil {
		fmt.Printf("Sync  disabled: %v\n", err)
		return
	}

	interval := 30 * time.Second
	if s := os.Getenv("WEFT_SYNC_INTERVAL"); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			interval = d
		}
	}
	fmt.Printf("Sync  on — converging every %s\n", interval)

	go func() {
		syncOnce(eng, v, ix, emb, hub) // converge once at startup
		t := time.NewTicker(interval)
		defer t.Stop()
		for range t.C {
			syncOnce(eng, v, ix, emb, hub)
		}
	}()
}

func syncOnce(eng *syncpkg.Engine, v *vault.Vault, ix *index.Index, emb embed.Embedder, hub *ambientHub) {
	res, err := eng.Sync()
	if err != nil {
		fmt.Printf("sync: %v\n", err)
		return
	}
	// Pulled changes wrote new .html; re-derive the index (incremental via Stale).
	if res.Applied > 0 || len(res.ConflictCopies) > 0 {
		_ = indexAll(v, ix, emb)
		// Tell open clients which notes changed so they reload/warn instead of
		// holding (and later clobbering) a stale buffer.
		if hub != nil {
			for _, p := range res.AppliedPaths {
				hub.changed(p)
			}
			for _, p := range res.ConflictCopies {
				hub.changed(p)
			}
		}
		fmt.Printf("sync: applied %d, conflicts %d\n", res.Applied, len(res.ConflictCopies))
	}
}

// --- device pairing (the responder/enrolled side, over HTTP) ----------------
//
// The endpoints mirror `weft sync pair-approve`: list pending requests, begin
// an approval, poll until the initiator reveals (status shows the SAS), and —
// only after the human compares the two codes — confirm, which seals the vault
// key. Approving NEVER seals; confirm is the human-confirms-SAS security
// boundary (see pairing_flow.go's commit-reveal notes).

// pairingStore holds the daemon's single in-flight pairing approval — mutex-
// guarded server state, same pattern as paramStore. One at a time because the
// flow is human-driven: a second concurrent approval would put two SAS codes
// in play on one machine and invite confirming the wrong one.
type pairingStore struct {
	mu       stdsync.Mutex
	reqID    string // "" when nothing is in flight
	approval *syncpkg.PairApproval
	sas      string // set once the initiator's reveal verified
	errMsg   string // set when the approval died (tamper/backend error)
}

func newPairingStore() *pairingStore { return &pairingStore{} }

// clearLocked abandons the in-flight approval. Caller holds st.mu.
func (st *pairingStore) clearLocked() {
	st.reqID, st.approval, st.sas, st.errMsg = "", nil, "", ""
}

func pairJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// reqIDPattern matches a minted pairing request id: randHex(4) → 8 lowercase
// hex chars (pairing_flow.go). The id becomes part of a bucket object key, so
// validate it before use — an unconstrained body value (e.g. "../x") would
// otherwise escape the pairing/ prefix (path traversal on a file backend).
var reqIDPattern = regexp.MustCompile(`^[0-9a-f]{8}$`)

// pairingBody decodes the {"req_id":"…"} POST body shared by approve/confirm/
// cancel. Writes the 400 itself and returns ok=false on a bad body.
func pairingBody(w http.ResponseWriter, r *http.Request) (string, bool) {
	var body struct {
		ReqID string `json:"req_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		pairJSON(w, http.StatusBadRequest, map[string]any{"error": "bad JSON body"})
		return "", false
	}
	if !reqIDPattern.MatchString(body.ReqID) {
		pairJSON(w, http.StatusBadRequest, map[string]any{"error": "req_id is required"})
		return "", false
	}
	return body.ReqID, true
}

// listPairingHandler is GET /api/sync/pairing: pending request ids from the
// bucket, or 409 when this vault has no sync to pair against.
func listPairingHandler(v *vault.Vault) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !syncpkg.Configured(v) {
			pairJSON(w, http.StatusConflict, map[string]any{"error": "sync not configured"})
			return
		}
		be, err := syncpkg.OpenBackend(v)
		if err != nil {
			pairJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
			return
		}
		ids, err := syncpkg.ListPairingRequests(be)
		if err != nil {
			pairJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		if ids == nil {
			ids = []string{} // contract: "requests" is always an array
		}
		pairJSON(w, http.StatusOK, map[string]any{"requests": ids})
	}
}

// approvePairingHandler is POST /api/sync/pairing/approve {"req_id"}: begins
// the approval (posts our key + nonce to the bucket) and parks it in the
// store. 202 — the caller polls /status for the SAS. Does NOT seal anything.
func approvePairingHandler(v *vault.Vault, st *pairingStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqID, ok := pairingBody(w, r)
		if !ok {
			return
		}
		if !syncpkg.Configured(v) {
			pairJSON(w, http.StatusConflict, map[string]any{"error": "sync not configured"})
			return
		}
		st.mu.Lock()
		defer st.mu.Unlock()
		if st.reqID != "" {
			// Surface the parked request id. If it's a stale orphan (the prior
			// approving session crashed, or the phone restarted pairing with a
			// fresh id), the client cancels THAT id and retries — otherwise an
			// abandoned approval would wedge every future one until a daemon
			// restart. clearLocked is keyed on the parked id, so a cancel for it
			// always frees the slot.
			pairJSON(w, http.StatusConflict, map[string]any{
				"error":     "another approval is in flight",
				"in_flight": st.reqID,
			})
			return
		}
		// Unlock the vault key exactly the way auto-sync does: the keychain-
		// cached copy first, the WEFT_PASSPHRASE-unwrapped keyfile as the
		// headless fallback. Never prompts.
		vk, err := syncpkg.LocalVaultKey(v, os.Getenv("WEFT_PASSPHRASE"))
		if err != nil {
			pairJSON(w, http.StatusConflict, map[string]any{"error": "vault key unavailable: " + err.Error()})
			return
		}
		be, err := syncpkg.OpenBackend(v)
		if err != nil {
			pairJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
			return
		}
		a, err := syncpkg.BeginApprove(be, reqID, vk)
		if errors.Is(err, syncpkg.ErrNoPairingRequest) {
			pairJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		if err != nil {
			pairJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		st.reqID, st.approval = reqID, a
		pairJSON(w, http.StatusAccepted, map[string]any{"req_id": reqID})
	}
}

// pairingStatusHandler is GET /api/sync/pairing/status?req_id=X. Each call
// polls AwaitReveal once until the SAS is ready; a dead approval keeps
// reporting its error (it is never retried — cancel to clear it).
func pairingStatusHandler(st *pairingStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqID := r.URL.Query().Get("req_id")
		st.mu.Lock()
		switch {
		case st.reqID == "" || reqID != st.reqID:
			st.mu.Unlock()
			pairJSON(w, http.StatusNotFound, map[string]any{"error": "no such approval"})
			return
		case st.errMsg != "":
			msg := st.errMsg
			st.mu.Unlock()
			pairJSON(w, http.StatusOK, map[string]any{"state": "error", "error": msg})
			return
		case st.sas != "":
			sas := st.sas
			st.mu.Unlock()
			pairJSON(w, http.StatusOK, map[string]any{"state": "sas", "sas": sas})
			return
		}
		// Poll the bucket for the initiator's reveal WITHOUT holding the lock:
		// the backend Get carries no timeout, so a slow or hostile bucket must
		// not be able to park st.mu and wedge cancel (which needs the same lock)
		// or pile up blocked status-poll goroutines.
		approval := st.approval
		st.mu.Unlock()

		ok, err := approval.AwaitReveal() // one poll per status call

		st.mu.Lock()
		defer st.mu.Unlock()
		// The slot may have been cancelled or replaced while we polled off-lock.
		if st.reqID != reqID || st.approval != approval {
			pairJSON(w, http.StatusNotFound, map[string]any{"error": "no such approval"})
			return
		}
		if err != nil {
			st.errMsg = err.Error()
			pairJSON(w, http.StatusOK, map[string]any{"state": "error", "error": st.errMsg})
			return
		}
		if !ok {
			pairJSON(w, http.StatusOK, map[string]any{"state": "waiting"})
			return
		}
		st.sas = approval.SAS()
		pairJSON(w, http.StatusOK, map[string]any{"state": "sas", "sas": st.sas})
	}
}

// confirmPairingHandler is POST /api/sync/pairing/confirm {"req_id"} — the
// ONLY place the vault key gets sealed for the new device. Requires the
// approval to already be at the sas state: the human has seen both codes.
func confirmPairingHandler(st *pairingStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqID, ok := pairingBody(w, r)
		if !ok {
			return
		}
		st.mu.Lock()
		defer st.mu.Unlock()
		if st.reqID == "" || reqID != st.reqID {
			pairJSON(w, http.StatusNotFound, map[string]any{"error": "no such approval"})
			return
		}
		if st.errMsg != "" || st.sas == "" {
			pairJSON(w, http.StatusConflict, map[string]any{"error": "approval is not at the sas state"})
			return
		}
		if err := st.approval.Finalize(); err != nil {
			// Keep the approval at sas so a transient backend failure is
			// retryable with another confirm.
			pairJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		st.clearLocked()
		pairJSON(w, http.StatusOK, map[string]any{"done": true})
	}
}

// cancelPairingHandler is POST /api/sync/pairing/cancel {"req_id"}: abandons
// the in-flight approval without sealing. Idempotent — cancelling a request
// that isn't in flight is a 200 no-op.
func cancelPairingHandler(st *pairingStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqID, ok := pairingBody(w, r)
		if !ok {
			return
		}
		st.mu.Lock()
		if reqID == st.reqID {
			st.clearLocked()
		}
		st.mu.Unlock()
		pairJSON(w, http.StatusOK, map[string]any{"done": true})
	}
}
