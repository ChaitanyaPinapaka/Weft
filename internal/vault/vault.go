// Package vault provides read/write access to a folder of .html notes on disk.
// The vault is the source of truth. Nothing is ever deleted by the system.
package vault

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Note is a single .html file in the vault.
type Note struct {
	Path    string    // relative path within vault, e.g. "daily/2026-05-25.html"
	Name    string    // human-readable name without extension, e.g. "daily/2026-05-25"
	ModTime time.Time
	Size    int64
}

// Vault is a directory of .html files on disk.
type Vault struct {
	Root string // absolute path to vault directory
}

// New returns a Vault rooted at root. The directory must already exist.
func New(root string) (*Vault, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, err
	}
	return &Vault{Root: abs}, nil
}

// List returns all .html notes in the vault, walking sub-directories.
func (v *Vault) List() ([]Note, error) {
	var notes []Note
	err := filepath.WalkDir(v.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".html") {
			return nil
		}
		rel, _ := filepath.Rel(v.Root, path)
		info, _ := d.Info()
		notes = append(notes, Note{
			Path:    rel,
			Name:    strings.TrimSuffix(rel, ".html"),
			ModTime: info.ModTime(),
			Size:    info.Size(),
		})
		return nil
	})
	return notes, err
}

// Read returns the raw HTML content of a note by its relative path.
func (v *Vault) Read(rel string) ([]byte, error) {
	return os.ReadFile(v.abs(rel))
}

// Write saves HTML content to a note. Creates parent directories as needed.
// Returns os.ErrPermission if rel contains ".." or would escape the vault root.
func (v *Vault) Write(rel string, content []byte) error {
	if strings.Contains(rel, "..") {
		return os.ErrPermission
	}
	full := v.abs(rel)
	if !strings.HasPrefix(full, v.Root+string(filepath.Separator)) {
		return os.ErrPermission
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, content, 0o644)
}

// Exists reports whether a note exists at rel.
func (v *Vault) Exists(rel string) bool {
	_, err := os.Stat(v.abs(rel))
	return err == nil
}

func (v *Vault) abs(rel string) string {
	return filepath.Join(v.Root, filepath.Clean("/"+rel))
}
