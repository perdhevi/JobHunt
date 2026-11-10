package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Lead is a scored listing Job Radar found on a job board. It stays a lead
// until it is saved, marked applied or skipped, which creates an application.
type Lead struct {
	UID         string   `json:"uid"` // source:id
	Source      string   `json:"source"`
	SourceName  string   `json:"sourceName"`
	Title       string   `json:"title"`
	Company     string   `json:"company"`
	URL         string   `json:"url"`         // the board's own page, which its terms ask us to link
	OriginalURL string   `json:"originalUrl"` // the employer's posting, when known
	Location    string   `json:"location"`
	Salary      string   `json:"salary"`
	Posted      string   `json:"posted"` // YYYY-MM-DD
	Score       int      `json:"score"`
	Hits        []string `json:"hits"`
	Flags       []string `json:"flags"`
	AlsoOn      []string `json:"alsoOn"`
	AltUIDs     []string `json:"altUids"`
	Snippet     string   `json:"snippet"`
	Antispam    string   `json:"antispam"` // Remote OK's "mention this word" check
	FirstSeen   string   `json:"firstSeen"`

	// Derived on read: the board record this job is on, if any.
	Application *LeadApp `json:"application,omitempty"`
}

// LeadApp is the part of an application the Radar list shows.
type LeadApp struct {
	ID         string `json:"id"`
	Ref        string `json:"ref"`
	Stage      string `json:"stage"`
	Applied    string `json:"applied,omitempty"`
	SkipReason string `json:"skipReason,omitempty"`
	SkipNote   string `json:"skipNote,omitempty"`
	Skipped    string `json:"skipped,omitempty"`
}

func leadApp(a *Application) *LeadApp {
	return &LeadApp{ID: a.ID, Ref: a.Ref, Stage: a.Stage, Applied: a.Applied,
		SkipReason: a.SkipReason, SkipNote: a.SkipNote, Skipped: a.Skipped}
}

// LeadKeepDays is how long a lead nobody acted on stays listed.
const LeadKeepDays = 60

func jsonList(v []string) string {
	if v == nil {
		v = []string{}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func parseList(s string) []string {
	out := []string{}
	json.Unmarshal([]byte(s), &out)
	return out
}

const leadColumns = `uid, source, source_name, title, company, url, original_url, location, salary, posted, score,
	hits, flags, also_on, alt_uids, snippet, antispam, first_seen, application_id`

func scanLead(r scanner) (Lead, string, error) {
	var l Lead
	var hits, flags, alsoOn, alt, appID string
	err := r.Scan(&l.UID, &l.Source, &l.SourceName, &l.Title, &l.Company, &l.URL, &l.OriginalURL, &l.Location,
		&l.Salary, &l.Posted, &l.Score, &hits, &flags, &alsoOn, &alt, &l.Snippet, &l.Antispam, &l.FirstSeen, &appID)
	l.Hits, l.Flags, l.AlsoOn, l.AltUIDs = parseList(hits), parseList(flags), parseList(alsoOn), parseList(alt)
	return l, appID, err
}

// appIndex finds the application a listing corresponds to: the one it was
// acted on as, else one with the same link or company and role.
type appIndex struct {
	byID, byURL, byIdentity map[string]*Application
}

func newAppIndex(apps []Application) appIndex {
	ix := appIndex{map[string]*Application{}, map[string]*Application{}, map[string]*Application{}}
	for i := range apps {
		a := &apps[i]
		ix.byID[a.ID] = a
		if k := URLKey(a.URL); k != "" {
			ix.byURL[k] = a
		}
		if k := IdentityKey(a.Company, a.Role); k != "" {
			ix.byIdentity[k] = a
		}
	}
	return ix
}

func (ix appIndex) find(appID, company, title string, urls ...string) *Application {
	if a := ix.byID[appID]; a != nil {
		return a
	}
	for _, u := range urls {
		if a := ix.byURL[URLKey(u)]; a != nil && u != "" {
			return a
		}
	}
	return ix.byIdentity[IdentityKey(company, title)]
}

// TrackedApps returns a lookup for the application already on the board for a
// listing, or nil when there is none.
func (s *Store) TrackedApps(ctx context.Context) (func(company, title string, urls ...string) *Application, error) {
	apps, err := s.ListApplications(ctx)
	if err != nil {
		return nil, err
	}
	ix := newAppIndex(apps)
	return func(company, title string, urls ...string) *Application { return ix.find("", company, title, urls...) }, nil
}

// SaveLeads adds new leads and refreshes known ones, keeping the day each was
// first seen and any application it was acted on as.
func (s *Store) SaveLeads(ctx context.Context, leads []Lead, today string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, l := range leads {
			_, err := tx.ExecContext(ctx, `INSERT INTO radar_leads (`+leadColumns+`)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'')
				ON CONFLICT(uid) DO UPDATE SET source=excluded.source, source_name=excluded.source_name,
				  title=excluded.title, company=excluded.company, url=excluded.url, original_url=excluded.original_url,
				  location=excluded.location, salary=excluded.salary, posted=excluded.posted, score=excluded.score,
				  hits=excluded.hits, flags=excluded.flags, also_on=excluded.also_on, alt_uids=excluded.alt_uids,
				  snippet=excluded.snippet, antispam=excluded.antispam`,
				l.UID, l.Source, l.SourceName, l.Title, l.Company, l.URL, l.OriginalURL, l.Location, l.Salary, l.Posted,
				l.Score, jsonList(l.Hits), jsonList(l.Flags), jsonList(l.AlsoOn), jsonList(l.AltUIDs), l.Snippet, l.Antispam,
				firstNonEmpty(l.FirstSeen, today))
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// PruneLeads drops leads first seen before the cutoff that were never acted on
// or whose application is gone.
func (s *Store) PruneLeads(ctx context.Context, before string) (int64, error) {
	r, err := s.db.ExecContext(ctx, `DELETE FROM radar_leads WHERE first_seen < ?
		AND application_id NOT IN (SELECT id FROM applications)`, before)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

// ListLeads returns every stored lead, best score first, each with the
// application it's on if any.
func (s *Store) ListLeads(ctx context.Context) ([]Lead, error) {
	apps, err := s.ListApplications(ctx)
	if err != nil {
		return nil, err
	}
	ix := newAppIndex(apps)
	rows, err := s.db.QueryContext(ctx, "SELECT "+leadColumns+" FROM radar_leads ORDER BY score DESC, first_seen DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Lead{}
	for rows.Next() {
		l, appID, err := scanLead(rows)
		if err != nil {
			return nil, err
		}
		if a := ix.find(appID, l.Company, l.Title, l.URL, l.OriginalURL); a != nil {
			l.Application = leadApp(a)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// GetLead finds a lead by its uid or the uid of one of its copies on other boards.
func (s *Store) GetLead(ctx context.Context, uid string) (Lead, error) {
	leads, err := s.ListLeads(ctx)
	if err != nil {
		return Lead{}, err
	}
	uid = strings.TrimSpace(uid)
	for _, l := range leads {
		if l.UID == uid {
			return l, nil
		}
	}
	for _, l := range leads {
		for _, a := range l.AltUIDs {
			if a == uid {
				return l, nil
			}
		}
	}
	return Lead{}, fmt.Errorf("%w: lead %q", ErrNotFound, uid)
}

// LeadAction is what to do with a lead: put it on the board in a stage.
type LeadAction struct {
	Stage      string // saved, applied or skipped
	SkipReason string
	SkipNote   string
}

// ActOnLead puts a lead on the board. If the job is already there, nothing is
// written, the lead is linked to that record, and Created is false.
func (s *Store) ActOnLead(ctx context.Context, uid string, act LeadAction, mode CreateMode) (CreateResult, error) {
	switch act.Stage {
	case "saved", "applied", "skipped":
	default:
		return CreateResult{}, fmt.Errorf("%w: a lead can be saved, applied or skipped", ErrInvalid)
	}
	l, err := s.GetLead(ctx, uid)
	if err != nil {
		return CreateResult{}, err
	}
	a := Application{Company: l.Company, Role: l.Title, URL: l.URL, Source: l.SourceName, Stage: act.Stage,
		Comp: l.Salary, Notes: leadNotes(l), SkipReason: act.SkipReason, SkipNote: act.SkipNote}
	if act.Stage == "applied" {
		a.Applied = today()
	}
	if a.Company == "" {
		a.Company = "(company not given)"
	}
	res, err := s.Create(ctx, a, mode)
	if err != nil {
		return res, err
	}
	link := ""
	if res.Created {
		link = res.Application.ID
	} else {
		for _, m := range res.Check.Matches {
			if m.MatchedOn != "similar_role" {
				link = m.ID
				break
			}
		}
	}
	if link != "" {
		if _, err := s.db.ExecContext(ctx, "UPDATE radar_leads SET application_id = ? WHERE uid = ?", link, l.UID); err != nil {
			return res, err
		}
	}
	return res, nil
}

// leadNotes carries what Radar knew about the listing onto the card.
func leadNotes(l Lead) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Found by Job Radar on %s via %s", l.FirstSeen, l.SourceName)
	if len(l.AlsoOn) > 0 {
		fmt.Fprintf(&b, " (also on %s)", strings.Join(l.AlsoOn, ", "))
	}
	fmt.Fprintf(&b, ", score %d.", l.Score)
	if l.OriginalURL != "" {
		b.WriteString("\nOriginal posting: " + l.OriginalURL)
	}
	if l.Location != "" {
		b.WriteString("\nLocation: " + l.Location)
	}
	if len(l.Flags) > 0 {
		b.WriteString("\nFlags: " + strings.Join(l.Flags, ", "))
	}
	if l.Antispam != "" {
		b.WriteString("\nApply note: mention the word " + l.Antispam + " (Remote OK's check).")
	}
	return b.String()
}

// LinkLead points a lead at an application, as the Radar importer does.
func (s *Store) LinkLead(ctx context.Context, uid, appID string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE radar_leads SET application_id = ? WHERE uid = ?", appID, uid)
	return err
}

// SeenListings returns the uids of listings already scored.
func (s *Store) SeenListings(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT uid FROM radar_seen")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out[u] = true
	}
	return out, rows.Err()
}

// SeenKeepDays is how long a listing id is remembered after it leaves the feeds.
const SeenKeepDays = 120

// MarkSeen records listing ids from today's feeds and forgets ids no feed has
// carried for SeenKeepDays.
func (s *Store) MarkSeen(ctx context.Context, uids []string, today string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, u := range uids {
			if _, err := tx.ExecContext(ctx, `INSERT INTO radar_seen(uid, seen) VALUES(?, ?)
				ON CONFLICT(uid) DO UPDATE SET seen = excluded.seen`, u, today); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM radar_seen WHERE seen < ?", daysBefore(today, SeenKeepDays))
		return err
	})
}

func daysBefore(day string, n int) string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		t = time.Now()
	}
	return t.AddDate(0, 0, -n).Format("2006-01-02")
}

// SkillObservation is the skills one relevant listing asked for.
type SkillObservation struct {
	UID     string   `json:"uid"`
	Date    string   `json:"date"`
	Title   string   `json:"title"`
	Company string   `json:"company"`
	Skills  []string `json:"skills"`
}

// SkillKeepDays bounds how far back observations are kept.
const SkillKeepDays = 365

// RecordSkills stores observations, replacing any for the same listing, and
// drops ones older than SkillKeepDays.
func (s *Store) RecordSkills(ctx context.Context, obs []SkillObservation, today string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, o := range obs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO radar_skills(uid, date, title, company, skills) VALUES(?,?,?,?,?)
				ON CONFLICT(uid) DO UPDATE SET date=excluded.date, title=excluded.title, company=excluded.company, skills=excluded.skills`,
				o.UID, o.Date, o.Title, o.Company, jsonList(o.Skills)); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM radar_skills WHERE date < ?", daysBefore(today, SkillKeepDays))
		return err
	})
}

// SkillObservations returns observations dated on or after since.
func (s *Store) SkillObservations(ctx context.Context, since string) ([]SkillObservation, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT uid, date, title, company, skills FROM radar_skills WHERE date >= ?", since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SkillObservation{}
	for rows.Next() {
		var o SkillObservation
		var skills string
		if err := rows.Scan(&o.UID, &o.Date, &o.Title, &o.Company, &skills); err != nil {
			return nil, err
		}
		o.Skills = parseList(skills)
		out = append(out, o)
	}
	return out, rows.Err()
}

// ErrNoSetting is returned by GetJSONSetting when the key is unset.
var ErrNoSetting = errors.New("setting not set")

// GetJSONSetting decodes a JSON setting into v.
func (s *Store) GetJSONSetting(ctx context.Context, key string, v any) error {
	raw, err := s.GetSetting(ctx, key)
	if err != nil {
		return err
	}
	if raw == "" {
		return ErrNoSetting
	}
	return json.Unmarshal([]byte(raw), v)
}

// SetJSONSetting stores v as a JSON setting.
func (s *Store) SetJSONSetting(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.SetSetting(ctx, key, string(b))
}
