package tracker

import (
	"context"
	"fmt"
	"slices"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/model"
)

// WarningKind identifies a warning's cause, so a caller can filter by kind and
// render its own message without parsing prose.
type WarningKind string

const (
	// WarnMultiPriority reports an issue carrying more than one priority
	// label; the highest urgency wins. It is a contradiction, so it rides on
	// a primer's warnings rather than a snapshot query's.
	WarnMultiPriority WarningKind = "multiple-priority-labels"
	// WarnMultiType reports more than one type label; the first of
	// bug|enhancement|task wins. Like the other contradictions, it is
	// primer-only.
	WarnMultiType WarningKind = "multiple-type-labels"
	// WarnInProgressEpic reports an epic carrying the in-progress label.
	// Epics are containers and are never worked directly, so it is a
	// primer-only contradiction.
	WarnInProgressEpic WarningKind = "in-progress-epic"
	// WarnDependencyCycle reports a blocked-by cycle. A cycle silently
	// removes all of its members from Ready, so the queue is incomplete.
	WarnDependencyCycle WarningKind = "dependency-cycle"
	// WarnSubIssuesCapped reports an epic whose sub-issue connection was
	// capped: the rollup counts come from the server and stay right, but
	// Children still reflects parent backlinks over the full fetch.
	WarnSubIssuesCapped WarningKind = "sub-issues-truncated"
	// WarnBlockersCapped reports an issue whose blocker connection was
	// capped: an unseen blocker may make Ready wrong.
	WarnBlockersCapped WarningKind = "blockers-truncated"
)

// Warning is one completeness or contradiction notice. A snapshot's query
// methods keep the completeness kinds; Primer keeps every kind.
type Warning struct {
	Kind WarningKind
	// Issue names the subject; 0 for a cycle, which spans several.
	Issue int
	// Cycle is the ordered member path for WarnDependencyCycle, ending with
	// its first member again.
	Cycle []int
	// Total and Fetched are the server-side and fetched counts for the
	// capped-connection kinds.
	Total   int
	Fetched int
}

// Reader answers read queries over an already-open store. It is the read half
// of a Tracker, kept separate so no Tracker exists without the backend and
// actor its write operations need: hew's own CLI resolves the backend and
// opens the store once for every command, then wraps that store rather than
// opening a second one. A consumer outside this module cannot name
// gh.IssueStore and uses the read methods on the Tracker Open returns.
type Reader struct{ store gh.IssueStore }

// Wrap makes a Reader from an already-open store: the seam hew's own CLI uses
// for reads. External callers use Open.
func Wrap(store gh.IssueStore) *Reader { return &Reader{store: store} }

// Snapshot fetches the tracker's issues in the requested states, once. The
// zero SnapshotOptions fetches open issues only. Warnings report dependency
// cycles and capped blocker or sub-issue connections — the conditions that
// make a query's answer incomplete rather than merely empty.
func (r *Reader) Snapshot(ctx context.Context, opts SnapshotOptions) (*Snapshot, error) {
	return snapshot(ctx, r.store, opts)
}

// Get fetches one issue in any state, with its body and recent comments.
func (r *Reader) Get(ctx context.Context, number int) (Issue, error) {
	return get(ctx, r.store, number)
}

// Snapshot fetches the tracker's issues in the requested states, once.
func (t *Tracker) Snapshot(ctx context.Context, opts SnapshotOptions) (*Snapshot, error) {
	return snapshot(ctx, t.store, opts)
}

// Get fetches one issue in any state, with its body and recent comments.
func (t *Tracker) Get(ctx context.Context, number int) (Issue, error) {
	return get(ctx, t.store, number)
}

func snapshot(ctx context.Context, store gh.IssueStore, opts SnapshotOptions) (*Snapshot, error) {
	states, fetch, err := resolveStates(opts.States)
	if err != nil {
		return nil, err
	}
	issues, err := store.ListIssues(ctx, fetch)
	if err != nil {
		return nil, asReadError(err)
	}
	return newSnapshot(issues, states), nil
}

// resolveStates turns the caller's states into the snapshot's record of what
// it covers and the backend's fetch filter. The zero value means open only.
func resolveStates(states []State) ([]State, []gh.IssueState, error) {
	if len(states) == 0 {
		states = []State{StateOpen}
	}
	record := make([]State, 0, len(states))
	fetch := make([]gh.IssueState, 0, len(states))
	for _, s := range states {
		switch s {
		case StateOpen:
			record = append(record, StateOpen)
			fetch = append(fetch, gh.StateOpen)
		case StateClosed:
			record = append(record, StateClosed)
			fetch = append(fetch, gh.StateClosed)
		default:
			return nil, nil, &InvalidInputError{Field: "states", Reason: fmt.Sprintf("unknown state %q", s)}
		}
	}
	return record, fetch, nil
}

// newSnapshot indexes a fetched issue set and keeps the warnings that bear on
// read queries. Contradiction warnings (multiple labels, an in-progress epic)
// are prime's business, not a snapshot's.
func newSnapshot(issues []model.Issue, states []State) *Snapshot {
	var warnings []Warning
	for _, w := range warningsFromModel(model.Warnings(issues)) {
		switch w.Kind {
		case WarnDependencyCycle, WarnSubIssuesCapped, WarnBlockersCapped:
			warnings = append(warnings, w)
		}
	}
	return &Snapshot{issues: issues, warnings: warnings, states: states}
}

// warningsFromModel maps every warning the domain produces, including the
// contradictions a snapshot query filters out but a primer reports.
func warningsFromModel(warnings []model.Warning) []Warning {
	if len(warnings) == 0 {
		return nil
	}
	out := make([]Warning, len(warnings))
	for n, w := range warnings {
		out[n] = Warning{
			Kind:    WarningKind(w.Kind),
			Issue:   w.Issue,
			Cycle:   w.Cycle,
			Total:   w.Total,
			Fetched: w.Fetched,
		}
	}
	return out
}

func get(ctx context.Context, store gh.IssueStore, number int) (Issue, error) {
	issue, err := store.GetIssue(ctx, number)
	if err != nil {
		return Issue{}, asReadError(err)
	}
	return issueFromModel(issue, true), nil
}

// Snapshot is one fetch of the tracker's issues, with the read-side queries a
// caller needs answered from it. The queries are pure: they filter and sort
// what the fetch returned, never call the backend again, and every answer is
// scoped to the states the fetch covered.
type Snapshot struct {
	issues   []model.Issue
	warnings []Warning
	states   []State
}

// States returns the states this snapshot fetched. A query can only answer
// about issues in them, so Get returning false can mean the issue is closed
// and this snapshot was open-only rather than that it does not exist.
func (s *Snapshot) States() []State { return slices.Clone(s.states) }

// Warnings returns the snapshot's completeness notices, in deterministic
// order: cycles first, then capped connections in issue order.
func (s *Snapshot) Warnings() []Warning {
	out := slices.Clone(s.warnings)
	for n := range out {
		out[n].Cycle = slices.Clone(out[n].Cycle)
	}
	return out
}

// Issues returns every fetched issue in list order — ready work first, then
// claimed, blocked, epics and closed — with body. Callers that want one
// question answered use Ready, Blocked, Claimed, Epics or Children instead.
func (s *Snapshot) Issues() []Issue {
	issues := slices.Clone(s.issues)
	model.SortForList(issues)
	return issuesFromModel(issues)
}

// Get returns the issue from the snapshot, if the fetched states included it.
// It is a lookup, not a fetch: use Reader.Get or Tracker.Get for an issue
// outside the snapshot.
func (s *Snapshot) Get(number int) (Issue, bool) {
	for _, i := range s.issues {
		if i.Number == number {
			return issueFromModel(i, true), true
		}
	}
	return Issue{}, false
}

// Untriaged returns how many open issues in the snapshot are untriaged — the
// same count Primer reports, with none of their titles or bodies. An untriaged
// issue is ordinary tracker state rather than a defect, so it stays out of the
// read queries; Triage is the deliberate API that returns its text. Epics are
// containers and are never untriaged, and a closed fetch holds no open triage
// work, so either counts zero.
func (s *Snapshot) Untriaged() int {
	return len(model.UntriagedIssues(s.issues))
}

// Ready returns open, non-epic, unclaimed, triaged issues with no open
// blockers, sorted by priority — the same set `hew ready` prints.
func (s *Snapshot) Ready() []Issue {
	return issuesFromModel(model.Ready(s.issues))
}

// Blocked returns open issues with at least one open blocker, sorted by
// priority. A claimed issue can be blocked too: the queries answer different
// questions rather than partitioning the tracker.
func (s *Snapshot) Blocked() []Issue {
	var out []model.Issue
	for _, i := range s.issues {
		if i.IsOpen() && len(i.OpenBlockers()) > 0 {
			out = append(out, i)
		}
	}
	model.SortByPriority(out)
	return issuesFromModel(out)
}

// Claimed returns open claimed issues — an assignee or the in-progress label
// — sorted by priority.
func (s *Snapshot) Claimed() []Issue {
	var out []model.Issue
	for _, i := range s.issues {
		if i.IsOpen() && i.Claimed() {
			out = append(out, i)
		}
	}
	model.SortByPriority(out)
	return issuesFromModel(out)
}

// Epics returns the issues that have sub-issues or carry the epic label,
// sorted by priority. It spans the states the snapshot fetched, so the zero
// SnapshotOptions yields open epics and a closed fetch adds the finished ones.
func (s *Snapshot) Epics() []Issue {
	return issuesFromModel(model.EpicsOf(s.issues))
}

// Children returns an epic's children, oldest first, from parent backlinks
// over the full fetch — complete even when the epic's own sub-issue
// connection was capped.
func (s *Snapshot) Children(epic int) []Issue {
	return issuesFromModel(model.Children(s.issues, epic))
}

// issuesFromModel converts a query result to the library's flat shape. A
// snapshot read carries bodies because the fetch already has them; comments
// are only present on Reader.Get and Tracker.Get.
func issuesFromModel(issues []model.Issue) []Issue {
	if issues == nil {
		return nil
	}
	out := make([]Issue, len(issues))
	for n, i := range issues {
		out[n] = issueFromModel(i, true)
	}
	return out
}
