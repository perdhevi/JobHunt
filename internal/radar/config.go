// Package radar is Job Radar: once a day it reads remote-job boards' official
// public feeds and target companies' applicant-tracking boards, merges the
// same job across boards, scores each new listing against a keyword profile,
// flags location and time-zone restrictions, and records the skills listings
// ask for. Matches become leads; acting on one puts it on the board.
//
// Only official public feeds and APIs are read, never HTML pages, and every
// lead keeps its board's URL, as the boards' terms ask.
package radar

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"time"

	"github.com/dlclark/regexp2"
)

//go:embed defaults/config.json
var DefaultConfig string

//go:embed defaults/skills.json
var DefaultSkills string

// Settings keys holding the user's JSON, kept as written so comments and the
// order of sources survive editing.
const (
	SettingConfig = "radar.config"
	SettingSkills = "radar.skills"
	settingLast   = "radar.lastRun"
	settingFetch  = "radar.lastFetch"
)

// Ordered is a JSON object whose key order matters: sources run in the order
// written, and the first board to list a job wins.
type Ordered[T any] struct {
	Keys   []string
	Values map[string]T
}

func (o *Ordered[T]) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("expected an object")
	}
	o.Keys, o.Values = nil, map[string]T{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		k := tok.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return err
		}
		if len(k) > 0 && k[0] == '_' { // "_comment" and similar notes
			continue
		}
		var v T
		if err := json.Unmarshal(raw, &v); err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
		if _, dup := o.Values[k]; !dup {
			o.Keys = append(o.Keys, k)
		}
		o.Values[k] = v
	}
	_, err = dec.Token()
	return err
}

// SourceConfig is one board's settings. Fields vary by board, so it stays a map.
type SourceConfig map[string]any

func (c SourceConfig) Bool(k string, def bool) bool {
	if v, ok := c[k].(bool); ok {
		return v
	}
	return def
}

func (c SourceConfig) Num(k string, def float64) float64 {
	if v, ok := c[k].(float64); ok {
		return v
	}
	return def
}

func (c SourceConfig) Str(k, def string) string {
	if v, ok := c[k].(string); ok && v != "" {
		return v
	}
	return def
}

// Companies is an ATS source's {"board token": "Company name"} list, in order.
func (c SourceConfig) Companies() [][2]string {
	m, _ := c["companies"].(map[string]any)
	raw, _ := json.Marshal(m)
	var o Ordered[string]
	json.Unmarshal(raw, &o)
	out := make([][2]string, 0, len(o.Keys))
	for _, k := range o.Keys {
		out = append(out, [2]string{k, o.Values[k]})
	}
	return out
}

// Config mirrors Job Radar's config.json.
type Config struct {
	UserAgent          string                `json:"user_agent"`
	AutoRun            *bool                 `json:"auto_run"`
	Sources            Ordered[SourceConfig] `json:"sources"`
	MinScore           int                   `json:"min_score"`
	MaxAgeDays         int                   `json:"max_age_days"`
	TitleWeights       Ordered[int]          `json:"title_weights"`
	DescriptionWeights Ordered[int]          `json:"description_weights"`
	TagWeights         Ordered[int]          `json:"tag_weights"`
	TitleExclude       string                `json:"title_exclude"`
	Restrictions       Ordered[string]       `json:"restrictions"`
	RestrictionPenalty int                   `json:"restriction_penalty"`
	LocationOK         string                `json:"location_ok"`
	LocationPenalty    int                   `json:"location_penalty"`
	TimezoneFlags      Ordered[string]       `json:"timezone_flags"`
	TimezonePenalty    *int                  `json:"timezone_penalty"`
}

// AutoRunOn says whether serve checks the boards once a day by itself.
func (c *Config) AutoRunOn() bool { return c.AutoRun == nil || *c.AutoRun }

// SkillsConfig mirrors Job Radar's skills.json.
type SkillsConfig struct {
	WindowDays    int    `json:"window_days"`
	MinJobs       int    `json:"min_jobs_for_report"`
	RelevantTitle string `json:"relevant_title"`
	Profile       struct {
		Have     []string `json:"have"`
		Learning []string `json:"learning"`
	} `json:"profile"`
	Skills Ordered[Ordered[string]] `json:"skills"`
}

// Patterns are Python regular expressions in the original files, some with
// look-behind, which Go's regexp lacks. regexp2 accepts them, and the timeout
// keeps a pathological pattern from hanging a run on hostile board text.
const matchTimeout = 250 * time.Millisecond

func compile(pattern string) (*regexp2.Regexp, error) {
	re, err := regexp2.Compile(pattern, regexp2.IgnoreCase)
	if err != nil {
		return nil, err
	}
	re.MatchTimeout = matchTimeout
	return re, nil
}

// find returns the first match of re in s, or "".
func find(re *regexp2.Regexp, s string) string {
	m, err := re.FindStringMatch(s)
	if err != nil || m == nil {
		return ""
	}
	return m.String()
}

func matches(re *regexp2.Regexp, s string) bool {
	ok, err := re.MatchString(s)
	return err == nil && ok
}

// ParseConfig decodes config.json text and checks every pattern compiles.
func ParseConfig(text string) (*Config, *Scorer, error) {
	var c Config
	if err := json.Unmarshal([]byte(text), &c); err != nil {
		return nil, nil, fmt.Errorf("radar config: %w", err)
	}
	if c.UserAgent == "" {
		c.UserAgent = "jobhunt-radar/1.0 (personal job search)"
	}
	if c.MaxAgeDays <= 0 {
		c.MaxAgeDays = 10
	}
	for _, k := range c.Sources.Keys {
		if _, ok := adapters[k]; !ok {
			return nil, nil, fmt.Errorf("radar config: unknown source %q (known: %s)", k, knownSources())
		}
	}
	sc, err := newScorer(&c)
	if err != nil {
		return nil, nil, fmt.Errorf("radar config: %w", err)
	}
	return &c, sc, nil
}

// ParseSkills decodes skills.json text and checks every pattern compiles.
func ParseSkills(text string) (*SkillModel, error) {
	var c SkillsConfig
	if err := json.Unmarshal([]byte(text), &c); err != nil {
		return nil, fmt.Errorf("radar skills: %w", err)
	}
	m, err := newSkillModel(&c)
	if err != nil {
		return nil, fmt.Errorf("radar skills: %w", err)
	}
	return m, nil
}
