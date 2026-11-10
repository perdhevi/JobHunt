package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"jobhunt/internal/radar"
	"jobhunt/internal/store"
)

type radarStateDTO struct {
	radar.Status
	Leads []leadDTO `json:"leads"`
}

// leadSkill is a skill a listing asks for, and what you can show for it.
type leadSkill struct {
	Skill string `json:"skill"`
	Proof string `json:"proof"` // proven, claimed, learning or gap
}

type leadDTO struct {
	store.Lead
	Skills []leadSkill `json:"skills"`
}

// radarState is everything the Radar view lists: run status and stored leads,
// each with the board record it's on, if any, and the skills it asks for.
func (s *Server) radarState(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, err := s.Radar.Status(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	leads, err := s.Store.ListLeads(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := radarStateDTO{Status: st, Leads: make([]leadDTO, len(leads))}
	for i, l := range leads {
		out.Leads[i] = leadDTO{Lead: l, Skills: []leadSkill{}}
	}
	// Skill marks are a convenience: settings that don't parse leave them off
	// (the header already reports the problem) instead of failing the list.
	model, _, err := s.Radar.Skills(ctx)
	if err == nil {
		observed, err := s.Store.LeadSkills(ctx)
		if err != nil {
			writeErr(w, err)
			return
		}
		ev, err := s.Store.ListEvidence(ctx)
		if err != nil {
			writeErr(w, err)
			return
		}
		proof := map[string]int{}
		for _, e := range ev {
			proof[e.Skill]++
		}
		for i := range out.Leads {
			l := &out.Leads[i].Lead
			skills, ok := observed[l.UID]
			for _, u := range l.AltUIDs {
				if !ok {
					skills, ok = observed[u]
				}
			}
			if !ok { // recorded before skills were observed: use what's stored
				skills = model.Extract(l.Title + "\n" + l.Snippet)
			}
			for _, sk := range skills {
				out.Leads[i].Skills = append(out.Leads[i].Skills, leadSkill{sk, model.Proof(sk, proof)})
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// radarRun starts a scan in the background; the page polls radarState.
func (s *Server) radarRun(w http.ResponseWriter, r *http.Request) {
	var in struct{}
	if !readJSON(w, r, &in) { // like every write: a cross-site form can't send JSON
		return
	}
	if s.Radar.Running() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": radar.ErrRunning.Error()})
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		if _, err := s.Radar.Run(ctx, radar.Options{}); err != nil && !errors.Is(err, radar.ErrRunning) {
			log.Printf("radar: %v", err)
		}
	}()
	// Give the run a moment to claim the lock, so the reply already says running.
	for i := 0; i < 20 && !s.Radar.Running(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	s.radarState(w, r)
}

// radarAct saves a lead to the board, marks it applied, or skips it. 409 with
// the check result means the job was already on the board; the lead is then
// linked to that record.
func (s *Server) radarAct(w http.ResponseWriter, r *http.Request) {
	var in struct {
		UID        string `json:"uid"`
		Stage      string `json:"stage"`
		SkipReason string `json:"skipReason"`
		SkipNote   string `json:"skipNote"`
		Force      bool   `json:"force"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	// Acting on a lead is a deliberate choice, so another role at the same
	// company doesn't block it; only the same job does, unless forced.
	mode := store.CreateAllowSimilar
	if in.Force {
		mode = store.CreateForce
	}
	res, err := s.Store.ActOnLead(r.Context(), in.UID, store.LeadAction{Stage: in.Stage, SkipReason: in.SkipReason, SkipNote: in.SkipNote}, mode)
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

func (s *Server) radarSkills(w http.ResponseWriter, r *http.Request) {
	m, _, err := s.Radar.Skills(r.Context())
	if err != nil {
		writeErr(w, fmt.Errorf("%w: %v", store.ErrInvalid, err))
		return
	}
	ctx := r.Context()
	now := time.Now()
	obs, err := s.Store.SkillObservations(ctx, now.AddDate(-1, 0, 0).Format("2006-01-02"))
	if err != nil {
		writeErr(w, err)
		return
	}
	ev, err := s.Store.ListEvidence(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	goals, err := s.Store.ListGoals(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	rep := m.Report(obs, now)
	m.AddProof(&rep, ev, goals)
	writeJSON(w, http.StatusOK, rep)
}

type evidenceDTO struct {
	Evidence []store.Evidence  `json:"evidence"`
	Goals    []store.SkillGoal `json:"goals"`
	Kinds    []string          `json:"kinds"`
}

func (s *Server) listEvidence(w http.ResponseWriter, r *http.Request) {
	ev, err := s.Store.ListEvidence(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	goals, err := s.Store.ListGoals(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, evidenceDTO{Evidence: ev, Goals: goals, Kinds: store.EvidenceKinds})
}

func (s *Server) saveEvidence(w http.ResponseWriter, r *http.Request) {
	var e store.Evidence
	if !readJSON(w, r, &e) {
		return
	}
	e.ID = r.PathValue("id")
	got, err := s.Store.SaveEvidence(r.Context(), e)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (s *Server) deleteEvidence(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteEvidence(r.Context(), r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// setGoal takes the skill in the body: names such as "CI/CD" hold slashes.
func (s *Server) setGoal(w http.ResponseWriter, r *http.Request) {
	var g store.SkillGoal
	if !readJSON(w, r, &g) {
		return
	}
	got, err := s.Store.SetGoal(r.Context(), g)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

type radarSettingsDTO struct {
	Config         string `json:"config"`
	Skills         string `json:"skills"`
	DefaultConfig  string `json:"defaultConfig"`
	DefaultSkills  string `json:"defaultSkills"`
	ConfigError    string `json:"configError,omitempty"`
	SkillsError    string `json:"skillsError,omitempty"`
	ConfigIsCustom bool   `json:"configIsCustom"`
	SkillsIsCustom bool   `json:"skillsIsCustom"`
}

func (s *Server) radarSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := radarSettingsDTO{DefaultConfig: radar.DefaultConfig, DefaultSkills: radar.DefaultSkills}
	var err error
	if out.Config, err = s.Store.GetSetting(ctx, radar.SettingConfig); err != nil {
		writeErr(w, err)
		return
	}
	if out.Skills, err = s.Store.GetSetting(ctx, radar.SettingSkills); err != nil {
		writeErr(w, err)
		return
	}
	out.ConfigIsCustom, out.SkillsIsCustom = out.Config != "", out.Skills != ""
	if !out.ConfigIsCustom {
		out.Config = radar.DefaultConfig
	}
	if !out.SkillsIsCustom {
		out.Skills = radar.DefaultSkills
	}
	if _, _, err := radar.ParseConfig(out.Config); err != nil {
		out.ConfigError = err.Error()
	}
	if _, err := radar.ParseSkills(out.Skills); err != nil {
		out.SkillsError = err.Error()
	}
	writeJSON(w, http.StatusOK, out)
}

// putRadarSettings saves config.json or skills.json text after checking it
// parses and every pattern compiles. An empty string goes back to the default.
func (s *Server) putRadarSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Config *string `json:"config"`
		Skills *string `json:"skills"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	ctx := r.Context()
	if in.Config != nil && strings.TrimSpace(*in.Config) != "" {
		if _, _, err := radar.ParseConfig(*in.Config); err != nil {
			writeErr(w, fmt.Errorf("%w: %v", store.ErrInvalid, err))
			return
		}
	}
	if in.Skills != nil && strings.TrimSpace(*in.Skills) != "" {
		if _, err := radar.ParseSkills(*in.Skills); err != nil {
			writeErr(w, fmt.Errorf("%w: %v", store.ErrInvalid, err))
			return
		}
	}
	for key, v := range map[string]*string{radar.SettingConfig: in.Config, radar.SettingSkills: in.Skills} {
		if v == nil {
			continue
		}
		if err := s.Store.SetSetting(ctx, key, strings.TrimSpace(*v)); err != nil {
			writeErr(w, err)
			return
		}
	}
	s.radarSettings(w, r)
}

func (s *Server) radarAddCompany(w http.ResponseWriter, r *http.Request) {
	var in struct{ Ref, Name string }
	if !readJSON(w, r, &in) {
		return
	}
	res, err := s.Radar.AddCompany(r.Context(), in.Ref, in.Name)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}
