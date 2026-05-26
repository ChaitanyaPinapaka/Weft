// Package mcp exposes the Weft vault to MCP clients (Claude Code, etc.) over
// stdio. Five tools are registered: list_notes, read_note, write_note,
// search_notes, surface_note. The transport is JSON-RPC over stdin/stdout —
// any stray write to stdout corrupts the protocol, so this package logs only
// to stderr.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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
}

// --- handlers ---------------------------------------------------------------

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
	Current   string             `json:"current"`
	Backlinks []string           `json:"backlinks"`
	Scored    []surface.Scored   `json:"scored"`
}

// buildSurface assembles surface.Candidate list from vault.List + index
// backlinks (both directions counted in HasBacklink) and runs surface.Rank.
// Similarity is left at 0: embeddings live behind the optional embed package
// and the daemon may not have populated them. The cheap, deterministic signal
// is enough for an MCP caller.
func buildSurface(v *vault.Vault, ix *index.Index, cur string, now time.Time) (*surfaceResult, error) {
	notes, err := v.List()
	if err != nil {
		return nil, err
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

	cands := make([]surface.Candidate, 0, len(notes))
	for _, n := range notes {
		cands = append(cands, surface.Candidate{
			Path:        n.Path,
			Title:       n.Name,
			ModTime:     n.ModTime,
			HasBacklink: linked[n.Path],
		})
	}

	scored := surface.Rank(surface.Candidate{Path: cur}, cands, now)
	const limit = 12
	if len(scored) > limit {
		scored = scored[:limit]
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
