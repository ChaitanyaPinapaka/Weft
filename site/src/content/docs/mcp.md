---
title: "MCP for Claude Code"
description: "Run weft mcp to expose your vault to Claude Code over stdio \u2014 list, read, search, surface, and write notes from inside Claude Code."
---

Weft ships an MCP server. Point Claude Code at your vault and it can list, read, search, surface, and write notes — directly, without copy-paste.

## What it does

`weft mcp <vault-path>` runs an MCP server over stdio. Claude Code launches it as a subprocess, talks to it on stdin/stdout, and gets access to the same vault the daemon owns: it can read notes, run full-text search, ask for what the brain panel would surface, and write new notes back to disk.

The vault stays plain `.html` on disk. Claude Code is just another surface over the one source of truth.

## Run the server

```sh
# stdio MCP server, scoped to one vault
weft mcp ~/notes
```

You normally don't run this by hand — Claude Code starts it for you once it's registered.

## Register it with Claude Code

Add an MCP server entry that runs `weft mcp` against your vault. In your Claude Code MCP config:

```json
{
  "mcpServers": {
    "weft": {
      "command": "weft",
      "args": ["mcp", "/Users/you/notes"]
    }
  }
}
```

Use the absolute path to your vault. If `weft` isn't on Claude Code's `PATH`, give the full path to the binary as `command` (for example `/usr/local/bin/weft`).

:::note[Restart to pick it up]
After editing the config, restart Claude Code so it spawns the server. The tools then appear under the `weft` server.
:::

## What Claude Code can do

| Capability | Backed by |
| --- | --- |
| List every note | The vault folder on disk |
| Read a note | The `.html` file on disk |
| Search the vault | SQLite FTS5 lexical search |
| Surface related notes | The same signals as the brain panel — backlinks, recency, co-access |
| Write a note | New `.html` in the vault; indexed on save |

:::note[Semantic neighbors need the source build]
Surfacing works fully on recency, backlinks, and co-access in the prebuilt binary. Semantic similarity requires the local embedding model from `make build-ort`. Without it, surfacing still returns useful results — just without the embedding-based neighbors.
:::

## Notes stay safe

Weft never deletes notes. Writes from Claude Code create or update `.html` files; nothing is removed by the system. As with every Weft surface, the vault is yours and remains openable in any browser.

See also [the CLI reference](/cli) for `weft capture` and `weft clip`, and [concepts](/concepts) for how surfacing works.
