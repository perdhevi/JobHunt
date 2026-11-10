package radar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"jobhunt/internal/store"
)

// ImportReport counts what importing a Job Radar folder did.
type ImportReport struct {
	Config, Skills            string // "imported", "kept existing" or "not found"
	Leads, Seen, Observations int
	Skipped, Applied          int      // tracker entries added to the board
	AlreadyOnBoard            int      // tracker entries that matched a record here
	Problems                  []string // entries that couldn't be imported, and why
}

// The old freeze quick-pick reasons, for "frozen" entries Job Radar never migrated.
var freezeChips = map[string]string{"location doesn't fit": "location", "eu/uk only": "location",
	"us time zones": "timezone", "seniority mismatch": "seniority", "salary too low": "rate",
	"language required": "language", "not interested": "not_interested"}

type trackerEntry struct {
	Status      string   `json:"status"`
	Title       string   `json:"title"`
	Company     string   `json:"company"`
	URL         string   `json:"url"`
	SourceName  string   `json:"source_name"`
	AltUIDs     []string `json:"alt_uids"`
	OriginalURL string   `json:"original_url"`
	Marked      string   `json:"marked"`
	Note        string   `json:"note"`
	Reason      string   `json:"reason"`
}

type indexEntry struct {
	Source      string   `json:"source"`
	SourceName  string   `json:"source_name"`
	Title       string   `json:"title"`
	Company     string   `json:"company"`
	URL         string   `json:"url"`
	OriginalURL string   `json:"original_url"`
	Location    string   `json:"location"`
	Salary      string   `json:"salary"`
	Date        string   `json:"date"`
	Score       int      `json:"score"`
	Hits        []string `json:"hits"`
	Flags       []string `json:"flags"`
	AlsoOn      []string `json:"also_on"`
	AltUIDs     []string `json:"alt_uids"`
	Snippet     string   `json:"snippet"`
	Antispam    *string  `json:"antispam_word"`
	FirstSeen   string   `json:"first_seen"`
}

func readJSON(path string, v any) (bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return false, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return true, nil
}

// migrateID prefixes the bare Remote OK ids very old Radar versions stored.
func migrateID(id string) string {
	if strings.Contains(id, ":") {
		return id
	}
	return "remoteok:" + id
}

// Import loads a Job Radar folder (the one holding config.json and data/).
// Settings already saved here are kept. Skipped and applied jobs go on the
// board; any already there are linked, not duplicated. Safe to re-run.
func (r *Radar) Import(ctx context.Context, dir string) (ImportReport, error) {
	var rep ImportReport
	if _, err := os.Stat(filepath.Join(dir, "job_radar.py")); err != nil {
		if _, err2 := os.Stat(filepath.Join(dir, "data", "match_index.json")); err2 != nil {
			return rep, fmt.Errorf("%s doesn't look like a Job Radar folder (no job_radar.py or data/match_index.json)", dir)
		}
	}
	today := time.Now().Format("2006-01-02")

	for _, s := range []struct {
		file, key string
		out       *string
		check     func(string) error
	}{
		{"config.json", SettingConfig, &rep.Config, func(t string) error { _, _, err := ParseConfig(t); return err }},
		{"skills.json", SettingSkills, &rep.Skills, func(t string) error { _, err := ParseSkills(t); return err }},
	} {
		b, err := os.ReadFile(filepath.Join(dir, s.file))
		if err != nil {
			*s.out = "not found"
			continue
		}
		if cur, _ := r.Store.GetSetting(ctx, s.key); cur != "" {
			*s.out = "kept existing"
			continue
		}
		if err := s.check(string(b)); err != nil {
			return rep, err
		}
		if err := r.Store.SetSetting(ctx, s.key, string(b)); err != nil {
			return rep, err
		}
		*s.out = "imported"
	}

	data := filepath.Join(dir, "data")
	var seen []any
	if ok, err := readJSON(filepath.Join(data, "seen.json"), &seen); err != nil {
		return rep, err
	} else if ok {
		uids := make([]string, 0, len(seen))
		for _, s := range seen {
			uids = append(uids, migrateID(fmt.Sprint(s)))
		}
		if err := r.Store.MarkSeen(ctx, uids, today); err != nil {
			return rep, err
		}
		rep.Seen = len(uids)
	}

	var obs map[string]store.SkillObservation
	if ok, err := readJSON(filepath.Join(data, "skill_observations.json"), &obs); err != nil {
		return rep, err
	} else if ok {
		list := make([]store.SkillObservation, 0, len(obs))
		for uid, o := range obs {
			o.UID = migrateID(uid)
			list = append(list, o)
		}
		if err := r.Store.RecordSkills(ctx, list, today); err != nil {
			return rep, err
		}
		rep.Observations = len(list)
	}

	var index map[string]indexEntry
	if _, err := readJSON(filepath.Join(data, "match_index.json"), &index); err != nil {
		return rep, err
	}
	leads := make([]store.Lead, 0, len(index))
	for uid, e := range index {
		l := store.Lead{UID: migrateID(uid), Source: e.Source, SourceName: e.SourceName, Title: e.Title, Company: e.Company,
			URL: webURL(e.URL), OriginalURL: webURL(e.OriginalURL), Location: e.Location, Salary: e.Salary,
			Posted: truncate(e.Date, 10), Score: e.Score, Hits: e.Hits, Flags: e.Flags, AlsoOn: e.AlsoOn,
			AltUIDs: e.AltUIDs, Snippet: e.Snippet, FirstSeen: e.FirstSeen}
		if e.Antispam != nil {
			l.Antispam = *e.Antispam
		}
		if l.Source == "" {
			l.Source, _, _ = strings.Cut(l.UID, ":")
		}
		leads = append(leads, l)
	}
	if err := r.Store.SaveLeads(ctx, leads, today); err != nil {
		return rep, err
	}
	rep.Leads = len(leads)

	var tracker map[string]trackerEntry
	if _, err := readJSON(filepath.Join(data, "tracker.json"), &tracker); err != nil {
		return rep, err
	}
	for uid, e := range tracker {
		uid = migrateID(uid)
		if e.Status == "frozen" {
			e.Status, e.Reason = "skipped", "other"
			if k, ok := freezeChips[strings.ToLower(strings.TrimSpace(e.Note))]; ok {
				e.Reason, e.Note = k, ""
			}
		}
		if e.Status != "skipped" && e.Status != "applied" {
			continue
		}
		a := store.Application{Company: firstNonBlank(e.Company, "(company not given)"), Role: firstNonBlank(e.Title, "(role not given)"),
			URL: webURL(e.URL), Source: e.SourceName, Stage: e.Status}
		var notes []string
		if e.OriginalURL != "" {
			notes = append(notes, "Original posting: "+e.OriginalURL)
		}
		if e.Status == "skipped" {
			a.SkipReason, a.SkipNote, a.Skipped = e.Reason, e.Note, validDay(e.Marked)
			if a.SkipReason == "" {
				a.SkipReason = "other"
			}
			if a.SkipReason == "other" && a.SkipNote == "" {
				a.SkipNote = "Skipped in Job Radar"
			}
		} else {
			a.Applied = validDay(e.Marked)
			if e.Note != "" {
				notes = append([]string{e.Note}, notes...)
			}
		}
		a.Notes = strings.Join(append([]string{"Imported from Job Radar (" + uid + ")."}, notes...), "\n")
		res, err := r.Store.Create(ctx, a, store.CreateStrict)
		if err != nil {
			rep.Problems = append(rep.Problems, fmt.Sprintf("%s (%s, %s): %v", uid, a.Company, a.Role, err))
			continue
		}
		appID := ""
		switch {
		case res.Created:
			appID = res.Application.ID
			if e.Status == "skipped" {
				rep.Skipped++
			} else {
				rep.Applied++
			}
		case res.Check.Verdict == store.VerdictPossibleDuplicate:
			rep.Problems = append(rep.Problems, fmt.Sprintf("%s (%s, %s): %s Add it on the board if it's a different job.", uid, a.Company, a.Role, res.Check.Message))
			continue
		default:
			appID = res.Check.Matches[0].ID
			rep.AlreadyOnBoard++
		}
		for _, u := range append([]string{uid}, e.AltUIDs...) {
			r.Store.LinkLead(ctx, u, appID)
		}
	}
	return rep, nil
}

func validDay(d string) string {
	d = truncate(strings.TrimSpace(d), 10)
	if _, err := time.Parse("2006-01-02", d); err != nil {
		return ""
	}
	return d
}
