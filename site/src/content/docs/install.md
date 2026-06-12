---
title: "Install"
description: "Install Weft, a single pure-Go binary for macOS, Linux, and Windows. One line, no dependencies, checksum-verified."
---

Weft is a single pure-Go binary for macOS, Linux, and Windows. Install it, point it at a folder, and you have a vault.

## macOS & Linux

```sh
# installs the right binary for your OS/arch, verifies its checksum
curl -fsSL https://tryweft.app/install.sh | sh
```

:::note[Rather not pipe to a shell?]
Reasonable. The installer just downloads the binary below and checks its SHA-256 — you can do the same by hand. The script is readable at [tryweft.app/install.sh](/install.sh).
:::

## Download directly

Pure-Go builds, no runtime dependencies. Checksums: [SHA256SUMS](/dl/latest/SHA256SUMS).

| Platform | Architecture | File |
| --- | --- | --- |
| macOS | Apple Silicon | [`weft-darwin-arm64`](/dl/latest/weft-darwin-arm64) |
| macOS | Intel | [`weft-darwin-amd64`](/dl/latest/weft-darwin-amd64) |
| Linux | arm64 | [`weft-linux-arm64`](/dl/latest/weft-linux-arm64) |
| Linux | x86-64 | [`weft-linux-amd64`](/dl/latest/weft-linux-amd64) |
| Windows | x86-64 | [`weft-windows-amd64.exe`](/dl/latest/weft-windows-amd64.exe) |

### Verify and install by hand

```sh
# macOS Apple Silicon shown; swap the filename for your platform
shasum -a 256 -c <(grep weft-darwin-arm64 SHA256SUMS)
chmod +x weft-darwin-arm64
sudo mv weft-darwin-arm64 /usr/local/bin/weft
```

On Windows, drop `weft-windows-amd64.exe` somewhere on your `PATH` (rename to `weft.exe` if you like).

## From source

Needs [Go](https://go.dev/dl/). The default build covers everything except semantic neighbors:

```sh
make build        # → bin/weft
make build-ort    # adds local CPU embeddings (see Concepts)
```

## Native apps

The binary above is everything you need: on macOS, Linux, and Windows it runs the daemon and serves the [web app](http://localhost:7777) — the desktop surface — at `localhost:7777`. Two platforms additionally have a native app.

### macOS

macOS has a native app (SwiftUI, in the menu bar and a window): reader, quick capture, ambient surfacing pushed live from the daemon, in-app editing, the graph view, and trash. Its **Set up this Mac** panel installs the rest for you — the daemon as a launchd LaunchAgent that starts at login and restarts on crash, the CLI, the Chrome extension, the MCP server — and wires your vault into Claude as memory. So on a Mac you can skip the curl line and let the app place the binary and start the daemon.

It is a signed local build, not an App Store download.

### iPhone

The iPhone app relates to the daemon differently: it does not use one. It is a full, offline sync peer — it embeds the same Go engine, holds its own vault and index on the device, and syncs directly to your own bucket. Read, capture (share sheet, lock-screen widgets, a Siri/Shortcuts intent, a `weft://` URL scheme), surface (on-device embeddings via Apple's NLEmbedding), search, edit, and trash, all without a daemon running anywhere.

It is installed through Xcode or TestFlight, not the App Store.

:::note[How they fit together]
Mac, Linux, and Windows get the binary plus the web app. macOS additionally has the native app, which can install and run the daemon for you. The iPhone app is its own self-contained peer — it talks to your bucket, not to a daemon. If you sync, every one of these is just another device on the same encrypted vault. See [Sync](/sync) and [Devices & recovery](/devices).
:::

## Next

```sh
weft serve ~/notes   # open your vault at http://localhost:7777
```

[Getting started](/getting-started)

[All docs](/getting-started)
