package sync

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"weft/internal/vault"
)

func rmFile(t *testing.T, root, rel string) {
	t.Helper()
	if err := os.Remove(filepath.Join(root, rel)); err != nil {
		t.Fatalf("rm %s: %v", rel, err)
	}
}

func inTrash(root, rel string) bool {
	_, err := os.Stat(filepath.Join(root, ".trash", rel))
	return err == nil
}

// trashContains reports whether any file anywhere under .trash holds substr.
func trashContains(t *testing.T, v *vault.Vault, substr string) bool {
	t.Helper()
	found := false
	_ = filepath.WalkDir(filepath.Join(v.Root, ".trash"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, _ := os.ReadFile(p)
		if strings.Contains(string(b), substr) {
			found = true
		}
		return nil
	})
	return found
}

// vaultFiles maps each live note's path to its content (skips .trash/.weft via List).
func vaultFiles(t *testing.T, v *vault.Vault) map[string]string {
	t.Helper()
	notes, err := v.List()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, n := range notes {
		out[n.Path] = read(t, v, n.Path)
	}
	return out
}

// syncToFixedPoint runs both engines until their vaults stop changing (or a cap).
func syncToFixedPoint(t *testing.T, ea, eb *Engine) {
	t.Helper()
	for i := 0; i < 8; i++ {
		if _, err := ea.Sync(); err != nil {
			t.Fatal(err)
		}
		if _, err := eb.Sync(); err != nil {
			t.Fatal(err)
		}
	}
}

// TestDeletePropagatesToTrash: deleting a note on A tombstones it; B removes it
// from the vault but PRESERVES the bytes in .trash (never a hard delete).
func TestDeletePropagatesToTrash(t *testing.T) {
	va, ea, vb, eb := twoDevices(t)
	mustWrite(t, va, "n.html", note("W", "<p>v1</p>"))
	ea.Sync()
	eb.Sync()
	if !strings.Contains(read(t, vb, "n.html"), "v1") {
		t.Fatal("B should have received n")
	}

	rmFile(t, va.Root, "n.html") // user deletes on A
	ea.Sync()                    // tombstone + push
	eb.Sync()                    // pull tombstone

	if vb.Exists("n.html") {
		t.Fatal("B should have removed the deleted note from the vault")
	}
	if !inTrash(vb.Root, "n.html") {
		t.Fatal("B must keep the deleted note's bytes in .trash (never hard-delete)")
	}
}

// TestDeleteLosesToEdit: a delete on A concurrent with an edit on B resolves in
// favor of the EDIT — the note is resurrected, not lost.
func TestDeleteLosesToEdit(t *testing.T) {
	va, ea, vb, eb := twoDevices(t)
	mustWrite(t, va, "n.html", note("W", "<p>base</p>"))
	ea.Sync()
	eb.Sync()

	rmFile(t, va.Root, "n.html")                           // A deletes
	ea.Sync()                                              // A tombstones + pushes
	mustWrite(t, vb, "n.html", note("W", "<p>B-edit</p>")) // B edits, concurrently
	eb.Sync()                                              // B pushes edit, pulls tombstone → edit wins

	if !strings.Contains(read(t, vb, "n.html"), "B-edit") {
		t.Fatalf("edit must win over concurrent delete on B: %q", read(t, vb, "n.html"))
	}
	// A pulls B's edit → resurrects the note it had tombstoned.
	ea.Sync()
	if !va.Exists("n.html") || !strings.Contains(read(t, va, "n.html"), "B-edit") {
		t.Fatal("A should resurrect the note from B's concurrent edit")
	}
}

// TestRenameMovesNotDuplicates: a rename (same WeftID, new path) propagates as a
// move — the new path appears and the old path is trashed, not duplicated.
func TestRenameMovesNotDuplicates(t *testing.T) {
	va, ea, vb, eb := twoDevices(t)
	mustWrite(t, va, "a.html", note("W", "<p>body</p>"))
	ea.Sync()
	eb.Sync()
	if !vb.Exists("a.html") {
		t.Fatal("B should have a.html")
	}

	// Rename on A: same WeftID W, new path b.html, old file removed.
	mustWrite(t, va, "b.html", note("W", "<p>body</p>"))
	rmFile(t, va.Root, "a.html")
	ea.Sync()
	eb.Sync()

	if !vb.Exists("b.html") {
		t.Fatal("rename target should arrive on B")
	}
	if vb.Exists("a.html") {
		t.Fatal("old path must be moved away on B, not left as a duplicate")
	}
	if !inTrash(vb.Root, "a.html") {
		t.Fatal("old path's bytes should be preserved in .trash")
	}
}

// TestCounterRegressionRaisesMaxSelf: if a peer's manifest shows this device at
// a higher counter than our (restored/low) state knows, MaxSelf is raised so the
// next local edit issues a counter ABOVE the already-published one — guarding
// against a backup restore silently dropping a genuine new edit.
func TestCounterRegressionRaisesMaxSelf(t *testing.T) {
	va, _ := vaultTemp(t)
	be, _ := NewFileBackend(t.TempDir())
	e, _ := New(va, be)
	dev := e.Device()

	var res Result
	// Peer reports us at counter 100 (we "restored" a state that only knows 0).
	_ = e.foldEntry("X", Entry{WeftID: "X", Path: "x.html", VV: VV{dev: 100}, BlobID: "deadbeef"}, "peer", &res)
	if e.st.MaxSelf < 100 {
		t.Fatalf("MaxSelf should be raised to the peer-observed counter, got %d", e.st.MaxSelf)
	}
	if e.nextSelf() != 101 {
		t.Fatalf("next self counter should clear the published one (101), got %d", e.st.MaxSelf)
	}
}

// TestMaxSelfRecoveredOnReload: a fresh engine on the same vault recovers the
// self high-water mark from the persisted manifest.
func TestMaxSelfRecoveredOnReload(t *testing.T) {
	va, _ := vaultTemp(t)
	be, _ := NewFileBackend(t.TempDir())
	e, _ := New(va, be)
	mustWrite(t, va, "n.html", note("W", "<p>x</p>"))
	if _, err := e.Sync(); err != nil { // bumps MaxSelf to 1, persists state
		t.Fatal(err)
	}
	e2, _ := New(va, be) // reload from state.json
	if got := e2.nextSelf(); got <= 1 {
		t.Fatalf("MaxSelf not recovered on reload: next=%d, want >1", got)
	}
}

// TestTrashPathReuseNeverClobbers: two DISTINCT notes that occupy the same vault
// path at different times and are both deleted must BOTH survive in .trash — the
// second trash must not overwrite the first (never-delete holds inside .trash).
// Reproduces the engine-driven collision the review found.
func TestTrashPathReuseNeverClobbers(t *testing.T) {
	va, ea, vb, eb := twoDevices(t)
	// W occupies slot.html; sync to B; delete on A → B trashes slot.html (= W).
	mustWrite(t, va, "slot.html", note("W", "<p>NOTE-W-IRREPLACEABLE</p>"))
	ea.Sync()
	eb.Sync()
	rmFile(t, va.Root, "slot.html")
	ea.Sync()
	eb.Sync()
	if vb.Exists("slot.html") {
		t.Fatal("W should be removed from B's live vault")
	}
	// A DIFFERENT note V now reuses slot.html; sync; delete → B trashes it again.
	mustWrite(t, va, "slot.html", note("V", "<p>NOTE-V-ALSO-PRECIOUS</p>"))
	ea.Sync()
	eb.Sync()
	rmFile(t, va.Root, "slot.html")
	ea.Sync()
	eb.Sync()

	if !trashContains(t, vb, "NOTE-W-IRREPLACEABLE") {
		t.Fatal("W's bytes must survive in .trash — the second trash must not clobber the first")
	}
	if !trashContains(t, vb, "NOTE-V-ALSO-PRECIOUS") {
		t.Fatal("V's bytes must survive in .trash")
	}
}

// TestRenameRacesEditConverges: a rename (a→b) concurrent with an in-place edit
// of the SAME note (different content) must converge to the SAME vault on both
// devices. The canonical path is the winner's own path, not the local path —
// sourcing it from the local entry leaves the two devices on different filenames
// forever (the divergence the review found on the shipped code).
func TestRenameRacesEditConverges(t *testing.T) {
	va, ea, vb, eb := twoDevices(t)
	mustWrite(t, va, "a.html", note("W", "<p>base</p>"))
	ea.Sync()
	eb.Sync()
	if !vb.Exists("a.html") {
		t.Fatal("B should have a.html")
	}

	// Concurrent: A renames a→b with an edit; B edits a in place. Distinct content.
	mustWrite(t, va, "b.html", note("W", "<p>A-edit</p>"))
	rmFile(t, va.Root, "a.html")
	mustWrite(t, vb, "a.html", note("W", "<p>B-edit</p>"))

	syncToFixedPoint(t, ea, eb)

	fa, fb := vaultFiles(t, va), vaultFiles(t, vb)
	if !reflect.DeepEqual(fa, fb) {
		t.Fatalf("rename-races-edit did not converge:\n A=%v\n B=%v", names(fa), names(fb))
	}
	if len(fa) != 2 { // the deterministic winner + exactly one conflict copy
		t.Fatalf("want winner + 1 conflict copy = 2 notes, got %d: %v", len(fa), names(fa))
	}
	joined := strings.Join(values(fa), "\n")
	if !strings.Contains(joined, "A-edit") || !strings.Contains(joined, "B-edit") {
		t.Fatalf("both concurrent edits must survive the conflict: %q", joined)
	}
}

// TestConcurrentRenameSameContentConverges: both devices rename the same note to
// DIFFERENT names without editing it. Same content is no content conflict, so it
// converges to a single deterministic canonical path (no conflict copy).
func TestConcurrentRenameSameContentConverges(t *testing.T) {
	va, ea, vb, eb := twoDevices(t)
	mustWrite(t, va, "note.html", note("W", "<p>body</p>"))
	ea.Sync()
	eb.Sync()

	mustWrite(t, va, "alpha.html", note("W", "<p>body</p>"))
	rmFile(t, va.Root, "note.html")
	mustWrite(t, vb, "beta.html", note("W", "<p>body</p>"))
	rmFile(t, vb.Root, "note.html")

	syncToFixedPoint(t, ea, eb)

	fa, fb := vaultFiles(t, va), vaultFiles(t, vb)
	if !reflect.DeepEqual(fa, fb) {
		t.Fatalf("concurrent same-content rename did not converge:\n A=%v\n B=%v", names(fa), names(fb))
	}
	if len(fa) != 1 {
		t.Fatalf("same content is not a divergence — want exactly 1 note, got %d: %v", len(fa), names(fa))
	}
}

// TestRestoreDoesNotDropNewEdit: restoring an older state.json backup (low Gen +
// MaxSelf) must NOT cause a genuine post-restore edit to be silently dropped by a
// peer. recoverSelf re-derives the device's high-water marks from its OWN
// published manifest before scanLocal mints a counter, so the new edit issues a
// counter ABOVE the already-published one and the peer fast-forwards to it.
func TestRestoreDoesNotDropNewEdit(t *testing.T) {
	be, _ := NewFileBackend(t.TempDir())
	va, _ := vault.New(t.TempDir())
	vb, _ := vault.New(t.TempDir())
	ea, _ := New(va, be)
	eb, _ := New(vb, be)
	statePath := filepath.Join(va.Root, ".weft", "sync", "state.json")

	mustWrite(t, va, "n.html", note("W", "<p>v1</p>"))
	if _, err := ea.Sync(); err != nil { // Gen 1, MaxSelf 1
		t.Fatal(err)
	}
	backup, err := os.ReadFile(statePath) // the "old backup"
	if err != nil {
		t.Fatal(err)
	}

	for i := 2; i <= 5; i++ { // A keeps working: Gen/MaxSelf climb well past the backup
		mustWrite(t, va, "n.html", note("W", fmt.Sprintf("<p>v%d</p>", i)))
		if _, err := ea.Sync(); err != nil {
			t.Fatal(err)
		}
	}
	eb.Sync()
	if !strings.Contains(read(t, vb, "n.html"), "v5") {
		t.Fatal("B should hold A's latest before the restore")
	}

	// Restore A from the old backup, then make a genuine NEW edit.
	if err := os.WriteFile(statePath, backup, 0o644); err != nil {
		t.Fatal(err)
	}
	ea2, _ := New(va, be)
	mustWrite(t, va, "n.html", note("W", "<p>post-restore</p>"))
	if _, err := ea2.Sync(); err != nil {
		t.Fatal(err)
	}
	if got := ea2.st.Manifest["W"].VV[ea2.st.Device]; got <= 5 {
		t.Fatalf("post-restore edit must issue a counter above the published 5, got %d", got)
	}

	eb.Sync()
	if !strings.Contains(read(t, vb, "n.html"), "post-restore") {
		t.Fatalf("peer silently dropped a genuine post-restore edit (counter regression): %q",
			read(t, vb, "n.html"))
	}
}

func names(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func values(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
