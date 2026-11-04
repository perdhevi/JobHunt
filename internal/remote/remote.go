// Package remote is the internet-facing MCP listener. It serves MCP and a small
// OAuth 2.1 authorization server, which is what Claude and ChatGPT connectors
// expect: discovery (RFC 9728, RFC 8414), dynamic client registration (RFC 7591),
// a password login page and PKCE. It never serves the UI or REST API.
//
// Nothing is stored. Client ids, codes and tokens are HMAC-signed claims, so a
// restart or redeploy doesn't log anyone out, and changing the password does.
package remote

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

const (
	accessTTL  = time.Hour
	refreshTTL = 90 * 24 * time.Hour
	codeTTL    = time.Minute
	scope      = "jobhunt"

	maxFailures = 5
	lockout     = 15 * time.Minute
)

// Options configures the listener. OAuth is on when PublicURL and Password are
// set; Token alone keeps the older static-token behaviour.
type Options struct {
	PublicURL string // https://jobhunt.example.com, as clients reach it
	Password  string // the login page password
	Secret    []byte // signing secret, kept across restarts
	Token     string // optional static token, by header or in the path
}

func (o Options) OAuth() bool { return o.PublicURL != "" && o.Password != "" }

type server struct {
	o         Options
	base      string
	clientKey []byte // signs client ids, so they survive a password change
	tokenKey  []byte // signs codes and tokens, so a password change revokes them
	mcp       http.Handler

	mu       sync.Mutex
	used     map[string]time.Time // spent authorization codes, until they expire
	failures map[string][]time.Time
}

// Handler serves MCP at /mcp behind a bearer token, plus the OAuth endpoints.
func Handler(next http.Handler, o Options) http.Handler {
	s := &server{
		o:         o,
		base:      strings.TrimSuffix(o.PublicURL, "/"),
		clientKey: mac(o.Secret, "client"),
		tokenKey:  mac(o.Secret, "token\x00"+o.Password),
		mcp:       next,
		used:      map[string]time.Time{},
		failures:  map[string][]time.Time{},
	}
	opts := &auth.RequireBearerTokenOptions{}
	m := http.NewServeMux()
	if o.OAuth() {
		opts.ResourceMetadataURL = s.base + "/.well-known/oauth-protected-resource/mcp"
		prm := auth.ProtectedResourceMetadataHandler(&oauthex.ProtectedResourceMetadata{
			Resource:               s.base + "/mcp",
			AuthorizationServers:   []string{s.base},
			ScopesSupported:        []string{scope},
			BearerMethodsSupported: []string{"header"},
			ResourceName:           "Job Hunt",
		})
		m.Handle("GET /.well-known/oauth-protected-resource", prm)
		m.Handle("GET /.well-known/oauth-protected-resource/mcp", prm)
		m.HandleFunc("GET /.well-known/oauth-authorization-server", s.metadata)
		m.HandleFunc("POST /register", s.register)
		m.HandleFunc("GET /authorize", s.authorize)
		m.HandleFunc("POST /authorize", s.authorize)
		m.HandleFunc("POST /token", s.token)
	}
	m.Handle("/mcp", auth.RequireBearerToken(s.verify, opts)(next))
	if o.Token != "" {
		// ChatGPT once had no header option, so the static token may be the path.
		m.HandleFunc("/mcp/{token}", func(w http.ResponseWriter, r *http.Request) {
			if !s.staticToken(r.PathValue("token")) {
				http.NotFound(w, r)
				return
			}
			r = r.Clone(r.Context())
			r.URL.Path = "/mcp"
			next.ServeHTTP(w, r)
		})
	}
	return cors(m)
}

func (s *server) staticToken(got string) bool {
	return s.o.Token != "" && subtle.ConstantTimeCompare([]byte(got), []byte(s.o.Token)) == 1
}

func (s *server) verify(_ context.Context, tok string, _ *http.Request) (*auth.TokenInfo, error) {
	if s.staticToken(tok) {
		return &auth.TokenInfo{Scopes: []string{scope}, Expiration: time.Now().Add(time.Hour), UserID: "owner"}, nil
	}
	var c claims
	if !s.o.OAuth() || !open(s.tokenKey, tok, "access", &c) {
		return nil, auth.ErrInvalidToken
	}
	return &auth.TokenInfo{Scopes: []string{scope}, Expiration: time.Unix(c.Exp, 0), UserID: "owner"}, nil
}

func (s *server) metadata(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                s.base,
		"authorization_endpoint":                s.base + "/authorize",
		"token_endpoint":                        s.base + "/token",
		"registration_endpoint":                 s.base + "/register",
		"scopes_supported":                      []string{scope},
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"token_endpoint_auth_methods_supported": []string{"none", "client_secret_post", "client_secret_basic"},
		"code_challenge_methods_supported":      []string{"S256"},
	})
}

// claims is the payload of every signed value. K says what it is, so one kind
// can never be passed off as another.
type claims struct {
	K         string   `json:"k"`
	Exp       int64    `json:"e,omitempty"`
	ID        string   `json:"i,omitempty"`  // code nonce
	Client    string   `json:"c,omitempty"`  // client id, on codes and tokens
	Redirect  string   `json:"r,omitempty"`  // on codes
	Challenge string   `json:"pk,omitempty"` // on codes
	Redirects []string `json:"rs,omitempty"` // on client ids
	Name      string   `json:"n,omitempty"`  // on client ids
	Secret    bool     `json:"s,omitempty"`  // client authenticates with a secret
}

func mac(key []byte, msg string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(msg))
	return h.Sum(nil)
}

func seal(key []byte, kind string, c claims) string {
	c.K = kind
	b, _ := json.Marshal(c)
	p := base64.RawURLEncoding.EncodeToString(b)
	return p + "." + base64.RawURLEncoding.EncodeToString(mac(key, p))
}

func open(key []byte, v, kind string, c *claims) bool {
	p, sig, ok := strings.Cut(v, ".")
	if !ok {
		return false
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, mac(key, p)) {
		return false
	}
	b, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil || json.Unmarshal(b, c) != nil || c.K != kind {
		return false
	}
	return c.Exp == 0 || time.Now().Unix() < c.Exp
}

func (s *server) client(id string) (claims, bool) {
	var c claims
	return c, open(s.clientKey, id, "client", &c)
}

func (s *server) clientSecret(id string) string {
	return base64.RawURLEncoding.EncodeToString(mac(s.clientKey, "secret\x00"+id))
}

// validRedirect allows https, and http only to this machine for desktop clients.
func validRedirect(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Fragment != "" || u.Host == "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		h := u.Hostname()
		ip := net.ParseIP(h)
		return h == "localhost" || (ip != nil && ip.IsLoopback())
	}
	return false
}

func (s *server) register(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RedirectURIs []string `json:"redirect_uris"`
		ClientName   string   `json:"client_name"`
		AuthMethod   string   `json:"token_endpoint_auth_method"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		regError(w, "invalid_client_metadata", "body must be JSON")
		return
	}
	if len(in.RedirectURIs) == 0 || len(in.RedirectURIs) > 10 {
		regError(w, "invalid_redirect_uri", "pass between 1 and 10 redirect_uris")
		return
	}
	for _, u := range in.RedirectURIs {
		if !validRedirect(u) {
			regError(w, "invalid_redirect_uri", "redirect_uris must be https, or http on localhost: "+u)
			return
		}
	}
	if in.AuthMethod == "" {
		in.AuthMethod = "none"
	}
	if !slices.Contains([]string{"none", "client_secret_post", "client_secret_basic"}, in.AuthMethod) {
		regError(w, "invalid_client_metadata", "unsupported token_endpoint_auth_method")
		return
	}
	name := in.ClientName
	if len(name) > 100 {
		name = name[:100]
	}
	id := seal(s.clientKey, "client", claims{Redirects: in.RedirectURIs, Name: name, Secret: in.AuthMethod != "none"})
	out := map[string]any{
		"client_id":                  id,
		"client_id_issued_at":        time.Now().Unix(),
		"client_name":                name,
		"redirect_uris":              in.RedirectURIs,
		"token_endpoint_auth_method": in.AuthMethod,
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"scope":                      scope,
	}
	if in.AuthMethod != "none" {
		out["client_secret"] = s.clientSecret(id)
		out["client_secret_expires_at"] = 0
	}
	writeJSON(w, http.StatusCreated, out)
}

func regError(w http.ResponseWriter, code, desc string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": code, "error_description": desc})
}

var page = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Job Hunt sign-in</title>
<style>
:root{color-scheme:light dark;--bg:#f6f6f4;--card:#fff;--fg:#1d1d1b;--muted:#6b6b66;--line:#ddd;--accent:#2d5bd7;--err:#b3261e}
@media (prefers-color-scheme:dark){:root{--bg:#161615;--card:#20201f;--fg:#ececea;--muted:#a3a39e;--line:#3a3a38;--accent:#7b9cff;--err:#ff8a80}}
body{margin:0;min-height:100vh;display:grid;place-items:center;background:var(--bg);color:var(--fg);font:16px/1.5 system-ui,sans-serif;padding:16px;box-sizing:border-box}
main{background:var(--card);border:1px solid var(--line);border-radius:12px;padding:28px;width:100%;max-width:380px;box-sizing:border-box}
h1{font-size:20px;margin:0 0 6px}p{margin:0 0 18px;color:var(--muted)}b{color:var(--fg)}
input,button{width:100%;box-sizing:border-box;font:inherit;padding:10px 12px;border-radius:8px}
input{border:1px solid var(--line);background:transparent;color:inherit;margin-bottom:12px}
button{border:0;background:var(--accent);color:#fff;font-weight:600;cursor:pointer}
.err{color:var(--err)}
</style></head><body><main>
{{if .Fatal}}<h1>Can't sign in</h1><p class="err">{{.Fatal}}</p>{{else}}
<h1>Connect to Job Hunt</h1>
<p><b>{{.Client}}</b> wants to read and update your job pipeline. After you sign in, you'll go back to <b>{{.Host}}</b>.</p>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<form method="post" action="/authorize">
{{range $k, $v := .Params}}<input type="hidden" name="{{$k}}" value="{{$v}}">{{end}}
<input type="password" name="password" placeholder="Password" autocomplete="current-password" autofocus required>
<button type="submit">Allow access</button>
</form>{{end}}
</main></body></html>`))

type pageData struct {
	Fatal, Error, Client, Host string
	Params                     map[string]string
}

func renderPage(w http.ResponseWriter, status int, d pageData) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
	h.Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	page.Execute(w, d)
}

// authorize shows the login page on GET and checks the password on POST. Until
// the client and redirect are known to be good, errors are shown, not redirected,
// so the endpoint can't be used to bounce someone to an arbitrary site.
func (s *server) authorize(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	q := map[string]string{}
	for _, k := range []string{"response_type", "client_id", "redirect_uri", "state", "code_challenge", "code_challenge_method", "scope", "resource"} {
		if v := r.Form.Get(k); v != "" {
			q[k] = v
		}
	}
	c, ok := s.client(q["client_id"])
	if !ok {
		renderPage(w, http.StatusBadRequest, pageData{Fatal: "This app isn't registered here. Remove the connector and add it again."})
		return
	}
	redirect := q["redirect_uri"]
	if redirect == "" && len(c.Redirects) == 1 {
		redirect = c.Redirects[0]
	}
	if !slices.Contains(c.Redirects, redirect) {
		renderPage(w, http.StatusBadRequest, pageData{Fatal: "The redirect address doesn't match the one this app registered."})
		return
	}
	back := func(params url.Values) {
		if q["state"] != "" {
			params.Set("state", q["state"])
		}
		params.Set("iss", s.base)
		u, _ := url.Parse(redirect)
		v := u.Query()
		for k := range params {
			v.Set(k, params.Get(k))
		}
		u.RawQuery = v.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	}
	fail := func(code, desc string) { back(url.Values{"error": {code}, "error_description": {desc}}) }
	switch {
	case q["response_type"] != "code":
		fail("unsupported_response_type", "only response_type=code is supported")
		return
	case q["code_challenge"] == "" || q["code_challenge_method"] != "S256":
		fail("invalid_request", "PKCE with S256 is required")
		return
	case q["resource"] != "" && strings.TrimSuffix(q["resource"], "/") != s.base+"/mcp" && strings.TrimSuffix(q["resource"], "/") != s.base:
		fail("invalid_target", "unknown resource")
		return
	}
	client := c.Name
	if client == "" {
		client = "An app"
	}
	u, _ := url.Parse(redirect)
	d := pageData{Client: client, Host: u.Host, Params: q}
	if r.Method == http.MethodGet {
		renderPage(w, http.StatusOK, d)
		return
	}
	ip := clientIP(r)
	if s.lockedOut(ip) {
		d.Error = "Too many wrong passwords. Try again in 15 minutes."
		renderPage(w, http.StatusTooManyRequests, d)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.PostForm.Get("password")), []byte(s.o.Password)) != 1 {
		s.recordFailure(ip)
		d.Error = "Wrong password."
		renderPage(w, http.StatusUnauthorized, d)
		return
	}
	code := seal(s.tokenKey, "code", claims{
		Exp: time.Now().Add(codeTTL).Unix(), ID: rand.Text(),
		Client: q["client_id"], Redirect: redirect, Challenge: q["code_challenge"],
	})
	back(url.Values{"code": {code}})
}

func (s *server) lockedOut(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	recent := slices.DeleteFunc(s.failures[ip], func(t time.Time) bool { return time.Since(t) > lockout })
	if len(recent) == 0 {
		delete(s.failures, ip)
	} else {
		s.failures[ip] = recent
	}
	return len(recent) >= maxFailures
}

func (s *server) recordFailure(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures[ip] = append(s.failures[ip], time.Now())
}

// clientIP trusts X-Forwarded-For only from a proxy on a private network, which
// is how Caddy reaches this listener. Caddy overwrites the header it receives.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" && ip != nil && (ip.IsLoopback() || ip.IsPrivate()) {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[len(parts)-1])
	}
	return host
}

// spend marks a code used, so a leaked code can't be redeemed a second time.
func (s *server) spend(id string, exp int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, t := range s.used {
		if time.Now().After(t) {
			delete(s.used, k)
		}
	}
	if _, dup := s.used[id]; dup {
		return false
	}
	s.used[id] = time.Unix(exp, 0)
	return true
}

type tokenError struct {
	status     int
	code, desc string
}

func (e *tokenError) Error() string { return e.code + ": " + e.desc }

func badGrant(desc string) error { return &tokenError{http.StatusBadRequest, "invalid_grant", desc} }

func (s *server) token(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	res, err := s.grant(r)
	var te *tokenError
	if errors.As(err, &te) {
		if te.status == http.StatusUnauthorized {
			w.Header().Set("WWW-Authenticate", `Basic realm="jobhunt"`)
		}
		writeJSON(w, te.status, map[string]string{"error": te.code, "error_description": te.desc})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *server) grant(r *http.Request) (map[string]any, error) {
	id, secret, basic := r.BasicAuth()
	if basic {
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	} else {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	c, ok := s.client(id)
	if !ok {
		return nil, &tokenError{http.StatusUnauthorized, "invalid_client", "unknown client"}
	}
	if c.Secret && subtle.ConstantTimeCompare([]byte(secret), []byte(s.clientSecret(id))) != 1 {
		return nil, &tokenError{http.StatusUnauthorized, "invalid_client", "wrong client secret"}
	}

	var g claims
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		if !open(s.tokenKey, r.PostForm.Get("code"), "code", &g) || g.Client != id {
			return nil, badGrant("code is invalid or expired")
		}
		if redirect := r.PostForm.Get("redirect_uri"); redirect != "" && redirect != g.Redirect {
			return nil, badGrant("redirect_uri doesn't match")
		}
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != g.Challenge {
			return nil, badGrant("code_verifier doesn't match")
		}
		if !s.spend(g.ID, g.Exp) {
			return nil, badGrant("code was already used")
		}
	case "refresh_token":
		if !open(s.tokenKey, r.PostForm.Get("refresh_token"), "refresh", &g) || g.Client != id {
			return nil, badGrant("refresh token is invalid or expired. Reconnect the app")
		}
	default:
		return nil, &tokenError{http.StatusBadRequest, "unsupported_grant_type", "use authorization_code or refresh_token"}
	}
	now := time.Now()
	return map[string]any{
		"access_token":  seal(s.tokenKey, "access", claims{Exp: now.Add(accessTTL).Unix(), Client: id}),
		"refresh_token": seal(s.tokenKey, "refresh", claims{Exp: now.Add(refreshTTL).Unix(), Client: id}),
		"token_type":    "Bearer",
		"expires_in":    int(accessTTL.Seconds()),
		"scope":         scope,
	}, nil
}

// cors lets browser-based clients such as MCP Inspector discover and call the
// server. Every route needs a token or a password, so no cookie is ever trusted.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/authorize" {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", "*")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Mcp-Session-Id, Mcp-Protocol-Version, Last-Event-ID")
			h.Set("Access-Control-Expose-Headers", "Mcp-Session-Id, WWW-Authenticate")
			h.Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// Validate checks the options before the listener starts.
func (o Options) Validate() error {
	if o.Token != "" && len(o.Token) < 24 {
		return errors.New("remoteMcp.token must be at least 24 characters")
	}
	if o.PublicURL == "" && o.Password == "" {
		if o.Token == "" {
			return errors.New("the remote MCP listener needs remoteMcp.publicUrl and password, or a token")
		}
		return nil
	}
	if o.PublicURL == "" || o.Password == "" {
		return errors.New("OAuth needs both remoteMcp.publicUrl (JOBHUNT_PUBLIC_URL) and remoteMcp.password (JOBHUNT_AUTH_PASSWORD)")
	}
	if len(o.Password) < 12 {
		return errors.New("remoteMcp.password must be at least 12 characters")
	}
	u, err := url.Parse(o.PublicURL)
	if err != nil || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		return fmt.Errorf("remoteMcp.publicUrl must look like https://jobhunt.example.com: %q", o.PublicURL)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && validRedirect(o.PublicURL)) {
		return errors.New("remoteMcp.publicUrl must be https, except on localhost")
	}
	if len(o.Secret) < 32 {
		return errors.New("auth secret must be at least 32 bytes")
	}
	return nil
}
