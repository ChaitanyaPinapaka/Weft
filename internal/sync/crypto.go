package sync

import (
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

// VaultKey is the single 256-bit secret that decrypts a vault's content. It is
// generated once and NEVER uploaded in plaintext; the bucket sees only data
// sealed under keys derived from it.
type VaultKey [32]byte

// NewVaultKey mints a fresh random vault key.
func NewVaultKey() (VaultKey, error) {
	var k VaultKey
	_, err := rand.Read(k[:])
	return k, err
}

// subkey derives a purpose-specific 32-byte key from the vault key via HKDF, so
// the same VK material is never reused across contexts (blob vs manifest vs the
// nonce-derivation key). The info label is the domain separator.
func subkey(vk VaultKey, label string) []byte {
	r := hkdf.New(sha256.New, vk[:], nil, []byte(label))
	out := make([]byte, chacha20poly1305.KeySize)
	io.ReadFull(r, out)
	return out
}

// Cipher seals and opens bytes. Implementations are deterministic per
// (key, plaintext) so blobs can be content-addressed by hash-of-ciphertext.
type Cipher interface {
	Seal(plaintext []byte) []byte
	Open(ciphertext []byte) ([]byte, error)
}

// nopCipher is the identity cipher — used for the plaintext (P1) engine and
// tests, so the same convergence code path runs encrypted or not.
type nopCipher struct{}

func (nopCipher) Seal(p []byte) []byte          { return p }
func (nopCipher) Open(c []byte) ([]byte, error) { return c, nil }

// aeadCipher is XChaCha20-Poly1305 with a SYNTHETIC nonce: nonce = keyed
// BLAKE2b(nonceKey, plaintext)[:24]. Identical plaintext under the same key
// therefore yields identical ciphertext — which lets the engine address blobs
// by hash-of-ciphertext (the bucket key leaks nothing about plaintext, and
// re-storing the same note dedups). Safety: the 192-bit XChaCha nonce is a
// keyed hash of the plaintext, so a given key never reuses a nonce for two
// DIFFERENT plaintexts (the only reuse is identical plaintext → identical
// ciphertext, which is the intended, safe convergent-encryption behavior).
//
// (This is a deliberate, documented refinement of the design's "random nonce":
// content-addressed dedup requires determinism, and a keyed-hash synthetic
// nonce delivers it without the nonce-reuse hazard of a naive fixed nonce.)
type aeadCipher struct {
	a        cipher.AEAD
	nonceKey []byte
}

func newCipher(vk VaultKey, label string) Cipher {
	a, err := chacha20poly1305.NewX(subkey(vk, label+" key"))
	if err != nil {
		panic("sync: XChaCha20 init: " + err.Error()) // key is always 32 bytes
	}
	return &aeadCipher{a: a, nonceKey: subkey(vk, label+" nonce")}
}

func (c *aeadCipher) Seal(plaintext []byte) []byte {
	h, _ := blake2b.New256(c.nonceKey)
	h.Write(plaintext)
	nonce := h.Sum(nil)[:chacha20poly1305.NonceSizeX]
	ct := c.a.Seal(nil, nonce, plaintext, nil)
	return append(nonce, ct...) // nonce || ciphertext
}

func (c *aeadCipher) Open(blob []byte) ([]byte, error) {
	if len(blob) < chacha20poly1305.NonceSizeX {
		return nil, errors.New("sync: ciphertext too short")
	}
	nonce, ct := blob[:chacha20poly1305.NonceSizeX], blob[chacha20poly1305.NonceSizeX:]
	return c.a.Open(nil, nonce, ct, nil)
}

// --- passphrase-wrapped keyfile (headless / self-host unlock) ---------------

// Pinned Argon2id parameters (security review): 256 MiB, 3 passes, 4 lanes.
const (
	argonMemKiB  = 256 * 1024
	argonTime    = 3
	argonThreads = 4
)

// KeyFile holds the vault key wrapped under a passphrase-derived KEK. Stored on
// disk for headless unlock; the OS keychain is the preferred store on desktops.
type KeyFile struct {
	Salt    []byte `json:"salt"`
	Wrapped []byte `json:"wrapped"` // nonce || sealed(VK) under the Argon2id KEK
	MemKiB  uint32 `json:"mem_kib"`
	Time    uint32 `json:"time"`
	Threads uint8  `json:"threads"`
}

// WrapVaultKey seals vk under a key derived from passphrase via Argon2id.
func WrapVaultKey(vk VaultKey, passphrase string) (KeyFile, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return KeyFile{}, err
	}
	kek := argon2.IDKey([]byte(passphrase), salt, argonTime, argonMemKiB, argonThreads, chacha20poly1305.KeySize)
	a, err := chacha20poly1305.NewX(kek)
	if err != nil {
		return KeyFile{}, err
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return KeyFile{}, err
	}
	sealed := a.Seal(nonce, nonce, vk[:], nil)
	return KeyFile{Salt: salt, Wrapped: sealed, MemKiB: argonMemKiB, Time: argonTime, Threads: argonThreads}, nil
}

// Unwrap recovers the vault key, or errors if the passphrase is wrong.
func (kf KeyFile) Unwrap(passphrase string) (VaultKey, error) {
	var vk VaultKey
	kek := argon2.IDKey([]byte(passphrase), kf.Salt, kf.Time, kf.MemKiB, kf.Threads, chacha20poly1305.KeySize)
	a, err := chacha20poly1305.NewX(kek)
	if err != nil {
		return vk, err
	}
	if len(kf.Wrapped) < chacha20poly1305.NonceSizeX {
		return vk, errors.New("sync: corrupt keyfile")
	}
	nonce, sealed := kf.Wrapped[:chacha20poly1305.NonceSizeX], kf.Wrapped[chacha20poly1305.NonceSizeX:]
	plain, err := a.Open(nil, nonce, sealed, nil)
	if err != nil {
		return vk, errors.New("sync: wrong passphrase")
	}
	copy(vk[:], plain)
	return vk, nil
}

// MarshalKeyFile / ParseKeyFile are the on-disk JSON form.
func MarshalKeyFile(kf KeyFile) ([]byte, error) { return json.MarshalIndent(kf, "", "  ") }
func ParseKeyFile(data []byte) (KeyFile, error) {
	var kf KeyFile
	err := json.Unmarshal(data, &kf)
	return kf, err
}
