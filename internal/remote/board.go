package remote

import (
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The board signs in with the same password as the OAuth page and keeps a
// signed session cookie. Like tokens, it's signed with the password-derived key,
// so changing the password signs the browser out too.
const sessionTTL = 30 * 24 * time.Hour

func (s *server) boardRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /login", s.loginPage)
	m.HandleFunc("POST /login", s.login)
	m.HandleFunc("POST /logout", s.logout)
	m.Handle("GET /api/session", s.requireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"remote": true})
	})))
	m.Handle("/", s.requireSession(s.o.Board))
}

// cookieName uses the __Host- prefix over https, which pins the cookie to this
// exact host and path and requires Secure.
func (s *server) cookieName() string {
	if s.secure() {
		return "__Host-jobhunt"
	}
	return "jobhunt"
}

func (s *server) secure() bool { return strings.HasPrefix(s.base, "https://") }

func (s *server) setSession(w http.ResponseWriter) {
	exp := time.Now().Add(sessionTTL)
	http.SetCookie(w, &http.Cookie{
		Name: s.cookieName(), Value: seal(s.tokenKey, "session", claims{Exp: exp.Unix()}),
		Path: "/", Expires: exp, HttpOnly: true, Secure: s.secure(), SameSite: http.SameSiteLaxMode,
	})
}

// session reports whether the request is signed in, and when its cookie expires.
func (s *server) session(r *http.Request) (time.Time, bool) {
	ck, err := r.Cookie(s.cookieName())
	if err != nil {
		return time.Time{}, false
	}
	var c claims
	if !open(s.tokenKey, ck.Value, "session", &c) {
		return time.Time{}, false
	}
	return time.Unix(c.Exp, 0), true
}

// sameOrigin blocks cross-site requests that change data. SameSite=Lax already
// keeps the cookie off cross-site POSTs, and this covers older browsers and
// sibling subdomains, which count as same-site.
func (s *server) sameOrigin(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	// Privacy settings can make a browser send "null", so fall back to
	// Sec-Fetch-Site, which every current browser sends and pages can't forge.
	if o := r.Header.Get("Origin"); o != "" && o != "null" {
		return o == s.base
	}
	return r.Header.Get("Sec-Fetch-Site") == "same-origin"
}

func (s *server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		if !s.sameOrigin(r) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		exp, ok := s.session(r)
		switch {
		case !ok && strings.HasPrefix(r.URL.Path, "/api/"):
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Signed out. Reload the page to sign in."})
			return
		case !ok:
			http.Redirect(w, r, "/login?"+url.Values{"next": {r.URL.RequestURI()}}.Encode(), http.StatusFound)
			return
		case time.Until(exp) < sessionTTL/2:
			s.setSession(w) // keep regular users signed in
		}
		next.ServeHTTP(w, r)
	})
}

// safeNext keeps the post-login redirect on this site.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, `/\`) {
		return "/"
	}
	return next
}

func (s *server) loginData(r *http.Request) pageData {
	return pageData{
		Title: "Sign in to Job Hunt", Button: "Sign in", Action: "/login",
		Params: map[string]string{"next": safeNext(r.FormValue("next"))},
	}
}

func (s *server) loginPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.session(r); ok {
		http.Redirect(w, r, safeNext(r.FormValue("next")), http.StatusFound)
		return
	}
	renderPage(w, http.StatusOK, s.loginData(r))
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	r.ParseForm()
	d := s.loginData(r)
	if !s.sameOrigin(r) {
		d.Error = "Sign-in was blocked because it came from another site."
		renderPage(w, http.StatusForbidden, d)
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
	s.setSession(w)
	http.Redirect(w, r, d.Params["next"], http.StatusSeeOther)
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(r) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: s.cookieName(), Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.secure(), SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
