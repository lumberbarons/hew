package cli

import (
	"bytes"
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

// primeEpicsCap caps the primer's epic section the same way: `hew epic
// status` lists every epic, so prime only names the highest-priority few.
const primeEpicsCap = 5

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
	// Bodies carries each issue's body on the NDJSON lines — triaged-tracker
	// dedup in one call (untriaged issues stay behind `hew triage`). JSON-only:
	// text output has no place for bodies.
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
// membership. Untriaged issues are omitted: their titles and bodies are
// unvetted input, and list is a read an agent can reach without a human in
// between — `hew triage` is the only command that emits them.
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
		if i.Untriaged() {
			continue
		}
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
// Untriaged matches are omitted (their text is unvetted input); the other
// half of dedup, over the untriaged queue, is `hew triage --search`.
func (a *App) Search(ctx context.Context, terms string) error {
	terms = strings.TrimSpace(terms)
	if terms == "" {
		return usageErr("usage: hew search <terms>")
	}
	issues, total, err := a.Client.SearchIssues(ctx, terms)
	if err != nil {
		return err
	}
	fetched := len(issues)
	out := slices.DeleteFunc(issues, func(i model.Issue) bool { return i.Untriaged() })
	switch {
	case len(out) == 0 && fetched > 0 && total > fetched:
		// The fetch was capped and everything it returned was untriaged —
		// the unseen matches may be triaged, so say only what is known.
		a.warnf("no triaged matches in the first %d of %d; refine the terms or hew triage --search", fetched, total)
	case len(out) == 0 && fetched > 0:
		// "No matches" would read as "safe to file" when every match was
		// untriaged; name where they went instead.
		a.warnf("no triaged matches; hew triage --search covers them")
	case total > fetched:
		a.warnf("showing %d of %d matches; refine the terms", len(out), total)
	}
	return a.emitList(out, "no matches", render.List)
}

// TriageOpts retargets triage at the dedup case.
type TriageOpts struct {
	// Search restricts triage to search matches instead of the open queue:
	// the dedup half over untriaged titles and bodies, spanning both states
	// so "already fixed" answers as well as "already filed". Empty is the
	// queue view: open untriaged issues, oldest first.
	Search string
}

// Triage lists issues missing their priority or type label, oldest first —
// work through them with `hew set`. With Search it is the dedup path over
// untriaged content, the one list-shaped read that emits it: an agent
// harness deny list keys on this command, so nothing else may.
func (a *App) Triage(ctx context.Context, opts TriageOpts) error {
	if opts.Search == "" {
		issues, err := a.Client.ListIssues(ctx, openStates)
		if err != nil {
			return err
		}
		untriaged := model.UntriagedIssues(issues)
		return a.emitList(untriaged, "no untriaged issues", render.List)
	}
	terms := strings.TrimSpace(opts.Search)
	if terms == "" {
		return usageErr("usage: hew triage --search <terms>")
	}
	issues, total, err := a.Client.SearchIssues(ctx, terms)
	if err != nil {
		return err
	}
	untriaged := slices.DeleteFunc(issues, func(i model.Issue) bool { return !i.Untriaged() })
	if total > len(issues) {
		a.warnf("showing %d of %d matches; refine the terms", len(untriaged), total)
	}
	return a.emitList(untriaged, "no untriaged matches", render.List)
}

// PrimeOpts re-targets the primer's output at a caller with its own
// format, such as an agent hook whose stdout contract is not text.
type PrimeOpts struct {
	// HookFormat is the session-start hook format to emit. Empty is the
	// plain text primer; "cursor" JSON-encodes the primer into the
	// additional_context field Cursor's sessionStart hook reads.
	HookFormat string
}

// Prime emits the session-start context: static conventions, live state,
// contradictions. prime never reads stdin — Cursor writes its sessionStart
// payload there, and ignoring it is what keeps that JSON out of the primer.
func (a *App) Prime(ctx context.Context, opts PrimeOpts) error {
	if opts.HookFormat != "" && opts.HookFormat != "cursor" {
		return usageErr("--hook-format must be cursor")
	}
	if opts.HookFormat != "" && a.JSON {
		// The hook path is machine-to-machine: plain text always, whatever
		// the terminal says, and never the --json schema.
		return usageErr("--hook-format and --json are alternatives")
	}
	issues, err := a.Client.ListIssues(ctx, openStates)
	if err != nil {
		return err
	}
	ready := model.Ready(issues)
	epics := model.Epics(issues)
	d := render.PrimeData{
		Repo:       a.Repo.String(),
		Ready:      ready,
		ReadyTotal: len(ready),
		OpenTotal:  len(issues),
		InProgress: model.InProgressIssues(issues),
		Epics:      epics,
		EpicsTotal: len(epics),
		Warnings:   model.Warnings(issues),
		Untriaged:  len(model.UntriagedIssues(issues)),
	}
	if len(d.Ready) > primeReadyCap {
		d.Ready = d.Ready[:primeReadyCap]
	}
	if len(d.Epics) > primeEpicsCap {
		d.Epics = d.Epics[:primeEpicsCap]
	}
	if opts.HookFormat == "cursor" {
		var buf bytes.Buffer
		render.Prime(&buf, conventions.PrimerStatic, d, render.Style{})
		return render.CursorHookJSON(a.Out, buf.String())
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
