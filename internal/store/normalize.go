package store

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// trackingParams never identify a posting, only how you reached it.
var trackingParams = map[string]bool{
	"ref": true, "refid": true, "trackingid": true, "source": true, "src": true,
	"gh_src": true, "lever-source": true, "lever-origin": true, "ashby_jid": true,
	"gclid": true, "fbclid": true, "trk": true, "position": true, "pagenum": true,
	"referrer": true, "origin": true,
}

var (
	linkedinJob = regexp.MustCompile(`/jobs/view/(?:[^/]*-)?(\d{6,})`)
	upworkJob   = regexp.MustCompile(`~[0-9a-z]{10,}`)
	applySuffix = regexp.MustCompile(`/(apply|application)$`)
)

// URLKey reduces a job link to a stable identity, so the same posting matches
// however it was copied. It returns "" when the input has no usable host.
func URLKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	path := strings.TrimRight(strings.ToLower(u.EscapedPath()), "/")

	if strings.HasSuffix(host, "linkedin.com") {
		if id := u.Query().Get("currentJobId"); id != "" {
			return "linkedin:" + id
		}
		if m := linkedinJob.FindStringSubmatch(path); m != nil {
			return "linkedin:" + m[1]
		}
	}
	if strings.HasSuffix(host, "upwork.com") {
		if m := upworkJob.FindString(path); m != "" {
			return "upwork:" + m
		}
	}
	path = applySuffix.ReplaceAllString(path, "")

	var keep []string
	for k, vs := range u.Query() {
		lk := strings.ToLower(k)
		if trackingParams[lk] || strings.HasPrefix(lk, "utm_") {
			continue
		}
		for _, v := range vs {
			keep = append(keep, lk+"="+v)
		}
	}
	sort.Strings(keep)
	key := host + path
	if len(keep) > 0 {
		key += "?" + strings.Join(keep, "&")
	}
	return key
}

var (
	nonWord = regexp.MustCompile(`[^\p{L}\p{N}+#]+`)

	// Legal forms carry no identity: "Acme Inc." and "ACME" are one company.
	companySuffix = map[string]bool{
		"inc": true, "incorporated": true, "llc": true, "ltd": true, "limited": true,
		"corp": true, "corporation": true, "co": true, "company": true, "gmbh": true,
		"pt": true, "tbk": true, "plc": true, "bv": true, "pty": true, "sa": true,
	}
	roleAlias = map[string]string{"sr": "senior", "jr": "junior", "eng": "engineer", "engr": "engineer", "dev": "developer", "mgr": "manager"}

	// Location and work-mode words vary between listings of one role.
	roleNoise = map[string]bool{
		"remote": true, "hybrid": true, "onsite": true, "apac": true, "emea": true, "latam": true,
		"amer": true, "global": true, "worldwide": true, "asia": true, "europe": true, "us": true,
		"usa": true, "uk": true, "the": true, "a": true, "an": true, "of": true, "and": true, "for": true,
		"in": true, "at": true, "contract": true, "fulltime": true, "full": true, "time": true,
	}
)

func tokens(s string) []string {
	return strings.Fields(nonWord.ReplaceAllString(strings.ToLower(s), " "))
}

// CompanyKey normalises a company name. It also accepts a bare website, because
// records sometimes hold one ("https://factory.com" means Factory).
func CompanyKey(s string) string {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "://") || strings.HasPrefix(strings.ToLower(s), "www.") {
		raw := s
		if !strings.Contains(raw, "://") {
			raw = "https://" + raw
		}
		if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
			parts := strings.Split(strings.TrimPrefix(strings.ToLower(u.Hostname()), "www."), ".")
			if len(parts) > 1 {
				parts = parts[:len(parts)-1]
			}
			s = parts[len(parts)-1]
		}
	}
	var out []string
	for _, t := range tokens(s) {
		if !companySuffix[t] {
			out = append(out, t)
		}
	}
	return strings.Join(out, " ")
}

func roleTokens(s string) []string {
	ts := tokens(s)
	for i, t := range ts {
		if a, ok := roleAlias[t]; ok {
			ts[i] = a
		}
	}
	return ts
}

// RoleKey normalises a role title for exact comparison.
func RoleKey(s string) string { return strings.Join(roleTokens(s), " ") }

// IdentityKey is the exact company-and-role identity, or "" if either is blank.
func IdentityKey(company, role string) string {
	c, r := CompanyKey(company), RoleKey(role)
	if c == "" || r == "" {
		return ""
	}
	return c + "|" + r
}

// sameCompany also accepts one name being a leading part of the other, so
// "Factory" matches "Factory AI".
func sameCompany(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return a == b || strings.HasPrefix(a, b+" ") || strings.HasPrefix(b, a+" ")
}

// roleSimilarity is the Jaccard overlap of role words, ignoring location noise.
func roleSimilarity(a, b string) float64 {
	set := func(s string) map[string]bool {
		m := map[string]bool{}
		for _, t := range roleTokens(s) {
			if !roleNoise[t] {
				m[t] = true
			}
		}
		return m
	}
	x, y := set(a), set(b)
	if len(x) == 0 || len(y) == 0 {
		return 0
	}
	common := 0
	for t := range x {
		if y[t] {
			common++
		}
	}
	return float64(common) / float64(len(x)+len(y)-common)
}
