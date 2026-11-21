package tracker

import (
	"context"

	"github.com/lumberbarons/hew/internal/conventions"
	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/model"
)

// PrimerResult is the live state a session-start primer renders: the ready,
// in-progress and epic sections with their uncapped totals, the untriaged
// count, every contradiction or completeness warning, and the backend's static
// convention text. Section caps, layout and the hook wrapper stay with the
// renderer; this value is the policy.
type PrimerResult struct {
	// Ready is every ready issue, priority-sorted; ideas are never in it, so
	// the section offers implementation work only. ReadyTotal is its uncapped
	// length.
	Ready      []Issue `json:"ready"`
	ReadyTotal int     `json:"readyTotal"`
	// OpenTotal is the number of open issues the fetch returned, which is the
	// denominator the primer reports.
	OpenTotal int `json:"openTotal"`
	// InProgress is every open, triaged issue carrying the in-progress label,
	// priority-sorted; InProgressTotal is its length.
	InProgress      []Issue `json:"inProgress"`
	InProgressTotal int     `json:"inProgressTotal"`
	// Epics is every open epic, priority-sorted; EpicsTotal is its length.
	Epics      []Issue `json:"epics"`
	EpicsTotal int     `json:"epicsTotal"`
	// Untriaged counts open untriaged issues. Their titles and bodies are not
	// in any section: Triage is the deliberate API that returns them.
	Untriaged int `json:"untriaged"`
	// Warnings is every contradiction and completeness notice the live state
	// carries — multiple labels, an in-progress epic, a dependency cycle, and
	// capped connections — in deterministic order.
	Warnings []Warning `json:"warnings"`
	// Conventions is the backend's static primer text: the GitHub variant with
	// the pull-request workflow, or the local variant that completes with
	// close --completed.
	Conventions string `json:"conventions"`
}

// Primer is the session-start read: one fetch of open issues, the ready,
// in-progress and epic sections, the untriaged count, and every warning the
// live state carries, together with the backend's convention text. It returns
// no untriaged title or body.
func (t *Tracker) Primer(ctx context.Context) (PrimerResult, error) {
	issues, err := t.store.ListIssues(ctx, []gh.IssueState{gh.StateOpen})
	if err != nil {
		return PrimerResult{}, asReadError(err)
	}
	ready := model.Ready(issues)
	inProgress := model.InProgressIssues(issues)
	epics := model.Epics(issues)
	static := conventions.PrimerStatic
	if t.backend == BackendLocal {
		static = conventions.PrimerLocal
	}
	return PrimerResult{
		Ready:           issuesFromModel(ready, false),
		ReadyTotal:      len(ready),
		OpenTotal:       len(issues),
		InProgress:      issuesFromModel(inProgress, false),
		InProgressTotal: len(inProgress),
		Epics:           issuesFromModel(epics, false),
		EpicsTotal:      len(epics),
		Untriaged:       len(model.UntriagedIssues(issues)),
		Warnings:        warningsFromModel(model.Warnings(issues)),
		Conventions:     static,
	}, nil
}
