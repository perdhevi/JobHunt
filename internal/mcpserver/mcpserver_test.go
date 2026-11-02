package mcpserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"jobhunt/internal/store"
)

func session(t *testing.T) *mcp.ClientSession {
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
	return cs
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
