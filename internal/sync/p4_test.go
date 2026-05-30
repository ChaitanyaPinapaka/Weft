package sync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
