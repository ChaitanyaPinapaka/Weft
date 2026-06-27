package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"weft/internal/event"
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
	h := trashHandler(v, ix, newTrashTombstones())

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

// TestSaveHandlerIfMatch exercises the R1 optimistic-concurrency flow: /raw
// hands out an ETag, saveHandler accepts a matching If-Match, rejects a stale
// one with 412 (no overwrite), and an unconditional save (no If-Match) wins.
func TestSaveHandlerIfMatch(t *testing.T) {
	v, ix := surfaceFixture(t)
	save := saveHandler(v, ix, nil, newTrashTombstones())
	raw := rawHandler(v)

	post := func(path, ifMatch, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/note/"+path, strings.NewReader(body))
		req.SetPathValue("path", path)
		if ifMatch != "" {
			req.Header.Set("If-Match", ifMatch)
		}
		rr := httptest.NewRecorder()
		save(rr, req)
		return rr
	}
	getETag := func(path string) string {
		req := httptest.NewRequest(http.MethodGet, "/raw/"+path, nil)
		req.SetPathValue("path", path)
		rr := httptest.NewRecorder()
		raw(rr, req)
		return rr.Header().Get("ETag")
	}

	// First conditional save matches the on-disk baseline.
	et := getETag("focus.html")
	if et == "" {
		t.Fatal("rawHandler must return an ETag")
	}
	if rr := post("focus.html", et, `<article><h1>Focus</h1><p>v2</p></article>`); rr.Code != http.StatusNoContent {
		t.Fatalf("matching If-Match: want 204, got %d (%s)", rr.Code, rr.Body.String())
	}

	// The old ETag is now stale — a save with it must be refused, not applied.
	rr := post("focus.html", et, `<article><h1>Focus</h1><p>v3-should-be-rejected</p></article>`)
	if rr.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match: want 412, got %d", rr.Code)
	}
	if got, _ := v.Read("focus.html"); strings.Contains(string(got), "v3-should-be-rejected") {
		t.Fatal("a 412 must not write the note")
	}

	// The new ETag (from the response or a fresh /raw) is accepted again.
	if rr := post("focus.html", getETag("focus.html"), `<article><h1>Focus</h1><p>v3</p></article>`); rr.Code != http.StatusNoContent {
		t.Fatalf("refreshed If-Match: want 204, got %d", rr.Code)
	}
	// No If-Match = unconditional overwrite (the "keep mine" path) always wins.
	if rr := post("focus.html", "", `<article><h1>Focus</h1><p>v4</p></article>`); rr.Code != http.StatusNoContent {
		t.Fatalf("unconditional save: want 204, got %d", rr.Code)
	}
	if got, _ := v.Read("focus.html"); !strings.Contains(string(got), "v4") {
		t.Fatal("unconditional save should have written v4")
	}
}

// TestDailyHandlerDateParam: GET /api/daily?date=YYYY-MM-DD creates/returns that
// day's daily; a malformed date is a 400 (not silently coerced); no date param
// falls back to today.
func TestDailyHandlerDateParam(t *testing.T) {
	v, ix := surfaceFixture(t)
	h := dailyHandler(v, ix, nil)

	get := func(query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/daily"+query, nil)
		rr := httptest.NewRecorder()
		h(rr, req)
		return rr
	}
	var resp struct {
		Path string `json:"path"`
	}

	// Explicit past date is created and returned.
	rr := get("?date=2026-01-15")
	if rr.Code != http.StatusOK {
		t.Fatalf("date param: want 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Path != "daily/2026-01-15.html" {
		t.Fatalf("path = %q, want daily/2026-01-15.html", resp.Path)
	}
	if !v.Exists("daily/2026-01-15.html") {
		t.Fatal("daily for the requested date was not created")
	}

	// Malformed date is rejected, not coerced to today.
	if rr := get("?date=15-01-2026"); rr.Code != http.StatusBadRequest {
		t.Fatalf("malformed date: want 400, got %d", rr.Code)
	}

	// No date param falls back to today.
	today := time.Now().Format("2006-01-02")
	rr = get("")
	if rr.Code != http.StatusOK {
		t.Fatalf("no date: want 200, got %d", rr.Code)
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Path != "daily/"+today+".html" {
		t.Fatalf("no date path = %q, want daily/%s.html", resp.Path, today)
	}
}

// TestExtractTasks: parse TipTap task markup (simple, wrapped, nested, none).
func TestExtractTasks(t *testing.T) {
	equal := func(a, b []index.TaskItem) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}
	cases := []struct {
		name string
		html string
		want []index.TaskItem
	}{
		{
			name: "simple unchecked + checked",
			html: `<article><ul data-type="taskList">` +
				`<li data-type="taskItem" data-checked="false">Buy milk</li>` +
				`<li data-type="taskItem" data-checked="true">Ship it</li></ul></article>`,
			want: []index.TaskItem{{Text: "Buy milk"}, {Text: "Ship it", Checked: true}},
		},
		{
			name: "tiptap label/input/div wrapper",
			html: `<ul data-type="taskList"><li data-type="taskItem" data-checked="false">` +
				`<label><input type="checkbox"><span></span></label><div><p>Write tests</p></div></li></ul>`,
			want: []index.TaskItem{{Text: "Write tests"}},
		},
		{
			name: "nested subtask: parent text excludes child; both captured",
			html: `<ul data-type="taskList"><li data-type="taskItem" data-checked="false"><div><p>Parent</p></div>` +
				`<ul data-type="taskList"><li data-type="taskItem" data-checked="true"><div><p>Child</p></div></li></ul>` +
				`</li></ul>`,
			want: []index.TaskItem{{Text: "Parent"}, {Text: "Child", Checked: true}},
		},
		{
			name: "no tasks",
			html: `<article><p>prose</p><ul><li>plain item</li></ul></article>`,
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractTasks([]byte(tc.html)); !equal(got, tc.want) {
				t.Fatalf("extractTasks =\n  %+v\nwant\n  %+v", got, tc.want)
			}
		})
	}
}

// TestTasksAPI: saving a note with mixed tasks makes GET /api/tasks return only
// the unchecked ones, in document order, with the note title joined.
func TestTasksAPI(t *testing.T) {
	v, ix := surfaceFixture(t)
	save := saveHandler(v, ix, nil, newTrashTombstones())
	body := `<article><h1>Chores</h1><ul data-type="taskList">` +
		`<li data-type="taskItem" data-checked="false">Open one</li>` +
		`<li data-type="taskItem" data-checked="true">Closed</li>` +
		`<li data-type="taskItem" data-checked="false">Open two</li></ul></article>`
	req := httptest.NewRequest(http.MethodPost, "/api/note/chores.html", strings.NewReader(body))
	req.SetPathValue("path", "chores.html")
	rr := httptest.NewRecorder()
	save(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("save: want 204, got %d (%s)", rr.Code, rr.Body.String())
	}

	h := tasksHandler(ix)
	req = httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	rr = httptest.NewRecorder()
	h(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("tasks: want 200, got %d", rr.Code)
	}
	var got []index.Task
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 open tasks, got %d (%+v)", len(got), got)
	}
	if got[0].Text != "Open one" || got[1].Text != "Open two" {
		t.Fatalf("order/text wrong: %+v", got)
	}
	if got[0].NoteTitle != "Chores" {
		t.Fatalf("note title not joined: %q", got[0].NoteTitle)
	}
}

// TestSaveReindexesTasks: checking a task off (data-checked true) via the normal
// save path must drop it from the open-tasks index — the loop the viewer toggle
// relies on.
func TestSaveReindexesTasks(t *testing.T) {
	v, ix := surfaceFixture(t)
	save := saveHandler(v, ix, nil, newTrashTombstones())
	raw := rawHandler(v)
	post := func(ifMatch, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/note/t.html", strings.NewReader(body))
		req.SetPathValue("path", "t.html")
		if ifMatch != "" {
			req.Header.Set("If-Match", ifMatch)
		}
		rr := httptest.NewRecorder()
		save(rr, req)
		return rr
	}
	etag := func() string {
		req := httptest.NewRequest(http.MethodGet, "/raw/t.html", nil)
		req.SetPathValue("path", "t.html")
		rr := httptest.NewRecorder()
		raw(rr, req)
		return rr.Header().Get("ETag")
	}

	open := `<article><h1>T</h1><ul data-type="taskList"><li data-type="taskItem" data-checked="false">Do it</li></ul></article>`
	if rr := post("", open); rr.Code != http.StatusNoContent {
		t.Fatalf("save open: want 204, got %d (%s)", rr.Code, rr.Body.String())
	}
	if all, _ := ix.AllTasks(); len(all) != 1 || all[0].Text != "Do it" {
		t.Fatalf("after open save: want 1 open task, got %+v", all)
	}

	done := `<article><h1>T</h1><ul data-type="taskList"><li data-type="taskItem" data-checked="true">Do it</li></ul></article>`
	if rr := post(etag(), done); rr.Code != http.StatusNoContent {
		t.Fatalf("save done: want 204, got %d (%s)", rr.Code, rr.Body.String())
	}
	if all, _ := ix.AllTasks(); len(all) != 0 {
		t.Fatalf("after checking off: want 0 open tasks, got %+v", all)
	}
}

// TestSurfaceClickRecording: a valid brain-panel click strengthens the learned
// edge; self-clicks and missing endpoints are rejected.
func TestSurfaceClickRecording(t *testing.T) {
	_, ix := surfaceFixture(t)
	h := surfaceClickHandler(ix)
	post := func(payload string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/surface/click", strings.NewReader(payload))
		rr := httptest.NewRecorder()
		h(rr, req)
		return rr
	}
	if rr := post(`{"from":"focus.html","to":"orphan.html"}`); rr.Code != http.StatusNoContent {
		t.Fatalf("click: want 204, got %d (%s)", rr.Code, rr.Body.String())
	}
	if w := ix.GetLearnedWeight("focus.html", "orphan.html"); w <= 1.0 {
		t.Fatalf("click should strengthen edge, weight=%v", w)
	}
	if rr := post(`{"from":"a.html","to":"a.html"}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("self-click: want 400, got %d", rr.Code)
	}
	if rr := post(`{"from":"a.html"}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("missing to: want 400, got %d", rr.Code)
	}
}

// TestRunnableNoteHandler: only meta-flagged notes serve runnable (scripts kept,
// CSP set); others 403. Defense-in-depth strips nested frames but keeps scripts.
func TestRunnableNoteHandler(t *testing.T) {
	v, err := vault.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write := func(rel, body string) {
		if err := v.Write(rel, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	write("app.html", `<!DOCTYPE html><html><head><meta name="weft-runnable" content="true"><title>App</title></head>`+
		`<body><article><h1>App</h1><script>window.x=1</script>`+
		`<iframe src="https://evil.example"></iframe><form><input></form></article></body></html>`)
	write("plain.html", `<article><h1>Plain</h1><p>hi</p></article>`)

	h := runnableNoteHandler(v)
	call := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/note-runnable/"+path, nil)
		req.SetPathValue("path", path)
		rr := httptest.NewRecorder()
		h(rr, req)
		return rr
	}

	rr := call("app.html")
	if rr.Code != http.StatusOK {
		t.Fatalf("runnable: want 200, got %d", rr.Code)
	}
	if rr.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("runnable response must set a CSP")
	}
	body := rr.Body.String()
	if !strings.Contains(body, "window.x=1") {
		t.Fatal("script must be kept for a runnable note")
	}
	if strings.Contains(body, "<iframe") {
		t.Fatal("nested iframe must be stripped (defense in depth)")
	}
	if !strings.Contains(body, "<form") {
		t.Fatal("form should be kept (sandbox + CSP neutralize it)")
	}

	if rr := call("plain.html"); rr.Code != http.StatusForbidden {
		t.Fatalf("non-runnable note: want 403, got %d", rr.Code)
	}
	if rr := call("nope.html"); rr.Code != http.StatusNotFound {
		t.Fatalf("missing note: want 404, got %d", rr.Code)
	}
}

// TestNewNoteHandler: quick-create slugs the title, writes an <h1> note, and
// collision-suffixes; an empty title is rejected.
func TestNewNoteHandler(t *testing.T) {
	v, ix := surfaceFixture(t)
	h := newNoteHandler(v, ix, nil)
	create := func(query string) (int, string) {
		req := httptest.NewRequest(http.MethodPost, "/api/note/new?"+query, nil)
		rr := httptest.NewRecorder()
		h(rr, req)
		var out struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return rr.Code, out.Path
	}

	code, p := create("title=My%20New%20Idea")
	if code != http.StatusOK || p != "my-new-idea.html" {
		t.Fatalf("create: code=%d path=%q", code, p)
	}
	if body, _ := v.Read(p); !strings.Contains(string(body), "<h1>My New Idea</h1>") {
		t.Fatalf("title not in body: %s", body)
	}
	// Same title again collision-suffixes rather than overwriting.
	if _, p2 := create("title=My%20New%20Idea"); p2 != "my-new-idea-2.html" {
		t.Fatalf("collision suffix: got %q", p2)
	}
	if code, _ := create("title="); code != http.StatusBadRequest {
		t.Fatalf("empty title: want 400, got %d", code)
	}
}

// TestCaptureEmitsEvent: a quick-capture lands in the datalake as a
// capture.created event whose content-addressed payload holds the text + path.
func TestCaptureEmitsEvent(t *testing.T) {
	v, err := vault.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ix, err := index.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	st := event.NewStore(v, "test-device")
	h := captureHandler(v, ix, nil, newAmbientHub(), st)

	req := httptest.NewRequest(http.MethodPost, "/api/capture", strings.NewReader(`{"text":"remember the milk"}`))
	rr := httptest.NewRecorder()
	h(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("capture: want 200, got %d (%s)", rr.Code, rr.Body.String())
	}

	var found *event.Envelope
	if err := st.Replay("", func(e event.Envelope) error {
		if e.Source == "capture" && e.Kind == "capture.created" {
			ec := e
			found = &ec
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if found == nil {
		t.Fatal("capture must emit a capture.created event into the lake")
	}
	blob, err := st.GetBlob(found.PayloadRef)
	if err != nil {
		t.Fatalf("capture event payload not durably stored: %v", err)
	}
	var p struct{ Text, Path string }
	if err := json.Unmarshal(blob, &p); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if p.Text != "remember the milk" || p.Path == "" {
		t.Fatalf("capture payload wrong: %+v", p)
	}
}

// TestIngestHandler: a POST envelope durably records an event + content-addressed
// payload, returns the eid; missing source/kind is a 400.
func TestIngestHandler(t *testing.T) {
	v, err := vault.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st := event.NewStore(v, "test-device")
	h := ingestHandler(st)
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/ingest", strings.NewReader(body))
		rr := httptest.NewRecorder()
		h(rr, req)
		return rr
	}

	rr := post(`{"source":"gmail","kind":"order.shipped","dedup_key":"ups-123","payload":"{\"carrier\":\"UPS\"}"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("ingest: want 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	var resp struct {
		EID        string `json:"eid"`
		PayloadRef string `json:"payload_ref"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.EID == "" {
		t.Fatal("ingest must return an eid")
	}
	if resp.PayloadRef != event.Digest([]byte(`{"carrier":"UPS"}`)) {
		t.Fatalf("payload_ref must be the payload digest, got %q", resp.PayloadRef)
	}
	if blob, err := st.GetBlob(resp.PayloadRef); err != nil || string(blob) != `{"carrier":"UPS"}` {
		t.Fatalf("payload not durably stored: %v", err)
	}

	if rr := post(`{"kind":"x"}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("missing source: want 400, got %d", rr.Code)
	}
}

// TestTrashTombstoneBlocksResurrection: a save to a just-trashed path must 409
// (R5) so a queued autosave/beacon can't recreate a removed note.
func TestTrashTombstoneBlocksResurrection(t *testing.T) {
	v, ix := surfaceFixture(t)
	tomb := newTrashTombstones()
	trash := trashHandler(v, ix, tomb)
	save := saveHandler(v, ix, nil, tomb)

	del := httptest.NewRequest(http.MethodDelete, "/api/note/focus.html", nil)
	del.SetPathValue("path", "focus.html")
	if rr := httptest.NewRecorder(); true {
		trash(rr, del)
		if rr.Code != http.StatusOK {
			t.Fatalf("trash: want 200, got %d", rr.Code)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/api/note/focus.html", strings.NewReader(`<article><h1>Focus</h1><p>resurrected</p></article>`))
	req.SetPathValue("path", "focus.html")
	rr := httptest.NewRecorder()
	save(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("save to just-trashed path: want 409, got %d", rr.Code)
	}
	if v.Exists("focus.html") {
		t.Fatal("a tombstoned save must not recreate the note")
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
