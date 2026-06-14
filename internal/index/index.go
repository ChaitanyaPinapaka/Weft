package index

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/html"
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
CREATE TABLE IF NOT EXISTS access_log (
  path TEXT NOT NULL,
  ts   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS access_log_path ON access_log(path);
CREATE INDEX IF NOT EXISTS access_log_ts   ON access_log(ts);
CREATE TABLE IF NOT EXISTS tags (
  path TEXT NOT NULL,
  tag  TEXT NOT NULL,
  PRIMARY KEY (path, tag)
);
CREATE INDEX IF NOT EXISTS tags_tag ON tags(tag);
CREATE TABLE IF NOT EXISTS settings (
  key   TEXT PRIMARY KEY,
  value TEXT
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
	// SQLite's ALTER TABLE has no IF NOT EXISTS for columns, so probe PRAGMA
	// table_info and add `content` only when missing. Lets old DBs upgrade in place.
	if err := ensureNotesContentColumn(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate notes.content: %w", err)
	}
	return &Index{db: db}, nil
}

func ensureNotesContentColumn(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(notes)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == "content" {
			return rows.Close()
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec(`ALTER TABLE notes ADD COLUMN content TEXT`)
	return err
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
		`INSERT INTO notes(path,title,mtime,size,content) VALUES(?,?,?,?,?)
		 ON CONFLICT(path) DO UPDATE SET title=excluded.title, mtime=excluded.mtime, size=excluded.size, content=excluded.content`,
		n.Path, n.Title, n.ModTime.Unix(), n.Size, n.Body,
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

	if _, err := tx.Exec(`DELETE FROM tags WHERE path = ?`, n.Path); err != nil {
		return err
	}
	for _, tag := range ParseTags([]byte(n.Body)) {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO tags(path, tag) VALUES(?, ?)`, n.Path, tag); err != nil {
			return err
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
		`DELETE FROM tags WHERE path = ?`,
	} {
		if _, err := tx.Exec(q, path); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Remove erases every row a note occupies: notes, FTS, backlinks in BOTH
// directions, embeddings, tags. Used when a note is trashed. It differs from
// Delete (the rename path) by also dropping incoming backlink rows — nothing
// will re-point them at a trashed note. access_log is deliberately untouched:
// it is append-only behavioral history, not note content, and old accesses
// still inform co-access scoring for the notes that remain.
func (ix *Index) Remove(path string) error {
	tx, err := ix.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM notes WHERE path = ?`,
		`DELETE FROM notes_fts WHERE path = ?`,
		`DELETE FROM backlinks WHERE src = ?`,
		`DELETE FROM backlinks WHERE dst = ?`,
		`DELETE FROM embeddings WHERE path = ?`,
		`DELETE FROM tags WHERE path = ?`,
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

// Paths returns every note path currently in the index. The reindex diffs
// this against the vault to prune rows for files that no longer exist on disk
// (e.g. a trash or rename whose own index cleanup failed).
func (ix *Index) Paths() ([]string, error) {
	rows, err := ix.db.Query(`SELECT path FROM notes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
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

// LogAccess records that `path` was opened at unix time `ts`. Append-only;
// bounded by PruneAccessLog (called on daemon start).
func (ix *Index) LogAccess(path string, ts int64) error {
	_, err := ix.db.Exec(`INSERT INTO access_log(path, ts) VALUES(?, ?)`, path, ts)
	return err
}

// GetSetting returns a daemon setting by key. ok is false if absent. Settings
// are the daemon's own mutable state (e.g. tuned surfacing weights) — kept in
// the rebuildable index, not an external config file (CLAUDE.md: no config files).
func (ix *Index) GetSetting(key string) (value string, ok bool, err error) {
	row := ix.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key)
	switch err = row.Scan(&value); err {
	case nil:
		return value, true, nil
	case sql.ErrNoRows:
		return "", false, nil
	default:
		return "", false, err
	}
}

// SetSetting upserts a daemon setting.
func (ix *Index) SetSetting(key, value string) error {
	_, err := ix.db.Exec(
		`INSERT INTO settings(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value,
	)
	return err
}

// PruneAccessLog deletes access rows older than `before` (unix seconds),
// bounding the table that the surface hot path self-joins (CoAccessCount) and
// scans (RecentAccesses/AllAccessHistory). The access log is derived telemetry,
// not source of truth — old entries contribute negligibly to base-level
// activation (t^-d decays hard) so dropping them is safe. Notes are never
// touched; only their access history is trimmed.
func (ix *Index) PruneAccessLog(before int64) error {
	_, err := ix.db.Exec(`DELETE FROM access_log WHERE ts < ?`, before)
	return err
}

// CoAccessed returns paths accessed within `window` of any access of `path`
// (the same surfacing-session heuristic). Excludes `path` itself.
func (ix *Index) CoAccessed(path string, window time.Duration) ([]string, error) {
	w := int64(window.Seconds())
	rows, err := ix.db.Query(
		`SELECT DISTINCT b.path
		   FROM access_log a
		   JOIN access_log b ON b.ts BETWEEN a.ts - ? AND a.ts + ?
		  WHERE a.path = ? AND b.path <> ?
		  ORDER BY b.path`,
		w, w, path, path,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Access is one access-log row.
type Access struct {
	Path string
	Ts   int64
}

// AccessHistory returns the ascending unix-second access timestamps for one
// note. Empty (not error) if the note was never opened. Feeds ACT-R base-level
// activation B_i = ln(Σ t_j^-d).
func (ix *Index) AccessHistory(path string) ([]int64, error) {
	rows, err := ix.db.Query(`SELECT ts FROM access_log WHERE path = ? ORDER BY ts ASC`, path)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var t int64
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// AllAccessHistory returns every note's ascending access history in a single
// scan. The surfacer needs base-level for every candidate at once; one query
// beats N round-trips on the single-connection modernc DB.
func (ix *Index) AllAccessHistory() (map[string][]int64, error) {
	rows, err := ix.db.Query(`SELECT path, ts FROM access_log ORDER BY path, ts ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]int64{}
	for rows.Next() {
		var p string
		var t int64
		if err := rows.Scan(&p, &t); err != nil {
			return nil, err
		}
		out[p] = append(out[p], t)
	}
	return out, rows.Err()
}

// CoAccessCount returns, for each note co-accessed with `path` inside `window`,
// the count of DISTINCT co-access events (candidate timestamps that fall near
// any access of path). COUNT(DISTINCT b.ts) — not COUNT(*) — so the metric is
// invariant to how many times `path` itself was opened; otherwise a busy focus
// note would inflate every co-access by its own visit count.
func (ix *Index) CoAccessCount(path string, window time.Duration) (map[string]int, error) {
	w := int64(window.Seconds())
	rows, err := ix.db.Query(
		`SELECT b.path, COUNT(DISTINCT b.ts)
		   FROM access_log a
		   JOIN access_log b ON b.ts BETWEEN a.ts - ? AND a.ts + ?
		  WHERE a.path = ? AND b.path <> ?
		  GROUP BY b.path`,
		w, w, path, path,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var p string
		var c int
		if err := rows.Scan(&p, &c); err != nil {
			return nil, err
		}
		out[p] = c
	}
	return out, rows.Err()
}

// RecentAccesses returns access rows with ts >= since, most-recent first. The
// server gap-walks this to reconstruct the current session's note sequence.
func (ix *Index) RecentAccesses(since int64) ([]Access, error) {
	rows, err := ix.db.Query(`SELECT path, ts FROM access_log WHERE ts >= ? ORDER BY ts DESC`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Access
	for rows.Next() {
		var a Access
		if err := rows.Scan(&a.Path, &a.Ts); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Stale reports whether the on-disk note at `path` (with the given filesystem
// mtime) is newer than what's in the index. Unknown paths are stale so the
// startup walker indexes them.
func (ix *Index) Stale(path string, fsModTime time.Time) (bool, error) {
	var stored int64
	err := ix.db.QueryRow(`SELECT mtime FROM notes WHERE path = ?`, path).Scan(&stored)
	if err == sql.ErrNoRows {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return fsModTime.Unix() > stored, nil
}

type TagCount struct {
	Tag   string
	Count int
}

// tagRe matches `#tag` only when `#` is at a non-word boundary (\B before #
// means the char before # is NOT a word char — which is true at SOS or after
// whitespace/punctuation, false in `foo#bar`). First char must be a letter;
// 0-30 trailing word/hyphen chars.
var tagRe = regexp.MustCompile(`\B#([a-zA-Z][a-zA-Z0-9_-]{0,30})\b`)

// ParseTags extracts #tagname tokens from an HTML body. Strips tags first
// (so the parser doesn't see CSS selectors or `#anchor` href fragments),
// then matches the tagRe regex over the visible text.
// Returns lowercased, deduped, sorted.
func ParseTags(htmlBody []byte) []string {
	text := stripHTML(htmlBody)
	matches := tagRe.FindAllStringSubmatch(text, -1)
	seen := map[string]struct{}{}
	for _, m := range matches {
		seen[strings.ToLower(m[1])] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// stripHTML walks the parsed DOM and concatenates text nodes, separating them
// with spaces so adjacent text in different elements doesn't fuse into a
// single word. Skips <script> and <style> bodies whose contents are code.
func stripHTML(htmlBody []byte) string {
	doc, err := html.Parse(bytes.NewReader(htmlBody))
	if err != nil {
		return string(htmlBody)
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
			return
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			b.WriteByte(' ')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return b.String()
}

// AllTags returns every tag in the index with its note count, sorted by
// count descending, then tag ascending.
func (ix *Index) AllTags() ([]TagCount, error) {
	rows, err := ix.db.Query(`SELECT tag, COUNT(*) FROM tags GROUP BY tag ORDER BY COUNT(*) DESC, tag ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TagCount
	for rows.Next() {
		var tc TagCount
		if err := rows.Scan(&tc.Tag, &tc.Count); err != nil {
			return nil, err
		}
		out = append(out, tc)
	}
	return out, rows.Err()
}

// NotesWithTag returns the paths of notes containing tag, sorted ascending.
func (ix *Index) NotesWithTag(tag string) ([]string, error) {
	return ix.queryStrings(`SELECT path FROM tags WHERE tag = ? ORDER BY path`, tag)
}

// AllTagsByPath returns every note's tags keyed by path (notes with no tags are
// absent), each tag list sorted ascending. One scan of the tags table — the
// batch-load companion to AllAccessHistory, for callers (e.g. graph.Build) that
// need every note's tags at once without an N+1 of NotesWithTag.
func (ix *Index) AllTagsByPath() (map[string][]string, error) {
	rows, err := ix.db.Query(`SELECT path, tag FROM tags ORDER BY path, tag ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var p, tag string
		if err := rows.Scan(&p, &tag); err != nil {
			return nil, err
		}
		out[p] = append(out[p], tag)
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
