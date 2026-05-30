package sync

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

// Device pairing: hand the vault key to a NEW device without retyping the
// passphrase, safely THROUGH the untrusted bucket.
//
// The new device (initiator) and an already-enrolled device (responder) each
// generate an ephemeral X25519 keypair and exchange public keys via the bucket.
// Both derive the same shared secret (DH), and from it a pairing key that seals
// the vault key for the hop. A short authentication string (SAS) is shown on both
// screens; the human compares them out-of-band, and the responder seals the key
// only after that match is confirmed.
//
// CRITICAL: the SAS is short, so it would be GRINDABLE by an active man-in-the-
// middle that picks its substituted keys AFTER seeing the honest ones — it could
// brute-force keys until both screens show the same code. The defence is a
// commit-reveal nonce round (see pairing_flow.go): each side commits its nonce
// before learning the other's, so neither the honest parties nor a MITM can adapt
// a nonce to a target SAS. The SAS therefore binds at ~10^-8 per attempt, not
// grindable. Do NOT drop the commitment or the nonces from the SAS.

// pairKeypair is an ephemeral X25519 keypair for one pairing exchange.
type pairKeypair struct {
	priv [32]byte
	Pub  [32]byte
}

func newPairKeypair() (pairKeypair, error) {
	var kp pairKeypair
	if _, err := rand.Read(kp.priv[:]); err != nil {
		return kp, err
	}
	pub, err := curve25519.X25519(kp.priv[:], curve25519.Basepoint)
	if err != nil {
		return kp, err
	}
	copy(kp.Pub[:], pub)
	return kp, nil
}

// pairKey derives the shared AEAD key from our private key and the peer's public
// key, bound to the ORDERED transcript (initiator pub, then responder pub) so a
// substituted key yields a different key. Both ends pass the same ordered
// transcript, so both derive the same key.
func pairKey(priv, peerPub, initiatorPub, responderPub [32]byte) ([]byte, error) {
	shared, err := curve25519.X25519(priv[:], peerPub[:])
	if err != nil {
		return nil, err
	}
	salt := append(append([]byte{}, initiatorPub[:]...), responderPub[:]...)
	r := hkdf.New(sha256.New, shared, salt, []byte("weft/v1 pairing"))
	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, err
	}
	return key, nil
}

// pairSAS is the 8-digit short authentication string over both public keys AND
// both nonces. Identical on both ends iff both saw the same keys+nonces. Because
// each side commits its nonce before learning the other's (pairing_flow.go), the
// SAS can't be ground to a target — 8 digits caps a MITM at ~10^-8 per run.
func pairSAS(initiatorPub, responderPub [32]byte, na, nb []byte) string {
	h, _ := blake2b.New256([]byte("weft/v1 pairing sas"))
	h.Write(initiatorPub[:])
	h.Write(responderPub[:])
	h.Write(na)
	h.Write(nb)
	sum := h.Sum(nil)
	return fmt.Sprintf("%08d", binary.BigEndian.Uint64(sum[:8])%100_000_000)
}

// commitNonce binds a committer's public key + nonce. The committer publishes it
// before learning the peer's nonce and reveals the nonce later; the peer checks
// the reveal against this commitment, so the committer can't change its nonce
// after seeing the peer's — the anti-grind guarantee for the SAS.
func commitNonce(pub [32]byte, nonce []byte) []byte {
	h, _ := blake2b.New256([]byte("weft/v1 pairing commit"))
	h.Write(pub[:])
	h.Write(nonce)
	return h.Sum(nil)
}

// sealVaultKey wraps the vault key under the pairing key for the bucket hop.
func sealVaultKey(pk []byte, vk VaultKey) ([]byte, error) {
	a, err := chacha20poly1305.NewX(pk)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return a.Seal(nonce, nonce, vk[:], nil), nil
}

// openVaultKey unwraps the vault key handed over during pairing.
func openVaultKey(pk, sealed []byte) (VaultKey, error) {
	var vk VaultKey
	a, err := chacha20poly1305.NewX(pk)
	if err != nil {
		return vk, err
	}
	if len(sealed) < chacha20poly1305.NonceSizeX {
		return vk, errors.New("sync: short pairing blob")
	}
	nonce, ct := sealed[:chacha20poly1305.NonceSizeX], sealed[chacha20poly1305.NonceSizeX:]
	plain, err := a.Open(nil, nonce, ct, nil)
	if err != nil {
		return vk, errors.New("sync: pairing unwrap failed (wrong device or tampered)")
	}
	copy(vk[:], plain)
	return vk, nil
}
