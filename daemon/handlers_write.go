package daemon

import (
	"encoding/json"

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

func (s *Server) create(q *request[createIn]) (reply, error) {
	in := q.in
	req := tracker.CreateRequest{
		Title: in.Title, Type: in.Type, Priority: in.Priority, Areas: in.Areas,
		Body: in.Body.body(in.DiscoveredFrom), Parent: in.Parent,
		BlockedBy: in.BlockedBy, DiscoveredFrom: in.DiscoveredFrom,
	}
	if q.dry {
		// CheckCreate runs the whole write path's validation, references
		// included, and writes nothing: the form's live validation.
		if err := q.tr.CheckCreate(q.ctx, req); err != nil {
			return reply{}, err
		}
		return dryRunResult(req.Body)
	}
	res, err := q.tr.Create(q.ctx, req)
	if err != nil {
		return reply{}, err
	}
	return created(res), nil
}

func (s *Server) createEpic(q *request[epicIn]) (reply, error) {
	req := tracker.CreateEpicRequest{Title: q.in.Title, Children: q.in.Children, Body: q.in.Body.body(0)}
	if q.dry {
		if err := q.tr.CheckCreateEpic(q.ctx, req); err != nil {
			return reply{}, err
		}
		return dryRunResult(req.Body)
	}
	res, err := q.tr.CreateEpic(q.ctx, req)
	if err != nil {
		return reply{}, err
	}
	return created(res), nil
}

// dryRunResult is the answer to a valid dry run: the advice the body would
// draw, the same for an issue as for an epic.
func dryRunResult(body tracker.Body) (reply, error) {
	composed, err := tracker.ComposeBody(body)
	if err != nil {
		return reply{}, err
	}
	return ok(checkOut{OK: true, Findings: composed.Findings}), nil
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

func (s *Server) edit(q *request[editIn]) (reply, error) {
	in := q.in
	res, err := q.tr.Edit(q.ctx, tracker.EditRequest{
		Issue: q.n, Priority: in.Priority, Type: in.Type, Title: in.Title, Body: in.Body,
		AddAreas: in.AddAreas, RemoveAreas: in.RemoveAreas, Parent: in.Parent,
		NoParent: in.NoParent, AllowClosed: in.AllowClosed,
	})
	if err != nil {
		return reply{}, err
	}
	return ok(res), nil
}

type startIn struct {
	Priority string `json:"priority"`
	Force    bool   `json:"force"`
}

func (s *Server) start(q *request[startIn]) (reply, error) {
	res, err := q.tr.Start(q.ctx, q.n, tracker.StartOptions{Priority: q.in.Priority, Force: q.in.Force})
	if err != nil {
		return reply{}, err
	}
	return ok(res), nil
}

type releaseIn struct {
	Force bool `json:"force"`
}

func (s *Server) release(q *request[releaseIn]) (reply, error) {
	if err := q.tr.Release(q.ctx, q.n, tracker.ReleaseOptions{Force: q.in.Force}); err != nil {
		return reply{}, err
	}
	return ok(map[string]any{"released": true}), nil
}

type closeIn struct {
	Reason      string `json:"reason"`
	Completed   bool   `json:"completed"`
	DuplicateOf int    `json:"duplicateOf"`
	Force       bool   `json:"force"`
}

func (s *Server) closeIssue(q *request[closeIn]) (reply, error) {
	in := q.in
	res, err := q.tr.Close(q.ctx, q.n, tracker.CloseOptions{
		Reason: in.Reason, Completed: in.Completed, DuplicateOf: in.DuplicateOf, Force: in.Force,
	})
	if err != nil {
		return reply{}, err
	}
	return ok(res), nil
}

type reopenIn struct {
	Reason string `json:"reason"`
}

func (s *Server) reopen(q *request[reopenIn]) (reply, error) {
	res, err := q.tr.Reopen(q.ctx, q.n, tracker.ReopenOptions{Reason: q.in.Reason})
	if err != nil {
		return reply{}, err
	}
	return ok(res), nil
}

type blockIn struct {
	Blockers    []int `json:"blockers"`
	AllowClosed bool  `json:"allowClosed"`
}

func (s *Server) block(q *request[blockIn]) (reply, error) {
	res, err := q.tr.Block(q.ctx, q.n, q.in.Blockers, tracker.BlockOptions{AllowClosed: q.in.AllowClosed})
	if err != nil {
		return reply{}, err
	}
	return ok(res), nil
}

func (s *Server) unblock(q *request[none]) (reply, error) {
	res, err := q.tr.Unblock(q.ctx, q.n, []int{q.m}, tracker.BlockOptions{AllowClosed: q.query.Get("allowClosed") == "1"})
	if err != nil {
		return reply{}, err
	}
	return ok(res), nil
}

type composeIn struct {
	Body           bodyIn `json:"body"`
	DiscoveredFrom int    `json:"discoveredFrom"`
}

func (s *Server) compose(q *request[composeIn]) (reply, error) {
	res, err := tracker.ComposeBody(q.in.Body.body(q.in.DiscoveredFrom))
	if err != nil {
		return reply{}, err
	}
	return ok(res), nil
}

type checkIn struct {
	Text string `json:"text"`
}

func (s *Server) check(q *request[checkIn]) (reply, error) {
	res, err := tracker.CheckBody(q.in.Text)
	if err != nil {
		return reply{}, err
	}
	return ok(checkOut{OK: true, Findings: res.Findings}), nil
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

type applyIn struct {
	Entries []planEntryIn `json:"entries"`
}

func (in applyIn) plan() []tracker.PlanEntry {
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
	return entries
}

// apply batch-creates issues from a JSON plan. A dry run writes nothing and
// needs no memory. A real run uses the plan's remembered checkpoint, so a
// retry after a failure resumes instead of repeating its first entries; a
// plan that finishes cleanly is forgotten, so sending it again is a new plan.
func (s *Server) apply(q *request[applyIn]) (reply, error) {
	entries := q.in.plan()
	if err := tracker.ValidatePlan(entries); err != nil {
		return reply{}, err
	}

	opts := tracker.ApplyOptions{DryRun: q.dry, Checkpoint: tracker.NewMemoryCheckpoint()}
	var run *applyRun
	finished := false
	if !q.dry {
		raw, err := json.Marshal(q.in)
		if err != nil {
			return reply{}, err
		}
		key := applyKey(raw, q.tr.Identity())
		run = s.applies.acquire(key)
		defer func() { s.applies.release(key, run, finished) }()
		run.mu.Lock()
		defer run.mu.Unlock()
		opts.Checkpoint = run.cp
	}

	res, err := q.tr.ApplyPlan(q.ctx, entries, opts)
	if err != nil {
		if run != nil {
			err = &applyError{err: err, created: run.created()}
		}
		return reply{}, err
	}
	finished = res.Unwired == 0
	return ok(res), nil
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

func (s *Server) prCreate(q *request[prIn]) (reply, error) {
	if s.prc == nil {
		return reply{}, &pr.NoPullRequestsError{}
	}
	in := q.in
	res, err := s.prc.Create(q.ctx, pr.CreateRequest{
		For: in.For, Title: in.Title, Ready: in.Ready, Base: in.Base,
		Body: pr.Body{Sections: pr.Sections{What: in.What, Why: in.Why, Testing: in.Testing, Risk: in.Risk}, Text: in.Text},
	})
	if err != nil {
		return reply{}, err
	}
	return created(res), nil
}

type amendIn struct {
	For     []int   `json:"for"`
	Title   *string `json:"title"`
	What    *string `json:"what"`
	Why     *string `json:"why"`
	Testing *string `json:"testing"`
	Risk    *string `json:"risk"`
	Text    *string `json:"text"`
}

func (s *Server) prAmend(q *request[amendIn]) (reply, error) {
	if s.prc == nil {
		return reply{}, &pr.NoPullRequestsError{}
	}
	in := q.in
	res, err := s.prc.Amend(q.ctx, pr.AmendRequest{
		For: in.For, Title: in.Title,
		Edit: pr.BodyEdit{What: in.What, Why: in.Why, Testing: in.Testing, Risk: in.Risk, Text: in.Text},
	})
	if err != nil {
		return reply{}, err
	}
	return ok(res), nil
}
