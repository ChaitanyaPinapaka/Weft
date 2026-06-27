---
title: "CLI reference"
description: "Every weft command and its flags: serve, capture, clip, mcp, import, and the full sync subcommands, plus keychain and recovery-phrase environment notes."
---

Every command the `weft` binary exposes. This is the authoritative reference; where it disagrees with prose elsewhere, trust this page.

## Commands at a glance

| Command | What it does |
| --- | --- |
| `weft serve <vault-path>` | Start the daemon and open the browser (http://localhost:7777) |
| `weft capture "<text>"` | Quick-capture to today's daily note |
| `weft clip <url>` | Clip a URL to the vault as `clips/YYYY-MM-DD-slug.html` |
| `weft weave` | Propose wikilinks between unlinked-but-similar notes (writes a dated digest) |
| `weft mcp <vault-path>` | Run the MCP server on stdio (for Claude Code) |
| `weft import <kind> <src>` | Import notes; kinds: `markdown` `notion` `bookmarks` `apple-notes` |
| `weft sync init` | Set up E2EE multi-device sync on your own cloud |
| `weft sync join` | Enroll this device with the shared passphrase |
| `weft sync pair` | Enroll this device from another one (no passphrase) |
| `weft sync pair-approve <code>` | Approve a pairing request on an enrolled device |
| `weft sync recovery` | Print this vault's 24-word recovery phrase |
| `weft sync recover --phrase "..."` | Rebuild a vault from its recovery phrase |
| `weft sync check` | Verify bucket creds + connectivity (no data touched) |
| `weft sync` | Run one convergence cycle (push + pull) |

## serve

Start the daemon. It owns the vault, builds the index, and serves the web app at http://localhost:7777, opening your browser.

```sh
# start the daemon on a vault folder
weft serve ~/notes
```

## capture

Append text to today's daily note (`daily/YYYY-MM-DD.html`), creating it if needed. The fastest way to get a thought into the vault without leaving the terminal.

```sh
weft capture "idea: surface co-accessed notes on hover"
```

## clip

Fetch a web page and save it to the vault as `clips/YYYY-MM-DD-slug.html`. Because the canonical format is already HTML, clipping is essentially a copy.

```sh
weft clip https://example.com/some-article
```

## weave

Scan the vault for pairs of notes that are semantically close but not yet linked, and write the proposals to a dated digest at `digests/YYYY-MM-DD.html`. The vault tends itself: open the digest and add a `[[wikilink]]` where the connection is real. Idempotent and never destructive — it only writes the digest, never edits your notes. Needs local embeddings (`make build-ort`) to compute similarity.

```sh
weft weave
```

## mcp

Run the MCP server over stdio so Claude Code can read, search, surface, and write notes in your vault.

```sh
weft mcp ~/notes
```

See [the MCP guide](/mcp) for wiring it into Claude Code.

## import

Bring existing notes into the vault. Pick a kind and point it at the source export.

| Kind | Source |
| --- | --- |
| `markdown` | A folder of Markdown files |
| `notion` | A Notion export |
| `bookmarks` | A browser bookmarks HTML export |
| `apple-notes` | Apple Notes |

| Flag | Meaning |
| --- | --- |
| `--force` | Overwrite existing notes |
| `-v <vault>` | Target vault path |

```sh
weft import markdown ~/old-notes --force -v ~/notes
```

More detail in [Import](/import).

## sync

Multi-device sync is optional, bring-your-own-cloud, and end-to-end encrypted. Weft runs no server; your notes pass through object storage you own, and the bucket holds only ciphertext. Each command is below; the convergence cycle and shared flags follow.

### sync init

Set up E2EE sync on the first device. Generates the vault key on your machine — it is never uploaded.

### sync join

Enroll an additional device using the shared passphrase.

### sync pair / pair-approve

Enroll a new device without retyping the passphrase. Run `weft sync pair` on the new device to print a code, then `weft sync pair-approve <code>` on an already-enrolled device. Both screens show an 8-digit code (SAS) you compare and confirm; the vault key is handed over end-to-end, resistant to a man-in-the-middle.

```sh
# on the new device
weft sync pair

# on an already-enrolled device
weft sync pair-approve 7Q3K-2F9P
```

### sync recovery / recover

`weft sync recovery` prints this vault's 24-word recovery phrase. `weft sync recover` rebuilds a vault from it if you lose every device.

```sh
weft sync recovery

weft sync recover --phrase "word1 word2 ... word24"
```

:::note[Phrase via environment or stdin]
`weft sync recover` also reads the phrase from stdin or from the `WEFT_RECOVERY_PHRASE` environment variable, so you need not pass `--phrase` on the command line.
:::

### sync check

Validate bucket credentials and connectivity before touching any data. Run this first.

```sh
weft sync check
```

### sync (convergence cycle)

Run one convergence cycle: push local changes, pull remote changes. A true concurrent edit becomes a conflict-copy — never a silent overwrite.

```sh
weft sync
```

### sync flags

| Flag | Meaning |
| --- | --- |
| `-v <vault>` | Vault path |
| `--passphrase <p>` | Vault passphrase |
| `--provider {r2\|aws\|minio\|b2\|fs}` | Storage backend |
| `--bucket` | Bucket name |
| `--endpoint` | Endpoint URL |
| `--region` | Region |
| `--access-key` | Access key |
| `--secret` | Secret key |
| `--path-style` | Use path-style addressing |
| `--prefix` | Key prefix in the bucket (default `weft/v1`) |
| `--fs-path` | Local path for the `fs` provider |

Full setup walkthrough in [Sync](/sync) and [Devices](/devices).

## Keys, keychain, and environment

:::note[The vault key is cached in the OS keychain after init/join/pair]
(macOS Keychain, Linux Secret Service, Windows Credential Manager), so the daemon and `weft sync` need no passphrase on that device. Set `WEFT_SECRET_STORE=file` to force the on-disk 0600 fallback for headless hosts.
:::

| Variable | Effect |
| --- | --- |
| `WEFT_SECRET_STORE=file` | Force the on-disk key store instead of the OS keychain (for headless hosts) |
| `WEFT_RECOVERY_PHRASE` | Supply the 24-word phrase to `weft sync recover` without `--phrase` |
