// Package core holds the vault operations that BOTH the HTTP daemon and the iOS
// gomobile facade need: incremental indexing, single-note reindex, and the
// surfacing (brain-panel) computation. It is HTTP-free — it depends only on the
// vault, index, surface, embed, and noteid packages — so it links cleanly into a
// gomobile/iOS build (no net/http, no embedded web assets).
//
// NOTE: internal/server currently carries its own copies of ExtractTitleBody /
// UpdateEmbedding / IndexAll / Surface. This package was split out for the mobile
// facade with minimal blast radius (zero edits to the running daemon). The daemon
// should be DRY-ed onto core in a later, dedicated change.
package core

import (
	"bytes"
	"sort"
	"strings"
	"time"

	gohtml "golang.org/x/net/html"

	"weft/internal/embed"
	"weft/internal/index"
	"weft/internal/noteid"
	"weft/internal/surface"
	"weft/internal/vault"
)

// ExtractTitleBody pulls the first <h1> text as the title and a tag-stripped,
// whitespace-collapsed version of the body for FTS. Falls back to <title>.
func ExtractTitleBody(content []byte) (title, body string) {
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

// UpdateEmbedding stores a note's vector in the index. It is a no-op when emb is
// nil (the default iOS build until a native embedder is registered) or the text
// is blank; embedding errors are swallowed so they never fail the index path.
func UpdateEmbedding(ix *index.Index, emb embed.Embedder, path, text string) {
	if emb == nil || strings.TrimSpace(text) == "" {
		return
	}
	vec, err := emb.Embed(text)
	if err != nil {
		return
	}
	_ = ix.UpsertEmbedding(path, embed.Encode(vec))
}

// ReindexNote reads one note and refreshes its FTS row + embedding. Used right
// after a capture/daily write lands so the new content is searchable + surfaced.
func ReindexNote(v *vault.Vault, ix *index.Index, emb embed.Embedder, rel string) error {
	content, err := v.Read(rel)
	if err != nil {
		return err
	}
	title, body := ExtractTitleBody(content)
	if err := ix.Upsert(index.Note{
		Path:    rel,
		Title:   title,
		Body:    body,
		Links:   index.ParseLinks(content),
		ModTime: time.Now(),
		Size:    int64(len(content)),
	}); err != nil {
		return err
	}
	UpdateEmbedding(ix, emb, rel, title+"\n"+body)
	return nil
}

// IndexAll incrementally (re)indexes the vault: only notes whose stored mtime is
// stale are re-read, re-parsed, and re-embedded; an un-stamped note gets a
// durable, content-seeded WeftID backfilled (crash-safe via atomic Write).
func IndexAll(v *vault.Vault, ix *index.Index, emb embed.Embedder) error {
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
		if out, _, did := noteid.Ensure(content, n.Path); did {
			if v.Write(n.Path, out) == nil {
				content = out
			}
		}
		title, body := ExtractTitleBody(content)
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
		UpdateEmbedding(ix, emb, n.Path, title+"\n"+body)
	}
	return nil
}

// SurfaceResult is the computed brain-panel payload: the ACT-R activation
// ranking plus the explicit backlinks, the on-this-day anniversaries, and the
// session trail (focus first, then earlier-session sources).
type SurfaceResult struct {
	Current   string
	Backlinks []string
	Scored    []surface.Scored
	OnThisDay []surface.Candidate
	Trail     []string
}

// Surface runs the surfacing engine for one focus note: it reconstructs the
// current session from the access log, weights attention across the sources,
// builds per-candidate association edges (backlink/semantic/co-access), and ranks
// every note by ACT-R activation. `cur` is the focus path (a trailing .html is
// appended if missing); `now` anchors recency and the noise seed.
func Surface(v *vault.Vault, ix *index.Index, p surface.Params, cur string, now time.Time) (SurfaceResult, error) {
	if !strings.HasSuffix(cur, ".html") {
		cur += ".html"
	}

	notes, err := v.List()
	if err != nil {
		return SurfaceResult{}, err
	}

	nowUnix := now.Unix()

	// (a) Reconstruct the current session by gap-walking recent accesses. The
	// focus is the source at full attention; earlier in-session notes spread with
	// decaying attention.
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

	// (e) Build candidates with per-source edges. Skip the self-source edge (a
	// note that is also a session source has cosine 1.0 to itself and would
	// fabricate a semantic boost). Path-sort for deterministic noise.
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
	scored := surface.Rank(surface.Candidate{Path: cur}, sources, cands, nowUnix, p, noiser, nil)
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

	return SurfaceResult{
		Current:   cur,
		Backlinks: back,
		Scored:    scored,
		OnThisDay: filtered,
		Trail:     sourcePaths,
	}, nil
}
