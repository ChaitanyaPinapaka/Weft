# Weft

HTML vault with brain-memory-style surfacing.

Your notes are `.html` files in a folder you own. Weft browses, indexes, and surfaces them — giving you back what your brain would recall right now, not just what you search for.

## Install

```sh
go install weft@latest
```

Or build from source:

```sh
make build          # → bin/weft
make build-ort      # → bin/weft with local bge-small embeddings (see Embeddings)
```

## Embeddings

Semantic surfacing uses `KnightsAnalytics/all-MiniLM-L6-v2` (384-dim, ~22 MB) via [hugot](https://github.com/knights-analytics/hugot) + ONNX Runtime. The default build ships a stub embedder; `make build-ort` swaps in the real one, which needs two system pieces:

```sh
# macOS
brew install onnxruntime
# then drop libtokenizers.a (from https://github.com/daulet/tokenizers/releases)
# at /usr/lib/tokenizers.a — or anywhere reachable via CGO_LDFLAGS=-L<dir>
```

Override the onnxruntime path with `WEFT_ONNXRUNTIME_LIB=/path/to/libonnxruntime.dylib`. The model itself (~30 MB) downloads to `<vault>/.weft/models/` on first daemon start.

## Usage

```sh
weft serve ~/notes          # start daemon, open browser at localhost:7777
weft capture "quick note"   # capture to today's daily note  (v0.2)
weft clip https://...       # clip a URL to vault             (v0.2)
```

## Vault

A vault is any folder of `.html` files. Sub-folders allowed. Nothing is ever deleted by Weft — dormant notes are ranked lower in surfacing, not removed.

```
~/notes/
  welcome.html
  daily/
    2026-05-25.html
  projects/
    weft.html
```

## Roadmap

| Version | What ships |
|---------|-----------|
| v0.1    | Vault browser + HTTP daemon (this) |
| v0.2    | SQLite index, FTS5 search, backlinks, brain panel |
| v0.3    | TipTap editor, `[[wikilinks]]`, daily note auto-create |
| v0.4    | Browser extension (web clipper + brain panel sidebar) |
| v0.5    | CLI MCP server for Claude Code |
| v0.6    | Mobile (capture + read) |

## Design

- HTML is the canonical format. Never a markdown intermediate.
- Notes persist forever. Dormancy is a ranking signal, not an existential one.
- Surfacing is the primary interface. Search is a fallback.
- Single Go binary. No config files — everything interactive.
- Single device at v0.1. Sync is a later problem.

See `CLAUDE.md` for full architecture and working-with-agent conventions.
