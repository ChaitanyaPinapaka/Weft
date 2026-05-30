package sync

import "testing"

// TestPairingRoundTrip: initiator (new device) and responder (enrolled device)
// derive the same pairing key and the same SAS, and the vault key handed over by
// the responder is recovered exactly by the initiator.
func TestPairingRoundTrip(t *testing.T) {
	initiator, err := newPairKeypair()
	if err != nil {
		t.Fatal(err)
	}
	responder, err := newPairKeypair()
	if err != nil {
		t.Fatal(err)
	}

	// Both ends agree on the ordered transcript (initiator pub, responder pub).
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

	if a, b := pairSAS(initiator.Pub, responder.Pub), pairSAS(initiator.Pub, responder.Pub); a != b {
		t.Fatalf("SAS must be deterministic: %s != %s", a, b)
	}

	vk, _ := NewVaultKey()
	sealed, err := sealVaultKey(keyR, vk) // responder seals the vault key
	if err != nil {
		t.Fatal(err)
	}
	got, err := openVaultKey(keyI, sealed) // initiator opens it
	if err != nil {
		t.Fatal(err)
	}
	if got != vk {
		t.Fatal("paired device must recover the exact vault key")
	}
}

// TestPairingMITMMismatchesSAS: a man-in-the-middle that substitutes its own
// public key to each side makes the two SAS codes differ, so the human comparing
// screens detects the attack — and the substituted key can't unwrap the vault key.
func TestPairingMITMMismatchesSAS(t *testing.T) {
	initiator, _ := newPairKeypair()
	responder, _ := newPairKeypair()
	mitm, _ := newPairKeypair()

	// What each honest party actually sees under a double-DH MITM: the initiator
	// believes it's talking to MITM; the responder believes it's talking to MITM.
	sasInitiatorSees := pairSAS(initiator.Pub, mitm.Pub)
	sasResponderSees := pairSAS(mitm.Pub, responder.Pub)
	if sasInitiatorSees == sasResponderSees {
		t.Fatal("MITM substitution must make the two SAS codes differ")
	}

	// And the key the responder seals under (its DH with the MITM) is not the key
	// the initiator derives (its DH with the MITM on the other leg), so even absent
	// the human check, the vault key won't unwrap across the seam.
	keyRespLeg, _ := pairKey(responder.priv, mitm.Pub, mitm.Pub, responder.Pub)
	keyInitLeg, _ := pairKey(initiator.priv, mitm.Pub, initiator.Pub, mitm.Pub)
	vk, _ := NewVaultKey()
	sealed, _ := sealVaultKey(keyRespLeg, vk)
	if _, err := openVaultKey(keyInitLeg, sealed); err == nil {
		t.Fatal("a cross-leg unwrap must fail under MITM")
	}
}

// TestPairingTamperedBlobRejected: a flipped byte in the sealed vault key is
// rejected by the AEAD, not silently accepted as a wrong key.
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
