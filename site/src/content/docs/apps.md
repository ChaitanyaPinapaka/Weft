---
title: "Apps & surfaces"
description: "The surfaces over your vault: the desktop web app and graph view, the native macOS menu-bar app, the iPhone offline sync peer, and the browser extension."
---

Weft is one local Go daemon and a set of thin clients over it. Every surface reads and writes the same folder of `.html` files; none of them is the source of truth, and none of them talks to a Weft server, because there isn't one. The exception is the iPhone app, which carries the engine inside it and syncs straight to your own bucket. This page walks through each surface and is honest about how you install it.

:::note[One daemon, many surfaces]
On any machine with the daemon — macOS, Linux, or Windows — the web app, the browser extension, the CLI, and the MCP server are all clients of `http://localhost:7777`. Native GUI apps exist for macOS and iPhone only; Linux and Windows get the CLI, the daemon, and the web app. See [Install](/install).
:::

## The desktop web app

The web app is the primary surface. Start the daemon with `weft serve ~/notes` and it opens at [localhost:7777](http://localhost:7777). It runs in any browser, on any OS that runs the daemon.

- **Vault browser and reader** — Every `.html` file in your folder, sub-folders and all, rendered for reading. A clipped page is just HTML on disk, so it reads the same way a written note does.
- **The editor** — Notes are written in a TipTap editor. Type `[[` for wikilink autocomplete over your existing notes; the links you make are parsed from `<a href>` on save and become backlinks on the other end. A `/` slash-command menu inserts blocks, and selecting text raises a bubble toolbar for inline formatting. Daily notes are auto-created at `daily/YYYY-MM-DD.html`.
- **The brain panel** — Alongside each note, Weft surfaces what your brain would recall right now: backlinks, semantic neighbors, recently co-accessed notes, and "this day in past years," ranked by the activation model. The explain toggle shows why each note surfaced, and a tuning page adjusts the weights.

### The graph view

The web app also has a graph of the whole vault. Nodes are notes, colored by folder and sized by activation — recency and frequency, the same memory signal that drives surfacing. Edges come in two kinds: solid backlink edges from `<a href>`, and fainter semantic-similarity edges drawn from local embeddings. There is a legend, a minimap, and tag and minimum-link filters; hover a node to highlight its neighborhood.

:::note[Semantic edges need the source build]
The faint similarity edges, like semantic neighbors in the brain panel, come from the local embedding model. The prebuilt binary ships a stub embedder, so backlink edges and activation sizing work out of the box, but semantic edges appear only after `make build-ort`. See [Concepts](/concepts).
:::

## The native macOS app

A SwiftUI app wrapping a `WKWebView`, the macOS surface lives in the menu bar and in its own window. It is a client of the same local daemon, so it reads and writes the same vault the web app does.

- **Reader and quick capture** — Read any note, and capture a thought from the menu bar without switching to the browser.
- **Ambient surfacing** — The daemon pushes surfacing to the app over SSE, so related notes come to you as your context shifts, rather than only when you open something.
- **Edit, graph, and trash** — In-app editing, the graph view inside the main window, and a trash for soft deletes. Nothing is hard-deleted: a removed note moves aside and stays recoverable.

### Set up this Mac

The app has a "Set up this Mac" panel that wires the rest of Weft into the machine for you, so you are not leaving a terminal open or copying config by hand. It installs:

- The `weft` CLI.
- The daemon as a per-user launchd **LaunchAgent** — it starts at login and restarts on crash, so the daemon is always there without a terminal window.
- The Chrome extension.
- The MCP server, and it wires your vault into Claude as memory.

### Add a Device

The macOS app also has an "Add a Device" sheet that drives sync pairing from a UI instead of the command line. It runs the same key handover as `weft sync pair`: an 8-digit code (SAS) shows on both screens, and you compare and confirm before the vault key is handed over end-to-end. The comparison is what makes the transfer resistant to a man-in-the-middle, including the cloud. See [Devices & recovery](/devices) for the model behind it.

:::caution[Signed local build, not the App Store]
The macOS app is a signed local build. It is not on the Mac App Store. You build and run it yourself; there is no store listing and no auto-update channel.
:::

## The iPhone app

The iPhone app is the one surface that is not a thin client. It embeds the Go engine via gomobile and holds its own vault and SQLite index, so it works fully offline with **no daemon**. Instead of talking to `localhost:7777`, it is a full end-to-end-encrypted sync peer that syncs directly to your own bucket — the same E2EE model as every other device, with the cloud holding only ciphertext.

- **Read, edit, surface, search** — The full loop on the phone: read and edit notes, full-text search, and surfacing. Semantic neighbors are computed on-device with Apple's native **NLEmbedding**, so there is no API call and nothing leaves the phone.
- **Trash** — Soft delete, recoverable, in keeping with the rest of Weft. Notes are never destroyed.

### Capture surfaces

The point of the phone is getting things into the vault from wherever you are, so capture has several entry points:

| Surface | What it captures |
| --- | --- |
| Share-sheet extension | Text and web URLs shared from other apps |
| Quick Capture & Today widgets | Lock-screen and home-screen capture, plus a Today widget |
| "Capture to Weft" Siri intent | A Shortcuts / Siri action for hands-free capture |
| `weft://` URL scheme | Deep links from other apps and automations |

Whatever you capture lands in the phone's local vault and rides the next sync up to your bucket and out to your other devices.

:::caution[Xcode or TestFlight, not the App Store]
The iPhone app is installed through Xcode or TestFlight. It is not on the App Store. As with the Mac app, you are running a build, not a store download.
:::

## The browser extension

A Manifest V3 extension for Chrome and Firefox that clips the web into your vault and brings surfacing into the page. It talks only to `http://localhost:7777` — your local daemon — and to no third party.

- **Readability clip** — Clip the article you are reading, with Readability extracting the main content out of the page chrome, saved as HTML in your vault.
- **Selection clip** — Right-click a selection and "clip selection" to save just the part you highlighted.
- **Capture overlay** — A quick-capture overlay for dropping a thought into the vault without leaving the tab.
- **Sidebar** — The brain panel as a sidebar, surfacing related notes alongside whatever you are looking at on the web.

Because the canonical format is HTML, a clip is essentially a copy — what you save is a real `.html` file you own, openable in any browser. See [Concepts](/concepts).

## The CLI and MCP server

Two more surfaces run from the terminal rather than a window:

- **CLI** — `weft serve | capture | clip | import | mcp | sync`. Quick-capture, web-clip, and import without opening an app; `weft sync` covers the full sync lifecycle (init, join, pair, pair-approve, recover, check). The [CLI reference](/cli) is authoritative.
- **MCP server** — `weft mcp <vault>` runs an MCP server over stdio so Claude Code and other MCP clients can list, read, search, surface, and write notes. See [MCP for Claude Code](/mcp).

## In short

- **Desktop web app** — Reader, TipTap editor with wikilinks, slash menu and bubble toolbar, daily notes, the brain panel, and the enriched graph view. Runs anywhere the daemon does.
- **macOS app** — Menu-bar reader, quick capture, ambient surfacing over SSE, in-app edit/graph/trash, plus "Set up this Mac" and "Add a Device." Signed local build.
- **iPhone app** — A full offline E2EE sync peer with an embedded engine, on-device NLEmbedding surfacing, and capture via the share sheet, widgets, Siri, and `weft://`. Xcode or TestFlight.
- **Browser extension** — Readability and selection clipping, a capture overlay, and the brain-panel sidebar, talking only to your local daemon.

[Getting started](/getting-started)
[Concepts](/concepts)
