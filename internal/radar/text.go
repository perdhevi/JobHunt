package radar

import (
	"fmt"
	"html"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// fixMojibake undoes UTF-8 that a feed encoded twice (â€™ for ’), the same test
// Python's text.encode("latin-1").decode("utf-8") makes.
func fixMojibake(s string) string {
	b := make([]byte, 0, len(s))
	for _, r := range s {
		if r > 255 {
			return s
		}
		b = append(b, byte(r))
	}
	if !utf8.Valid(b) {
		return s
	}
	return string(b)
}

var (
	breakTags = regexp.MustCompile(`(?i)<(br|/p|/li|/h\d)[^>]*>`)
	anyTag    = regexp.MustCompile(`<[^>]+>`)
	spaces    = regexp.MustCompile("[ \t ]+")
)

// toPlain turns a listing's HTML description into text for scoring.
func toPlain(desc string) string {
	t := breakTags.ReplaceAllString(desc, "\n")
	t = anyTag.ReplaceAllString(t, " ")
	t = fixMojibake(html.UnescapeString(t))
	return strings.TrimSpace(spaces.ReplaceAllString(t, " "))
}

var antispamRE = regexp.MustCompile(`(?i)mention the word \*\*(\w+)\*\*`)

// splitAntispam pulls out Remote OK's "mention the word" note, which would
// otherwise be scored as part of the job.
func splitAntispam(text string) (string, string) {
	word := ""
	if m := antispamRE.FindStringSubmatch(text); m != nil {
		word = m[1]
	}
	if i := strings.Index(text, "Please mention the word"); i >= 0 {
		text = text[:i]
	}
	return text, word
}

var (
	nonAlnum    = regexp.MustCompile(`[^a-z0-9]+`)
	legalSuffix = regexp.MustCompile(`\b(inc|llc|ltd|limited|gmbh|corp(oration)?|co)\b\.?`)
)

// dedupeKey is Job Radar's company+title identity, used to merge the same job
// across boards within a run.
func dedupeKey(company, title string) string {
	c := legalSuffix.ReplaceAllString(strings.ToLower(company), "")
	return nonAlnum.ReplaceAllString(c, "") + "|" + nonAlnum.ReplaceAllString(strings.ToLower(title), "")
}

// num reads a JSON number or numeric string.
func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f
	}
	return 0
}

func str(m map[string]any, k string) string {
	switch v := m[k].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

func strs(m map[string]any, k string) []string {
	var out []string
	switch v := m[k].(type) {
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok && s != "" {
				out = append(out, s)
			}
		}
	case string:
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func obj(m map[string]any, k string) map[string]any {
	o, _ := m[k].(map[string]any)
	if o == nil {
		o = map[string]any{}
	}
	return o
}

func commas(f float64) string {
	s := strconv.FormatFloat(math.Round(f), 'f', 0, 64)
	n := len(s)
	if n <= 3 {
		return s
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (n-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

var periods = map[string]string{"annual": "/yr", "yearly": "/yr", "year": "/yr", "monthly": "/mo", "month": "/mo", "hourly": "/hr", "hour": "/hr"}

// money formats a salary range, or "" when the board gives nothing useful.
func money(lo, hi any, currency, period string) string {
	l, h := num(lo), num(hi)
	if l < 1000 && h < 1000 {
		return ""
	}
	cur := strings.ToUpper(currency)
	if cur == "" {
		cur = "USD"
	}
	sym := cur + " "
	if cur == "USD" {
		sym = "$"
	}
	if period == "" {
		period = "annual"
	}
	per := periods[strings.ToLower(period)]
	if l > 0 && h > 0 && l != h {
		return fmt.Sprintf("%s%s–%s%s", sym, commas(l), commas(h), per)
	}
	if h == 0 {
		h = l
	}
	return sym + commas(h) + per
}

// externalURL returns link if it's http(s) and off the board's own site(s).
func externalURL(link string, boardHosts ...string) string {
	link = strings.TrimSpace(link)
	low := strings.ToLower(link)
	if !strings.HasPrefix(low, "http://") && !strings.HasPrefix(low, "https://") {
		return ""
	}
	u, err := url.Parse(link)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return ""
	}
	for _, b := range boardHosts {
		if host == b || strings.HasSuffix(host, "."+b) {
			return ""
		}
	}
	return link
}

// webURL keeps only http(s) links, since the UI renders them as hrefs and
// they come from third-party feeds.
func webURL(link string) string {
	link = strings.TrimSpace(link)
	u, err := url.Parse(link)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return ""
	}
	return link
}

func lastSegment(link string) string {
	t := strings.TrimRight(link, "/")
	if i := strings.LastIndex(t, "/"); i >= 0 && i < len(t)-1 {
		return t[i+1:]
	}
	return link
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}
