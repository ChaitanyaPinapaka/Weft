// Package graph builds the JSON-friendly node/edge structure consumed by the
// v0.9 graph view. It is a read-only projection over Vault.List + Index links +
// embeddings + access log; the graph package owns no storage of its own.
package graph

import (
	"math"
	"sort"
	"strings"
	"time"

	"weft/internal/embed"
	"weft/internal/index"
	"weft/internal/vault"
)

const (
	// semanticThreshold is the cosine floor for a semantic edge to exist. Below
	// this, the relation is noise; above it, two notes are genuinely "about" the
	// same thing. Matches the spirit of surface.Params.SemThreshold (which gates
	// the brain panel) but is set slightly higher here per the graph contract —
	// the graph view wants a sparser, more legible neighbor set.
	semanticThreshold = 0.60

	// maxSemanticNeighbors caps each note's outgoing semantic candidates (top-K).
	// Bounds the snapshot's edge count to O(n·K) and keeps the force layout from
	// degenerating into a hairball around topically-dense clusters.
	maxSemanticNeighbors = 4

	// activationDecay mirrors surface.DefaultParams().Decay (ACT-R d in t^-d).
	// Kept local so the graph snapshot stays decoupled from the surfacer's
	// tunable Params — same recency *shape*, no shared mutable config.
	activationDecay = 0.5

	// activationMinAge clamps t_j just like surface.BaseLevel, so a just-now
	// access (t=0 → +Inf) and clock skew (t<0) don't blow up the base level.
	activationMinAge = 1.0
)

type Node struct {
	Path          string    `json:"path"`
	Title         string    `json:"title"`
	BacklinkCount int       `json:"backlink_count"`
	ModTime       time.Time `json:"mtime"`
	Tags          []string  `json:"tags,omitempty"`
	// Folder is the top-level vault directory of the note ("" for root notes).
	// Drives cluster color in the UI.
	Folder string `json:"folder"`
	// Activation is a 0..1 "warmth": access-log frequency+recency blended with
	// mtime recency. Drives node size/intensity. Never-opened notes are low but
	// strictly nonzero. See activation() for the formula.
	Activation float64 `json:"activation"`
}

type Edge struct {
	Src string `json:"src"`
	Dst string `json:"dst"`
	// Kind is "backlink" (an <a href>/wikilink edge) or "semantic" (an embedding
	// cosine neighbor). A pair that is both keeps only the backlink edge.
	Kind string `json:"kind"`
	// Weight is the cosine similarity for semantic edges; omitted (and treated as
	// 1) for backlink edges.
	Weight float64 `json:"weight,omitempty"`
}

const (
	kindBacklink = "backlink"
	kindSemantic = "semantic"
)

type Graph struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

// Build walks the vault and index, returning a directed graph enriched for the
// brain-driven view: per-node folder + activation, plus semantic edges from
// embeddings on top of the existing backlink edges.
//
// Backlink directionality is preserved (mutual links produce two edges), but
// identical (src,dst) pairs from duplicate link entries are collapsed. Semantic
// edges are undirected-deduped (one per unordered pair, max weight), and a pair
// that is also a backlink contributes no semantic edge.
//
// Each node carries its tags (the graph view filters on them); they're loaded in
// one batch scan (AllTagsByPath), mirroring the activation-history scan rather
// than an N+1 over NotesWithTag.
func Build(v *vault.Vault, ix *index.Index) (Graph, error) {
	// Use make so an empty vault encodes as JSON `[]` rather than `null`.
	g := Graph{
		Nodes: make([]Node, 0),
		Edges: make([]Edge, 0),
	}

	notes, err := v.List()
	if err != nil {
		return g, err
	}

	// One scan each for the activation inputs — far cheaper than per-note queries
	// on the single-connection DB (mirrors the surfacer's batch pattern).
	history, err := ix.AllAccessHistory()
	if err != nil {
		return g, err
	}
	tagsByPath, err := ix.AllTagsByPath()
	if err != nil {
		return g, err
	}
	now := time.Now().Unix()

	// backlinkPairs records unordered {a,b} pairs already connected by a backlink,
	// so a semantically-near pair that is also linked yields only the backlink.
	backlinkPairs := make(map[string]struct{})
	seen := make(map[string]struct{})

	for _, n := range notes {
		bl, err := ix.BacklinksTo(n.Path)
		if err != nil {
			return g, err
		}
		g.Nodes = append(g.Nodes, Node{
			Path:          n.Path,
			Title:         n.Name,
			BacklinkCount: len(bl),
			ModTime:       n.ModTime,
			Tags:          tagsByPath[n.Path],
			Folder:        topFolder(n.Path),
			Activation:    activation(history[n.Path], n.ModTime, now),
		})

		links, err := ix.LinksFrom(n.Path)
		if err != nil {
			return g, err
		}
		for _, dst := range links {
			key := n.Path + "|" + dst
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			g.Edges = append(g.Edges, Edge{Src: n.Path, Dst: dst, Kind: kindBacklink})
			backlinkPairs[unorderedKey(n.Path, dst)] = struct{}{}
		}
	}

	semEdges, err := semanticEdges(ix, backlinkPairs)
	if err != nil {
		return g, err
	}
	g.Edges = append(g.Edges, semEdges...)

	return g, nil
}

// topFolder returns the top-level vault directory of a relative note path:
// "daily/2026-05-25.html" → "daily", "projects/sub/p.html" → "projects",
// "root.html" → "". Vault paths always use forward slashes.
func topFolder(path string) string {
	if i := strings.IndexByte(path, '/'); i >= 0 {
		return path[:i]
	}
	return ""
}

// activation blends ACT-R base-level activation (access-log frequency+recency)
// with mtime recency, squashed into 0..1.
//
// Base level B = ln(Σ_j t_j^-d) over access timestamps (the surfacer's recency
// curve; see surface.BaseLevel). For never-opened notes we seed a single
// synthetic access at the file's mtime, so a freshly-clipped note has a finite,
// recency-weighted warmth before its first open (mirrors surface.SeedMtime).
//
// B is unbounded below (an ancient single access → large negative). We squash it
// to (0,1) with the logistic 1/(1+e^-B). A note opened just now (t≈MinAge=1s) has
// B = -d·ln(1) = 0 → 0.5; repeated recent opens push B>0 → warmer; a note last
// touched long ago has B<0 → cooler but strictly positive. Never-opened-and-no-
// mtime notes fall back to a small nonzero floor.
func activation(accesses []int64, mtime time.Time, now int64) float64 {
	ts := accesses
	if len(ts) == 0 && !mtime.IsZero() {
		ts = []int64{mtime.Unix()} // synthetic mtime seed (surface.SeedMtime parity)
	}
	if len(ts) == 0 {
		return activationFloor
	}
	var sum float64
	for _, t := range ts {
		age := float64(now - t)
		if age < activationMinAge {
			age = activationMinAge // clamp t=0 (→+Inf) and clock skew (t<0)
		}
		sum += math.Pow(age, -activationDecay)
	}
	b := math.Log(sum)              // ACT-R base level
	a := 1.0 / (1.0 + math.Exp(-b)) // logistic squash → (0,1)
	if a < activationFloor {
		a = activationFloor
	}
	return a
}

// activationFloor is the warmth of a note with no access history and no usable
// mtime: low, but strictly nonzero so it still renders.
const activationFloor = 0.05

// semanticEdges computes top-K (≤maxSemanticNeighbors) cosine neighbors per note
// at cosine ≥ semanticThreshold, undirected-deduped to one edge per unordered
// pair (keeping the max weight), excluding pairs already joined by a backlink.
//
// O(n²) cosine over decoded vectors, bounded by the threshold + top-K cap on the
// emitted edges. Notes lacking an embedding contribute nothing.
//
// Each vector is L2-normalized once up front (n·d work), so the inner pair loop
// is a bare dot product — cosine without recomputing both norms + two sqrts on
// every pair, which is what dominates the n² cost at vault scale.
func semanticEdges(ix *index.Index, backlinkPairs map[string]struct{}) ([]Edge, error) {
	blobs, err := ix.AllEmbeddings()
	if err != nil {
		return nil, err
	}
	if len(blobs) < 2 {
		return nil, nil
	}

	// Decode and L2-normalize once. Stable, sorted path order makes the n² loop
	// deterministic. Zero-norm vectors are dropped so a dot product on them can't
	// masquerade as a real (cosine) similarity.
	paths := make([]string, 0, len(blobs))
	for p := range blobs {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	vecs := make(map[string][]float32, len(blobs))
	kept := paths[:0] // reuse backing array; only normalizable vectors survive
	for _, p := range paths {
		v, derr := embed.Decode(blobs[p])
		if derr != nil {
			return nil, derr
		}
		if !normalize(v) {
			continue // zero-norm vector contributes no edges
		}
		vecs[p] = v
		kept = append(kept, p)
	}
	paths = kept

	type neighbor struct {
		path string
		sim  float64
	}

	// best[pair] keeps the max cosine for an unordered pair selected by either
	// endpoint's top-K, so the result is undirected-deduped.
	best := make(map[string]float64)

	for _, src := range paths {
		sv := vecs[src]
		var nbrs []neighbor
		for _, dst := range paths {
			if dst == src {
				continue
			}
			// Both vectors are unit-length, so the dot product is the cosine.
			sim := dot(sv, vecs[dst])
			if sim < semanticThreshold {
				continue
			}
			nbrs = append(nbrs, neighbor{path: dst, sim: sim})
		}
		// Top-K by descending similarity (path as a stable tiebreak).
		sort.Slice(nbrs, func(i, j int) bool {
			if nbrs[i].sim != nbrs[j].sim {
				return nbrs[i].sim > nbrs[j].sim
			}
			return nbrs[i].path < nbrs[j].path
		})
		if len(nbrs) > maxSemanticNeighbors {
			nbrs = nbrs[:maxSemanticNeighbors]
		}
		for _, nb := range nbrs {
			key := unorderedKey(src, nb.path)
			if _, isBacklink := backlinkPairs[key]; isBacklink {
				continue // backlink wins; emit no semantic edge for this pair
			}
			if cur, ok := best[key]; !ok || nb.sim > cur {
				best[key] = nb.sim
			}
		}
	}

	// Materialize. Sort by key so output is deterministic.
	keys := make([]string, 0, len(best))
	for k := range best {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	edges := make([]Edge, 0, len(keys))
	for _, k := range keys {
		a, b := splitKey(k)
		edges = append(edges, Edge{Src: a, Dst: b, Kind: kindSemantic, Weight: best[k]})
	}
	return edges, nil
}

// normalize scales v to unit L2 length in place, returning false (leaving v
// untouched) for a zero-norm vector. After this, the dot product of two vectors
// equals their cosine similarity.
func normalize(v []float32) bool {
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	if n == 0 {
		return false
	}
	inv := float32(1.0 / math.Sqrt(n))
	for i := range v {
		v[i] *= inv
	}
	return true
}

// dot is the plain inner product of two equal-length vectors. For unit vectors
// (see normalize) it equals the cosine similarity. Returns 0 on a length
// mismatch, matching embed.CosineSimilarity's defensive contract.
func dot(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0
	}
	var d float64
	for i := range a {
		d += float64(a[i]) * float64(b[i])
	}
	return d
}

// unorderedKey returns a canonical key for the unordered pair {a,b}. The "\x00"
// separator can't appear in a vault path, so the join is unambiguous.
func unorderedKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "\x00" + b
}

// splitKey inverts unorderedKey.
func splitKey(k string) (string, string) {
	if i := strings.IndexByte(k, '\x00'); i >= 0 {
		return k[:i], k[i+1:]
	}
	return k, ""
}
