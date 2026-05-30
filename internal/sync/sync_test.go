package sync

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"weft/internal/vault"
)

// note builds a full HTML document carrying weft-id `id` and the given body.
func note(id, body string) []byte {
	return []byte(fmt.Sprintf(
		`<!DOCTYPE html><html><head><meta charset="utf-8"><meta name="weft-id" content="%s"><title>n</title></head><body><article>%s</article></body></html>`,
		id, body))
}

func vaultTemp(t *testing.T) (*vault.Vault, error) {
	t.Helper()
	return vault.New(t.TempDir())
}

func mustWrite(t *testing.T, v *vault.Vault, rel string, content []byte) {
	t.Helper()
	if err := v.Write(rel, content); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, v *vault.Vault, rel string) string {
	t.Helper()
	b, err := v.Read(rel)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// twoDevices wires two vaults sharing one filesystem backend (the "bucket").
func twoDevices(t *testing.T) (*vault.Vault, *Engine, *vault.Vault, *Engine) {
	t.Helper()
	bucketDir := t.TempDir()
	be, err := NewFileBackend(bucketDir)
	if err != nil {
		t.Fatal(err)
	}
	va, _ := vault.New(t.TempDir())
	vb, _ := vault.New(t.TempDir())
	ea, err := New(va, be)
	if err != nil {
		t.Fatal(err)
	}
	eb, err := New(vb, be)
	if err != nil {
		t.Fatal(err)
	}
	return va, ea, vb, eb
}

func TestConvergeCreateAndFastForward(t *testing.T) {
	va, ea, vb, eb := twoDevices(t)

	// A creates a note; B should receive it.
	mustWrite(t, va, "n.html", note("W", "<p>v1</p>"))
	if _, err := ea.Sync(); err != nil {
		t.Fatal(err)
	}
	if _, err := eb.Sync(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, vb, "n.html"), "v1") {
		t.Fatalf("B did not receive v1: %q", read(t, vb, "n.html"))
	}

	// A edits; B fast-forwards (causal, no conflict).
	mustWrite(t, va, "n.html", note("W", "<p>v2</p>"))
	if _, err := ea.Sync(); err != nil {
		t.Fatal(err)
	}
	rb, err := eb.Sync()
	if err != nil {
		t.Fatal(err)
	}
	if len(rb.ConflictCopies) != 0 {
		t.Fatalf("causal edit must not conflict, got %v", rb.ConflictCopies)
	}
	if !strings.Contains(read(t, vb, "n.html"), "v2") {
		t.Fatalf("B did not fast-forward to v2: %q", read(t, vb, "n.html"))
	}
}

// TestEncryptedConvergeAndBucketIsCiphertext runs the same convergence through
// the E2EE engine and asserts the bucket leaks NO plaintext — not the body, not
// the WeftID, not the on-disk path (all live only inside encrypted manifests).
func TestEncryptedConvergeAndBucketIsCiphertext(t *testing.T) {
	bucketDir := t.TempDir()
	be, _ := NewFileBackend(bucketDir)
	vk, _ := NewVaultKey()
	va, _ := vault.New(t.TempDir())
	vb, _ := vault.New(t.TempDir())
	ea, _ := NewEncrypted(va, be, vk)
	eb, _ := NewEncrypted(vb, be, vk)

	mustWrite(t, va, "secretpath.html", note("SECRETID", "<p>SECRETBODY</p>"))
	if _, err := ea.Sync(); err != nil {
		t.Fatal(err)
	}
	if _, err := eb.Sync(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, vb, "secretpath.html"), "SECRETBODY") {
		t.Fatalf("B did not receive the note through E2EE: %q", read(t, vb, "secretpath.html"))
	}

	// Every object in the bucket must be opaque: no plaintext markers anywhere.
	leaks := []string{"SECRETBODY", "SECRETID", "secretpath"}
	filepath.WalkDir(bucketDir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, _ := os.ReadFile(p)
		for _, m := range leaks {
			if strings.Contains(string(data), m) {
				t.Fatalf("plaintext %q leaked into bucket object %s", m, p)
			}
		}
		return nil
	})

	// A device WITHOUT the key sees only undecryptable bytes — gets nothing.
	wrongKey, _ := NewVaultKey()
	vc, _ := vault.New(t.TempDir())
	ec, _ := NewEncrypted(vc, be, wrongKey)
	if _, err := ec.Sync(); err != nil {
		t.Fatal(err)
	}
	if _, err := vc.Read("secretpath.html"); err == nil {
		t.Fatal("a device with the wrong key reconstructed a note")
	}
}

func TestConcurrentEditYieldsOneConflictCopy(t *testing.T) {
	va, ea, vb, eb := twoDevices(t)

	// Shared base: A creates, B receives.
	mustWrite(t, va, "n.html", note("W", "<p>base</p>"))
	ea.Sync()
	eb.Sync()

	// Concurrent offline edits to the SAME note on both devices.
	mustWrite(t, va, "n.html", note("W", "<p>A-edit</p>"))
	ea.Sync() // A publishes its edit
	mustWrite(t, vb, "n.html", note("W", "<p>B-edit</p>"))

	rb, err := eb.Sync() // B publishes its edit, then pulls A's → detects conflict
	if err != nil {
		t.Fatal(err)
	}
	if len(rb.ConflictCopies) != 1 {
		t.Fatalf("want exactly one conflict copy, got %d (%v)", len(rb.ConflictCopies), rb.ConflictCopies)
	}
	// Both edits are preserved across the main note + the conflict copy (which
	// of the two wins the canonical path is deterministic but arbitrary — what
	// matters is that neither is lost).
	bothB := read(t, vb, "n.html") + read(t, vb, rb.ConflictCopies[0])
	if !strings.Contains(bothB, "A-edit") || !strings.Contains(bothB, "B-edit") {
		t.Fatalf("an edit was lost on B: %q", bothB)
	}

	// Drive to convergence: B re-syncs to publish the resolution, then A pulls.
	// A must fast-forward (not spawn a second copy) and receive the same copy.
	eb.Sync()
	ra, err := ea.Sync()
	if err != nil {
		t.Fatal(err)
	}
	if len(ra.ConflictCopies) != 0 {
		t.Fatalf("A should fast-forward the resolved divergence, not re-conflict: %v", ra.ConflictCopies)
	}

	// Converged: both vaults hold the same two notes with the same contents.
	if read(t, va, "n.html") != read(t, vb, "n.html") {
		t.Fatalf("main note diverged:\nA=%q\nB=%q", read(t, va, "n.html"), read(t, vb, "n.html"))
	}
	cp := rb.ConflictCopies[0]
	if read(t, va, cp) != read(t, vb, cp) {
		t.Fatalf("conflict copy diverged:\nA=%q\nB=%q", read(t, va, cp), read(t, vb, cp))
	}
	// Both edits survive somewhere across the two converged files.
	all := read(t, va, "n.html") + read(t, va, cp)
	if !strings.Contains(all, "A-edit") || !strings.Contains(all, "B-edit") {
		t.Fatalf("an edit was lost; converged content: %q", all)
	}
}

// TestSimultaneousDetectionDedupes is the P4 fix for P1's known limitation: when
// BOTH devices detect the same divergence before either publishes a resolution,
// deterministic conflict ids must still yield exactly ONE conflict copy (same
// path, same content) on both, not two.
func TestSimultaneousDetectionDedupes(t *testing.T) {
	va, ea, vb, eb := twoDevices(t)
	mustWrite(t, va, "n.html", note("W", "<p>base</p>"))
	ea.Sync()
	eb.Sync()

	// Both edit offline and each publishes its OWN edit, BEFORE pulling the other.
	mustWrite(t, va, "n.html", note("W", "<p>A-edit</p>"))
	mustWrite(t, vb, "n.html", note("W", "<p>B-edit</p>"))
	// First Sync on each: scanLocal + push (publishes own edit); the pull in the
	// same cycle may already see the peer — so run a second round to let BOTH
	// detect against the other's published edit.
	ea.Sync()
	eb.Sync()
	ra, _ := ea.Sync()
	rb, _ := eb.Sync()

	// Drive to quiescence.
	for i := 0; i < 3; i++ {
		ea.Sync()
		eb.Sync()
	}

	listConflicts := func(v *vault.Vault) []string {
		notes, _ := v.List()
		var c []string
		for _, n := range notes {
			if strings.Contains(n.Path, ".conflict-") {
				c = append(c, n.Path)
			}
		}
		return c
	}
	ca, cb := listConflicts(va), listConflicts(vb)
	if len(ca) != 1 || len(cb) != 1 {
		t.Fatalf("simultaneous detection must dedup to ONE conflict copy each: A=%v B=%v (ra=%v rb=%v)", ca, cb, ra.ConflictCopies, rb.ConflictCopies)
	}
	if ca[0] != cb[0] {
		t.Fatalf("conflict copy path must be deterministic across devices: A=%q B=%q", ca[0], cb[0])
	}
	// Fully converged: identical main + identical copy, both edits present.
	if read(t, va, "n.html") != read(t, vb, "n.html") {
		t.Fatal("main note did not converge")
	}
	if read(t, va, ca[0]) != read(t, vb, cb[0]) {
		t.Fatal("conflict copy did not converge")
	}
	all := read(t, va, "n.html") + read(t, va, ca[0])
	if !strings.Contains(all, "A-edit") || !strings.Contains(all, "B-edit") {
		t.Fatalf("an edit was lost: %q", all)
	}
}
