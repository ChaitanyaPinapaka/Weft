package mobile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"weft/internal/noteid"
	"weft/internal/sync"
)

// noteWith builds a minimal well-formed note carrying a fixed WeftID, so
// scanLocal (which skips un-stamped notes) picks it up deterministically.
func noteWith(id, title string) []byte {
	return []byte(`<!doctype html><html><head><meta name="weft-id" content="` + id +
		`"></head><body><article><h1>` + title + `</h1></article></body></html>`)
}

// TestConvergenceOverFileBackend is the Phase 0 spike, minus the real-R2 /
// Simulator leg: two "devices" enroll from one recovery phrase and a note
// converges both directions through the real sync engine over a shared
// FileBackend "bucket" — exercising the exact crypto + version-vector merge the
// phone will run.
func TestConvergenceOverFileBackend(t *testing.T) {
	t.Setenv("WEFT_SECRET_STORE", "file") // no macOS keychain in the test

	bucket := t.TempDir()
	vaultA := filepath.Join(t.TempDir(), "A")
	vaultB := filepath.Join(t.TempDir(), "B")

	vk, err := sync.NewVaultKey()
	if err != nil {
		t.Fatal(err)
	}
	phrase, err := sync.VaultKeyMnemonic(vk)
	if err != nil {
		t.Fatal(err)
	}

	// Device A enrolls and writes a note.
	a, err := Configure(vaultA, "fs", bucket, "", "", "", "")
	if err != nil {
		t.Fatalf("configure A: %v", err)
	}
	if err := a.RecoverWithPhrase(phrase); err != nil {
		t.Fatalf("enroll A: %v", err)
	}
	note := noteWith("11111111-2222-3333-4444-555555555555", "from A")
	if noteid.ReadWeftID(note) == "" {
		t.Fatal("fixture note has no readable weft-id")
	}
	if err := a.v.Write("hello.html", note); err != nil {
		t.Fatalf("write A: %v", err)
	}
	if _, err := a.Sync(); err != nil {
		t.Fatalf("sync A: %v", err)
	}

	// Device B enrolls from the SAME phrase against the SAME bucket and pulls.
	b, err := Configure(vaultB, "fs", bucket, "", "", "", "")
	if err != nil {
		t.Fatalf("configure B: %v", err)
	}
	if err := b.RecoverWithPhrase(phrase); err != nil {
		t.Fatalf("enroll B: %v", err)
	}
	if _, err := b.Sync(); err != nil {
		t.Fatalf("sync B: %v", err)
	}
	got, err := b.v.Read("hello.html")
	if err != nil {
		t.Fatalf("B should have pulled hello.html: %v", err)
	}
	if string(got) != string(note) {
		t.Fatalf("A→B content mismatch:\n got: %s\nwant: %s", got, note)
	}

	// Reverse: B writes, A pulls.
	note2 := noteWith("66666666-7777-8888-9999-000000000000", "from B")
	if err := b.v.Write("back.html", note2); err != nil {
		t.Fatalf("write B: %v", err)
	}
	if _, err := b.Sync(); err != nil {
		t.Fatalf("sync B2: %v", err)
	}
	if _, err := a.Sync(); err != nil {
		t.Fatalf("sync A2: %v", err)
	}
	if _, err := os.Stat(filepath.Join(vaultA, "back.html")); err != nil {
		t.Fatalf("A should have pulled back.html: %v", err)
	}
}

// TestFacadeReadCaptureSurface exercises the read/write surface the iOS
// WeftBackend wraps: daily, capture, list, read, log-access, and surface — all
// against a single configured vault (no bucket round-trip needed).
func TestFacadeReadCaptureSurface(t *testing.T) {
	t.Setenv("WEFT_SECRET_STORE", "file")

	bucket := t.TempDir()
	vaultDir := filepath.Join(t.TempDir(), "V")

	vk, err := sync.NewVaultKey()
	if err != nil {
		t.Fatal(err)
	}
	phrase, err := sync.VaultKeyMnemonic(vk)
	if err != nil {
		t.Fatal(err)
	}

	s, err := Configure(vaultDir, "fs", bucket, "", "", "", "")
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	defer s.Close()
	if err := s.RecoverWithPhrase(phrase); err != nil {
		t.Fatalf("enroll: %v", err)
	}

	daily, err := s.Daily()
	if err != nil || daily == "" {
		t.Fatalf("daily: %q err=%v", daily, err)
	}

	capPath, err := s.Capture("a passing thought")
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if capPath != daily {
		t.Fatalf("capture should land in today's daily %q, got %q", daily, capPath)
	}

	raw, err := s.ReadRaw(daily)
	if err != nil || !strings.Contains(raw, "a passing thought") {
		t.Fatalf("ReadRaw should contain the captured text; err=%v raw=%q", err, raw)
	}

	listJSON, err := s.ListNotes()
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	var list []struct{ Path string }
	if err := json.Unmarshal([]byte(listJSON), &list); err != nil {
		t.Fatalf("ListNotes JSON: %v (%s)", err, listJSON)
	}
	found := false
	for _, n := range list {
		if n.Path == daily {
			found = true
		}
	}
	if !found {
		t.Fatalf("ListNotes should include the daily %q; got %s", daily, listJSON)
	}

	if err := s.LogAccess(daily); err != nil {
		t.Fatalf("LogAccess: %v", err)
	}

	surfJSON, err := s.Surface(daily)
	if err != nil {
		t.Fatalf("Surface: %v", err)
	}
	var surf struct {
		Current string `json:"current"`
	}
	if err := json.Unmarshal([]byte(surfJSON), &surf); err != nil {
		t.Fatalf("Surface JSON: %v (%s)", err, surfJSON)
	}
	if surf.Current != daily {
		t.Fatalf("Surface current = %q, want %q", surf.Current, daily)
	}
}

// TestSearchNotes exercises the FTS facade: a captured note is findable, the
// JSON is the daemon's GET /api/search shape ([]index.Hit, PascalCase), and a
// blank query is rejected.
func TestSearchNotes(t *testing.T) {
	t.Setenv("WEFT_SECRET_STORE", "file")

	s, err := Configure(filepath.Join(t.TempDir(), "V"), "fs", t.TempDir(), "", "", "", "")
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	defer s.Close()

	daily, err := s.Capture("the xylophone fund")
	if err != nil {
		t.Fatalf("capture: %v", err)
	}

	hitsJSON, err := s.SearchNotes("xylophone")
	if err != nil {
		t.Fatalf("SearchNotes: %v", err)
	}
	var hits []struct {
		Path    string
		Title   string
		Snippet string
		Score   float64
	}
	if err := json.Unmarshal([]byte(hitsJSON), &hits); err != nil {
		t.Fatalf("SearchNotes JSON: %v (%s)", err, hitsJSON)
	}
	if len(hits) != 1 || hits[0].Path != daily {
		t.Fatalf("SearchNotes should hit the daily %q once; got %s", daily, hitsJSON)
	}
	if !strings.Contains(hits[0].Snippet, "<mark>xylophone</mark>") {
		t.Fatalf("snippet should mark the match; got %q", hits[0].Snippet)
	}

	if _, err := s.SearchNotes("   "); err == nil {
		t.Fatal("SearchNotes should reject a blank query")
	}
}

// TestTrash exercises the human-initiated soft delete: the note's bytes move to
// .trash (never hard-deleted), its index rows go, the response is the daemon's
// {"trashed": path} shape, and traversal / missing paths are rejected.
func TestTrash(t *testing.T) {
	t.Setenv("WEFT_SECRET_STORE", "file")

	vaultDir := filepath.Join(t.TempDir(), "V")
	s, err := Configure(vaultDir, "fs", t.TempDir(), "", "", "", "")
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	defer s.Close()

	rel, err := s.Capture("a quixotic scheme")
	if err != nil {
		t.Fatalf("capture: %v", err)
	}

	out, err := s.Trash(rel)
	if err != nil {
		t.Fatalf("Trash: %v", err)
	}
	var resp struct {
		Trashed string `json:"trashed"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil || resp.Trashed != rel {
		t.Fatalf("Trash should return {\"trashed\": %q}; got %s (err=%v)", rel, out, err)
	}

	// Never-delete invariant: the file left the vault but its bytes live on in .trash.
	if _, err := os.Stat(filepath.Join(vaultDir, rel)); !os.IsNotExist(err) {
		t.Fatalf("note should be gone from the vault; stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(vaultDir, ".trash", rel)); err != nil {
		t.Fatalf("note bytes should survive in .trash: %v", err)
	}

	// The index rows went with it: the note no longer turns up in search.
	hitsJSON, err := s.SearchNotes("quixotic")
	if err != nil {
		t.Fatalf("SearchNotes after trash: %v", err)
	}
	var hits []struct{ Path string }
	if err := json.Unmarshal([]byte(hitsJSON), &hits); err != nil {
		t.Fatalf("SearchNotes JSON: %v (%s)", err, hitsJSON)
	}
	if len(hits) != 0 {
		t.Fatalf("trashed note should not surface in search; got %s", hitsJSON)
	}

	if _, err := s.Trash("../escape.html"); err == nil {
		t.Fatal("Trash should reject path traversal")
	}
	if _, err := s.Trash("no-such-note.html"); err == nil {
		t.Fatal("Trash should reject a missing note")
	}
}

// TestPairingEnrollsAndSyncs drives the SAS pairing wrappers end to end: the
// facade is the new device (initiator); raw internal/sync plays the already-
// enrolled Mac (what `weft sync pair-approve` does). It asserts the two SAS codes
// agree, the phone enrolls, and a note the Mac wrote then converges to the phone.
func TestPairingEnrollsAndSyncs(t *testing.T) {
	t.Setenv("WEFT_SECRET_STORE", "file")

	bucket := t.TempDir()
	vaultMac := filepath.Join(t.TempDir(), "Mac")
	vaultPhone := filepath.Join(t.TempDir(), "Phone")

	vk, err := sync.NewVaultKey()
	if err != nil {
		t.Fatal(err)
	}
	phrase, err := sync.VaultKeyMnemonic(vk)
	if err != nil {
		t.Fatal(err)
	}

	// "Mac" enrolls with the vault key and writes a note.
	mac, err := Configure(vaultMac, "fs", bucket, "", "", "", "")
	if err != nil {
		t.Fatalf("configure mac: %v", err)
	}
	defer mac.Close()
	if err := mac.RecoverWithPhrase(phrase); err != nil {
		t.Fatalf("mac enroll: %v", err)
	}
	if err := mac.v.Write("shared.html", noteWith("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "from the Mac")); err != nil {
		t.Fatal(err)
	}
	if _, err := mac.Sync(); err != nil {
		t.Fatalf("mac sync: %v", err)
	}

	// "Phone" pairs as a new device.
	phone, err := Configure(vaultPhone, "fs", bucket, "", "", "", "")
	if err != nil {
		t.Fatalf("configure phone: %v", err)
	}
	defer phone.Close()
	reqID, err := phone.PairStart()
	if err != nil || reqID == "" {
		t.Fatalf("PairStart: %q %v", reqID, err)
	}

	// The Mac approves — exactly what `weft sync pair-approve <reqID>` runs.
	be, err := sync.NewFileBackend(bucket)
	if err != nil {
		t.Fatal(err)
	}
	appr, err := sync.BeginApprove(be, reqID, vk)
	if err != nil {
		t.Fatalf("BeginApprove: %v", err)
	}

	// Phone polls for the responder key → its SAS.
	var phoneSAS string
	for i := 0; i < 5 && phoneSAS == ""; i++ {
		if phoneSAS, err = phone.PairPoll(); err != nil {
			t.Fatalf("PairPoll: %v", err)
		}
	}
	if phoneSAS == "" {
		t.Fatal("phone never received an SAS")
	}

	// Mac verifies the reveal and computes its SAS — must match.
	ready, err := appr.AwaitReveal()
	if err != nil || !ready {
		t.Fatalf("AwaitReveal: ready=%v err=%v", ready, err)
	}
	if macSAS := appr.SAS(); macSAS != phoneSAS {
		t.Fatalf("SAS mismatch: phone %q vs mac %q", phoneSAS, macSAS)
	}

	// Human confirms the codes match → Mac seals the key; phone finishes.
	if err := appr.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	done := false
	for i := 0; i < 5 && !done; i++ {
		if done, err = phone.PairFinish(); err != nil {
			t.Fatalf("PairFinish: %v", err)
		}
	}
	if !done {
		t.Fatal("phone never finished pairing")
	}

	// The paired phone now syncs and pulls the Mac's note.
	if _, err := phone.Sync(); err != nil {
		t.Fatalf("phone sync: %v", err)
	}
	if _, err := os.Stat(filepath.Join(vaultPhone, "shared.html")); err != nil {
		t.Fatalf("paired phone should have pulled shared.html: %v", err)
	}
}
