package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"jobhunt/internal/mcpserver"
	"jobhunt/internal/store"
)

const (
	password = "correct horse battery"
	redirect = "https://claude.ai/api/mcp/auth_callback"
	verifier = "a-long-random-verifier-string-that-is-over-43-chars"
)

type env struct {
	t    *testing.T
	srv  *httptest.Server
	mcp  http.Handler
	opts Options
	h    http.Handler
}

// setOptions rebuilds the handler, as a restart with new settings would.
func (e *env) setOptions(o Options) {
	e.opts = o
	e.h = Handler(e.mcp, o)
}

func newEnv(t *testing.T, o Options) *env {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	e := &env{t: t, mcp: mcpserver.HTTPHandler(mcpserver.New(s, "test"))}
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { e.h.ServeHTTP(w, r) }))
	t.Cleanup(e.srv.Close)
	if o.PublicURL == "" && o.Password != "" {
		o.PublicURL = e.srv.URL
	}
	if o.Secret == nil {
		o.Secret = bytes.Repeat([]byte("k"), 32)
	}
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	e.setOptions(o)
	return e
}

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func (e *env) postForm(path string, v url.Values) *http.Response {
	e.t.Helper()
	res, err := noRedirect.PostForm(e.srv.URL+path, v)
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

func decode[T any](t *testing.T, res *http.Response) T {
	t.Helper()
	defer res.Body.Close()
	var v T
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func (e *env) register(body string) map[string]any {
	e.t.Helper()
	res, err := http.Post(e.srv.URL+"/register", "application/json", strings.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	if res.StatusCode != http.StatusCreated {
		e.t.Fatalf("register: %d", res.StatusCode)
	}
	return decode[map[string]any](e.t, res)
}

func challenge(v string) string {
	sum := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (e *env) login(clientID, pw string) *http.Response {
	return e.postForm("/authorize", url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirect},
		"state": {"xyz"}, "code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"},
		"resource": {e.opts.PublicURL + "/mcp"}, "password": {pw},
	})
}

func codeFrom(t *testing.T, res *http.Response) string {
	t.Helper()
	if res.StatusCode != http.StatusFound {
		t.Fatalf("authorize: %d", res.StatusCode)
	}
	u, _ := url.Parse(res.Header.Get("Location"))
	if !strings.HasPrefix(u.String(), redirect) || u.Query().Get("state") != "xyz" || u.Query().Get("code") == "" {
		t.Fatalf("redirect %s", u)
	}
	return u.Query().Get("code")
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func (e *env) callMCP(token string) error {
	tr := &mcp.StreamableClientTransport{Endpoint: e.srv.URL + "/mcp", HTTPClient: &http.Client{Transport: bearer{token}}}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t"}, nil).Connect(context.Background(), tr, nil)
	if err != nil {
		return err
	}
	defer cs.Close()
	_, err = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "pipeline_summary", Arguments: map[string]any{}})
	return err
}

func TestOAuthFlow(t *testing.T) {
	e := newEnv(t, Options{Password: password})

	// An unauthenticated MCP call points the client at the metadata.
	res, _ := http.Post(e.srv.URL+"/mcp", "application/json", strings.NewReader("{}"))
	if res.StatusCode != http.StatusUnauthorized || !strings.Contains(res.Header.Get("WWW-Authenticate"), "/.well-known/oauth-protected-resource/mcp") {
		t.Fatalf("unauthenticated: %d %q", res.StatusCode, res.Header.Get("WWW-Authenticate"))
	}
	res, _ = http.Get(e.srv.URL + "/.well-known/oauth-protected-resource/mcp")
	if prm := decode[map[string]any](t, res); prm["resource"] != e.srv.URL+"/mcp" {
		t.Fatalf("prm %v", prm)
	}
	res, _ = http.Get(e.srv.URL + "/.well-known/oauth-authorization-server")
	if asm := decode[map[string]any](t, res); asm["registration_endpoint"] != e.srv.URL+"/register" {
		t.Fatalf("asm %v", asm)
	}

	client := e.register(`{"client_name":"Claude","redirect_uris":["` + redirect + `"]}`)
	id := client["client_id"].(string)

	res, _ = http.Get(e.srv.URL + "/authorize?" + url.Values{"response_type": {"code"}, "client_id": {id}, "redirect_uri": {redirect}, "code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"}}.Encode())
	if res.StatusCode != http.StatusOK || res.Header.Get("X-Frame-Options") != "DENY" {
		t.Fatalf("login page: %d", res.StatusCode)
	}
	// Browsers apply form-action to the redirect after the post, so the page
	// must allow the app's origin or the sign-in never gets back to it.
	const formAction = "form-action 'self' https://claude.ai;"
	if csp := res.Header.Get("Content-Security-Policy"); !strings.Contains(csp, formAction) {
		t.Fatalf("login page CSP %q lacks %q", csp, formAction)
	}

	if res := e.login(id, "wrong"); res.StatusCode != http.StatusUnauthorized || !strings.Contains(res.Header.Get("Content-Security-Policy"), formAction) {
		t.Fatalf("wrong password: %d %q", res.StatusCode, res.Header.Get("Content-Security-Policy"))
	}
	code := codeFrom(t, e.login(id, password))

	exchange := url.Values{"grant_type": {"authorization_code"}, "client_id": {id}, "code": {code}, "redirect_uri": {redirect}, "code_verifier": {"wrong-verifier"}}
	if res := e.postForm("/token", exchange); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad verifier: %d", res.StatusCode)
	}
	exchange.Set("code_verifier", verifier)
	tok := decode[map[string]any](t, e.postForm("/token", exchange))
	access, _ := tok["access_token"].(string)
	if access == "" {
		t.Fatalf("token %v", tok)
	}
	if err := e.callMCP(access); err != nil {
		t.Fatalf("mcp with access token: %v", err)
	}

	// A code used twice has leaked, so the tokens it gave stop working too.
	if res := e.postForm("/token", exchange); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("code reused: %d", res.StatusCode)
	}
	if err := e.callMCP(access); err == nil {
		t.Fatal("token from a reused code still works")
	}

	exchange.Set("code", codeFrom(t, e.login(id, password)))
	tok = decode[map[string]any](t, e.postForm("/token", exchange))
	access = tok["access_token"].(string)
	if err := e.callMCP(tok["refresh_token"].(string)); err == nil {
		t.Fatal("a refresh token must not work as an access token")
	}

	refreshed := decode[map[string]any](t, e.postForm("/token", url.Values{"grant_type": {"refresh_token"}, "client_id": {id}, "refresh_token": {tok["refresh_token"].(string)}}))
	if err := e.callMCP(refreshed["access_token"].(string)); err != nil {
		t.Fatalf("mcp after refresh: %v", err)
	}

	// Changing the password revokes every token but keeps the client registered.
	changed := e.opts
	changed.Password = "a different password"
	e.setOptions(changed)
	if err := e.callMCP(access); err == nil {
		t.Fatal("token survived a password change")
	}
	if res := e.login(id, "a different password"); res.StatusCode != http.StatusFound {
		t.Fatalf("client lost on password change: %d", res.StatusCode)
	}
}

func TestConfidentialClient(t *testing.T) {
	e := newEnv(t, Options{Password: password})
	client := e.register(`{"redirect_uris":["` + redirect + `"],"token_endpoint_auth_method":"client_secret_post"}`)
	id, secret := client["client_id"].(string), client["client_secret"].(string)
	code := codeFrom(t, e.login(id, password))
	v := url.Values{"grant_type": {"authorization_code"}, "client_id": {id}, "code": {code}, "code_verifier": {verifier}}
	if res := e.postForm("/token", v); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing secret: %d", res.StatusCode)
	}
	v.Set("client_secret", secret)
	if res := e.postForm("/token", v); res.StatusCode != http.StatusOK {
		t.Fatalf("with secret: %d", res.StatusCode)
	}
}

func TestAuthorizeRefusesUnknownRedirect(t *testing.T) {
	e := newEnv(t, Options{Password: password})
	id := e.register(`{"redirect_uris":["` + redirect + `"]}`)["client_id"].(string)
	res := e.postForm("/authorize", url.Values{
		"response_type": {"code"}, "client_id": {id}, "redirect_uri": {"https://evil.example/cb"},
		"code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"}, "password": {password},
	})
	if res.StatusCode != http.StatusBadRequest || res.Header.Get("Location") != "" {
		t.Fatalf("got %d to %q", res.StatusCode, res.Header.Get("Location"))
	}
	for _, bad := range []string{`"http://evil.example/cb"`, `"javascript:alert(1)"`} {
		res, _ := http.Post(e.srv.URL+"/register", "application/json", strings.NewReader(`{"redirect_uris":[`+bad+`]}`))
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("registered %s", bad)
		}
	}
}

func TestLoginLockout(t *testing.T) {
	e := newEnv(t, Options{Password: password})
	id := e.register(`{"redirect_uris":["` + redirect + `"]}`)["client_id"].(string)
	for range maxFailures {
		e.login(id, "wrong")
	}
	if res := e.login(id, password); res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("after %d failures: %d", maxFailures, res.StatusCode)
	}
}

func TestStaticToken(t *testing.T) {
	const token = "0123456789abcdef0123456789"
	e := newEnv(t, Options{Token: token})
	if err := e.callMCP(token); err != nil {
		t.Fatalf("header token: %v", err)
	}
	if err := e.callMCP("nope"); err == nil {
		t.Fatal("wrong token accepted")
	}
	res, _ := http.Get(e.srv.URL + "/.well-known/oauth-authorization-server")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("OAuth served without a password: %d", res.StatusCode)
	}
}

func TestValidate(t *testing.T) {
	key := bytes.Repeat([]byte("k"), 32)
	for _, o := range []Options{
		{},
		{Token: "short"},
		{PublicURL: "https://x.example"},
		{PublicURL: "https://x.example", Password: "short", Secret: key},
		{PublicURL: "http://x.example", Password: password, Secret: key},
		{PublicURL: "https://x.example/mcp", Password: password, Secret: key},
	} {
		if o.Validate() == nil {
			t.Errorf("accepted %+v", o)
		}
	}
	if err := (Options{PublicURL: "https://x.example", Password: password, Secret: key}).Validate(); err != nil {
		t.Error(err)
	}
}
