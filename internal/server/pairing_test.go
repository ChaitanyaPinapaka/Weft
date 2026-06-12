package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	syncpkg "weft/internal/sync"
	"weft/internal/vault"
)

// pairingEnv is a daemon-side vault enrolled in sync over an fs bucket, plus
// the bucket handle/config so a test can drive the initiator (new device)
// directly against the same bucket the endpoints talk to.
type pairingEnv struct {
	v   *vault.Vault
	be  syncpkg.Backend
	cfg syncpkg.Config
	eng *syncpkg.Engine
	st  *pairingStore
}

func newPairingEnv(t *testing.T) *pairingEnv {
	t.Helper()
	t.Setenv("WEFT_SECRET_STORE", "file") // keep tests off the real OS keychain
	bucket := t.TempDir()
	be, err := syncpkg.NewFileBackend(bucket)
	if err != nil {
		t.Fatal(err)
	}
	cfg := syncpkg.Config{Provider: "fs", FSPath: bucket}
	v, err := vault.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Init enrolls the daemon's vault and caches the vault key in the (file)
	// secret store — the same state LocalVaultKey reads for auto-sync.
	eng, err := syncpkg.Init(v, cfg, "test-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	return &pairingEnv{v: v, be: be, cfg: cfg, eng: eng, st: newPairingStore()}
}

// call drives a pairing handler with an optional JSON body and decodes the
// JSON response into a map (empty map if the body isn't JSON).
func call(t *testing.T, h http.HandlerFunc, method, target string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, target, rd)
	rr := httptest.NewRecorder()
	h(rr, req)
	out := map[string]any{}
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return rr, out
}

func reqBody(id string) map[string]any { return map[string]any{"req_id": id} }

// TestPairingNotConfigured: without sync set up, listing 409s with the
// contract's error body, and approve refuses likewise.
func TestPairingNotConfigured(t *testing.T) {
	t.Setenv("WEFT_SECRET_STORE", "file")
	v, err := vault.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rr, out := call(t, listPairingHandler(v), http.MethodGet, "/api/sync/pairing", nil)
	if rr.Code != http.StatusConflict {
		t.Fatalf("list without sync: want 409, got %d: %s", rr.Code, rr.Body.String())
	}
	if out["error"] != "sync not configured" {
		t.Fatalf(`want {"error":"sync not configured"}, got %v`, out)
	}
	rr, _ = call(t, approvePairingHandler(v, newPairingStore()), http.MethodPost, "/api/sync/pairing/approve", reqBody("aabbccdd"))
	if rr.Code != http.StatusConflict {
		t.Fatalf("approve without sync: want 409, got %d", rr.Code)
	}
}

// TestPairingApproveConfirmFlow walks the whole responder contract: list shows
// the request, approve begins (202), status walks waiting → sas (matching the
// initiator's SAS), confirm — and ONLY confirm — seals the vault key, and the
// initiator finishes with a working engine that decrypts the vault.
func TestPairingApproveConfirmFlow(t *testing.T) {
	env := newPairingEnv(t)

	// Daemon-side content the paired device must end up able to decrypt.
	if err := env.v.Write("n.html", []byte(`<!DOCTYPE html><html><head><meta name="weft-id" content="W"><title>n</title></head><body><article><p>paired-content</p></article></body></html>`)); err != nil {
		t.Fatal(err)
	}
	if _, err := env.eng.Sync(); err != nil {
		t.Fatal(err)
	}

	// The NEW device posts its pairing request (initiator driven from the test).
	vb, err := vault.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := syncpkg.StartPairing(vb, env.cfg)
	if err != nil {
		t.Fatal(err)
	}

	list := listPairingHandler(env.v)
	approve := approvePairingHandler(env.v, env.st)
	status := pairingStatusHandler(env.st)
	confirm := confirmPairingHandler(env.st)

	rr, out := call(t, list, http.MethodGet, "/api/sync/pairing", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	found := false
	reqs, _ := out["requests"].([]any)
	for _, id := range reqs {
		if id == p.ReqID() {
			found = true
		}
	}
	if !found {
		t.Fatalf("list should include %q, got %v", p.ReqID(), out)
	}

	// Status before any approval is a 404.
	if rr, _ := call(t, status, http.MethodGet, "/api/sync/pairing/status?req_id="+p.ReqID(), nil); rr.Code != http.StatusNotFound {
		t.Fatalf("status before approve: want 404, got %d", rr.Code)
	}

	rr, out = call(t, approve, http.MethodPost, "/api/sync/pairing/approve", reqBody(p.ReqID()))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("approve: want 202, got %d: %s", rr.Code, rr.Body.String())
	}
	if out["req_id"] != p.ReqID() {
		t.Fatalf(`approve: want {"req_id":%q}, got %v`, p.ReqID(), out)
	}

	// Initiator hasn't revealed its nonce yet → waiting.
	rr, out = call(t, status, http.MethodGet, "/api/sync/pairing/status?req_id="+p.ReqID(), nil)
	if rr.Code != http.StatusOK || out["state"] != "waiting" {
		t.Fatalf(`status: want 200 {"state":"waiting"}, got %d %v`, rr.Code, out)
	}
	// Confirming before the SAS is shown must be refused — the human hasn't
	// compared anything yet.
	if rr, _ := call(t, confirm, http.MethodPost, "/api/sync/pairing/confirm", reqBody(p.ReqID())); rr.Code != http.StatusConflict {
		t.Fatalf("confirm before sas: want 409, got %d", rr.Code)
	}
	// Status for a req that isn't the in-flight one is a 404.
	if rr, _ := call(t, status, http.MethodGet, "/api/sync/pairing/status?req_id=zzzzzzzz", nil); rr.Code != http.StatusNotFound {
		t.Fatalf("status unknown req: want 404, got %d", rr.Code)
	}

	if ok, err := p.FetchResponderKey(); err != nil || !ok {
		t.Fatalf("FetchResponderKey: ok=%v err=%v", ok, err)
	}

	rr, out = call(t, status, http.MethodGet, "/api/sync/pairing/status?req_id="+p.ReqID(), nil)
	if rr.Code != http.StatusOK || out["state"] != "sas" {
		t.Fatalf(`status: want 200 {"state":"sas"}, got %d %v`, rr.Code, out)
	}
	if out["sas"] != p.SAS() {
		t.Fatalf("SAS must match the initiator's: daemon=%v initiator=%s", out["sas"], p.SAS())
	}

	// SECURITY BOUNDARY: approving + reaching sas must NOT have sealed the
	// vault key — only the human-confirmed confirm call may.
	if ok, _ := env.be.Head("pairing/" + p.ReqID() + "/sealed"); ok {
		t.Fatal("vault key sealed before confirm — approval must never seal")
	}

	rr, out = call(t, confirm, http.MethodPost, "/api/sync/pairing/confirm", reqBody(p.ReqID()))
	if rr.Code != http.StatusOK || out["done"] != true {
		t.Fatalf(`confirm: want 200 {"done":true}, got %d %v`, rr.Code, out)
	}
	if ok, _ := env.be.Head("pairing/" + p.ReqID() + "/sealed"); !ok {
		t.Fatal("confirm must seal the vault key")
	}

	// The new device finishes and can decrypt the vault — proof the daemon
	// sealed the real key.
	eng, err := p.Finish()
	if err != nil || eng == nil {
		t.Fatalf("Finish: eng=%v err=%v", eng, err)
	}
	if _, err := eng.Sync(); err != nil {
		t.Fatal(err)
	}
	got, err := vb.Read("n.html")
	if err != nil || !strings.Contains(string(got), "paired-content") {
		t.Fatalf("paired device must decrypt the vault: err=%v content=%q", err, got)
	}

	// The slot is freed after confirm.
	if rr, _ := call(t, status, http.MethodGet, "/api/sync/pairing/status?req_id="+p.ReqID(), nil); rr.Code != http.StatusNotFound {
		t.Fatalf("status after confirm: want 404, got %d", rr.Code)
	}
}

// TestPairingSingleInFlightAndCancel: a second approve while one is in flight
// 409s; cancel frees the slot WITHOUT sealing; an unknown req 404s.
func TestPairingSingleInFlightAndCancel(t *testing.T) {
	env := newPairingEnv(t)
	approve := approvePairingHandler(env.v, env.st)
	cancel := cancelPairingHandler(env.st)

	vb1, _ := vault.New(t.TempDir())
	p1, err := syncpkg.StartPairing(vb1, env.cfg)
	if err != nil {
		t.Fatal(err)
	}
	vb2, _ := vault.New(t.TempDir())
	p2, err := syncpkg.StartPairing(vb2, env.cfg)
	if err != nil {
		t.Fatal(err)
	}

	if rr, _ := call(t, approve, http.MethodPost, "/api/sync/pairing/approve", reqBody(p1.ReqID())); rr.Code != http.StatusAccepted {
		t.Fatalf("approve p1: want 202, got %d", rr.Code)
	}
	if rr, _ := call(t, approve, http.MethodPost, "/api/sync/pairing/approve", reqBody(p2.ReqID())); rr.Code != http.StatusConflict {
		t.Fatalf("approve while in flight: want 409, got %d", rr.Code)
	}

	rr, out := call(t, cancel, http.MethodPost, "/api/sync/pairing/cancel", reqBody(p1.ReqID()))
	if rr.Code != http.StatusOK || out["done"] != true {
		t.Fatalf(`cancel: want 200 {"done":true}, got %d %v`, rr.Code, out)
	}
	if ok, _ := env.be.Head("pairing/" + p1.ReqID() + "/sealed"); ok {
		t.Fatal("cancel must never seal the vault key")
	}

	// Slot freed — the other request can now be approved.
	if rr, _ := call(t, approve, http.MethodPost, "/api/sync/pairing/approve", reqBody(p2.ReqID())); rr.Code != http.StatusAccepted {
		t.Fatalf("approve after cancel: want 202, got %d", rr.Code)
	}
	if rr, _ := call(t, cancel, http.MethodPost, "/api/sync/pairing/cancel", reqBody(p2.ReqID())); rr.Code != http.StatusOK {
		t.Fatalf("cancel p2: want 200, got %d", rr.Code)
	}

	// Unknown request id → 404.
	if rr, _ := call(t, approve, http.MethodPost, "/api/sync/pairing/approve", reqBody("ffffffff")); rr.Code != http.StatusNotFound {
		t.Fatalf("approve unknown req: want 404, got %d", rr.Code)
	}
	// Malformed body → 400.
	req := httptest.NewRequest(http.MethodPost, "/api/sync/pairing/approve", strings.NewReader("not json"))
	rec := httptest.NewRecorder()
	approve(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad body: want 400, got %d", rec.Code)
	}
}

// TestPairingStatusTamperedReveal: a reveal that doesn't match the initiator's
// commitment surfaces as {"state":"error"} — and stays an error on re-poll
// (the dead approval is never retried behind the human's back).
func TestPairingStatusTamperedReveal(t *testing.T) {
	env := newPairingEnv(t)
	vb, _ := vault.New(t.TempDir())
	p, err := syncpkg.StartPairing(vb, env.cfg)
	if err != nil {
		t.Fatal(err)
	}
	approve := approvePairingHandler(env.v, env.st)
	status := pairingStatusHandler(env.st)
	if rr, _ := call(t, approve, http.MethodPost, "/api/sync/pairing/approve", reqBody(p.ReqID())); rr.Code != http.StatusAccepted {
		t.Fatalf("approve: want 202, got %d", rr.Code)
	}

	// Tamper: post a 16-byte nonce that can't match the commitment in req.
	fake := `{"na":"` + base64.StdEncoding.EncodeToString(make([]byte, 16)) + `"}`
	if err := env.be.Put("pairing/"+p.ReqID()+"/reveal", []byte(fake)); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ { // second poll must keep reporting the error
		rr, out := call(t, status, http.MethodGet, "/api/sync/pairing/status?req_id="+p.ReqID(), nil)
		if rr.Code != http.StatusOK || out["state"] != "error" {
			t.Fatalf(`status: want 200 {"state":"error"}, got %d %v`, rr.Code, out)
		}
		if msg, _ := out["error"].(string); !strings.Contains(msg, "commitment mismatch") {
			t.Fatalf("error should name the commitment mismatch, got %v", out)
		}
	}
	// A dead approval cannot be confirmed.
	if rr, _ := call(t, confirmPairingHandler(env.st), http.MethodPost, "/api/sync/pairing/confirm", reqBody(p.ReqID())); rr.Code != http.StatusConflict {
		t.Fatalf("confirm after error: want 409, got %d", rr.Code)
	}
	if ok, _ := env.be.Head("pairing/" + p.ReqID() + "/sealed"); ok {
		t.Fatal("an errored approval must never seal")
	}
}

// TestPairingCORSMutatingAllowlist verifies the new POST endpoints sit behind
// the existing mutating-origin allowlist: a random web page's preflight gets
// no ACAO, extension origins and the daemon's own pages get an echo.
func TestPairingCORSMutatingAllowlist(t *testing.T) {
	h := withCORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	preflight := func(path, origin string) string {
		req := httptest.NewRequest(http.MethodOptions, "http://"+addr+path, nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", http.MethodPost)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr.Header().Get("Access-Control-Allow-Origin")
	}
	for _, path := range []string{
		"/api/sync/pairing/approve",
		"/api/sync/pairing/confirm",
		"/api/sync/pairing/cancel",
	} {
		if got := preflight(path, "https://evil.example"); got != "" {
			t.Fatalf("%s: a web page's POST preflight must not be approved, got ACAO %q", path, got)
		}
		if origin := "http://" + addr; preflight(path, origin) != origin {
			t.Fatalf("%s: the daemon's own pages should be allowlisted", path)
		}
	}
}

// The load-bearing CSRF check: a "simple" cross-origin POST (text/plain → no
// preflight) must be REJECTED before the handler runs. CORS headers alone only
// stop the browser from reading the response; the side effect would otherwise
// already have happened.
func TestPairingCSRFBlocksExecution(t *testing.T) {
	var ran bool
	h := withCORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { ran = true }))

	post := func(origin, contentType string) (int, bool) {
		ran = false
		req := httptest.NewRequest(http.MethodPost, "http://"+addr+"/api/sync/pairing/confirm",
			strings.NewReader(`{"req_id":"00000000"}`))
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		req.Header.Set("Content-Type", contentType)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr.Code, ran
	}

	// Evil web origin, simple request: blocked, handler never runs.
	if code, ran := post("https://evil.example", "text/plain"); code != http.StatusForbidden || ran {
		t.Fatalf("cross-origin simple POST: want 403 and handler not run, got code=%d ran=%v", code, ran)
	}
	// Evil web origin, json (would-be preflighted): also blocked.
	if code, ran := post("https://evil.example", "application/json"); code != http.StatusForbidden || ran {
		t.Fatalf("cross-origin json POST: want 403 and handler not run, got code=%d ran=%v", code, ran)
	}
	// The daemon's own pages: allowed through.
	if code, ran := post("http://"+addr, "application/json"); code == http.StatusForbidden || !ran {
		t.Fatalf("same-origin POST: want handler to run, got code=%d ran=%v", code, ran)
	}
	// A native client (no Origin header): allowed through.
	if code, ran := post("", "application/json"); code == http.StatusForbidden || !ran {
		t.Fatalf("no-origin POST: want handler to run, got code=%d ran=%v", code, ran)
	}
}

// The pairing control plane must not be served to arbitrary web origins even on
// GET — a disallowed cross-origin read is rejected, and no ACAO:* is leaked.
func TestPairingGETNotWebReadable(t *testing.T) {
	var ran bool
	h := withCORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { ran = true }))
	ran = false
	req := httptest.NewRequest(http.MethodGet, "http://"+addr+"/api/sync/pairing", nil)
	req.Header.Set("Origin", "https://evil.example")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden || ran {
		t.Fatalf("cross-origin GET of pairing list: want 403 and handler not run, got code=%d ran=%v", rr.Code, ran)
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got == "*" {
		t.Fatalf("pairing control plane must not be ACAO:* readable, got %q", got)
	}
}
