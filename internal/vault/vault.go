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
	Path    string // relative path within vault, e.g. "daily/2026-05-25.html"
	Name    string // human-readable name without extension, e.g. "daily/2026-05-25"
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

// List returns all .html notes in the vault, walking sub-directories. The
// internal .weft directory and the .trash directory (deleted notes, kept for
// the never-delete invariant) are skipped — neither should surface, index, or
// re-sync.
func (v *Vault) List() ([]Note, error) {
	var notes []Note
	err := filepath.WalkDir(v.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); (name == ".weft" || name == ".trash") && path != v.Root {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".html") {
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
//
// The write is atomic: content goes to a temp file in the same directory, is
// fsync'd, then renamed over the target (rename is atomic on POSIX/NTFS). A
// crash thus leaves either the old file or the complete new one — never a
// half-written note. This matters on its own (power loss mid-save) and is a
// prerequisite for sync, where torn writes would propagate as corruption.
func (v *Vault) Write(rel string, content []byte) error {
	if strings.Contains(rel, "..") {
		return os.ErrPermission
	}
	full := v.abs(rel)
	if !strings.HasPrefix(full, v.Root+string(filepath.Separator)) {
		return os.ErrPermission
	}
	dir := filepath.Dir(full)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".weft-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we bail before the rename succeeds.
	defer os.Remove(tmpName)

	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil { // flush bytes to disk before rename
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil { // CreateTemp makes 0600
		return err
	}
	if err := os.Rename(tmpName, full); err != nil {
		return err
	}
	// fsync the directory so the rename itself survives a crash — and surface
	// any error rather than reporting a save that could be lost on power loss.
	// (Directory fsync is valid on darwin/linux, Weft's targets.)
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		d.Close()
		return err
	}
	return d.Close()
}

// Exists reports whether a note exists at rel.
func (v *Vault) Exists(rel string) bool {
	_, err := os.Stat(v.abs(rel))
	return err == nil
}

// Trash moves a note into the vault's .trash directory, preserving its bytes —
// the system never hard-deletes (the never-delete invariant). A no-op if the
// source is already gone. Any prior trash entry at the same relative path is
// overwritten. .trash is excluded from List, so trashed notes don't surface,
// index, or re-sync, but the bytes remain recoverable on disk.
func (v *Vault) Trash(rel string) error {
	src := v.abs(rel)
	if _, err := os.Stat(src); err != nil {
		return nil // already gone
	}
	dst := v.abs(filepath.Join(".trash", rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	_ = os.Remove(dst)
	return os.Rename(src, dst)
}

func (v *Vault) abs(rel string) string {
	return filepath.Join(v.Root, filepath.Clean("/"+rel))
}
