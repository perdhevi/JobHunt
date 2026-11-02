package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Backup is the tracker's "JSON backup" export format.
type Backup struct {
	Applications []backupApp `json:"applications"`
	Letters      []backupDoc `json:"letters"`
	Email        string      `json:"email"`
}

type backupApp struct {
	Application
	Example bool  `json:"example"`
	File    *File `json:"file"` // older records held a single attachment here
}

type backupDoc struct {
	Document
	Example bool `json:"example"`
}

// ImportReport counts what an import did.
type ImportReport struct {
	Applications, Documents, Files int
	SkippedExisting, SkippedSample int
	Conflicts                      []string
	MissingFiles                   []string
}

func isSample(id string, flagged bool) bool { return flagged || strings.HasPrefix(id, "example-") }

// Import loads a backup, keeping ids and reference codes. It is safe to re-run:
// records already present are left alone, so edits made here are never undone.
// filesDir, if set, holds attachment bytes named <id> or <id>.<ext>.
func (s *Store) Import(ctx context.Context, b Backup, filesDir string) (ImportReport, error) {
	var rep ImportReport
	addFile := func(f *File) *File {
		if f == nil || f.ID == "" {
			return nil
		}
		src := ""
		if filesDir != "" {
			m, _ := filepath.Glob(filepath.Join(filesDir, f.ID+"*"))
			if len(m) > 0 {
				src = m[0]
			}
		}
		if src == "" {
			rep.MissingFiles = append(rep.MissingFiles, f.Name)
			return nil
		}
		r, err := os.Open(src)
		if err != nil {
			rep.MissingFiles = append(rep.MissingFiles, f.Name)
			return nil
		}
		defer r.Close()
		if f.Type == "" {
			f.Type = FileTypeFor(f.Name)
		}
		if _, err := s.writeBlob(f.ID, f.Type, r); err != nil {
			rep.MissingFiles = append(rep.MissingFiles, f.Name)
			return nil
		}
		if err := s.insertFile(ctx, *f); err != nil {
			rep.MissingFiles = append(rep.MissingFiles, f.Name)
			return nil
		}
		rep.Files++
		return f
	}

	exists := func(table, id string) (bool, error) {
		var n int
		err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM "+table+" WHERE id = ?", id).Scan(&n)
		return n > 0, err
	}

	for _, d := range b.Letters {
		if isSample(d.ID, d.Example) {
			rep.SkippedSample++
			continue
		}
		if ok, err := exists("documents", d.ID); err != nil {
			return rep, err
		} else if ok {
			rep.SkippedExisting++
			continue
		}
		if d.Kind == "" {
			d.Kind = "cover"
		}
		if err := d.Document.clean(); err != nil {
			rep.Conflicts = append(rep.Conflicts, d.Name+": "+err.Error())
			continue
		}
		d.File = addFile(d.File)
		if _, err := s.db.ExecContext(ctx, `INSERT INTO documents(id, kind, name, use, body, file_id, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?)`, d.ID, d.Kind, d.Name, d.Use, d.Body, fileIDArg(d.File), d.CreatedAt, d.UpdatedAt); err != nil {
			return rep, err
		}
		rep.Documents++
	}

	for _, ba := range b.Applications {
		a := ba.Application
		if isSample(a.ID, ba.Example) {
			rep.SkippedSample++
			continue
		}
		if ok, err := exists("applications", a.ID); err != nil {
			return rep, err
		} else if ok {
			rep.SkippedExisting++
			continue
		}
		// Same migration the original page applied to old single-file records.
		if ba.File != nil && a.CoverFile == nil && a.ResumeFile == nil {
			if strings.Contains(strings.ToLower(ba.File.Name), "resume") || strings.Contains(strings.ToLower(ba.File.Name), "cv") {
				a.ResumeFile = ba.File
			} else {
				a.CoverFile = ba.File
			}
		}
		savedApplied := a.Applied
		if err := a.clean(); err != nil {
			rep.Conflicts = append(rep.Conflicts, a.Company+": "+err.Error())
			continue
		}
		a.Applied = savedApplied // keep history as it was, even a blank date
		if a.Ref == "" {
			a.Ref = ComputeRef(a.ID)
		}
		a.CoverFile, a.ResumeFile = addFile(a.CoverFile), addFile(a.ResumeFile)
		err := s.tx(ctx, func(tx *sql.Tx) error { return insertApp(ctx, tx, &a, 0) })
		if isUniqueViolation(err) {
			rep.Conflicts = append(rep.Conflicts, fmt.Sprintf("%s, %s: duplicates an existing record", a.Company, a.Role))
			continue
		}
		if err != nil {
			return rep, err
		}
		rep.Applications++
	}

	if b.Email != "" {
		if cur, err := s.GetSetting(ctx, SettingEmail); err != nil {
			return rep, err
		} else if cur == "" {
			if err := s.SetSetting(ctx, SettingEmail, b.Email); err != nil {
				return rep, err
			}
		}
	}
	return rep, nil
}

// SettingEmail holds the base address for +ref reference emails.
const SettingEmail = "email"

// ReadBackup parses a backup file.
func ReadBackup(path string) (Backup, error) {
	var b Backup
	raw, err := os.ReadFile(path)
	if err != nil {
		return b, err
	}
	return b, json.Unmarshal(raw, &b)
}
