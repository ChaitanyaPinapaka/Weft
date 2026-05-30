package sync

import (
	"encoding/json"
	"strings"
	"testing"

	"weft/internal/vault"
)

// TestPairingFlowEndToEnd drives the full bucket-mediated pairing: an enrolled
// device hands the vault key to a new device, which then converges and decrypts
// the vault with NO passphrase typed on it.
func TestPairingFlowEndToEnd(t *testing.T) {
	t.Setenv("WEFT_SECRET_STORE", "file") // don't touch the real OS keychain in tests
	dir := t.TempDir()
	be, _ := NewFileBackend(dir)
	cfg := Config{Provider: "fs", FSPath: dir} // cfg.backend() resolves to the same store

	// Enrolled device A: has the vault key and a note in the bucket.
	vkReal, _ := NewVaultKey()
	va, _ := vault.New(t.TempDir())
	ea, err := NewEncrypted(va, be, vkReal)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, va, "n.html", note("W", "<p>paired-content</p>"))
	if _, err := ea.Sync(); err != nil {
		t.Fatal(err)
	}

	// New device B begins pairing.
	vb, _ := vault.New(t.TempDir())
	p, err := StartPairing(vb, cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Enrolled device approves: posts its key, returns the SAS + finalize.
	sasA, finalize, err := ApprovePairing(be, p.ReqID(), vkReal)
	if err != nil {
		t.Fatal(err)
	}

	// New device fetches the responder key and computes its SAS — must match.
	if ok, err := p.FetchResponderKey(); err != nil || !ok {
		t.Fatalf("new device should see the responder key: ok=%v err=%v", ok, err)
	}
	if p.SAS() != sasA {
		t.Fatalf("SAS must match across devices: new=%s enrolled=%s", p.SAS(), sasA)
	}

	// Human confirmed the codes match → enrolled seals the key; new device finishes.
	if err := finalize(); err != nil {
		t.Fatal(err)
	}
	eng, err := p.Finish()
	if err != nil || eng == nil {
		t.Fatalf("pairing should complete: eng=%v err=%v", eng, err)
	}

	// B converges and decrypts A's content with the paired key — no passphrase.
	if _, err := eng.Sync(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, vb, "n.html"), "paired-content") {
		t.Fatalf("paired device must decrypt the vault: %q", read(t, vb, "n.html"))
	}

	if ids, _ := ListPairingRequests(be); len(ids) != 0 {
		t.Fatalf("pairing artifacts should be cleaned up, found %v", ids)
	}
}

// TestPairingWrongSASKeyDiffers: if the new device pairs against a DIFFERENT
// responder key than the one shown, its SAS differs and the seal won't open —
// the SAS comparison is load-bearing.
func TestPairingMismatchedResponder(t *testing.T) {
	t.Setenv("WEFT_SECRET_STORE", "file")
	dir := t.TempDir()
	be, _ := NewFileBackend(dir)
	cfg := Config{Provider: "fs", FSPath: dir}

	vb, _ := vault.New(t.TempDir())
	p, err := StartPairing(vb, cfg)
	if err != nil {
		t.Fatal(err)
	}
	vkReal, _ := NewVaultKey()
	sasA, finalize, err := ApprovePairing(be, p.ReqID(), vkReal)
	if err != nil {
		t.Fatal(err)
	}
	_ = finalize

	// An attacker overwrites the responder key with its own before B reads it.
	attacker, _ := newPairKeypair()
	bad, _ := json.Marshal(pairKeyMsg{ResponderPub: attacker.Pub[:]})
	if err := be.Put(pairPrefix(p.ReqID())+"key", bad); err != nil {
		t.Fatal(err)
	}
	if _, err := p.FetchResponderKey(); err != nil {
		t.Fatal(err)
	}
	if p.SAS() == sasA {
		t.Fatal("a substituted responder key must change the new device's SAS (human catches it)")
	}
}
