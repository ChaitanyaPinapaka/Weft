// Package mcp exposes the Weft vault to MCP clients (Claude Code, etc.) over
// stdio. Seven tools are registered: describe_vault (capability discovery),
// ground (token-budgeted context bundle), list_notes, read_note, write_note,
// search_notes, surface_note. The transport is JSON-RPC over stdin/stdout —
// any stray write to stdout corrupts the protocol, so this package logs only
// to stderr.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"weft/internal/index"
	"weft/internal/surface"
	"weft/internal/vault"
)

// Server wraps mcp-go's MCPServer with references to Weft's vault and index.
// Not running until Serve is called.
type Server struct {
	v   *vault.Vault
	ix  *index.Index
	mcp *server.MCPServer
}

// New builds an MCP server that exposes vault + index over the five Weft
// tools. The returned server is configured but not yet listening.
func New(v *vault.Vault, ix *index.Index) *Server {
	s := &Server{
		v:   v,
		ix:  ix,
		mcp: server.NewMCPServer("weft", "0.1.0", server.WithToolCapabilities(true)),
	}
	s.registerTools()
	return s
}

// Serve runs the MCP server on stdin/stdout until ctx is cancelled or the
// peer closes the connection. mcp-go's stdio loop reads until EOF, so the
// cancellation path closes stdin transitively when the client exits.
func (s *Server) Serve(ctx context.Context) error {
	// MCP frames JSON-RPC directly on stdout, so nothing else in this binary
	// may write here while the server is running. Logs go to stderr only.
	return server.NewStdioServer(s.mcp).Listen(ctx, os.Stdin, os.Stdout)
}

func (s *Server) registerTools() {
	s.mcp.AddTool(
		mcplib.NewTool("describe_vault",
			mcplib.WithDescription(
				"Describe this Weft vault and HOW to consume it: vault stats, the "+
					"available tools and when to use each, and the surfacing-over-search "+
					"recall model. Call this first to orient before reading or searching."),
		),
		s.handleDescribeVault,
	)

	s.mcp.AddTool(
		mcplib.NewTool("list_notes",
			mcplib.WithDescription(
				"List every note in the Weft vault. Returns an array of "+
					"{path, name, mtime, size}. `path` is vault-relative and is the "+
					"value to pass back to read_note / write_note / surface_note."),
		),
		s.handleListNotes,
	)

	s.mcp.AddTool(
		mcplib.NewTool("read_note",
			mcplib.WithDescription(
				"Read the full HTML body of one note. Returns {path, content}. "+
					"Errors with 'note not found' if the path is missing."),
			mcplib.WithString("path",
				mcplib.Required(),
				mcplib.Description("Vault-relative path, e.g. \"daily/2026-05-25.html\".")),
		),
		s.handleReadNote,
	)

	s.mcp.AddTool(
		mcplib.NewTool("write_note",
			mcplib.WithDescription(
				"Create or overwrite a note. The vault stores HTML — pass complete, "+
					"well-formed HTML (a fragment is fine; a full document is fine). "+
					"Returns {path, bytes_written}. Notes are never deleted by this tool."),
			mcplib.WithString("path",
				mcplib.Required(),
				mcplib.Description("Vault-relative path; created if it doesn't exist.")),
			mcplib.WithString("content",
				mcplib.Required(),
				mcplib.Description("HTML body to write. UTF-8.")),
		),
		s.handleWriteNote,
	)

	s.mcp.AddTool(
		mcplib.NewTool("search_notes",
			mcplib.WithDescription(
				"Full-text search across the vault via SQLite FTS5. Returns an "+
					"array of {path, title, snippet, score}, ranked by BM25 (higher "+
					"score = better). Errors on empty query."),
			mcplib.WithString("query",
				mcplib.Required(),
				mcplib.Description("FTS5 query string. Plain words OK; supports prefix* and \"phrases\".")),
			mcplib.WithNumber("limit",
				mcplib.Description("Maximum hits to return; defaults to 20.")),
		),
		s.handleSearchNotes,
	)

	s.mcp.AddTool(
		mcplib.NewTool("surface_note",
			mcplib.WithDescription(
				"Return the brain-panel context for a note: explicit backlinks plus "+
					"surfacing-ranked neighbors. Result shape is {backlinks: [path...], "+
					"scored: [{path, title, score, reasons}, ...]}. Use this when you "+
					"want to know what else in the vault relates to the given note."),
			mcplib.WithString("path",
				mcplib.Required(),
				mcplib.Description("Vault-relative path of the note to surface around.")),
		),
		s.handleSurfaceNote,
	)

	s.mcp.AddTool(
		mcplib.NewTool("ground",
			mcplib.WithDescription(
				"Assemble a token-budgeted context bundle for an INTENT (a question or "+
					"topic): lexical full-text matches PLUS associative neighbors of the top "+
					"match, deduped, with a coverage signal. Use this as the FIRST call when "+
					"you need relevant personal context for a task, then read_note the items "+
					"you want. Returns {intent, items:[{path,title,snippet,reason}], coverage}."),
			mcplib.WithString("intent",
				mcplib.Required(),
				mcplib.Description("The question or topic to ground in the vault.")),
			mcplib.WithNumber("limit",
				mcplib.Description("Max items in the bundle; defaults to 8.")),
		),
		s.handleGround,
	)
}

// --- handlers ---------------------------------------------------------------

// handleDescribeVault is the agent-native capability/discovery manifest — the
// MCP analog of /llms.txt. It tells a connecting agent what's here and how to
// consume it, so it can orient before reading or searching.
func (s *Server) handleDescribeVault(_ context.Context, _ mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	notes, _ := s.v.List()
	return jsonResult(map[string]any{
		"name":       "weft",
		"summary":    "A local-first personal-context vault. Recall is surfacing — associative and cue-driven — not just search.",
		"note_count": len(notes),
		"tools": []map[string]string{
			{"name": "ground", "use": "FIRST call for a task: a token-budgeted bundle of context for an intent (lexical matches + associative neighbors + a coverage signal)"},
			{"name": "list_notes", "use": "enumerate every note (path/title/mtime/size)"},
			{"name": "read_note", "use": "read a note's full HTML by path"},
			{"name": "search_notes", "use": "FTS5 full-text search, BM25-ranked"},
			{"name": "surface_note", "use": "associative recall: backlinks + activation-ranked neighbors for a note; prefer over search for 'what relates to X'"},
			{"name": "write_note", "use": "create/overwrite a note (HTML); never deletes"},
		},
		"model": map[string]string{
			"recall":    "ACT-R memory activation: base-level recency/frequency decay + spreading activation over backlink and co-access edges",
			"retention": "notes are never deleted; dormancy lowers ranking, not retention",
			"format":    "notes are HTML files on disk",
		},
		"how_to_consume": "Call describe_vault to orient, then ground(intent) for a task's context bundle (or list_notes/search_notes to locate, surface_note to expand a note). Persist durable findings with write_note.",
	})
}

// handleGround assembles a token-budgeted context bundle for an intent: the
// lexical (FTS) matches plus the associative neighbors of the top match, deduped
// and capped, with a coverage signal. It is the recall-first "give me the
// relevant context for this task" primitive — composing search + surface so an
// agent makes one call instead of orchestrating both. The bundle carries
// title/snippet/reason, not full bodies, keeping it within an agent's token
// budget; the agent read_note's the items it actually needs.
func (s *Server) handleGround(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	intent, err := stringArg(req, "intent")
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	intent = strings.TrimSpace(intent)
	if intent == "" {
		return mcplib.NewToolResultError("empty intent"), nil
	}
	limit := intArg(req, "limit", 8)

	type item struct {
		Path    string  `json:"path"`
		Title   string  `json:"title"`
		Snippet string  `json:"snippet,omitempty"`
		Reason  string  `json:"reason"`
		Score   float64 `json:"score,omitempty"`
	}
	seen := map[string]bool{}
	items := []item{}

	hits, _ := s.ix.Search(intent, limit)
	for _, h := range hits {
		if seen[h.Path] {
			continue
		}
		seen[h.Path] = true
		items = append(items, item{Path: h.Path, Title: h.Title, Snippet: h.Snippet, Reason: "match", Score: h.Score})
	}
	// Associative expansion: pull the top match's surfaced neighbors so the bundle
	// includes related context that doesn't lexically match — recall, not just
	// precision. Capped at the item budget.
	if len(hits) > 0 {
		if res, err := buildSurface(s.v, s.ix, hits[0].Path, time.Now()); err == nil {
			for _, sc := range res.Scored {
				if len(items) >= limit || seen[sc.Path] || sc.Spread <= 0 {
					continue
				}
				seen[sc.Path] = true
				items = append(items, item{Path: sc.Path, Title: sc.Title, Reason: "related"})
			}
		}
	}

	// Coverage is a v0 heuristic, NOT a calibrated completeness measure (that is a
	// later refinement): higher confidence when several lexical matches landed.
	confidence := "low"
	if len(hits) >= 1 {
		confidence = "medium"
	}
	if len(hits) >= 3 {
		confidence = "high"
	}
	return jsonResult(map[string]any{
		"intent": intent,
		"items":  items,
		"coverage": map[string]any{
			"retrieved":    len(items),
			"lexical_hits": len(hits),
			"confidence":   confidence,
			"note":         "heuristic v0, not a calibrated completeness measure",
		},
		"how_to_use": "read_note the items you need; surface_note any item to expand its neighborhood further.",
	})
}

func (s *Server) handleListNotes(_ context.Context, _ mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	notes, err := s.v.List()
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	out := make([]map[string]any, 0, len(notes))
	for _, n := range notes {
		out = append(out, map[string]any{
			"path":  n.Path,
			"name":  n.Name,
			"mtime": n.ModTime,
			"size":  n.Size,
		})
	}
	return jsonResult(out)
}

func (s *Server) handleReadNote(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	rel, err := stringArg(req, "path")
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	rel = ensureHTMLSuffix(rel)
	if !s.v.Exists(rel) {
		return mcplib.NewToolResultError("note not found: " + rel), nil
	}
	body, err := s.v.Read(rel)
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	return jsonResult(map[string]any{
		"path":    rel,
		"content": string(body),
	})
}

func (s *Server) handleWriteNote(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	rel, err := stringArg(req, "path")
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	content, err := stringArg(req, "content")
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	rel = ensureHTMLSuffix(rel)
	if err := s.v.Write(rel, []byte(content)); err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	return jsonResult(map[string]any{
		"path":          rel,
		"bytes_written": len(content),
	})
}

func (s *Server) handleSearchNotes(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	q, err := stringArg(req, "query")
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	q = strings.TrimSpace(q)
	if q == "" {
		return mcplib.NewToolResultError("empty query"), nil
	}
	limit := intArg(req, "limit", 20)
	hits, err := s.ix.Search(q, limit)
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	if hits == nil {
		hits = []index.Hit{}
	}
	return jsonResult(hits)
}

func (s *Server) handleSurfaceNote(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	rel, err := stringArg(req, "path")
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	rel = ensureHTMLSuffix(rel)

	res, err := buildSurface(s.v, s.ix, rel, time.Now())
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	return jsonResult(res)
}

// --- surface helper ---------------------------------------------------------

// surfaceResult mirrors the JSON shape /api/surface returns. We rebuild
// candidate lists here because the server package's helpers aren't importable
// (and pulling embeddings into MCP would force the embed package on every
// surface call — keep MCP cheap).
type surfaceResult struct {
	Current   string           `json:"current"`
	Backlinks []string         `json:"backlinks"`
	Scored    []surface.Scored `json:"scored"`
}

// buildSurface runs the ACT-R activation engine with the queried note as the
// sole focus source (no session model over MCP). Base level comes from the
// access log; spreading from backlink + co-access edges. Semantic similarity is
// left at 0 — embeddings live behind the optional embed package and pulling it
// into MCP would force the ONNX dependency on every surface call. The engine
// degrades gracefully without it. Deterministic (zero noise) so an MCP caller
// gets a reproducible result.
func buildSurface(v *vault.Vault, ix *index.Index, cur string, now time.Time) (*surfaceResult, error) {
	notes, err := v.List()
	if err != nil {
		return nil, err
	}
	p := surface.DefaultParams()

	back, _ := ix.BacklinksTo(cur)
	forward, _ := ix.LinksFrom(cur)
	linked := map[string]bool{}
	for _, x := range back {
		linked[x] = true
	}
	for _, x := range forward {
		linked[x] = true
	}
	coCounts, _ := ix.CoAccessCount(cur, p.SessionGap)
	hist, _ := ix.AllAccessHistory()

	sources := []surface.Source{{Path: cur, Weight: p.FocusWeight}}
	cands := make([]surface.Candidate, 0, len(notes))
	for _, n := range notes {
		edges := map[string]surface.EdgeSet{}
		if n.Path != cur {
			edges[cur] = surface.EdgeSet{
				Backlink:      linked[n.Path],
				CoAccessCount: coCounts[n.Path],
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

	scored := surface.Rank(surface.Candidate{Path: cur}, sources, cands, now.Unix(), p, surface.NewNoiser(0, 0, false), nil)
	if len(scored) > p.TopN {
		scored = scored[:p.TopN]
	}
	if back == nil {
		back = []string{}
	}
	return &surfaceResult{
		Current:   cur,
		Backlinks: back,
		Scored:    scored,
	}, nil
}

// --- arg helpers ------------------------------------------------------------

func stringArg(req mcplib.CallToolRequest, key string) (string, error) {
	args, ok := req.Params.Arguments.(map[string]any)
	if !ok {
		return "", fmt.Errorf("missing arguments object")
	}
	raw, ok := args[key]
	if !ok {
		return "", fmt.Errorf("missing %q argument", key)
	}
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%q must be a string", key)
	}
	return s, nil
}

// intArg pulls a number from the request, accepting JSON numbers (float64) or
// integer-typed values. Returns deflt when the key is absent or unparseable —
// these arguments are documented as optional, callers shouldn't be punished
// for sending nothing.
func intArg(req mcplib.CallToolRequest, key string, deflt int) int {
	args, ok := req.Params.Arguments.(map[string]any)
	if !ok {
		return deflt
	}
	raw, ok := args[key]
	if !ok {
		return deflt
	}
	switch v := raw.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	}
	return deflt
}

func ensureHTMLSuffix(rel string) string {
	if !strings.HasSuffix(rel, ".html") {
		return rel + ".html"
	}
	return rel
}

func jsonResult(v any) (*mcplib.CallToolResult, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	return mcplib.NewToolResultText(string(b)), nil
}
