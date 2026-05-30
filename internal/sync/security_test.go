package sync

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"weft/internal/vault"
)

// twoEncrypted wires two E2EE engines sharing one backend under the same vault
// key — the real multi-device shape (signed HEADs + sealed registry + sealed
// content), unlike twoDevices which runs the plaintext nop ciphers.
func twoEncrypted(t *testing.T) (*vault.Vault, *Engine, *vault.Vault, *Engine) {
	t.Helper()
	be, err := NewFileBackend(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	vk, err := NewVaultKey()
	if err != nil {
		t.Fatal(err)
	}
	va, _ := vault.New(t.TempDir())
	vb, _ := vault.New(t.TempDir())
	ea, err := NewEncrypted(va, be, vk)
	if err != nil {
		t.Fatal(err)
	}
	eb, err := NewEncrypted(vb, be, vk)
	if err != nil {
		t.Fatal(err)
	}
	return va, ea, vb, eb
}

func headKey(dev DeviceID) string { return fmt.Sprintf("manifest/%s/HEAD", dev) }

// TestForgedHeadRejected: a cloud that fabricates a HEAD it can't sign is
// rejected — the bogus generation is never folded.
func TestForgedHeadRejected(t *testing.T) {
	va, ea, _, eb := twoDevices(t)
	mustWrite(t, va, "n.html", note("W", "<p>v1</p>"))
	ea.Sync()
	eb.Sync() // B learns A's signing key + folds v1

	// Malicious cloud forges a far-future HEAD for A with a junk signature.
	forged, _ := json.Marshal(signedHead{Gen: 99, ManifestHash: "00", Sig: make([]byte, 64)})
	if err := ea.be.Put(headKey(ea.st.Device), forged); err != nil {
		t.Fatal(err)
	}

	res, err := eb.Sync()
	if err != nil {
		t.Fatal(err)
	}
	if res.Rejected == 0 {
		t.Fatal("forged HEAD must be rejected")
	}
	if eb.st.LastSeen[ea.st.Device] != 1 {
		t.Fatalf("a forged HEAD must not advance the high-water mark: %d", eb.st.LastSeen[ea.st.Device])
	}
}

// TestManifestSwapRejected: a HEAD is bound to its manifest by hash, so a cloud
// that swaps the manifest bytes under a still-valid signed HEAD is caught and the
// manifest is never applied.
func TestManifestSwapRejected(t *testing.T) {
	va, ea, vb, eb := twoDevices(t)
	mustWrite(t, va, "n.html", note("W", "<p>secret</p>"))
	ea.Sync() // gen 1, signed HEAD binds the real manifest hash

	// Cloud replaces A's gen-1 manifest with different bytes (HEAD untouched).
	if err := ea.be.Put(fmt.Sprintf("manifest/%s/1.json", ea.st.Device), []byte("tampered")); err != nil {
		t.Fatal(err)
	}

	res, err := eb.Sync() // B's first fold of A
	if err != nil {
		t.Fatal(err)
	}
	if res.Rejected == 0 {
		t.Fatal("a HEAD pointing at a swapped manifest must be rejected")
	}
	if vb.Exists("n.html") {
		t.Fatal("a tampered manifest must not be applied")
	}
}

// TestRollbackBelowHighWaterIgnored: replaying an OLD, validly-signed HEAD (a
// freeze/rollback attack) does not revert a device that has already seen a newer
// generation.
func TestRollbackBelowHighWaterIgnored(t *testing.T) {
	va, ea, vb, eb := twoDevices(t)
	mustWrite(t, va, "n.html", note("W", "<p>v1</p>"))
	ea.Sync()
	oldHead, err := ea.be.Get(headKey(ea.st.Device)) // a genuine signed gen-1 HEAD
	if err != nil {
		t.Fatal(err)
	}
	eb.Sync() // B at gen 1

	mustWrite(t, va, "n.html", note("W", "<p>v2</p>"))
	ea.Sync() // gen 2
	eb.Sync() // B at gen 2
	if !strings.Contains(read(t, vb, "n.html"), "v2") {
		t.Fatal("B should hold v2 before the rollback")
	}

	// Cloud rolls A's HEAD back to the real gen-1 pointer.
	if err := ea.be.Put(headKey(ea.st.Device), oldHead); err != nil {
		t.Fatal(err)
	}
	if _, err := eb.Sync(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, vb, "n.html"), "v2") {
		t.Fatalf("a rolled-back HEAD must not revert B: %q", read(t, vb, "n.html"))
	}
}

// TestEncryptedRegistryRoundTrips: under real E2EE, signed-HEAD sync converges,
// and the device registry record is sealed at rest (not plaintext) so the cloud
// can neither read device metadata nor forge a verifying key.
func TestEncryptedRegistryRoundTrips(t *testing.T) {
	va, ea, vb, eb := twoEncrypted(t)
	mustWrite(t, va, "n.html", note("W", "<p>e2ee</p>"))
	if _, err := ea.Sync(); err != nil {
		t.Fatal(err)
	}
	if _, err := eb.Sync(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, vb, "n.html"), "e2ee") {
		t.Fatal("encrypted signed-HEAD sync should converge")
	}
	sealed, err := ea.be.Get(deviceKey(ea.st.Device))
	if err != nil {
		t.Fatal(err)
	}
	if json.Valid(sealed) {
		t.Fatal("device record must be sealed at rest, not plaintext JSON the cloud can read/forge")
	}
}
