package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"weft/internal/index"
	"weft/internal/surface"
	"weft/internal/vault"
)

// surfaceFixture builds a small vault + index for exercising surfaceHandler:
// linker.html → focus.html (a backlink), plus an orphan and two session notes.
func surfaceFixture(t *testing.T) (*vault.Vault, *index.Index) {
	t.Helper()
	v, err := vault.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write := func(rel, body string) {
		if err := v.Write(rel, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	write("focus.html", `<article><h1>Focus</h1><p>focus note</p></article>`)
	write("linker.html", `<article><h1>Linker</h1><p>see <a href="focus.html">focus</a></p></article>`)
	write("orphan.html", `<article><h1>Orphan</h1><p>unrelated quarterly tax filing</p></article>`)
	write("earlier.html", `<article><h1>Earlier</h1><p>touched earlier this session</p></article>`)
	write("stale.html", `<article><h1>Stale</h1><p>from a previous session</p></article>`)

	ix, err := index.Open(v.Root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ix.Close() })
	notes, _ := v.List()
	for _, n := range notes {
		content, _ := v.Read(n.Path)
		if err := ix.Upsert(index.Note{
			Path: n.Path, Title: n.Name, Body: string(content),
			Links: index.ParseLinks(content), ModTime: n.ModTime, Size: n.Size,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return v, ix
}

type surfaceResp struct {
	Current   string           `json:"current"`
	Backlinks []string         `json:"backlinks"`
	Scored    []surface.Scored `json:"scored"`
	Trail     []string         `json:"trail"`
}

// callSurface drives surfaceHandler directly with embeddings OFF (emb=nil) and
// the default param store.
func callSurface(t *testing.T, v *vault.Vault, ix *index.Index, path string) surfaceResp {
	t.Helper()
	h := surfaceHandler(v, ix, nil, newParamStore(ix))
	req := httptest.NewRequest(http.MethodGet, "/api/surface/"+path, nil)
	req.SetPathValue("path", path)
	rr := httptest.NewRecorder()
	h(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var resp surfaceResp
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

// TestSurfaceHandlerBacklinkEmbeddingsOff verifies the engine wiring with
// embeddings disabled: a backlinked note surfaces via spreading (Spread>0,
// "backlink" reason), the focus is excluded, and NO semantic reason ever fires
// (all cosines are 0 without an embedder).
func TestSurfaceHandlerBacklinkEmbeddingsOff(t *testing.T) {
	v, ix := surfaceFixture(t)
	resp := callSurface(t, v, ix, "focus.html")

	var linker *surface.Scored
	for i := range resp.Scored {
		sc := &resp.Scored[i]
		if sc.Path == "focus.html" {
			t.Fatal("focus must be excluded from its own surface results")
		}
		if sc.Path == "linker.html" {
			linker = sc
		}
		for _, r := range sc.Reasons {
			if r == "semantic" {
				t.Fatalf("embeddings off: no semantic reason expected, got one on %s", sc.Path)
			}
		}
	}
	if linker == nil || linker.Spread <= 0 {
		t.Fatalf("linker should surface with Spread>0 from the backlink, got %+v", linker)
	}
	hasBacklink := false
	for _, r := range linker.Reasons {
		if r == "backlink" {
			hasBacklink = true
		}
	}
	if !hasBacklink {
		t.Fatalf("linker should carry a backlink reason, got %v", linker.Reasons)
	}

	found := false
	for _, b := range resp.Backlinks {
		if b == "linker.html" {
			found = true
		}
	}
	if !found {
		t.Fatalf("backlinks should list linker.html, got %v", resp.Backlinks)
	}
}

// TestParamStorePersistAndClamp verifies the live-tuning store: a patch is
// merged + clamped, persisted to the index, and reloaded by a fresh store;
// reset restores defaults.
func TestParamStorePersistAndClamp(t *testing.T) {
	_, ix := surfaceFixture(t)
	ps := newParamStore(ix)

	ss := 9.0
	tooBig := 999.0
	got := ps.apply(paramPatch{SpreadScale: &ss, WBacklink: &tooBig})
	if got.SpreadScale != 9.0 {
		t.Fatalf("spread_scale should be 9, got %v", got.SpreadScale)
	}
	if got.WBacklink != 1.0 { // clamped from 999 to the [0,1] max
		t.Fatalf("w_backlink should clamp to 1.0, got %v", got.WBacklink)
	}

	// A fresh store must reload the persisted value from the index.
	if reloaded := newParamStore(ix).get(); reloaded.SpreadScale != 9.0 {
		t.Fatalf("persisted spread_scale should reload as 9, got %v", reloaded.SpreadScale)
	}

	// Reset restores defaults.
	if d := ps.apply(paramPatch{Reset: true}); d.SpreadScale != surface.DefaultParams().SpreadScale {
		t.Fatalf("reset should restore default spread_scale, got %v", d.SpreadScale)
	}
}

// TestSurfaceHandlerSessionGapWalk verifies the session reconstruction: a note
// touched within the 30-min gap joins the trail; one touched an hour ago (past
// the gap) does not. The focus is always trail[0].
func TestSurfaceHandlerSessionGapWalk(t *testing.T) {
	v, ix := surfaceFixture(t)
	now := time.Now().Unix()
	_ = ix.LogAccess("earlier.html", now-60)  // 1 min ago — in session
	_ = ix.LogAccess("stale.html", now-60*60) // 1 hour ago — past the 30-min gap

	resp := callSurface(t, v, ix, "focus.html")
	if len(resp.Trail) == 0 || resp.Trail[0] != "focus.html" {
		t.Fatalf("trail[0] must be the focus, got %v", resp.Trail)
	}
	inTrail := map[string]bool{}
	for _, p := range resp.Trail {
		inTrail[p] = true
	}
	if !inTrail["earlier.html"] {
		t.Fatalf("an in-session note must join the trail, got %v", resp.Trail)
	}
	if inTrail["stale.html"] {
		t.Fatalf("a note past the session gap must NOT join the trail, got %v", resp.Trail)
	}
}

// TestSurfaceHandlerSelfSourceNoBoost is the lens-2 regression at the HTTP
// layer: a note that is itself an earlier-session source must not receive a
// fabricated semantic boost from its cosine-1.0 self-edge. With embeddings off
// the cleanest assertion is that the in-session note never carries a "semantic"
// reason (the self-edge, if not skipped, would be the only way one could).
func TestSurfaceHandlerSelfSourceNoBoost(t *testing.T) {
	v, ix := surfaceFixture(t)
	now := time.Now().Unix()
	_ = ix.LogAccess("earlier.html", now-60) // earlier.html is both a source and a candidate

	resp := callSurface(t, v, ix, "focus.html")
	for i := range resp.Scored {
		sc := &resp.Scored[i]
		if sc.Path != "earlier.html" {
			continue
		}
		for _, r := range sc.Reasons {
			if r == "semantic" {
				t.Fatalf("earlier.html must not get a self-edge semantic boost, reasons=%v", sc.Reasons)
			}
		}
	}
}

// TestTrashHandler exercises DELETE /api/note/{path}: the note's file moves to
// .trash/ (never hard-deleted), its index rows go away (both backlink
// directions), and the handler 404s on missing notes and 400s on traversal.
func TestTrashHandler(t *testing.T) {
	v, ix := surfaceFixture(t)
	h := trashHandler(v, ix)

	call := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodDelete, "/api/note/"+path, nil)
		req.SetPathValue("path", path)
		rr := httptest.NewRecorder()
		h(rr, req)
		return rr
	}

	// Suffix-less path mirrors noteHandler/saveHandler: ".html" is appended.
	rr := call("focus")
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["trashed"] != "focus.html" {
		t.Fatalf(`want {"trashed":"focus.html"}, got %v`, resp)
	}
	if v.Exists("focus.html") {
		t.Fatal("focus.html should be gone from the live vault")
	}
	if _, err := os.Stat(filepath.Join(v.Root, ".trash", "focus.html")); err != nil {
		t.Fatalf("bytes must survive in .trash: %v", err)
	}
	// linker.html -> focus.html backlink rows must be gone (incoming direction).
	if back, _ := ix.BacklinksTo("focus.html"); len(back) != 0 {
		t.Fatalf("backlinks to trashed note should be cleared, got %v", back)
	}

	if rr := call("ghost.html"); rr.Code != http.StatusNotFound {
		t.Fatalf("missing note: want 404, got %d", rr.Code)
	}
	if rr := call("../evil.html"); rr.Code != http.StatusBadRequest {
		t.Fatalf("traversal: want 400, got %d", rr.Code)
	}
}

// TestIndexAllPrunesGhostRows: the reindex must sweep index rows whose file is
// gone from the vault — the heal trashHandler's best-effort ix.Remove leans
// on. A ghost row (left behind when a trash's index cleanup failed) must stop
// appearing in search and backlinks after the next indexAll.
func TestIndexAllPrunesGhostRows(t *testing.T) {
	v, ix := surfaceFixture(t)
	ghost := `<p>ghost body links to <a href="focus.html">focus</a></p>`
	if err := ix.Upsert(index.Note{
		Path: "ghost.html", Title: "Ghost", Body: ghost,
		Links: index.ParseLinks([]byte(ghost)), ModTime: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	if err := indexAll(v, ix, nil); err != nil {
		t.Fatal(err)
	}

	hits, err := ix.Search("ghost", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if h.Path == "ghost.html" {
			t.Fatalf("ghost.html should be pruned from search, got %+v", hits)
		}
	}
	back, err := ix.BacklinksTo("focus.html")
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 1 || back[0] != "linker.html" {
		t.Fatalf("after the sweep only the live backlink should remain, got %v", back)
	}
	// Live notes must survive the sweep.
	if hits, _ := ix.Search("focus", 10); len(hits) == 0 {
		t.Fatal("live notes must survive the orphan sweep")
	}
}

// TestWithCORSMutatingAllowlist locks the cross-origin posture: reads stay
// wide open (`*`), but DELETE/POST — and their preflights — only get an
// Access-Control-Allow-Origin echo for extension origins and the daemon's own
// pages. A random web page's DELETE preflight gets no ACAO, so the browser
// blocks a cross-origin vault trashing.
func TestWithCORSMutatingAllowlist(t *testing.T) {
	h := withCORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	acao := func(method, origin, reqMethod string) string {
		req := httptest.NewRequest(method, "http://"+addr+"/api/note/x.html", nil)
		req.Header.Set("Origin", origin)
		if reqMethod != "" {
			req.Header.Set("Access-Control-Request-Method", reqMethod)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr.Header().Get("Access-Control-Allow-Origin")
	}

	if got := acao(http.MethodGet, "https://evil.example", ""); got != "*" {
		t.Fatalf("cross-origin reads should stay wide open, got ACAO %q", got)
	}
	if got := acao(http.MethodOptions, "https://evil.example", http.MethodDelete); got != "" {
		t.Fatalf("a web page's DELETE preflight must not be approved, got ACAO %q", got)
	}
	if got := acao(http.MethodDelete, "https://evil.example", ""); got != "" {
		t.Fatalf("a web page's DELETE must get no ACAO, got %q", got)
	}
	for _, origin := range []string{
		"chrome-extension://abcdefgh",
		"moz-extension://0123-4567",
		"http://" + addr,
	} {
		if got := acao(http.MethodOptions, origin, http.MethodDelete); got != origin {
			t.Fatalf("%s DELETE preflight should echo the origin, got ACAO %q", origin, got)
		}
	}
}

// TestMutatingOriginLoopbackAliases guards the save-broke-via-127.0.0.1
// regression: the daemon binds "localhost:7777" but a browser may address it
// under any loopback alias, and all must be allowed to POST/DELETE.
func TestMutatingOriginLoopbackAliases(t *testing.T) {
	allow := []string{
		"http://localhost:7777",
		"http://127.0.0.1:7777",
		"http://[::1]:7777",
		"chrome-extension://abc",
		"moz-extension://abc",
	}
	deny := []string{
		"http://localhost:8080",    // wrong port
		"http://127.0.0.1:9999",    // wrong port
		"https://localhost:7777",   // not http (no https surface)
		"http://evil.example:7777", // non-loopback host
		"http://localhost",         // no port
		"",                         // no origin (handled separately, not "allowed")
	}
	for _, o := range allow {
		if !mutatingOriginAllowed(o) {
			t.Errorf("origin %q should be allowed", o)
		}
	}
	for _, o := range deny {
		if mutatingOriginAllowed(o) {
			t.Errorf("origin %q should be denied", o)
		}
	}
}
