package sync

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// drives A to gen 5 and B to fold it, snapshots A's state.json as a "backup",
// then returns everything needed to simulate a post-restore edit.
func restoreSetup(t *testing.T) (va *vault.Vault, ea, eb *Engine, backup []byte, statePath string) {
	t.Helper()
	be, _ := NewFileBackend(t.TempDir())
	va, _ = vault.New(t.TempDir())
	vb, _ := vault.New(t.TempDir())
	ea, _ = New(va, be)
	eb, _ = New(vb, be)
	statePath = filepath.Join(va.Root, ".weft", "sync", "state.json")

	mustWrite(t, va, "n.html", note("W", "<p>v1</p>"))
	ea.Sync()
	backup, _ = os.ReadFile(statePath) // gen 1 backup
	for i := 2; i <= 5; i++ {
		mustWrite(t, va, "n.html", note("W", fmt.Sprintf("<p>v%d</p>", i)))
		ea.Sync()
	}
	eb.Sync()
	if !strings.Contains(read(t, eb.v, "n.html"), "v5") {
		t.Fatal("B should hold v5 before the restore")
	}
	return va, ea, eb, backup, statePath
}

// TestRestoreSurvivesSelfHeadDeletion: a malicious cloud deleting our own HEAD
// pointer must NOT defeat counter recovery — recoverSelf heals from the surviving
// sealed self-manifests, so a genuine post-restore edit still outranks the peer's
// high-water mark and is folded.
func TestRestoreSurvivesSelfHeadDeletion(t *testing.T) {
	va, ea, eb, backup, statePath := restoreSetup(t)

	// Cloud drops A's HEAD pointer (one write within the threat model).
	if err := ea.be.Delete(headKey(ea.st.Device)); err != nil {
		t.Fatal(err)
	}
	// User restores A from the gen-1 backup, then makes a genuine new edit.
	if err := os.WriteFile(statePath, backup, 0o644); err != nil {
		t.Fatal(err)
	}
	ea2, _ := New(va, ea.be)
	mustWrite(t, va, "n.html", note("W", "<p>POST-RESTORE</p>"))
	if _, err := ea2.Sync(); err != nil {
		t.Fatal(err)
	}
	if got := ea2.st.Manifest["W"].VV[ea2.st.Device]; got <= 5 {
		t.Fatalf("post-restore edit must outrank the published high-water 5, got %d", got)
	}
	eb.Sync()
	if !strings.Contains(read(t, eb.v, "n.html"), "POST-RESTORE") {
		t.Fatalf("HEAD deletion + restore silently dropped the post-restore edit: %q", read(t, eb.v, "n.html"))
	}
}

// TestRestoreSurvivesGarbageSelfHead: same as above but the cloud writes garbage
// (not a deletion) at our HEAD key.
func TestRestoreSurvivesGarbageSelfHead(t *testing.T) {
	va, ea, eb, backup, statePath := restoreSetup(t)

	if err := ea.be.Put(headKey(ea.st.Device), []byte("not json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, backup, 0o644); err != nil {
		t.Fatal(err)
	}
	ea2, _ := New(va, ea.be)
	mustWrite(t, va, "n.html", note("W", "<p>POST-RESTORE</p>"))
	if _, err := ea2.Sync(); err != nil {
		t.Fatal(err)
	}
	eb.Sync()
	if !strings.Contains(read(t, eb.v, "n.html"), "POST-RESTORE") {
		t.Fatalf("garbage HEAD + restore silently dropped the post-restore edit: %q", read(t, eb.v, "n.html"))
	}
}

// TestRelabeledManifestCannotLowerHighWater: copying a LOW self-manifest onto a
// HIGH generation key must not trick recoverSelf into healing a too-low MaxSelf —
// it scans manifest CONTENT, not key numbers, so the true high-water survives.
func TestRelabeledManifestCannotLowerHighWater(t *testing.T) {
	va, ea, eb, backup, statePath := restoreSetup(t)

	// Cloud copies A's gen-1 manifest bytes onto a far-higher generation key.
	g1, err := ea.be.Get(fmt.Sprintf("manifest/%s/1.json", ea.st.Device))
	if err != nil {
		t.Fatal(err)
	}
	if err := ea.be.Put(fmt.Sprintf("manifest/%s/99.json", ea.st.Device), g1); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, backup, 0o644); err != nil {
		t.Fatal(err)
	}
	ea2, _ := New(va, ea.be)
	mustWrite(t, va, "n.html", note("W", "<p>POST-RESTORE</p>"))
	if _, err := ea2.Sync(); err != nil {
		t.Fatal(err)
	}
	if got := ea2.st.Manifest["W"].VV[ea2.st.Device]; got <= 5 {
		t.Fatalf("relabel attack lowered the high-water mark: counter %d <= 5", got)
	}
	eb.Sync()
	if !strings.Contains(read(t, eb.v, "n.html"), "POST-RESTORE") {
		t.Fatalf("relabel attack caused a silent drop: %q", read(t, eb.v, "n.html"))
	}
}
