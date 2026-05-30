package sync

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Per-device identity and the authenticated HEAD.
//
// Blobs and manifests are already sealed under the vault key (NewEncrypted), so
// an untrusted cloud can neither read nor forge content. The one piece of sync
// metadata the content AEAD doesn't cover is the HEAD pointer — a plaintext
// integer the cloud could forge, or roll back to freeze/hide a device's recent
// generations. This file closes that gap:
//
//   - Each device holds an Ed25519 signing key. The private half never leaves the
//     device; the public half is published in a registry (sealed under the vault
//     key) so peers can verify that device's HEADs.
//   - HEAD becomes a signed object binding (device, generation, manifest hash), so
//     the cloud can't forge it or point a real device's HEAD at a different or
//     rolled-back manifest. Combined with the monotonic per-peer high-water mark
//     (LastSeen), a device that has seen generation N rejects any later HEAD below
//     N — rollback is detected.
//
// Known limit (fundamental to dumb storage): a brand-new device with no
// high-water mark can still be FROZEN at an old, internally-consistent snapshot
// by a malicious cloud serving stale-but-validly-signed HEADs. Detecting that
// needs a trusted monotonic anchor a plain bucket can't provide.

// deviceRecord binds a device id to its signing pubkey. Stored sealed at
// meta/devices/<id> — one object per device (no shared-object write contention,
// matching the per-device manifest layout). Sealing under the vault key means the
// cloud can neither forge a record (no key) nor read device labels.
type deviceRecord struct {
	DeviceID DeviceID `json:"device"`
	SignPub  []byte   `json:"sign_pub"`
	Label    string   `json:"label,omitempty"`
	Created  int64    `json:"created,omitempty"`
}

func deviceKey(dev DeviceID) string { return "meta/devices/" + string(dev) }

// signedHead is the authenticated HEAD pointer: a device's latest generation,
// bound to the exact manifest bytes by hash and signed by the device key. It
// replaces the bare integer HEAD.
type signedHead struct {
	Gen          uint64 `json:"gen"`
	ManifestHash string `json:"manifest_sha256"`
	Sig          []byte `json:"sig"`
}

// headMessage is the canonical signed payload: domain-separated and binding
// device + generation + manifest hash, so a signature can't be lifted to another
// device's slot, another generation, or another manifest.
func headMessage(dev DeviceID, gen uint64, manifestHash string) []byte {
	return []byte(fmt.Sprintf("weft/head/v1\x00%s\x00%d\x00%s", dev, gen, manifestHash))
}

func signHead(priv ed25519.PrivateKey, dev DeviceID, gen uint64, manifestHash string) signedHead {
	return signedHead{
		Gen:          gen,
		ManifestHash: manifestHash,
		Sig:          ed25519.Sign(priv, headMessage(dev, gen, manifestHash)),
	}
}

func (h signedHead) verify(pub ed25519.PublicKey, dev DeviceID) bool {
	if len(pub) != ed25519.PublicKeySize {
		return false
	}
	return ed25519.Verify(pub, headMessage(dev, h.Gen, h.ManifestHash), h.Sig)
}

// hashBytes is the manifest-binding hash carried in a signed HEAD: sha256 of the
// exact (sealed) manifest bytes, so HEAD and manifest can't be mismatched.
func hashBytes(sealed []byte) string {
	sum := sha256.Sum256(sealed)
	return hex.EncodeToString(sum[:])
}
