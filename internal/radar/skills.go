package radar

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/dlclark/regexp2"

	"jobhunt/internal/store"
)

// SkillModel is skills.json compiled: which skills to look for, which titles
// count as relevant, and the user's profile.
type SkillModel struct {
	cfg        *SkillsConfig
	relevant   *regexp2.Regexp
	have, lrn  map[string]bool
	categoryOf map[string]string
	categories []string
	names      []string
	patterns   map[string]*regexp2.Regexp
}

func newSkillModel(c *SkillsConfig) (*SkillModel, error) {
	if c.WindowDays <= 0 {
		c.WindowDays = 90
	}
	m := &SkillModel{cfg: c, have: map[string]bool{}, lrn: map[string]bool{},
		categoryOf: map[string]string{}, patterns: map[string]*regexp2.Regexp{}}
	var err error
	if m.relevant, err = compile(firstNonEmptyPattern(c.RelevantTitle, `.`)); err != nil {
		return nil, fmt.Errorf("relevant_title: %w", err)
	}
	for _, s := range c.Profile.Have {
		m.have[s] = true
	}
	for _, s := range c.Profile.Learning {
		m.lrn[s] = true
	}
	for _, cat := range c.Skills.Keys {
		m.categories = append(m.categories, cat)
		group := c.Skills.Values[cat]
		for _, name := range group.Keys {
			re, err := compile(group.Values[name])
			if err != nil {
				return nil, fmt.Errorf("skill %q: %w", name, err)
			}
			if _, dup := m.patterns[name]; !dup {
				m.names = append(m.names, name)
			}
			m.categoryOf[name], m.patterns[name] = cat, re
		}
	}
	sort.Strings(m.names)
	return m, nil
}

// Status is "have", "learning" or "gap".
func (m *SkillModel) Status(skill string) string {
	switch {
	case m.have[skill]:
		return "have"
	case m.lrn[skill]:
		return "learning"
	}
	return "gap"
}

// Observe returns what to record for a listing, or nil if its title isn't a
// role the user would consider.
func (m *SkillModel) Observe(j *Job, body string, excluded bool) *store.SkillObservation {
	if excluded || !matches(m.relevant, j.Title) {
		return nil
	}
	text := j.Title + "\n" + body
	skills := []string{}
	for _, n := range m.names {
		if matches(m.patterns[n], text) {
			skills = append(skills, n)
		}
	}
	return &store.SkillObservation{UID: j.UID, Date: j.Date.Format("2006-01-02"), Title: j.Title, Company: j.Company, Skills: skills}
}

// SkillRow is one skill's line in the report.
type SkillRow struct {
	Skill  string `json:"skill"`
	Area   string `json:"area"`
	Status string `json:"status"`
	Count  int    `json:"count"`
	Share  int    `json:"share"` // percent of listings in the window
}

// AreaRow totals one area of skills.
type AreaRow struct {
	Area  string `json:"area"`
	Share int    `json:"share"` // percent of listings naming any skill in it
	Have  int    `json:"have"`
	Seen  int    `json:"seen"`
}

// SkillReport is the skill-gap report over the rolling window.
type SkillReport struct {
	Date       string     `json:"date"`
	WindowDays int        `json:"windowDays"`
	Listings   int        `json:"listings"`
	MinJobs    int        `json:"minJobs"`
	Enough     bool       `json:"enough"`
	Asked      int        `json:"asked"`    // listings naming at least one tracked skill
	Coverage   int        `json:"coverage"` // percent of a typical listing's skills you have
	Gaps       []SkillRow `json:"gaps"`     // most requested skills you don't list, top 10
	Skills     []SkillRow `json:"skills"`
	Areas      []AreaRow  `json:"areas"`
}

func pct(n, d int) int {
	if d == 0 {
		return 0
	}
	return int(math.Round(float64(n) * 100 / float64(d)))
}

// Report builds the skill-gap report from observations.
func (m *SkillModel) Report(obs []store.SkillObservation, now time.Time) SkillReport {
	cutoff := now.AddDate(0, 0, -m.cfg.WindowDays).Format("2006-01-02")
	var window []store.SkillObservation
	for _, o := range obs {
		if o.Date >= cutoff {
			window = append(window, o)
		}
	}
	n := len(window)
	r := SkillReport{Date: now.Format("2006-01-02"), WindowDays: m.cfg.WindowDays, Listings: n, MinJobs: m.cfg.MinJobs,
		Gaps: []SkillRow{}, Skills: []SkillRow{}, Areas: []AreaRow{}}
	if n < m.cfg.MinJobs || n == 0 {
		return r
	}
	r.Enough = true
	counts := map[string]int{}
	var coverage float64
	for _, o := range window {
		if len(o.Skills) == 0 {
			continue
		}
		r.Asked++
		have := 0
		for _, s := range o.Skills {
			counts[s]++
			if m.Status(s) == "have" {
				have++
			}
		}
		coverage += float64(have) / float64(len(o.Skills))
	}
	if r.Asked > 0 {
		r.Coverage = int(math.Round(coverage * 100 / float64(r.Asked)))
	}
	for s, c := range counts {
		area := m.categoryOf[s]
		if area == "" {
			area = "(no longer tracked)"
		}
		r.Skills = append(r.Skills, SkillRow{Skill: s, Area: area, Status: m.Status(s), Count: c, Share: pct(c, n)})
	}
	sort.Slice(r.Skills, func(i, j int) bool {
		if r.Skills[i].Count != r.Skills[j].Count {
			return r.Skills[i].Count > r.Skills[j].Count
		}
		return r.Skills[i].Skill < r.Skills[j].Skill
	})
	for _, row := range r.Skills {
		if row.Status == "gap" && len(r.Gaps) < 10 {
			r.Gaps = append(r.Gaps, row)
		}
	}
	for _, cat := range m.categories {
		row := AreaRow{Area: cat}
		for s := range counts {
			if m.categoryOf[s] == cat {
				row.Seen++
				if m.Status(s) == "have" {
					row.Have++
				}
			}
		}
		if row.Seen == 0 {
			continue
		}
		hits := 0
		for _, o := range window {
			for _, s := range o.Skills {
				if m.categoryOf[s] == cat {
					hits++
					break
				}
			}
		}
		row.Share = pct(hits, n)
		r.Areas = append(r.Areas, row)
	}
	return r
}
