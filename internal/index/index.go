package index

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Note struct {
	Path    string
	Title   string
	Body    string   // text content for FTS (tag-stripped is fine)
	Links   []string // outgoing vault-relative links; caller pre-parses with ParseLinks
	ModTime time.Time
	Size    int64
}

type Hit struct {
	Path    string
	Title   string
	Snippet string
	Score   float64
}

type Index struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS notes (
  path     TEXT PRIMARY KEY,
  title    TEXT,
  mtime    INTEGER,
  size     INTEGER
);
CREATE VIRTUAL TABLE IF NOT EXISTS notes_fts USING fts5(path UNINDEXED, title, body);
CREATE TABLE IF NOT EXISTS backlinks (
  src TEXT NOT NULL,
  dst TEXT NOT NULL,
  PRIMARY KEY (src, dst)
);
CREATE INDEX IF NOT EXISTS backlinks_dst ON backlinks(dst);
CREATE TABLE IF NOT EXISTS embeddings (
  path TEXT PRIMARY KEY,
  vec  BLOB
);
`

func Open(vaultRoot string) (*Index, error) {
	dir := filepath.Join(vaultRoot, ".weft")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return openDSN(filepath.Join(dir, "index.db"))
}

func OpenMemory() (*Index, error) {
	return openDSN(":memory:")
}

func openDSN(dsn string) (*Index, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// modernc.org/sqlite handles concurrent reads, but FTS5 writes from
	// multiple connections fight. Pin to one connection for now.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &Index{db: db}, nil
}

func (ix *Index) Close() error {
	return ix.db.Close()
}

func (ix *Index) Upsert(n Note) error {
	tx, err := ix.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		`INSERT INTO notes(path,title,mtime,size) VALUES(?,?,?,?)
		 ON CONFLICT(path) DO UPDATE SET title=excluded.title, mtime=excluded.mtime, size=excluded.size`,
		n.Path, n.Title, n.ModTime.Unix(), n.Size,
	); err != nil {
		return err
	}

	if _, err := tx.Exec(`DELETE FROM notes_fts WHERE path = ?`, n.Path); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO notes_fts(path,title,body) VALUES(?,?,?)`,
		n.Path, n.Title, n.Body,
	); err != nil {
		return err
	}

	if _, err := tx.Exec(`DELETE FROM backlinks WHERE src = ?`, n.Path); err != nil {
		return err
	}
	if len(n.Links) > 0 {
		stmt, err := tx.Prepare(`INSERT OR IGNORE INTO backlinks(src,dst) VALUES(?,?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, dst := range n.Links {
			if dst == n.Path {
				continue
			}
			if _, err := stmt.Exec(n.Path, dst); err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

func (ix *Index) Delete(path string) error {
	tx, err := ix.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM notes WHERE path = ?`,
		`DELETE FROM notes_fts WHERE path = ?`,
		`DELETE FROM backlinks WHERE src = ?`,
		`DELETE FROM embeddings WHERE path = ?`,
	} {
		if _, err := tx.Exec(q, path); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (ix *Index) Search(query string, limit int) ([]Hit, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := ix.db.Query(
		`SELECT f.path, COALESCE(n.title, ''), snippet(notes_fts, 2, '<mark>', '</mark>', '…', 16), bm25(notes_fts)
		   FROM notes_fts f LEFT JOIN notes n ON n.path = f.path
		  WHERE notes_fts MATCH ?
		  ORDER BY bm25(notes_fts)
		  LIMIT ?`,
		query, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hits []Hit
	for rows.Next() {
		var h Hit
		// bm25 returns lower-is-better; flip sign so a larger Score means a better hit.
		var bm float64
		if err := rows.Scan(&h.Path, &h.Title, &h.Snippet, &bm); err != nil {
			return nil, err
		}
		h.Score = -bm
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

func (ix *Index) BacklinksTo(path string) ([]string, error) {
	return ix.queryStrings(`SELECT src FROM backlinks WHERE dst = ? ORDER BY src`, path)
}

func (ix *Index) LinksFrom(path string) ([]string, error) {
	return ix.queryStrings(`SELECT dst FROM backlinks WHERE src = ? ORDER BY dst`, path)
}

// UpsertEmbedding stores a serialized vector for path. Embedding format is
// owned by the embed package (LE float32 blob); index treats it as opaque.
func (ix *Index) UpsertEmbedding(path string, blob []byte) error {
	_, err := ix.db.Exec(
		`INSERT INTO embeddings(path, vec) VALUES(?, ?)
		 ON CONFLICT(path) DO UPDATE SET vec=excluded.vec`,
		path, blob,
	)
	return err
}

// GetEmbedding returns the raw blob stored for path, or (nil, nil) if absent.
func (ix *Index) GetEmbedding(path string) ([]byte, error) {
	var blob []byte
	err := ix.db.QueryRow(`SELECT vec FROM embeddings WHERE path = ?`, path).Scan(&blob)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return blob, err
}

// AllEmbeddings returns every (path, blob) pair currently stored. Used by the
// surfacer to compute cosine similarity in-process; fine for personal vaults,
// would need bounding past a few thousand notes.
func (ix *Index) AllEmbeddings() (map[string][]byte, error) {
	rows, err := ix.db.Query(`SELECT path, vec FROM embeddings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]byte{}
	for rows.Next() {
		var p string
		var b []byte
		if err := rows.Scan(&p, &b); err != nil {
			return nil, err
		}
		out[p] = b
	}
	return out, rows.Err()
}

func (ix *Index) queryStrings(q, arg string) ([]string, error) {
	rows, err := ix.db.Query(q, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
