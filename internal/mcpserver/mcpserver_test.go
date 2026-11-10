package mcpserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"jobhunt/internal/radar"
	"jobhunt/internal/store"
)

func session(t *testing.T) *mcp.ClientSession {
	t.Helper()
	_, cs := sessionWithStore(t)
	return cs
}

func sessionWithStore(t *testing.T) (*store.Store, *mcp.ClientSession) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	if _, err := New(s, "test").Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return s, cs
}

// callErr calls a tool that should fail and returns the error text.
func callErr(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return err.Error()
	}
	if !res.IsError {
		t.Fatalf("%s should have failed: %+v", name, res.StructuredContent)
	}
	b, _ := json.Marshal(res.Content)
	return string(b)
}

func call[T any](t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) T {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("%s: %+v", name, res.Content[0])
	}
	var out T
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// The workflow every harness is told to follow, including the retries that
// would otherwise cause a double send.
func TestSendWorkflowNeverDoubles(t *testing.T) {
	cs := session(t)
	job := map[string]any{"company": "Acme", "role": "SRE", "url": "https://acme.com/jobs/1"}

	if c := call[store.CheckResult](t, cs, "check_application", job); c.Verdict != store.VerdictNew || !c.SafeToSend {
		t.Fatalf("%+v", c)
	}
	added := call[store.CreateResult](t, cs, "add_application", job)
	if !added.Created {
		t.Fatalf("%+v", added)
	}
	if again := call[store.CreateResult](t, cs, "add_application", job); again.Created || len(again.Check.Matches) != 1 {
		t.Fatalf("retry must return the existing record: %+v", again)
	}

	ref := map[string]any{"id_or_ref": added.Application.Ref}
	if m := call[markSentOut](t, cs, "mark_sent", ref); !m.Changed {
		t.Fatalf("%+v", m)
	}
	if m := call[markSentOut](t, cs, "mark_sent", ref); m.Changed {
		t.Fatalf("second mark_sent must not change anything: %+v", m)
	}
	if c := call[store.CheckResult](t, cs, "check_application", job); c.Verdict != store.VerdictAlreadySent || c.SafeToSend {
		t.Fatalf("%+v", c)
	}
}

func TestPossibleDuplicateNeedsConfirmation(t *testing.T) {
	cs := session(t)
	call[store.CreateResult](t, cs, "add_application", map[string]any{"company": "Acme", "role": "Support Engineer APAC"})
	similar := map[string]any{"company": "Acme Inc", "role": "Support Engineer"}
	if r := call[store.CreateResult](t, cs, "add_application", similar); r.Created {
		t.Fatalf("%+v", r)
	}
	similar["confirm_distinct"] = true
	if r := call[store.CreateResult](t, cs, "add_application", similar); !r.Created {
		t.Fatalf("%+v", r)
	}
}

func TestUpdateOutcomeCloses(t *testing.T) {
	cs := session(t)
	a := call[store.CreateResult](t, cs, "add_application", map[string]any{"company": "Acme", "role": "SRE", "stage": "applied"})
	got := call[store.Application](t, cs, "update_application", map[string]any{
		"id_or_ref": a.Application.ID, "outcome": "Rejected", "append_notes": "Form email."})
	if got.Stage != "closed" || got.Notes != "Form email." {
		t.Fatalf("%+v", got)
	}
}

func TestSkipApplicationBlocksSending(t *testing.T) {
	cs := session(t)
	job := map[string]any{"company": "Acme", "role": "SRE", "url": "https://acme.com/jobs/2"}
	a := call[store.CreateResult](t, cs, "add_application", job)
	got := call[store.Application](t, cs, "skip_application", map[string]any{
		"id_or_ref": a.Application.Ref, "reason": "timezone", "details": "US Pacific hours"})
	if got.Stage != "skipped" || got.SkipReason != "timezone" || got.SkipNote != "US Pacific hours" {
		t.Fatalf("%+v", got)
	}
	c := call[store.CheckResult](t, cs, "check_application", job)
	if c.Verdict != store.VerdictSkipped || c.SafeToSend || c.Matches[0].SkipReason != "timezone" {
		t.Fatalf("%+v", c)
	}
	res, _ := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "skip_application",
		Arguments: map[string]any{"id_or_ref": a.Application.Ref, "reason": "other"}})
	if !res.IsError {
		t.Error("other without details should be refused")
	}
}

func TestLeadTools(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.SaveLeads(ctx, []store.Lead{
		{UID: "himalayas:a", Source: "himalayas", SourceName: "Himalayas", Title: "LLM Engineer", Company: "Acme", URL: "https://himalayas.app/a", Score: 20},
		{UID: "jobicy:b", Source: "jobicy", SourceName: "Jobicy", Title: "Support Engineer", Company: "Lumen", URL: "https://jobicy.com/b", Score: 9},
	}, "2026-11-10"); err != nil {
		t.Fatal(err)
	}
	st, ct := mcp.NewInMemoryTransports()
	if _, err := New(s, "test").Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	if l := call[leadsOut](t, cs, "list_leads", map[string]any{}); l.Total != 2 || l.Leads[0].UID != "himalayas:a" {
		t.Fatalf("%+v", l)
	}
	saved := call[store.CreateResult](t, cs, "save_lead", map[string]any{"lead_id": "himalayas:a"})
	if !saved.Created || saved.Application.Stage != "saved" || saved.Application.Source != "Himalayas" {
		t.Fatalf("%+v", saved)
	}
	skipped := call[store.CreateResult](t, cs, "skip_lead", map[string]any{"lead_id": "jobicy:b", "reason": "seniority", "details": "junior role"})
	if !skipped.Created || skipped.Application.Stage != "skipped" || skipped.Application.SkipNote != "junior role" {
		t.Fatalf("%+v", skipped)
	}
	if l := call[leadsOut](t, cs, "list_leads", map[string]any{}); l.Total != 0 {
		t.Fatalf("acted-on leads still listed: %+v", l)
	}
	if l := call[leadsOut](t, cs, "list_leads", map[string]any{"include_tracked": true}); l.Total != 2 || l.Leads[1].Application.Stage != "skipped" {
		t.Fatalf("%+v", l)
	}
}

// With the default skills.json, Python and Swift are "have", Go is
// "learning", and Docker and Kubernetes are gaps.
func TestSkillTools(t *testing.T) {
	s, cs := sessionWithStore(t)
	ctx := context.Background()
	if err := s.SaveLeads(ctx, []store.Lead{{UID: "wwr:y", Source: "wwr", SourceName: "We Work Remotely", Title: "Platform Engineer",
		Company: "Lumen", URL: "https://weworkremotely.com/remote-jobs/y", Score: 9, Snippet: "Python, Docker and Kubernetes"}}, "2026-11-10"); err != nil {
		t.Fatal(err)
	}

	py := call[savedEvidenceOut](t, cs, "add_skill_evidence", map[string]any{"skill": "python", "kind": "repo", "title": "jobhunt",
		"url": "https://github.com/perdhevi/JobHunt", "note": "Go and Python tooling"})
	if py.Evidence.Skill != "Python" || py.Status != "have" || py.Message != "Proof recorded." {
		t.Errorf("skill should be matched to its tracked spelling: %+v", py)
	}
	dk := call[savedEvidenceOut](t, cs, "add_skill_evidence", map[string]any{"skill": "Docker", "kind": "project", "title": "compose stack"})
	if dk.Status != "gap" || !strings.Contains(dk.Message, "Docker is gap in the profile") {
		t.Errorf("proof for a gap should say it stays a gap: %+v", dk)
	}
	if typo := call[savedEvidenceOut](t, cs, "add_skill_evidence", map[string]any{"skill": "Pythn", "kind": "course", "title": "x"}); !strings.Contains(typo.Message, "isn't a skill") {
		t.Errorf("an unknown skill should be flagged: %+v", typo)
	}
	if msg := callErr(t, cs, "add_skill_evidence", map[string]any{"skill": "Go", "kind": "rumour", "title": "x"}); !strings.Contains(msg, "kind must be one of") {
		t.Errorf("bad kind: %s", msg)
	}
	if msg := callErr(t, cs, "add_skill_evidence", map[string]any{"skill": "Go", "kind": "repo", "title": "x", "url": "javascript:alert(1)"}); !strings.Contains(msg, "web address") {
		t.Errorf("non-web link: %s", msg)
	}

	l := call[leadsOut](t, cs, "list_leads", map[string]any{})
	got := map[string]string{}
	for _, sk := range l.Leads[0].Skills {
		got[sk.Skill] = sk.Proof
	}
	if got["Python"] != "proven" || got["Docker"] != "gap" || got["Kubernetes"] != "gap" {
		t.Errorf("lead skill marks: %v", got)
	}
	if l.Leads[0].Snippet != "" {
		t.Error("snippet should be left out of list_leads")
	}

	if g := call[setGoalOut](t, cs, "set_skill_goal", map[string]any{"skill": "kubernetes", "goal": "k3s deploy", "due": "2026-12-01"}); g.Goal.Skill != "Kubernetes" || g.Message != "Goal set." {
		t.Errorf("%+v", g)
	}
	if msg := callErr(t, cs, "set_skill_goal", map[string]any{"skill": "Go", "goal": "x", "due": "soon"}); !strings.Contains(msg, "YYYY-MM-DD") {
		t.Errorf("bad due date: %s", msg)
	}
	rep := call[radar.SkillReport](t, cs, "skill_gaps", map[string]any{})
	mine := map[string]radar.SkillRow{}
	for _, r := range rep.Mine {
		mine[r.Skill] = r
	}
	if rep.Proven != 1 || mine["Kubernetes"].Goal != "k3s deploy" || mine["Docker"].Proof != 1 || mine["Docker"].Status != "gap" {
		t.Errorf("report: proven %d, mine %+v", rep.Proven, rep.Mine)
	}

	ev := call[evidenceOut](t, cs, "list_skill_evidence", map[string]any{"skill": "docker"})
	if len(ev.Evidence) != 1 || len(ev.Kinds) == 0 {
		t.Fatalf("%+v", ev)
	}
	up := call[savedEvidenceOut](t, cs, "update_skill_evidence", map[string]any{"id": dk.Evidence.ID, "title": "compose stack on a VPS"})
	if up.Evidence.Title != "compose stack on a VPS" || up.Evidence.Kind != "project" || up.Evidence.CreatedAt != dk.Evidence.CreatedAt {
		t.Errorf("only the title should change: %+v", up.Evidence)
	}
	if msg := callErr(t, cs, "update_skill_evidence", map[string]any{"id": "nope", "title": "x"}); !strings.Contains(msg, "not found") {
		t.Errorf("missing id: %s", msg)
	}
	if ev := call[evidenceOut](t, cs, "list_skill_evidence", map[string]any{}); len(ev.Evidence) != 3 || len(ev.Goals) != 1 {
		t.Errorf("all proof: %+v", ev)
	}
	if g := call[setGoalOut](t, cs, "set_skill_goal", map[string]any{"skill": "Kubernetes", "goal": ""}); g.Message != "Goal removed." {
		t.Errorf("%+v", g)
	}
}

// Deleting stays in the UI: no MCP tool can remove a record.
func TestNoDeleteTools(t *testing.T) {
	res, err := session(t).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		if strings.Contains(tool.Name, "delete") || strings.Contains(tool.Name, "remove") {
			t.Errorf("unexpected delete tool %s", tool.Name)
		}
	}
}
