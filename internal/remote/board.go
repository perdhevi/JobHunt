package remote

import (
	"crypto/rand"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The board signs in with the same password as the OAuth page and keeps a
// signed session cookie. Like tokens, it's signed with the password-derived key,
// so changing the password signs the browser out too. Signing out revokes the
// session on the server, so a copied cookie stops working as well.
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

// setSession issues the cookie. A renewal keeps the session's id and sign-in
// time, so signing out and "sign out everywhere" still catch it.
func (s *server) setSession(w http.ResponseWriter, c claims) {
	exp := time.Now().Add(sessionTTL)
	c.Exp = exp.Unix()
	http.SetCookie(w, &http.Cookie{
		Name: s.cookieName(), Value: seal(s.tokenKey, "session", c),
		Path: "/", Expires: exp, HttpOnly: true, Secure: s.secure(), SameSite: http.SameSiteLaxMode,
	})
}

// session returns the request's session if it's signed in.
func (s *server) session(r *http.Request) (claims, bool) {
	var c claims
	ck, err := r.Cookie(s.cookieName())
	if err != nil || !open(s.tokenKey, ck.Value, "session", &c) {
		return c, false
	}
	return c, s.live(r.Context(), c, "session:"+c.ID)
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
		c, ok := s.session(r)
		switch {
		case !ok && strings.HasPrefix(r.URL.Path, "/api/"):
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Signed out. Reload the page to sign in."})
			return
		case !ok:
			http.Redirect(w, r, "/login?"+url.Values{"next": {r.URL.RequestURI()}}.Encode(), http.StatusFound)
			return
		case time.Until(time.Unix(c.Exp, 0)) < sessionTTL/2:
			s.setSession(w, c) // keep regular users signed in
		}
		next.ServeHTTP(w, r)
	})
}

// safeNext keeps the post-login redirect on this site. url.Parse refuses
// control characters, which matters because browsers delete tabs and newlines
// from a Location header: "/<TAB>/evil.example" would arrive as //evil.example.
func safeNext(next string) string {
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil ||
		!strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.Contains(next, `\`) {
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
	if status, msg := s.checkPassword(r, ip, "board sign-in"); status != 0 {
		d.Error = msg
		renderPage(w, status, d)
		return
	}
	log.Printf("auth: board signed in from %s", ip)
	s.setSession(w, claims{Iat: time.Now().UnixMilli(), ID: rand.Text()})
	http.Redirect(w, r, d.Params["next"], http.StatusSeeOther)
}

// logout signs this browser out, or with everywhere=1 every browser and app.
// Only a signed-in browser may do the latter, since it would otherwise let
// anyone sign the owner out of everything.
func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(r) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	r.ParseForm()
	if c, ok := s.session(r); ok {
		var err error
		if r.PostForm.Get("everywhere") != "" {
			if err = s.state.RevokeAll(r.Context()); err == nil {
				log.Printf("auth: every browser and app signed out, from %s", clientIP(r))
			}
		} else {
			// A cookie with this id can't be renewed any more, so none
			// outlives sessionTTL from now.
			_, _, err = s.state.Mark(r.Context(), "session:"+c.ID, time.Now().Add(sessionTTL))
		}
		if err != nil {
			log.Printf("auth: sign out: %v", err)
			http.Error(w, "couldn't sign out, try again", http.StatusInternalServerError)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: s.cookieName(), Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.secure(), SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
