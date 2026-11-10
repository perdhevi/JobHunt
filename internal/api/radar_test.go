package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jobhunt/internal/radar"
	"jobhunt/internal/store"
)

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "http://127.0.0.1:8787"+path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestDragToSkippedNeedsReason(t *testing.T) {
	srv, _ := newServer(t)
	h := srv.Handler()
	res, err := srv.Store.Create(context.Background(), store.Application{Company: "Acme", Role: "SRE"}, store.CreateStrict)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/applications/" + res.Application.ID + "/stage"
	if w := do(t, h, "PATCH", path, `{"stage":"skipped","skipReason":"other"}`); w.Code != http.StatusBadRequest {
		t.Errorf("other without details: %d %s", w.Code, w.Body)
	}
	w := do(t, h, "PATCH", path, `{"stage":"skipped","skipReason":"rate","skipNote":"max $35/h"}`)
	var a store.Application
	json.Unmarshal(w.Body.Bytes(), &a)
	if w.Code != http.StatusOK || a.SkipReason != "rate" || a.SkipNote != "max $35/h" {
		t.Errorf("%d %+v", w.Code, a)
	}
}

func TestRadarActOnLead(t *testing.T) {
	srv, _ := newServer(t)
	srv.Radar = &radar.Radar{Store: srv.Store}
	h := srv.Handler()
	ctx := context.Background()
	srv.Store.SaveLeads(ctx, []store.Lead{{UID: "wwr:x", Source: "wwr", SourceName: "We Work Remotely", Title: "Support Engineer",
		Company: "Lumen", URL: "https://weworkremotely.com/remote-jobs/x", Score: 9}}, "2026-11-10")

	w := do(t, h, "POST", "/api/radar/act", `{"uid":"wwr:x","stage":"skipped","skipReason":"timezone","skipNote":"US Pacific"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	// Acting again on the same job is a conflict that names the record.
	w = do(t, h, "POST", "/api/radar/act", `{"uid":"wwr:x","stage":"saved"}`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"verdict":"skipped"`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var st radarStateDTO
	json.Unmarshal(do(t, h, "GET", "/api/radar", "").Body.Bytes(), &st)
	if len(st.Leads) != 1 || st.Leads[0].Application == nil || st.Leads[0].Application.SkipNote != "US Pacific" {
		t.Errorf("%+v", st.Leads)
	}
	if w := do(t, h, "PUT", "/api/radar/settings", `{"config":"{\"sources\":{\"nope\":{}}}"}`); w.Code != http.StatusBadRequest {
		t.Errorf("bad config saved: %d", w.Code)
	}
}

func TestLeadsShowSkillProof(t *testing.T) {
	srv, _ := newServer(t)
	srv.Radar = &radar.Radar{Store: srv.Store}
	h := srv.Handler()
	ctx := context.Background()
	srv.Store.SaveLeads(ctx, []store.Lead{{UID: "wwr:y", Source: "wwr", SourceName: "We Work Remotely", Title: "Platform Engineer",
		Company: "Lumen", URL: "https://weworkremotely.com/remote-jobs/y", Score: 9, Snippet: "Python, Docker and Kubernetes"}}, "2026-11-10")
	if w := do(t, h, "POST", "/api/radar/evidence", `{"skill":"Python","kind":"repo","title":"jobhunt","url":"https://github.com/perdhevi/JobHunt"}`); w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := do(t, h, "PUT", "/api/radar/goal", `{"skill":"CI/CD","goal":"Actions workflow","due":"2026-12-01"}`); w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var st radarStateDTO
	json.Unmarshal(do(t, h, "GET", "/api/radar", "").Body.Bytes(), &st)
	got := map[string]string{}
	for _, s := range st.Leads[0].Skills {
		got[s.Skill] = s.Proof
	}
	if got["Python"] != "proven" || got["Kubernetes"] != "gap" || got["Docker"] != "gap" {
		t.Errorf("skill marks: %v", got)
	}
	var rep radar.SkillReport
	json.Unmarshal(do(t, h, "GET", "/api/radar/skills", "").Body.Bytes(), &rep)
	found := false
	for _, r := range rep.Mine {
		found = found || (r.Skill == "CI/CD" && r.Goal == "Actions workflow")
	}
	if !found || rep.Proven != 1 {
		t.Errorf("report: proven %d, mine %+v", rep.Proven, rep.Mine)
	}
}
