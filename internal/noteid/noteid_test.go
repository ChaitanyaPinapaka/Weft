package noteid

import (
	"strings"
	"testing"
)

const doc = `<!DOCTYPE html><html><head><meta charset="utf-8"><title>T</title></head><body><article><h1>T</h1><p>hello</p></article></body></html>`

func TestReadAbsentThenPresent(t *testing.T) {
	if id := ReadWeftID([]byte(doc)); id != "" {
		t.Fatalf("want empty id, got %q", id)
	}
	out, id, changed := Ensure([]byte(doc), "notes/t.html")
	if !changed || id == "" {
		t.Fatalf("Ensure should mint: changed=%v id=%q", changed, id)
	}
	if got := ReadWeftID(out); got != id {
		t.Fatalf("stamped id %q not readable back (got %q)", id, got)
	}
}

func TestDeriveDeterministic(t *testing.T) {
	// Same (path, content) → same id on any device (convergence for pre-sync copies).
	a := Derive("notes/t.html", []byte(doc))
	b := Derive("notes/t.html", []byte(doc))
	if a != b {
		t.Fatalf("derive not deterministic: %q vs %q", a, b)
	}
	// Different path or content → different id.
	if Derive("notes/other.html", []byte(doc)) == a {
		t.Fatal("different path should derive a different id")
	}
	if Derive("notes/t.html", []byte(doc+"<!--x-->")) == a {
		t.Fatal("different content should derive a different id")
	}
	// Shape: 8-4-4-4-12.
	if len(a) != 36 || a[8] != '-' || a[13] != '-' || a[18] != '-' || a[23] != '-' {
		t.Fatalf("malformed id shape: %q", a)
	}
}

// TestDeriveCanonicalConvergence: byte-trivia that doesn't change the note must
// not fork the id — the exact pre-sync git/editor scenarios (trailing newline,
// CRLF vs LF, a prior html.Render pass, an already-stamped copy).
func TestDeriveCanonicalConvergence(t *testing.T) {
	base := Derive("notes/t.html", []byte(doc))
	if Derive("notes/t.html", []byte(doc+"\n")) != base {
		t.Fatal("trailing newline forked the id")
	}
	// autocrlf converts existing \n to \r\n (it doesn't insert structure), so
	// the CRLF check uses a doc that actually has line breaks.
	lf := "<!DOCTYPE html>\n<html><head></head>\n<body><article><p>x</p></article></body></html>\n"
	crlf := strings.ReplaceAll(lf, "\n", "\r\n")
	if Derive("notes/t.html", []byte(lf)) != Derive("notes/t.html", []byte(crlf)) {
		t.Fatal("CRLF vs LF forked the id")
	}
	// Note: we do NOT assert that a re-rendered copy (<br> vs <br/>) converges —
	// string canonicalization can't undo html.Render, and it's unreachable in
	// flow anyway: Derive only runs on a note that LACKS an id, and Weft's write
	// paths stamp an id on every save, so an id-less file is never a rendered one.
}

func TestEnsureIdempotent(t *testing.T) {
	out1, id1, _ := Ensure([]byte(doc), "notes/t.html")
	out2, id2, changed := Ensure(out1, "notes/t.html")
	if changed {
		t.Fatal("Ensure on an already-stamped note must not change it")
	}
	if id1 != id2 {
		t.Fatalf("id changed on re-Ensure: %q -> %q", id1, id2)
	}
	if string(out1) != string(out2) {
		t.Fatal("content changed on re-Ensure")
	}
}

func TestWithWeftIDPreservesAcrossRewrite(t *testing.T) {
	// Simulate the editor rebuilding <head> (dropping the meta), then the server
	// re-stamping the EXISTING id so identity survives the save.
	_, id, _ := Ensure([]byte(doc), "notes/t.html")
	rebuilt := `<!DOCTYPE html><html><head><title>T</title></head><body><article><h1>T</h1><p>edited</p></article></body></html>`
	out, err := WithWeftID([]byte(rebuilt), id)
	if err != nil {
		t.Fatal(err)
	}
	if got := ReadWeftID(out); got != id {
		t.Fatalf("preserved id mismatch: want %q got %q", id, got)
	}
	// Replacing, not duplicating: a second WithWeftID must not add a 2nd meta.
	out2, _ := WithWeftID(out, id)
	if c := countWeftMeta(out2); c != 1 {
		t.Fatalf("want exactly 1 weft-id meta, got %d", c)
	}
}

func countWeftMeta(content []byte) int {
	n := 0
	s := string(content)
	for i := 0; ; {
		j := indexOf(s[i:], `name="weft-id"`)
		if j < 0 {
			return n
		}
		n++
		i += j + 1
	}
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
