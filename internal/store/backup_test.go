package store

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The archive must restore into a working data folder: the database with
// its records, and the attachments they point at.
func TestBackupRestores(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	f, err := s.SaveFile(ctx, "cv.pdf", strings.NewReader("%PDF-1.7 resume"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, Application{Company: "Acme", Role: "Engineer", ResumeFile: &f}, CreateStrict); err != nil {
		t.Fatal(err)
	}
	// An upload still in progress stays out of the archive.
	os.WriteFile(filepath.Join(s.FilesDir(), ".upload-123"), []byte("partial"), 0o600)

	var buf bytes.Buffer
	if err := s.Backup(ctx, &buf); err != nil {
		t.Fatal(err)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(s.Path()), ".backup-*")); len(left) != 0 {
		t.Fatalf("snapshot left behind: %v", left)
	}

	out := t.TempDir()
	tr := tar.NewReader(&buf)
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		dst := filepath.Join(out, filepath.FromSlash(h.Name))
		if h.Typeflag == tar.TypeDir {
			os.MkdirAll(dst, 0o700)
			continue
		}
		if h.Mode&0o077 != 0 {
			t.Errorf("%s is readable by others: %o", h.Name, h.Mode)
		}
		b, _ := io.ReadAll(tr)
		os.WriteFile(dst, b, 0o600)
	}
	for _, n := range names {
		if strings.Contains(n, ".upload-") {
			t.Errorf("partial upload archived: %v", names)
		}
	}

	r, err := Open(filepath.Join(out, filepath.Base(s.Path())))
	if err != nil {
		t.Fatalf("restored database: %v (archive %v)", err, names)
	}
	defer r.Close()
	apps, err := r.ListApplications(ctx)
	if err != nil || len(apps) != 1 || apps[0].ResumeFile == nil {
		t.Fatalf("restored records %+v %v", apps, err)
	}
	if _, _, err := r.OpenFile(ctx, apps[0].ResumeFile.ID); err != nil {
		t.Fatalf("restored attachment: %v", err)
	}
}
