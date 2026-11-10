package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkipNeedsAReason(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	res, err := s.Create(ctx, Application{Company: "Acme", Role: "SRE"}, CreateStrict)
	if err != nil {
		t.Fatal(err)
	}
	id := res.Application.ID
	skip := func(reason, note string) (Application, error) {
		return s.Patch(ctx, id, func(a *Application) error {
			a.Stage, a.SkipReason, a.SkipNote = "skipped", reason, note
			return nil
		})
	}
	if _, err := skip("bogus", ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown reason: %v", err)
	}
	if _, err := skip("other", "  "); !errors.Is(err, ErrInvalid) {
		t.Errorf("other without details: %v", err)
	}
	a, err := skip("rate", "max $35/h")
	if err != nil {
		t.Fatal(err)
	}
	if a.Stage != "skipped" || a.SkipReason != "rate" || a.SkipNote != "max $35/h" || a.Skipped != today() || a.Sent || a.Due != "" || a.Applied != "" {
		t.Errorf("skipped record: %+v", a)
	}
	if sum := Summarize([]Application{a}); sum.ByStage["skipped"] != 1 || sum.InFlight != 0 {
		t.Errorf("summary: %+v", sum)
	}

	// Moving it back clears the reason.
	a, err = s.Patch(ctx, id, func(a *Application) error { a.Stage = "saved"; return nil })
	if err != nil || a.SkipReason != "" || a.SkipNote != "" || a.Skipped != "" {
		t.Errorf("unskipped: %+v %v", a, err)
	}
}

func TestSkippedJobBlocksReAdding(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	job := Application{Company: "Acme Inc.", Role: "Senior AI Engineer", URL: "https://acme.com/jobs/7",
		Stage: "skipped", SkipReason: "location", SkipNote: "US residents only"}
	if res, err := s.Create(ctx, job, CreateStrict); err != nil || !res.Created {
		t.Fatalf("%+v %v", res, err)
	}
	for _, c := range []struct{ company, role, url string }{
		{"Acme", "Sr AI Engineer", ""},
		{"", "", "https://www.acme.com/jobs/7?utm_source=x"},
	} {
		chk, _ := s.Check(ctx, c.company, c.role, c.url)
		if chk.Verdict != VerdictSkipped || chk.SafeToSend {
			t.Errorf("%v: %+v", c, chk)
		}
		if !strings.Contains(chk.Message, "Location not applicable: US residents only") || chk.Matches[0].SkipNote != "US residents only" {
			t.Errorf("message should say why: %q", chk.Message)
		}
	}
	for _, mode := range []CreateMode{CreateStrict, CreateAllowSimilar} {
		if res, _ := s.Create(ctx, Application{Company: "Acme", Role: "Senior AI Engineer"}, mode); res.Created {
			t.Errorf("mode %d re-added a skipped job", mode)
		}
	}
	// If it went out after all, mark_sent records that and clears the skip.
	apps, _ := s.ListApplications(ctx)
	a := apps[0]
	got, changed, err := s.MarkSent(ctx, a.ID, "", "", "")
	if err != nil || !changed || got.Stage != "applied" || got.SkipReason != "" {
		t.Errorf("mark sent from skipped: %+v %v", got, err)
	}
}

// A database from before the skipped stage keeps every record, with its
// unique guards, and accepts the new stage.
func TestMigrationAddsSkippedStage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations[:2] {
		if _, err := db.Exec(m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`PRAGMA user_version = 2;
		INSERT INTO applications (id, ref, company, role, track, stage, url, url_key, identity_key, created_at, updated_at)
		VALUES ('a1', 'x5y13n', 'Acme', 'SRE', 'fulltime', 'applied', 'https://acme.com/1', 'acme.com/1', 'acme|sre', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	a, err := s.GetApplication(ctx, "x5y13n")
	if err != nil || a.Company != "Acme" || a.Stage != "applied" {
		t.Fatalf("%+v %v", a, err)
	}
	if res, _ := s.Create(ctx, Application{Company: "Other", Role: "Dev", URL: "https://acme.com/1"}, CreateForce); !res.Created {
		t.Fatal("forced add failed")
	}
	if _, err := s.db.Exec(`INSERT INTO applications (id, ref, company, role, track, stage, url_key, created_at, updated_at)
		VALUES ('a3', 'r', 'X', 'Y', 'fulltime', 'saved', 'acme.com/1', 1, 1)`); err == nil {
		t.Error("the unique link guard is gone after migrating")
	}
	if _, err := s.Patch(ctx, "a1", func(a *Application) error { a.Stage, a.SkipReason = "skipped", "timezone"; return nil }); err != nil {
		t.Errorf("skipped stage refused after migrating: %v", err)
	}
}
