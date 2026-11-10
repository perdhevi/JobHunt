package store

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// EvidenceKinds are the kinds of proof, roughly strongest first.
var EvidenceKinds = []string{"project", "repo", "certificate", "course", "writeup", "work"}

// Evidence is one piece of proof for a skill: a running project, a repo, a
// certificate, a write-up, or a described piece of work.
type Evidence struct {
	ID        string `json:"id"`
	Skill     string `json:"skill"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	Date      string `json:"date"` // when it was done, YYYY-MM-DD
	Note      string `json:"note"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

// SkillGoal is what the user plans to do about a skill, and by when.
type SkillGoal struct {
	Skill string `json:"skill"`
	Goal  string `json:"goal"`
	Due   string `json:"due"` // YYYY-MM-DD or ""
}

func tooLong(name, s string, n int) error {
	if utf8.RuneCountInString(s) > n {
		return fmt.Errorf("%w: %s is longer than %d characters", ErrInvalid, name, n)
	}
	return nil
}

func validDate(name, d string) error {
	if d == "" {
		return nil
	}
	if _, err := time.Parse("2006-01-02", d); err != nil {
		return fmt.Errorf("%w: %s must be YYYY-MM-DD", ErrInvalid, name)
	}
	return nil
}

func (e *Evidence) clean() error {
	e.Skill, e.Title, e.Note, e.Date = strings.TrimSpace(e.Skill), strings.TrimSpace(e.Title), strings.TrimSpace(e.Note), strings.TrimSpace(e.Date)
	if e.Skill == "" || e.Title == "" {
		return fmt.Errorf("%w: skill and title are required", ErrInvalid)
	}
	ok := false
	for _, k := range EvidenceKinds {
		ok = ok || k == e.Kind
	}
	if !ok {
		return fmt.Errorf("%w: kind must be one of %s", ErrInvalid, strings.Join(EvidenceKinds, ", "))
	}
	// The link is rendered as an href and may be shared, so web links only.
	u, err := cleanLink(e.URL)
	if err != nil {
		return fmt.Errorf("%w: the proof link must be a web address starting with https:// or http://", ErrInvalid)
	}
	e.URL = u
	for _, c := range []struct {
		name, v string
		n       int
	}{{"skill", e.Skill, 100}, {"title", e.Title, 200}, {"link", e.URL, 2000}, {"note", e.Note, 4000}} {
		if err := tooLong(c.name, c.v, c.n); err != nil {
			return err
		}
	}
	return validDate("date", e.Date)
}

// ListEvidence returns all proof, grouped by skill, newest first within each.
func (s *Store) ListEvidence(ctx context.Context) ([]Evidence, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, skill, kind, title, url, date, note, created_at, updated_at
		FROM skill_evidence ORDER BY skill, date DESC, created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Evidence{}
	for rows.Next() {
		var e Evidence
		if err := rows.Scan(&e.ID, &e.Skill, &e.Kind, &e.Title, &e.URL, &e.Date, &e.Note, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// SaveEvidence adds proof (empty ID) or replaces it.
func (s *Store) SaveEvidence(ctx context.Context, e Evidence) (Evidence, error) {
	if err := e.clean(); err != nil {
		return e, err
	}
	now := time.Now().UnixMilli()
	e.UpdatedAt = now
	if e.ID == "" {
		e.ID, e.CreatedAt = newID(), now
		_, err := s.db.ExecContext(ctx, `INSERT INTO skill_evidence (id, skill, kind, title, url, date, note, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?)`, e.ID, e.Skill, e.Kind, e.Title, e.URL, e.Date, e.Note, e.CreatedAt, e.UpdatedAt)
		return e, err
	}
	r, err := s.db.ExecContext(ctx, `UPDATE skill_evidence SET skill=?, kind=?, title=?, url=?, date=?, note=?, updated_at=? WHERE id=?`,
		e.Skill, e.Kind, e.Title, e.URL, e.Date, e.Note, e.UpdatedAt, e.ID)
	if err != nil {
		return e, err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return e, fmt.Errorf("%w: evidence %q", ErrNotFound, e.ID)
	}
	err = s.db.QueryRowContext(ctx, "SELECT created_at FROM skill_evidence WHERE id = ?", e.ID).Scan(&e.CreatedAt)
	return e, err
}

// DeleteEvidence removes one piece of proof.
func (s *Store) DeleteEvidence(ctx context.Context, id string) error {
	r, err := s.db.ExecContext(ctx, "DELETE FROM skill_evidence WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: evidence %q", ErrNotFound, id)
	}
	return nil
}

// ListGoals returns every skill goal.
func (s *Store) ListGoals(ctx context.Context) ([]SkillGoal, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT skill, goal, due FROM skill_goals ORDER BY skill")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SkillGoal{}
	for rows.Next() {
		var g SkillGoal
		if err := rows.Scan(&g.Skill, &g.Goal, &g.Due); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// SetGoal sets a skill's goal; an empty goal removes it.
func (s *Store) SetGoal(ctx context.Context, g SkillGoal) (SkillGoal, error) {
	g.Skill, g.Goal, g.Due = strings.TrimSpace(g.Skill), strings.TrimSpace(g.Goal), strings.TrimSpace(g.Due)
	if g.Skill == "" {
		return g, fmt.Errorf("%w: skill is required", ErrInvalid)
	}
	if g.Goal == "" {
		_, err := s.db.ExecContext(ctx, "DELETE FROM skill_goals WHERE skill = ?", g.Skill)
		return SkillGoal{Skill: g.Skill}, err
	}
	if err := tooLong("goal", g.Goal, 500); err != nil {
		return g, err
	}
	if err := tooLong("skill", g.Skill, 100); err != nil {
		return g, err
	}
	if err := validDate("due", g.Due); err != nil {
		return g, err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO skill_goals(skill, goal, due) VALUES(?,?,?)
		ON CONFLICT(skill) DO UPDATE SET goal=excluded.goal, due=excluded.due`, g.Skill, g.Goal, g.Due)
	return g, err
}

// LeadSkills returns the skills recorded for each listing uid that was
// observed for the skill-gap report.
func (s *Store) LeadSkills(ctx context.Context) (map[string][]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT uid, skills FROM radar_skills")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var uid, skills string
		if err := rows.Scan(&uid, &skills); err != nil {
			return nil, err
		}
		out[uid] = parseList(skills)
	}
	return out, rows.Err()
}
