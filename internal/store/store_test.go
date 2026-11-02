package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestURLKey(t *testing.T) {
	same := [][2]string{
		{"https://www.linkedin.com/jobs/view/senior-eng-at-acme-4012345678/?trackingId=abc", "https://linkedin.com/jobs/search/?currentJobId=4012345678&refId=z"},
		{"https://jobs.ashbyhq.com/factory/a70c/application?utm_source=x", "jobs.ashbyhq.com/factory/a70c/"},
		{"https://www.upwork.com/jobs/Build-RAG_~0123456789abcdef/", "https://upwork.com/freelance-jobs/apply/~0123456789abcdef"},
		{"https://boards.greenhouse.io/acme/jobs/1?gh_src=li#app", "https://boards.greenhouse.io/acme/jobs/1"},
	}
	for _, c := range same {
		if a, b := URLKey(c[0]), URLKey(c[1]); a != b || a == "" {
			t.Errorf("%q and %q: %q != %q", c[0], c[1], a, b)
		}
	}
	if URLKey("https://x.com/job?id=1") == URLKey("https://x.com/job?id=2") {
		t.Error("meaningful query params must be kept")
	}
}

func TestCompanyKey(t *testing.T) {
	for in, want := range map[string]string{
		"https://factory.com": "factory", "Acme, Inc.": "acme", "PT Maju Jaya Tbk": "maju jaya", "www.hercules.app": "hercules",
	} {
		if got := CompanyKey(in); got != want {
			t.Errorf("CompanyKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestComputeRefMatchesOriginalTracker(t *testing.T) {
	// Refs the original page stored for these ids.
	for id, want := range map[string]string{"ckbhfr0rlom02agrgn7r": "x5y13n", "yfnx54d8dcjak2pjh061": "bzzdqg"} {
		if got := ComputeRef(id); got != want {
			t.Errorf("ComputeRef(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestCheckVerdicts(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	mustCreate := func(a Application) {
		t.Helper()
		if r, err := s.Create(ctx, a, CreateStrict); err != nil || !r.Created {
			t.Fatalf("create %+v: %+v %v", a, r, err)
		}
	}
	mustCreate(Application{Company: "https://factory.com", Role: "Technical Support Engineer - Remote APAC", Stage: "applied",
		URL: "https://jobs.ashbyhq.com/factory/a70c/application"})
	mustCreate(Application{Company: "BetterRX", Role: "Software Engineer", Stage: "saved"})

	cases := []struct {
		name, company, role, url string
		want                     Verdict
		safe                     bool
	}{
		{"same link, other wording", "Whoever", "Anything", "https://jobs.ashbyhq.com/factory/a70c?utm_source=li", VerdictAlreadySent, false},
		{"same name", "BetterRX, Inc.", "software engineer", "", VerdictAlreadyTracked, true},
		{"same job minus location", "Factory AI", "Technical Support Engineer", "", VerdictPossibleDuplicate, false},
		{"other role, same company", "Factory", "Head of Finance", "", VerdictNew, true},
		{"unrelated", "Stripe", "Support Engineer", "", VerdictNew, true},
	}
	for _, c := range cases {
		r, err := s.Check(ctx, c.company, c.role, c.url)
		if err != nil {
			t.Fatal(err)
		}
		if r.Verdict != c.want || r.SafeToSend != c.safe {
			t.Errorf("%s: got %s safe=%v, want %s safe=%v", c.name, r.Verdict, r.SafeToSend, c.want, c.safe)
		}
	}
	if r, _ := s.Check(ctx, "Factory", "Head of Finance", ""); len(r.OtherRoles) != 1 {
		t.Errorf("other roles at the company should be listed: %+v", r)
	}
}

func TestCreateRefusesDuplicatesWithoutWriting(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	a := Application{Company: "Acme", Role: "SRE", URL: "https://acme.com/jobs/1"}
	if r, err := s.Create(ctx, a, CreateStrict); err != nil || !r.Created {
		t.Fatal(r, err)
	}
	if r, err := s.Create(ctx, a, CreateStrict); err != nil || r.Created || r.Check.Verdict != VerdictAlreadyTracked {
		t.Fatalf("retry must not create: %+v %v", r, err)
	}
	if r, _ := s.Create(ctx, a, CreateAllowSimilar); r.Created {
		t.Fatal("confirm_distinct must not override an exact duplicate")
	}
	if r, err := s.Create(ctx, a, CreateForce); err != nil || !r.Created {
		t.Fatalf("UI override should work: %+v %v", r, err)
	}
	apps, _ := s.ListApplications(ctx)
	if len(apps) != 2 {
		t.Fatalf("got %d records", len(apps))
	}
}

// Two processes racing on one file: exactly one record may survive.
func TestConcurrentCreateAcrossHandles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	ctx := context.Background()
	var wg sync.WaitGroup
	var mu sync.Mutex
	created := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := Open(path)
			if err != nil {
				t.Error(err)
				return
			}
			defer s.Close()
			r, err := s.Create(ctx, Application{Company: "Acme", Role: "SRE", URL: "https://acme.com/1"}, CreateStrict)
			if err != nil {
				t.Error(err)
				return
			}
			if r.Created {
				mu.Lock()
				created++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if created != 1 {
		t.Fatalf("created %d records, want 1", created)
	}
}

func TestMarkSentIsIdempotent(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	r, _ := s.Create(ctx, Application{Company: "Acme", Role: "SRE"}, CreateStrict)
	a, changed, err := s.MarkSent(ctx, "+"+r.Application.Ref, "2026-11-01", "", "")
	if err != nil || !changed || a.Stage != "applied" || a.Applied != "2026-11-01" {
		t.Fatalf("first: %+v %v %v", a, changed, err)
	}
	a, changed, err = s.MarkSent(ctx, r.Application.ID, "2026-11-02", "", "")
	if err != nil || changed || a.Applied != "2026-11-01" {
		t.Fatalf("second must change nothing: %+v %v %v", a, changed, err)
	}
}

func TestUpdateDetectsStaleEdit(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	r, _ := s.Create(ctx, Application{Company: "Acme", Role: "SRE"}, CreateStrict)
	form := *r.Application
	if _, err := s.Patch(ctx, form.ID, func(a *Application) error { a.Notes = "from agent"; return nil }); err != nil {
		t.Fatal(err)
	}
	form.Notes = "from stale form"
	if _, err := s.Update(ctx, form, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
}

func TestUpdateCannotCreateDuplicate(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	s.Create(ctx, Application{Company: "Acme", Role: "SRE"}, CreateStrict)
	r, _ := s.Create(ctx, Application{Company: "Other", Role: "SRE"}, CreateStrict)
	a := *r.Application
	a.Company = "Acme"
	if _, err := s.Update(ctx, a, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
}

func TestStageRules(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	r, _ := s.Create(ctx, Application{Company: "Acme", Role: "SRE"}, CreateStrict)
	a, err := s.Patch(ctx, r.Application.ID, func(a *Application) error { a.Stage = "interview"; return nil })
	if err != nil || a.Applied != today() {
		t.Fatalf("applied date should fill in: %+v %v", a, err)
	}
	a, _ = s.Patch(ctx, a.ID, func(a *Application) error { a.Stage, a.Outcome = "closed", "Rejected"; return nil })
	a, _ = s.Patch(ctx, a.ID, func(a *Application) error { a.Stage = "offer"; return nil })
	if a.Outcome != "" {
		t.Fatalf("outcome should clear on reopen: %+v", a)
	}
}

func TestFileRoundTripAndPrune(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	f, err := s.SaveFile(ctx, "cv.pdf", strings.NewReader("%PDF-1.7"))
	if err != nil {
		t.Fatal(err)
	}
	_, p, err := s.OpenFile(ctx, f.ID)
	if err != nil || filepath.Ext(p) != ".pdf" {
		t.Fatalf("blob must keep its extension or Drive renames it: %q %v", p, err)
	}
	if _, err := s.SaveFile(ctx, "cv.docx", strings.NewReader("x")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
	r, _ := s.Create(ctx, Application{Company: "Acme", Role: "SRE", ResumeFile: &f}, CreateStrict)
	if err := s.DeleteApplication(ctx, r.Application.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.OpenFile(ctx, f.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unreferenced file should be pruned: %v", err)
	}
	if s.findBlob(f.ID) != "" {
		t.Fatal("blob should be gone")
	}
}

func TestImportIsRepeatableAndSkipsSamples(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	b := Backup{
		Applications: []backupApp{
			{Application: Application{ID: "real1", Ref: "abc123", Company: "Portless", Role: "AI Engineer", Track: "fulltime", Stage: "closed", Outcome: "Role closed"}},
			{Application: Application{ID: "example-1", Company: "Example: Acme", Role: "X", Stage: "closed"}},
		},
		Email: "me@example.com",
	}
	for i := 0; i < 2; i++ {
		if _, err := s.Import(ctx, b, ""); err != nil {
			t.Fatal(err)
		}
	}
	apps, _ := s.ListApplications(ctx)
	if len(apps) != 1 || apps[0].Ref != "abc123" || apps[0].Outcome != "Role closed" {
		t.Fatalf("%+v", apps)
	}
}
