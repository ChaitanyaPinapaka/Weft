// Package server runs the Weft HTTP daemon on localhost:7777.
package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	gohtml "golang.org/x/net/html"

	"weft/internal/embed"
	"weft/internal/index"
	"weft/internal/surface"
	"weft/internal/vault"
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

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", listHandler(v))
	mux.HandleFunc("GET /note/{path...}", noteHandler(v, ix))
	mux.HandleFunc("GET /api/notes", apiNotesHandler(v))
	mux.HandleFunc("GET /api/search", searchHandler(ix))
	mux.HandleFunc("GET /api/surface/{path...}", surfaceHandler(v, ix, emb))
	mux.HandleFunc("POST /api/note/{path...}", saveHandler(v, ix, emb))
	mux.HandleFunc("GET /api/daily", dailyHandler(v, ix, emb))
	mux.Handle("GET /web/", http.StripPrefix("/web/", http.FileServerFS(web.FS)))

	url := "http://" + addr
	dailyURL := url + "/web/viewer.html?path=" + v.DailyPath(time.Now())
	fmt.Printf("Weft  %s\n", url)
	fmt.Printf("Vault %s\n", v.Root)
	fmt.Printf("Daily %s\n\n", dailyURL)

	go func() {
		time.Sleep(150 * time.Millisecond)
		openBrowser(url)
	}()

	return http.ListenAndServe(addr, mux)
}

func listHandler(v *vault.Vault) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		notes, err := v.List()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
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

func noteHandler(v *vault.Vault, ix *index.Index) http.HandlerFunc {
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
		// Append-only access log feeds the co-access boost in surfacing.
		// Best-effort; a failed log shouldn't poison the response.
		_ = ix.LogAccess(rel, time.Now().Unix())
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

// surfaceHandler returns the brain-panel payload: explicit backlinks plus
// surface.Rank applied across the rest of the vault. When an embedder is
// available, each candidate's Similarity is filled from cosine(current, c).
func surfaceHandler(v *vault.Vault, ix *index.Index, emb embed.Embedder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cur := r.PathValue("path")
		if !strings.HasSuffix(cur, ".html") {
			cur += ".html"
		}

		notes, err := v.List()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		back, _ := ix.BacklinksTo(cur)
		forward, _ := ix.LinksFrom(cur)

		linked := map[string]bool{}
		for _, p := range back {
			linked[p] = true
		}
		for _, p := range forward {
			linked[p] = true
		}

		sims := candidateSimilarities(ix, cur)

		coAcc := map[string]bool{}
		if co, err := ix.CoAccessed(cur, 30*time.Minute); err == nil {
			for _, p := range co {
				coAcc[p] = true
			}
		}

		cands := make([]surface.Candidate, 0, len(notes))
		for _, n := range notes {
			cands = append(cands, surface.Candidate{
				Path:        n.Path,
				Title:       n.Name,
				ModTime:     n.ModTime,
				HasBacklink: linked[n.Path],
				CoAccessed:  coAcc[n.Path],
				Similarity:  float64(sims[n.Path]),
			})
		}

		now := time.Now()
		scored := surface.Rank(surface.Candidate{Path: cur}, cands, now)
		const surfaceLimit = 12
		if len(scored) > surfaceLimit {
			scored = scored[:surfaceLimit]
		}

		// OnThisDay is independent of Rank — separate panel section.
		// Exclude the current note from the prior-year matches.
		otd := surface.OnThisDay(cands, now)
		filtered := otd[:0]
		for _, c := range otd {
			if c.Path != cur {
				filtered = append(filtered, c)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"current":     cur,
			"backlinks":   back,
			"scored":      scored,
			"on_this_day": filtered,
		})
	}
}

// candidateSimilarities returns a map[path]cosine vs the current note's
// embedding. Returns empty map (not nil) if any precondition fails — callers
// just read sims[path] and get 0.
func candidateSimilarities(ix *index.Index, curPath string) map[string]float32 {
	curBlob, err := ix.GetEmbedding(curPath)
	if err != nil || curBlob == nil {
		return map[string]float32{}
	}
	curVec, err := embed.Decode(curBlob)
	if err != nil {
		return map[string]float32{}
	}
	all, err := ix.AllEmbeddings()
	if err != nil {
		return map[string]float32{}
	}
	out := make(map[string]float32, len(all))
	for p, blob := range all {
		if p == curPath {
			continue
		}
		v, err := embed.Decode(blob)
		if err != nil {
			continue
		}
		out[p] = embed.CosineSimilarity(curVec, v)
	}
	return out
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
		rel, err := v.EnsureDaily(time.Now())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if content, err := v.Read(rel); err == nil {
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
      <a href="#" id="open-daily">today</a>
    </span>
  </header>

  <div class="search-wrap">
    <input id="search" type="search" placeholder="Filter notes…" autocomplete="off">
  </div>

  {{if .Notes}}
  <ul class="notes" id="notes-list">
    {{range .Notes}}
    <li class="note-item" data-name="{{.Name}}">
      <a href="/web/viewer.html?path={{.Path}}">{{.Name}}</a>
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

  document.getElementById('open-daily').addEventListener('click', async (e) => {
    e.preventDefault();
    const res = await fetch('/api/daily');
    if (!res.ok) return;
    const { path } = await res.json();
    location.href = '/web/viewer.html?path=' + encodeURIComponent(path);
  });
</script>
</body>
</html>`))
