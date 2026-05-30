package sync

import (
	"bytes"
	"testing"
)

func TestCipherRoundTripDeterministicTamper(t *testing.T) {
	vk, _ := NewVaultKey()
	c := newCipher(vk, "test")
	pt := []byte("hello secret world")

	ct1 := c.Seal(pt)
	ct2 := c.Seal(pt)
	if !bytes.Equal(ct1, ct2) {
		t.Fatal("seal must be deterministic for content-addressing")
	}
	if bytes.Contains(ct1, pt) {
		t.Fatal("ciphertext leaks plaintext")
	}
	got, err := c.Open(ct1)
	if err != nil || !bytes.Equal(got, pt) {
		t.Fatalf("round-trip failed: %v %q", err, got)
	}
	// Tamper: flipping any byte must fail authentication.
	bad := append([]byte(nil), ct1...)
	bad[len(bad)-1] ^= 1
	if _, err := c.Open(bad); err == nil {
		t.Fatal("tampered ciphertext opened — AEAD integrity broken")
	}
	// A different vault key cannot open it.
	vk2, _ := NewVaultKey()
	if _, err := newCipher(vk2, "test").Open(ct1); err == nil {
		t.Fatal("wrong key opened ciphertext")
	}
	// Domain separation: a different label cannot open it either.
	if _, err := newCipher(vk, "other").Open(ct1); err == nil {
		t.Fatal("wrong label opened ciphertext")
	}
}

func TestKeyFileWrapUnwrap(t *testing.T) {
	vk, _ := NewVaultKey()
	kf, err := WrapVaultKey(vk, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	got, err := kf.Unwrap("correct horse battery staple")
	if err != nil || got != vk {
		t.Fatalf("unwrap with correct passphrase failed: %v", err)
	}
	if _, err := kf.Unwrap("wrong passphrase"); err == nil {
		t.Fatal("unwrap accepted a wrong passphrase")
	}
	// Survives the on-disk JSON round-trip.
	blob, err := MarshalKeyFile(kf)
	if err != nil {
		t.Fatal(err)
	}
	kf2, err := ParseKeyFile(blob)
	if err != nil {
		t.Fatal(err)
	}
	if got2, err := kf2.Unwrap("correct horse battery staple"); err != nil || got2 != vk {
		t.Fatalf("unwrap after marshal round-trip failed: %v", err)
	}
}
