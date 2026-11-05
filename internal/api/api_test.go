package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"jobhunt/internal/config"
	"jobhunt/internal/store"
)

func newServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(config.EnvConfig, filepath.Join(dir, "config.json"))
	s, err := store.Open(filepath.Join(dir, "data", "jobhunt.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &Server{Store: s, UI: fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}}, dir
}

// Saving Settings must not copy the password or token from the environment
// into config.json, where they'd outlive their removal from .env.
func TestSettingsDoNotSaveEnvironmentSecrets(t *testing.T) {
	srv, dir := newServer(t)
	t.Setenv(config.EnvAuthPassword, "env-password-123456")
	t.Setenv(config.EnvRemoteToken, "env-static-token-0123456789abcdef")
	t.Setenv(config.EnvPublicURL, "https://jobhunt.example.com")

	db := filepath.Join(dir, "data", "jobhunt.db")
	r := httptest.NewRequest("PUT", "http://127.0.0.1:8787/api/settings", strings.NewReader(`{"email":"me@example.com","dbPath":`+strconv.Quote(db)+`}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"env-password", "env-static-token", "jobhunt.example.com"} {
		if strings.Contains(string(b), secret) {
			t.Errorf("config.json holds %q:\n%s", secret, b)
		}
	}
	if !strings.Contains(string(b), "jobhunt.db") {
		t.Errorf("dbPath not saved:\n%s", b)
	}
}

func TestBoardSendsSecurityHeaders(t *testing.T) {
	srv, _ := newServer(t)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1:8787/", nil))
	csp := w.Header().Get("Content-Security-Policy")
	for _, want := range []string{"script-src 'self'", "object-src 'none'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
	if w.Header().Get("X-Frame-Options") != "DENY" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("headers %v", w.Header())
	}
}

func TestLoopbackGuard(t *testing.T) {
	srv, _ := newServer(t)
	for _, c := range []struct {
		host, origin string
		want         int
	}{
		{"127.0.0.1:8787", "", http.StatusOK},
		{"localhost:8787", "http://localhost:8787", http.StatusOK},
		{"jobhunt.example.com", "", http.StatusForbidden},                // DNS rebinding or a public proxy
		{"127.0.0.1:8787", "https://evil.example", http.StatusForbidden}, // cross-site page
	} {
		r := httptest.NewRequest("GET", "http://"+c.host+"/api/applications", nil)
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("host %s origin %q: %d, want %d", c.host, c.origin, w.Code, c.want)
		}
	}
}
