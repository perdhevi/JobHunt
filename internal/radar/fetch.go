package radar

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Job is one listing in the shape every adapter returns.
type Job struct {
	UID, Source, SourceName string
	Title, Company          string
	URL                     string // the board's own page
	OriginalURL             string // the employer's posting, when the board gives it
	Date                    time.Time
	Location, Salary        string
	Tags                    []string
	Description             string
	TZOK                    *bool // nil when no time-zone restriction is stated
	TZNote                  string

	AlsoOn, AltUIDs []string // filled by mergeDuplicates
}

func newJob(source, sourceName, id, title, company, link string, date time.Time) Job {
	if date.IsZero() {
		date = time.Now()
	}
	return Job{UID: source + ":" + id, Source: source, SourceName: sourceName,
		Title: strings.TrimSpace(title), Company: strings.TrimSpace(company),
		URL: webURL(link), Date: date.UTC()}
}

func (j *Job) setOriginal(link string) {
	if link = webURL(link); link != "" && link != j.URL {
		j.OriginalURL = link
	}
}

func cleanTags(tags []string) []string {
	out := tags[:0:0]
	for _, t := range tags {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// fetcher is the HTTP access every adapter shares: one user agent, retries
// that back off, and an offline mode that reads fixture files instead.
type fetcher struct {
	ctx       context.Context
	client    *http.Client
	userAgent string
	fixtures  string // when set, read <fixtures>/<source>.json|xml instead of the network
	cutoff    time.Time
	problems  map[string]string // partial failures inside a source, e.g. one company's board
	wait      time.Duration     // between retries
}

func (f *fetcher) get(link, source string) ([]byte, error) {
	if f.fixtures != "" {
		for _, ext := range []string{".json", ".xml"} {
			p := filepath.Join(f.fixtures, filepath.FromSlash(source)+ext)
			if b, err := os.ReadFile(p); err == nil {
				return b, nil
			}
		}
		return nil, fmt.Errorf("no fixture for %s in %s", source, f.fixtures)
	}
	var last error
	for attempt := 1; attempt <= 3; attempt++ {
		b, status, err := f.once(link)
		if err == nil && status == http.StatusOK {
			return b, nil
		}
		if err == nil {
			last = fmt.Errorf("HTTP %d", status)
			// A 4xx other than rate limiting won't fix itself; don't hammer the board.
			if status != http.StatusTooManyRequests && status >= 400 && status < 500 {
				return nil, last
			}
		} else {
			last = err
		}
		if attempt < 3 {
			log.Printf("radar [%s] %v; retrying in %s", source, last, f.wait)
			if err := f.sleep(f.wait); err != nil {
				return nil, err
			}
		}
	}
	return nil, last
}

// maxBody bounds one response; the largest feeds are a few megabytes.
const maxBody = 32 << 20

func (f *fetcher) once(link string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(f.ctx, http.MethodGet, link, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", f.userAgent)
	req.Header.Set("Accept", "application/json, application/rss+xml, */*")
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	return b, resp.StatusCode, err
}

func (f *fetcher) getJSON(link, source string, v any) error {
	b, err := f.get(link, source)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("unexpected response: %w", err)
	}
	return nil
}

// pause spaces requests to one board, as its terms ask. No-op offline.
func (f *fetcher) pause(d time.Duration) error {
	if f.fixtures != "" {
		return nil
	}
	return f.sleep(d)
}

func (f *fetcher) sleep(d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-f.ctx.Done():
		return f.ctx.Err()
	}
}

// parseTime reads the date formats the feeds use: ISO 8601 with or without a
// zone (no zone means UTC), or epoch milliseconds.
func parseTime(v any) time.Time {
	switch x := v.(type) {
	case float64:
		return time.UnixMilli(int64(x)).UTC()
	case string:
		x = strings.TrimSpace(x)
		if x == "" {
			return time.Time{}
		}
		if n, err := strconv.ParseInt(x, 10, 64); err == nil {
			return time.UnixMilli(n).UTC()
		}
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02"} {
			if t, err := time.Parse(layout, x); err == nil {
				return t.UTC()
			}
		}
	}
	return time.Time{}
}
