---
title: "Devices & recovery"
description: "Enroll devices with weft sync pair (no passphrase) or join, compare the 8-digit SAS, cache the key in the keychain, and recover with a 24-word phrase."
---

Once sync is initialized, you can enroll more devices and recover a vault if you lose them all. The vault key is never uploaded; every method below hands it over end-to-end or rebuilds it from a phrase you hold.

:::note[Prerequisite]
Sync must already be set up on at least one device with `weft sync init`. See [Sync](/sync) for the full model. New devices read and write only ciphertext in your own bucket; Weft runs no server.
:::

## Two ways to enroll a device

Both methods end with the same result: the new device holds the vault key and caches it in the OS keychain. They differ only in how the key reaches the new device.

| Method | What you provide | Best when |
| --- | --- | --- |
| `weft sync pair` | Nothing typed on the new device; you confirm an 8-digit code on both screens | You have access to an already-enrolled device |
| `weft sync join` | The shared passphrase | You only have the passphrase, not another device |

Either method can be driven from the CLI or from a GUI. The macOS app has an **Add a Device** sheet that runs the same pairing flow and shows the same 8-digit code to compare; the iPhone enrols by pairing with an already-enrolled device or by reconstructing the vault from the 24-word recovery phrase. The CLI commands below are the canonical description of what those sheets do.

## Pairing without the passphrase

Pairing transfers the vault key from an enrolled device to a new one without anyone typing the passphrase. It is the recommended path.

On the **new** device, start pairing. It prints a pairing code.

```sh
weft sync pair
```

On an **already-enrolled** device, approve that code.

```sh
weft sync pair-approve <code>
```

Both screens then show an **8-digit code** — a short authentication string (SAS). Compare the two codes. If they match, confirm on both devices; if they differ, abort. Only after both sides confirm is the vault key handed over.

:::note[From a GUI]
The macOS app's **Add a Device** sheet and the iPhone's pairing screen run exactly this flow without the terminal: one side starts pairing, the other approves, and both show the same 8-digit SAS to compare side by side. The check below applies the same way — confirm only when the digits match on both screens.
:::

:::caution[Why the SAS matters]
The key handover is end-to-end encrypted and resistant to a man-in-the-middle — including the cloud itself. The 8-digit code is what closes that gap: a network or storage attacker who tried to interpose would produce a different SAS on each screen. Comparing the digits out-of-band (read them aloud, look at both screens) is the step that makes the transfer MITM-resistant. Do not skip it.
:::

## Enrolling with the passphrase

If you do not have a second device on hand, enroll with the shared passphrase instead.

```sh
weft sync join
```

This derives access from the passphrase you set at `weft sync init`. After joining, the device caches the vault key locally just like pairing does.

## After enrollment: the keychain

Once a device is enrolled — by pairing or joining — the vault key is cached in the OS keychain:

| Platform | Store |
| --- | --- |
| macOS | Keychain |
| Linux | Secret Service |
| Windows | Credential Manager |

Because the key is cached, the daemon and `weft sync` need no passphrase on that device going forward. The passphrase is only for the initial `join`; the key itself lives in the keychain, not in your bucket.

:::caution[Headless hosts]
On a server with no keychain, force the on-disk fallback with `WEFT_SECRET_STORE=file`. The key is then written to a `0600` file. Protect that host accordingly — anyone who can read the file can decrypt the vault.
:::

## Recovery: the 24-word phrase

The recovery phrase is your escape hatch if every device is lost. Print it on a device that already has the vault, and store it offline.

```sh
weft sync recovery
```

This prints a **24-word phrase**. Write it down and keep it somewhere physical and private. It can reconstruct the vault key, so treat it with the same care as the key itself — anyone holding it can decrypt your vault.

To rebuild a vault from the phrase on a fresh machine:

```sh
# pass the phrase inline, or via stdin / WEFT_RECOVERY_PHRASE
weft sync recover --phrase "word1 word2 ... word24"
```

Recovery rebuilds the vault from the ciphertext in your bucket using the key the phrase derives. The local SQLite index is not synced; each device rebuilds it from the `.html` files after recovery.

The iPhone uses this same phrase to enrol when you do not have a second device to pair with: enter the 24-word phrase in the app and it reconstructs the vault key on-device, no daemon involved.

## The security model, precisely

- **One key, never uploaded** — The vault key is generated on your first device and never leaves it over the network. Pairing and recovery move or reconstruct that key; the bucket never holds it.
- **Cloud sees only ciphertext** — Blobs and manifests are sealed with XChaCha20-Poly1305. Cloud credentials are separate from the vault key and decrypt nothing. A compromised bucket leaks no plaintext.
- **SAS-checked handover** — Pairing transfers the key end-to-end. The 8-digit SAS on both screens is what makes it man-in-the-middle resistant. Compare before confirming.
- **Tamper detection** — A per-device Ed25519-signed HEAD bound to the manifest by hash, a sealed device registry, and rollback detection mean the cloud cannot forge, splice, or silently roll back your history.

:::caution[Where the trust lives]
Your security rests on three things you control: the passphrase, the keychain (or `0600` file) on each device, and the offline 24-word phrase. Lose all of them and the ciphertext in your bucket is unrecoverable — by design, since the cloud can decrypt nothing.
:::

## See also

[Sync](/sync) for setup and convergence, and the [CLI reference](/cli) for every flag.
