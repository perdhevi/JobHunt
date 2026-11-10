package radar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"jobhunt/internal/store"
)

var companyURLs = []struct {
	ats string
	re  *regexp.Regexp
}{
	{"greenhouse", regexp.MustCompile(`(?i)boards-api\.greenhouse\.io/v1/boards/([\w.-]+)`)},
	{"greenhouse", regexp.MustCompile(`(?i)greenhouse\.io/embed/job_board\?for=([\w.-]+)`)},
	{"greenhouse", regexp.MustCompile(`(?i)(?:job-)?boards(?:\.eu)?\.greenhouse\.io/([\w.-]+)`)},
	{"lever", regexp.MustCompile(`(?i)jobs\.(eu\.)?lever\.co/([\w.-]+)`)},
	{"ashby", regexp.MustCompile(`(?i)jobs\.ashbyhq\.com/([\w.%-]+)`)},
}

var atsRef = regexp.MustCompile(`(?i)^(greenhouse|lever|ashby):(.+)$`)

// ParseCompanyRef turns a careers-board URL or "ats:token" into the ATS and
// board token. Lever's EU boards come back as "eu:<token>".
func ParseCompanyRef(ref string) (ats, token string, ok bool) {
	ref = strings.TrimSpace(ref)
	if m := atsRef.FindStringSubmatch(ref); m != nil {
		return strings.ToLower(m[1]), m[2], true
	}
	for _, p := range companyURLs {
		if m := p.re.FindStringSubmatch(ref); m != nil {
			if p.ats == "lever" {
				prefix := ""
				if m[1] != "" {
					prefix = "eu:"
				}
				return "lever", prefix + m[2], true
			}
			return p.ats, m[1], true
		}
	}
	return "", "", false
}

// CompanyResult reports a board that was checked and added.
type CompanyResult struct {
	ATS    string `json:"ats"`
	Token  string `json:"token"`
	Name   string `json:"name"`
	Jobs   int    `json:"jobs"`
	Remote int    `json:"remote"`
}

// AddCompany checks a company's board with one call and, if it exists, adds
// it to the radar config.
func (r *Radar) AddCompany(ctx context.Context, ref, name string) (CompanyResult, error) {
	ats, token, ok := ParseCompanyRef(ref)
	if !ok {
		return CompanyResult{}, fmt.Errorf("%w: not a Greenhouse, Lever or Ashby board link. Use the company's job-board URL, such as https://jobs.lever.co/acme, or ats:token", store.ErrInvalid)
	}
	cfg, _, text, err := r.Config(ctx)
	if err != nil {
		return CompanyResult{}, err
	}
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	f := &fetcher{ctx: ctx, client: client, userAgent: cfg.UserAgent, problems: map[string]string{}}
	var urlFor func(string) string
	var parse func([]byte, string) ([]parsed, error)
	scfg := cfg.Sources.Values[ats]
	switch ats {
	case "greenhouse":
		urlFor, parse = boardURL(scfg, "https://boards-api.greenhouse.io/v1/boards/{board}/jobs?content=true"), parseGreenhouse
	case "lever":
		urlFor, parse = leverURL, parseLever
	default:
		urlFor, parse = boardURL(scfg, "https://api.ashbyhq.com/posting-api/job-board/{board}?includeCompensation=true"), parseAshby
	}
	b, status, err := f.once(urlFor(token))
	if err == nil && status == http.StatusNotFound {
		err = fmt.Errorf("%w: %s has no board called %q. Nothing added", store.ErrInvalid, adapters[ats].name, token)
	} else if err == nil && status != http.StatusOK {
		err = fmt.Errorf("%s answered HTTP %d. Nothing added", adapters[ats].name, status)
	}
	var list []parsed
	if err == nil {
		list, err = parse(b, "")
	}
	if err != nil {
		return CompanyResult{}, err
	}
	res := CompanyResult{ATS: ats, Token: token, Name: strings.TrimSpace(name), Jobs: len(list)}
	for _, p := range list {
		if (p.remote != nil && *p.remote) || (p.remote == nil && remoteRE.MatchString(p.job.Location)) {
			res.Remote++
		}
		if res.Name == "" && p.job.Company != "" {
			res.Name = p.job.Company
		}
	}
	if res.Name == "" {
		parts := strings.Split(token, ":")
		res.Name = titleCase(strings.ReplaceAll(parts[len(parts)-1], "-", " "))
	}
	updated, err := setPath(text, []string{"sources", ats, "companies", token}, res.Name)
	if err != nil {
		return res, err
	}
	if _, _, err := ParseConfig(updated); err != nil {
		return res, err
	}
	return res, r.Store.SetSetting(ctx, SettingConfig, updated)
}

// node is a JSON value that keeps its object keys in order, so editing the
// config keeps the user's layout of sources and notes.
type node struct {
	keys []string
	vals map[string]*node
	raw  json.RawMessage // non-objects
}

func parseNode(b []byte) (*node, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	return decodeNode(dec)
}

func decodeNode(dec *json.Decoder) (*node, error) {
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || t[0] != '{' {
		return &node{raw: raw}, nil
	}
	inner := json.NewDecoder(bytes.NewReader(t))
	inner.Token() // {
	n := &node{vals: map[string]*node{}}
	for inner.More() {
		tok, err := inner.Token()
		if err != nil {
			return nil, err
		}
		k := tok.(string)
		v, err := decodeNode(inner)
		if err != nil {
			return nil, err
		}
		if _, dup := n.vals[k]; !dup {
			n.keys = append(n.keys, k)
		}
		n.vals[k] = v
	}
	return n, nil
}

func (n *node) write(b *bytes.Buffer, indent string) {
	if n.vals == nil {
		var out bytes.Buffer
		if json.Indent(&out, n.raw, indent, "  ") != nil {
			b.Write(n.raw)
			return
		}
		b.Write(out.Bytes())
		return
	}
	if len(n.keys) == 0 {
		b.WriteString("{}")
		return
	}
	b.WriteString("{\n")
	for i, k := range n.keys {
		kb, _ := json.Marshal(k)
		b.WriteString(indent + "  ")
		b.Write(kb)
		b.WriteString(": ")
		n.vals[k].write(b, indent+"  ")
		if i < len(n.keys)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString(indent + "}")
}

// setPath sets a string at a path of object keys, creating objects on the way.
func setPath(text string, path []string, value string) (string, error) {
	root, err := parseNode([]byte(text))
	if err != nil {
		return "", err
	}
	n := root
	for i, k := range path {
		if n.vals == nil {
			return "", fmt.Errorf("radar config: %s is not an object", strings.Join(path[:i], "."))
		}
		if i == len(path)-1 {
			vb, _ := json.Marshal(value)
			if _, ok := n.vals[k]; !ok {
				n.keys = append(n.keys, k)
			}
			n.vals[k] = &node{raw: vb}
			break
		}
		next, ok := n.vals[k]
		if !ok {
			next = &node{vals: map[string]*node{}}
			n.keys = append(n.keys, k)
			n.vals[k] = next
		}
		n = next
	}
	var b bytes.Buffer
	root.write(&b, "")
	b.WriteByte('\n')
	return b.String(), nil
}
