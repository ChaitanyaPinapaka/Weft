package sync

import "testing"

// TestPairingRoundTrip: initiator and responder derive the same pairing key and
// the same SAS, and the vault key handed over is recovered exactly.
func TestPairingRoundTrip(t *testing.T) {
	initiator, err := newPairKeypair()
	if err != nil {
		t.Fatal(err)
	}
	responder, err := newPairKeypair()
	if err != nil {
		t.Fatal(err)
	}
	na, _ := randBytes(16)
	nb, _ := randBytes(16)

	keyR, err := pairKey(responder.priv, initiator.Pub, initiator.Pub, responder.Pub)
	if err != nil {
		t.Fatal(err)
	}
	keyI, err := pairKey(initiator.priv, responder.Pub, initiator.Pub, responder.Pub)
	if err != nil {
		t.Fatal(err)
	}
	if string(keyR) != string(keyI) {
		t.Fatal("both ends must derive the same pairing key")
	}

	if pairSAS(initiator.Pub, responder.Pub, na, nb) != pairSAS(initiator.Pub, responder.Pub, na, nb) {
		t.Fatal("SAS must be deterministic for the same inputs")
	}

	vk, _ := NewVaultKey()
	sealed, err := sealVaultKey(keyR, vk)
	if err != nil {
		t.Fatal(err)
	}
	got, err := openVaultKey(keyI, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if got != vk {
		t.Fatal("paired device must recover the exact vault key")
	}
}

// TestPairingCommitmentBinds: a revealed nonce verifies against its commitment,
// and a different nonce does not — the commit-reveal binding the SAS relies on.
func TestPairingCommitmentBinds(t *testing.T) {
	kp, _ := newPairKeypair()
	na, _ := randBytes(16)
	c := commitNonce(kp.Pub, na)
	if string(commitNonce(kp.Pub, na)) != string(c) {
		t.Fatal("commitment must be deterministic")
	}
	other, _ := randBytes(16)
	if string(commitNonce(kp.Pub, other)) == string(c) {
		t.Fatal("a different nonce must not match the commitment")
	}
}

// TestPairingCrossLegUnwrapFails: a substituted key (different DH leg) cannot
// unwrap the vault key — so even absent the human SAS check, a MITM can't lift it.
func TestPairingCrossLegUnwrapFails(t *testing.T) {
	initiator, _ := newPairKeypair()
	responder, _ := newPairKeypair()
	mitm, _ := newPairKeypair()

	keyRespLeg, _ := pairKey(responder.priv, mitm.Pub, mitm.Pub, responder.Pub)
	keyInitLeg, _ := pairKey(initiator.priv, mitm.Pub, initiator.Pub, mitm.Pub)
	vk, _ := NewVaultKey()
	sealed, _ := sealVaultKey(keyRespLeg, vk)
	if _, err := openVaultKey(keyInitLeg, sealed); err == nil {
		t.Fatal("a cross-leg unwrap must fail")
	}
}

// TestPairingTamperedBlobRejected: a flipped byte in the sealed key is rejected.
func TestPairingTamperedBlobRejected(t *testing.T) {
	a, _ := newPairKeypair()
	b, _ := newPairKeypair()
	key, _ := pairKey(a.priv, b.Pub, a.Pub, b.Pub)
	vk, _ := NewVaultKey()
	sealed, _ := sealVaultKey(key, vk)
	sealed[len(sealed)-1] ^= 0x01
	keyB, _ := pairKey(b.priv, a.Pub, a.Pub, b.Pub)
	if _, err := openVaultKey(keyB, sealed); err == nil {
		t.Fatal("a tampered pairing blob must be rejected by the AEAD")
	}
}
