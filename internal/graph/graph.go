// Package graph builds the JSON-friendly node/edge structure consumed by the
// v0.9 graph view. It is a read-only projection over Vault.List + Index links;
// the graph package owns no storage of its own.
package graph

import (
	"time"

	"weft/internal/index"
	"weft/internal/vault"
)

type Node struct {
	Path          string    `json:"path"`
	Title         string    `json:"title"`
	BacklinkCount int       `json:"backlink_count"`
	ModTime       time.Time `json:"mtime"`
	Tags          []string  `json:"tags,omitempty"`
}

type Edge struct {
	Src string `json:"src"`
	Dst string `json:"dst"`
}

type Graph struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

// Build walks the vault and index, returning a directed graph. Directionality
// is preserved (mutual links produce two edges), but identical (src,dst) pairs
// from duplicate link entries are collapsed so the UI doesn't double-render.
//
// Tags is intentionally left nil: the only way to populate it without a new
// index method would be O(notes*tags) — calling NotesWithTag for every tag and
// inverting — which is wasteful here. UI can hit /api/tags/<tag> on demand.
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
			g.Edges = append(g.Edges, Edge{Src: n.Path, Dst: dst})
		}
	}

	return g, nil
}
