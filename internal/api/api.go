// Package api is the REST interface behind the React board.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"jobhunt/internal/config"
	"jobhunt/internal/store"
)

// Server serves the API, the embedded UI and the local MCP endpoint.
type Server struct {
	Store  *store.Store
	Config config.Config
	UI     fs.FS
	MCP    http.Handler
}

// Handler assembles the routes for the loopback listener.
func (s *Server) Handler() http.Handler {
	return localOnly(s.Routes())
}

// Routes is Handler without the loopback checks, for a listener that puts its
// own login in front.
func (s *Server) Routes() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/applications", s.listApps)
	m.HandleFunc("POST /api/applications", s.createApp)
	m.HandleFunc("POST /api/applications/check", s.checkApp)
	m.HandleFunc("PUT /api/applications/{id}", s.updateApp)
	m.HandleFunc("PATCH /api/applications/{id}/stage", s.moveApp)
	m.HandleFunc("DELETE /api/applications/{id}", s.deleteApp)
	m.HandleFunc("GET /api/documents", s.listDocs)
	m.HandleFunc("POST /api/documents", s.saveDoc)
	m.HandleFunc("PUT /api/documents/{id}", s.saveDoc)
	m.HandleFunc("DELETE /api/documents/{id}", s.deleteDoc)
	m.HandleFunc("POST /api/files", s.uploadFile)
	m.HandleFunc("GET /api/files/{id}", s.getFile)
	m.HandleFunc("GET /api/settings", s.getSettings)
	m.HandleFunc("PUT /api/settings", s.putSettings)
	// The remote listener answers this itself, so the UI knows to offer sign-out.
	m.HandleFunc("GET /api/session", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"remote": false})
	})
	if s.MCP != nil {
		m.Handle("/mcp", s.MCP)
	}
	if s.UI != nil {
		m.Handle("/", spa(s.UI))
	}
	return m
}

// localOnly blocks DNS rebinding and cross-site requests. Without it, any web
// page open in the browser could read or change this loopback server.
func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackHost(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" {
			u, err := url.Parse(o)
			if err != nil || !isLoopbackHost(u.Host) {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isLoopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// csp allows only the board's own code, plus the Google Fonts stylesheet and
// font files that index.html loads. It's the backstop if a stored value ever
// reaches the page as markup or a link.
const csp = "default-src 'self'; script-src 'self'; style-src 'self' https://fonts.googleapis.com; " +
	"font-src https://fonts.gstatic.com; img-src 'self' data: blob:; connect-src 'self'; " +
	"object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// spa serves built assets and falls back to index.html.
func spa(ui fs.FS) http.Handler {
	files := http.FileServerFS(ui)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if _, err := fs.Stat(ui, p); err != nil {
				r = r.Clone(r.Context())
				r.URL.Path = "/"
			}
		}
		files.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, store.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, store.ErrConflict):
		status = http.StatusConflict
	case errors.Is(err, store.ErrInvalid):
		status = http.StatusBadRequest
	default:
		log.Printf("api error: %v", err)
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// readJSON also enforces a JSON content type, which cross-site forms can't send.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "expected application/json"})
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(v); err != nil {
		writeErr(w, fmt.Errorf("%w: %v", store.ErrInvalid, err))
		return false
	}
	return true
}

func (s *Server) listApps(w http.ResponseWriter, r *http.Request) {
	apps, err := s.Store.ListApplications(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apps)
}

func (s *Server) checkApp(w http.ResponseWriter, r *http.Request) {
	var in struct{ Company, Role, URL string }
	if !readJSON(w, r, &in) {
		return
	}
	res, err := s.Store.Check(r.Context(), in.Company, in.Role, in.URL)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// createApp returns 409 with the check result on a duplicate. The UI shows it
// and may retry with ?force=1 once the person confirms.
func (s *Server) createApp(w http.ResponseWriter, r *http.Request) {
	var a store.Application
	if !readJSON(w, r, &a) {
		return
	}
	mode := store.CreateStrict
	if r.URL.Query().Get("force") == "1" {
		mode = store.CreateForce
	}
	res, err := s.Store.Create(r.Context(), a, mode)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !res.Created {
		writeJSON(w, http.StatusConflict, res)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

func (s *Server) updateApp(w http.ResponseWriter, r *http.Request) {
	var a store.Application
	if !readJSON(w, r, &a) {
		return
	}
	a.ID = r.PathValue("id")
	got, err := s.Store.Update(r.Context(), a, r.URL.Query().Get("force") == "1")
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (s *Server) moveApp(w http.ResponseWriter, r *http.Request) {
	var in struct{ Stage, Outcome string }
	if !readJSON(w, r, &in) {
		return
	}
	got, err := s.Store.Patch(r.Context(), r.PathValue("id"), func(a *store.Application) error {
		a.Stage, a.Outcome = in.Stage, in.Outcome
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (s *Server) deleteApp(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteApplication(r.Context(), r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listDocs(w http.ResponseWriter, r *http.Request) {
	d, err := s.Store.ListDocuments(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) saveDoc(w http.ResponseWriter, r *http.Request) {
	var d store.Document
	if !readJSON(w, r, &d) {
		return
	}
	d.ID = r.PathValue("id")
	got, err := s.Store.SaveDocument(r.Context(), d)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (s *Server) deleteDoc(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteDocument(r.Context(), r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) uploadFile(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, store.MaxFileSize+1<<20)
	f, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, fmt.Errorf("%w: file is missing or over 20 MB", store.ErrInvalid))
		return
	}
	defer f.Close()
	meta, err := s.Store.SaveFile(r.Context(), hdr.Filename, f)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, meta)
}

func (s *Server) getFile(w http.ResponseWriter, r *http.Request) {
	f, path, err := s.Store.OpenFile(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	// Uploaded content is served as inert: PDFs inline, text as plain text.
	ct := f.Type
	if strings.HasPrefix(ct, "text/") {
		ct = "text/plain; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": f.Name}))
	http.ServeFile(w, r, path)
}

type settingsDTO struct {
	Email      string `json:"email"`
	DBPath     string `json:"dbPath"`     // saved path, applies from next start
	ActivePath string `json:"activePath"` // path in use right now
	ConfigPath string `json:"configPath"`
	Docker     bool   `json:"docker"`
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	email, err := s.Store.GetSetting(r.Context(), store.SettingEmail)
	if err != nil {
		writeErr(w, err)
		return
	}
	cfg, err := config.Load()
	if err != nil {
		writeErr(w, err)
		return
	}
	cp, err := config.Path()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settingsDTO{
		Email:      email,
		DBPath:     cfg.DBPath,
		ActivePath: config.HostPath(s.Store.Path()),
		ConfigPath: cp,
		Docker:     os.Getenv(config.EnvHostDrive) != "",
	})
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email  *string `json:"email"`
		DBPath *string `json:"dbPath"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.Email != nil {
		if err := s.Store.SetSetting(r.Context(), store.SettingEmail, strings.TrimSpace(*in.Email)); err != nil {
			writeErr(w, err)
			return
		}
	}
	if in.DBPath != nil {
		p, err := config.ValidateDBPath(*in.DBPath)
		if err != nil {
			writeErr(w, fmt.Errorf("%w: %v", store.ErrInvalid, err))
			return
		}
		// The file alone, so the password and token from the environment
		// aren't written into it.
		cfg, err := config.LoadFile()
		if err != nil {
			writeErr(w, err)
			return
		}
		cfg.DBPath = p
		if err := config.Save(cfg); err != nil {
			writeErr(w, err)
			return
		}
	}
	s.getSettings(w, r)
}
