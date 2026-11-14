package tracker

import (
	"context"
	"slices"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/model"
)

// WarningKind identifies a snapshot's completeness notice, so a caller can
// filter by cause and render its own message without parsing prose.
type WarningKind string

const (
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

// Warning is one completeness notice about a snapshot.
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

// Snapshot is one fetch of the tracker's issues, with the read-side queries a
// caller needs answered from it. The queries are pure: they filter and sort
// what the fetch returned, never call the backend again.
type Snapshot struct {
	issues   []model.Issue
	warnings []Warning
}

// Snapshot fetches the tracker's issues in the requested states, once. The
// zero SnapshotOptions fetches open issues only. Warnings report dependency
// cycles and capped blocker or sub-issue connections — the conditions that
// make a query's answer incomplete rather than merely empty.
func (t *Tracker) Snapshot(ctx context.Context, opts SnapshotOptions) (*Snapshot, error) {
	states := []gh.IssueState{gh.StateOpen}
	if opts.IncludeClosed {
		states = append(states, gh.StateClosed)
	}
	issues, err := t.store.ListIssues(ctx, states)
	if err != nil {
		return nil, err
	}
	return newSnapshot(issues), nil
}

// newSnapshot indexes a fetched issue set and keeps the warnings that bear on
// read queries. Contradiction warnings (multiple labels, an in-progress epic)
// are prime's business, not a snapshot's.
func newSnapshot(issues []model.Issue) *Snapshot {
	var warnings []Warning
	for _, w := range model.Warnings(issues) {
		switch w.Kind {
		case model.WarnDependencyCycle:
			warnings = append(warnings, Warning{Kind: WarnDependencyCycle, Cycle: w.Cycle})
		case model.WarnSubIssuesCapped:
			warnings = append(warnings, Warning{Kind: WarnSubIssuesCapped, Issue: w.Issue, Total: w.Total, Fetched: w.Fetched})
		case model.WarnBlockersCapped:
			warnings = append(warnings, Warning{Kind: WarnBlockersCapped, Issue: w.Issue, Total: w.Total, Fetched: w.Fetched})
		}
	}
	return &Snapshot{issues: issues, warnings: warnings}
}

// Get fetches one issue in any state, with its body and recent comments.
func (t *Tracker) Get(ctx context.Context, number int) (Issue, error) {
	issue, err := t.store.GetIssue(ctx, number)
	if err != nil {
		return Issue{}, err
	}
	return issueFromModel(issue, true), nil
}

// Warnings returns the snapshot's completeness notices, in deterministic
// order: cycles first, then capped connections in issue order.
func (s *Snapshot) Warnings() []Warning { return slices.Clone(s.warnings) }

// Issues returns every fetched issue in list order — ready work first, then
// claimed, blocked, epics and closed — with body. Callers that want one
// question answered use Ready, Blocked, Claimed, Epics or Children instead.
func (s *Snapshot) Issues() []Issue {
	issues := slices.Clone(s.issues)
	model.SortForList(issues)
	return issuesFromModel(issues)
}

// Get returns the issue from the snapshot, if the fetch included it. It is a
// lookup, not a fetch: use Tracker.Get for an issue outside the snapshot.
func (s *Snapshot) Get(number int) (Issue, bool) {
	for _, i := range s.issues {
		if i.Number == number {
			return issueFromModel(i, true), true
		}
	}
	return Issue{}, false
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

// Epics returns open issues that have sub-issues or carry the epic label,
// sorted by priority.
func (s *Snapshot) Epics() []Issue {
	return issuesFromModel(model.Epics(s.issues))
}

// Children returns an epic's children, oldest first, from parent backlinks
// over the full fetch — complete even when the epic's own sub-issue
// connection was capped.
func (s *Snapshot) Children(epic int) []Issue {
	return issuesFromModel(model.Children(s.issues, epic))
}

// issuesFromModel converts a query result to the library's flat shape. A
// snapshot read carries bodies because the fetch already has them; comments
// are only present on Tracker.Get.
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
