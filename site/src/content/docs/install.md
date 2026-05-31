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

## Next

```sh
weft serve ~/notes   # open your vault at http://localhost:7777
```

[Getting started](/getting-started)

[All docs](/getting-started)
