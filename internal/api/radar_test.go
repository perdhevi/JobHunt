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
