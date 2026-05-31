---
title: "Getting started"
description: "Install one binary, point it at a folder, and you have a vault of plain .html files \u2014 plus a brain panel that surfaces what matters."
---

Install one binary, point it at a folder, and you have a vault. Your notes are plain `.html` files you own — delete Weft and they still open in any browser.

## Install

Weft is a single pure-Go binary for macOS, Linux, and Windows. No runtime dependencies.

### macOS & Linux

```sh
# downloads the right binary for your OS/arch, verifies its SHA-256 against the published checksums
curl -fsSL https://tryweft.app/install.sh | sh
```

### Windows

Download `weft-windows-amd64.exe` and drop it somewhere on your `PATH` (rename to `weft.exe` if you like). See the [install page](/install) for direct download links and checksums.

### From source

Needs [Go](https://go.dev/dl/). The default build covers everything except semantic neighbors:

```sh
make build        # → bin/weft
make build-ort    # adds local CPU embeddings for semantic neighbors
```

:::note[Embeddings are optional]
The prebuilt binary ships a stub embedder — recency, backlinks, and co-access surfacing work fully without it. Building from source with `make build-ort` swaps in a local CPU model (all-MiniLM-L6-v2, 384-dim) for semantic neighbors. Local, no API. See [Concepts](/concepts).
:::

## Start the daemon

Point Weft at any folder. Sub-folders are allowed; if the folder is empty, it becomes your new vault.

```sh
weft serve ~/notes
```

This starts the local daemon and opens the web app at [the web app on localhost:7777](http://localhost:7777). There are no accounts and no config files, and your notes never leave your machine. (One exception to "fully offline": the in-app editor loads TipTap from a CDN on first use, then caches it — the note data itself stays local.)

## What you see

The web app is the desktop surface over the daemon. Three things to know:

- **Vault browser** — A list of every `.html` file in your folder, sub-folders and all. This is your whole vault — nothing is hidden, nothing is a proprietary blob.
- **Reader** — Open any note to read it rendered. Click again to drop into the editor. The same view serves clipped web pages, since a clip is just HTML on disk.
- **Brain panel** — Alongside each note, Weft surfaces what your brain would right now: backlinks, semantic neighbors, recently co-accessed notes, and "this day in past years."

The brain panel is the point. Instead of a search bar as the front door, opening a note pulls in associated notes ranked by an ACT-R-style memory model — recency and frequency decay like memory, and activation spreads across links. A per-note **explain** toggle shows why each note surfaced, and there's a tuning page for the weights. Full-text search (SQLite FTS5) is there as a fallback, not the entry point. [Concepts](/concepts) goes deep on the model.

## Create and edit a note

New notes are written in a TipTap editor in the web app. HTML is the canonical format throughout — there is no markdown intermediate.

- **Wikilinks.** Type `[[` to get autocomplete over your existing notes. Links you create are parsed from `<a href>` on save and become backlinks on the other end.
- **Daily notes.** A daily note is auto-created at `daily/YYYY-MM-DD.html`. Templates can interpolate values like `{{today}}`.

Everything you write lands as readable HTML in your folder — publishable as-is.

## Capture and clip from the CLI

You don't have to open the app to add to your vault. Two commands cover quick capture and web clipping:

```sh
# append a line to today's daily note
weft capture "remember to wire up the brain panel toggle"

# clip a web page to clips/YYYY-MM-DD-slug.html
weft clip https://example.com/article
```

Both write straight into the same vault the daemon serves, so anything you capture or clip is immediately browseable and surfaceable in the app.

:::note[Importing from elsewhere?]
`weft import <kind> <src>` pulls notes in from markdown, Notion exports, browser bookmark exports, and Apple Notes. See [Import](/import).
:::

## Next

You now have a running vault, a reader, an editor, and capture from the CLI. Two places to go from here:

- [Concepts](/concepts)
- [Sync](/sync)

[Concepts](/concepts) explains surfacing, the activation model, and the optional embeddings. [Sync](/sync) covers optional, end-to-end-encrypted multi-device sync through cloud storage you own.
