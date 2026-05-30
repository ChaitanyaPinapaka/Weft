// Package sync converges a Weft vault across devices through a dumb object
// store the user owns. All convergence logic is here and runs identically on
// every device; the backend only stores opaque key→bytes. P1 is plaintext over
// a filesystem backend; P2 wraps it in E2EE; P3 adds an S3 backend.
//
// Layout in the store (prefix omitted):
//
//	blobs/<blobid>                 immutable content-addressed note version
//	manifest/<deviceid>/HEAD       text: the device's latest generation number
//	manifest/<deviceid>/<gen>.json the device's full manifest snapshot at <gen>
//
// Per-device manifest prefixes mean two devices never write the same object, so
// there is no lost-update race and no dependency on conditional PUT — the key
// portability win across S3 clones.
package sync

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ErrNotFound is returned by Get/Head when a key is absent.
var ErrNotFound = errors.New("sync: key not found")

// Backend is the entire portability contract: any store that can do these six
// verbs (S3, R2, B2, GCS, MinIO, a local dir) can back sync. Keys are
// forward-slash paths; values are opaque bytes.
type Backend interface {
	Get(key string) ([]byte, error)                    // ErrNotFound if absent
	Put(key string, data []byte) error                 // overwrite
	PutIfAbsent(key string, data []byte) (bool, error) // true if written; false if it already existed
	List(prefix string) ([]string, error)              // keys under prefix, sorted
	Head(key string) (bool, error)                     // exists?
	Delete(key string) error                           // no error if absent
}

// FileBackend is a Backend over a local directory — used for tests and as the
// self-host target before MinIO. Two Engines pointed at the same root simulate
// two devices sharing one bucket.
type FileBackend struct{ root string }

// NewFileBackend roots a filesystem backend at dir (created if needed).
func NewFileBackend(dir string) (*FileBackend, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &FileBackend{root: dir}, nil
}

func (b *FileBackend) path(key string) string {
	return filepath.Join(b.root, filepath.FromSlash(key))
}

func (b *FileBackend) Get(key string) ([]byte, error) {
	data, err := os.ReadFile(b.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return data, err
}

func (b *FileBackend) Put(key string, data []byte) error {
	full := b.path(key)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	// Atomic within the backend dir: temp + rename, mirroring vault.Write.
	tmp, err := os.CreateTemp(filepath.Dir(full), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, full)
}

func (b *FileBackend) PutIfAbsent(key string, data []byte) (bool, error) {
	if ok, _ := b.Head(key); ok {
		return false, nil
	}
	return true, b.Put(key, data)
}

func (b *FileBackend) Head(key string) (bool, error) {
	_, err := os.Stat(b.path(key))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func (b *FileBackend) List(prefix string) ([]string, error) {
	base := b.path(prefix)
	// List walks the prefix's directory subtree and returns slash-keys.
	var keys []string
	root := b.root
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, e := filepath.Rel(root, p)
		if e != nil {
			return e
		}
		key := filepath.ToSlash(rel)
		if strings.HasPrefix(b.path(key), base) || strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Filter strictly by key prefix (the WalkDir guard above is loose) + skip temps.
	out := keys[:0]
	for _, k := range keys {
		if strings.HasPrefix(k, prefix) && !strings.Contains(k, "/.tmp-") && !strings.HasPrefix(filepath.Base(k), ".tmp-") {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (b *FileBackend) Delete(key string) error {
	err := os.Remove(b.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
