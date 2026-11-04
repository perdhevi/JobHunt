package store

// Regression tests for the November 2026 security review.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestJobLinkMustBeWeb(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for _, bad := range []string{"javascript:alert(document.domain)", "JavaScript:alert(1)", "data:text/html,<script>alert(1)</script>",
		"\x01javascript:alert(1)", "vbscript:x", "https://", "file:///etc/passwd"} {
		_, err := s.Create(ctx, Application{Company: "Acme", Role: bad, URL: bad}, CreateStrict)
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("accepted %q: %v", bad, err)
		}
	}
	for in, want := range map[string]string{
		"https://acme.com/jobs/1":     "https://acme.com/jobs/1",
		"  http://acme.com/jobs/2  ":  "http://acme.com/jobs/2",
		"linkedin.com/jobs/view/1234": "https://linkedin.com/jobs/view/1234",
		"//acme.com/jobs/3":           "https://acme.com/jobs/3",
		"":                            "",
	} {
		got, err := cleanLink(in)
		if err != nil || got != want {
			t.Errorf("cleanLink(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestImportRefusesUnsafeFileIDs(t *testing.T) {
	root := t.TempDir()
	s, err := Open(filepath.Join(root, "db", "jobhunt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	in := filepath.Join(root, "in", "x", "y")
	os.MkdirAll(in, 0o755)
	os.WriteFile(filepath.Join(root, "in", "outside-payload"), []byte("attacker bytes"), 0o644)
	good := "0123456789abcdef0123456789abcdef"
	os.WriteFile(filepath.Join(in, good+".pdf"), []byte("%PDF"), 0o644)

	b := Backup{Letters: []backupDoc{
		{Document: Document{ID: "d1", Kind: "cover", Name: "bad id", File: &File{ID: "../../outside", Name: "a.pdf", Type: "application/pdf"}}},
		{Document: Document{ID: "d2", Kind: "cover", Name: "bad type", File: &File{ID: good, Name: "a.html", Type: "text/html"}}},
		{Document: Document{ID: "d3", Kind: "cover", Name: "fine", File: &File{ID: good, Name: "../a.pdf"}}},
	}}
	rep, err := s.Import(context.Background(), b, in)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Files != 1 || len(rep.MissingFiles) != 2 {
		t.Fatalf("report %+v", rep)
	}
	if _, err := os.Stat(filepath.Join(root, "outside")); err == nil {
		t.Fatal("wrote outside the attachments folder")
	}
	f, _, err := s.OpenFile(context.Background(), good)
	if err != nil || f.Name != "a.pdf" {
		t.Fatalf("good file: %+v %v", f, err)
	}
}

func TestAuthMarks(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if cut, err := s.RevokedBefore(ctx); err != nil || !cut.IsZero() {
		t.Fatalf("fresh cutoff %v %v", cut, err)
	}
	before := time.Now()
	if err := s.RevokeAll(ctx); err != nil {
		t.Fatal(err)
	}
	if cut, _ := s.RevokedBefore(ctx); cut.Before(before.Truncate(time.Millisecond)) {
		t.Fatalf("cutoff %v before %v", cut, before)
	}

	first, at, err := s.Mark(ctx, "refresh:a", time.Now().Add(time.Hour))
	if err != nil || !first {
		t.Fatalf("first mark %v %v", first, err)
	}
	again, at2, _ := s.Mark(ctx, "refresh:a", time.Now().Add(time.Hour))
	if again || !at.Equal(at2) {
		t.Fatalf("second mark %v %v %v", again, at, at2)
	}
	if ok, _ := s.Marked(ctx, "refresh:a"); !ok {
		t.Fatal("mark not found")
	}
	s.Mark(ctx, "old", time.Now().Add(-time.Second))
	if ok, _ := s.Marked(ctx, "old"); ok {
		t.Fatal("expired mark still counts")
	}
}
