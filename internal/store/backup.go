package store

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Backup writes the database and its attachments to w as a tar archive laid
// out like the data folder: the .db file and attachments/. The server keeps
// running meanwhile. Copying the .db file itself can catch a write half-way,
// whereas VACUUM INTO copies inside one read transaction, so the copy is
// always consistent.
func (s *Store) Backup(ctx context.Context, w io.Writer) error {
	// Beside the database rather than in /tmp, which may be a small tmpfs.
	snap := filepath.Join(filepath.Dir(s.path), ".backup-"+newID()+".db")
	defer os.Remove(snap)
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", snap); err != nil {
		return fmt.Errorf("snapshot database: %w", err)
	}
	tw := tar.NewWriter(w)
	if err := tarFile(tw, snap, filepath.Base(s.path)); err != nil {
		return err
	}
	dir := filepath.Base(s.filesDir)
	if err := tw.WriteHeader(&tar.Header{Name: dir + "/", Mode: 0o700, Typeflag: tar.TypeDir, ModTime: time.Now()}); err != nil {
		return err
	}
	// Attachments are copied after the snapshot, so one uploaded meanwhile may
	// come along unreferenced, which is harmless.
	entries, err := os.ReadDir(s.filesDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		// Dot files are uploads still being written.
		if !e.Type().IsRegular() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		err := tarFile(tw, filepath.Join(s.filesDir, e.Name()), dir+"/"+e.Name())
		if errors.Is(err, fs.ErrNotExist) { // deleted since the listing
			continue
		}
		if err != nil {
			return err
		}
	}
	return tw.Close()
}

// tarFile adds one file, readable by its owner only, since it holds personal data.
func tarFile(tw *tar.Writer, src, name string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: fi.Size(), ModTime: fi.ModTime(), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}
