package remote

// Regression tests for the November 2026 security review.

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func (e *env) refresh(id, rt string) *http.Response {
	return e.postForm("/token", url.Values{"grant_type": {"refresh_token"}, "client_id": {id}, "refresh_token": {rt}})
}

// connect registers a Claude client and signs it in, returning its client id and tokens.
func (e *env) connect() (string, map[string]any) {
	e.t.Helper()
	id := e.register(`{"client_name":"Claude","redirect_uris":["` + redirect + `"]}`)["client_id"].(string)
	code := codeFrom(e.t, e.login(id, password))
	tok := decode[map[string]any](e.t, e.postForm("/token", url.Values{
		"grant_type": {"authorization_code"}, "client_id": {id}, "code": {code}, "code_verifier": {verifier},
	}))
	return id, tok
}

// Browsers delete tabs and newlines from a Location header, so these would
// otherwise land on //evil.example.
func TestBoardLoginRefusesControlCharacters(t *testing.T) {
	e := newEnv(t, Options{Password: password, Board: stubBoard})
	b := browser(t)
	for _, next := range []string{"/\t/evil.example", "/\n/evil.example", "/\r/evil.example", "/\x00/evil.example"} {
		res := e.do(b, "POST", "/login", e.srv.URL, url.Values{"password": {password}, "next": {next}})
		if res.Header.Get("Location") != "/" {
			t.Errorf("next=%q went to %q", next, res.Header.Get("Location"))
		}
	}
	// Already signed in, /login redirects straight away.
	res := e.do(b, "GET", "/login?next="+url.QueryEscape("/\t/evil.example"), "", nil)
	if res.Header.Get("Location") != "/" {
		t.Errorf("signed-in redirect went to %q", res.Header.Get("Location"))
	}
	if got := safeNext("/applications?x=1"); got != "/applications?x=1" {
		t.Errorf("a local path was rewritten to %q", got)
	}
}

func TestRedirectHostsAllowlist(t *testing.T) {
	e := newEnv(t, Options{Password: password})
	for _, bad := range []string{"https://attacker.example/cb", "https://claude.ai.attacker.example/cb"} {
		res, _ := http.Post(e.srv.URL+"/register", "application/json", strings.NewReader(`{"redirect_uris":["`+bad+`"]}`))
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("registered %s: %d", bad, res.StatusCode)
		}
	}
	for _, good := range []string{redirect, "https://chatgpt.com/connector_platform_oauth_redirect", "http://localhost:33418/callback", "http://127.0.0.1:6274/oauth/callback"} {
		e.register(`{"redirect_uris":["` + good + `"]}`)
	}

	// A client id signed before the allowlist narrowed is refused without a redirect.
	id := e.register(`{"client_name":"Claude","redirect_uris":["` + redirect + `"]}`)["client_id"].(string)
	narrowed := e.opts
	narrowed.RedirectHosts = []string{"chatgpt.com"}
	e.setOptions(narrowed)
	q := url.Values{"client_id": {id}, "response_type": {"token"}}
	res, _ := noRedirect.Get(e.srv.URL + "/authorize?" + q.Encode())
	if res.StatusCode != http.StatusBadRequest || res.Header.Get("Location") != "" {
		t.Fatalf("old client: %d to %q", res.StatusCode, res.Header.Get("Location"))
	}

	if (Options{PublicURL: "https://x.example", Password: password, Secret: bytes.Repeat([]byte("k"), 32), RedirectHosts: []string{"https://claude.ai"}}).Validate() == nil {
		t.Error("accepted a URL as a redirect host")
	}
}

func TestRefreshTokenRotation(t *testing.T) {
	st := newMemState()
	e := newEnv(t, Options{Password: password, State: st})
	id, tok := e.connect()
	first := tok["refresh_token"].(string)

	next := decode[map[string]any](t, e.refresh(id, first))
	second := next["refresh_token"].(string)
	if second == first {
		t.Fatal("refresh token wasn't rotated")
	}
	// A retry within the leeway, as after a lost response, still works.
	if res := e.refresh(id, first); res.StatusCode != http.StatusOK {
		t.Fatalf("retry within leeway: %d", res.StatusCode)
	}
	if err := e.callMCP(next["access_token"].(string)); err != nil {
		t.Fatalf("rotated access token: %v", err)
	}

	// Past the leeway, a reused refresh token signs that app out entirely.
	st.age("refresh:", time.Minute)
	if res := e.refresh(id, first); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("replayed refresh token: %d", res.StatusCode)
	}
	if res := e.refresh(id, second); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("family survived a replay: %d", res.StatusCode)
	}
	if err := e.callMCP(next["access_token"].(string)); err == nil {
		t.Fatal("access token survived a replay")
	}

	// Other apps are unaffected.
	_, other := e.connect()
	if err := e.callMCP(other["access_token"].(string)); err != nil {
		t.Fatalf("another app was signed out: %v", err)
	}
}

func TestSignOutEverywhere(t *testing.T) {
	e := newEnv(t, Options{Password: password, Board: stubBoard})
	_, tok := e.connect()
	b, other := browser(t), browser(t)
	e.do(b, "POST", "/login", e.srv.URL, url.Values{"password": {password}})
	e.do(other, "POST", "/login", e.srv.URL, url.Values{"password": {password}})

	// Without a session, nobody can sign the owner out of everything.
	e.do(browser(t), "POST", "/logout", e.srv.URL, url.Values{"everywhere": {"1"}})
	if res := e.do(other, "GET", "/", "", nil); res.StatusCode != http.StatusOK {
		t.Fatalf("signed-out visitor revoked sessions: %d", res.StatusCode)
	}

	e.do(b, "POST", "/logout", e.srv.URL, url.Values{"everywhere": {"1"}})
	if res := e.do(other, "GET", "/", "", nil); res.StatusCode != http.StatusFound {
		t.Fatalf("other browser still signed in: %d", res.StatusCode)
	}
	if err := e.callMCP(tok["access_token"].(string)); err == nil {
		t.Fatal("app still signed in")
	}
	if res := e.do(b, "POST", "/login", e.srv.URL, url.Values{"password": {password}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("can't sign in again: %d", res.StatusCode)
	}
}

func TestLogoutRevokesCopiedCookie(t *testing.T) {
	e := newEnv(t, Options{Password: password, Board: stubBoard})
	b := browser(t)
	e.do(b, "POST", "/login", e.srv.URL, url.Values{"password": {password}})
	u, _ := url.Parse(e.srv.URL)
	stolen := b.Jar.Cookies(u)

	e.do(b, "POST", "/logout", e.srv.URL, nil)
	thief := browser(t)
	thief.Jar.SetCookies(u, stolen)
	if res := e.do(thief, "GET", "/api/session", "", nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("copied cookie works after sign-out: %d", res.StatusCode)
	}
}

// Behind a proxy every request may come from one private address with a
// forged X-Forwarded-For. The global cap still stops guessing.
func TestGlobalLockout(t *testing.T) {
	e := newEnv(t, Options{Password: password, Board: stubBoard})
	for i := range maxGlobalFailures {
		e.wrongLogin(fmt.Sprintf("198.51.100.%d", i+1))
	}
	if got := e.wrongLogin("203.0.113.99"); got != http.StatusTooManyRequests {
		t.Fatalf("after %d failures from many addresses: %d", maxGlobalFailures, got)
	}
}

// wrongLogin posts a wrong board password as if Caddy forwarded it from xff.
func (e *env) wrongLogin(xff string) int {
	e.t.Helper()
	req, _ := http.NewRequest("POST", e.srv.URL+"/login", strings.NewReader(url.Values{"password": {"nope"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", e.srv.URL)
	req.Header.Set("X-Forwarded-For", xff)
	res, err := browser(e.t).Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

// One IPv6 machine owns a /64, so moving around inside it earns no more guesses.
func TestLockoutCountsIPv6Prefix(t *testing.T) {
	e := newEnv(t, Options{Password: password, Board: stubBoard})
	for i := range maxFailures {
		e.wrongLogin(fmt.Sprintf("2001:db8:1:2::%x", i+1))
	}
	if got := e.wrongLogin("2001:db8:1:2:ffff:ffff:ffff:1"); got != http.StatusTooManyRequests {
		t.Fatalf("new address in the same /64: %d", got)
	}
	if got := e.wrongLogin("2001:db8:1:3::1"); got != http.StatusUnauthorized {
		t.Fatalf("a different /64 is a different machine: %d", got)
	}
}

func TestClientIP(t *testing.T) {
	for _, c := range []struct{ remote, xff, want string }{
		{"10.0.0.2:5000", "203.0.113.7", "203.0.113.7"},          // Caddy on the compose network
		{"10.0.0.2:5000", "1.1.1.1, 203.0.113.7", "203.0.113.7"}, // Caddy appends the real peer
		{"10.0.0.2:5000", "evil\" injected", "10.0.0.2"},         // not an address
		{"203.0.113.9:5000", "198.51.100.1", "203.0.113.9"},      // not from a proxy
		{"127.0.0.1:5000", "2001:DB8::1", "2001:db8::1"},         // normalised for the log and the key
	} {
		r := httptest.NewRequest("POST", "/login", nil)
		r.RemoteAddr = c.remote
		r.Header.Set("X-Forwarded-For", c.xff)
		if got := clientIP(r); got != c.want {
			t.Errorf("remote %s xff %q: %q, want %q", c.remote, c.xff, got, c.want)
		}
	}
	if got := lockKey("203.0.113.7"); got != "203.0.113.7" {
		t.Errorf("IPv4 key %q", got)
	}
	if got := lockKey("2001:db8:1:2:3:4:5:6"); got != "2001:db8:1:2::/64" {
		t.Errorf("IPv6 key %q", got)
	}
}

// Addresses that fail once and never return must not pile up forever.
func TestFailuresMapIsPruned(t *testing.T) {
	s := &server{failures: map[string][]time.Time{}}
	for i := range 10 * maxGlobalFailures {
		s.failures[strings.Repeat("x", i+1)] = []time.Time{time.Now().Add(-2 * lockout)}
	}
	s.recordFailure("203.0.113.1")
	if n := len(s.failures); n != 1 {
		t.Fatalf("stale addresses kept: %d", n)
	}
}

// memState.age backdates marks with the prefix, standing in for time passing.
func (m *memState) age(prefix string, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, v := range m.marks {
		if strings.HasPrefix(k, prefix) {
			v[0] = v[0].Add(-d)
			m.marks[k] = v
		}
	}
}

func TestMemStateMark(t *testing.T) {
	m, ctx := newMemState(), context.Background()
	first, at, _ := m.Mark(ctx, "a", time.Now().Add(time.Hour))
	again, at2, _ := m.Mark(ctx, "a", time.Now().Add(time.Hour))
	if !first || again || !at.Equal(at2) {
		t.Fatalf("first=%v again=%v", first, again)
	}
	m.Mark(ctx, "gone", time.Now().Add(-time.Second))
	if ok, _ := m.Marked(ctx, "gone"); ok {
		t.Fatal("expired mark still counts")
	}
}
