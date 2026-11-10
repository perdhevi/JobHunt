package radar

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"

	"jobhunt/internal/store"
)

// Radar runs the daily scan against one store. Only one run happens at a time.
type Radar struct {
	Store  *store.Store
	Client *http.Client // nil means a client with a 40-second timeout

	mu      sync.Mutex
	running bool
}

// ErrRunning is returned when a scan is already in progress.
var ErrRunning = errors.New("the radar is already checking the boards")

// Options adjust a run, mostly for testing.
type Options struct {
	Fixtures string    // read <dir>/<source>.json|xml instead of the network
	All      bool      // re-score listings already seen
	DryRun   bool      // write nothing
	Now      time.Time // zero means now
	Wait     time.Duration
}

// SourceResult is one board's outcome in a run.
type SourceResult struct {
	Name    string `json:"name"`
	Home    string `json:"home"`
	Count   int    `json:"count"`
	Error   string `json:"error,omitempty"`
	Skipped string `json:"skipped,omitempty"` // why it wasn't fetched this time
}

// Summary describes the last run, for the Radar header.
type Summary struct {
	Date     string         `json:"date"` // local day of the run
	Started  time.Time      `json:"started"`
	Finished time.Time      `json:"finished"`
	OK       bool           `json:"ok"` // at least one board answered
	Sources  []SourceResult `json:"sources"`
	Problems []SourceResult `json:"problems"` // partial failures, e.g. one company's board
	Scanned  int            `json:"scanned"`  // new listings looked at
	Matches  int            `json:"matches"`
	Hidden   int            `json:"hidden"` // matches already on the board
	Error    string         `json:"error,omitempty"`

	Leads []store.Lead `json:"-"` // this run's new matches, for the command line
}

// Config loads the user's config.json, or the default.
func (r *Radar) Config(ctx context.Context) (*Config, *Scorer, string, error) {
	text, err := r.Store.GetSetting(ctx, SettingConfig)
	if err != nil {
		return nil, nil, "", err
	}
	if text == "" {
		text = DefaultConfig
	}
	c, s, err := ParseConfig(text)
	return c, s, text, err
}

// Skills loads the user's skills.json, or the default.
func (r *Radar) Skills(ctx context.Context) (*SkillModel, string, error) {
	text, err := r.Store.GetSetting(ctx, SettingSkills)
	if err != nil {
		return nil, "", err
	}
	if text == "" {
		text = DefaultSkills
	}
	m, err := ParseSkills(text)
	return m, text, err
}

// LastRun returns the previous run's summary, or nil.
func (r *Radar) LastRun(ctx context.Context) (*Summary, error) {
	var s Summary
	err := r.Store.GetJSONSetting(ctx, settingLast, &s)
	if errors.Is(err, store.ErrNoSetting) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// Running reports whether a scan is in progress.
func (r *Radar) Running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running
}

// Run checks every enabled board once and stores new matches as leads.
func (r *Radar) Run(ctx context.Context, opt Options) (Summary, error) {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return Summary{}, ErrRunning
	}
	r.running = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.running = false
		r.mu.Unlock()
	}()

	now := opt.Now
	if now.IsZero() {
		now = time.Now()
	}
	sum := Summary{Date: now.Format("2006-01-02"), Started: time.Now(), Sources: []SourceResult{}, Problems: []SourceResult{}}
	err := r.run(ctx, opt, now, &sum)
	sum.Finished = time.Now()
	if err != nil {
		sum.Error = err.Error()
	}
	if !opt.DryRun {
		if serr := r.Store.SetJSONSetting(context.WithoutCancel(ctx), settingLast, sum); serr != nil && err == nil {
			err = serr
		}
	}
	return sum, err
}

func (r *Radar) run(ctx context.Context, opt Options, now time.Time, sum *Summary) error {
	cfg, scorer, _, err := r.Config(ctx)
	if err != nil {
		return err
	}
	skills, _, err := r.Skills(ctx)
	if err != nil {
		return err
	}
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 40 * time.Second}
	}
	wait := opt.Wait
	if wait == 0 {
		wait = 15 * time.Second
	}
	f := &fetcher{ctx: ctx, client: client, userAgent: cfg.UserAgent, fixtures: opt.Fixtures,
		cutoff: now.AddDate(0, 0, -cfg.MaxAgeDays), problems: map[string]string{}, wait: wait}

	lastFetch := map[string]time.Time{}
	if opt.Fixtures == "" {
		r.Store.GetJSONSetting(ctx, settingFetch, &lastFetch)
	}

	// Fetch. One board failing must not stop the others.
	var raw []Job
	for _, key := range cfg.Sources.Keys {
		scfg := cfg.Sources.Values[key]
		if !scfg.Bool("enabled", true) {
			continue
		}
		a := adapters[key]
		if opt.Fixtures == "" && !configured(key, scfg) {
			continue // an ATS source with no companies yet
		}
		res := SourceResult{Name: a.name, Home: a.home}
		if last, ok := lastFetch[key]; ok && opt.Fixtures == "" && now.Sub(last) < a.minInterval {
			res.Skipped = fmt.Sprintf("checked at %s; %s asks for at least %s between checks", last.Local().Format("15:04"), a.name, a.minInterval)
			sum.Sources = append(sum.Sources, res)
			continue
		}
		got, err := a.fetch(f, scfg)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			res.Error = err.Error()
			log.Printf("radar [%s] failed: %v", key, err)
		} else {
			uids := map[string]bool{}
			for _, j := range got {
				uids[j.UID] = true
			}
			res.Count = len(uids)
			sum.OK = true
			lastFetch[key] = now
			raw = append(raw, got...)
		}
		sum.Sources = append(sum.Sources, res)
	}
	for _, k := range sortedKeys(f.problems) {
		sum.Problems = append(sum.Problems, SourceResult{Name: k, Error: f.problems[k]})
	}

	jobs := mergeDuplicates(raw)
	allUIDs := make([]string, 0, len(raw))
	for _, j := range raw {
		allUIDs = append(allUIDs, j.UID)
	}

	seen := map[string]bool{}
	if !opt.All {
		if seen, err = r.Store.SeenListings(ctx); err != nil {
			return err
		}
	}
	// A source may set its own max_age_days: employers keep roles open for weeks.
	cutoffFor := func(source string) time.Time {
		if s, ok := cfg.Sources.Values[source]; ok {
			if d := s.Num("max_age_days", 0); d > 0 {
				return now.AddDate(0, 0, -int(d))
			}
		}
		return f.cutoff
	}

	tracked, err := r.Store.TrackedApps(ctx)
	if err != nil {
		return err
	}
	var observations []store.SkillObservation
	var above, matches []store.Lead
	for i := range jobs {
		j := &jobs[i]
		if seen[j.UID] || anySeen(seen, j.AltUIDs) || j.Date.Before(cutoffFor(j.Source)) {
			continue
		}
		sum.Scanned++
		body, antispam := splitAntispam(toPlain(j.Description))
		if o := skills.Observe(j, body, scorer.Excluded(j.Title)); o != nil {
			observations = append(observations, *o)
		}
		l := scorer.Score(j, body)
		if l == nil || l.Score < cfg.MinScore {
			continue
		}
		l.Antispam = antispam
		l.FirstSeen = sum.Date
		above = append(above, *l)
		if tracked(l.Company, l.Title, l.URL, l.OriginalURL) == nil {
			matches = append(matches, *l)
		}
	}
	sort.SliceStable(matches, func(a, b int) bool { return matches[a].Score > matches[b].Score })
	sum.Matches, sum.Hidden, sum.Leads = len(matches), len(above)-len(matches), matches

	if opt.DryRun {
		return nil
	}
	if err := r.Store.SaveLeads(ctx, matches, sum.Date); err != nil {
		return err
	}
	if _, err := r.Store.PruneLeads(ctx, now.AddDate(0, 0, -store.LeadKeepDays).Format("2006-01-02")); err != nil {
		return err
	}
	if err := r.Store.MarkSeen(ctx, allUIDs, sum.Date); err != nil {
		return err
	}
	if err := r.Store.RecordSkills(ctx, observations, sum.Date); err != nil {
		return err
	}
	if opt.Fixtures == "" {
		return r.Store.SetJSONSetting(ctx, settingFetch, lastFetch)
	}
	return nil
}

func anySeen(seen map[string]bool, uids []string) bool {
	for _, u := range uids {
		if seen[u] {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// mergeDuplicates keeps the first listing of each company+title and notes the
// boards the others came from. The kept listing's original posting is filled
// from a copy: that copy's own, or its URL if it came from the employer's board.
func mergeDuplicates(jobs []Job) []Job {
	byKey := map[string]int{}
	var out []Job
	for _, j := range jobs {
		j.Title, j.Company = fixMojibake(j.Title), fixMojibake(j.Company)
		k := dedupeKey(j.Company, j.Title)
		i, ok := byKey[k]
		if !ok || j.Title == "" {
			byKey[k] = len(out)
			j.AlsoOn, j.AltUIDs = []string{}, []string{}
			out = append(out, j)
			continue
		}
		first := &out[i]
		if first.OriginalURL == "" && !adapters[first.Source].ats {
			if j.OriginalURL != "" {
				first.OriginalURL = j.OriginalURL
			} else if adapters[j.Source].ats {
				first.OriginalURL = j.URL
			}
		}
		if j.SourceName != first.SourceName && !contains(first.AlsoOn, j.SourceName) {
			first.AlsoOn = append(first.AlsoOn, j.SourceName)
			first.AltUIDs = append(first.AltUIDs, j.UID)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Status is what the Radar header shows.
type Status struct {
	Running bool     `json:"running"`
	AutoRun bool     `json:"autoRun"`
	Last    *Summary `json:"last"`
	// ConfigError is set when the saved settings no longer parse.
	ConfigError string `json:"configError,omitempty"`
}

// Status reports whether a run is in progress and how the last one went.
func (r *Radar) Status(ctx context.Context) (Status, error) {
	st := Status{Running: r.Running()}
	last, err := r.LastRun(ctx)
	if err != nil {
		return st, err
	}
	st.Last = last
	cfg, _, _, err := r.Config(ctx)
	if err != nil {
		st.ConfigError = err.Error()
	} else {
		st.AutoRun = cfg.AutoRunOn()
	}
	return st, nil
}

// Loop runs the scan once a day while serve is up: soon after start if today's
// run hasn't happened, then on the hour. A day only counts once a board has
// answered, so a run without network retries an hour later.
func (r *Radar) Loop(ctx context.Context) {
	t := time.NewTimer(30 * time.Second) // let the server settle first
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		r.maybeRun(ctx)
		t.Reset(time.Hour)
	}
}

func (r *Radar) maybeRun(ctx context.Context) {
	cfg, _, _, err := r.Config(ctx)
	if err != nil {
		log.Printf("radar: %v", err)
		return
	}
	if !cfg.AutoRunOn() {
		return
	}
	last, err := r.LastRun(ctx)
	if err != nil {
		log.Printf("radar: %v", err)
		return
	}
	if last != nil && last.OK && last.Date == time.Now().Format("2006-01-02") {
		return
	}
	sum, err := r.Run(ctx, Options{})
	switch {
	case errors.Is(err, ErrRunning):
	case err != nil:
		log.Printf("radar: %v", err)
	default:
		log.Printf("radar: %d new lead(s) from %d new listings", sum.Matches, sum.Scanned)
	}
}
