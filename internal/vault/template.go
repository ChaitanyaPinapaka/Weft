package vault

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// TemplateRoot is the vault-relative directory holding templates.
const TemplateRoot = "templates"

// DailyTemplatePath is the conventional vault-relative path Weft looks at
// when EnsureDaily wants a template — "daily/template.html". The handler
// uses Exists(DailyTemplatePath) to decide whether to use it.
const DailyTemplatePath = "daily/template.html"

// IsTemplate reports whether a vault-relative path lives under templates/.
// Pure string check; does not touch the filesystem.
func IsTemplate(rel string) bool {
	rel = strings.TrimPrefix(rel, "/")
	return strings.HasPrefix(rel, TemplateRoot+"/")
}

// ListTemplates returns all .html files under templates/ as Note entries
// (vault-relative paths, sorted). The templates directory is optional:
// a missing directory yields an empty slice, not an error.
func (v *Vault) ListTemplates() ([]Note, error) {
	root := filepath.Join(v.Root, TemplateRoot)
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return []Note{}, nil
		}
		return nil, err
	}

	var notes []Note
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".html") {
			return nil
		}
		rel, _ := filepath.Rel(v.Root, path)
		// Normalize separators so vault-relative paths use forward slashes,
		// matching how the rest of the package addresses notes.
		rel = filepath.ToSlash(rel)
		info, _ := d.Info()
		notes = append(notes, Note{
			Path:    rel,
			Name:    strings.TrimSuffix(rel, ".html"),
			ModTime: info.ModTime(),
			Size:    info.Size(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(notes, func(i, j int) bool { return notes[i].Path < notes[j].Path })
	return notes, nil
}

// NewFromTemplate copies the bytes from templateRel to dstRel. Refuses to
// overwrite an existing dstRel (returns os.ErrExist). The destination's
// parent directories are created as needed (Write already does this).
// Both arguments are vault-relative.
func (v *Vault) NewFromTemplate(templateRel, dstRel string) error {
	// Templates are sources, not sinks — instantiating back into templates/
	// would muddy the predicate that excludes them from the main list.
	if IsTemplate(dstRel) {
		return os.ErrPermission
	}
	if v.Exists(dstRel) {
		return os.ErrExist
	}
	content, err := v.Read(templateRel)
	if err != nil {
		return err
	}
	return v.Write(dstRel, content)
}

// EnsureDailyFromTemplate creates today's daily note. If DailyTemplatePath
// exists, it is copied into the new daily; otherwise the behavior matches
// EnsureDaily exactly (minimal "<h1>YYYY-MM-DD</h1>\n" stub). Returns the
// vault-relative path of today's daily. Never overwrites an existing daily.
func (v *Vault) EnsureDailyFromTemplate(t time.Time) (string, error) {
	rel := v.DailyPath(t)
	if v.Exists(rel) {
		return rel, nil
	}
	if v.Exists(DailyTemplatePath) {
		content, err := v.Read(DailyTemplatePath)
		if err != nil {
			return "", err
		}
		if err := v.Write(rel, content); err != nil {
			return "", err
		}
		return rel, nil
	}
	// Fall back to the same stub EnsureDaily writes. Duplicated intentionally:
	// this file may not modify daily.go, and a one-line stub is cheaper than
	// an indirection that couples the two code paths.
	stub := "<h1>" + t.Format("2006-01-02") + "</h1>\n"
	if err := v.Write(rel, []byte(stub)); err != nil {
		return "", err
	}
	return rel, nil
}
