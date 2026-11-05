package api

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"jobhunt/internal/config"
)

func serveOn(t *testing.T, srv *Server, addr string) net.Listener {
	t.Helper()
	ln, err := Listen(addr)
	if err != nil {
		t.Fatal(err)
	}
	hs := &http.Server{Handler: srv.Handler()}
	go hs.Serve(ln)
	t.Cleanup(func() { hs.Close() })
	return ln
}

func TestHealthOverTCPAndSocket(t *testing.T) {
	srv, dir := newServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ln := serveOn(t, srv, "127.0.0.1:0")
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	// A container listens on 0.0.0.0, and the check still reaches it.
	if err := CheckHealth(ctx, "0.0.0.0:"+port); err != nil {
		t.Fatalf("tcp: %v", err)
	}

	sock := filepath.Join(dir, "board.sock")
	serveOn(t, srv, config.UnixPrefix+sock)
	if err := CheckHealth(ctx, config.UnixPrefix+sock); err != nil {
		t.Fatalf("socket: %v", err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(sock)
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("socket mode %v %v, want 0600", fi.Mode(), err)
		}
	}
}

// A crash leaves the socket file behind. The next start must still bind.
func TestListenReplacesStaleSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows doesn't report AF_UNIX files as sockets; the socket is for Linux servers")
	}
	sock := filepath.Join(t.TempDir(), "board.sock")
	ln, err := Listen(config.UnixPrefix + sock)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	ln, err = Listen(config.UnixPrefix + sock)
	if err != nil {
		t.Fatalf("after a crash: %v", err)
	}
	ln.Close()
	// Anything other than a socket is left alone.
	os.WriteFile(sock, []byte("not a socket"), 0o600)
	if ln, err := Listen(config.UnixPrefix + sock); err == nil {
		ln.Close()
		t.Fatal("replaced a regular file")
	}
}

func TestSettingsPathFixedByEnvironment(t *testing.T) {
	srv, dir := newServer(t)
	env := filepath.Join(dir, "data", "jobhunt.db")
	t.Setenv(config.EnvDB, env)
	put := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("PUT", "http://127.0.0.1:8787/api/settings", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		return w
	}
	// The board sends the path back unchanged with every save.
	if w := put(`{"email":"me@example.com","dbPath":` + strconv.Quote(env) + `}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"dbPathFixed":true`) {
		t.Fatalf("unchanged path: %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); !os.IsNotExist(err) {
		t.Fatalf("config.json written although the environment decides: %v", err)
	}
	if w := put(`{"email":"other@example.com","dbPath":"/tmp/elsewhere.db"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("changed path: %d %s", w.Code, w.Body)
	}
	if v, _ := srv.Store.GetSetting(context.Background(), "email"); v != "me@example.com" {
		t.Fatalf("a refused save still changed the email to %q", v)
	}
}

func TestAssetsCacheAndNoListing(t *testing.T) {
	srv, _ := newServer(t)
	srv.UI = fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html>index")},
		"assets/index-abc1.js": {Data: []byte("console.log(1)")},
	}
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1:8787"+path, nil))
		return w
	}
	if w := get("/assets/index-abc1.js"); !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("hashed asset: Cache-Control %q", w.Header().Get("Cache-Control"))
	}
	if w := get("/"); w.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("index: Cache-Control %q", w.Header().Get("Cache-Control"))
	}
	if w := get("/assets/"); strings.Contains(w.Body.String(), "index-abc1.js") || !strings.Contains(w.Body.String(), "index") {
		t.Errorf("folder listed: %s", w.Body)
	}
}
