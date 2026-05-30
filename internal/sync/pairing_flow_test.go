package sync

import (
	"encoding/json"
	"strings"
	"testing"

	"weft/internal/vault"
)

// drives the commit-reveal pairing to the point where both sides can show the SAS.
func runPairing(t *testing.T, be Backend, cfg Config, vb *vault.Vault, vk VaultKey) (*Pairing, *PairApproval) {
	t.Helper()
	p, err := StartPairing(vb, cfg)
	if err != nil {
		t.Fatal(err)
	}
	a, err := BeginApprove(be, p.ReqID(), vk) // responder posts key + nonce
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := p.FetchResponderKey(); err != nil || !ok { // initiator reveals its nonce
		t.Fatalf("FetchResponderKey: ok=%v err=%v", ok, err)
	}
	if ok, err := a.AwaitReveal(); err != nil || !ok { // responder verifies the commitment
		t.Fatalf("AwaitReveal: ok=%v err=%v", ok, err)
	}
	return p, a
}

// TestPairingFlowEndToEnd: an enrolled device hands the vault key to a new device,
// which converges and decrypts the vault with NO passphrase typed on it.
func TestPairingFlowEndToEnd(t *testing.T) {
	t.Setenv("WEFT_SECRET_STORE", "file") // don't touch the real OS keychain in tests
	dir := t.TempDir()
	be, _ := NewFileBackend(dir)
	cfg := Config{Provider: "fs", FSPath: dir}

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

	vb, _ := vault.New(t.TempDir())
	p, a := runPairing(t, be, cfg, vb, vkReal)

	if p.SAS() != a.SAS() {
		t.Fatalf("SAS must match across devices: new=%s enrolled=%s", p.SAS(), a.SAS())
	}
	if len(p.SAS()) != 8 {
		t.Fatalf("SAS should be 8 digits, got %q", p.SAS())
	}

	if err := a.Finalize(); err != nil { // human confirmed → seal
		t.Fatal(err)
	}
	eng, err := p.Finish()
	if err != nil || eng == nil {
		t.Fatalf("pairing should complete: eng=%v err=%v", eng, err)
	}
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

// TestPairingMITMMismatchesSAS: a bucket-MITM that substitutes the responder key
// the new device sees produces a different SAS — the human catches it. (The
// commit-reveal round is what stops the MITM from grinding the codes to match.)
func TestPairingMITMMismatchesSAS(t *testing.T) {
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
	a, err := BeginApprove(be, p.ReqID(), vkReal)
	if err != nil {
		t.Fatal(err)
	}
	// MITM overwrites the responder key the initiator will read with its own.
	attacker, _ := newPairKeypair()
	attackerNb, _ := randBytes(16)
	bad, _ := json.Marshal(pairKeyMsg{ResponderPub: attacker.Pub[:], Nb: attackerNb})
	if err := be.Put(pairPrefix(p.ReqID())+"key", bad); err != nil {
		t.Fatal(err)
	}
	if _, err := p.FetchResponderKey(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AwaitReveal(); err != nil {
		t.Fatal(err)
	}
	if p.SAS() == a.SAS() {
		t.Fatal("a substituted responder key must change the SAS the human compares")
	}
}

// TestPairingCommitmentTamperRejected: if the initiator's revealed nonce doesn't
// match its committed value (a rolled/forged request), the responder aborts —
// the commitment is verified, not just present.
func TestPairingCommitmentTamperRejected(t *testing.T) {
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
	a, err := BeginApprove(be, p.ReqID(), vkReal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.FetchResponderKey(); err != nil { // posts the real reveal
		t.Fatal(err)
	}
	// Tamper: overwrite the reveal with a nonce that doesn't match the commitment.
	wrong, _ := json.Marshal(pairReveal{Na: []byte("a-different-nonce")})
	if err := be.Put(pairPrefix(p.ReqID())+"reveal", wrong); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AwaitReveal(); err == nil {
		t.Fatal("a reveal that doesn't match the commitment must be rejected")
	}
}
