package radar

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jobhunt/internal/store"
)

func newRadar(t *testing.T) *Radar {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &Radar{Store: s}
}

// The fixtures were written in October 2026.
var fixtureNow = time.Date(2026, 10, 16, 9, 0, 0, 0, time.Local)

func fixtureRun(t *testing.T, r *Radar, opt Options) Summary {
	t.Helper()
	opt.Fixtures, opt.Now = filepath.Join("testdata", "fixtures"), fixtureNow
	sum, err := r.Run(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

func byTitle(leads []store.Lead, part string) *store.Lead {
	for i := range leads {
		if strings.Contains(strings.ToLower(leads[i].Title), strings.ToLower(part)) {
			return &leads[i]
		}
	}
	return nil
}

func TestFixtureRun(t *testing.T) {
	r := newRadar(t)
	sum := fixtureRun(t, r, Options{})
	for _, l := range sum.Leads {
		t.Logf("%3d %-45s %-22s %-16s %v %v", l.Score, l.Title, l.Company, l.SourceName, l.AlsoOn, l.Flags)
	}
	if !sum.OK || sum.Matches == 0 {
		t.Fatalf("no matches: %+v", sum)
	}
	sources := map[string]bool{}
	for _, l := range sum.Leads {
		sources[l.Source] = true
	}
	for _, s := range []string{"himalayas", "jobicy", "remoteok", "remotive", "wwr", "greenhouse", "ashby"} {
		if !sources[s] {
			t.Errorf("no lead from %s", s)
		}
	}

	acme := 0
	for _, l := range sum.Leads {
		if strings.HasPrefix(l.Company, "Acme AI") && strings.HasPrefix(l.Title, "Senior AI Engineer") {
			acme++
			if l.Source != "himalayas" || strings.Join(l.AlsoOn, ",") != "Jobicy" {
				t.Errorf("Acme AI should be via Himalayas, also on Jobicy: %+v", l)
			}
		}
	}
	if acme != 1 {
		t.Errorf("Acme AI shown %d times, want once", acme)
	}
	if l := byTitle(sum.Leads, "solutions"); l == nil || l.Source != "himalayas" || !contains(l.AlsoOn, "Lever") || l.OriginalURL == "" {
		t.Errorf("Tasman's solutions role should be via Himalayas, also on Lever, with the Lever page as original: %+v", l)
	}
	for _, l := range sum.Leads {
		for _, f := range l.Flags {
			if f == "US only" {
				t.Errorf("US-only role shown: %s", l.Title)
			}
		}
		if strings.Contains(l.Location, "Europe") {
			t.Errorf("Europe-only role shown: %s", l.Title)
		}
	}
	if l := byTitle(sum.Leads, "Technical Customer Support"); l != nil && l.Antispam != "TRUMP" {
		t.Errorf("Remote OK's anti-spam word not kept: %+v", l)
	}

	// Stored, and a second run sees nothing new.
	leads, err := r.Store.ListLeads(context.Background())
	if err != nil || len(leads) != sum.Matches {
		t.Fatalf("stored %d leads, want %d (%v)", len(leads), sum.Matches, err)
	}
	if again := fixtureRun(t, r, Options{}); again.Scanned != 0 {
		t.Errorf("second run scanned %d listings, want 0", again.Scanned)
	}

	m, _, err := r.Skills(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	obs, _ := r.Store.SkillObservations(context.Background(), "2000-01-01")
	rep := m.Report(obs, fixtureNow)
	if !rep.Enough || len(rep.Skills) == 0 {
		t.Errorf("skill report empty: %+v", rep)
	}
}

func TestActingOnLeadHidesIt(t *testing.T) {
	r := newRadar(t)
	ctx := context.Background()
	sum := fixtureRun(t, r, Options{})
	first := sum.Leads[0]

	res, err := r.Store.ActOnLead(ctx, first.UID, store.LeadAction{Stage: "skipped", SkipReason: "location", SkipNote: "needs a US address"}, store.CreateAllowSimilar)
	if err != nil || !res.Created {
		t.Fatalf("%+v %v", res, err)
	}
	if a := res.Application; a.Stage != "skipped" || a.SkipReason != "location" || a.SkipNote != "needs a US address" || a.Skipped == "" {
		t.Errorf("skipped application: %+v", a)
	}
	l, _ := r.Store.GetLead(ctx, first.UID)
	if l.Application == nil || l.Application.Stage != "skipped" {
		t.Errorf("lead not linked: %+v", l)
	}
	// The same job can't be added or sent now.
	chk, _ := r.Store.Check(ctx, first.Company, first.Title, "")
	if chk.Verdict != store.VerdictSkipped || chk.SafeToSend || !strings.Contains(chk.Message, "needs a US address") {
		t.Errorf("check after skip: %+v", chk)
	}
	// A fresh scan of everything leaves it out of the new matches.
	again := fixtureRun(t, r, Options{All: true})
	for _, x := range again.Leads {
		if x.UID == first.UID {
			t.Error("skipped job came back as a new match")
		}
	}
	if again.Hidden == 0 {
		t.Error("hidden count should include the skipped job")
	}
}

func TestPatternsKeepPythonSyntax(t *testing.T) {
	m, err := ParseSkills(DefaultSkills)
	if err != nil {
		t.Fatal(err) // the Go pattern uses look-behind
	}
	j := &Job{Title: "Backend engineer", Date: fixtureNow}
	o := m.Observe(j, "We use Python, Go, and Postgres. Javascript is a plus.", false)
	got := strings.Join(o.Skills, ",")
	for _, want := range []string{"Go", "Python", "PostgreSQL", "JavaScript"} {
		if !contains(o.Skills, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	if contains(m.Observe(j, "Java and JavaScript", false).Skills, "Go") {
		t.Error("Go matched where it isn't mentioned")
	}
}

func TestBadConfigRefused(t *testing.T) {
	for _, bad := range []string{
		`{"sources": {"nosuchboard": {}}}`,
		`{"sources": {}, "title_weights": {"(unclosed": 1}}`,
		`not json`,
	} {
		if _, _, err := ParseConfig(bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	if _, _, err := ParseConfig(DefaultConfig); err != nil {
		t.Errorf("default config: %v", err)
	}
}

func TestHelpers(t *testing.T) {
	if got := fixMojibake("cafÃ© Ã¼ber"); got != "café über" {
		t.Errorf("fixMojibake = %q", got)
	}
	if got := money(70000, 110000, "USD", "yearly"); got != "$70,000–110,000/yr" {
		t.Errorf("money = %q", got)
	}
	if got := money(30, 50, "", ""); got != "" {
		t.Errorf("money for an hourly-looking number = %q", got)
	}
	if dedupeKey("Acme AI Inc.", "Senior AI Engineer (LLM Agents)") != dedupeKey("Acme AI", "Senior AI Engineer (LLM Agents)") {
		t.Error("dedupeKey should ignore legal suffixes")
	}
	if externalURL("https://himalayas.app/x", "himalayas.app") != "" || externalURL("javascript:alert(1)") != "" {
		t.Error("externalURL let a board or non-web link through")
	}
}
