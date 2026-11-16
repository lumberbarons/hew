package daemon

import (
	"net/http"

	"github.com/lumberbarons/hew/pr"
	"github.com/lumberbarons/hew/tracker"
)

type createIn struct {
	Title          string   `json:"title"`
	Type           string   `json:"type"`
	Priority       string   `json:"priority"`
	Areas          []string `json:"areas"`
	Body           bodyIn   `json:"body"`
	Parent         int      `json:"parent"`
	BlockedBy      []int    `json:"blockedBy"`
	DiscoveredFrom int      `json:"discoveredFrom"`
}

type epicIn struct {
	Title    string `json:"title"`
	Children []int  `json:"children"`
	Body     bodyIn `json:"body"`
}

// dryRun reports whether the caller asked to validate without writing.
func dryRun(r *http.Request) bool { return r.URL.Query().Get("dryRun") == "1" }

type checkOut struct {
	OK       bool         `json:"ok"`
	Findings []findingOut `json:"findings"`
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	var in createIn
	if err := decode(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	tr := s.acting(r)
	req := tracker.CreateRequest{
		Title: in.Title, Type: in.Type, Priority: in.Priority, Areas: in.Areas,
		Body: in.Body.body(in.DiscoveredFrom), Parent: in.Parent,
		BlockedBy: in.BlockedBy, DiscoveredFrom: in.DiscoveredFrom,
	}
	if dryRun(r) {
		// CheckCreate runs the whole write path's validation, references
		// included, and writes nothing: the form's live validation.
		if err := tr.CheckCreate(r.Context(), req); err != nil {
			writeErr(w, err)
			return
		}
		composed, err := tracker.ComposeBody(req.Body)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, checkOut{OK: true, Findings: findingsOut(composed.Findings)})
		return
	}
	res, err := tr.Create(r.Context(), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, createFrom(res))
}

func (s *Server) handleCreateEpic(w http.ResponseWriter, r *http.Request) {
	var in epicIn
	if err := decode(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	tr := s.acting(r)
	req := tracker.CreateEpicRequest{Title: in.Title, Children: in.Children, Body: in.Body.body(0)}
	if dryRun(r) {
		if err := tr.CheckCreateEpic(r.Context(), req); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, checkOut{OK: true, Findings: []findingOut{}})
		return
	}
	res, err := tr.CreateEpic(r.Context(), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, createFrom(res))
}

type editIn struct {
	Priority    *string  `json:"priority"`
	Type        *string  `json:"type"`
	Title       *string  `json:"title"`
	Body        *string  `json:"body"`
	AddAreas    []string `json:"addAreas"`
	RemoveAreas []string `json:"removeAreas"`
	Parent      *int     `json:"parent"`
	NoParent    bool     `json:"noParent"`
	AllowClosed bool     `json:"allowClosed"`
}

func (s *Server) handleEdit(w http.ResponseWriter, r *http.Request) {
	n, err := number(r, "n")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in editIn
	if err := decode(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	res, err := s.acting(r).Edit(r.Context(), tracker.EditRequest{
		Issue: n, Priority: in.Priority, Type: in.Type, Title: in.Title, Body: in.Body,
		AddAreas: in.AddAreas, RemoveAreas: in.RemoveAreas, Parent: in.Parent,
		NoParent: in.NoParent, AllowClosed: in.AllowClosed,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"applied": orEmpty(res.Applied), "parentNoop": res.ParentNoop})
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	n, err := number(r, "n")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		Priority string `json:"priority"`
		Force    bool   `json:"force"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	res, err := s.acting(r).Start(r.Context(), n, tracker.StartOptions{Priority: in.Priority, Force: in.Force})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"issue": res.Issue, "claimant": res.Claimant})
}

func (s *Server) handleRelease(w http.ResponseWriter, r *http.Request) {
	n, err := number(r, "n")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		Force bool `json:"force"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.acting(r).Release(r.Context(), n, tracker.ReleaseOptions{Force: in.Force}); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"released": true})
}

func (s *Server) handleClose(w http.ResponseWriter, r *http.Request) {
	n, err := number(r, "n")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		Reason      string `json:"reason"`
		Completed   bool   `json:"completed"`
		DuplicateOf int    `json:"duplicateOf"`
		Force       bool   `json:"force"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	res, err := s.acting(r).Close(r.Context(), n, tracker.CloseOptions{
		Reason: in.Reason, Completed: in.Completed, DuplicateOf: in.DuplicateOf, Force: in.Force,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stateReason": res.StateReason})
}

func (s *Server) handleReopen(w http.ResponseWriter, r *http.Request) {
	n, err := number(r, "n")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	res, err := s.acting(r).Reopen(r.Context(), n, tracker.ReopenOptions{Reason: in.Reason})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"reopened": res.Reopened, "releasedLabel": res.ReleasedLabel,
		"releasedAssignees": orEmpty(res.ReleasedAssignees),
	})
}

func (s *Server) handleBlock(w http.ResponseWriter, r *http.Request) {
	n, err := number(r, "n")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		Blockers    []int `json:"blockers"`
		AllowClosed bool  `json:"allowClosed"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	res, err := s.acting(r).Block(r.Context(), n, in.Blockers, tracker.BlockOptions{AllowClosed: in.AllowClosed})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"added": orEmpty(res.Added), "existing": orEmpty(res.Existing)})
}

func (s *Server) handleUnblock(w http.ResponseWriter, r *http.Request) {
	n, err := number(r, "n")
	if err != nil {
		writeErr(w, err)
		return
	}
	m, err := number(r, "m")
	if err != nil {
		writeErr(w, err)
		return
	}
	res, err := s.acting(r).Unblock(r.Context(), n, []int{m}, tracker.BlockOptions{AllowClosed: r.URL.Query().Get("allowClosed") == "1"})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": orEmpty(res.Removed), "missing": orEmpty(res.Missing)})
}

func (s *Server) handleCompose(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Body           bodyIn `json:"body"`
		DiscoveredFrom int    `json:"discoveredFrom"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	res, err := tracker.ComposeBody(in.Body.body(in.DiscoveredFrom))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"body": res.Body, "findings": findingsOut(res.Findings)})
}

func (s *Server) handleCheck(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text string `json:"text"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	res, err := tracker.CheckBody(in.Text)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, checkOut{OK: true, Findings: findingsOut(res.Findings)})
}

type planRefIn struct {
	ID     string `json:"id"`
	Number int    `json:"number"`
}

type planEntryIn struct {
	ID             string      `json:"id"`
	Title          string      `json:"title"`
	Type           string      `json:"type"`
	Priority       string      `json:"priority"`
	Areas          []string    `json:"areas"`
	Body           string      `json:"body"`
	Sections       *bodyIn     `json:"sections"`
	Parent         *planRefIn  `json:"parent"`
	BlockedBy      []planRefIn `json:"blockedBy"`
	DiscoveredFrom int         `json:"discoveredFrom"`
}

// handleApply batch-creates issues from a JSON plan. Each request gets its
// own in-memory checkpoint: MemoryCheckpoint has no lock, so sharing one
// across requests would race, and a resume across HTTP calls is out of scope.
func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Entries []planEntryIn `json:"entries"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	entries := make([]tracker.PlanEntry, 0, len(in.Entries))
	for i, e := range in.Entries {
		pe := tracker.PlanEntry{
			ID: e.ID, Title: e.Title, Type: e.Type, Priority: e.Priority, Areas: e.Areas,
			Body: e.Body, DiscoveredFrom: e.DiscoveredFrom, Line: i + 1,
		}
		if e.Sections != nil {
			pe.Sections = tracker.Sections{
				Where: e.Sections.Where, Problem: e.Sections.Problem, Goal: e.Sections.Goal,
				Fix: e.Sections.Fix, Approach: e.Sections.Approach, DoneWhen: e.Sections.DoneWhen,
			}
		}
		if e.Parent != nil {
			pe.Parent = &tracker.PlanRef{ID: e.Parent.ID, Number: e.Parent.Number}
		}
		for _, b := range e.BlockedBy {
			pe.BlockedBy = append(pe.BlockedBy, tracker.PlanRef{ID: b.ID, Number: b.Number})
		}
		entries = append(entries, pe)
	}
	if err := tracker.ValidatePlan(entries); err != nil {
		writeErr(w, err)
		return
	}
	res, err := s.acting(r).ApplyPlan(r.Context(), entries, tracker.ApplyOptions{
		DryRun: dryRun(r), Checkpoint: tracker.NewMemoryCheckpoint(),
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"dryRun": res.DryRun, "created": res.Created, "wired": res.Wired, "unwired": res.Unwired,
		"warnings": orEmpty(res.Warnings), "mapping": res.Mapping, "wouldCreate": orEmpty(res.WouldCreate),
	})
}

type prIn struct {
	For     []int  `json:"for"`
	Title   string `json:"title"`
	What    string `json:"what"`
	Why     string `json:"why"`
	Testing string `json:"testing"`
	Risk    string `json:"risk"`
	Text    string `json:"text"`
	Ready   bool   `json:"ready"`
	Base    string `json:"base"`
}

func (s *Server) handlePRCreate(w http.ResponseWriter, r *http.Request) {
	if s.prc == nil {
		writeErr(w, &pr.NoPullRequestsError{})
		return
	}
	var in prIn
	if err := decode(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	res, err := s.prc.Create(r.Context(), pr.CreateRequest{
		For: in.For, Title: in.Title, Ready: in.Ready, Base: in.Base,
		Body: pr.Body{Sections: pr.Sections{What: in.What, Why: in.Why, Testing: in.Testing, Risk: in.Risk}, Text: in.Text},
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, prFrom(res))
}

func (s *Server) handlePRAmend(w http.ResponseWriter, r *http.Request) {
	if s.prc == nil {
		writeErr(w, &pr.NoPullRequestsError{})
		return
	}
	var in struct {
		For     []int   `json:"for"`
		Title   *string `json:"title"`
		What    *string `json:"what"`
		Why     *string `json:"why"`
		Testing *string `json:"testing"`
		Risk    *string `json:"risk"`
		Text    *string `json:"text"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	res, err := s.prc.Amend(r.Context(), pr.AmendRequest{
		For: in.For, Title: in.Title,
		Edit: pr.BodyEdit{What: in.What, Why: in.Why, Testing: in.Testing, Risk: in.Risk, Text: in.Text},
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, prFrom(res))
}
