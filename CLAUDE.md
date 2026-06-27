# CLAUDE.md — Weft

> **Weft** — HTML vault with brain-memory-style surfacing.

## What this is

A local-first personal knowledge tool. The vault is a folder of `.html` files on disk. Notes are authored, captured, and clipped through multiple surfaces (desktop, browser extension, CLI, mobile). The defining feature is **surfacing, not searching**: open any note, and the system shows what your brain would surface right now — backlinks, semantic neighbors, recently co-accessed notes, "this day in past years."

Notes persist forever. The brain decays; the vault does not. Dormancy affects ranking inside the surfacing layer, never retention.

**Where it's heading.** That PKM core is the seed. Weft is growing into the single substrate for personal life (work stays elsewhere): a local-first **personal-context datalake**. What you produce, collect, and think flows in behind the scenes — captures, clips, and, via adapters, your apps' data — as an append-only event log that derives a **context graph any LLM can consume over MCP**. Surfacing turns ambient and push-style: it nudges you; you don't open an app. The long-term bet is an autonomous agent grounded in that context whose *confidence is learned from your own feedback*. Principle: **completeness in the store, tendedness in the view.**

Still a fun project, not a startup — optimized for the one builder using it daily.

## Hard rules

- HTML files are the canonical format for human-authored notes (the human↔LLM view layer). Never introduce a markdown intermediate.
- Nothing is deleted. Notes: dormancy is a ranking signal, not an existential one. The event lake: append-only, never rewritten.
- Surfacing is the primary interface. No search-first UI — the ⌘K palette is a keyboard escape hatch, not a search bar.
- One Go binary for the substrate. No external config files. (A future ML/RL tier may be a separate Python service behind an API — out of the binary, by design.)
- Resist extensibility scaffolding until at least three concrete needs have shown up. The context-graph substrate is the one deliberate, *named* exception — the bet the "personal-context OS" direction rests on.
- Single-user. Multi-device via optional E2EE BYOC sync — file-level, no CRDT, no Weft-run sync server.

## Architecture

- **Daemon** — Go HTTP + WebSocket server on `localhost:7777`. Owns the vault, index, and surfacing logic.
- **Vault** — folder of `.html` files. Sub-folders allowed.
- **Index** — SQLite. FTS5 for lexical search. Embeddings blob for semantic similarity. Backlinks from `<a href>` parsing on save.
- **Surfaces** — thin clients over the daemon API:
  - Desktop: web app at `localhost:7777` (editor, viewer, open-tasks, graph, ⌘K palette)
  - Browser extension: web clipper
  - CLI: `weft capture / clip / weave / import / mcp / sync`
  - Mobile: native macOS + iOS apps (iOS embeds the Go engine via gomobile — a full offline E2EE sync peer)
- **Context-graph substrate** (the datalake) — an append-only **event log** + content-addressed **blob store** (`POST /api/ingest` is the single intake) is the lossless source of truth; deterministic **replay** derives a **context graph** (SQLite `nodes`/`edges` + vectors + FTS) that an LLM consumes over MCP. Capture/clip/save emit events behind the scenes; the derived graph is disposable and rebuildable.

## Roadmap

| Version | Ships |
|---------|-------|
| v0.1 | Vault browser + HTTP daemon ✓ |
| v0.2 | SQLite index, FTS5, backlinks, brain panel ✓ |
| v0.3 | TipTap editor, `[[wikilinks]]`, daily note auto-create ✓ |
| v0.4 | Browser extension (clipper) ✓ |
| v0.5 | MCP server for Claude Code ✓ |
| v0.6 | Mobile (native macOS + iOS) ✓ |
| v0.7 | E2EE BYOC multi-device sync ✓ |
| v0.8 | Tasks, runnable artifacts, ⌘K palette, the Weaver, reinforcement-learned surfacing ✓ |
| v0.9 | Personal-context datalake → context graph (event log ✓, derive ✓); ambient/push capture; LLM consumption over MCP; learned-confidence autonomous agent — *in progress* |

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

- **No markdown-to-HTML conversion layer.** HTML is the format for notes.
- **No search-first UI.** Surfacing is the interface; ⌘K is a palette, not a search bar.
- **No file deletion by the system.** Ever. (Notes and the event lake alike.)
- **No frontend framework on the desktop UI.** Vanilla JS + TipTap only.
- **No CRDT (no Yjs/Automerge).** Multi-device is file-level E2EE sync with conflict copies, not operational merge.
- **No premature plugin system.** (The event-envelope + `/api/ingest` is the one extension seam — adapters are config, not Go.)

## Resolved (were open questions)

- Editor: **TipTap** (not hand-rolled contenteditable).
- Embedding model: **local `all-MiniLM`/`bge` via ONNX** — no API.
- Vault shape: **sub-folders** (`daily/`, `projects/`, `clips/`, plus the substrate's `events/`, `blobs/`).
- Daily note location: **`daily/YYYY-MM-DD.html`.**

## Open questions (the next layer)

- LLM-facing context serialization: **Structured JSON** default, with a **TRON-style compaction behind a format seam**, flipped per-workload on measured token-vs-accuracy.
- The learned-confidence policy: calibrated selective autonomy (contextual bandit + conformal), trained on commit-gate feedback — bootstrap heuristic first, learned gate later.
- Event-file batching at sensor scale (the substrate's one real scaling risk — time-bucket, don't per-sample).

## Why these choices

- **HTML over markdown:** LLMs author rich content cleanly in HTML. Notes are publishable as-is. Web pages are already in the native format so clipping is `cp`.
- **Surfacing over searching:** cue-driven, associative recall — closest analog to how brain memory actually works.
- **Single daemon + thin clients:** one source of truth, easy to add surfaces without touching core.
- **Sync is BYOC + E2EE:** your data, your cloud (R2/S3/B2/Minio), no Weft server — file-level so it stays simple.
- **Event log as the source of truth:** the derived graph is disposable; completeness lives in the immutable lake, and any LLM-facing view is a projection over it.

## File layout

```
.
├── CLAUDE.md  Makefile  README.md  go.mod
├── cmd/weft/main.go
├── internal/
│   ├── vault/        # .html file operations, atomic crash-safe writes
│   ├── index/        # SQLite: FTS5, embeddings, backlinks, tasks, graph nodes/edges
│   ├── surface/      # brain-panel ranking (ACT-R) + reinforcement
│   ├── event/        # append-only event log + content-addressed blobs (the lake)
│   ├── derive/       # replay events → context graph
│   ├── weaver/       # propose wikilinks between unlinked-but-similar notes
│   ├── clip/ embed/ graph/ mcp/ sync/ wiki/ noteid/ imports/ core/
│   └── server/       # HTTP daemon + ambient push surface
├── web/              # desktop UI (vanilla JS + TipTap)
├── ext/              # browser extension (clipper)
├── macos/  ios/      # native apps (iOS embeds the Go engine via gomobile)
├── site/             # tryweft.app — Astro/Starlight landing + docs
└── testdata/vault/   # fixture notes for tests
```
