---
title: "Sync"
description: "Bring-your-own-cloud, end-to-end encrypted sync for your Weft vault. The vault key never leaves your device; the cloud holds only ciphertext."
---

Weft syncs your vault through object storage you own, end-to-end encrypted. There is no Weft server and no Weft account. The bucket holds only ciphertext; the key that decrypts it never leaves your devices.

## Why it works this way

Your notes are plain `.html` files. Sync should keep them clean and keep you in control of where they live. So Weft makes two choices:

- **Own your storage** — You point Weft at a bucket you control. Cloudflare R2 by default (zero egress), or AWS S3, Backblaze B2, MinIO, or any S3-compatible storage. Weft runs no server in the middle.
- **Zero-knowledge cloud** — Everything written to the bucket is sealed with XChaCha20-Poly1305. The cloud sees only ciphertext and can decrypt nothing. Your storage credentials decrypt nothing either.

## What it guarantees

| Guarantee | How |
| --- | --- |
| End-to-end encryption | One vault key, generated on your device and never uploaded. Blobs and manifests are sealed with XChaCha20-Poly1305 before they touch the bucket. |
| No silent data loss | File-level sync with version vectors (clock-independent). When two devices change the same note concurrently, the result is a conflict-copy, never an overwrite. |
| Deletes are recoverable | A deletion during sync moves the note to a `.trash` folder rather than removing it. Nothing is ever gone. |
| Integrity vs a malicious cloud | A per-device Ed25519-signed HEAD is bound to the manifest by hash, alongside a sealed device registry and rollback detection. The cloud cannot forge, splice, or silently roll back your history. |

:::note[Index is local]
The SQLite index, FTS5 search, embeddings, and backlinks are derived data and are never synced. Each device rebuilds them from the `.html` files.
:::

## Providers

Choose a provider with `--provider`. R2 is the default; S3, B2, MinIO, and any S3-compatible bucket work the same way.

| Provider | Flag | Notes |
| --- | --- | --- |
| Cloudflare R2 | `--provider r2` | Default. Zero egress fees. |
| AWS S3 | `--provider aws` | Use `--region`. |
| Backblaze B2 | `--provider b2` | S3-compatible endpoint. |
| MinIO / self-host | `--provider minio` | Set `--endpoint` and often `--path-style`. |
| Local filesystem | `--provider fs` | For testing; set `--fs-path`. |

## Set it up

First, validate your bucket credentials and connectivity. This touches no data and writes nothing.

```sh
# verify creds + connectivity before writing anything
weft sync check --provider r2 \
  --endpoint https://<account>.r2.cloudflarestorage.com \
  --bucket my-weft-vault \
  --access-key <key> --secret <secret>
```

Once `check` passes, initialize sync on this first device. This generates the vault key locally, seals the device registry, and does the first push.

```sh
# R2 example — sets up E2EE sync on your own bucket
weft sync init --provider r2 \
  --endpoint https://<account>.r2.cloudflarestorage.com \
  --bucket my-weft-vault \
  --access-key <key> --secret <secret>
```

:::note[Keys at rest]
After `init`, the vault key is cached in your OS keychain (macOS Keychain, Linux Secret Service, Windows Credential Manager), so the daemon and `weft sync` need no passphrase on this device. For headless hosts, `WEFT_SECRET_STORE=file` forces a `0600` file fallback.
:::

## Day to day

When the daemon is running, editing syncs automatically — there is nothing to remember.

```sh
# the daemon converges in the background while it runs
weft serve ~/notes
```

If the daemon is not running, run one convergence cycle by hand. It pushes local changes and pulls remote ones in a single pass.

```sh
# one push + pull cycle
weft sync
```

## Recovery

Print the vault's 24-word recovery phrase and keep it somewhere safe. If you ever lose every device, it rebuilds the vault from the bucket.

```sh
# print the 24-word phrase
weft sync recovery

# rebuild a vault from the phrase
weft sync recover --phrase "word1 word2 ... word24"
```

## Add more devices

You do not enroll the second device here. Pairing hands the vault key device-to-device — resistant to a man-in-the-middle, even the cloud — without retyping a passphrase. See [Devices](/devices) for `weft sync pair`, `weft sync pair-approve`, and the passphrase-based `weft sync join`.
