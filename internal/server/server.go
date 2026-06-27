// Package server runs the Weft HTTP daemon on localhost:7777.
package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	gohtml "golang.org/x/net/html"

	"weft/internal/clip"
	"weft/internal/derive"
	"weft/internal/embed"
	"weft/internal/event"
	"weft/internal/graph"
	"weft/internal/index"
	"weft/internal/noteid"
	"weft/internal/surface"
	"weft/internal/vault"
	"weft/internal/wiki"
	"weft/web"
)

const addr = "localhost:7777"

func Run(vaultPath string) error {
	v, err := vault.New(vaultPath)
	if err != nil {
		return fmt.Errorf("vault: %w", err)
	}

	ix, err := index.Open(v.Root)
	if err != nil {
		return fmt.Errorf("index: %w", err)
	}
	defer ix.Close()

	// Embedder is optional. Default builds get the stub (ErrNoORT); we log and
	// keep running so semantic similarity just stays at 0.
	emb, embErr := embed.NewLocal(filepath.Join(v.Root, ".weft", "models"))
	if embErr != nil {
		fmt.Printf("Note  embeddings disabled: %v\n\n", embErr)
		emb = nil
	} else {
		defer emb.Close()
	}

	if err := indexAll(v, ix, emb); err != nil {
		return fmt.Errorf("initial index: %w", err)
	}

	// Bound the append-only access log so the surface hot path's self-join and
	// scans don't grow without ceiling. Two years is far beyond where t^-d
	// decay makes an access matter to base-level activation.
	const accessLogRetention = 2 * 365 * 24 * time.Hour
	_ = ix.PruneAccessLog(time.Now().Add(-accessLogRetention).Unix())

	// Live, runtime-tunable surfacing weights (persisted in the index).
	ps := newParamStore(ix)

	// Proactive surfacing + change notifications: push "what's worth resurfacing
	// now" AND "this note changed on disk" to connected surfaces (macOS app, web
	// editor/viewer) over SSE. No-op until a client subscribes.
	hub := newAmbientHub()

	// If sync is configured, converge through the E2EE bucket in the background;
	// the hub lets it tell open clients which pulled notes to reload.
	startAutoSync(v, ix, emb, hub)

	// The proactive push surface: a calm periodic nudge plus an event-driven
	// push fired the moment new context lands (see recordEvent → pushFromEvent).
	surf := newAmbientSurfacer(hub, v, ix, ps)
	surf.start()
	startLearnedEdgeDecayer(ix)

	// Capture substrate: the append-only event log + content-addressed blob store
	// every ingestion adapter writes into. The derived graph is rebuilt from it
	// (P2); this log is the source of truth ("completeness in the store").
	evStore := event.NewStore(v, deviceID())
	// Catch the derived graph up to the lake on startup (events captured offline,
	// pulled via sync, or POSTed by adapters while the daemon was down).
	if _, err := derive.Catchup(evStore, ix); err != nil {
		fmt.Printf("startup: graph derive catchup: %v\n", err)
	}

	mux := http.NewServeMux()
	// Home is today's daily note in the editor, cursor ready — capture-first,
	// the default state is writing, not browsing (HANDOFF: "Daily note as home").
	// The vault list lives at /notes.
	mux.HandleFunc("GET /{$}", homeHandler(v))
	mux.HandleFunc("GET /notes", listHandler(v))
	mux.HandleFunc("GET /note/{path...}", noteHandler(v, ix))
	mux.HandleFunc("GET /raw/{path...}", rawHandler(v))
	mux.HandleFunc("GET /api/note-runnable/{path...}", runnableNoteHandler(v))
	mux.HandleFunc("GET /edit/{path...}", editRedirectHandler())
	mux.HandleFunc("GET /daily", dailyRedirectHandler(v))
	mux.HandleFunc("GET /api/notes", apiNotesHandler(v))
	mux.HandleFunc("GET /llms.txt", llmsTextHandler(v, ix))
	mux.HandleFunc("GET /api/search", searchHandler(ix))
	// More specific than the {path...} wildcard below, so ServeMux routes the
	// literal "stream" here instead of treating it as a note path.
	mux.HandleFunc("GET /api/surface/stream", surfaceStreamHandler(hub))
	mux.HandleFunc("GET /api/surface/{path...}", surfaceHandler(v, ix, emb, ps))
	mux.HandleFunc("POST /api/surface/click", surfaceClickHandler(ix))
	mux.HandleFunc("POST /api/ingest", ingestHandler(evStore))
	// Short-lived trash tombstones block a queued save from resurrecting a note
	// the user just removed (shared by save + trash).
	tomb := newTrashTombstones()
	mux.HandleFunc("POST /api/note/new", newNoteHandler(v, ix, emb, evStore, surf))
	mux.HandleFunc("POST /api/note/{path...}", saveHandler(v, ix, emb, tomb))
	mux.HandleFunc("DELETE /api/note/{path...}", trashHandler(v, ix, tomb))
	mux.HandleFunc("POST /api/rename", renameHandler(v, ix, emb, hub))
	mux.HandleFunc("GET /api/daily", dailyHandler(v, ix, emb))
	mux.HandleFunc("GET /api/tags", tagsHandler(ix))
	mux.HandleFunc("GET /api/tags/{tag}", tagHandler(ix))
	mux.HandleFunc("GET /api/tasks", tasksHandler(ix))
	mux.HandleFunc("POST /api/clip", clipHandler(v, ix, emb, evStore, surf))
	mux.HandleFunc("POST /api/capture", captureHandler(v, ix, emb, hub, evStore, surf))
	mux.HandleFunc("GET /api/graph", graphHandler(v, ix))
	mux.HandleFunc("GET /graph", graphRedirectHandler())
	mux.HandleFunc("GET /api/params", getParamsHandler(ps))
	mux.HandleFunc("POST /api/params", postParamsHandler(ps))
	// Device pairing (the enrolled/responder side of `weft sync pair`). The
	// single in-flight approval lives in pairs; confirm — never approve — is
	// what seals the vault key for the new device.
	pairs := newPairingStore()
	mux.HandleFunc("GET /api/sync/pairing", listPairingHandler(v))
	mux.HandleFunc("POST /api/sync/pairing/approve", approvePairingHandler(v, pairs))
	mux.HandleFunc("GET /api/sync/pairing/status", pairingStatusHandler(pairs))
	mux.HandleFunc("POST /api/sync/pairing/confirm", confirmPairingHandler(pairs))
	mux.HandleFunc("POST /api/sync/pairing/cancel", cancelPairingHandler(pairs))
	mux.HandleFunc("GET /tune", tuneRedirectHandler())
	mux.Handle("GET /web/", http.StripPrefix("/web/", http.FileServerFS(web.FS)))

	url := "http://" + addr
	dailyURL := url + "/note/" + v.DailyPath(time.Now())
	fmt.Printf("Weft  %s\n", url)
	fmt.Printf("Vault %s\n", v.Root)
	fmt.Printf("Daily %s\n\n", dailyURL)

	go func() {
		time.Sleep(150 * time.Millisecond)
		openBrowser(url)
	}()

	return http.ListenAndServe(addr, withCORS(mux))
}

// withCORS makes /api/* reachable from the browser extension (origins like
// `chrome-extension://…` and `moz-extension://…`) and from any local web
// surface. The daemon is bound to localhost, so most reads stay wide open
// (`*`) — but mutating verbs and the pairing control plane are restricted to
// allowlisted origins.
//
// Crucially, CORS response headers only govern whether a browser may READ a
// response; they do NOT stop a "simple" request (a text/plain or no-cors POST
// needs no preflight) from EXECUTING server-side. So for anything that mutates
// state or exposes the pairing control plane, we ENFORCE the origin here —
// a present-but-disallowed Origin is rejected before the handler runs.
// Non-browser clients (CLI, the native apps via URLSession) send no Origin and
// pass through untouched.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			method := r.Method
			if method == http.MethodOptions {
				// Preflight: judge the method the browser intends to send.
				method = r.Header.Get("Access-Control-Request-Method")
			}
			origin := r.Header.Get("Origin")
			read := method == http.MethodGet || method == http.MethodHead
			// The pairing endpoints (pending request ids + the live SAS) are a
			// control plane, not note content — never open them to arbitrary
			// web origins, even on GET.
			sensitive := strings.HasPrefix(r.URL.Path, "/api/sync/pairing")
			allowed := origin != "" && mutatingOriginAllowed(origin)

			// Block execution for a disallowed cross-origin request that could
			// mutate or read sensitive state. OPTIONS is the preflight itself —
			// don't 403 it; denying ACAO below makes the browser withhold the
			// real request anyway.
			if r.Method != http.MethodOptions && origin != "" && !allowed && (!read || sensitive) {
				http.Error(w, "cross-origin request forbidden", http.StatusForbidden)
				return
			}

			if allowed {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
			} else if read && !sensitive {
				w.Header().Set("Access-Control-Allow-Origin", "*")
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, If-Match")
			w.Header().Set("Access-Control-Expose-Headers", "ETag")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// mutatingOriginAllowed reports whether a browser origin may make mutating
// (POST/DELETE) /api/ calls: the browser-extension surfaces plus the daemon's
// own pages, nothing else. Non-browser clients (CLI, native apps) send no
// Origin header and are untouched by CORS anyway.
func mutatingOriginAllowed(origin string) bool {
	if strings.HasPrefix(origin, "chrome-extension://") ||
		strings.HasPrefix(origin, "moz-extension://") {
		return true
	}
	// The daemon's own pages. A browser may reach localhost:7777 under any
	// loopback alias (localhost, 127.0.0.1, [::1]) — they're the same single-user
	// host, so accept them all on the daemon's port. Matching only the literal
	// "localhost" string 403'd every save when the tab was opened via 127.0.0.1.
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" {
		return false
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil || u.Port() != port {
		return false
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

func listHandler(v *vault.Vault) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		notes, err := v.List()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Templates are vault residents but not browsing fodder — filter them
		// out of the list view per v0.3 spec (still reachable by direct URL).
		filtered := notes[:0]
		for _, n := range notes {
			if !vault.IsTemplate(n.Path) {
				filtered = append(filtered, n)
			}
		}
		notes = filtered
		sort.Slice(notes, func(i, j int) bool {
			return notes[i].ModTime.After(notes[j].ModTime)
		})
		if err := listTmpl.Execute(w, map[string]any{
			"Notes": notes,
			"Root":  v.Root,
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// noteHandler is now the viewer-redirect for /note/{path}. The path also gets
// logged into the access log so the surface ranker can boost co-accessed notes.
// Raw HTML is at /raw/{path} (the viewer's xhr target).
func noteHandler(v *vault.Vault, ix *index.Index) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rel := r.PathValue("path")
		if !strings.HasSuffix(rel, ".html") {
			rel += ".html"
		}
		if !v.Exists(rel) {
			http.Error(w, "note not found", http.StatusNotFound)
			return
		}
		_ = ix.LogAccess(rel, time.Now().Unix())
		http.Redirect(w, r, "/web/viewer.html?path="+rel, http.StatusFound)
	}
}

// trashTombstones remembers recently-trashed note paths so a queued save — an
// unload beacon, or a save racing a trash from another surface — can't recreate
// a note the user just removed (the resurrection class M8/L1/L2). Entries expire
// after trashTombstoneTTL; the set stays tiny since it only holds the last few
// seconds of trashes.
const trashTombstoneTTL = 10 * time.Second

type trashTombstones struct {
	mu sync.Mutex
	at map[string]time.Time
}

func newTrashTombstones() *trashTombstones {
	return &trashTombstones{at: map[string]time.Time{}}
}

func (t *trashTombstones) mark(rel string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.at[rel] = time.Now()
}

// recent reports whether rel was trashed within the TTL, pruning the entry once
// it expires so the path can be legitimately recreated afterward.
func (t *trashTombstones) recent(rel string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	ts, ok := t.at[rel]
	if !ok {
		return false
	}
	if time.Since(ts) > trashTombstoneTTL {
		delete(t.at, rel)
		return false
	}
	return true
}

// etag is a strong validator over a note's exact on-disk bytes. The editor reads
// it on load (from /raw) and sends it back as If-Match on save, so saveHandler
// can reject a write whose baseline has since changed (another tab, device, or a
// capture) instead of blindly clobbering it. Truncated to 16 bytes — ample for
// collision-free change detection on a personal vault.
func etag(content []byte) string {
	sum := sha256.Sum256(content)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

// rawHandler serves the raw .html bytes — what /note/{path} used to do.
// Used by viewer.js to fetch the note content; not in the access log.
func rawHandler(v *vault.Vault) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rel := r.PathValue("path")
		if !strings.HasSuffix(rel, ".html") {
			rel += ".html"
		}
		content, err := v.Read(rel)
		if err != nil {
			http.Error(w, "note not found", http.StatusNotFound)
			return
		}
		w.Header().Set("ETag", etag(content))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(content)
	}
}

// runnableNoteHandler serves a SELF-AUTHORED note's raw HTML — scripts intact —
// for the viewer to load inside a sandboxed iframe. It refuses any note not
// explicitly opted in via <meta name="weft-runnable" content="true"> (403). The
// real isolation boundary is the iframe (sandbox="allow-scripts" WITHOUT
// allow-same-origin → a null origin that can't reach the daemon's pages, cookies
// or storage); the strict CSP set here is defence in depth so even a trusted
// artifact can't phone home (connect-src 'none') or pull external resources. We
// also strip frame/object/embed/base as belt-and-suspenders — none belong in a
// self-contained artifact — while keeping <script> (the whole point) and forms
// (CSP form-action 'none' neutralizes them).
func runnableNoteHandler(v *vault.Vault) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rel := r.PathValue("path")
		if !strings.HasSuffix(rel, ".html") {
			rel += ".html"
		}
		content, err := v.Read(rel)
		if err != nil {
			http.Error(w, "note not found", http.StatusNotFound)
			return
		}
		if !hasRunnableMeta(content) {
			http.Error(w, "note is not marked runnable", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy",
			"default-src 'none'; script-src 'unsafe-inline' 'unsafe-eval'; "+
				"style-src 'unsafe-inline'; img-src data: blob:; font-src data:; "+
				"media-src data: blob:; connect-src 'none'; form-action 'none'; base-uri 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Write(sanitizeRunnable(content))
	}
}

// hasRunnableMeta reports whether the note opted into running its own JS via
// <meta name="weft-runnable" content="true"> anywhere in the document.
func hasRunnableMeta(content []byte) bool {
	doc, err := gohtml.Parse(bytes.NewReader(content))
	if err != nil {
		return false
	}
	found := false
	var walk func(n *gohtml.Node)
	walk = func(n *gohtml.Node) {
		if found {
			return
		}
		if n.Type == gohtml.ElementNode && n.Data == "meta" &&
			strings.EqualFold(attrVal(n, "name"), "weft-runnable") &&
			strings.EqualFold(attrVal(n, "content"), "true") {
			found = true
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return found
}

// sanitizeRunnable strips frame/object/embed/base elements from a runnable note
// (defence in depth on top of the sandbox + CSP) while KEEPING scripts, inline
// handlers, styles and forms — what an interactive artifact actually needs.
func sanitizeRunnable(content []byte) []byte {
	doc, err := gohtml.Parse(bytes.NewReader(content))
	if err != nil {
		return content
	}
	var drop []*gohtml.Node
	var walk func(n *gohtml.Node)
	walk = func(n *gohtml.Node) {
		if n.Type == gohtml.ElementNode {
			switch n.Data {
			case "iframe", "object", "embed", "base":
				drop = append(drop, n)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	for _, n := range drop {
		if n.Parent != nil {
			n.Parent.RemoveChild(n)
		}
	}
	var buf bytes.Buffer
	if err := gohtml.Render(&buf, doc); err != nil {
		return content
	}
	return buf.Bytes()
}

// editRedirectHandler hands /edit/{path} → the editor surface. Doesn't touch
// the access log: opening for edit doesn't count as a read.
func editRedirectHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rel := r.PathValue("path")
		if !strings.HasSuffix(rel, ".html") {
			rel += ".html"
		}
		http.Redirect(w, r, "/web/index.html?path="+rel, http.StatusFound)
	}
}

// dailyDate resolves the target daily date from an optional ?date=YYYY-MM-DD
// query param, defaulting to today. ok=false means the param was present but
// malformed, so the caller should 400 rather than silently land on today.
func dailyDate(r *http.Request) (day time.Time, ok bool) {
	q := r.URL.Query().Get("date")
	if q == "" {
		return time.Now(), true
	}
	t, err := time.ParseInLocation("2006-01-02", q, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// dailyRedirectHandler ensures the requested day's daily exists (today by
// default, or ?date=YYYY-MM-DD; using the template if daily/template.html is
// present) and 302s to /note/{path}.
func dailyRedirectHandler(v *vault.Vault) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		day, ok := dailyDate(r)
		if !ok {
			http.Error(w, "invalid date (want YYYY-MM-DD)", http.StatusBadRequest)
			return
		}
		rel, err := v.EnsureDailyFromTemplate(day)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/note/"+rel, http.StatusFound)
	}
}

// homeHandler lands on today's daily note in the EDITOR (not the viewer):
// capture-first, cursor ready. This is what "Open Weft → land in today" means.
func homeHandler(v *vault.Vault) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rel, err := v.EnsureDailyFromTemplate(time.Now())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/edit/"+rel, http.StatusFound)
	}
}

func tagsHandler(ix *index.Index) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tags, err := ix.AllTags()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tags)
	}
}

// tasksHandler serves every unchecked task across the vault as JSON — the data
// behind the /web/tasks.html open-tasks view.
func tasksHandler(ix *index.Index) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tasks, err := ix.AllTasks()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tasks)
	}
}

// llmsTextHandler serves /llms.txt — the llms.txt-convention discovery manifest
// (Jeremy Howard / Answer.AI) that lets an LLM agent discover what Weft exposes
// and HOW to consume a person's context: the MCP tools, the key HTTP endpoints,
// and the surfacing-over-search model. This is the Agent-Experience "Access +
// Context" discovery seam — generated live so it stays accurate as the vault and
// API evolve.
func llmsTextHandler(v *vault.Vault, ix *index.Index) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		count := 0
		if notes, err := v.List(); err == nil {
			count = len(notes)
		}
		var b strings.Builder
		b.WriteString("# Weft — personal-context vault\n\n")
		b.WriteString("> Weft is a local-first personal knowledge vault whose defining feature is surfacing, not searching: open any note and it returns what your brain would recall right now — backlinks, semantic neighbors, recently co-accessed notes, and \"this day in past years.\" It is built for an LLM agent to consume a person's evolving personal context.\n\n")
		fmt.Fprintf(&b, "This vault currently holds %d notes. Notes are HTML files on disk; nothing is ever deleted (dormancy is a ranking signal, not removal).\n\n", count)
		b.WriteString("## Consume Weft over MCP (preferred)\n\n")
		b.WriteString("Run `weft mcp <vault>`; the server exposes these tools:\n\n")
		b.WriteString("- `list_notes` — every note (path, title, mtime, size)\n")
		b.WriteString("- `read_note` — full HTML of one note\n")
		b.WriteString("- `search_notes` — FTS5 full-text search, BM25-ranked\n")
		b.WriteString("- `surface_note` — the brain panel: explicit backlinks plus activation-ranked associative neighbors. Prefer this for recall — it returns what relates to a note, not just lexical matches.\n")
		b.WriteString("- `write_note` — create or overwrite a note (HTML body)\n\n")
		b.WriteString("## HTTP API (http://localhost:7777)\n\n")
		b.WriteString("- `GET /api/notes` — note list (JSON)\n")
		b.WriteString("- `GET /api/search?q=<query>` — full-text search\n")
		b.WriteString("- `GET /api/surface/{path}` — backlinks + surfaced neighbors for a note\n")
		b.WriteString("- `GET /raw/{path}` — raw note HTML\n")
		b.WriteString("- `GET /api/tasks` — open tasks across the vault\n")
		b.WriteString("- `POST /api/capture` — append a thought to today's daily note\n")
		b.WriteString("- `POST /api/ingest` — append an event to the capture lake (the lossless substrate intake)\n\n")
		b.WriteString("## Model\n\n")
		b.WriteString("- **Surfacing over searching** — recall is cue-driven and associative (an ACT-R memory-activation model), not query-first.\n")
		b.WriteString("- **Completeness in the store, tendedness in the view** — an append-only event lake is the source of truth; HTML notes are tended projections an agent and a human both read.\n")
		b.WriteString("- **Local-first** — one Go binary, your files, optional E2EE bring-your-own-cloud sync.\n")
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Write([]byte(b.String()))
	}
}

// newNoteHandler creates a note from a title (slugified to {slug}.html at the
// vault root, collision-suffixed so it never overwrites) and returns {path}.
// Powers the command palette's quick-create; the title becomes the note's <h1>.
func newNoteHandler(v *vault.Vault, ix *index.Index, emb embed.Embedder, evStore *event.Store, surf *ambientSurfacer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		title := strings.TrimSpace(r.URL.Query().Get("title"))
		if title == "" {
			http.Error(w, "title required", http.StatusBadRequest)
			return
		}
		rel := uniqueNotePath(v, clip.Slug(title))
		esc := template.HTMLEscapeString(title)
		body := "<!DOCTYPE html><html><head><meta charset=\"utf-8\"><title>" + esc +
			"</title></head>\n<body>\n<article>\n<h1>" + esc + "</h1>\n<p></p>\n</article>\n</body></html>\n"
		content := prepareNote(v, rel, []byte(body))
		if err := v.Write(rel, content); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		t, b := extractTitleBody(content)
		mt := time.Now()
		if m, err := v.ModTime(rel); err == nil {
			mt = m
		}
		if err := ix.Upsert(index.Note{Path: rel, Title: t, Body: b, Links: index.ParseLinks(content), ModTime: mt, Size: int64(len(content))}); err != nil {
			fmt.Printf("new note: index upsert %s: %v\n", rel, err)
		}
		updateEmbedding(ix, emb, rel, t+"\n"+b)
		// Creating a note silently becomes a graph Document, behind the scenes.
		if payload, err := json.Marshal(map[string]string{"title": title, "path": rel}); err == nil {
			recordEvent(evStore, ix, surf, event.Envelope{Source: "note", Kind: "note.created"}, payload, rel)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"path": rel})
	}
}

// uniqueNotePath returns {slug}.html at the vault root, suffixing -2, -3, … on
// collision so a quick-create never clobbers an existing note.
func uniqueNotePath(v *vault.Vault, slug string) string {
	base := slug + ".html"
	if !v.Exists(base) {
		return base
	}
	for i := 2; i < 1000; i++ {
		c := fmt.Sprintf("%s-%d.html", slug, i)
		if !v.Exists(c) {
			return c
		}
	}
	return base
}

// ingestHandler is the single entry point every ingestion adapter (email,
// calendar, sensor, on-device, or an integration platform's webhook) POSTs to.
// Body is JSON {source, kind, occurred_at?, dedup_key?, schema?, device?,
// payload?}; payload is an optional raw string stored as a content-addressed
// blob. It returns {eid, payload_ref} only AFTER the event is durable
// (ack-after-durable), so a caller that gets 200 can safely 200 its webhook /
// advance its cursor. The canonical envelope is the whole integration contract:
// adapters become config elsewhere, not Go code here.
func ingestHandler(st *event.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Source     string `json:"source"`
			Kind       string `json:"kind"`
			OccurredAt int64  `json:"occurred_at"`
			DedupKey   string `json:"dedup_key"`
			Schema     string `json:"schema"`
			Device     string `json:"device"`
			Payload    string `json:"payload"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&body); err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
		var payload []byte
		if body.Payload != "" {
			payload = []byte(body.Payload)
		}
		env, err := st.Append(event.Envelope{
			Source:     body.Source,
			Kind:       body.Kind,
			OccurredAt: body.OccurredAt,
			DedupKey:   body.DedupKey,
			Schema:     body.Schema,
			Device:     body.Device,
		}, payload)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"eid": env.EID, "payload_ref": env.PayloadRef})
	}
}

// recordEvent is the one helper every "behind-the-scenes" producer uses: append
// an event to the lake, then fold it into the context graph. Best-effort and
// nil-safe — a save/capture/clip is never blocked or failed by lake bookkeeping;
// failures are logged and heal on the next catch-up. This is how saving anything
// to Weft silently becomes graph.
func recordEvent(evStore *event.Store, ix *index.Index, surf *ambientSurfacer, env event.Envelope, payload []byte, focus string) {
	if evStore == nil {
		return
	}
	if _, err := evStore.Append(env, payload); err != nil {
		fmt.Printf("event: append %s/%s: %v\n", env.Source, env.Kind, err)
		return
	}
	if _, err := derive.Catchup(evStore, ix); err != nil {
		fmt.Printf("event: derive after %s: %v\n", env.Kind, err)
	}
	// Event-driven push: the new context may be worth a proactive nudge. Gated
	// (resurfaced/on-this-day only) and nil-safe, so it's a no-op when quiet.
	surf.pushFromEvent(focus)
}

// deviceID names this device on captured events. Hostname is a stable, adequate
// placeholder until the sync layer's signed device identity is threaded through.
func deviceID() string {
	if h, err := os.Hostname(); err == nil && strings.TrimSpace(h) != "" {
		return h
	}
	return "weft"
}

func tagHandler(ix *index.Index) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tag := strings.ToLower(strings.TrimSpace(r.PathValue("tag")))
		if tag == "" {
			http.Error(w, "empty tag", http.StatusBadRequest)
			return
		}
		paths, err := ix.NotesWithTag(tag)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(paths)
	}
}

// clipHandler accepts `POST /api/clip` with `{url, html, title}` (title is
// optional and used as a slug hint; clip.Clean derives one if absent).
// Browser extension is the primary caller.
func clipHandler(v *vault.Vault, ix *index.Index, emb embed.Embedder, evStore *event.Store, surf *ambientSurfacer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			URL   string `json:"url"`
			HTML  string `json:"html"`
			Title string `json:"title"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 20<<20)).Decode(&body); err != nil {
			http.Error(w, "bad JSON body", http.StatusBadRequest)
			return
		}
		if body.URL == "" || body.HTML == "" {
			http.Error(w, "url and html are required", http.StatusBadRequest)
			return
		}
		cleaned, title, err := clip.Clean([]byte(body.HTML), body.URL)
		if err != nil {
			http.Error(w, "clean: "+err.Error(), http.StatusInternalServerError)
			return
		}
		// Caller-provided title wins over what Clean extracted (the extension
		// has access to the page's DOM <title> directly).
		if body.Title != "" {
			title = body.Title
		}
		rel := uniqueClipPath(v, time.Now(), clip.Slug(title))
		cleaned = prepareNote(v, rel, cleaned) // stamp WeftID + link ids
		if err := v.Write(rel, cleaned); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		t, txt := extractTitleBody(cleaned)
		_ = ix.Upsert(index.Note{
			Path:    rel,
			Title:   t,
			Body:    txt,
			Links:   index.ParseLinks(cleaned),
			ModTime: time.Now(),
			Size:    int64(len(cleaned)),
		})
		updateEmbedding(ix, emb, rel, t+"\n"+txt)
		// Saving a clip silently becomes a graph Document, behind the scenes.
		if payload, err := json.Marshal(map[string]string{"url": body.URL, "title": t, "path": rel}); err == nil {
			recordEvent(evStore, ix, surf, event.Envelope{Source: "clip", Kind: "clip.created"}, payload, rel)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"path": rel})
	}
}

// uniqueClipPath returns the first clips/YYYY-MM-DD-{slug}.html that doesn't
// already exist, suffixing -2, -3 on collision. Mirrors cmd/weft's helper —
// fine to duplicate, the alternative is exporting from cmd/.
func uniqueClipPath(v *vault.Vault, t time.Time, slug string) string {
	base := clip.ClipPath(t, slug)
	if !v.Exists(base) {
		return base
	}
	for i := 2; i < 1000; i++ {
		c := clip.ClipPath(t, fmt.Sprintf("%s-%d", slug, i))
		if !v.Exists(c) {
			return c
		}
	}
	return base
}

// graphHandler returns the node-link graph of the vault. UI consumes this
// via web/graph.js (D3 force layout).
func graphHandler(v *vault.Vault, ix *index.Index) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		g, err := graph.Build(v, ix)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(g)
	}
}

// graphRedirectHandler 302s /graph to the static graph page so the URL is
// clean (matches /daily's pattern).
func graphRedirectHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/web/graph.html", http.StatusFound)
	}
}

// captureHandler accepts `POST /api/capture` with `{text}` and appends to
// today's daily note (creating it if missing). Same engine as `weft capture`.
func captureHandler(v *vault.Vault, ix *index.Index, emb embed.Embedder, hub *ambientHub, evStore *event.Store, surf *ambientSurfacer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
			http.Error(w, "bad JSON body", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(body.Text) == "" {
			http.Error(w, "text is empty", http.StatusBadRequest)
			return
		}
		rel, err := v.AppendCapture(time.Now(), body.Text)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Re-index the daily note so the new bullet is searchable + embedded.
		// Hold the path lock so the read+upsert reflects one consistent version.
		v.Lock(rel)
		if content, err := v.Read(rel); err == nil {
			t, txt := extractTitleBody(content)
			mt := time.Now()
			if m, err := v.ModTime(rel); err == nil {
				mt = m
			}
			if err := ix.Upsert(index.Note{
				Path:    rel,
				Title:   t,
				Body:    txt,
				Links:   index.ParseLinks(content),
				ModTime: mt,
				Size:    int64(len(content)),
			}); err != nil {
				fmt.Printf("capture: index upsert %s: %v (heals on next reindex)\n", rel, err)
			}
			updateEmbedding(ix, emb, rel, t+"\n"+txt)
		}
		v.Unlock(rel)
		// Tell any open editor/reader of this daily note to reload — the capture
		// landed out of band and must not be clobbered by a stale autosave.
		hub.changed(rel)
		// Land the capture in the datalake as an event — the lake's first live
		// producer. Best-effort: a capture is never blocked by lake bookkeeping.
		// Payload (text + target note path) is content-addressed.
		if payload, err := json.Marshal(map[string]string{"text": body.Text, "path": rel}); err == nil {
			recordEvent(evStore, ix, surf, event.Envelope{Source: "capture", Kind: "capture.created"}, payload, rel)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"path": rel})
	}
}

// renameHandler accepts `POST /api/rename` with a JSON body
// `{"from": "old.html", "to": "new.html"}`. Both paths are vault-relative.
// Moves the file, re-indexes both paths, and rewrites any in-vault anchors
// that pointed at the old path.
//
// Why both in the body (not in the URL): Go's ServeMux requires `{...}`
// wildcards at the end of the pattern, which can't express two arbitrary
// vault-relative paths in one route.
func renameHandler(v *vault.Vault, ix *index.Index, emb embed.Embedder, hub *ambientHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			From string `json:"from"`
			To   string `json:"to"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
			http.Error(w, "bad JSON body", http.StatusBadRequest)
			return
		}
		oldRel := strings.TrimSpace(body.From)
		if !strings.HasSuffix(oldRel, ".html") {
			oldRel += ".html"
		}
		newRel := strings.TrimSpace(body.To)
		if !strings.HasSuffix(newRel, ".html") {
			newRel += ".html"
		}
		if newRel == "" || newRel == oldRel {
			http.Error(w, "to must differ from source", http.StatusBadRequest)
			return
		}
		// Lock both paths (stable order) for the whole move so a save/capture to
		// either side can't race the read-write-delete.
		unlock := v.LockTwo(oldRel, newRel)
		defer unlock()

		if !v.Exists(oldRel) {
			http.Error(w, "source not found", http.StatusNotFound)
			return
		}
		if v.Exists(newRel) {
			http.Error(w, "destination exists", http.StatusConflict)
			return
		}

		// Move the file: write to new path, then delete the old. The delete
		// only fires on a successful write, so a failed move leaves the
		// original untouched (the no-data-loss principle).
		content, err := v.Read(oldRel)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := v.Write(newRel, content); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := osRemove(v, oldRel); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Index: delete old, upsert new with current text.
		_ = ix.Delete(oldRel)
		title, text := extractTitleBody(content)
		_ = ix.Upsert(index.Note{
			Path:    newRel,
			Title:   title,
			Body:    text,
			Links:   index.ParseLinks(content),
			ModTime: time.Now(),
			Size:    int64(len(content)),
		})
		updateEmbedding(ix, emb, newRel, title+"\n"+text)

		// Rewrite any anchors across the vault that pointed at oldRel. Each linking
		// note gets its own path lock for the read-modify-write (R2), and a
		// changed event so an open editor of it reloads the new href (R3).
		rewritten := []string{}
		notes, _ := v.List()
		for _, n := range notes {
			if n.Path == newRel {
				continue
			}
			func() {
				v.Lock(n.Path)
				defer v.Unlock(n.Path)
				c, err := v.Read(n.Path)
				if err != nil {
					return
				}
				updated, changed := rewriteHrefInDoc(c, oldRel, newRel)
				if !changed {
					return
				}
				if err := v.Write(n.Path, updated); err != nil {
					return
				}
				t, b := extractTitleBody(updated)
				mt := time.Now()
				if m, err := v.ModTime(n.Path); err == nil {
					mt = m
				}
				_ = ix.Upsert(index.Note{
					Path:    n.Path,
					Title:   t,
					Body:    b,
					Links:   index.ParseLinks(updated),
					ModTime: mt,
					Size:    int64(len(updated)),
				})
				rewritten = append(rewritten, n.Path)
			}()
		}

		// Notify open clients: the note moved, and any note whose links we rewrote.
		hub.changed(oldRel)
		hub.changed(newRel)
		for _, p := range rewritten {
			hub.changed(p)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"to": newRel})
	}
}

// trashHandler is DELETE /api/note/{path}: the user-initiated "remove". The
// file moves into .trash/ (vault.Trash — bytes are never deleted, per the
// never-delete invariant) and its rows leave the index, so the note stops
// listing, searching, and surfacing. Human-only by design: the MCP surface
// exposes no delete tool.
func trashHandler(v *vault.Vault, ix *index.Index, tomb *trashTombstones) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rel := r.PathValue("path")
		if !strings.HasSuffix(rel, ".html") {
			rel += ".html"
		}
		// Mirror osRemove's guard so a traversal attempt gets a 400 here
		// rather than vault.Trash's opaque permission error.
		if strings.Contains(rel, "..") {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}
		v.Lock(rel)
		defer v.Unlock(rel)
		// Tombstone first, inside the lock: a save that was waiting on this lock
		// will see the tombstone and refuse to recreate the note.
		tomb.mark(rel)
		if !v.Exists(rel) {
			http.Error(w, "note not found", http.StatusNotFound)
			return
		}
		if err := v.Trash(rel); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// The vault is authoritative: the file is already in .trash, so a
		// failed index cleanup doesn't fail the request — but it is logged,
		// and indexAll's orphan sweep prunes the rows on the next reindex
		// (startup or post-sync).
		if err := ix.Remove(rel); err != nil {
			fmt.Printf("trash: index remove %s: %v (prunes on next reindex)\n", rel, err)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"trashed": rel})
	}
}

func apiNotesHandler(v *vault.Vault) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		notes, err := v.List()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(notes)
	}
}

func searchHandler(ix *index.Index) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if q == "" {
			http.Error(w, "empty query", http.StatusBadRequest)
			return
		}
		hits, err := ix.Search(q, 20)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(hits)
	}
}

// surfaceHandler returns the brain-panel payload computed by the ACT-R
// activation engine: base-level (recency+frequency from the access log) plus
// spreading activation from the current session's sources (the focus note and
// notes touched earlier this session), over backlink/semantic/co-access edges.
// Also returns the explicit backlinks list, the on_this_day anniversary array,
// and the session "trail" (focus → earlier sources) for the thought-trail UI.
// surfaceClickHandler records that the user followed a surfaced suggestion from
// `from` to `to`, strengthening that learned association edge (the reinforcement
// loop). Fire-and-forget from the client (a sendBeacon during navigation), so a
// best-effort record never blocks the click: we 204 even if the write hiccups.
func surfaceClickHandler(ix *index.Index) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			From string `json:"from"`
			To   string `json:"to"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if body.From == "" || body.To == "" || body.From == body.To {
			http.Error(w, "from and to required and distinct", http.StatusBadRequest)
			return
		}
		if err := ix.RecordEdgeClick(body.From, body.To, time.Now().Unix()); err != nil {
			fmt.Printf("surface click %s→%s: %v\n", body.From, body.To, err)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func surfaceHandler(v *vault.Vault, ix *index.Index, emb embed.Embedder, ps *paramStore) http.HandlerFunc {
	// emb is unused: surfacing reads the embeddings stored in the index, not the
	// live embedder. Kept in the signature for call-site/test symmetry with the
	// other handlers and so a future per-request embed has a home.
	_ = emb
	return func(w http.ResponseWriter, r *http.Request) {
		res, err := computeSurface(v, ix, ps.get(), r.PathValue("path"), time.Now())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"current":     res.Current,
			"backlinks":   res.Backlinks,
			"scored":      res.Scored,
			"on_this_day": res.OnThisDay,
			"trail":       res.Trail, // focus first, then earlier-session sources
		})
	}
}

// surfaceResult is the computed brain-panel payload: the ACT-R activation
// ranking plus the explicit backlinks, the on-this-day anniversaries, and the
// session trail. Produced by computeSurface and consumed by both surfaceHandler
// (HTTP) and the ambient surfacer (SSE push).
type surfaceResult struct {
	Current   string
	Backlinks []string
	Scored    []surface.Scored
	OnThisDay []surface.Candidate
	Trail     []string // focus first, then earlier-session sources
}

// computeSurface runs the surfacing engine for one focus note: it reconstructs
// the current session from the access log, weights attention across the
// sources, builds per-candidate association edges (backlink/semantic/co-access),
// and ranks every note by ACT-R activation. `cur` is the focus path (a trailing
// .html is appended if missing); `now` anchors recency and the noise seed.
func computeSurface(v *vault.Vault, ix *index.Index, p surface.Params, cur string, now time.Time) (surfaceResult, error) {
	if !strings.HasSuffix(cur, ".html") {
		cur += ".html"
	}

	notes, err := v.List()
	if err != nil {
		return surfaceResult{}, err
	}

	nowUnix := now.Unix()

	// (a) Reconstruct the current session by gap-walking recent accesses.
	// The focus is the source at full attention; earlier in-session notes
	// spread with decaying attention. /note/{path} already logs every open.
	since := nowUnix - int64(p.SessionLookback.Seconds())
	recent, _ := ix.RecentAccesses(since) // DESC by ts
	lastTouch := map[string]int64{cur: nowUnix}
	sourcePaths := []string{cur}
	prevTs := nowUnix
	for _, a := range recent {
		if prevTs-a.Ts > int64(p.SessionGap.Seconds()) {
			break // crossed a session boundary
		}
		prevTs = a.Ts
		if a.Path == cur {
			continue
		}
		if _, seen := lastTouch[a.Path]; !seen {
			lastTouch[a.Path] = a.Ts // DESC ⇒ first sighting is most-recent touch
			sourcePaths = append(sourcePaths, a.Path)
		}
	}

	// (b) Attention weights: focus pinned, earlier sources decayed+normalized.
	raw := make([]surface.Source, len(sourcePaths))
	for i, sp := range sourcePaths {
		raw[i] = surface.Source{Path: sp}
	}
	sources := surface.AttentionWeights(raw, nowUnix, lastTouch, cur, p)

	// (c) Per-source association inputs. Embeddings decoded once.
	vecs := map[string][]float32{}
	if all, err := ix.AllEmbeddings(); err == nil {
		for pth, blob := range all {
			if vc, e := embed.Decode(blob); e == nil {
				vecs[pth] = vc
			}
		}
	}
	linkSets := map[string]map[string]bool{}
	coCounts := map[string]map[string]int{}
	for _, src := range sources {
		ls := map[string]bool{}
		if b, e := ix.BacklinksTo(src.Path); e == nil {
			for _, x := range b {
				ls[x] = true
			}
		}
		if f, e := ix.LinksFrom(src.Path); e == nil {
			for _, x := range f {
				ls[x] = true
			}
		}
		linkSets[src.Path] = ls
		cc, _ := ix.CoAccessCount(src.Path, p.SessionGap)
		coCounts[src.Path] = cc
	}

	// (d) Access history for base-level, in one scan.
	hist, _ := ix.AllAccessHistory()

	// (e) Build candidates with per-source edges. Skip the self-source edge
	// (a note that is also a session source has cosine 1.0 to itself and
	// would fabricate a semantic boost). Path-sort for deterministic noise.
	cands := make([]surface.Candidate, 0, len(notes))
	for _, n := range notes {
		edges := map[string]surface.EdgeSet{}
		for _, src := range sources {
			if src.Path == n.Path {
				continue
			}
			sim := 0.0
			if sv, cv := vecs[src.Path], vecs[n.Path]; sv != nil && cv != nil {
				sim = float64(embed.CosineSimilarity(sv, cv))
			}
			edges[src.Path] = surface.EdgeSet{
				Backlink:      linkSets[src.Path][n.Path],
				Similarity:    sim,
				CoAccessCount: coCounts[src.Path][n.Path],
			}
		}
		cands = append(cands, surface.Candidate{
			Path:     n.Path,
			Title:    n.Name,
			ModTime:  n.ModTime,
			Accesses: hist[n.Path],
			Edges:    edges,
		})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].Path < cands[j].Path })

	// (f) Rank by activation.
	noiser := surface.NewNoiser(p.NoiseScale, now.UnixNano(), p.Gaussian)
	scored := surface.Rank(surface.Candidate{Path: cur}, sources, cands, nowUnix, p, noiser, ix.GetLearnedWeight)
	if len(scored) > p.TopN {
		scored = scored[:p.TopN]
	}

	// (g) Explicit backlinks list + on_this_day, both unchanged in shape.
	back, _ := ix.BacklinksTo(cur)
	otd := surface.OnThisDay(cands, now, p)
	filtered := otd[:0]
	for _, c := range otd {
		if c.Path != cur {
			filtered = append(filtered, c)
		}
	}

	return surfaceResult{
		Current:   cur,
		Backlinks: back,
		Scored:    scored,
		OnThisDay: filtered,
		Trail:     sourcePaths,
	}, nil
}

func saveHandler(v *vault.Vault, ix *index.Index, emb embed.Embedder, tomb *trashTombstones) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rel := r.PathValue("path")
		if !strings.HasSuffix(rel, ".html") {
			rel += ".html"
		}
		content, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Expand any raw [[wikilinks]] before persisting. The wiki package
		// operates on body fragments, so we lift the article inner-HTML out,
		// expand, and put it back. If anything fails, fall through with the
		// original bytes — never block a save on link expansion.
		if inner, ok := articleInnerHTML(content); ok {
			notes, _ := v.List()
			paths := make([]string, 0, len(notes))
			for _, n := range notes {
				paths = append(paths, n.Path)
			}
			if expanded, err := wiki.ExpandWikilinks(inner, paths); err == nil {
				if rebuilt, ok := replaceArticleInner(content, expanded); ok {
					content = rebuilt
				}
			}
		}

		// Hold the note's per-path lock across read-existing → write → index so a
		// concurrent save / capture / sync-apply can't interleave and lose data.
		v.Lock(rel)
		defer v.Unlock(rel)

		// Don't let a queued/racing save resurrect a just-trashed note (R5).
		if tomb.recent(rel) {
			http.Error(w, "note was just removed", http.StatusConflict)
			return
		}

		// Optimistic concurrency (R1): if the client sent the baseline it loaded,
		// reject the write when the on-disk bytes have changed since (another tab,
		// device, or a capture), so a stale editor buffer can't silently clobber a
		// newer version. "*" means "only if it exists". The editor surfaces the 412
		// as a reload-or-overwrite choice instead of losing data.
		if ifMatch := r.Header.Get("If-Match"); ifMatch != "" {
			cur, err := v.Read(rel)
			switch {
			case err != nil: // baseline note is gone (e.g. trashed under us)
				http.Error(w, "note no longer exists", http.StatusPreconditionFailed)
				return
			case ifMatch != "*" && etag(cur) != ifMatch:
				w.Header().Set("ETag", etag(cur))
				http.Error(w, "note changed on disk", http.StatusPreconditionFailed)
				return
			}
		}

		content = prepareNote(v, rel, content) // stamp/preserve WeftID + link ids
		if err := v.Write(rel, content); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		title, body := extractTitleBody(content)
		// Stamp the index with the real on-disk mtime (not time.Now()) so the
		// startup/post-sync staleness check can't be fooled by clock skew, and
		// surface the upsert error rather than silently desyncing the index.
		mt := time.Now()
		if m, err := v.ModTime(rel); err == nil {
			mt = m
		}
		if err := ix.Upsert(index.Note{
			Path:    rel,
			Title:   title,
			Body:    body,
			Links:   index.ParseLinks(content),
			ModTime: mt,
			Size:    int64(len(content)),
		}); err != nil {
			fmt.Printf("save: index upsert %s: %v (heals on next reindex)\n", rel, err)
		}
		updateEmbedding(ix, emb, rel, title+"\n"+body)
		// Index task checkboxes (replace-on-save) for the cross-vault tasks view.
		if err := ix.UpsertTasks(rel, extractTasks(content)); err != nil {
			fmt.Printf("save: index task upsert %s: %v (heals on next reindex)\n", rel, err)
		}
		// Return the new baseline so the editor can keep saving without a reload.
		w.Header().Set("ETag", etag(content))
		w.WriteHeader(http.StatusNoContent)
	}
}

func dailyHandler(v *vault.Vault, ix *index.Index, emb embed.Embedder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		day, ok := dailyDate(r)
		if !ok {
			http.Error(w, "invalid date (want YYYY-MM-DD)", http.StatusBadRequest)
			return
		}
		rel, err := v.EnsureDailyFromTemplate(day)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if content, err := v.Read(rel); err == nil {
			// Stamp the daily's WeftID on first touch (the stub has none).
			if out, _, did := noteid.Ensure(content, rel); did {
				if v.Write(rel, out) == nil {
					content = out
				}
			}
			title, body := extractTitleBody(content)
			_ = ix.Upsert(index.Note{
				Path:    rel,
				Title:   title,
				Body:    body,
				Links:   index.ParseLinks(content),
				ModTime: time.Now(),
				Size:    int64(len(content)),
			})
			updateEmbedding(ix, emb, rel, title+"\n"+body)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"path": rel})
	}
}

// indexAll incrementally re-indexes the vault on startup. Skips notes whose
// stored mtime is already up to date (cheap PRAGMA-style mtime check); only
// re-reads, re-parses, and re-embeds the stale ones. It also sweeps index
// rows whose file has left the vault (trashed, renamed, removed externally) —
// the heal that makes trashHandler's and renameHandler's best-effort index
// cleanup safe instead of leaving permanent search/surfacing ghosts.
func indexAll(v *vault.Vault, ix *index.Index, emb embed.Embedder) error {
	notes, err := v.List()
	if err != nil {
		return err
	}
	for _, n := range notes {
		stale, err := ix.Stale(n.Path, n.ModTime, n.Size)
		if err != nil {
			return err
		}
		if !stale {
			continue
		}
		content, err := v.Read(n.Path)
		if err != nil {
			continue
		}
		// Backfill a durable WeftID into any note that lacks one (rewritten once,
		// crash-safe via atomic Write). Content-seeded so two devices holding the
		// same pre-sync note converge on the same id rather than forking.
		if out, _, did := noteid.Ensure(content, n.Path); did {
			if v.Write(n.Path, out) == nil {
				content = out
			}
		}
		title, body := extractTitleBody(content)
		if err := ix.Upsert(index.Note{
			Path:    n.Path,
			Title:   title,
			Body:    body,
			Links:   index.ParseLinks(content),
			ModTime: n.ModTime,
			Size:    n.Size,
		}); err != nil {
			return err
		}
		updateEmbedding(ix, emb, n.Path, title+"\n"+body)
		// Populate the tasks index for existing notes on (re)index so the
		// open-tasks view works without re-saving every note first.
		if err := ix.UpsertTasks(n.Path, extractTasks(content)); err != nil {
			return err
		}
	}

	// Orphan sweep: the loop above only ever upserts, so without this a note
	// pulled from disk (e.g. a trash whose ix.Remove failed) would keep its
	// rows — and keep appearing in search, tags, and surfacing — forever.
	live := make(map[string]bool, len(notes))
	for _, n := range notes {
		live[n.Path] = true
	}
	indexed, err := ix.Paths()
	if err != nil {
		return err
	}
	for _, p := range indexed {
		if !live[p] {
			if err := ix.Remove(p); err != nil {
				return err
			}
		}
	}
	return nil
}

// prepareNote stamps the note's durable WeftID and data-weft-id on its internal
// links before persisting. It PRESERVES an existing id (read from disk) so a
// writer that rebuilds <head> and drops the meta — the TipTap editor, the
// clipper — doesn't churn the note's identity on every save; only a genuinely
// new note mints a (content-seeded, device-convergent) id.
func prepareNote(v *vault.Vault, rel string, posted []byte) []byte {
	id := ""
	if existing, err := v.Read(rel); err == nil {
		id = noteid.ReadWeftID(existing)
	}
	if id == "" {
		id = noteid.Derive(rel, posted)
	}
	withID, err := noteid.WithWeftID(posted, id)
	if err != nil {
		withID = posted
	}
	return stampLinks(v, withID)
}

// stampLinks adds data-weft-id to each internal .html anchor, resolved from the
// target note's WeftID. The readable href stays (so a file still opens
// standalone in any browser); the id is what lets sync repair a link after the
// target is renamed. Read-only on targets — a target without an id yet is
// skipped and picked up on a later save (its own save/backfill mints it).
func stampLinks(v *vault.Vault, content []byte) []byte {
	doc, err := gohtml.Parse(bytes.NewReader(content))
	if err != nil {
		return content
	}
	changed := false
	var walk func(n *gohtml.Node)
	walk = func(n *gohtml.Node) {
		if n.Type == gohtml.ElementNode && n.Data == "a" {
			href, hasID := "", false
			for _, a := range n.Attr {
				switch a.Key {
				case "href":
					href = a.Val
				case "data-weft-id":
					hasID = true
				}
			}
			if href != "" && !hasID {
				if rel, ok := internalNoteRel(href); ok {
					if tc, err := v.Read(rel); err == nil {
						if id := noteid.ReadWeftID(tc); id != "" {
							n.Attr = append(n.Attr, gohtml.Attribute{Key: "data-weft-id", Val: id})
							changed = true
						}
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if !changed {
		return content
	}
	var buf bytes.Buffer
	if gohtml.Render(&buf, doc) != nil {
		return content
	}
	return buf.Bytes()
}

// internalNoteRel normalizes an href to a vault-relative .html path, or ok=false
// for external/anchor/site-absolute/weft:// links and non-.html targets.
func internalNoteRel(href string) (string, bool) {
	h := strings.TrimSpace(href)
	if h == "" {
		return "", false
	}
	low := strings.ToLower(h)
	for _, p := range []string{"http://", "https://", "mailto:", "javascript:", "weft://", "#", "/"} {
		if strings.HasPrefix(low, p) {
			return "", false
		}
	}
	h = strings.TrimPrefix(h, "./")
	if i := strings.IndexAny(h, "?#"); i >= 0 {
		h = h[:i]
	}
	if h == "" || !strings.HasSuffix(strings.ToLower(h), ".html") {
		return "", false
	}
	return h, true
}

// updateEmbedding is a no-op when emb is nil. Errors are swallowed: a failed
// embedding shouldn't fail the surrounding save/index path.
func updateEmbedding(ix *index.Index, emb embed.Embedder, path, text string) {
	if emb == nil || strings.TrimSpace(text) == "" {
		return
	}
	vec, err := emb.Embed(text)
	if err != nil {
		return
	}
	_ = ix.UpsertEmbedding(path, embed.Encode(vec))
}

// extractTitleBody pulls the first <h1> text as the title and a tag-stripped,
// whitespace-collapsed version of the body for FTS. Falls back to <title>.
func extractTitleBody(content []byte) (title, body string) {
	doc, err := gohtml.Parse(bytes.NewReader(content))
	if err != nil {
		return "", string(content)
	}
	var fallbackTitle string
	var sb strings.Builder
	var walk func(n *gohtml.Node)
	walk = func(n *gohtml.Node) {
		if n.Type == gohtml.ElementNode {
			switch n.Data {
			case "h1":
				if title == "" {
					title = strings.TrimSpace(textOf(n))
				}
			case "title":
				if fallbackTitle == "" {
					fallbackTitle = strings.TrimSpace(textOf(n))
				}
			case "script", "style":
				return
			}
		}
		if n.Type == gohtml.TextNode {
			sb.WriteString(n.Data)
			sb.WriteByte(' ')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if title == "" {
		title = fallbackTitle
	}
	return title, strings.Join(strings.Fields(sb.String()), " ")
}

// articleInnerHTML returns the inner HTML of the first <article> element in
// the document, or false if none. Used to feed wiki.ExpandWikilinks, which
// expects body-fragment input.
func articleInnerHTML(content []byte) ([]byte, bool) {
	doc, err := gohtml.Parse(bytes.NewReader(content))
	if err != nil {
		return nil, false
	}
	article := findElement(doc, "article")
	if article == nil {
		return nil, false
	}
	var buf bytes.Buffer
	for c := article.FirstChild; c != nil; c = c.NextSibling {
		if err := gohtml.Render(&buf, c); err != nil {
			return nil, false
		}
	}
	return buf.Bytes(), true
}

// replaceArticleInner swaps the inner HTML of the first <article> with
// newInner. Returns the rebuilt full document.
func replaceArticleInner(content, newInner []byte) ([]byte, bool) {
	doc, err := gohtml.Parse(bytes.NewReader(content))
	if err != nil {
		return nil, false
	}
	article := findElement(doc, "article")
	if article == nil {
		return nil, false
	}
	for c := article.FirstChild; c != nil; {
		next := c.NextSibling
		article.RemoveChild(c)
		c = next
	}
	nodes, err := gohtml.ParseFragment(bytes.NewReader(newInner), article)
	if err != nil {
		return nil, false
	}
	for _, n := range nodes {
		article.AppendChild(n)
	}
	var buf bytes.Buffer
	if err := gohtml.Render(&buf, doc); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}

// rewriteHrefInDoc walks the full document and rewrites <a href=oldHref>
// occurrences to newHref. Returns (rewritten bytes, true) if anything changed.
func rewriteHrefInDoc(content []byte, oldHref, newHref string) ([]byte, bool) {
	doc, err := gohtml.Parse(bytes.NewReader(content))
	if err != nil {
		return content, false
	}
	changed := false
	var walk func(n *gohtml.Node)
	walk = func(n *gohtml.Node) {
		if n.Type == gohtml.ElementNode && n.Data == "a" {
			for i, a := range n.Attr {
				if a.Key == "href" && a.Val == oldHref {
					n.Attr[i].Val = newHref
					changed = true
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if !changed {
		return content, false
	}
	var buf bytes.Buffer
	if err := gohtml.Render(&buf, doc); err != nil {
		return content, false
	}
	return buf.Bytes(), true
}

func findElement(n *gohtml.Node, tag string) *gohtml.Node {
	if n.Type == gohtml.ElementNode && n.Data == tag {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findElement(c, tag); found != nil {
			return found
		}
	}
	return nil
}

// osRemove deletes a file at the vault-relative path. It mirrors vault.Write's
// "never escape root" guard so rename can't be tricked into deleting outside
// the vault.
func osRemove(v *vault.Vault, rel string) error {
	if strings.Contains(rel, "..") {
		return fmt.Errorf("invalid path")
	}
	full := filepath.Join(v.Root, filepath.Clean("/"+rel))
	if !strings.HasPrefix(full, v.Root+string(filepath.Separator)) {
		return fmt.Errorf("path escapes vault")
	}
	return os.Remove(full)
}

func textOf(n *gohtml.Node) string {
	var sb strings.Builder
	var walk func(n *gohtml.Node)
	walk = func(n *gohtml.Node) {
		if n.Type == gohtml.TextNode {
			sb.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return sb.String()
}

// attrVal returns the value of n's named attribute, or "" if absent.
func attrVal(n *gohtml.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// extractTasks parses TipTap task items from a note's HTML in document order.
// Markup: <ul data-type="taskList"><li data-type="taskItem" data-checked="true|false">…</li></ul>.
// Each item's text is its own text content with any nested sub-task list excluded
// (nested items are captured as their own entries). Returns nil when there are
// none. Keyed downstream by position (item_idx), so order is the contract.
func extractTasks(content []byte) []index.TaskItem {
	doc, err := gohtml.Parse(bytes.NewReader(content))
	if err != nil {
		return nil
	}
	var out []index.TaskItem
	var walk func(n *gohtml.Node)
	walk = func(n *gohtml.Node) {
		if n.Type == gohtml.ElementNode && n.Data == "li" && attrVal(n, "data-type") == "taskItem" {
			out = append(out, index.TaskItem{
				Text:    taskItemText(n),
				Checked: attrVal(n, "data-checked") == "true",
			})
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return out
}

// taskItemText returns a task item's own text, excluding any nested task list
// (whose items are captured separately). Whitespace is collapsed.
func taskItemText(li *gohtml.Node) string {
	var sb strings.Builder
	var walk func(n *gohtml.Node)
	walk = func(n *gohtml.Node) {
		if n.Type == gohtml.ElementNode && n.Data == "ul" && attrVal(n, "data-type") == "taskList" {
			return // nested sub-tasks are their own entries
		}
		if n.Type == gohtml.TextNode {
			sb.WriteString(n.Data)
			sb.WriteByte(' ')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	for c := li.FirstChild; c != nil; c = c.NextSibling {
		walk(c)
	}
	return strings.Join(strings.Fields(sb.String()), " ")
}

func openBrowser(url string) {
	switch runtime.GOOS {
	case "darwin":
		exec.Command("open", url).Start()
	case "linux":
		exec.Command("xdg-open", url).Start()
	case "windows":
		exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	}
}

var listTmpl = template.Must(template.New("list").Funcs(template.FuncMap{
	"fmtDate": func(t time.Time) string { return t.Format("Jan 2, 2006") },
}).Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Weft</title>
<style>
  *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }

  :root {
    --bg:      #fafafa;
    --surface: #ffffff;
    --border:  #e8e8e8;
    --text:    #1a1a1a;
    --muted:   #888;
    --accent:  #2563eb;
    --radius:  6px;
  }
  @media (prefers-color-scheme: dark) {
    :root {
      --bg:      #0f0f0f;
      --surface: #1a1a1a;
      --border:  #2a2a2a;
      --text:    #e8e8e8;
      --muted:   #666;
      --accent:  #60a5fa;
    }
  }

  body {
    background: var(--bg);
    color: var(--text);
    font-family: system-ui, -apple-system, sans-serif;
    font-size: 15px;
    line-height: 1.5;
    min-height: 100vh;
  }

  .shell {
    max-width: 720px;
    margin: 0 auto;
    padding: 48px 24px 80px;
  }

  header {
    display: flex;
    align-items: baseline;
    gap: 12px;
    margin-bottom: 32px;
  }
  header h1 {
    font-size: 1.2rem;
    font-weight: 700;
    letter-spacing: -0.02em;
  }
  header .vault-path {
    font-size: 0.78rem;
    color: var(--muted);
    font-family: ui-monospace, monospace;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  header .actions {
    margin-left: auto;
    display: flex;
    gap: 8px;
  }
  header .actions a {
    font-size: 0.82rem;
    color: var(--accent);
    text-decoration: none;
  }
  header .actions a:hover { text-decoration: underline; }

  .search-wrap { margin-bottom: 24px; }
  #search {
    width: 100%;
    padding: 9px 14px;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
    color: var(--text);
    font-size: 0.9rem;
    outline: none;
  }
  #search:focus { border-color: var(--accent); }

  .notes { list-style: none; }
  .note-item {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    padding: 11px 0;
    border-bottom: 1px solid var(--border);
    gap: 16px;
  }
  .note-item:first-child { border-top: 1px solid var(--border); }
  .note-item a {
    color: var(--text);
    text-decoration: none;
    font-size: 0.92rem;
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .note-item a:hover { color: var(--accent); }
  .note-date {
    font-size: 0.78rem;
    color: var(--muted);
    flex-shrink: 0;
  }

  .empty { color: var(--muted); font-size: 0.9rem; padding: 32px 0; text-align: center; }
  .empty code {
    font-family: ui-monospace, monospace;
    background: var(--border);
    padding: 2px 6px;
    border-radius: 3px;
  }
</style>
</head>
<body>
<div class="shell">
  <header>
    <h1>Weft</h1>
    <span class="vault-path">{{.Root}}</span>
    <span class="actions">
      <a href="/daily">today</a>
    </span>
  </header>

  <div class="search-wrap">
    <input id="search" type="search" placeholder="Filter notes…" autocomplete="off">
  </div>

  {{if .Notes}}
  <ul class="notes" id="notes-list">
    {{range .Notes}}
    <li class="note-item" data-name="{{.Name}}">
      <a href="/note/{{.Path}}">{{.Name}}</a>
      <span class="note-date">{{fmtDate .ModTime}}</span>
    </li>
    {{end}}
  </ul>
  {{else}}
  <p class="empty">No notes yet — add <code>.html</code> files to your vault.</p>
  {{end}}
</div>

<script>
  const search = document.getElementById('search');
  const items  = document.querySelectorAll('.note-item');
  search.addEventListener('input', () => {
    const q = search.value.toLowerCase();
    items.forEach(el => {
      el.hidden = q && !el.dataset.name.toLowerCase().includes(q);
    });
  });
  search.focus();
</script>
</body>
</html>`))
