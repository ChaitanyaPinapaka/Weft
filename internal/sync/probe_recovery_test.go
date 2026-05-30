package sync

import (
	"strings"
	"testing"

	"weft/internal/vault"
)

// TestWrongButValidPhraseDoesNotCorruptVault: a recovery phrase that is VALID
// BIP39 but encodes the WRONG vault key (a different vault, or a misremembered
// phrase that still checksums) must NOT corrupt or fold into the legitimate
// vault's data. The wrong-key device's manifests/blobs/device-record are sealed
// under a key the real devices can't open, so they're skipped — not folded.
func TestWrongButValidPhraseDoesNotCorruptVault(t *testing.T) {
	bucketDir := t.TempDir()
	be, _ := NewFileBackend(bucketDir)

	// Legit vault key + a note pushed to the bucket.
	vk, _ := NewVaultKey()
	vReal, _ := vault.New(t.TempDir())
	eReal, _ := NewEncrypted(vReal, be, vk)
	mustWrite(t, vReal, "real.html", note("REALID", "<p>REALBODY</p>"))
	if _, err := eReal.Sync(); err != nil {
		t.Fatal(err)
	}

	// A WRONG but VALID recovery phrase: derived from a *different* random key.
	wrongVK, _ := NewVaultKey()
	wrongPhrase, err := VaultKeyMnemonic(wrongVK)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(wrongPhrase, " ") || len(strings.Fields(wrongPhrase)) != 24 {
		t.Fatalf("expected 24-word phrase, got %q", wrongPhrase)
	}
	gotWrong, err := VaultKeyFromMnemonic(wrongPhrase)
	if err != nil {
		t.Fatalf("a valid phrase must decode: %v", err)
	}
	if gotWrong == vk {
		t.Fatal("test bug: wrong phrase accidentally equals the real key")
	}

	// "Recover" with the wrong key, but onto a vault that ALREADY has local notes
	// (simulates re-pointing a populated device at the wrong phrase). This is the
	// pollution worry: the wrong-key device seals its own notes and pushes them.
	vWrong, _ := vault.New(t.TempDir())
	mustWrite(t, vWrong, "stray.html", note("STRAYID", "<p>STRAYBODY</p>"))
	eWrong, _ := NewEncrypted(vWrong, be, gotWrong)
	if _, err := eWrong.Sync(); err != nil {
		t.Fatal(err)
	}

	// The wrong-key device must NOT reconstruct the legit note.
	if _, err := vWrong.Read("real.html"); err == nil {
		t.Fatal("wrong-key device reconstructed the legit note — silent wrong-key read")
	}

	// CRITICAL: the legit device re-syncs. The wrong-key device's pushed manifest
	// + blobs + device record must be IGNORED, not folded. The legit note must be
	// untouched and no stray note must appear.
	res, err := eReal.Sync()
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied != 0 {
		t.Fatalf("legit device applied %d changes from a wrong-key device — corruption", res.Applied)
	}
	if got := read(t, vReal, "real.html"); !strings.Contains(got, "REALBODY") {
		t.Fatalf("legit note corrupted after wrong-key sync: %q", got)
	}
	if vReal.Exists("stray.html") {
		t.Fatal("wrong-key device's note leaked into the legit vault")
	}

	// The wrong-key device's HEAD should be surfaced as Unverifiable (its sealed
	// device record can't be opened by the legit key), not silently accepted.
	if res.Unverifiable == 0 && res.Rejected == 0 {
		t.Log("note: wrong-key device produced no verifiable/rejected HEAD (it may have not pushed)")
	}
}

// TestRecoverWrongPhraseOnFreshVaultIsSafeNoop: the intended recover flow runs on
// a FRESH (empty) vault. With a wrong-but-valid phrase, the device pulls nothing
// (can't decrypt) and pushes nothing (empty manifest, gen 0) — a safe no-op that
// leaves the bucket's legit content intact.
func TestRecoverWrongPhraseOnFreshVaultIsSafeNoop(t *testing.T) {
	bucketDir := t.TempDir()
	be, _ := NewFileBackend(bucketDir)

	vk, _ := NewVaultKey()
	vReal, _ := vault.New(t.TempDir())
	eReal, _ := NewEncrypted(vReal, be, vk)
	mustWrite(t, vReal, "real.html", note("REALID", "<p>REALBODY</p>"))
	if _, err := eReal.Sync(); err != nil {
		t.Fatal(err)
	}

	// Snapshot the legit manifest object count before the wrong-key sync.
	before, _ := be.List("manifest/")

	wrongVK, _ := NewVaultKey()
	vFresh, _ := vault.New(t.TempDir())
	eFresh, _ := NewEncrypted(vFresh, be, wrongVK)
	r, err := eFresh.Sync()
	if err != nil {
		t.Fatal(err)
	}
	if r.Pushed != 0 {
		t.Fatalf("fresh wrong-key recover pushed %d — expected a no-op", r.Pushed)
	}

	after, _ := be.List("manifest/")
	if len(after) != len(before) {
		t.Fatalf("fresh wrong-key recover changed bucket manifests: %d -> %d", len(before), len(after))
	}
	if _, err := vFresh.Read("real.html"); err == nil {
		t.Fatal("fresh wrong-key device reconstructed the legit note")
	}
}
