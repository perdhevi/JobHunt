package remote

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
)

var stubBoard = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	io.WriteString(w, "board "+r.Method+" "+r.URL.Path)
})

// browser keeps cookies and doesn't follow redirects, so each step is visible.
func browser(t *testing.T) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (e *env) do(c *http.Client, method, path, origin string, body url.Values) *http.Response {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body.Encode()))
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	res, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

func bodyOf(res *http.Response) string {
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return string(b)
}

func TestBoardLogin(t *testing.T) {
	e := newEnv(t, Options{Password: password, Board: stubBoard})
	b := browser(t)
	base := e.srv.URL

	res := e.do(b, "GET", "/applications?x=1", "", nil)
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/login?next=%2Fapplications%3Fx%3D1" {
		t.Fatalf("signed out page: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	if res := e.do(b, "GET", "/api/applications", "", nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("signed out API: %d", res.StatusCode)
	}
	if res := e.do(b, "GET", "/login", "", nil); res.StatusCode != http.StatusOK || !strings.Contains(bodyOf(res), "Sign in to Job Hunt") {
		t.Fatal("login page")
	}

	if res := e.do(b, "POST", "/login", base, url.Values{"password": {"wrong"}}); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", res.StatusCode)
	}
	if res := e.do(b, "POST", "/login", "https://evil.example", url.Values{"password": {password}}); res.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site login: %d", res.StatusCode)
	}
	res = e.do(b, "POST", "/login", base, url.Values{"password": {password}, "next": {"/applications?x=1"}})
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/applications?x=1" {
		t.Fatalf("login: %d %q", res.StatusCode, res.Header.Get("Location"))
	}

	if got := bodyOf(e.do(b, "GET", "/", "", nil)); got != "board GET /" {
		t.Fatalf("signed in page: %q", got)
	}
	if got := bodyOf(e.do(b, "GET", "/api/session", "", nil)); !strings.Contains(got, `"remote":true`) {
		t.Fatalf("session: %q", got)
	}
	if res := e.do(b, "PUT", "/api/applications/1", "https://evil.example", nil); res.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site write: %d", res.StatusCode)
	}
	res = e.do(b, "PUT", "/api/applications/1", base, nil)
	if got := bodyOf(res); got != "board PUT /api/applications/1" || res.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("same-site write: %q, CORS %q", got, res.Header.Get("Access-Control-Allow-Origin"))
	}

	// MCP stays bearer-only: a board cookie isn't a token.
	if res := e.do(b, "POST", "/mcp", base, nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cookie on MCP: %d", res.StatusCode)
	}

	e.do(b, "POST", "/logout", base, nil)
	if res := e.do(b, "GET", "/", "", nil); res.StatusCode != http.StatusFound {
		t.Fatalf("after logout: %d", res.StatusCode)
	}
}

// A browser told not to send referrers posts the form with "Origin: null".
func TestBoardLoginWithNullOrigin(t *testing.T) {
	e := newEnv(t, Options{Password: password, Board: stubBoard})
	post := func(site string) int {
		req, _ := http.NewRequest("POST", e.srv.URL+"/login", strings.NewReader(url.Values{"password": {password}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "null")
		req.Header.Set("Sec-Fetch-Site", site)
		res, err := browser(t).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode
	}
	if got := post("same-origin"); got != http.StatusSeeOther {
		t.Fatalf("same-origin: %d", got)
	}
	if got := post("cross-site"); got != http.StatusForbidden {
		t.Fatalf("cross-site: %d", got)
	}
}

func TestBoardSignedOutByPasswordChange(t *testing.T) {
	e := newEnv(t, Options{Password: password, Board: stubBoard})
	b := browser(t)
	e.do(b, "POST", "/login", e.srv.URL, url.Values{"password": {password}})
	changed := e.opts
	changed.Password = "a different password"
	e.setOptions(changed)
	if res := e.do(b, "GET", "/", "", nil); res.StatusCode != http.StatusFound {
		t.Fatalf("session survived a password change: %d", res.StatusCode)
	}
}

func TestBoardLoginStaysOnSite(t *testing.T) {
	e := newEnv(t, Options{Password: password, Board: stubBoard})
	for _, next := range []string{"//evil.example", `/\evil.example`, "https://evil.example", ""} {
		res := e.do(browser(t), "POST", "/login", e.srv.URL, url.Values{"password": {password}, "next": {next}})
		if res.Header.Get("Location") != "/" {
			t.Errorf("next=%q went to %q", next, res.Header.Get("Location"))
		}
	}
}

func TestBoardOffByDefault(t *testing.T) {
	e := newEnv(t, Options{Password: password})
	if res := e.do(browser(t), "GET", "/login", "", nil); res.StatusCode != http.StatusNotFound {
		t.Fatalf("board served without Board: %d", res.StatusCode)
	}
	if (Options{Token: "0123456789abcdef0123456789", Board: stubBoard}).Validate() == nil {
		t.Fatal("board allowed without a password")
	}
}
