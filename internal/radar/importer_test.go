package radar

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestImportRadarFolder(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "job_radar.py"), "")
	writeFile(t, filepath.Join(dir, "config.json"), DefaultConfig)
	writeFile(t, filepath.Join(dir, "data", "seen.json"), `["1137506", "himalayas:abc"]`)
	writeFile(t, filepath.Join(dir, "data", "match_index.json"), `{
	  "himalayas:senior-ai": {"source": "himalayas", "source_name": "Himalayas", "title": "Senior AI Engineer", "company": "Acme",
	    "url": "https://himalayas.app/companies/acme/jobs/senior-ai", "location": "Worldwide", "salary": "", "date": "2026-10-14T00:00:00+00:00",
	    "score": 20, "hits": ["llm"], "flags": [], "also_on": [], "alt_uids": [], "snippet": "x", "antispam_word": null, "first_seen": "2026-10-14"},
	  "remotive:9": {"source": "remotive", "source_name": "Remotive", "title": "Support Engineer", "company": "Lumen",
	    "url": "https://remotive.com/remote-jobs/9", "location": "USA", "date": "2026-10-13", "score": 7, "first_seen": "2026-10-13"}
	}`)
	writeFile(t, filepath.Join(dir, "data", "tracker.json"), `{
	  "himalayas:senior-ai": {"status": "skipped", "title": "Senior AI Engineer", "company": "Acme",
	    "url": "https://himalayas.app/companies/acme/jobs/senior-ai", "marked": "2026-10-15", "note": "needs US work authorization", "reason": "location"},
	  "remotive:9": {"status": "frozen", "title": "Support Engineer", "company": "Lumen", "url": "https://remotive.com/remote-jobs/9",
	    "marked": "2026-10-15", "note": "salary too low"},
	  "manual:https://example.com/careers/42": {"status": "applied", "title": "Platform Engineer", "company": "Example Co",
	    "url": "https://example.com/careers/42", "marked": "2026-10-16", "note": "sent CV"}
	}`)

	r := newRadar(t)
	ctx := context.Background()
	rep, err := r.Import(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Config != "imported" || rep.Leads != 2 || rep.Seen != 2 || rep.Skipped != 2 || rep.Applied != 1 || len(rep.Problems) != 0 {
		t.Fatalf("%+v", rep)
	}
	apps, _ := r.Store.ListApplications(ctx)
	byRole := map[string]string{}
	for _, a := range apps {
		byRole[a.Role] = a.Stage + "|" + a.SkipReason + "|" + a.SkipNote + "|" + a.Skipped + "|" + a.Applied
	}
	for role, want := range map[string]string{
		"Senior AI Engineer": "skipped|location|needs US work authorization|2026-10-15|",
		"Support Engineer":   "skipped|rate||2026-10-15|", // an old freeze chip becomes its category
		"Platform Engineer":  "applied||||2026-10-16",
	} {
		if byRole[role] != want {
			t.Errorf("%s: got %q, want %q", role, byRole[role], want)
		}
	}
	leads, _ := r.Store.ListLeads(ctx)
	for _, l := range leads {
		if l.Application == nil || l.Application.Stage != "skipped" {
			t.Errorf("lead %s not linked to its skipped record: %+v", l.UID, l.Application)
		}
	}
	seen, _ := r.Store.SeenListings(ctx)
	if !seen["remoteok:1137506"] {
		t.Error("a bare Remote OK id wasn't migrated")
	}

	// Again: nothing new, and settings saved here are kept.
	again, err := r.Import(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if again.Config != "kept existing" || again.Skipped+again.Applied != 0 || again.AlreadyOnBoard != 3 {
		t.Errorf("second import: %+v", again)
	}
}

func TestParseCompanyRef(t *testing.T) {
	for in, want := range map[string]string{
		"https://job-boards.greenhouse.io/acme":             "greenhouse:acme",
		"https://boards.greenhouse.io/acme/jobs/123":        "greenhouse:acme",
		"https://jobs.lever.co/tasman-labs":                 "lever:tasman-labs",
		"https://jobs.eu.lever.co/tasman":                   "lever:eu:tasman",
		"https://jobs.ashbyhq.com/kopi-ai/123":              "ashby:kopi-ai",
		"Ashby:Kopi":                                        "ashby:Kopi",
		"https://boards-api.greenhouse.io/v1/boards/x/jobs": "greenhouse:x",
	} {
		ats, tok, ok := ParseCompanyRef(in)
		if got := ats + ":" + tok; !ok || got != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
	if _, _, ok := ParseCompanyRef("https://example.com/careers"); ok {
		t.Error("accepted a non-ATS link")
	}
}

func TestSetPathKeepsOrderAndNotes(t *testing.T) {
	out, err := setPath(DefaultConfig, []string{"sources", "lever", "companies", "tasman-labs"}, "Tasman Labs")
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := ParseConfig(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Sources.Values["lever"].Companies(); len(got) != 1 || got[0] != [2]string{"tasman-labs", "Tasman Labs"} {
		t.Errorf("companies = %v", got)
	}
	orig, _, _ := ParseConfig(DefaultConfig)
	if strings.Join(cfg.Sources.Keys, ",") != strings.Join(orig.Sources.Keys, ",") {
		t.Errorf("source order changed: %v", cfg.Sources.Keys)
	}
	if !strings.Contains(out, `"_comment"`) {
		t.Error("notes were dropped")
	}
}
