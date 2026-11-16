package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/model"
	"github.com/lumberbarons/hew/internal/render"
	"github.com/lumberbarons/hew/tracker"
)

// The primer caps every live section and points at the command that lists
// the rest, so its size is bounded by section count rather than repo size:
// ready → `hew ready`, in-progress → `hew list` (claimed sorts right behind
// ready), epics → `hew epic status`.
const (
	primeReadyCap      = 5
	primeInProgressCap = 5
	primeEpicsCap      = 5
)

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
	snapshot, err := tracker.Wrap(a.Client).Snapshot(ctx, tracker.SnapshotOptions{})
	if err != nil {
		return err
	}
	// A cycle or a truncated blocker list can make ready wrong; surface both
	// so the agent knows the queue may be incomplete.
	for _, w := range warningsFromTracker(snapshot.Warnings()) {
		switch w.Kind {
		case model.WarnDependencyCycle, model.WarnBlockersCapped:
			a.warnf("%s", render.FormatWarning(w))
		}
	}
	ready := issuesFromTracker(snapshot.Ready())
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
	snapshot, err := tracker.Wrap(a.Client).Snapshot(ctx, tracker.SnapshotOptions{
		States: trackerStates(states),
	})
	if err != nil {
		return err
	}
	out := issuesFromTracker(snapshot.List(tracker.ListFilter{Label: opts.Label, Epic: opts.Epic}))
	return a.emitListBodies(out, "no issues", render.List, opts.Bodies)
}

// Show prints one issue in full: body, deps, parent, children, comments.
// An epic's detail view also names its next workable child, which needs each
// child's blockers — a second two-state fetch, but only when the subject is
// an epic.
func (a *App) Show(ctx context.Context, number int) error {
	detail, err := a.readTracker().Detail(ctx, number)
	if err != nil {
		return err
	}
	subject := issueFromTracker(detail.Issue)
	children := issuesFromTracker(detail.Children)
	a.warnChildrenBlockersCapped(children, warningsFromTracker(detail.Warnings))
	return a.emitIssue(subject, children)
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
	result, err := a.readTracker().Search(ctx, tracker.SearchRequest{Terms: terms})
	if err != nil {
		return err
	}
	out := issuesFromTracker(result.Issues)
	switch {
	case len(out) == 0 && result.Fetched > 0 && result.Total > result.Fetched:
		// The fetch was capped and everything it returned was untriaged —
		// the unseen matches may be triaged, so say only what is known.
		a.warnf("no triaged matches in the first %d of %d; refine the terms or hew triage --search", result.Fetched, result.Total)
	case len(out) == 0 && result.Fetched > 0:
		// "No matches" would read as "safe to file" when every match was
		// untriaged; name where they went instead.
		a.warnf("no triaged matches; hew triage --search covers them")
	case result.Total > result.Fetched:
		a.warnf("showing %d of %d matches; refine the terms", len(out), result.Total)
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
	if opts.Search != "" && strings.TrimSpace(opts.Search) == "" {
		return usageErr("usage: hew triage --search <terms>")
	}
	result, err := a.readTracker().Triage(ctx, tracker.TriageRequest{Search: opts.Search})
	if err != nil {
		return err
	}
	if result.Total > result.Fetched {
		a.warnf("showing %d of %d matches; refine the terms", len(result.Issues), result.Total)
	}
	empty := "no untriaged issues"
	if opts.Search != "" {
		empty = "no untriaged matches"
	}
	return a.emitTriage(issuesFromTracker(result.Issues), empty)
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
	result, err := a.readTracker().Primer(ctx)
	if err != nil {
		return err
	}
	d := render.PrimeData{
		Repo:            a.Repo.String(),
		Ready:           issuesFromTracker(result.Ready),
		ReadyTotal:      result.ReadyTotal,
		OpenTotal:       result.OpenTotal,
		InProgress:      issuesFromTracker(result.InProgress),
		InProgressTotal: result.InProgressTotal,
		Epics:           issuesFromTracker(result.Epics),
		EpicsTotal:      result.EpicsTotal,
		Warnings:        warningsFromTracker(result.Warnings),
		Untriaged:       result.Untriaged,
	}
	if len(d.Ready) > primeReadyCap {
		d.Ready = d.Ready[:primeReadyCap]
	}
	if len(d.InProgress) > primeInProgressCap {
		d.InProgress = d.InProgress[:primeInProgressCap]
	}
	if len(d.Epics) > primeEpicsCap {
		d.Epics = d.Epics[:primeEpicsCap]
	}
	if opts.HookFormat == "cursor" {
		var buf bytes.Buffer
		render.Prime(&buf, result.Conventions, d, render.Style{})
		return render.CursorHookJSON(a.Out, buf.String())
	}
	return a.emitPrime(result.Conventions, d)
}

// EpicStatus with number <= 0 lists all open epics with progress rollups;
// with a number it shows that epic's children and the next workable one.
func (a *App) EpicStatus(ctx context.Context, number int) error {
	t := a.readTracker()
	if number <= 0 {
		snapshot, err := t.Snapshot(ctx, tracker.SnapshotOptions{})
		if err != nil {
			return err
		}
		return a.emitList(issuesFromTracker(snapshot.Epics()), "no epics", render.EpicList)
	}
	detail, err := t.Detail(ctx, number)
	if err != nil {
		var notFound *tracker.NotFoundError
		if errors.As(err, &notFound) {
			return genericErr("issue #%d not found in %s", number, a.Repo)
		}
		return err
	}
	epic := issueFromTracker(detail.Issue)
	if !epic.IsEpic() {
		return genericErr("#%d has no sub-issues; not an epic", number)
	}
	children := issuesFromTracker(detail.Children)
	a.warnChildrenBlockersCapped(children, warningsFromTracker(detail.Warnings))
	return a.emitEpicStatus(epic, children)
}

// warnChildrenBlockersCapped surfaces the one case that can make `next`
// wrong: a capped blocker list may hide an open blocker, so an unworkable
// child could be named. The warning stays per-child so the agent can see
// which child of the sequence is suspect.
func (a *App) warnChildrenBlockersCapped(children []model.Issue, warnings []model.Warning) {
	members := make(map[int]bool, len(children))
	for _, c := range children {
		members[c.Number] = true
	}
	for _, w := range model.WarningsOfKind(warnings, model.WarnBlockersCapped) {
		if members[w.Issue] {
			a.warnf("#%d has %d blockers, only %d fetched; next may be wrong", w.Issue, w.Total, w.Fetched)
		}
	}
}

// readTracker is the library handle the read commands delegate their policy
// to, over the store the app already opened. Reads never resolve the actor.
func (a *App) readTracker() *tracker.Tracker {
	backend := tracker.BackendGitHub
	if a.Local {
		backend = tracker.BackendLocal
	}
	return tracker.WrapAs(a.Client, backend, "")
}

// trackerStates maps the CLI's resolved state filter onto the library's
// states, so `list` asks the backend for exactly what it prints.
func trackerStates(states []gh.IssueState) []tracker.State {
	out := make([]tracker.State, len(states))
	for n, s := range states {
		switch s {
		case gh.StateOpen:
			out[n] = tracker.StateOpen
		case gh.StateClosed:
			out[n] = tracker.StateClosed
		}
	}
	return out
}

// issueFromTracker converts the library's flat issue back to the domain shape
// the text and JSON renderers take. It is total: tracker keeps the raw labels,
// reference and parent states, parent title and close reason the flat schema
// hides, so the rendered output is byte-for-byte what it was before the read
// went through tracker.
func issueFromTracker(i tracker.Issue) model.Issue {
	out := model.Issue{
		Number:             i.Number,
		Title:              i.Title,
		State:              strings.ToUpper(i.State),
		StateReason:        i.StateReason(),
		CreatedAt:          i.CreatedAt,
		Labels:             i.Labels(),
		Assignees:          i.Assignees,
		ParentTitle:        i.ParentTitle(),
		SubIssues:          refsFromTracker(i.SubIssueRefs()),
		SubIssuesTotal:     i.SubIssuesTotal,
		SubIssuesCompleted: i.SubIssuesCompleted,
		BlockedBy:          refsFromTracker(i.BlockedByRefs()),
		BlockedByTotal:     i.BlockedByTotal,
		CommentsTotal:      i.CommentsTotal,
	}
	if parent, ok := i.ParentRef(); ok {
		out.Parent = &model.Ref{Number: parent.Number, State: parent.State}
	} else if i.Parent != nil {
		out.Parent = &model.Ref{Number: *i.Parent}
	}
	if i.Body != nil {
		out.Body = *i.Body
	}
	for _, c := range i.Comments {
		out.Comments = append(out.Comments, model.Comment{Author: c.Author, CreatedAt: c.CreatedAt, Body: c.Body})
	}
	return out
}

func issuesFromTracker(issues []tracker.Issue) []model.Issue {
	if issues == nil {
		return nil
	}
	out := make([]model.Issue, len(issues))
	for n, i := range issues {
		out[n] = issueFromTracker(i)
	}
	return out
}

func refsFromTracker(refs []tracker.Ref) []model.Ref {
	if refs == nil {
		return nil
	}
	out := make([]model.Ref, len(refs))
	for n, r := range refs {
		out[n] = model.Ref{Number: r.Number, State: r.State}
	}
	return out
}

// warningsFromTracker keeps the kind values in step with model's, which is
// what lets the existing prose renderer format them unchanged.
func warningsFromTracker(warnings []tracker.Warning) []model.Warning {
	out := make([]model.Warning, len(warnings))
	for n, w := range warnings {
		out[n] = model.Warning{
			Kind:    model.WarningKind(w.Kind),
			Issue:   w.Issue,
			Cycle:   w.Cycle,
			Total:   w.Total,
			Fetched: w.Fetched,
		}
	}
	return out
}
