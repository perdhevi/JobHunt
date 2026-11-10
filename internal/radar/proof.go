package radar

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"jobhunt/internal/store"
)

// ErrSkillsSettings means skills.json couldn't be loaded, so nothing can be
// marked against the profile.
var ErrSkillsSettings = errors.New("the skills settings could not be loaded")

// LeadSkill is a skill a listing asks for, and what you can show for it.
type LeadSkill struct {
	Skill string `json:"skill"`
	Proof string `json:"proof"` // proven, claimed, learning or gap
}

// ProofCounts counts pieces of proof by skill.
func ProofCounts(ev []store.Evidence) map[string]int {
	proof := map[string]int{}
	for _, e := range ev {
		proof[e.Skill]++
	}
	return proof
}

// MarkLeads returns, for each lead in order, the skills it asks for, each
// marked proven, claimed, learning or gap. observed is what was recorded per
// listing uid; a lead recorded before skills were observed is read from its
// stored title and snippet.
func (m *SkillModel) MarkLeads(leads []store.Lead, observed map[string][]string, proof map[string]int) [][]LeadSkill {
	out := make([][]LeadSkill, len(leads))
	for i, l := range leads {
		skills, ok := observed[l.UID]
		for _, u := range l.AltUIDs {
			if !ok {
				skills, ok = observed[u]
			}
		}
		if !ok {
			skills = m.Extract(l.Title + "\n" + l.Snippet)
		}
		out[i] = []LeadSkill{}
		for _, sk := range skills {
			out[i] = append(out[i], LeadSkill{sk, m.Proof(sk, proof)})
		}
	}
	return out
}

// Canonical returns the spelling skills.json uses for a skill, matched without
// regard to case, and whether the skill is tracked or in the profile at all.
// An unknown skill comes back trimmed, as given.
func (m *SkillModel) Canonical(skill string) (string, bool) {
	skill = strings.TrimSpace(skill)
	known := append([]string{}, m.names...)
	for n := range m.have {
		known = append(known, n)
	}
	for n := range m.lrn {
		known = append(known, n)
	}
	for _, n := range known {
		if n == skill {
			return n, true
		}
	}
	for _, n := range known {
		if strings.EqualFold(n, skill) {
			return n, true
		}
	}
	return skill, false
}

// LeadSkills marks the skills each lead asks for, in the order given. It
// returns an error wrapping ErrSkillsSettings when skills.json can't be
// loaded, so callers can leave the marks off instead of failing.
func (r *Radar) LeadSkills(ctx context.Context, leads []store.Lead) ([][]LeadSkill, error) {
	model, _, err := r.Skills(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSkillsSettings, err)
	}
	observed, err := r.Store.LeadSkills(ctx)
	if err != nil {
		return nil, err
	}
	ev, err := r.Store.ListEvidence(ctx)
	if err != nil {
		return nil, err
	}
	return model.MarkLeads(leads, observed, ProofCounts(ev)), nil
}

// SkillReport builds the skill-gap report over the last year's observations,
// with proof and goals filled in.
func (r *Radar) SkillReport(ctx context.Context, now time.Time) (SkillReport, error) {
	m, _, err := r.Skills(ctx)
	if err != nil {
		return SkillReport{}, fmt.Errorf("%w: %v", ErrSkillsSettings, err)
	}
	obs, err := r.Store.SkillObservations(ctx, now.AddDate(-1, 0, 0).Format("2006-01-02"))
	if err != nil {
		return SkillReport{}, err
	}
	ev, err := r.Store.ListEvidence(ctx)
	if err != nil {
		return SkillReport{}, err
	}
	goals, err := r.Store.ListGoals(ctx)
	if err != nil {
		return SkillReport{}, err
	}
	rep := m.Report(obs, now)
	m.AddProof(&rep, ev, goals)
	return rep, nil
}
