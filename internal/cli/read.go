package cli

import (
	"context"
	"slices"
	"strings"

	"github.com/lumberbarons/hew/internal/conventions"
	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/model"
	"github.com/lumberbarons/hew/internal/render"
)

// primeReadyCap keeps the primer's live half inside its token budget; the
// full list is one `hew ready` away.
const primeReadyCap = 10

var (
	openStates   = []gh.IssueState{gh.StateOpen}
	closedStates = []gh.IssueState{gh.StateClosed}
	allStates    = []gh.IssueState{gh.StateOpen, gh.StateClosed}
)

// The values --state accepts. "all" is what makes `list --json --bodies` an
// exhaustive dedup path: one call covering open and closed.
const (
	stateOpen   = "open"
	stateClosed = "closed"
	stateAll    = "all"
)

// DefaultReadyLimit caps `ready` output unless the caller says otherwise. It
// sits above primeReadyCap on purpose: the primer truncates its own ready
// section and points at `hew ready` for the rest, so a cap at or below that
// one would leave the pointer pointing at nothing new.
const DefaultReadyLimit = 30

// ReadyOpts filters ready output.
type ReadyOpts struct {
	// Limit caps how many issues are printed; 0 means unlimited. Capping is
	// safe because model.Ready is priority-sorted — the top N is the top N by
	// priority — but it is never silent: ready is the queue agents branch on,
	// and a truncated list read as complete reads as an empty backlog.
	Limit int
}

// Ready lists open, non-epic, unclaimed issues with zero open blockers.
// No results is exit 0: an empty queue is an answer, not an error.
func (a *App) Ready(ctx context.Context, opts ReadyOpts) error {
	if opts.Limit < 0 {
		return usageErr("--limit must be 0 or greater")
	}
	issues, err := a.Client.ListIssues(ctx, openStates)
	if err != nil {
		return err
	}
	// A cycle or a truncated blocker list can make ready wrong; surface both
	// so the agent knows the queue may be incomplete.
	for _, w := range model.WarningsOfKind(model.Warnings(issues), model.WarnDependencyCycle, model.WarnBlockersCapped) {
		a.warnf("%s", render.FormatWarning(w))
	}
	ready := model.Ready(issues)
	if opts.Limit > 0 && len(ready) > opts.Limit {
		a.warnf("showing %d of %d ready issues; --limit 0 for all", opts.Limit, len(ready))
		ready = ready[:opts.Limit]
	}
	return a.emitList(ready, "no ready work", render.List)
}

// ListOpts filters list output.
type ListOpts struct {
	Label string
	Epic  int
	// State is "open", "closed", or "all"; empty means the default, which is
	// open — except under Epic, where progress means seeing what is done as
	// well as what is left.
	State string
	// Closed is the older spelling of State "closed". Kept working for
	// existing callers; passing both is a usage error rather than a silent
	// precedence rule.
	Closed bool
	// Bodies carries each issue's body on the NDJSON lines — whole-tracker
	// dedup in one call. JSON-only: text output has no place for bodies.
	Bodies bool
}

// listStates resolves the requested state filter to the states to fetch, so
// the query asks for exactly what the caller wants and nothing is dropped
// again client-side.
func listStates(opts ListOpts) ([]gh.IssueState, error) {
	state := opts.State
	if opts.Closed {
		if state != "" {
			return nil, usageErr("--closed and --state are alternatives; use --state %s", stateClosed)
		}
		state = stateClosed
	}
	switch state {
	case stateOpen:
		return openStates, nil
	case stateClosed:
		return closedStates, nil
	case stateAll:
		return allStates, nil
	case "":
		if opts.Epic > 0 {
			return allStates, nil
		}
		return openStates, nil
	default:
		return nil, usageErr("--state must be %s, %s, or %s", stateOpen, stateClosed, stateAll)
	}
}

// List shows issues, open by default, filtered by state, label, or epic
// membership.
func (a *App) List(ctx context.Context, opts ListOpts) error {
	if opts.Bodies && !a.JSON {
		return usageErr("--bodies requires --json")
	}
	states, err := listStates(opts)
	if err != nil {
		return err
	}
	issues, err := a.Client.ListIssues(ctx, states)
	if err != nil {
		return err
	}
	var out []model.Issue
	for _, i := range issues {
		if opts.Label != "" && !slices.Contains(i.Labels, opts.Label) {
			continue
		}
		if opts.Epic > 0 && (i.Parent == nil || i.Parent.Number != opts.Epic) {
			continue
		}
		out = append(out, i)
	}
	model.SortForList(out)
	return a.emitListBodies(out, "no issues", render.List, opts.Bodies)
}

// Show prints one issue in full: body, deps, parent, children, comments.
func (a *App) Show(ctx context.Context, number int) error {
	issue, err := a.Client.GetIssue(ctx, number)
	if err != nil {
		return err
	}
	return a.emitIssue(issue)
}

// Search runs a repo-scoped text search over open and closed issues — the
// dedupe step before filing discovered work, where "already fixed" answers
// the question as well as "already filed". Output keeps the API's
// best-match order rather than the list sort: relevance is the point.
func (a *App) Search(ctx context.Context, terms string) error {
	terms = strings.TrimSpace(terms)
	if terms == "" {
		return usageErr("usage: hew search <terms>")
	}
	issues, total, err := a.Client.SearchIssues(ctx, terms)
	if err != nil {
		return err
	}
	if total > len(issues) {
		a.warnf("showing %d of %d matches; refine the terms", len(issues), total)
	}
	return a.emitList(issues, "no matches", render.List)
}

// Triage lists open issues missing their priority or type label, oldest
// first — work through them with `hew set`.
func (a *App) Triage(ctx context.Context) error {
	issues, err := a.Client.ListIssues(ctx, openStates)
	if err != nil {
		return err
	}
	untriaged := model.UntriagedIssues(issues)
	return a.emitList(untriaged, "no untriaged issues", render.List)
}

// Prime emits the session-start context: static conventions, live state,
// contradictions.
func (a *App) Prime(ctx context.Context) error {
	issues, err := a.Client.ListIssues(ctx, openStates)
	if err != nil {
		return err
	}
	ready := model.Ready(issues)
	d := render.PrimeData{
		Repo:       a.Repo.String(),
		Ready:      ready,
		ReadyTotal: len(ready),
		OpenTotal:  len(issues),
		InProgress: model.InProgressIssues(issues),
		Epics:      model.Epics(issues),
		Warnings:   model.Warnings(issues),
		Untriaged:  len(model.UntriagedIssues(issues)),
	}
	if len(d.Ready) > primeReadyCap {
		d.Ready = d.Ready[:primeReadyCap]
	}
	return a.emitPrime(conventions.PrimerStatic, d)
}

// EpicStatus with number <= 0 lists all open epics with progress rollups;
// with a number it shows that epic's children.
func (a *App) EpicStatus(ctx context.Context, number int) error {
	if number <= 0 {
		issues, err := a.Client.ListIssues(ctx, openStates)
		if err != nil {
			return err
		}
		epics := model.Epics(issues)
		return a.emitList(epics, "no epics", render.EpicList)
	}
	// One fetch of both states resolves child titles without N+1 queries.
	issues, err := a.Client.ListIssues(ctx, allStates)
	if err != nil {
		return err
	}
	byNum := model.ByNumber(issues)
	epic, ok := byNum[number]
	if !ok {
		return genericErr("issue #%d not found in %s", number, a.Repo)
	}
	if !epic.IsEpic() {
		return genericErr("#%d has no sub-issues; not an epic", number)
	}
	children := model.Children(issues, number)
	return a.emitEpicStatus(epic, children)
}
