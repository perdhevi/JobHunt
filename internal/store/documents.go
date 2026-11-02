package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// MaxFileSize matches the original tracker's upload limit.
const MaxFileSize = 20 << 20

var fileTypes = map[string]string{".pdf": "application/pdf", ".txt": "text/plain", ".md": "text/markdown"}

var fileIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Document is a saved cover letter or resume version.
type Document struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Use       string `json:"use"`
	Body      string `json:"body"`
	File      *File  `json:"file"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

// ListDocuments returns all document versions by name.
func (s *Store) ListDocuments(ctx context.Context) ([]Document, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT d.id, d.kind, d.name, d.use, d.body, d.created_at, d.updated_at,
		f.id, f.name, f.size, f.type FROM documents d LEFT JOIN files f ON f.id = d.file_id ORDER BY d.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Document{}
	for rows.Next() {
		var d Document
		var fID, fName, fType sql.NullString
		var fSize sql.NullInt64
		if err := rows.Scan(&d.ID, &d.Kind, &d.Name, &d.Use, &d.Body, &d.CreatedAt, &d.UpdatedAt, &fID, &fName, &fSize, &fType); err != nil {
			return nil, err
		}
		if fID.Valid {
			d.File = &File{ID: fID.String, Name: fName.String, Size: fSize.Int64, Type: fType.String}
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (d *Document) clean() error {
	d.Name = strings.TrimSpace(d.Name)
	if d.Name == "" {
		return fmt.Errorf("%w: version name is required", ErrInvalid)
	}
	if d.Kind != "cover" && d.Kind != "resume" {
		return fmt.Errorf("%w: kind must be cover or resume", ErrInvalid)
	}
	return nil
}

// SaveDocument creates the version when d.ID is empty, otherwise replaces it.
func (s *Store) SaveDocument(ctx context.Context, d Document) (Document, error) {
	if err := d.clean(); err != nil {
		return d, err
	}
	now := time.Now().UnixMilli()
	d.UpdatedAt = now
	if d.ID == "" {
		d.ID, d.CreatedAt = newID(), now
		_, err := s.db.ExecContext(ctx, `INSERT INTO documents(id, kind, name, use, body, file_id, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?)`, d.ID, d.Kind, d.Name, d.Use, d.Body, fileIDArg(d.File), now, now)
		return d, err
	}
	var prev sql.NullString
	err := s.db.QueryRowContext(ctx, "SELECT file_id FROM documents WHERE id = ?", d.ID).Scan(&prev)
	if errors.Is(err, sql.ErrNoRows) {
		return d, fmt.Errorf("%w: document %q", ErrNotFound, d.ID)
	}
	if err != nil {
		return d, err
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE documents SET kind=?, name=?, use=?, body=?, file_id=?, updated_at=? WHERE id=?`,
		d.Kind, d.Name, d.Use, d.Body, fileIDArg(d.File), now, d.ID); err != nil {
		return d, err
	}
	if prev.Valid && (d.File == nil || d.File.ID != prev.String) {
		s.pruneFiles(ctx, []string{prev.String})
	}
	return d, nil
}

// DeleteDocument removes a version. Applications keep their reference and show
// it as a deleted version, as in the original tracker.
func (s *Store) DeleteDocument(ctx context.Context, id string) error {
	var prev sql.NullString
	err := s.db.QueryRowContext(ctx, "SELECT file_id FROM documents WHERE id = ?", id).Scan(&prev)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: document %q", ErrNotFound, id)
	}
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM documents WHERE id = ?", id); err != nil {
		return err
	}
	if prev.Valid {
		s.pruneFiles(ctx, []string{prev.String})
	}
	return nil
}

// FileTypeFor returns the stored media type for a filename, or "" if not allowed.
func FileTypeFor(name string) string { return fileTypes[strings.ToLower(filepath.Ext(name))] }

// SaveFile stores an upload. The file is on disk before its row exists, so a
// crash leaves at worst an unreferenced file, never a dangling record.
func (s *Store) SaveFile(ctx context.Context, name string, r io.Reader) (File, error) {
	typ := FileTypeFor(name)
	if typ == "" {
		return File{}, fmt.Errorf("%w: only PDF, TXT or MD files can be stored", ErrInvalid)
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return File{}, err
	}
	id := hex.EncodeToString(raw)
	f, err := s.writeBlob(id, typ, r)
	if err != nil {
		return File{}, err
	}
	meta := File{ID: id, Name: filepath.Base(name), Size: f, Type: typ}
	if err := s.insertFile(ctx, meta); err != nil {
		s.removeBlob(id)
		return File{}, err
	}
	return meta, nil
}

func (s *Store) insertFile(ctx context.Context, f File) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO files(id, name, size, type, created_at) VALUES (?,?,?,?,?)`,
		f.ID, f.Name, f.Size, f.Type, time.Now().UnixMilli())
	return err
}

// blobPath names a blob with its real extension. Google Drive for Desktop renames
// extensionless files based on their content, so a bare id would not stay put.
func (s *Store) blobPath(id, typ string) string {
	for ext, t := range fileTypes {
		if t == typ {
			return filepath.Join(s.filesDir, id+ext)
		}
	}
	return filepath.Join(s.filesDir, id)
}

// findBlob locates a blob by id, whatever a sync client has done to its suffix.
func (s *Store) findBlob(id string) string {
	m, _ := filepath.Glob(filepath.Join(s.filesDir, id+"*"))
	if len(m) == 0 {
		return ""
	}
	return m[0]
}

func (s *Store) removeBlob(id string) {
	if p := s.findBlob(id); p != "" {
		os.Remove(p)
	}
}

func (s *Store) writeBlob(id, typ string, r io.Reader) (int64, error) {
	tmp, err := os.CreateTemp(s.filesDir, ".upload-*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, io.LimitReader(r, MaxFileSize+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, err
	}
	if n > MaxFileSize {
		return 0, fmt.Errorf("%w: file is over 20 MB", ErrInvalid)
	}
	return n, os.Rename(tmp.Name(), s.blobPath(id, typ))
}

// OpenFile returns an attachment's metadata and path.
func (s *Store) OpenFile(ctx context.Context, id string) (File, string, error) {
	var f File
	if !fileIDPattern.MatchString(id) {
		return f, "", ErrNotFound
	}
	err := s.db.QueryRowContext(ctx, "SELECT id, name, size, type FROM files WHERE id = ?", id).Scan(&f.ID, &f.Name, &f.Size, &f.Type)
	if errors.Is(err, sql.ErrNoRows) {
		return f, "", ErrNotFound
	}
	if err != nil {
		return f, "", err
	}
	p := s.findBlob(id)
	if p == "" {
		return f, "", fmt.Errorf("%w: %s is missing from the attachments folder. Drive may still be syncing it", ErrNotFound, f.Name)
	}
	return f, p, nil
}

// pruneFiles deletes attachments nothing references any more. Failures are
// ignored: an orphaned file is harmless and the record change already succeeded.
func (s *Store) pruneFiles(ctx context.Context, ids []string) {
	for _, id := range ids {
		var n int
		err := s.db.QueryRowContext(ctx, `SELECT
			(SELECT count(*) FROM applications WHERE cover_file_id = ?1 OR resume_file_id = ?1) +
			(SELECT count(*) FROM documents WHERE file_id = ?1)`, id).Scan(&n)
		if err != nil || n > 0 {
			continue
		}
		if _, err := s.db.ExecContext(ctx, "DELETE FROM files WHERE id = ?", id); err == nil {
			s.removeBlob(id)
		}
	}
}
