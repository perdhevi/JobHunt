package radar

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dlclark/regexp2"

	"jobhunt/internal/store"
)

type weighted struct {
	re *regexp2.Regexp
	w  int
}

type named struct {
	name string
	re   *regexp2.Regexp
}

// Scorer holds config.json's patterns, compiled once per run.
type Scorer struct {
	cfg          *Config
	title, desc  []weighted
	titleExclude *regexp2.Regexp
	restrictions []named
	locationOK   *regexp2.Regexp
	timezones    []named
}

func compileWeights(o Ordered[int]) ([]weighted, error) {
	var out []weighted
	for _, p := range o.Keys {
		re, err := compile(p)
		if err != nil {
			return nil, fmt.Errorf("pattern %q: %w", p, err)
		}
		out = append(out, weighted{re, o.Values[p]})
	}
	return out, nil
}

func compileNamed(o Ordered[string]) ([]named, error) {
	var out []named
	for _, k := range o.Keys {
		re, err := compile(o.Values[k])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		out = append(out, named{k, re})
	}
	return out, nil
}

func newScorer(c *Config) (*Scorer, error) {
	s := &Scorer{cfg: c}
	var err error
	if s.title, err = compileWeights(c.TitleWeights); err != nil {
		return nil, fmt.Errorf("title_weights: %w", err)
	}
	if s.desc, err = compileWeights(c.DescriptionWeights); err != nil {
		return nil, fmt.Errorf("description_weights: %w", err)
	}
	if s.restrictions, err = compileNamed(c.Restrictions); err != nil {
		return nil, fmt.Errorf("restrictions: %w", err)
	}
	if s.timezones, err = compileNamed(c.TimezoneFlags); err != nil {
		return nil, fmt.Errorf("timezone_flags: %w", err)
	}
	// An empty exclusion would match every title, so it means "exclude nothing".
	if s.titleExclude, err = compile(firstNonEmptyPattern(c.TitleExclude, `(?!)`)); err != nil {
		return nil, fmt.Errorf("title_exclude: %w", err)
	}
	if s.locationOK, err = compile(firstNonEmptyPattern(c.LocationOK, `.`)); err != nil {
		return nil, fmt.Errorf("location_ok: %w", err)
	}
	return s, nil
}

func firstNonEmptyPattern(p, def string) string {
	if strings.TrimSpace(p) == "" {
		return def
	}
	return p
}

// Excluded reports a title that's clearly out of scope (sales, design, ...).
func (s *Scorer) Excluded(title string) bool { return matches(s.titleExclude, title) }

// Score rates a listing, or returns nil for an excluded title. body is the
// plain-text description.
func (s *Scorer) Score(j *Job, body string) *store.Lead {
	if s.Excluded(j.Title) {
		return nil
	}
	location := fixMojibake(j.Location)
	score := 0
	hits := map[string]bool{}
	for _, w := range s.title {
		if m := find(w.re, j.Title); m != "" {
			score += w.w
			hits["title:"+m] = true
		}
	}
	for _, w := range s.desc {
		if m := find(w.re, body); m != "" {
			score += w.w
			hits[strings.ToLower(m)] = true
		}
	}
	tags := map[string]bool{}
	for _, t := range j.Tags {
		tags[strings.ToLower(t)] = true
	}
	for _, t := range s.cfg.TagWeights.Keys {
		if tags[t] {
			score += s.cfg.TagWeights.Values[t]
		}
	}

	flags := []string{}
	for _, r := range s.restrictions {
		if matches(r.re, body) || matches(r.re, location) {
			flags = append(flags, r.name)
			score -= s.cfg.RestrictionPenalty
		}
	}
	if !matches(s.locationOK, strings.ToLower(location)) {
		flags = append(flags, "Location: "+location)
		score -= s.cfg.LocationPenalty
	}
	if j.TZOK != nil && !*j.TZOK {
		flags = append(flags, "Time zones: "+j.TZNote)
		pen := 3
		if s.cfg.TimezonePenalty != nil {
			pen = *s.cfg.TimezonePenalty
		}
		score -= pen
	}
	for _, t := range s.timezones {
		if matches(t.re, body) {
			flags = append(flags, t.name)
		}
	}

	hitList := make([]string, 0, len(hits))
	for h := range hits {
		hitList = append(hitList, h)
	}
	sort.Strings(hitList)
	if location == "" {
		location = "(not stated)"
	}
	alsoOn := j.AlsoOn
	if alsoOn == nil {
		alsoOn = []string{}
	}
	return &store.Lead{
		UID: j.UID, Source: j.Source, SourceName: j.SourceName, Title: j.Title, Company: j.Company,
		URL: j.URL, OriginalURL: j.OriginalURL, Location: location, Salary: j.Salary,
		Posted: j.Date.Format("2006-01-02"), Score: score, Hits: hitList, Flags: flags,
		AlsoOn: alsoOn, AltUIDs: j.AltUIDs,
		Snippet: strings.ReplaceAll(truncate(body, 280), "\n", " "),
	}
}
