package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// NudgeDays is how long an application can sit in Applied before it is flagged.
const NudgeDays = 7

// Stages lists the pipeline columns in board order. "skipped" is a decision not
// to apply, kept so the same job isn't picked up again.
var Stages = []string{"saved", "applied", "screening", "interview", "offer", "closed", "skipped"}

// SkipReason is why a job was skipped. "other" needs details.
type SkipReason struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// SkipReasons are the categories Job Radar used, so its records import as they are.
var SkipReasons = []SkipReason{
	{"rate", "Rate too low"},
	{"location", "Location not applicable"},
	{"timezone", "Time zone doesn't fit"},
	{"seniority", "Seniority mismatch"},
	{"language", "Language required"},
	{"not_interested", "Not interested"},
	{"other", "Other"},
}

// SkipLabel returns the label for a reason key, or the key itself.
func SkipLabel(key string) string {
	for _, r := range SkipReasons {
		if r.Key == key {
			return r.Label
		}
	}
	return key
}

// SkipWhy is the reason label followed by the details, for messages.
func SkipWhy(reason, note string) string {
	if note == "" {
		return SkipLabel(reason)
	}
	return SkipLabel(reason) + ": " + note
}

func validSkipReason(key string) bool {
	for _, r := range SkipReasons {
		if r.Key == key {
			return true
		}
	}
	return false
}

func skipKeys() string {
	keys := make([]string, len(SkipReasons))
	for i, r := range SkipReasons {
		keys[i] = r.Key
	}
	return strings.Join(keys, ", ")
}

// Application is one tracked job. JSON names match the original tracker's
// backup format so its exports import unchanged.
type Application struct {
	ID         string `json:"id"`
	Ref        string `json:"ref"`
	Company    string `json:"company"`
	Role       string `json:"role"`
	Track      string `json:"track"`
	Source     string `json:"source"`
	Stage      string `json:"stage"`
	Outcome    string `json:"outcome"`
	Applied    string `json:"applied"`
	Comp       string `json:"comp"`
	NextStep   string `json:"nextStep"`
	NextDate   string `json:"nextDate"`
	Contact    string `json:"contact"`
	URL        string `json:"url"`
	Notes      string `json:"notes"`
	LetterID   string `json:"letterId"`
	LetterText string `json:"letterText"`
	ResumeID   string `json:"resumeId"`
	CoverFile  *File  `json:"coverFile"`
	ResumeFile *File  `json:"resumeFile"`
	// Set only in the skipped stage: a SkipReasons key, the details, and the day.
	SkipReason string `json:"skipReason"`
	SkipNote   string `json:"skipNote"`
	Skipped    string `json:"skipped"`
	Version    int64  `json:"version"`
	CreatedAt  int64  `json:"createdAt"`
	UpdatedAt  int64  `json:"updatedAt"`

	// Derived on read, never stored.
	Due     string `json:"due"`
	Replied bool   `json:"replied"`
	Sent    bool   `json:"sent"`
}

// File is an attachment's metadata; the bytes live in the attachments folder.
type File struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Type      string `json:"type"`
	CreatedAt int64  `json:"-"`
}

func today() string { return time.Now().Format("2006-01-02") }

func fmtDay(d string) string {
	t, err := time.ParseInLocation("2006-01-02", d, time.Local)
	if err != nil {
		return d
	}
	return t.Format("2 Jan")
}

// derive fills the computed fields.
func (a *Application) derive() {
	a.Sent = a.Stage != "saved" && !((a.Stage == "closed" || a.Stage == "skipped") && a.Applied == "")
	a.Replied = a.Stage == "screening" || a.Stage == "interview" || a.Stage == "offer" ||
		(a.Stage == "closed" && a.Outcome != "" && a.Outcome != "No response" && a.Outcome != "Role closed")
	a.Due = dueReason(a)
}

func dueReason(a *Application) string {
	if a.Stage == "closed" || a.Stage == "skipped" {
		return ""
	}
	step := a.NextStep
	if step == "" {
		step = "next step"
	}
	now := today()
	if a.NextDate != "" && a.NextDate <= now {
		if a.NextDate == now {
			return "Due today: " + step
		}
		return "Overdue since " + fmtDay(a.NextDate) + ": " + step
	}
	if a.Stage == "applied" && a.NextDate == "" && a.Applied != "" {
		if t, err := time.ParseInLocation("2006-01-02", a.Applied, time.Local); err == nil {
			n, _ := time.ParseInLocation("2006-01-02", now, time.Local)
			if d := int(n.Sub(t).Hours() / 24); d >= NudgeDays {
				return fmt.Sprintf("%d days since applying, no reply. Send a follow-up or mark it closed.", d)
			}
		}
	}
	return ""
}

// clean validates input and applies the rules that hold for every writer.
func (a *Application) clean() error {
	a.Company, a.Role = strings.TrimSpace(a.Company), strings.TrimSpace(a.Role)
	if a.Company == "" || a.Role == "" {
		return fmt.Errorf("%w: company and role are required", ErrInvalid)
	}
	u, err := cleanLink(a.URL)
	if err != nil {
		return err
	}
	a.URL = u
	if a.Track == "" {
		a.Track = "fulltime"
	}
	if a.Track != "fulltime" && a.Track != "contract" {
		return fmt.Errorf("%w: track must be fulltime or contract", ErrInvalid)
	}
	if a.Stage == "" {
		a.Stage = "saved"
	}
	valid := false
	for _, s := range Stages {
		valid = valid || s == a.Stage
	}
	if !valid {
		return fmt.Errorf("%w: stage must be one of %s", ErrInvalid, strings.Join(Stages, ", "))
	}
	for name, d := range map[string]string{"applied": a.Applied, "nextDate": a.NextDate, "skipped": a.Skipped} {
		if d != "" {
			if _, err := time.Parse("2006-01-02", d); err != nil {
				return fmt.Errorf("%w: %s must be YYYY-MM-DD", ErrInvalid, name)
			}
		}
	}
	if a.Stage != "closed" {
		a.Outcome = ""
	}
	if a.Stage == "skipped" {
		a.SkipNote = strings.TrimSpace(a.SkipNote)
		if a.SkipReason == "" {
			a.SkipReason = "other"
		}
		if !validSkipReason(a.SkipReason) {
			return fmt.Errorf("%w: skip reason must be one of %s", ErrInvalid, skipKeys())
		}
		if a.SkipReason == "other" && a.SkipNote == "" {
			return fmt.Errorf("%w: say why in the skip details when the reason is other", ErrInvalid)
		}
		if a.Skipped == "" {
			a.Skipped = today()
		}
	} else {
		a.SkipReason, a.SkipNote, a.Skipped = "", "", ""
	}
	// Reaching an active stage means it went out, so the date can't stay blank.
	if a.Applied == "" && a.Stage != "saved" && a.Stage != "closed" && a.Stage != "skipped" {
		a.Applied = today()
	}
	return nil
}

// cleanLink accepts only web links, because the board renders the job link as
// a clickable href and agents fill it from pages they've read. A link written
// without a scheme, such as "linkedin.com/jobs/view/123", gets https.
func cleanLink(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	bad := fmt.Errorf("%w: the job link must be a web address starting with https:// or http://", ErrInvalid)
	u, err := url.Parse(raw)
	if err != nil {
		return "", bad
	}
	if u.Scheme == "" {
		raw = "https://" + strings.TrimLeft(raw, "/")
		if u, err = url.Parse(raw); err != nil {
			return "", bad
		}
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", bad
	}
	return raw, nil
}

const appColumns = `a.id, a.ref, a.company, a.role, a.track, a.source, a.stage, a.outcome, a.applied, a.comp,
	a.next_step, a.next_date, a.contact, a.url, a.notes, a.letter_id, a.letter_text, a.resume_id,
	a.skip_reason, a.skip_note, a.skipped, a.version, a.created_at, a.updated_at,
	cf.id, cf.name, cf.size, cf.type, rf.id, rf.name, rf.size, rf.type
	FROM applications a
	LEFT JOIN files cf ON cf.id = a.cover_file_id
	LEFT JOIN files rf ON rf.id = a.resume_file_id`

type scanner interface{ Scan(...any) error }

func scanApp(r scanner) (Application, error) {
	var a Application
	var cID, cName, cType, rID, rName, rType sql.NullString
	var cSize, rSize sql.NullInt64
	err := r.Scan(&a.ID, &a.Ref, &a.Company, &a.Role, &a.Track, &a.Source, &a.Stage, &a.Outcome, &a.Applied, &a.Comp,
		&a.NextStep, &a.NextDate, &a.Contact, &a.URL, &a.Notes, &a.LetterID, &a.LetterText, &a.ResumeID,
		&a.SkipReason, &a.SkipNote, &a.Skipped, &a.Version, &a.CreatedAt, &a.UpdatedAt,
		&cID, &cName, &cSize, &cType, &rID, &rName, &rSize, &rType)
	if err != nil {
		return a, err
	}
	if cID.Valid {
		a.CoverFile = &File{ID: cID.String, Name: cName.String, Size: cSize.Int64, Type: cType.String}
	}
	if rID.Valid {
		a.ResumeFile = &File{ID: rID.String, Name: rName.String, Size: rSize.Int64, Type: rType.String}
	}
	a.derive()
	return a, nil
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func listApps(ctx context.Context, q queryer) ([]Application, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+appColumns+" ORDER BY a.updated_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Application{}
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListApplications returns every application, most recently updated first.
func (s *Store) ListApplications(ctx context.Context) ([]Application, error) {
	return listApps(ctx, s.db)
}

// GetApplication finds a record by id or reference code (with or without "+").
func (s *Store) GetApplication(ctx context.Context, idOrRef string) (Application, error) {
	key := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(idOrRef), "+"))
	a, err := scanApp(s.db.QueryRowContext(ctx, "SELECT "+appColumns+" WHERE a.id = ? OR a.ref = ? LIMIT 1", idOrRef, key))
	if errors.Is(err, sql.ErrNoRows) {
		return a, fmt.Errorf("%w: application %q", ErrNotFound, idOrRef)
	}
	return a, err
}

// Verdict is the outcome of a duplicate check.
type Verdict string

const (
	VerdictNew               Verdict = "new"                // nothing like it on record
	VerdictPossibleDuplicate Verdict = "possible_duplicate" // similar role, needs a human or agent judgement
	VerdictAlreadyTracked    Verdict = "already_tracked"    // same job on record, not yet sent
	VerdictAlreadySent       Verdict = "already_sent"       // same job, application already went out
	VerdictSkipped           Verdict = "skipped"            // same job, deliberately skipped
)

// Match is one existing record that resembles the candidate.
type Match struct {
	ID        string `json:"id"`
	Ref       string `json:"ref"`
	Company   string `json:"company"`
	Role      string `json:"role"`
	Stage     string `json:"stage"`
	Outcome   string `json:"outcome,omitempty"`
	Applied   string `json:"applied,omitempty"`
	URL       string `json:"url,omitempty"`
	Sent      bool   `json:"sent"`
	MatchedOn string `json:"matchedOn"` // "url", "company+role" or "similar_role"
	// Set when the match is in the skipped stage.
	SkipReason string `json:"skipReason,omitempty"`
	SkipNote   string `json:"skipNote,omitempty"`
	Skipped    string `json:"skipped,omitempty"`
}

// CheckResult reports what is already known about a candidate application.
type CheckResult struct {
	Verdict Verdict `json:"verdict"`
	// SafeToSend is false whenever an application for this job already went out
	// or the match is uncertain.
	SafeToSend bool    `json:"safeToSend"`
	Message    string  `json:"message"`
	Matches    []Match `json:"matches"`
	// OtherRoles lists different roles at the same company. Informational only.
	OtherRoles []Match `json:"otherRoles"`
}

const similarRoleThreshold = 0.6

func check(apps []Application, company, role, rawURL, excludeID string) CheckResult {
	urlKey := URLKey(rawURL)
	idKey := IdentityKey(company, role)
	coKey := CompanyKey(company)

	res := CheckResult{Matches: []Match{}, OtherRoles: []Match{}}
	exact, fuzzy := false, false
	for i := range apps {
		a := &apps[i]
		if a.ID == excludeID {
			continue
		}
		m := Match{ID: a.ID, Ref: a.Ref, Company: a.Company, Role: a.Role, Stage: a.Stage,
			Outcome: a.Outcome, Applied: a.Applied, URL: a.URL, Sent: a.Sent,
			SkipReason: a.SkipReason, SkipNote: a.SkipNote, Skipped: a.Skipped}
		theirCo := CompanyKey(a.Company)
		switch {
		case urlKey != "" && URLKey(a.URL) == urlKey:
			m.MatchedOn, exact = "url", true
			res.Matches = append(res.Matches, m)
		case idKey != "" && IdentityKey(a.Company, a.Role) == idKey:
			m.MatchedOn, exact = "company+role", true
			res.Matches = append(res.Matches, m)
		case sameCompany(coKey, theirCo) && role != "":
			if roleSimilarity(role, a.Role) >= similarRoleThreshold {
				m.MatchedOn, fuzzy = "similar_role", true
				res.Matches = append(res.Matches, m)
			} else {
				res.OtherRoles = append(res.OtherRoles, m)
			}
		case sameCompany(coKey, theirCo):
			res.OtherRoles = append(res.OtherRoles, m)
		}
	}
	sort.SliceStable(res.Matches, func(i, j int) bool { return res.Matches[i].Sent && !res.Matches[j].Sent })

	anySent := false
	var skipped *Match
	for i, m := range res.Matches {
		anySent = anySent || (m.Sent && m.MatchedOn != "similar_role")
		if skipped == nil && m.Stage == "skipped" && m.MatchedOn != "similar_role" {
			skipped = &res.Matches[i]
		}
	}
	switch {
	case anySent:
		res.Verdict, res.SafeToSend = VerdictAlreadySent, false
		m := res.Matches[0]
		res.Message = fmt.Sprintf("Already sent: %s, %s (stage %s, applied %s, ref %s). Do not send again.",
			m.Company, m.Role, m.Stage, orDash(m.Applied), m.Ref)
	case skipped != nil:
		res.Verdict, res.SafeToSend = VerdictSkipped, false
		res.Message = fmt.Sprintf("Skipped on %s (%s). Do not apply. To apply after all, the user first moves %s, %s (ref %s) out of Skipped on the board.",
			orDash(skipped.Skipped), SkipWhy(skipped.SkipReason, skipped.SkipNote), skipped.Company, skipped.Role, skipped.Ref)
	case exact:
		res.Verdict, res.SafeToSend = VerdictAlreadyTracked, true
		res.Message = "Already on the board but not sent yet. Use the existing record, do not add a new one."
	case fuzzy:
		res.Verdict, res.SafeToSend = VerdictPossibleDuplicate, false
		res.Message = "A similar role at this company is on record. Compare before sending."
	default:
		res.Verdict, res.SafeToSend = VerdictNew, true
		res.Message = "No matching record."
	}
	return res
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// Check looks for existing records resembling the candidate. It writes nothing.
func (s *Store) Check(ctx context.Context, company, role, rawURL string) (CheckResult, error) {
	apps, err := s.ListApplications(ctx)
	if err != nil {
		return CheckResult{}, err
	}
	return check(apps, company, role, rawURL, ""), nil
}

// CreateMode controls how Create treats suspected duplicates.
type CreateMode int

const (
	// CreateStrict refuses exact and possible duplicates.
	CreateStrict CreateMode = iota
	// CreateAllowSimilar refuses exact duplicates only.
	CreateAllowSimilar
	// CreateForce records even an exact duplicate. UI only, after a human confirms.
	CreateForce
)

// CreateResult says whether a record was written. When Created is false, Check
// explains why and Application is the zero value.
type CreateResult struct {
	Created     bool         `json:"created"`
	Application *Application `json:"application,omitempty"`
	Check       CheckResult  `json:"check"`
}

// Create inserts an application unless it duplicates one. The check and insert
// share a transaction, and the unique indexes catch anything that slips through,
// so a retried or concurrent call cannot produce a second record.
func (s *Store) Create(ctx context.Context, a Application, mode CreateMode) (CreateResult, error) {
	if err := a.clean(); err != nil {
		return CreateResult{}, err
	}
	now := time.Now().UnixMilli()
	a.ID, a.CreatedAt, a.UpdatedAt = newID(), now, now
	a.Ref = ComputeRef(a.ID)

	var res CreateResult
	err := s.tx(ctx, func(tx *sql.Tx) error {
		apps, err := listApps(ctx, tx)
		if err != nil {
			return err
		}
		res.Check = check(apps, a.Company, a.Role, a.URL, "")
		blocked := false
		switch res.Check.Verdict {
		case VerdictAlreadySent, VerdictAlreadyTracked, VerdictSkipped:
			blocked = mode != CreateForce
		case VerdictPossibleDuplicate:
			blocked = mode == CreateStrict
		}
		if blocked {
			return nil
		}
		allowDup := 0
		if mode == CreateForce {
			allowDup = 1
		}
		if err := insertApp(ctx, tx, &a, allowDup); err != nil {
			return err
		}
		res.Created = true
		return nil
	})
	if isUniqueViolation(err) { // lost a race with another process
		apps, lerr := s.ListApplications(ctx)
		if lerr != nil {
			return res, lerr
		}
		return CreateResult{Check: check(apps, a.Company, a.Role, a.URL, "")}, nil
	}
	if err != nil {
		return CreateResult{}, err
	}
	if res.Created {
		got, err := s.GetApplication(ctx, a.ID)
		if err != nil {
			return res, err
		}
		res.Application = &got
	}
	return res, nil
}

func fileIDArg(f *File) any {
	if f == nil || f.ID == "" {
		return nil
	}
	return f.ID
}

func insertApp(ctx context.Context, tx *sql.Tx, a *Application, allowDup int) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO applications
		(id, ref, company, role, track, source, stage, outcome, applied, comp, next_step, next_date, contact, url, notes,
		 letter_id, letter_text, resume_id, cover_file_id, resume_file_id, skip_reason, skip_note, skipped,
		 url_key, identity_key, allow_dup, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.Ref, a.Company, a.Role, a.Track, a.Source, a.Stage, a.Outcome, a.Applied, a.Comp, a.NextStep, a.NextDate,
		a.Contact, a.URL, a.Notes, a.LetterID, a.LetterText, a.ResumeID, fileIDArg(a.CoverFile), fileIDArg(a.ResumeFile),
		a.SkipReason, a.SkipNote, a.Skipped,
		URLKey(a.URL), IdentityKey(a.Company, a.Role), allowDup, a.CreatedAt, a.UpdatedAt)
	return err
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// Update replaces an application. a.Version must match the stored version, so a
// form opened before an agent's change can't silently overwrite it. An edit
// that would turn the record into a duplicate fails with ErrConflict unless
// force is set.
func (s *Store) Update(ctx context.Context, a Application, force bool) (Application, error) {
	if err := a.clean(); err != nil {
		return a, err
	}
	var orphans []string
	err := s.tx(ctx, func(tx *sql.Tx) error {
		prev, err := scanApp(tx.QueryRowContext(ctx, "SELECT "+appColumns+" WHERE a.id = ?", a.ID))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: application %q", ErrNotFound, a.ID)
		}
		if err != nil {
			return err
		}
		if a.Version != prev.Version {
			return ErrConflict
		}
		allowDup := "allow_dup"
		if force {
			allowDup = "1"
		}
		_, err = tx.ExecContext(ctx, `UPDATE applications SET
			company=?, role=?, track=?, source=?, stage=?, outcome=?, applied=?, comp=?, next_step=?, next_date=?, contact=?,
			url=?, notes=?, letter_id=?, letter_text=?, resume_id=?, cover_file_id=?, resume_file_id=?,
			skip_reason=?, skip_note=?, skipped=?, url_key=?, identity_key=?,
			allow_dup=`+allowDup+`, version=version+1, updated_at=? WHERE id=?`,
			a.Company, a.Role, a.Track, a.Source, a.Stage, a.Outcome, a.Applied, a.Comp, a.NextStep, a.NextDate, a.Contact,
			a.URL, a.Notes, a.LetterID, a.LetterText, a.ResumeID, fileIDArg(a.CoverFile), fileIDArg(a.ResumeFile),
			a.SkipReason, a.SkipNote, a.Skipped, URLKey(a.URL), IdentityKey(a.Company, a.Role), time.Now().UnixMilli(), a.ID)
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: another record already has this link or company and role", ErrConflict)
		}
		if err != nil {
			return err
		}
		if prev.CoverFile != nil && (a.CoverFile == nil || a.CoverFile.ID != prev.CoverFile.ID) {
			orphans = append(orphans, prev.CoverFile.ID)
		}
		if prev.ResumeFile != nil && (a.ResumeFile == nil || a.ResumeFile.ID != prev.ResumeFile.ID) {
			orphans = append(orphans, prev.ResumeFile.ID)
		}
		return nil
	})
	if err != nil {
		return a, err
	}
	s.pruneFiles(ctx, orphans)
	return s.GetApplication(ctx, a.ID)
}

// Patch applies fn to the latest copy of a record, retrying if another writer
// gets in between. It suits small changes such as a stage move or an MCP edit.
func (s *Store) Patch(ctx context.Context, idOrRef string, fn func(*Application) error) (Application, error) {
	for attempt := 0; ; attempt++ {
		a, err := s.GetApplication(ctx, idOrRef)
		if err != nil {
			return a, err
		}
		if err := fn(&a); err != nil {
			return a, err
		}
		got, err := s.Update(ctx, a, false)
		if errors.Is(err, ErrConflict) && attempt < 3 {
			continue
		}
		return got, err
	}
}

// MarkSent records that the application went out, exactly once. If it was
// already sent the record is returned untouched with changed=false.
func (s *Store) MarkSent(ctx context.Context, idOrRef, date, letterID, resumeID string) (a Application, changed bool, err error) {
	if date == "" {
		date = today()
	}
	a, err = s.GetApplication(ctx, idOrRef)
	if err != nil {
		return a, false, err
	}
	if a.Sent {
		return a, false, nil
	}
	a, err = s.Patch(ctx, a.ID, func(p *Application) error {
		if p.Sent { // another client won the race
			return nil
		}
		p.Stage, p.Applied, p.Outcome = "applied", date, ""
		if letterID != "" {
			p.LetterID = letterID
		}
		if resumeID != "" {
			p.ResumeID = resumeID
		}
		changed = true
		return nil
	})
	return a, changed, err
}

// DeleteApplication removes a record and any attachment only it referenced.
func (s *Store) DeleteApplication(ctx context.Context, id string) error {
	a, err := s.GetApplication(ctx, id)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM applications WHERE id = ?", a.ID); err != nil {
		return err
	}
	var orphans []string
	for _, f := range []*File{a.CoverFile, a.ResumeFile} {
		if f != nil {
			orphans = append(orphans, f.ID)
		}
	}
	s.pruneFiles(ctx, orphans)
	return nil
}

// Summary is a pipeline snapshot for agents.
type Summary struct {
	ByStage      map[string]int `json:"byStage"`
	InFlight     int            `json:"inFlight"`
	Sent         int            `json:"sent"`
	Replied      int            `json:"replied"`
	ResponseRate string         `json:"responseRate"`
	FollowUpsDue int            `json:"followUpsDue"`
}

// Summarize computes the same figures the board's header shows.
func Summarize(apps []Application) Summary {
	s := Summary{ByStage: map[string]int{}, ResponseRate: "n/a"}
	for _, st := range Stages {
		s.ByStage[st] = 0
	}
	for i := range apps {
		a := &apps[i]
		s.ByStage[a.Stage]++
		if a.Sent {
			s.Sent++
			if a.Stage != "closed" && a.Stage != "skipped" {
				s.InFlight++
			}
			if a.Replied {
				s.Replied++
			}
		}
		if a.Due != "" {
			s.FollowUpsDue++
		}
	}
	if s.Sent > 0 {
		s.ResponseRate = fmt.Sprintf("%d%%", (s.Replied*100+s.Sent/2)/s.Sent)
	}
	return s
}
