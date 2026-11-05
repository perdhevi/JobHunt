// Package remote is the internet-facing listener. It serves MCP and a small
// OAuth 2.1 authorization server, which is what Claude and ChatGPT connectors
// expect: discovery (RFC 9728, RFC 8414), dynamic client registration (RFC 7591),
// a password login page and PKCE. It serves the board only when Options.Board
// is set, and then only behind the same password (see board.go).
//
// Client ids, codes and tokens are HMAC-signed claims, so a restart or redeploy
// doesn't log anyone out, and changing the password does. State stores only
// what a signature can't express: revocations, and codes and refresh tokens
// that were already used.
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
	"log"
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

	// A refresh token works once. A client that retries a refresh whose answer
	// it lost gets this long before the retry counts as theft.
	reuseLeeway = 30 * time.Second

	maxFailures = 5
	lockout     = 15 * time.Minute
	// Wrong passwords from all addresses together, per lockout window. This
	// caps guessing from many addresses; existing sessions keep working.
	maxGlobalFailures = 50
)

// DefaultRedirectHosts are where Claude and ChatGPT receive sign-in codes.
var DefaultRedirectHosts = []string{"claude.ai", "claude.com", "chatgpt.com"}

// Options configures the listener. OAuth is on when PublicURL and Password are
// set; Token alone keeps the older static-token behaviour.
type Options struct {
	PublicURL string // https://jobhunt.example.com, as clients reach it
	Password  string // the login page password
	Secret    []byte // signing secret, kept across restarts
	Token     string // optional static token, by header or in the path
	// Board is the UI and REST API, without loopback checks. Nil keeps it off.
	Board http.Handler
	// RedirectHosts limits where sign-in codes may be sent, besides localhost.
	// Nil means DefaultRedirectHosts.
	RedirectHosts []string
	// State records revocations. Nil keeps them in memory.
	State State
}

func (o Options) OAuth() bool { return o.PublicURL != "" && o.Password != "" }

type server struct {
	o             Options
	base          string
	clientKey     []byte // signs client ids, so they survive a password change
	tokenKey      []byte // signs codes and tokens, so a password change revokes them
	mcp           http.Handler
	state         State
	redirectHosts []string

	mu          sync.Mutex
	failures    map[string][]time.Time
	allFailures []time.Time
}

// Handler serves MCP at /mcp behind a bearer token, plus the OAuth endpoints.
func Handler(next http.Handler, o Options) http.Handler {
	s := &server{
		o:         o,
		base:      strings.TrimSuffix(o.PublicURL, "/"),
		clientKey: mac(o.Secret, "client"),
		// v2 added issue times and token families, so older tokens are refused
		// and each app signs in once more.
		tokenKey:      mac(o.Secret, "token/v2\x00"+o.Password),
		mcp:           next,
		state:         o.State,
		redirectHosts: o.RedirectHosts,
		failures:      map[string][]time.Time{},
	}
	if s.state == nil {
		s.state = newMemState()
	}
	if s.redirectHosts == nil {
		s.redirectHosts = DefaultRedirectHosts
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
	if o.Board != nil {
		s.boardRoutes(m)
	}
	return cors(m)
}

func (s *server) staticToken(got string) bool {
	return s.o.Token != "" && subtle.ConstantTimeCompare([]byte(got), []byte(s.o.Token)) == 1
}

func (s *server) verify(ctx context.Context, tok string, _ *http.Request) (*auth.TokenInfo, error) {
	if s.staticToken(tok) {
		return &auth.TokenInfo{Scopes: []string{scope}, Expiration: time.Now().Add(time.Hour), UserID: "owner"}, nil
	}
	var c claims
	if !s.o.OAuth() || !open(s.tokenKey, tok, "access", &c) || !s.live(ctx, c, "fam:"+c.Fam) {
		return nil, auth.ErrInvalidToken
	}
	return &auth.TokenInfo{Scopes: []string{scope}, Expiration: time.Unix(c.Exp, 0), UserID: "owner"}, nil
}

// live reports whether a signed value is still in force: issued after the last
// "sign out everywhere", and none of the given marks (a revoked session or
// token family) recorded. It fails closed if the database can't be read.
func (s *server) live(ctx context.Context, c claims, revoked ...string) bool {
	cut, err := s.state.RevokedBefore(ctx)
	if err != nil {
		log.Printf("auth: read revocations: %v", err)
		return false
	}
	if c.Iat == 0 || c.Iat <= cut.UnixMilli() {
		return false
	}
	for _, id := range revoked {
		gone, err := s.state.Marked(ctx, id)
		if err != nil {
			log.Printf("auth: read revocations: %v", err)
			return false
		}
		if gone {
			return false
		}
	}
	return true
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
	Iat       int64    `json:"t,omitempty"`  // sign-in time in unix ms, kept across refreshes and renewals
	Fam       string   `json:"f,omitempty"`  // one sign-in of one app: its code and every token from it
	ID        string   `json:"i,omitempty"`  // nonce of a code, refresh token or session
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
		return isLocal(u.Hostname())
	}
	return false
}

func isLocal(host string) bool {
	ip := net.ParseIP(host)
	return strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())
}

// redirectAllowed also limits https redirects to known hosts. Registration is
// open to anyone, so without this a stranger could register a client named
// "Claude" that sends your sign-in code to their own site.
func (s *server) redirectAllowed(raw string) bool {
	if !validRedirect(raw) {
		return false
	}
	u, _ := url.Parse(raw)
	h := u.Hostname()
	return isLocal(h) || slices.ContainsFunc(s.redirectHosts, func(a string) bool { return strings.EqualFold(a, h) })
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
		if !s.redirectAllowed(u) {
			log.Printf("auth: refused to register %q with redirect %q, host not allowed", in.ClientName, u)
			regError(w, "invalid_redirect_uri", "this server doesn't send sign-ins to that host. Its owner can add it with JOBHUNT_REDIRECT_HOSTS: "+u)
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
<h1>{{.Title}}</h1>
{{if .Client}}<p><b>{{.Client}}</b> wants to read and update your job pipeline. After you sign in, you'll go back to <b>{{.Host}}</b>.</p>
{{else}}<p>Enter your password to open the board.</p>{{end}}
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<form method="post" action="{{.Action}}">
{{range $k, $v := .Params}}<input type="hidden" name="{{$k}}" value="{{$v}}">{{end}}
<input type="password" name="password" placeholder="Password" autocomplete="current-password" autofocus required>
<button type="submit">{{.Button}}</button>
</form>{{end}}
</main></body></html>`))

type pageData struct {
	Fatal, Error, Client, Host string
	Title, Button, Action      string
	Params                     map[string]string
	// ReturnTo is the origin the form may lead to besides this server: the
	// app's checked redirect. Browsers apply form-action to the redirect that
	// follows a post too, so without it the sign-in never gets back to the app.
	ReturnTo string
}

func renderPage(w http.ResponseWriter, status int, d pageData) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Frame-Options", "DENY")
	formAction := "'self'"
	if d.ReturnTo != "" {
		formAction += " " + d.ReturnTo
	}
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action "+formAction+"; frame-ancestors 'none'")
	// Not no-referrer: with that, browsers send "Origin: null" on the form post.
	h.Set("Referrer-Policy", "same-origin")
	w.WriteHeader(status)
	page.Execute(w, d)
}

// authorize shows the login page on GET and checks the password on POST. Until
// the client and redirect are known to be good, errors are shown, not redirected,
// so the endpoint can't be used to bounce someone to an arbitrary site.
func (s *server) authorize(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
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
	// Checked again here because a client id stays valid after the allowed
	// hosts change, and until now nothing may redirect anywhere.
	if !s.redirectAllowed(redirect) {
		renderPage(w, http.StatusBadRequest, pageData{Fatal: "This app sends sign-ins to a host this server doesn't allow."})
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
	d := pageData{Client: client, Host: u.Host, Params: q, ReturnTo: u.Scheme + "://" + u.Host}
	d.Title, d.Button, d.Action = "Connect to Job Hunt", "Allow access", "/authorize"
	if r.Method == http.MethodGet {
		renderPage(w, http.StatusOK, d)
		return
	}
	ip := clientIP(r)
	if status, msg := s.checkPassword(r, ip, "connector sign-in"); status != 0 {
		d.Error = msg
		renderPage(w, status, d)
		return
	}
	now := time.Now()
	code := seal(s.tokenKey, "code", claims{
		Exp: now.Add(codeTTL).Unix(), Iat: now.UnixMilli(), ID: rand.Text(), Fam: rand.Text(),
		Client: q["client_id"], Redirect: redirect, Challenge: q["code_challenge"],
	})
	log.Printf("auth: %q signed in from %s, returning to %s", client, ip, u.Host)
	back(url.Values{"code": {code}})
}

// checkPassword applies the lockout and compares the posted password, logging
// the outcome. It returns 0 for the right password, otherwise the status and
// message for the sign-in page.
func (s *server) checkPassword(r *http.Request, ip, where string) (int, string) {
	key := lockKey(ip)
	if s.lockedOut(key) {
		log.Printf("auth: %s from %s refused, too many wrong passwords", where, ip)
		return http.StatusTooManyRequests, "Too many wrong passwords. Try again in 15 minutes."
	}
	// Comparing MACs keeps the time taken independent of the password's length.
	if !hmac.Equal(mac(s.clientKey, "pw\x00"+r.PostForm.Get("password")), mac(s.clientKey, "pw\x00"+s.o.Password)) {
		s.recordFailure(key)
		log.Printf("auth: wrong password on %s from %s", where, ip)
		return http.StatusUnauthorized, "Wrong password."
	}
	return 0, ""
}

// lockedOut is true after maxFailures from one address, or maxGlobalFailures
// from all of them, within the lockout window.
func (s *server) lockedOut(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := func(t time.Time) bool { return time.Since(t) > lockout }
	s.allFailures = slices.DeleteFunc(s.allFailures, old)
	recent := slices.DeleteFunc(s.failures[ip], old)
	if len(recent) == 0 {
		delete(s.failures, ip)
	} else {
		s.failures[ip] = recent
	}
	return len(recent) >= maxFailures || len(s.allFailures) >= maxGlobalFailures
}

func (s *server) recordFailure(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.failures[ip] = append(s.failures[ip], now)
	s.allFailures = append(s.allFailures, now)
	// Addresses that never come back would otherwise stay forever.
	if len(s.failures) > 2*maxGlobalFailures {
		for k, ts := range s.failures {
			if now.Sub(ts[len(ts)-1]) > lockout {
				delete(s.failures, k)
			}
		}
	}
}

// lockKey is what lockouts count against. An IPv6 machine usually owns a whole
// /64, so counting single addresses there would give one machine unlimited
// guesses until the global cap shut everyone out.
func lockKey(ip string) string {
	a := net.ParseIP(ip)
	if a == nil || a.To4() != nil {
		return ip
	}
	return a.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// clientIP trusts X-Forwarded-For only from a proxy on a private network, which
// is how Caddy reaches this listener. Caddy overwrites the header it receives.
// A value that isn't an address is ignored, so it can't reach the log or
// stand in for a fresh lockout key.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" && ip != nil && (ip.IsLoopback() || ip.IsPrivate()) {
		parts := strings.Split(xff, ",")
		if fwd := net.ParseIP(strings.TrimSpace(parts[len(parts)-1])); fwd != nil {
			return fwd.String()
		}
	}
	return host
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
	if err != nil {
		log.Printf("auth: token endpoint: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// revokeFamily signs one app out: every token from one sign-in stops working.
// Nothing in a family outlives refreshTTL from its last refresh, and after
// this there are no more refreshes, so the mark can expire then.
func (s *server) revokeFamily(ctx context.Context, fam, why string) {
	if _, _, err := s.state.Mark(ctx, "fam:"+fam, time.Now().Add(refreshTTL)); err != nil {
		log.Printf("auth: revoke after %s: %v", why, err)
		return
	}
	log.Printf("auth: %s, signed that app out", why)
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

	ctx := r.Context()
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
		if !s.live(ctx, g, "fam:"+g.Fam) {
			return nil, badGrant("code was revoked")
		}
		first, _, err := s.state.Mark(ctx, "code:"+g.ID, time.Unix(g.Exp, 0))
		if err != nil {
			return nil, err
		}
		if !first {
			// A second redemption means the code leaked. Whoever redeemed it
			// first may be the thief, so the tokens it got stop working too.
			s.revokeFamily(ctx, g.Fam, fmt.Sprintf("sign-in code for %q used twice", c.Name))
			return nil, badGrant("code was already used")
		}
	case "refresh_token":
		if !open(s.tokenKey, r.PostForm.Get("refresh_token"), "refresh", &g) || g.Client != id {
			return nil, badGrant("refresh token is invalid or expired. Reconnect the app")
		}
		if !s.live(ctx, g, "fam:"+g.Fam) {
			return nil, badGrant("this app was signed out. Reconnect it")
		}
		first, at, err := s.state.Mark(ctx, "refresh:"+g.ID, time.Unix(g.Exp, 0))
		if err != nil {
			return nil, err
		}
		if !first && time.Since(at) > reuseLeeway {
			// Each refresh hands out a new refresh token, so an old one coming
			// back means two parties hold it. Sign the app out rather than
			// guess which one is legitimate.
			s.revokeFamily(ctx, g.Fam, fmt.Sprintf("old refresh token for %q used again", c.Name))
			return nil, badGrant("refresh token was already used. Reconnect the app")
		}
	default:
		return nil, &tokenError{http.StatusBadRequest, "unsupported_grant_type", "use authorization_code or refresh_token"}
	}
	now := time.Now()
	return map[string]any{
		"access_token":  seal(s.tokenKey, "access", claims{Exp: now.Add(accessTTL).Unix(), Iat: g.Iat, Fam: g.Fam, Client: id}),
		"refresh_token": seal(s.tokenKey, "refresh", claims{Exp: now.Add(refreshTTL).Unix(), Iat: g.Iat, Fam: g.Fam, ID: rand.Text(), Client: id}),
		"token_type":    "Bearer",
		"expires_in":    int(accessTTL.Seconds()),
		"scope":         scope,
	}, nil
}

// cors lets browser-based clients such as MCP Inspector discover and call the
// server. It covers only the MCP and OAuth routes, which take bearer tokens or
// client credentials, never the board's session cookie.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/mcp" || strings.HasPrefix(p, "/mcp/") || strings.HasPrefix(p, "/.well-known/") || p == "/register" || p == "/token" {
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
	if o.Board != nil && !o.OAuth() {
		return errors.New("the board needs remoteMcp.publicUrl and password, so it can ask for a login")
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
	for _, h := range o.RedirectHosts {
		if h == "" || strings.ContainsAny(h, "/:@ ") {
			return fmt.Errorf("redirect hosts must be bare host names such as claude.ai: %q", h)
		}
	}
	return nil
}
