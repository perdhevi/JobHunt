package store

import (
	"context"
	"errors"
	"testing"
)

func TestEvidenceRules(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	for name, e := range map[string]Evidence{
		"no title":     {Skill: "Docker", Kind: "repo"},
		"unknown kind": {Skill: "Docker", Kind: "vibes", Title: "x"},
		"script link":  {Skill: "Docker", Kind: "repo", Title: "x", URL: "javascript:alert(1)"},
		"bad date":     {Skill: "Docker", Kind: "repo", Title: "x", Date: "last week"},
	} {
		if _, err := s.SaveEvidence(ctx, e); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	e, err := s.SaveEvidence(ctx, Evidence{Skill: " CI/CD ", Kind: "repo", Title: "GitHub Actions on JobHunt", URL: "github.com/perdhevi/JobHunt/actions", Date: "2026-11-12"})
	if err != nil {
		t.Fatal(err)
	}
	if e.ID == "" || e.Skill != "CI/CD" || e.URL != "https://github.com/perdhevi/JobHunt/actions" {
		t.Errorf("%+v", e)
	}
	e.Note = "runs go test and docker build on every PR"
	if got, err := s.SaveEvidence(ctx, e); err != nil || got.CreatedAt != e.CreatedAt || got.Note != e.Note {
		t.Errorf("update: %+v %v", got, err)
	}
	if _, err := s.SaveEvidence(ctx, Evidence{ID: "nope", Skill: "x", Kind: "repo", Title: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update of a missing id: %v", err)
	}
	if list, _ := s.ListEvidence(ctx); len(list) != 1 {
		t.Errorf("%+v", list)
	}
	if err := s.DeleteEvidence(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListEvidence(ctx); len(list) != 0 {
		t.Errorf("not deleted: %+v", list)
	}

	if _, err := s.SetGoal(ctx, SkillGoal{Skill: "Kubernetes", Goal: "Deploy jobhunt on k3s", Due: "2026-12-01"}); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.ListGoals(ctx); len(g) != 1 || g[0].Due != "2026-12-01" {
		t.Errorf("%+v", g)
	}
	s.SetGoal(ctx, SkillGoal{Skill: "Kubernetes"}) // empty goal clears it
	if g, _ := s.ListGoals(ctx); len(g) != 0 {
		t.Errorf("goal not cleared: %+v", g)
	}
}
