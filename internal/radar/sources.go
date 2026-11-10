package radar

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"net/mail"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// adapter reads one board. Each uses only the board's official public feed
// and follows its published limits.
type adapter struct {
	name, home string
	// ats marks employers' own boards, whose listing URL is the employer's page.
	ats bool
	// minInterval is the least time between two fetches, from the board's terms.
	minInterval time.Duration
	fetch       func(f *fetcher, cfg SourceConfig) ([]Job, error)
}

var adapters = map[string]adapter{
	"remoteok":   {name: "Remote OK", home: "https://remoteok.com", minInterval: time.Hour, fetch: fetchRemoteOK},
	"himalayas":  {name: "Himalayas", home: "https://himalayas.app", minInterval: time.Hour, fetch: fetchHimalayas},
	"jobicy":     {name: "Jobicy", home: "https://jobicy.com", minInterval: time.Hour, fetch: fetchJobicy},
	"remotive":   {name: "Remotive", home: "https://remotive.com", minInterval: 6 * time.Hour, fetch: fetchRemotive}, // at most 4 calls a day
	"wwr":        {name: "We Work Remotely", home: "https://weworkremotely.com", minInterval: time.Hour, fetch: fetchWWR},
	"greenhouse": {name: "Greenhouse", home: "https://www.greenhouse.com", ats: true, minInterval: time.Hour, fetch: fetchGreenhouse},
	"lever":      {name: "Lever", home: "https://www.lever.co", ats: true, minInterval: time.Hour, fetch: fetchLever},
	"ashby":      {name: "Ashby", home: "https://www.ashbyhq.com", ats: true, minInterval: time.Hour, fetch: fetchAshby},
}

func knownSources() string {
	keys := make([]string, 0, len(adapters))
	for k := range adapters {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// configured is false for an ATS source with no companies yet, which is then
// left out of the run and the credits.
func configured(key string, cfg SourceConfig) bool {
	return !adapters[key].ats || len(cfg.Companies()) > 0
}

// ---------- Remote OK: public JSON feed ----------

func fetchRemoteOK(f *fetcher, cfg SourceConfig) ([]Job, error) {
	var data []any
	if err := f.getJSON(cfg.Str("url", "https://remoteok.com/api"), "remoteok", &data); err != nil {
		return nil, err
	}
	var jobs []Job
	for _, item := range data {
		j, ok := item.(map[string]any)
		if !ok || j["id"] == nil { // the first item is the legal notice
			continue
		}
		link := strings.ReplaceAll(str(j, "url"), "remoteOK.com", "remoteok.com")
		job := newJob("remoteok", "Remote OK", str(j, "id"), str(j, "position"), str(j, "company"), link, parseTime(j["date"]))
		job.Location = str(j, "location")
		job.Salary = money(j["salary_min"], j["salary_max"], "", "")
		job.Tags = cleanTags(strs(j, "tags"))
		job.Description = str(j, "description")
		job.setOriginal(externalURL(str(j, "apply_url"), "remoteok.com", "remoteok.io"))
		jobs = append(jobs, job)
	}
	return jobs, nil
}

// ---------- Himalayas: browse API, cursor pages of 20, rate-limited ----------

func tzCheck(offsets []any, home, tolerance float64) (*bool, string) {
	if len(offsets) == 0 {
		return nil, ""
	}
	vals := make([]float64, 0, len(offsets))
	for _, o := range offsets {
		switch x := o.(type) {
		case float64:
			vals = append(vals, x)
		case string:
			var v float64
			if _, err := fmt.Sscan(x, &v); err != nil {
				return nil, ""
			}
			vals = append(vals, v)
		default:
			return nil, ""
		}
	}
	ok := false
	lo, hi := vals[0], vals[0]
	for _, v := range vals {
		ok = ok || abs(v-home) <= tolerance
		lo, hi = min(lo, v), max(hi, v)
	}
	note := "UTC" + signed(lo)
	if lo != hi {
		note += " to UTC" + signed(hi)
	}
	return &ok, note
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func signed(f float64) string {
	s := fmt.Sprintf("%+g", f)
	return s
}

func fetchHimalayas(f *fetcher, cfg SourceConfig) ([]Job, error) {
	base := cfg.Str("url", "https://himalayas.app/jobs/api")
	maxPages := int(cfg.Num("max_pages", 15))
	home, tol := cfg.Num("home_utc_offset", 7), cfg.Num("timezone_tolerance_hours", 2)
	var jobs []Job
	cursor := ""
	for page := 0; page < maxPages; page++ {
		link := base
		if cursor != "" {
			link += "?" + url.Values{"cursor": {cursor}}.Encode()
		}
		var data map[string]any
		if err := f.getJSON(link, "himalayas", &data); err != nil {
			if page > 0 { // keep what the earlier pages gave
				f.problems["Himalayas (page "+fmt.Sprint(page+1)+")"] = err.Error()
				break
			}
			return nil, err
		}
		batch, _ := data["jobs"].([]any)
		var oldest float64
		for _, item := range batch {
			j, ok := item.(map[string]any)
			if !ok {
				continue
			}
			var date time.Time
			if ts, ok := j["pubDate"].(float64); ok {
				date = time.Unix(int64(ts), 0)
				if oldest == 0 || ts < oldest {
					oldest = ts
				}
			}
			link := firstNonBlank(str(j, "guid"), str(j, "applicationLink"))
			job := newJob("himalayas", "Himalayas", lastSegment(link), str(j, "title"), str(j, "companyName"), link, date)
			if r := strs(j, "locationRestrictions"); len(r) > 0 {
				job.Location = strings.Join(r, ", ")
			} else {
				job.Location = "Worldwide"
			}
			job.Salary = money(j["minSalary"], j["maxSalary"], str(j, "currency"), str(j, "salaryPeriod"))
			job.Tags = cleanTags(append(append(strs(j, "categories"), strs(j, "parentCategories")...), strs(j, "seniority")...))
			job.Description = firstNonBlank(str(j, "description"), str(j, "excerpt"))
			tz, _ := j["timezoneRestrictions"].([]any)
			job.TZOK, job.TZNote = tzCheck(tz, home, tol)
			job.setOriginal(externalURL(str(j, "applicationLink"), "himalayas.app"))
			jobs = append(jobs, job)
		}
		cursor = str(data, "nextCursor")
		if len(batch) == 0 || cursor == "" || (oldest != 0 && time.Unix(int64(oldest), 0).Before(f.cutoff)) {
			break
		}
		if err := f.pause(2 * time.Second); err != nil { // stay well under their rate limit
			return jobs, err
		}
	}
	return jobs, nil
}

func firstNonBlank(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// ---------- Jobicy: public API, no polling more than hourly ----------

func fetchJobicy(f *fetcher, cfg SourceConfig) ([]Job, error) {
	base := cfg.Str("url", "https://jobicy.com/api/v2/remote-jobs")
	queries, _ := cfg["queries"].([]any)
	if len(queries) == 0 {
		queries = []any{map[string]any{"count": float64(200)}}
	}
	var jobs []Job
	for i, q := range queries {
		if i > 0 {
			if err := f.pause(3 * time.Second); err != nil {
				return jobs, err
			}
		}
		params := url.Values{}
		if m, ok := q.(map[string]any); ok {
			for k := range m {
				params.Set(k, str(m, k))
			}
		}
		var data map[string]any
		if err := f.getJSON(base+"?"+params.Encode(), "jobicy", &data); err != nil {
			return nil, err
		}
		list, _ := data["jobs"].([]any)
		for _, item := range list {
			j, ok := item.(map[string]any)
			if !ok {
				continue
			}
			job := newJob("jobicy", "Jobicy", str(j, "id"), str(j, "jobTitle"), str(j, "companyName"), str(j, "url"), parseTime(j["pubDate"]))
			job.Location = str(j, "jobGeo")
			job.Salary = money(j["salaryMin"], j["salaryMax"], str(j, "salaryCurrency"), str(j, "salaryPeriod"))
			job.Tags = cleanTags(append(append(strs(j, "jobIndustry"), strs(j, "jobType")...), str(j, "jobLevel")))
			job.Description = firstNonBlank(str(j, "jobDescription"), str(j, "jobExcerpt"))
			jobs = append(jobs, job)
		}
	}
	return jobs, nil
}

// ---------- Remotive: public API, at most 4 calls a day ----------

func fetchRemotive(f *fetcher, cfg SourceConfig) ([]Job, error) {
	params := url.Values{"limit": {fmt.Sprint(int(cfg.Num("limit", 300)))}}
	if c := cfg.Str("category", ""); c != "" {
		params.Set("category", c)
	}
	var data map[string]any
	if err := f.getJSON(cfg.Str("url", "https://remotive.com/api/remote-jobs")+"?"+params.Encode(), "remotive", &data); err != nil {
		return nil, err
	}
	list, _ := data["jobs"].([]any)
	var jobs []Job
	for _, item := range list {
		j, ok := item.(map[string]any)
		if !ok {
			continue
		}
		job := newJob("remotive", "Remotive", str(j, "id"), str(j, "title"), str(j, "company_name"), str(j, "url"), parseTime(j["publication_date"]))
		job.Location = str(j, "candidate_required_location")
		job.Salary = strings.TrimSpace(str(j, "salary"))
		job.Tags = cleanTags(append([]string{str(j, "category"), strings.ReplaceAll(str(j, "job_type"), "_", " ")}, strs(j, "tags")...))
		job.Description = str(j, "description")
		jobs = append(jobs, job)
	}
	return jobs, nil
}

// ---------- We Work Remotely: category RSS feeds ----------

var wwrFeeds = []string{
	"https://weworkremotely.com/categories/remote-programming-jobs.rss",
	"https://weworkremotely.com/categories/remote-back-end-programming-jobs.rss",
	"https://weworkremotely.com/categories/remote-full-stack-programming-jobs.rss",
	"https://weworkremotely.com/categories/remote-devops-sysadmin-jobs.rss",
	"https://weworkremotely.com/categories/remote-customer-support-jobs.rss",
}

type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	GUID        string `xml:"guid"`
	PubDate     string `xml:"pubDate"`
	Region      string `xml:"region"`
	Country     string `xml:"country"`
	Skills      string `xml:"skills"`
	Category    string `xml:"category"`
	Type        string `xml:"type"`
	Description string `xml:"description"`
}

func fetchWWR(f *fetcher, cfg SourceConfig) ([]Job, error) {
	feeds := wwrFeeds
	if list, ok := cfg["feeds"].([]any); ok && len(list) > 0 {
		feeds = nil
		for _, x := range list {
			if s, ok := x.(string); ok {
				feeds = append(feeds, s)
			}
		}
	}
	var jobs []Job
	for i, feed := range feeds {
		if i > 0 {
			if err := f.pause(2 * time.Second); err != nil {
				return jobs, err
			}
		}
		b, err := f.get(feed, "wwr")
		if err != nil {
			return nil, err
		}
		var doc struct {
			Items []rssItem `xml:"channel>item"`
		}
		if err := xml.Unmarshal(b, &doc); err != nil {
			return nil, fmt.Errorf("unexpected response: %w", err)
		}
		for _, it := range doc.Items {
			raw := strings.TrimSpace(it.Title)
			company, title, found := strings.Cut(raw, ": ")
			if !found || title == "" {
				company, title = "", raw
			}
			date, _ := mail.ParseDate(strings.TrimSpace(it.PubDate))
			region := strings.TrimSpace(it.Region)
			location := region
			if strings.Contains(strings.ToLower(it.Country), "indonesia") && !strings.Contains(strings.ToLower(region), "anywhere") {
				location = strings.TrimSpace(region + " (incl. Indonesia)")
			}
			link := firstNonBlank(strings.TrimSpace(it.Link), strings.TrimSpace(it.GUID))
			job := newJob("wwr", "We Work Remotely", lastSegment(link), title, company, link, date)
			job.Location = location
			var tags []string
			for _, s := range strings.Split(strings.ReplaceAll(it.Skills, " and ", ", "), ",") {
				tags = append(tags, s)
			}
			job.Tags = cleanTags(append(tags, it.Category, it.Type))
			job.Description = strings.TrimSpace(it.Description)
			jobs = append(jobs, job)
		}
	}
	return jobs, nil
}

// ---------- Greenhouse / Lever / Ashby: target companies' own boards ----------

// remoteRE decides remoteness when an ATS has no flag for it.
var remoteRE = regexp.MustCompile(`(?i)\b(remote|anywhere|worldwide|distributed|work from home|wfh)\b`)

// parsed is a job and whether its ATS says it's remote (nil: it doesn't say).
type parsed struct {
	job    Job
	remote *bool
}

func boolp(b bool) *bool { return &b }

// boards lists (token, company) pairs to read. Offline, every file in the
// source's fixture folder is a board.
func boards(f *fetcher, cfg SourceConfig, source string) [][2]string {
	list := cfg.Companies()
	if f.fixtures == "" {
		return list
	}
	names := map[string]string{}
	for _, c := range list {
		names[c[0]] = c[1]
	}
	files, _ := filepath.Glob(filepath.Join(f.fixtures, source, "*.json"))
	sort.Strings(files)
	var out [][2]string
	for _, p := range files {
		tok := strings.TrimSuffix(filepath.Base(p), ".json")
		out = append(out, [2]string{tok, names[tok]})
	}
	return out
}

// fetchBoards reads each company's board, one call each, paused apart. One
// board failing is noted and the rest still run.
func fetchBoards(f *fetcher, cfg SourceConfig, source, name string, urlFor func(string) string,
	parse func(data []byte, company string) ([]parsed, error)) ([]Job, error) {
	remoteOnly := cfg.Bool("remote_only", true)
	pause := time.Duration(cfg.Num("pause_seconds", 1) * float64(time.Second))
	var jobs []Job
	for i, b := range boards(f, cfg, source) {
		token, company := b[0], b[1]
		if i > 0 {
			if err := f.pause(pause); err != nil {
				return jobs, err
			}
		}
		raw, err := f.get(urlFor(token), source+"/"+token)
		var list []parsed
		if err == nil {
			list, err = parse(raw, company)
		}
		if err != nil {
			f.problems[name+": "+token] = err.Error()
			continue
		}
		for _, p := range list {
			if p.job.Company == "" {
				parts := strings.Split(token, ":")
				p.job.Company = titleCase(strings.ReplaceAll(parts[len(parts)-1], "-", " "))
			}
			remote := p.remote
			if remote == nil {
				remote = boolp(remoteRE.MatchString(p.job.Location))
			}
			if *remote || !remoteOnly {
				jobs = append(jobs, p.job)
			}
		}
	}
	return jobs, nil
}

func titleCase(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
	}
	return strings.Join(words, " ")
}

func boardURL(cfg SourceConfig, def string) func(string) string {
	tmpl := cfg.Str("api", def)
	return func(b string) string { return strings.ReplaceAll(tmpl, "{board}", url.PathEscape(b)) }
}

func parseGreenhouse(raw []byte, company string) ([]parsed, error) {
	var data map[string]any
	if err := jsonUnmarshal(raw, &data); err != nil {
		return nil, err
	}
	list, _ := data["jobs"].([]any)
	var out []parsed
	for _, item := range list {
		j, ok := item.(map[string]any)
		if !ok {
			continue
		}
		date := parseTime(j["first_published"])
		if date.IsZero() {
			date = parseTime(j["updated_at"])
		}
		job := newJob("greenhouse", "Greenhouse", str(j, "id"), str(j, "title"), firstNonBlank(company, str(j, "company_name")), str(j, "absolute_url"), date)
		job.Location = str(obj(j, "location"), "name")
		if deps, ok := j["departments"].([]any); ok {
			for _, d := range deps {
				if m, ok := d.(map[string]any); ok {
					job.Tags = append(job.Tags, str(m, "name"))
				}
			}
		}
		job.Tags = cleanTags(job.Tags)
		job.Description = html.UnescapeString(str(j, "content")) // the API sends escaped HTML
		out = append(out, parsed{job, nil})
	}
	return out, nil
}

func fetchGreenhouse(f *fetcher, cfg SourceConfig) ([]Job, error) {
	return fetchBoards(f, cfg, "greenhouse", "Greenhouse",
		boardURL(cfg, "https://boards-api.greenhouse.io/v1/boards/{board}/jobs?content=true"), parseGreenhouse)
}

var leverPeriod = map[string]string{"per-year-salary": "annual", "per-month-salary": "monthly", "per-hour-wage": "hourly"}

func leverURL(board string) string {
	if t, ok := strings.CutPrefix(board, "eu:"); ok {
		return "https://api.eu.lever.co/v0/postings/" + url.PathEscape(t) + "?mode=json"
	}
	return "https://api.lever.co/v0/postings/" + url.PathEscape(board) + "?mode=json"
}

func parseLever(raw []byte, company string) ([]parsed, error) {
	var list []map[string]any
	if err := jsonUnmarshal(raw, &list); err != nil {
		var e map[string]any // {"ok": false, "error": ...}
		if jsonUnmarshal(raw, &e) == nil && str(e, "error") != "" {
			return nil, fmt.Errorf("%s", str(e, "error"))
		}
		return nil, err
	}
	var out []parsed
	for _, j := range list {
		cats := obj(j, "categories")
		locations := strs(cats, "allLocations")
		if len(locations) == 0 {
			locations = strs(cats, "location")
		}
		job := newJob("lever", "Lever", str(j, "id"), str(j, "text"), company, str(j, "hostedUrl"), parseTime(j["createdAt"]))
		job.Location = strings.Join(locations, "; ")
		if sal := obj(j, "salaryRange"); len(sal) > 0 {
			job.Salary = money(sal["min"], sal["max"], str(sal, "currency"), leverPeriod[str(sal, "interval")])
		}
		job.Tags = cleanTags([]string{str(cats, "team"), str(cats, "department"), str(cats, "commitment")})
		var b strings.Builder
		b.WriteString(str(j, "description"))
		if lists, ok := j["lists"].([]any); ok {
			for _, l := range lists {
				if m, ok := l.(map[string]any); ok {
					fmt.Fprintf(&b, "<h3>%s</h3><ul>%s</ul>", str(m, "text"), str(m, "content"))
				}
			}
		}
		b.WriteString(str(j, "additional"))
		job.Description = b.String()
		var remote *bool
		switch strings.ToLower(str(j, "workplaceType")) {
		case "remote":
			remote = boolp(true)
		case "on-site", "onsite", "hybrid":
			remote = boolp(false)
		}
		out = append(out, parsed{job, remote})
	}
	return out, nil
}

func fetchLever(f *fetcher, cfg SourceConfig) ([]Job, error) {
	return fetchBoards(f, cfg, "lever", "Lever", leverURL, parseLever)
}

func parseAshby(raw []byte, company string) ([]parsed, error) {
	var data map[string]any
	if err := jsonUnmarshal(raw, &data); err != nil {
		return nil, err
	}
	list, _ := data["jobs"].([]any)
	var out []parsed
	for _, item := range list {
		j, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if listed, ok := j["isListed"].(bool); ok && !listed {
			continue
		}
		locations := []string{str(j, "location")}
		if sec, ok := j["secondaryLocations"].([]any); ok {
			for _, s := range sec {
				if m, ok := s.(map[string]any); ok {
					locations = append(locations, str(m, "location"))
				}
			}
		}
		var remote *bool
		switch strings.ToLower(str(j, "workplaceType")) {
		case "remote":
			remote = boolp(true)
		case "onsite", "hybrid":
			remote = boolp(false)
		default:
			if r, ok := j["isRemote"].(bool); ok {
				remote = boolp(r)
			}
		}
		job := newJob("ashby", "Ashby", str(j, "id"), str(j, "title"), company, str(j, "jobUrl"), parseTime(j["publishedAt"]))
		job.Location = strings.Join(cleanTags(locations), "; ")
		job.Salary = str(obj(j, "compensation"), "scrapeableCompensationSalarySummary")
		job.Tags = cleanTags([]string{str(j, "department"), str(j, "team"), str(j, "employmentType")})
		job.Description = firstNonBlank(str(j, "descriptionHtml"), str(j, "descriptionPlain"))
		out = append(out, parsed{job, remote})
	}
	return out, nil
}

func fetchAshby(f *fetcher, cfg SourceConfig) ([]Job, error) {
	return fetchBoards(f, cfg, "ashby", "Ashby",
		boardURL(cfg, "https://api.ashbyhq.com/posting-api/job-board/{board}?includeCompensation=true"), parseAshby)
}

var jsonUnmarshal = json.Unmarshal
