// Package weaver finds notes that are semantically close but not yet linked and
// proposes them as wikilinks, written to a dated digest note. It is the "vault
// tends itself" layer: a runnable pass (today via `weft weave`, a daemon ticker
// or nightly Claude later) that surfaces latent connections the brain panel can
// already rank but you never linked. Pure proposal — it never edits your notes,
// only writes an idempotent suggestion digest you act on by hand.
package weaver

import (
	"bytes"
	"fmt"
	"html"
	"sort"
	"strings"
	"time"

	"weft/internal/embed"
	"weft/internal/index"
	"weft/internal/vault"
)

// SimilarPair is an unlinked pair of notes whose vectors are close. A < B.
type SimilarPair struct {
	A, B       string
	Similarity float64
}

const (
	// defaultThreshold matches surface.DefaultParams().SemThreshold so a proposal
	// fires exactly when a semantic edge would in the brain panel.
	defaultThreshold = 0.55
	// defaultLimit keeps a digest glanceable rather than a wall of weak pairs.
	defaultLimit = 30
)

// UnlinkedSemanticPairs returns pairs (A<B) whose cosine >= threshold and that
// are not already linked (per linked, nil = none linked), ranked by similarity
// desc and capped at limit (<=0 = uncapped). Vectors must be non-empty; missing
// ones are skipped. Deterministic: paths are sorted so the output is stable.
func UnlinkedSemanticPairs(vecs map[string][]float32, linked func(a, b string) bool, threshold float64, limit int) []SimilarPair {
	paths := make([]string, 0, len(vecs))
	for p, v := range vecs {
		if len(v) > 0 {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	var out []SimilarPair
	for i := 0; i < len(paths); i++ {
		for j := i + 1; j < len(paths); j++ {
			a, b := paths[i], paths[j]
			if linked != nil && linked(a, b) {
				continue
			}
			sim := float64(embed.CosineSimilarity(vecs[a], vecs[b]))
			if sim >= threshold {
				out = append(out, SimilarPair{A: a, B: b, Similarity: sim})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Similarity > out[j].Similarity })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// GenerateDigest scans the vault for unlinked-but-similar note pairs and writes
// a dated digest proposing them as wikilinks (digests/YYYY-MM-DD.html). It is
// idempotent and never destructive: an existing digest for now's date is left
// untouched, and zero proposals writes nothing (no empty-digest noise). Returns
// the digest path, the proposal count, whether a file was written, and any error.
func GenerateDigest(v *vault.Vault, ix *index.Index, now time.Time) (path string, proposals int, wrote bool, err error) {
	rel := fmt.Sprintf("digests/%s.html", now.Format("2006-01-02"))
	if v.Exists(rel) {
		return rel, 0, false, nil // never overwrite
	}
	blobs, err := ix.AllEmbeddings()
	if err != nil {
		return "", 0, false, err
	}
	vecs := make(map[string][]float32, len(blobs))
	for p, blob := range blobs {
		if vec, derr := embed.Decode(blob); derr == nil {
			vecs[p] = vec
		}
	}
	pairs := UnlinkedSemanticPairs(vecs, linkedSet(ix, vecs), defaultThreshold, defaultLimit)
	if len(pairs) == 0 {
		return rel, 0, false, nil
	}
	if err := v.Write(rel, []byte(renderDigest(pairs, now))); err != nil {
		return "", 0, false, err
	}
	return rel, len(pairs), true, nil
}

// linkedSet builds an "already connected" predicate over the embedded notes from
// each note's outgoing links (canonicalized A<B, so direction doesn't matter).
func linkedSet(ix *index.Index, vecs map[string][]float32) func(a, b string) bool {
	m := make(map[string]bool)
	for p := range vecs {
		outs, err := ix.LinksFrom(p)
		if err != nil {
			continue
		}
		for _, d := range outs {
			m[canonKey(p, d)] = true
		}
	}
	return func(a, b string) bool { return m[canonKey(a, b)] }
}

func canonKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "\x00" + b
}

// renderDigest emits the digest note. Links use /note/{path} so they navigate
// straight to the viewer and are NOT parsed back as real backlinks (the proposal
// list shouldn't fabricate links the user hasn't accepted yet).
func renderDigest(pairs []SimilarPair, now time.Time) string {
	date := now.Format("2006-01-02")
	var b bytes.Buffer
	fmt.Fprintf(&b, "<!DOCTYPE html><html><head><meta charset=\"utf-8\"><title>Weave digest %s</title></head>\n<body>\n<article>\n", date)
	fmt.Fprintf(&b, "<h1>Weave digest — %s</h1>\n", date)
	fmt.Fprintf(&b, "<p>%d note pairs are semantically close but not yet linked. Open one and add a <code>[[wikilink]]</code> if the connection is real.</p>\n", len(pairs))
	b.WriteString("<ul>\n")
	for _, p := range pairs {
		fmt.Fprintf(&b, "  <li><a href=\"/note/%s\">%s</a> &harr; <a href=\"/note/%s\">%s</a> <em>(%.2f)</em></li>\n",
			html.EscapeString(p.A), html.EscapeString(displayName(p.A)),
			html.EscapeString(p.B), html.EscapeString(displayName(p.B)), p.Similarity)
	}
	b.WriteString("</ul>\n</article>\n</body></html>\n")
	return b.String()
}

func displayName(path string) string {
	base := path
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	return strings.TrimSuffix(base, ".html")
}
