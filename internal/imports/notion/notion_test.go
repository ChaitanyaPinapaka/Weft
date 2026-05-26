package notion

import (
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"weft/internal/vault"
)

const (
	topHash = "abc123def4567890abc123def4567890"
	subHash = "def456abc789012def456abc789012ab"
	dbHash  = "9876fedcba3210fedcba9876fedcba32"
	csvHash = "1111222233334444555566667777aaaa"
)

// writeExport synthesizes a minimal Notion HTML export rooted at dir.
func writeExport(t *testing.T, dir string) {
	t.Helper()

	topName := "My Top Page " + topHash
	subName := "Sub Page " + subHash
	dbName := "Things " + dbHash
	csvName := "Things " + csvHash + ".csv"
	imgRel := topName + "/image.png"

	mustWrite := func(p, body string) {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Top page links to its subpage (URL-encoded as Notion does) and embeds
	// an image from its own subfolder.
	topBody := `<!DOCTYPE html><html><body>` +
		`<a href="` + url.PathEscape(topName) + `/` + url.PathEscape(subName) + `.html">go</a>` +
		`<img src="` + url.PathEscape(topName) + `/image.png">` +
		`<a href="https://example.com">ext</a>` +
		`</body></html>`
	mustWrite(topName+".html", topBody)

	// Sub page lives inside the top page's folder.
	subBody := `<!DOCTYPE html><html><body><p>sub</p></body></html>`
	mustWrite(topName+"/"+subName+".html", subBody)

	// A png asset.
	mustWrite(imgRel, "\x89PNG\r\n\x1a\nFAKE")

	// A database CSV at the top level.
	csvBody := "Name,Status,Count\nAlpha,Open,1\nBeta,Done,2\n"
	mustWrite(csvName, csvBody)

	// A database folder with an item page (exists in real exports).
	itemBody := `<!DOCTYPE html><html><body><p>item</p></body></html>`
	mustWrite(dbName+"/Item One "+csvHash+".html", itemBody)
}

func newVault(t *testing.T) *vault.Vault {
	t.Helper()
	root := t.TempDir()
	v, err := vault.New(root)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestImport_StripsHashFromFilename(t *testing.T) {
	src := t.TempDir()
	writeExport(t, src)
	v := newVault(t)

	rep := Import(src, v, Options{})
	if len(rep.Errors) != 0 {
		t.Fatalf("errors: %v", rep.Errors)
	}

	if !v.Exists("notion/my-top-page.html") {
		t.Fatalf("expected notion/my-top-page.html; vault has: %v", listVault(t, v))
	}
}

func TestImport_RewritesInternalLink(t *testing.T) {
	src := t.TempDir()
	writeExport(t, src)
	v := newVault(t)

	rep := Import(src, v, Options{})
	if len(rep.Errors) != 0 {
		t.Fatalf("errors: %v", rep.Errors)
	}

	data, err := v.Read("notion/my-top-page.html")
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	// Link to the subpage should now point at the slugged location relative
	// to the top page's directory.
	if !strings.Contains(body, `href="my-top-page/sub-page.html"`) {
		t.Errorf("link not rewritten; got:\n%s", body)
	}
	// External link must pass through unchanged.
	if !strings.Contains(body, `href="https://example.com"`) {
		t.Errorf("external link mangled; got:\n%s", body)
	}
}

func TestImport_CSVToHTMLTable(t *testing.T) {
	src := t.TempDir()
	writeExport(t, src)
	v := newVault(t)

	rep := Import(src, v, Options{})
	if len(rep.Errors) != 0 {
		t.Fatalf("errors: %v", rep.Errors)
	}

	data, err := v.Read("notion/things.html")
	if err != nil {
		t.Fatalf("csv output missing: %v (vault has %v)", err, listVault(t, v))
	}
	body := string(data)
	for _, want := range []string{"<table>", "<thead>", "<th>Name</th>", "<th>Status</th>", "<tbody>", "<td>Alpha</td>", "<td>Done</td>"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
}

func TestImport_CopiesAndRewritesAsset(t *testing.T) {
	src := t.TempDir()
	writeExport(t, src)
	v := newVault(t)

	rep := Import(src, v, Options{})
	if len(rep.Errors) != 0 {
		t.Fatalf("errors: %v", rep.Errors)
	}

	if !v.Exists("notion/my-top-page/image.png") {
		t.Errorf("image not copied; vault: %v", listVault(t, v))
	}
	data, _ := v.Read("notion/my-top-page.html")
	if !strings.Contains(string(data), `src="my-top-page/image.png"`) {
		t.Errorf("image src not rewritten; got:\n%s", string(data))
	}
}

func TestImport_RefusesOverwriteWithoutForce(t *testing.T) {
	src := t.TempDir()
	writeExport(t, src)
	v := newVault(t)

	if err := v.Write("notion/my-top-page.html", []byte("ORIGINAL")); err != nil {
		t.Fatal(err)
	}

	rep := Import(src, v, Options{})
	got, _ := v.Read("notion/my-top-page.html")
	if string(got) != "ORIGINAL" {
		t.Errorf("file overwritten without Force; content=%q", string(got))
	}
	found := false
	for _, s := range rep.Skipped {
		if s == "notion/my-top-page.html" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected skip record for notion/my-top-page.html, got %v", rep.Skipped)
	}

	// With Force it should overwrite.
	rep2 := Import(src, v, Options{Force: true})
	if len(rep2.Errors) != 0 {
		t.Fatalf("errors with Force: %v", rep2.Errors)
	}
	got2, _ := v.Read("notion/my-top-page.html")
	if string(got2) == "ORIGINAL" {
		t.Errorf("Force did not overwrite")
	}
}

func TestSlugDirSegment(t *testing.T) {
	cases := map[string]string{
		"My Top Page " + topHash: "my-top-page",
		"Plain Folder":           "plain-folder",
		"":                       "untitled",
	}
	for in, want := range cases {
		if got := slugDirSegment(in); got != want {
			t.Errorf("slugDirSegment(%q) = %q, want %q", in, got, want)
		}
	}
}

func listVault(t *testing.T, v *vault.Vault) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(v.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(v.Root, p)
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	return out
}
