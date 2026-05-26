# CLAUDE.md — Weft

> **Weft** — HTML vault with brain-memory-style surfacing.

## What this is

A local-first personal knowledge tool. The vault is a folder of `.html` files on disk. Notes are authored, captured, and clipped through multiple surfaces (desktop, browser extension, CLI, mobile). The defining feature is **surfacing, not searching**: open any note, and the system shows what your brain would surface right now — backlinks, semantic neighbors, recently co-accessed notes, "this day in past years."

Notes persist forever. The brain decays; the vault does not. Dormancy affects ranking inside the surfacing layer, never retention.

This is a fun project. Not a startup. Optimize for the builder using it daily.

## Hard rules

- HTML files are the canonical format. Never introduce a markdown intermediate.
- Notes are **never deleted** by the system. Dormancy is a ranking signal, not an existential one.
- Surfacing is the primary interface. No global search bar at v0.1.
- One Go binary. No external config files.
- Resist extensibility scaffolding until at least three concrete needs have shown up.
- Single-device, single-user at v0.1. No CRDT, no E2EE, no sync server.

## Architecture

- **Daemon** — Go HTTP + WebSocket server on `localhost:7777`. Owns the vault, index, and surfacing logic.
- **Vault** — folder of `.html` files. Sub-folders allowed.
- **Index** — SQLite. FTS5 for lexical search. Embeddings blob for semantic similarity. Backlinks from `<a href>` parsing on save.
- **Surfaces** — thin clients over the daemon API:
  - Desktop: web app at `localhost:7777`
  - Browser extension: web clipper + brain-panel sidebar
  - CLI: `weft capture`, `weft clip`, MCP server endpoint
  - Mobile: later

## Roadmap

| Version | Ships |
|---------|-------|
| v0.1 | Vault browser + HTTP daemon ✓ |
| v0.2 | SQLite index, FTS5, backlinks, brain panel |
| v0.3 | TipTap editor, `[[wikilinks]]`, daily note auto-create |
| v0.4 | Browser extension (clipper + brain panel sidebar) |
| v0.5 | MCP server for Claude Code |
| v0.6 | Mobile (capture + read) |

## Build & run

```sh
make run              # go run ./cmd/weft serve ~/notes
make build            # → bin/weft
make test             # go test ./...
make lint             # gofmt + golangci-lint
make deps             # go get modernc.org/sqlite && go mod tidy
```

## Principles (Karpathy-flavored)

- **Make it work, make it right, make it fast** — in that order, never in parallel.
- **Every line is technical debt.** When in doubt, delete.
- **Boring code beats clever code.** No metaprogramming, no abstract factories.
- **Concrete before generic.** Write the same thing twice before abstracting.
- **Tight diffs, tight reviews.** One concern per change.
- **Comments explain why, not what.**
- **TDD when feasible.** Write the failing test first; the test is the spec.
- **Read the code yourself.** Don't trust LLM-generated output without reading it line by line.

## Working with the agent

- One change, one PR-shaped diff. Stop and outline if a task balloons past ~150 lines.
- Write the failing test first.
- Do not add dependencies without flagging them explicitly.
- Do not restructure files that weren't part of the task.
- Prefer modifying existing files over creating new ones.
- If an architectural choice is ambiguous, ask before committing to it.
- After implementing, summarize what changed and what's still open.

## Anti-patterns

- **No markdown-to-HTML conversion layer.** HTML is the format.
- **No global search bar at v0.1.** Surfacing is the interface.
- **No file deletion by the system.** Ever.
- **No frontend framework on the desktop UI.** Vanilla JS + TipTap only.
- **No CRDT, no Yjs, no Automerge.** Single-device until it isn't.
- **No premature plugin system.**

## Open questions (decide before locking architecture)

- TipTap vs hand-rolled `contenteditable` + shortcut layer.
- Embedding model: local CPU (`bge-small`, ~30MB) vs API-hosted. Local is the right call.
- Flat vault vs sub-folder hierarchy. Recommend flat at v0.1.
- Daily note location: `daily/YYYY-MM-DD.html` or vault root.

## Why these choices

- **HTML over markdown:** LLMs author rich content cleanly in HTML. Notes are publishable as-is. Web pages are already in the native format so clipping is `cp`.
- **Surfacing over searching:** cue-driven, associative recall — closest analog to how brain memory actually works.
- **Single daemon + thin clients:** one source of truth, easy to add surfaces without touching core.
- **No sync at v0.1:** friction kills fun projects.

## File layout

```
.
├── CLAUDE.md
├── Makefile
├── README.md
├── go.mod
├── cmd/
│   └── weft/main.go
├── internal/
│   ├── vault/        # .html file operations
│   ├── index/        # SQLite, FTS5, embeddings  (v0.2)
│   ├── surface/      # brain panel logic          (v0.2)
│   └── server/       # HTTP daemon
├── web/              # desktop UI assets
├── ext/              # browser extension          (v0.4)
└── testdata/
    └── vault/        # fixture notes for tests
```
