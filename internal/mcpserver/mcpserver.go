// Package mcpserver exposes the pipeline to any MCP client, over stdio or HTTP.
// Tools carry no client-specific behaviour, so Claude, ChatGPT and others all
// get the same contract, including the duplicate guard.
package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"jobhunt/internal/store"
)

const instructions = `Tracks the user's job applications.
ALWAYS call check_application before preparing or sending an application, and do not send when safeToSend is false.
After the application actually goes out, call mark_sent. Never call mark_sent for something that was only drafted.
add_application refuses duplicates, so retrying it is safe.`

// New builds a server with all tools registered.
func New(s *store.Store, version string) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "jobhunt", Version: version}, &mcp.ServerOptions{Instructions: instructions})
	h := &handlers{s: s}

	ro := &mcp.ToolAnnotations{ReadOnlyHint: true}
	notDestructive, destructive := false, true
	write := &mcp.ToolAnnotations{DestructiveHint: &notDestructive}
	// update_application can replace notes wholesale, so clients that ask
	// before destructive calls should ask here.
	overwrite := &mcp.ToolAnnotations{DestructiveHint: &destructive}

	mcp.AddTool(srv, &mcp.Tool{Name: "check_application", Annotations: ro,
		Description: "Check whether a job is already tracked or was already sent. Call this before preparing or sending anything. Pass the job link when you have one: it is the strongest match. Returns verdict (new, possible_duplicate, already_tracked, already_sent), safeToSend, and the matching records."},
		h.check)
	mcp.AddTool(srv, &mcp.Tool{Name: "add_application", Annotations: write,
		Description: "Add a job to the board, in the saved stage unless another is given. Runs the duplicate check itself: if the job exists, nothing is written and created=false with the existing record in check.matches. A possible_duplicate is also refused unless confirm_distinct=true. An exact duplicate cannot be forced here; only the user can do that in the web UI."},
		h.add)
	mcp.AddTool(srv, &mcp.Tool{Name: "mark_sent", Annotations: write,
		Description: "Record that an application was actually sent: moves it to applied and sets the date. Idempotent: if it was already sent, nothing changes and changed=false, so this also tells you a send already happened."},
		h.markSent)
	mcp.AddTool(srv, &mcp.Tool{Name: "update_application", Annotations: overwrite,
		Description: "Change fields on an application, for example stage, next step, or outcome. Only the fields you pass change. append_notes adds to the notes instead of replacing them. Setting an outcome closes the record."},
		h.update)
	mcp.AddTool(srv, &mcp.Tool{Name: "get_application", Annotations: ro,
		Description: "Fetch one application by id or reference code."},
		h.get)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_applications", Annotations: ro,
		Description: "List applications, optionally filtered by stage, track and a text query over company, role, notes, contact, source and ref."},
		h.list)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_follow_ups", Annotations: ro,
		Description: "List applications needing attention now: due or overdue next steps, and applications with no reply after 7 days."},
		h.followUps)
	mcp.AddTool(srv, &mcp.Tool{Name: "pipeline_summary", Annotations: ro,
		Description: "Counts by stage, response rate and number of follow-ups due."},
		h.summary)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_documents", Annotations: ro,
		Description: "List saved cover letter and resume versions, with ids usable in mark_sent and add_application."},
		h.documents)
	return srv
}

// HTTPHandler serves MCP over streamable HTTP. It is stateless so it survives
// server restarts and works with clients that don't keep sessions, such as
// ChatGPT behind a tunnel.
func HTTPHandler(srv *mcp.Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
}

type handlers struct{ s *store.Store }

type checkIn struct {
	Company string `json:"company" jsonschema:"company or client name"`
	Role    string `json:"role" jsonschema:"role title"`
	URL     string `json:"url,omitempty" jsonschema:"job post link, if known"`
}

func (h *handlers) check(ctx context.Context, _ *mcp.CallToolRequest, in checkIn) (*mcp.CallToolResult, store.CheckResult, error) {
	if strings.TrimSpace(in.URL) == "" && (strings.TrimSpace(in.Company) == "" || strings.TrimSpace(in.Role) == "") {
		return nil, store.CheckResult{}, errors.New("pass url, or both company and role")
	}
	r, err := h.s.Check(ctx, in.Company, in.Role, in.URL)
	return nil, r, err
}

type addIn struct {
	Company         string `json:"company" jsonschema:"company or client name"`
	Role            string `json:"role" jsonschema:"role title"`
	URL             string `json:"url,omitempty" jsonschema:"job post link"`
	Track           string `json:"track,omitempty" jsonschema:"fulltime (default) or contract"`
	Source          string `json:"source,omitempty" jsonschema:"where it was found, such as LinkedIn or Upwork"`
	Stage           string `json:"stage,omitempty" jsonschema:"saved (default), applied, screening, interview, offer or closed"`
	Comp            string `json:"comp,omitempty" jsonschema:"salary or rate"`
	Contact         string `json:"contact,omitempty" jsonschema:"contact name and title"`
	NextStep        string `json:"next_step,omitempty" jsonschema:"next action"`
	NextDate        string `json:"next_date,omitempty" jsonschema:"next action due date, YYYY-MM-DD"`
	Notes           string `json:"notes,omitempty" jsonschema:"free-form notes"`
	LetterID        string `json:"letter_id,omitempty" jsonschema:"cover letter version id from list_documents"`
	ResumeID        string `json:"resume_id,omitempty" jsonschema:"resume version id from list_documents"`
	ConfirmDistinct bool   `json:"confirm_distinct,omitempty" jsonschema:"set true only after comparing with a possible_duplicate and concluding it is a different job"`
}

func (h *handlers) add(ctx context.Context, _ *mcp.CallToolRequest, in addIn) (*mcp.CallToolResult, store.CreateResult, error) {
	mode := store.CreateStrict
	if in.ConfirmDistinct {
		mode = store.CreateAllowSimilar
	}
	r, err := h.s.Create(ctx, store.Application{Company: in.Company, Role: in.Role, URL: in.URL, Track: in.Track,
		Source: in.Source, Stage: in.Stage, Comp: in.Comp, Contact: in.Contact, NextStep: in.NextStep, NextDate: in.NextDate,
		Notes: in.Notes, LetterID: in.LetterID, ResumeID: in.ResumeID}, mode)
	return nil, r, err
}

type markSentIn struct {
	IDOrRef  string `json:"id_or_ref" jsonschema:"application id or reference code"`
	Date     string `json:"date,omitempty" jsonschema:"date sent, YYYY-MM-DD; defaults to today"`
	LetterID string `json:"letter_id,omitempty" jsonschema:"cover letter version sent"`
	ResumeID string `json:"resume_id,omitempty" jsonschema:"resume version sent"`
}

type markSentOut struct {
	Changed     bool              `json:"changed"`
	Message     string            `json:"message"`
	Application store.Application `json:"application"`
}

func (h *handlers) markSent(ctx context.Context, _ *mcp.CallToolRequest, in markSentIn) (*mcp.CallToolResult, markSentOut, error) {
	a, changed, err := h.s.MarkSent(ctx, in.IDOrRef, in.Date, in.LetterID, in.ResumeID)
	if err != nil {
		return nil, markSentOut{}, err
	}
	msg := "Marked as sent."
	if !changed {
		msg = "Already sent on " + a.Applied + ". Nothing changed. Do not send again."
	}
	return nil, markSentOut{Changed: changed, Message: msg, Application: a}, nil
}

type updateIn struct {
	IDOrRef     string  `json:"id_or_ref" jsonschema:"application id or reference code"`
	Stage       *string `json:"stage,omitempty" jsonschema:"saved, applied, screening, interview, offer or closed"`
	Outcome     *string `json:"outcome,omitempty" jsonschema:"closing reason, such as Rejected, No response, Withdrew, Declined offer, Accepted"`
	NextStep    *string `json:"next_step,omitempty" jsonschema:"next action"`
	NextDate    *string `json:"next_date,omitempty" jsonschema:"next action due date, YYYY-MM-DD, or empty to clear"`
	Comp        *string `json:"comp,omitempty" jsonschema:"salary or rate"`
	Contact     *string `json:"contact,omitempty" jsonschema:"contact name and title"`
	URL         *string `json:"url,omitempty" jsonschema:"job post link"`
	Notes       *string `json:"notes,omitempty" jsonschema:"replaces all notes"`
	AppendNotes string  `json:"append_notes,omitempty" jsonschema:"text added after the existing notes"`
}

func (h *handlers) update(ctx context.Context, _ *mcp.CallToolRequest, in updateIn) (*mcp.CallToolResult, store.Application, error) {
	a, err := h.s.Patch(ctx, in.IDOrRef, func(a *store.Application) error {
		for _, f := range []struct {
			dst *string
			src *string
		}{{&a.Stage, in.Stage}, {&a.Outcome, in.Outcome}, {&a.NextStep, in.NextStep}, {&a.NextDate, in.NextDate},
			{&a.Comp, in.Comp}, {&a.Contact, in.Contact}, {&a.URL, in.URL}, {&a.Notes, in.Notes}} {
			if f.src != nil {
				*f.dst = *f.src
			}
		}
		if in.Outcome != nil && *in.Outcome != "" {
			a.Stage = "closed"
		}
		if in.AppendNotes != "" {
			a.Notes = strings.TrimSpace(a.Notes + "\n" + in.AppendNotes)
		}
		return nil
	})
	return nil, a, err
}

type getIn struct {
	IDOrRef string `json:"id_or_ref" jsonschema:"application id or reference code"`
}

func (h *handlers) get(ctx context.Context, _ *mcp.CallToolRequest, in getIn) (*mcp.CallToolResult, store.Application, error) {
	a, err := h.s.GetApplication(ctx, in.IDOrRef)
	return nil, a, err
}

type listIn struct {
	Stage string `json:"stage,omitempty" jsonschema:"filter by stage"`
	Track string `json:"track,omitempty" jsonschema:"filter by track: fulltime or contract"`
	Query string `json:"query,omitempty" jsonschema:"text to find"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum results, default 50"`
}

type listOut struct {
	Total        int                 `json:"total"`
	Applications []store.Application `json:"applications"`
}

func (h *handlers) list(ctx context.Context, _ *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, listOut, error) {
	apps, err := h.s.ListApplications(ctx)
	if err != nil {
		return nil, listOut{}, err
	}
	q := strings.ToLower(strings.TrimSpace(in.Query))
	out := listOut{Applications: []store.Application{}}
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	for _, a := range apps {
		if (in.Stage != "" && a.Stage != in.Stage) || (in.Track != "" && a.Track != in.Track) {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(strings.Join([]string{a.Company, a.Role, a.Notes, a.Contact, a.Source, a.Ref}, " ")), q) {
			continue
		}
		out.Total++
		if len(out.Applications) < limit {
			out.Applications = append(out.Applications, a)
		}
	}
	return nil, out, nil
}

type noInput struct{}

type followUp struct {
	ID       string `json:"id"`
	Ref      string `json:"ref"`
	Company  string `json:"company"`
	Role     string `json:"role"`
	Stage    string `json:"stage"`
	NextDate string `json:"nextDate,omitempty"`
	Reason   string `json:"reason"`
}

type followUpsOut struct {
	FollowUps []followUp `json:"followUps"`
}

func (h *handlers) followUps(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, followUpsOut, error) {
	apps, err := h.s.ListApplications(ctx)
	if err != nil {
		return nil, followUpsOut{}, err
	}
	out := followUpsOut{FollowUps: []followUp{}}
	for _, a := range apps {
		if a.Due != "" {
			out.FollowUps = append(out.FollowUps, followUp{a.ID, a.Ref, a.Company, a.Role, a.Stage, a.NextDate, a.Due})
		}
	}
	sort.Slice(out.FollowUps, func(i, j int) bool { return out.FollowUps[i].Company < out.FollowUps[j].Company })
	return nil, out, nil
}

func (h *handlers) summary(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, store.Summary, error) {
	apps, err := h.s.ListApplications(ctx)
	if err != nil {
		return nil, store.Summary{}, err
	}
	return nil, store.Summarize(apps), nil
}

type documentsOut struct {
	Documents []store.Document `json:"documents"`
}

func (h *handlers) documents(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, documentsOut, error) {
	d, err := h.s.ListDocuments(ctx)
	return nil, documentsOut{Documents: d}, err
}
