// Package store owns the SQLite database. The web server and every MCP client
// go through it, possibly from separate processes, so all invariants live here.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// journal_mode=TRUNCATE rather than WAL: WAL keeps committed rows in a side file
// that Google Drive would sync independently of the .db, so the uploaded copy
// could be stale or inconsistent. TRUNCATE keeps every commit in the main file.
// _txlock=immediate takes the write lock up front, so check-then-insert
// transactions fail fast into busy_timeout instead of deadlocking on upgrade.
const dsnOptions = "_pragma=busy_timeout(15000)&_pragma=journal_mode(TRUNCATE)&_pragma=synchronous(FULL)&_pragma=foreign_keys(ON)&_txlock=immediate"

var migrations = []string{`
CREATE TABLE files (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  size       INTEGER NOT NULL,
  type       TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE TABLE documents (
  id         TEXT PRIMARY KEY,
  kind       TEXT NOT NULL CHECK (kind IN ('cover','resume')),
  name       TEXT NOT NULL,
  use        TEXT NOT NULL DEFAULT '',
  body       TEXT NOT NULL DEFAULT '',
  file_id    TEXT REFERENCES files(id),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE applications (
  id             TEXT PRIMARY KEY,
  ref            TEXT NOT NULL,
  company        TEXT NOT NULL,
  role           TEXT NOT NULL,
  track          TEXT NOT NULL CHECK (track IN ('fulltime','contract')),
  source         TEXT NOT NULL DEFAULT '',
  stage          TEXT NOT NULL CHECK (stage IN ('saved','applied','screening','interview','offer','closed')),
  outcome        TEXT NOT NULL DEFAULT '',
  applied        TEXT NOT NULL DEFAULT '',
  comp           TEXT NOT NULL DEFAULT '',
  next_step      TEXT NOT NULL DEFAULT '',
  next_date      TEXT NOT NULL DEFAULT '',
  contact        TEXT NOT NULL DEFAULT '',
  url            TEXT NOT NULL DEFAULT '',
  notes          TEXT NOT NULL DEFAULT '',
  letter_id      TEXT NOT NULL DEFAULT '',
  letter_text    TEXT NOT NULL DEFAULT '',
  resume_id      TEXT NOT NULL DEFAULT '',
  cover_file_id  TEXT REFERENCES files(id),
  resume_file_id TEXT REFERENCES files(id),
  url_key        TEXT NOT NULL DEFAULT '',
  identity_key   TEXT NOT NULL DEFAULT '',
  allow_dup      INTEGER NOT NULL DEFAULT 0,
  version        INTEGER NOT NULL DEFAULT 1,
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL
);
-- The real duplicate guard. A racing second client gets a constraint error even
-- if its check ran before the first client's insert. allow_dup is the deliberate
-- "add anyway" escape hatch, reachable only from the UI.
CREATE UNIQUE INDEX applications_url_key ON applications(url_key) WHERE url_key <> '' AND allow_dup = 0;
CREATE UNIQUE INDEX applications_identity_key ON applications(identity_key) WHERE identity_key <> '' AND allow_dup = 0;
CREATE INDEX applications_ref ON applications(ref);
CREATE INDEX applications_stage ON applications(stage);
CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
`, `
-- Sign-in state the remote listener can't sign into a token: revoked sessions
-- and token families, and spent codes and refresh tokens. See auth.go.
CREATE TABLE auth_marks (
  id      TEXT PRIMARY KEY,
  at      INTEGER NOT NULL, -- unix ms, when first recorded
  expires INTEGER NOT NULL  -- unix ms, after which the row means nothing
);
`}

// Sentinel errors, mapped to HTTP statuses and MCP results by the callers.
var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("record changed since it was loaded")
	ErrInvalid  = errors.New("invalid input")
)

// Store is safe for concurrent use and tolerates other processes on the same file.
type Store struct {
	db       *sql.DB
	path     string
	filesDir string
	release  func() // gives up the lease, set by OpenAs
}

// Open opens the database, creating it and applying migrations as needed.
func Open(path string) (*Store, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create database folder: %w", err)
	}
	filesDir := filepath.Join(dir, "attachments")
	if err := os.MkdirAll(filesDir, 0o755); err != nil {
		return nil, fmt.Errorf("create attachments folder: %w", err)
	}
	u := url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(path), RawQuery: dsnOptions}
	if !strings.Contains(path, ":") { // POSIX absolute path already starts with /
		u.Path = filepath.ToSlash(path)
	}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	// One connection per process: writers serialise here, and between processes
	// through SQLite's file lock.
	db.SetMaxOpenConns(1)
	s := &Store{db: db, path: path, filesDir: filesDir}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate %s: %w", path, err)
	}
	return s, nil
}

func (s *Store) Close() error {
	err := s.db.Close()
	if s.release != nil {
		s.release()
	}
	return err
}

func (s *Store) Path() string     { return s.path }
func (s *Store) FilesDir() string { return s.filesDir }

// Ping reads the schema version. That reads the database file, so it fails
// when the disk or mount underneath has gone away.
func (s *Store) Ping(ctx context.Context) error {
	var v int
	return s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v)
}

func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var v int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	if v > len(migrations) {
		return fmt.Errorf("database schema v%d is newer than this build (v%d)", v, len(migrations))
	}
	for i := v; i < len(migrations); i++ {
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", len(migrations))); err != nil {
		return err
	}
	return tx.Commit()
}

// tx runs fn in a write transaction.
func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

const b32 = "0123456789abcdefghjkmnpqrstvwxyz"

// newID returns a 20-character random id in the same alphabet as refs.
func newID() string {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = b32[b[i]&31]
	}
	return string(b)
}

// ComputeRef derives the 6-character reference code used as an email plus-tag.
// It matches the original tracker, so existing codes stay valid.
func ComputeRef(id string) string {
	sum := sha256.Sum256([]byte("jhp:" + id))
	var val, bits uint
	out := make([]byte, 0, 6)
	for _, b := range sum {
		val = val<<8 | uint(b)
		bits += 8
		for bits >= 5 && len(out) < 6 {
			out = append(out, b32[(val>>(bits-5))&31])
			bits -= 5
		}
		if len(out) >= 6 {
			break
		}
		val &= 1<<bits - 1
	}
	return string(out)
}

// GetSetting returns a setting, or "" if unset.
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetSetting stores a setting.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}
