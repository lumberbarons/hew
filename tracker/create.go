package tracker

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/lumberbarons/hew/internal/conventions"
	"github.com/lumberbarons/hew/internal/model"
)

// CreateRequest is one issue to file with Create: the typed counterpart of the
// `create` command's inputs, minus the flags and file and editor handling the
// adapter owns.
//
// A zero Body composes an empty body. Title, Type, Priority, Areas and the Body
// are validated before anything is written, so a refused request leaves the
// tracker untouched.
type CreateRequest struct {
	Title string
	// Type is bug, enhancement or task. Use CreateEpic for a parent issue.
	Type string
	// Priority is P0..P4; empty means P2.
	Priority string
	Areas    []string
	// Body is the issue body: structured sections or text the caller already
	// has. Create composes and validates it, appending DiscoveredFrom's link.
	Body Body
	// Parent attaches the new issue as a sub-issue of Parent. A closed parent
	// is a valid reference; only a blocker has to be open.
	Parent int
	// BlockedBy makes the new issue depend on these issues, each of which must
	// be open. A new issue has no dependents, so these edges cannot form a
	// cycle; Block owns the transitive check for edges between existing issues.
	BlockedBy []int
	// DiscoveredFrom links the new issue to the issue that surfaced it. A
	// body may carry the same number instead of this field.
	DiscoveredFrom int
	// Assignees claims the new issue for these logins, preserving a source
	// claim when a migration files it. A non-empty list adds the in-progress
	// label too, so the result is a claim the claim guard recognises rather
	// than a bare assignment.
	Assignees []string
}

// CreateEpicRequest is one epic (parent issue) to file with CreateEpic. Title
// need not carry the cosmetic Epic prefix; CreateEpic adds it if missing.
type CreateEpicRequest struct {
	Title string
	// Children attaches these existing issues as sub-issues. Each must exist
	// and have no parent yet.
	Children []int
	// Body is the epic's body, composed the same way an issue's is.
	Body Body
}

// CreatedEdge is one relationship a creation wrote after the issue existed.
// To is the other issue: the parent, a blocker, or a child.
type CreatedEdge struct {
	Kind EdgeKind `json:"kind"`
	To   int      `json:"to"`
}

// String renders the edge as "blocked-by #2", the form error messages use.
func (e CreatedEdge) String() string { return fmt.Sprintf("%s #%d", e.Kind, e.To) }

// CreateResult is a creation that finished: the issue as the creation left it,
// the relationship edges it wrote in attempt order, and the advisory findings
// for the body it composed.
//
// The Issue reflects the write rather than a re-read: it carries the number,
// title and labels the backend returned plus the body, assignees and edges the
// call wrote. Get is the live read when a caller needs the backend's own view.
type CreateResult struct {
	Issue    Issue         `json:"issue"`
	Edges    []CreatedEdge `json:"edges"`
	Findings []Finding     `json:"findings"`
}

// CreateStage names the step a creation was in when it stopped partway.
type CreateStage string

const (
	// CreateStageAssignees adds the requested assignees.
	CreateStageAssignees CreateStage = "assignees"
	// CreateStageBlockedBy attaches the requested blockers.
	CreateStageBlockedBy CreateStage = "blocked-by"
	// CreateStageParent attaches the requested parent.
	CreateStageParent CreateStage = "parent"
	// CreateStageChildren attaches an epic's children.
	CreateStageChildren CreateStage = "children"
)

// PartialCreateError reports a creation that filed its issue but stopped
// before every follow-up write landed. Issue is the created issue and always
// exists; Stage names the step that failed.
//
// Applied lists the relationship edges written before the failure, in attempt
// order. Failed is the edge whose write failed, and NotAttempted the edges
// after it that never ran; both are meaningful only for the edge stages
// (blocked-by, parent, children). An assignee failure has no edge to name, so
// Failed stays zero and Err identifies the assignee.
//
// Err is the backend cause, retained for errors.Is and errors.As.
type PartialCreateError struct {
	Issue        Issue
	Stage        CreateStage
	Applied      []CreatedEdge
	Failed       CreatedEdge
	NotAttempted []CreatedEdge
	Err          error
}

func (e *PartialCreateError) Error() string {
	msg := fmt.Sprintf("created #%d but %s failed", e.Issue.Number, e.failedWrite())
	if len(e.Applied) > 0 {
		msg += fmt.Sprintf(" after %s", formatEdges(e.Applied))
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *PartialCreateError) Unwrap() error { return e.Err }

// failedWrite describes the failing stage for the error message.
func (e *PartialCreateError) failedWrite() string {
	switch e.Stage {
	case CreateStageAssignees:
		return "assigning its assignees"
	case CreateStageBlockedBy:
		return fmt.Sprintf("attaching blocked-by #%d", e.Failed.To)
	case CreateStageParent:
		return fmt.Sprintf("attaching parent #%d", e.Failed.To)
	case CreateStageChildren:
		return fmt.Sprintf("attaching child #%d", e.Failed.To)
	}
	return "finishing it"
}

// ParentedError reports an epic child that already belongs to another epic.
// AddSubIssue refuses the attach, and catching it before the epic is filed is
// what keeps a predictable failure from leaving a childless epic behind.
type ParentedError struct {
	Issue  int
	Parent int
}

func (e *ParentedError) Error() string {
	return fmt.Sprintf("issue #%d already has a parent (#%d)", e.Issue, e.Parent)
}

// Create files one issue conforming to the conventions: exactly one priority
// and one type label, a template body, and native parent, dependency and
// discovered-from links.
//
// The whole request is validated before the first write: title, type, priority,
// areas and body through ValidateTitle, NormalizeAreas and ComposeBody, then
// every reference by reading it. A refusal therefore leaves the tracker
// untouched. The writes themselves are multi-step — create, then assignees,
// blockers and parent — and a failure after the issue exists is a
// *PartialCreateError naming the created issue, the stage and the edges that
// landed.
func (t *Tracker) Create(ctx context.Context, req CreateRequest) (CreateResult, error) {
	plan, err := t.prepareCreate(ctx, req)
	if err != nil {
		return CreateResult{}, err
	}
	title, composed, labels := plan.title, plan.composed, plan.labels
	assignees, blockerRefs, parentRef, parentTitle := plan.assignees, plan.blockers, plan.parent, plan.parentTitle
	created, err := t.store.CreateIssue(ctx, title, composed.Body, labels)
	if err != nil {
		return CreateResult{}, asWriteError(err)
	}
	issue := createdIssue(created, title, composed.Body, labels)

	for _, login := range assignees {
		if err := t.store.AddAssignee(ctx, created.Number, login); err != nil {
			return CreateResult{}, partialCreate(issue, CreateStageAssignees, nil, CreatedEdge{}, nil, err)
		}
	}
	issue.Assignees = slices.Clone(assignees)

	var applied []CreatedEdge
	for i, blocker := range blockerRefs {
		if err := t.store.AddBlockedBy(ctx, created.Number, blocker.Number); err != nil {
			failed := CreatedEdge{Kind: BlockedByEdge, To: blocker.Number}
			return CreateResult{}, partialCreate(issue, CreateStageBlockedBy, applied, failed, remainingBlockers(blockerRefs[i+1:]), err)
		}
		applied = append(applied, CreatedEdge{Kind: BlockedByEdge, To: blocker.Number})
		issue.BlockedBy = append(issue.BlockedBy, blocker)
		issue.BlockedByTotal = len(issue.BlockedBy)
	}
	if parentRef != nil {
		if err := t.store.AddSubIssue(ctx, parentRef.Number, created.Number, false); err != nil {
			failed := CreatedEdge{Kind: ParentEdge, To: parentRef.Number}
			return CreateResult{}, partialCreate(issue, CreateStageParent, applied, failed, nil, err)
		}
		applied = append(applied, CreatedEdge{Kind: ParentEdge, To: parentRef.Number})
		issue.Parent = parentRef
		issue.ParentTitle = parentTitle
	}
	return CreateResult{Issue: issueFromModel(issue, true), Edges: applied, Findings: composed.Findings}, nil
}

func (t *Tracker) prepareCreate(ctx context.Context, req CreateRequest) (createPlan, error) {
	title := req.Title
	if err := ValidateTitle(title); err != nil {
		return createPlan{}, err
	}
	if req.Type == PlanTypeEpic {
		return createPlan{}, &InvalidInputError{Field: "type", Reason: "epic issues are created with CreateEpic"}
	}
	if !model.IsType(req.Type) {
		return createPlan{}, &InvalidInputError{Field: "type", Reason: fmt.Sprintf("must be one of %s", strings.Join(model.Types, "|"))}
	}
	priority := model.DefaultPriority
	if req.Priority != "" {
		p, ok := model.ParsePriority(req.Priority)
		if !ok {
			return createPlan{}, &InvalidInputError{Field: "priority", Reason: "must be P0..P4"}
		}
		priority = p
	}
	areas, err := NormalizeAreas(req.Areas)
	if err != nil {
		return createPlan{}, err
	}
	assignees, err := normalizeAssignees(req.Assignees)
	if err != nil {
		return createPlan{}, err
	}
	blockedBy, err := issueNumbers("blockedBy", req.BlockedBy)
	if err != nil {
		return createPlan{}, err
	}
	if req.Parent < 0 {
		return createPlan{}, &InvalidInputError{Field: "parent", Reason: "must be an issue number"}
	}
	if req.DiscoveredFrom < 0 {
		return createPlan{}, &InvalidInputError{Field: "discoveredFrom", Reason: "must be an issue number"}
	}
	discoveredFrom := req.DiscoveredFrom
	if discoveredFrom == 0 {
		discoveredFrom = req.Body.DiscoveredFrom
	} else if req.Body.DiscoveredFrom != 0 && req.Body.DiscoveredFrom != discoveredFrom {
		// The body would name one issue and the request another; picking
		// either silently writes a link the caller did not mean.
		return createPlan{}, &InvalidInputError{Field: "discoveredFrom", Reason: "is set on both the request and its body"}
	}
	body := req.Body
	body.DiscoveredFrom = discoveredFrom
	composed, err := ComposeBody(body)
	if err != nil {
		return createPlan{}, err
	}

	// Every reference is resolved before the first write, so a typo leaves
	// nothing behind. A closed issue is still a valid parent or discovered-from
	// reference: discovery follows a close. A closed blocker is refused, as
	// Block refuses it: it blocks nothing.
	if discoveredFrom > 0 {
		if _, err := t.store.GetIssue(ctx, discoveredFrom); err != nil {
			return createPlan{}, asReadError(err)
		}
	}
	blockerRefs := make([]model.Ref, 0, len(blockedBy))
	for _, number := range blockedBy {
		blocker, err := t.store.GetIssue(ctx, number)
		if err != nil {
			return createPlan{}, asReadError(err)
		}
		if !blocker.IsOpen() {
			return createPlan{}, &ClosedError{Issue: number, Reason: blocker.StateReason}
		}
		blockerRefs = append(blockerRefs, model.Ref{Number: number, State: blocker.State})
	}
	var parentRef *model.Ref
	parentTitle := ""
	if req.Parent > 0 {
		parent, err := t.store.GetIssue(ctx, req.Parent)
		if err != nil {
			return createPlan{}, asReadError(err)
		}
		parentRef = &model.Ref{Number: parent.Number, State: parent.State}
		parentTitle = parent.Title
	}

	labels := append([]string{priority.String(), req.Type}, areas...)
	if len(assignees) > 0 {
		// Assignees is a claim, not a bare assignment: the label is the half
		// that keeps ready and the claim guard agreeing about it.
		labels = append(labels, model.InProgressLabel)
	}
	return createPlan{
		title: title, composed: composed, labels: labels, assignees: assignees,
		blockers: blockerRefs, parent: parentRef, parentTitle: parentTitle,
	}, nil
}

// CheckCreate runs everything Create does before its first write — the field
// validation and the reference reads — and writes nothing. An adapter that
// must collect the body interactively calls it first, so a typo in a reference
// or a flag fails before the author has typed anything. The request's Body is
// validated as given; a caller that has not collected it yet passes the zero
// Body, and Create validates the real one.
//
// Create runs the same preparation, so the pre-flight and the creation cannot
// disagree about what is refused.
func (t *Tracker) CheckCreate(ctx context.Context, req CreateRequest) error {
	_, err := t.prepareCreate(ctx, req)
	return err
}

// createPlan is a validated CreateRequest: everything Create writes, resolved
// before the first write.
type createPlan struct {
	title       string
	composed    ComposedBody
	labels      []string
	assignees   []string
	blockers    []model.Ref
	parent      *model.Ref
	parentTitle string
}

// CreateEpic files a parent issue with the cosmetic Epic title prefix, a
// priority label and the epic label but no type, then attaches its children.
// The epic label is what keeps a childless epic triaged, listed and unworkable
// until its first child lands.
//
// Every reference is read and checked before the epic is filed: a missing
// child, a discovered-from the body names, or a child that already belongs to
// another epic refuses the call with nothing written. A failure after the epic
// exists is a *PartialCreateError naming it, the child that failed, and the
// children already attached.
func (t *Tracker) CreateEpic(ctx context.Context, req CreateEpicRequest) (CreateResult, error) {
	plan, err := t.prepareCreateEpic(ctx, req)
	if err != nil {
		return CreateResult{}, err
	}
	title, composed, childRefs := plan.title, plan.composed, plan.children

	labels := []string{model.DefaultPriority.String(), model.EpicLabel}
	created, err := t.store.CreateIssue(ctx, title, composed.Body, labels)
	if err != nil {
		return CreateResult{}, asWriteError(err)
	}
	issue := createdIssue(created, title, composed.Body, labels)

	var applied []CreatedEdge
	for i, child := range childRefs {
		if err := t.store.AddSubIssue(ctx, created.Number, child.Number, false); err != nil {
			failed := CreatedEdge{Kind: ParentEdge, To: child.Number}
			return CreateResult{}, partialCreate(issue, CreateStageChildren, applied, failed, remainingChildren(childRefs[i+1:]), err)
		}
		applied = append(applied, CreatedEdge{Kind: ParentEdge, To: child.Number})
		issue.SubIssues = append(issue.SubIssues, child)
		issue.SubIssuesTotal = len(issue.SubIssues)
	}
	return CreateResult{Issue: issueFromModel(issue, true), Edges: applied, Findings: composed.Findings}, nil
}

func (t *Tracker) prepareCreateEpic(ctx context.Context, req CreateEpicRequest) (epicPlan, error) {
	title := req.Title
	if strings.TrimSpace(title) == "" {
		return epicPlan{}, &InvalidInputError{Field: "title", Reason: "must not be empty"}
	}
	if !strings.HasPrefix(title, conventions.EpicTitlePrefix) {
		title = conventions.EpicTitlePrefix + title
	}
	if err := ValidateTitle(title); err != nil {
		return epicPlan{}, err
	}
	children, err := issueNumbers("children", req.Children)
	if err != nil {
		return epicPlan{}, err
	}
	composed, err := ComposeBody(req.Body)
	if err != nil {
		return epicPlan{}, err
	}
	if req.Body.DiscoveredFrom > 0 {
		if _, err := t.store.GetIssue(ctx, req.Body.DiscoveredFrom); err != nil {
			return epicPlan{}, asReadError(err)
		}
	}
	childRefs := make([]model.Ref, 0, len(children))
	for _, number := range children {
		child, err := t.store.GetIssue(ctx, number)
		if err != nil {
			return epicPlan{}, asReadError(err)
		}
		if child.Parent != nil {
			return epicPlan{}, &ParentedError{Issue: number, Parent: child.Parent.Number}
		}
		childRefs = append(childRefs, model.Ref{Number: number, State: child.State})
	}

	return epicPlan{title: title, composed: composed, children: childRefs}, nil
}

// CheckCreateEpic is CheckCreate for an epic: the title, children and body
// validation and every reference read CreateEpic does before its first write,
// with nothing written. It exists so an adapter can refuse a missing or
// already-parented child before opening an editor; CreateEpic runs the same
// preparation, so the two cannot drift.
func (t *Tracker) CheckCreateEpic(ctx context.Context, req CreateEpicRequest) error {
	_, err := t.prepareCreateEpic(ctx, req)
	return err
}

// epicPlan is a validated CreateEpicRequest: the prefixed title, the composed
// body and the children, resolved before the first write.
type epicPlan struct {
	title    string
	composed ComposedBody
	children []model.Ref
}

// createdIssue is the create response carrying what the request wrote, which
// is what the result reports. The backend's response is authoritative for the
// number and any label spelling it normalized; the body is this call's own
// input.
func createdIssue(created model.Issue, title, body string, labels []string) model.Issue {
	issue := created
	issue.Title = title
	issue.Body = body
	if len(issue.Labels) == 0 {
		issue.Labels = slices.Clone(labels)
	}
	if issue.State == "" {
		issue.State = "OPEN"
	}
	return issue
}

// partialCreate wraps a follow-up write failure with the issue that already
// exists and the edges that landed around it.
func partialCreate(issue model.Issue, stage CreateStage, applied []CreatedEdge, failed CreatedEdge, rest []CreatedEdge, err error) error {
	return &PartialCreateError{
		Issue:        issueFromModel(issue, true),
		Stage:        stage,
		Applied:      slices.Clone(applied),
		Failed:       failed,
		NotAttempted: slices.Clone(rest),
		Err:          asWriteError(err),
	}
}

// remainingBlockers is the blocked-by edges after the one that failed.
func remainingBlockers(refs []model.Ref) []CreatedEdge {
	out := make([]CreatedEdge, len(refs))
	for i, ref := range refs {
		out[i] = CreatedEdge{Kind: BlockedByEdge, To: ref.Number}
	}
	return out
}

// remainingChildren is the epic child edges after the one that failed.
func remainingChildren(refs []model.Ref) []CreatedEdge {
	out := make([]CreatedEdge, len(refs))
	for i, ref := range refs {
		out[i] = CreatedEdge{Kind: ParentEdge, To: ref.Number}
	}
	return out
}

// issueNumbers validates and de-duplicates a request's issue-number list,
// keeping first-seen order so a repeated reference is fetched and written once.
func issueNumbers(field string, numbers []int) ([]int, error) {
	seen := make(map[int]bool, len(numbers))
	out := make([]int, 0, len(numbers))
	for _, number := range numbers {
		if number <= 0 {
			return nil, &InvalidInputError{Field: field, Reason: "must be positive issue numbers"}
		}
		if !seen[number] {
			seen[number] = true
			out = append(out, number)
		}
	}
	return out, nil
}

// normalizeAssignees trims and de-duplicates logins, refusing a blank one: an
// empty assignee is not a claim and the backend would reject it after the
// issue exists.
func normalizeAssignees(logins []string) ([]string, error) {
	seen := make(map[string]bool, len(logins))
	out := make([]string, 0, len(logins))
	for _, login := range logins {
		login = strings.TrimSpace(login)
		if login == "" {
			return nil, &InvalidInputError{Field: "assignees", Reason: "must not be blank"}
		}
		if !seen[login] {
			seen[login] = true
			out = append(out, login)
		}
	}
	return out, nil
}

// formatEdges renders edges for an error message: "blocked-by #2, parent #7".
func formatEdges(edges []CreatedEdge) string {
	parts := make([]string, len(edges))
	for i, edge := range edges {
		parts[i] = edge.String()
	}
	return strings.Join(parts, ", ")
}
