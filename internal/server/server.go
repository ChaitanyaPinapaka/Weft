// Package server runs the Weft HTTP daemon on localhost:7777.
package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	gohtml "golang.org/x/net/html"

	"weft/internal/clip"
	"weft/internal/embed"
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

	// If sync is configured, converge through the E2EE bucket in the background.
	startAutoSync(v, ix, emb)

	// Proactive surfacing: push "what's worth resurfacing now" to connected
	// surfaces (the macOS app) over SSE. No-op until a client subscribes.
	hub := newAmbientHub()
	startAmbientSurfacer(hub, v, ix, ps)

	mux := http.NewServeMux()
	// Home is today's daily note in the editor, cursor ready — capture-first,
	// the default state is writing, not browsing (HANDOFF: "Daily note as home").
	// The vault list lives at /notes.
	mux.HandleFunc("GET /{$}", homeHandler(v))
	mux.HandleFunc("GET /notes", listHandler(v))
	mux.HandleFunc("GET /note/{path...}", noteHandler(v, ix))
	mux.HandleFunc("GET /raw/{path...}", rawHandler(v))
	mux.HandleFunc("GET /edit/{path...}", editRedirectHandler())
	mux.HandleFunc("GET /daily", dailyRedirectHandler(v))
	mux.HandleFunc("GET /api/notes", apiNotesHandler(v))
	mux.HandleFunc("GET /api/search", searchHandler(ix))
	// More specific than the {path...} wildcard below, so ServeMux routes the
	// literal "stream" here instead of treating it as a note path.
	mux.HandleFunc("GET /api/surface/stream", surfaceStreamHandler(hub))
	mux.HandleFunc("GET /api/surface/{path...}", surfaceHandler(v, ix, emb, ps))
	mux.HandleFunc("POST /api/note/{path...}", saveHandler(v, ix, emb))
	mux.HandleFunc("POST /api/rename", renameHandler(v, ix, emb))
	mux.HandleFunc("GET /api/daily", dailyHandler(v, ix, emb))
	mux.HandleFunc("GET /api/tags", tagsHandler(ix))
	mux.HandleFunc("GET /api/tags/{tag}", tagHandler(ix))
	mux.HandleFunc("POST /api/clip", clipHandler(v, ix, emb))
	mux.HandleFunc("POST /api/capture", captureHandler(v, ix, emb))
	mux.HandleFunc("GET /api/graph", graphHandler(v, ix))
	mux.HandleFunc("GET /graph", graphRedirectHandler())
	mux.HandleFunc("GET /api/params", getParamsHandler(ps))
	mux.HandleFunc("POST /api/params", postParamsHandler(ps))
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
// surface. The daemon is bound to localhost so wide-open CORS is fine here.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
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
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(content)
	}
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

// dailyRedirectHandler ensures today's daily exists (using the template if
// daily/template.html is present) and 302s to /note/{path}.
func dailyRedirectHandler(v *vault.Vault) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rel, err := v.EnsureDailyFromTemplate(time.Now())
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
func clipHandler(v *vault.Vault, ix *index.Index, emb embed.Embedder) http.HandlerFunc {
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
func captureHandler(v *vault.Vault, ix *index.Index, emb embed.Embedder) http.HandlerFunc {
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
		if content, err := v.Read(rel); err == nil {
			t, txt := extractTitleBody(content)
			_ = ix.Upsert(index.Note{
				Path:    rel,
				Title:   t,
				Body:    txt,
				Links:   index.ParseLinks(content),
				ModTime: time.Now(),
				Size:    int64(len(content)),
			})
			updateEmbedding(ix, emb, rel, t+"\n"+txt)
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
func renameHandler(v *vault.Vault, ix *index.Index, emb embed.Embedder) http.HandlerFunc {
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

		// Rewrite any anchors across the vault that pointed at oldRel.
		notes, _ := v.List()
		for _, n := range notes {
			if n.Path == newRel {
				continue
			}
			c, err := v.Read(n.Path)
			if err != nil {
				continue
			}
			updated, changed := rewriteHrefInDoc(c, oldRel, newRel)
			if !changed {
				continue
			}
			if err := v.Write(n.Path, updated); err != nil {
				continue
			}
			t, b := extractTitleBody(updated)
			_ = ix.Upsert(index.Note{
				Path:    n.Path,
				Title:   t,
				Body:    b,
				Links:   index.ParseLinks(updated),
				ModTime: time.Now(),
				Size:    int64(len(updated)),
			})
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"to": newRel})
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
	scored := surface.Rank(surface.Candidate{Path: cur}, sources, cands, nowUnix, p, noiser)
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

func saveHandler(v *vault.Vault, ix *index.Index, emb embed.Embedder) http.HandlerFunc {
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

		content = prepareNote(v, rel, content) // stamp/preserve WeftID + link ids
		if err := v.Write(rel, content); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
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
		w.WriteHeader(http.StatusNoContent)
	}
}

func dailyHandler(v *vault.Vault, ix *index.Index, emb embed.Embedder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rel, err := v.EnsureDailyFromTemplate(time.Now())
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
// re-reads, re-parses, and re-embeds the stale ones.
func indexAll(v *vault.Vault, ix *index.Index, emb embed.Embedder) error {
	notes, err := v.List()
	if err != nil {
		return err
	}
	for _, n := range notes {
		stale, err := ix.Stale(n.Path, n.ModTime)
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
