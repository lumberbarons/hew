package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"

	"github.com/lumberbarons/hew/internal/conventions"
	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/git"
	"github.com/lumberbarons/hew/internal/local"
	"github.com/lumberbarons/hew/internal/model"
	"github.com/lumberbarons/hew/internal/render"
)

// CreateOpts are the create command's inputs.
type CreateOpts struct {
	Title          string
	Type           string
	Priority       string // empty means DefaultPriority
	Areas          []string
	BlockedBy      []int
	Parent         int
	DiscoveredFrom int
	Sections       conventions.Sections
	BodyFile       string
	Edit           bool
}

// Create files a new issue conforming to the conventions: exactly one
// priority and one type label, template-scaffolded body, native
// dependencies and parent links.
func (a *App) Create(ctx context.Context, opts CreateOpts) error {
	if opts.Type == "epic" {
		return usageErr("--type epic is not supported here; use hew epic create")
	}
	if opts.Title == "" {
		return usageErr("--title is required")
	}
	if !model.IsType(opts.Type) {
		return usageErr("--type must be one of %s", strings.Join(model.Types, "|"))
	}
	priority := model.DefaultPriority
	if opts.Priority != "" {
		p, ok := model.ParsePriority(opts.Priority)
		if !ok {
			return usageErr("--priority must be P0..P4")
		}
		priority = p
	}
	if err := validateBodySource(opts.Sections, opts.BodyFile, opts.Edit); err != nil {
		return err
	}
	if err := validateAreas("--area", opts.Areas); err != nil {
		return err
	}

	if opts.DiscoveredFrom > 0 {
		// Before the editor opens or the issue is created, so a typo leaves
		// nothing behind. A closed source is fine: discovery follows a close.
		if _, err := a.requireIssue(ctx, opts.DiscoveredFrom, "--discovered-from"); err != nil {
			return err
		}
	}

	body, err := a.composeBody(bodySource{
		issueType: opts.Type, sections: opts.Sections,
		bodyFile: opts.BodyFile, edit: opts.Edit,
		discoveredFrom: opts.DiscoveredFrom,
	})
	if err != nil {
		return err
	}

	labels := append([]string{priority.String(), opts.Type}, opts.Areas...)
	created, err := a.Client.CreateIssue(ctx, opts.Title, body, labels)
	if err != nil {
		return err
	}
	// A brand-new issue has no dependents, so --blocked-by can't create a
	// cycle; no transitive check needed on this path.
	for _, blocker := range opts.BlockedBy {
		if err := a.Client.AddBlockedBy(ctx, created.Number, blocker); err != nil {
			return fmt.Errorf("created #%d but --blocked-by %d failed: %w", created.Number, blocker, err)
		}
	}
	if opts.Parent > 0 {
		if err := a.Client.AddSubIssue(ctx, opts.Parent, created.Number, false); err != nil {
			return fmt.Errorf("created #%d but --parent %d failed: %w", created.Number, opts.Parent, err)
		}
	}
	return a.reportMutation(ctx, created.Number, "created #%d: %s\n", created.Number, opts.Title)
}

// validateBodySource enforces the body paths' exclusivity: section flags
// compose the template, --body-file supplies long-form text, --edit opens
// the editor — one at a time. Within the section flags, --problem/--goal
// and --fix/--approach are wording pairs: pick the one that fits; word
// choice is never checked against --type.
func validateBodySource(s conventions.Sections, bodyFile string, edit bool) error {
	if s.Problem != "" && s.Goal != "" {
		return usageErr("--problem and --goal are mutually exclusive; pick one wording")
	}
	if s.Fix != "" && s.Approach != "" {
		return usageErr("--fix and --approach are mutually exclusive; pick one wording")
	}
	for _, item := range s.DoneWhen {
		if strings.TrimSpace(item) == "" {
			return usageErr("--done-when items cannot be empty")
		}
	}
	if bodyFile != "" && edit {
		return usageErr("--body-file and --edit are mutually exclusive")
	}
	if !s.IsZero() && (bodyFile != "" || edit) {
		return usageErr("section flags (--where/--problem/--goal/--fix/--approach/--done-when) and --body-file/--edit are mutually exclusive")
	}
	return nil
}

// bodySource is the body input shared by create and epic create.
type bodySource struct {
	issueType      string // seeds the --edit skeleton; empty means goal/approach wording
	sections       conventions.Sections
	bodyFile       string
	edit           bool
	discoveredFrom int
}

func (a *App) composeBody(src bodySource) (string, error) {
	body, err := a.renderBody(src)
	if err != nil {
		return "", err
	}
	a.warnUnmarkedCode(body, "hew set <n> --body-file")
	return body, nil
}

// renderBody is composeBody without the markup warning, so the local draft
// preflight shares the exact composition create publishes and supplies its
// own remedy.
func (a *App) renderBody(src bodySource) (string, error) {
	body := ""
	switch {
	case !src.sections.IsZero():
		body = src.sections.Compose()
	case src.bodyFile != "":
		b, err := os.ReadFile(src.bodyFile)
		if err != nil {
			return "", genericErr("cannot read --body-file: %v", err)
		}
		body = string(b)
	case src.edit:
		if a.Edit == nil {
			return "", genericErr("--edit is not available here")
		}
		edited, err := a.Edit(conventions.TemplateSkeleton(src.issueType))
		if err != nil {
			return "", genericErr("editor failed: %v", err)
		}
		body = conventions.StripEmptySections(edited)
	}
	if src.discoveredFrom > 0 {
		link := conventions.DiscoveredFrom(src.discoveredFrom)
		if body == "" {
			body = link
		} else {
			body = strings.TrimRight(body, "\n") + "\n\n" + link
		}
	}
	return body, nil
}

// warnUnmarkedCode reports code-shaped text the author left as prose. The
// convention is stated in help text, but help text is read by someone who
// goes looking for it and the agent composing a body generally does not —
// so the tool says it at the moment the body is written, and names the
// remedy rather than only the rule.
func (a *App) warnUnmarkedCode(body, remedy string) {
	tokens := conventions.UnmarkedCodeText(body)
	if len(tokens) == 0 {
		return
	}
	a.warnf("not in code spans: %s — wrap them (and any command around them) in backticks: %s",
		conventions.FormatUnmarkedCodeText(tokens), remedy)
}

// formatStateReason renders GitHub's close-reason enum (COMPLETED,
// NOT_PLANNED, DUPLICATE) as the prose the CLI prints.
func formatStateReason(reason string) string {
	return strings.ToLower(strings.ReplaceAll(reason, "_", " "))
}

// closeState renders " (completed)" for an issue whose close reason GitHub
// recorded, and nothing for one closed before that field existed — "#20 is
// closed ()" reads as a bug in the tool rather than a gap in the data.
func closeState(issue model.Issue) string {
	if issue.StateReason == "" {
		return ""
	}
	return " (" + formatStateReason(issue.StateReason) + ")"
}

// guardClosed refuses a write aimed at a closed issue. Editing one is almost
// always stale state rather than intent — the case this comes from is an issue
// closed by a PR merge mid-session, quietly absorbing a later edit that nobody
// noticed until much later. The close state is in the message because that is
// what separates "the target moved under me" from "I typed the wrong number".
// The override exists because a guard with no escape hatch just moves the rare
// deliberate edit to the web UI, where none of the conventions are enforced.
func guardClosed(issue model.Issue, allowClosed bool) error {
	if issue.IsOpen() || allowClosed {
		return nil
	}
	return genericErr("#%d is closed%s — pass --closed to edit anyway", issue.Number, closeState(issue))
}

// claimRefusal turns a tripped claim guard into the exit code that tells the
// caller what to do about it: someone else's claim is exit 3 (pick the next
// ready item), your own is exit 5 (resume the work you already claimed). The
// viewer lookup lives here rather than in Start's happy path so the refusal,
// not every claim, pays for the extra call.
func (a *App) claimRefusal(ctx context.Context, issue model.Issue) error {
	if len(issue.Assignees) == 0 {
		// In-progress with nobody assigned: no claimant to attribute.
		return &ExitError{Code: ExitClaimed, Message: fmt.Sprintf("#%d already claimed (in-progress); pick the next ready item or --force", issue.Number)}
	}
	who := "assigned to @" + render.SanitizeInline(strings.Join(issue.Assignees, " @"))
	viewer, err := a.Client.Viewer(ctx)
	switch {
	case err != nil:
		// The refusal stands regardless of whose claim it is; only the
		// refinement is lost, so degrade to the conservative signal.
		a.warnf("cannot resolve the authenticated user (%v); reporting #%d as someone else's claim", err, issue.Number)
	case slices.Contains(issue.Assignees, viewer):
		return &ExitError{Code: ExitClaimedByYou, Message: fmt.Sprintf("#%d is already claimed by you (%s); resume that work or --force to re-claim", issue.Number, who)}
	}
	return &ExitError{Code: ExitClaimed, Message: fmt.Sprintf("#%d already claimed (%s); pick the next ready item or --force", issue.Number, who)}
}

// blockerRefusal keeps start in step with ready: an issue with an open
// blocker is hidden from ready, so claiming it by id would take work whose
// prerequisite is unfinished.
func blockerRefusal(issue model.Issue) error {
	open := issue.OpenBlockers()
	if len(open) == 0 {
		return nil
	}
	refs := make([]string, len(open))
	for i, n := range open {
		refs[i] = fmt.Sprintf("#%d", n)
	}
	return &ExitError{Code: ExitBlocked, Message: fmt.Sprintf("#%d is blocked by open %s; pick the next ready item or --force", issue.Number, strings.Join(refs, ", "))}
}

// StartOpts are the claim inputs; zero values mean the default behavior.
type StartOpts struct {
	// Priority is required when the issue is untriaged (claiming is triage).
	Priority string
	// Force steals an already-claimed issue and claims one that has open
	// blockers.
	Force bool
	// Show follows the claim confirmation with the issue's detail view, so
	// claiming and reading are one call. --json already includes the body
	// (as every mutation's JSON does, without text findings), so it ignores
	// this.
	Show bool
}

// Start claims an issue: assign @me plus the in-progress label. The guard
// refuses issues that are already assigned or in-progress — exit 3 for
// someone else's claim so an agent loop moves on to the next ready item,
// exit 5 when the claim is already yours; --force steals. It also refuses
// an issue with open blockers (exit 6), which ready would have hidden;
// --force overrides that too. Claiming an
// untriaged issue requires --priority — claiming forces triage.
func (a *App) Start(ctx context.Context, number int, opts StartOpts) error {
	priorityFlag, force := opts.Priority, opts.Force
	issue, err := a.Client.GetIssue(ctx, number)
	if err != nil {
		return err
	}
	if !issue.IsOpen() {
		return genericErr("#%d is closed", number)
	}
	if issue.IsEpic() {
		return genericErr("#%d is an epic; epics are never worked directly — start one of its sub-issues", number)
	}
	if (len(issue.Assignees) > 0 || issue.InProgress()) && !force {
		return a.claimRefusal(ctx, issue)
	}
	if !force {
		if err := blockerRefusal(issue); err != nil {
			return err
		}
	}
	priority, _ := issue.Priority()
	if priorityFlag != "" {
		p, ok := model.ParsePriority(priorityFlag)
		if !ok {
			return usageErr("--priority must be P0..P4")
		}
		priority = p
	} else if priority == model.PriorityUnknown {
		return usageErr("#%d is untriaged; start requires --priority (claiming is triage)", number)
	}
	// A capped blocker list can hide an open blocker, so the check cannot
	// vouch for it; say so once the claim is certain to be attempted.
	if !force && issue.BlockedByTotal > len(issue.BlockedBy) {
		a.warnf("#%d has %d blockers, only %d fetched; an open blocker may be hidden", number, issue.BlockedByTotal, len(issue.BlockedBy))
	}

	viewer, err := a.Client.Viewer(ctx)
	if err != nil {
		return err
	}
	// The local store can make the claim conditional: one locked write checks
	// and claims, so simultaneous starts have exactly one winner. Claim
	// before the priority swap so a loser has not already changed the issue.
	// GitHub has no conditional writes, so it falls through to the
	// check-then-act path and the re-read after claiming.
	if claimer, ok := a.Client.(gh.Claimer); ok {
		if err := claimer.ClaimIssue(ctx, number, viewer, force); err != nil {
			if errors.Is(err, gh.ErrClaimConflict) {
				current, readErr := a.Client.GetIssue(ctx, number)
				if readErr != nil {
					return readErr
				}
				return a.claimRefusal(ctx, current)
			}
			return err
		}
		if err := a.swapPriority(ctx, issue, priority); err != nil {
			// Claiming is triage: a claim without the priority would leave the
			// issue half-started, and a retry would then be told it is already
			// the caller's (exit 5) instead of finishing the job.
			return errors.Join(err, a.undoClaim(ctx, issue, viewer, force))
		}
	} else {
		if err := a.swapPriority(ctx, issue, priority); err != nil {
			return err
		}
		if force && len(issue.Assignees) > 0 {
			if err := a.Client.RemoveAssignees(ctx, number, issue.Assignees); err != nil {
				return err
			}
		}
		if !issue.InProgress() {
			if err := a.Client.AddLabels(ctx, number, []string{model.InProgressLabel}); err != nil {
				return err
			}
		}
		if err := a.Client.AddAssignee(ctx, number, viewer); err != nil {
			return err
		}
	}
	after, err := a.Client.GetIssue(ctx, number)
	if err != nil {
		return err
	}
	if len(after.Assignees) != 1 || after.Assignees[0] != viewer {
		a.warnf("claim may have raced: #%d now assigned to @%s", number, render.SanitizeInline(strings.Join(after.Assignees, " @")))
	}
	if opts.Show && !a.JSON {
		// The detail view leads with the issue's own line, title included,
		// so the confirmation shrinks to the claim itself.
		a.printf("started #%d\n", number)
		return a.emitIssue(after, nil)
	}
	return a.emitMutation(after, "started #%d: %s\n", number, render.SanitizeInline(issue.Title))
}

// SetOpts are the retriage/edit inputs; zero values mean "leave alone".
type SetOpts struct {
	Priority    string
	Type        string
	AddAreas    []string
	RemoveAreas []string
	Parent      int
	NoParent    bool
	Title       string
	BodyFile    string
	AllowClosed bool
}

func (o SetOpts) empty() bool {
	return o.Priority == "" && o.Type == "" && len(o.AddAreas) == 0 &&
		len(o.RemoveAreas) == 0 && o.Parent == 0 && !o.NoParent && o.Title == "" &&
		o.BodyFile == ""
}

// Set retriages or edits within the conventions, swapping the old
// priority/type label rather than stacking a second one. It refuses a closed
// target unless AllowClosed is set.
func (a *App) Set(ctx context.Context, number int, opts SetOpts) error {
	if opts.empty() {
		return usageErr("nothing to change; pass --priority, --type, --add-area, --remove-area, --parent, --no-parent, --title, or --body-file")
	}
	if opts.Parent > 0 && opts.NoParent {
		return usageErr("--parent and --no-parent are mutually exclusive")
	}
	// Validate every flag up front: Set applies several mutations in
	// sequence, so a usage error discovered mid-way would exit 2 ("nothing
	// happened") after earlier changes had already been written.
	var priority model.Priority
	if opts.Priority != "" {
		p, ok := model.ParsePriority(opts.Priority)
		if !ok {
			return usageErr("--priority must be P0..P4")
		}
		priority = p
	}
	if opts.Type != "" && !model.IsType(opts.Type) {
		return usageErr("--type must be one of %s", strings.Join(model.Types, "|"))
	}
	if err := validateAreas("--add-area", opts.AddAreas); err != nil {
		return err
	}
	if err := validateAreas("--remove-area", opts.RemoveAreas); err != nil {
		return err
	}
	// Read the replacement body before any mutation: a --body-file typo
	// would otherwise surface after the label swaps had already landed.
	var body string
	if opts.BodyFile != "" {
		b, err := a.composeBody(bodySource{bodyFile: opts.BodyFile})
		if err != nil {
			return err
		}
		if strings.TrimSpace(b) == "" {
			return usageErr("--body-file %s is empty; set does not blank an issue body", opts.BodyFile)
		}
		body = b
	}
	issue, err := a.Client.GetIssue(ctx, number)
	if err != nil {
		return err
	}
	// The pre-mutation read is already here, so the guard costs no extra call.
	// It has to run before the first step below: Set applies its changes in
	// sequence, and a refusal discovered halfway would leave a partial edit on
	// an issue that should not have been touched at all.
	if err := guardClosed(issue, opts.AllowClosed); err != nil {
		return err
	}

	// Track applied changes so a later failure reports what already landed
	// rather than looking like a clean no-op.
	var applied []string
	step := func(name string, err error) error {
		if err == nil {
			applied = append(applied, name)
			return nil
		}
		if len(applied) == 0 {
			return err
		}
		return fmt.Errorf("#%d partially updated (applied %s); %s failed: %w",
			number, strings.Join(applied, ", "), name, err)
	}

	if opts.Priority != "" {
		if err := step("priority", a.swapPriority(ctx, issue, priority)); err != nil {
			return err
		}
	}
	if opts.Type != "" {
		if err := step("type", a.swapType(ctx, issue, opts.Type)); err != nil {
			return err
		}
	}
	if len(opts.AddAreas) > 0 {
		if err := step("add-area", a.Client.AddLabels(ctx, number, opts.AddAreas)); err != nil {
			return err
		}
	}
	for _, area := range opts.RemoveAreas {
		if err := step("remove-area", a.Client.RemoveLabel(ctx, number, area)); err != nil {
			return err
		}
	}
	if opts.Title != "" {
		if err := step("title", a.Client.EditTitle(ctx, number, opts.Title)); err != nil {
			return err
		}
	}
	if opts.BodyFile != "" {
		if err := step("body", a.Client.EditBody(ctx, number, body)); err != nil {
			return err
		}
	}
	if opts.Parent > 0 {
		// AddSubIssue with replace moves the issue when it already has one.
		if err := step("parent", a.Client.AddSubIssue(ctx, opts.Parent, number, true)); err != nil {
			return err
		}
	}
	if opts.NoParent {
		if issue.Parent == nil {
			a.warnf("#%d has no parent; --no-parent is a no-op", number)
		} else {
			if err := step("no-parent", a.Client.RemoveSubIssue(ctx, issue.Parent.Number, number)); err != nil {
				return err
			}
		}
	}
	return a.reportMutation(ctx, number, "updated #%d\n", number)
}

// validateAreas refuses area names that collide with the priority/type
// vocabulary: passing them through verbatim would stack a second convention
// label (or strip the only one), breaking the exactly-one invariant the
// write path exists to enforce.
func validateAreas(flag string, areas []string) error {
	for _, area := range areas {
		if _, ok := model.ParsePriority(area); ok {
			return usageErr("%s %q is a priority label; use --priority", flag, area)
		}
		if model.IsType(area) {
			return usageErr("%s %q is a type label; use --type", flag, area)
		}
	}
	return nil
}

// swapType enforces the one-type-label invariant: remove the others, add the
// target if absent.
func (a *App) swapType(ctx context.Context, issue model.Issue, typ string) error {
	for _, l := range issue.Labels {
		if model.IsType(l) && l != typ {
			if err := a.Client.RemoveLabel(ctx, issue.Number, l); err != nil {
				return err
			}
		}
	}
	if !slices.Contains(issue.Labels, typ) {
		return a.Client.AddLabels(ctx, issue.Number, []string{typ})
	}
	return nil
}

// Close comments the reason and closes: not-planned unless --completed or
// --duplicate-of. Closing via PR is the norm; this is for wontfix/duplicate.
func (a *App) Close(ctx context.Context, number int, reason string, completed bool, duplicateOf int) error {
	if completed && duplicateOf > 0 {
		return usageErr("--completed and --duplicate-of are mutually exclusive")
	}
	if duplicateOf == number {
		return usageErr("--duplicate-of #%d is the issue being closed", number)
	}
	stateReason := gh.CloseNotPlanned
	switch {
	case completed:
		stateReason = gh.CloseCompleted
	case duplicateOf > 0:
		stateReason = gh.CloseDuplicate
		if reason == "" {
			reason = fmt.Sprintf("Duplicate of #%d", duplicateOf)
		}
	}
	if reason == "" {
		return usageErr("--reason is required")
	}
	issue, err := a.Client.GetIssue(ctx, number)
	if err != nil {
		return err
	}
	if !issue.IsOpen() {
		// No override here: re-closing a closed issue has no effect to
		// authorize. Naming the existing state is the whole remedy — it says
		// whether the issue was completed or dropped, which is what decides
		// if this call was a mistake.
		return genericErr("#%d is already closed%s", number, closeState(issue))
	}
	if duplicateOf > 0 {
		// Before the comment, so a typo leaves the issue open and untouched.
		if _, err := a.requireIssue(ctx, duplicateOf, "--duplicate-of"); err != nil {
			return err
		}
		// A custom reason is free text; the original must stay findable from
		// the closed duplicate, which stores it nowhere but the comment.
		if !mentionsIssue(reason, duplicateOf) {
			reason = strings.TrimRight(reason, " \t\r\n") + fmt.Sprintf("\n\nDuplicate of #%d", duplicateOf)
		}
	}
	if err := a.Client.Comment(ctx, number, reason); err != nil {
		return err
	}
	if err := a.Client.CloseIssue(ctx, number, stateReason); err != nil {
		// The reason comment already posted; flag it so a retry isn't read as
		// a clean redo — re-running would post the comment a second time.
		return fmt.Errorf("posted the reason comment on #%d but closing it failed (a retry will comment again): %w", number, err)
	}
	return a.reportMutation(ctx, number, "closed #%d (%s)\n", number, formatStateReason(string(stateReason)))
}

// requireIssue resolves an issue number the caller names from another issue's
// text or state, so a typo fails before anything is written rather than
// leaving a reference that points nowhere. The error keeps its chain, so an
// auth failure still maps to its exit code.
func (a *App) requireIssue(ctx context.Context, number int, flag string) (model.Issue, error) {
	issue, err := a.Client.GetIssue(ctx, number)
	if err != nil {
		return model.Issue{}, fmt.Errorf("%s: %w", flag, err)
	}
	return issue, nil
}

// mentionsIssue reports whether text already names #number as a reference of
// this repository: not a longer number that merely starts with it, and not
// the tail of another repository's owner/repo#number.
func mentionsIssue(text string, number int) bool {
	ref := fmt.Sprintf("#%d", number)
	for from := 0; ; {
		i := strings.Index(text[from:], ref)
		if i < 0 {
			return false
		}
		start, end := from+i, from+i+len(ref)
		from = end
		if start > 0 && (text[start-1] == '/' || isWordByte(text[start-1])) {
			continue
		}
		if end < len(text) && isWordByte(text[end]) {
			continue
		}
		return true
	}
}

func isWordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// Reopen is close's inverse: comment the reason, reopen the issue. Closing
// comments routinely encode the condition to reopen on ("reopen only if #24
// stalls"), and acting on one should keep close's comment-plus-state-change
// shape rather than degrade to a bare state flip. No retriage — the issue
// rejoins list/triage/ready with the priority, type, and area labels it
// already had.
//
// A claim is the one thing reopen does clear. Neither close nor a PR merge
// removes the in-progress label or the assignee, so most issues closed in
// the normal workflow still carry the claim that produced the fix. Left in
// place on a reopened issue it makes the claim guard lie: exit 5 tells the
// old owner to resume merged work, exit 3 tells everyone else the issue is
// taken, and ready hides it. Whoever wants it back runs start.
func (a *App) Reopen(ctx context.Context, number int, reason string) error {
	if reason == "" {
		return usageErr("--reason is required")
	}
	issue, err := a.Client.GetIssue(ctx, number)
	if err != nil {
		return err
	}
	if issue.IsOpen() {
		// The requested state already holds, so this is not an error to
		// report: erroring would make an idempotent retry look like a
		// failure. Commenting is skipped with it — the note would document a
		// reopen that never happened.
		return a.emitNoOp(issue, "#%d is already open\n", number)
	}
	if err := a.Client.Comment(ctx, number, reason); err != nil {
		return err
	}
	if err := a.Client.ReopenIssue(ctx, number); err != nil {
		// The reason comment already posted; flag it so a retry isn't read as
		// a clean redo — re-running would post the comment a second time.
		return fmt.Errorf("posted the reason comment on #%d but reopening it failed (a retry will comment again): %w", number, err)
	}
	released, err := a.releaseClaim(ctx, issue)
	if err != nil {
		// The reopen landed; only the release failed. A retry is a no-op
		// reopen and will not clear it, so name the remedy.
		return fmt.Errorf("reopened #%d but releasing its stale claim failed (it is open and still claimed; hew start --force takes it): %w", number, err)
	}
	if released == "" {
		return a.reportMutation(ctx, number, "reopened #%d\n", number)
	}
	return a.reportMutation(ctx, number, "reopened #%d (released %s)\n", number, released)
}

// releaseClaim drops the in-progress label and every assignee from issue and
// returns a description of what was released, or "" if it was not claimed.
// The label goes first: if the assignee removal then fails the issue is
// still claimed either way, and the error path reports exactly that.
func (a *App) releaseClaim(ctx context.Context, issue model.Issue) (string, error) {
	if !issue.Claimed() {
		return "", nil
	}
	if issue.InProgress() {
		if err := a.Client.RemoveLabel(ctx, issue.Number, model.InProgressLabel); err != nil {
			return "", err
		}
	}
	if len(issue.Assignees) == 0 {
		return "stale in-progress label", nil
	}
	if err := a.Client.RemoveAssignees(ctx, issue.Number, issue.Assignees); err != nil {
		return "", err
	}
	return "@" + render.SanitizeInline(strings.Join(issue.Assignees, " and @")) + "'s stale claim", nil
}

// Block adds a native dependency after a transitive client-side cycle
// check — GitHub itself only rejects self-blocks and direct two-issue
// cycles. It refuses a closed target unless allowClosed is set; a closed
// blocker is refused outright, since closed blockers don't block.
func (a *App) Block(ctx context.Context, number, blocker int, allowClosed bool) error {
	issues, err := a.Client.ListIssues(ctx, openStates)
	if err != nil {
		return err
	}
	byNum := model.ByNumber(issues)
	issue, ok := byNum[number]
	if !ok {
		// Absent from the open set means closed or nonexistent, and the old
		// message conflated the two. Re-read to tell them apart; only this
		// path pays for the extra call, so the common case stays one query.
		target, getErr := a.Client.GetIssue(ctx, number)
		if getErr != nil {
			return genericErr("#%d is not an open issue in %s", number, a.Repo)
		}
		if err := guardClosed(target, allowClosed); err != nil {
			return err
		}
		// Overridden: a closed issue is absent from the open-issue graph, so
		// no open blocked-by edge can reach it and the cycle check below is
		// vacuously safe for it.
		issue = target
	}
	if _, ok := byNum[blocker]; !ok {
		return genericErr("#%d is not an open issue in %s; closed blockers don't block", blocker, a.Repo)
	}
	if slices.Contains(issue.OpenBlockers(), blocker) {
		return a.reportNoOp(ctx, number, "#%d is already blocked by #%d\n", number, blocker)
	}
	check := model.CheckBlockedBy(issues, number, blocker)
	if check.Cycle != nil {
		return genericErr("refusing: would create dependency cycle %s", render.FormatCycle(check.Cycle))
	}
	if !check.Verifiable {
		return genericErr("refusing: cannot verify #%d → #%d is cycle-free because some issues have more blockers than were fetched; reduce blockers on the issues involved and retry", number, blocker)
	}
	if err := a.Client.AddBlockedBy(ctx, number, blocker); err != nil {
		return err
	}
	return a.reportMutation(ctx, number, "blocked #%d on #%d\n", number, blocker)
}

// Unblock removes a dependency. It refuses a closed target unless allowClosed
// is set.
func (a *App) Unblock(ctx context.Context, number, blocker int, allowClosed bool) error {
	issue, err := a.Client.GetIssue(ctx, number)
	if err != nil {
		return err
	}
	if err := guardClosed(issue, allowClosed); err != nil {
		return err
	}
	found := false
	for _, b := range issue.BlockedBy {
		if b.Number == blocker {
			found = true
		}
	}
	if !found {
		return a.emitNoOp(issue, "#%d is not blocked by #%d\n", number, blocker)
	}
	if err := a.Client.RemoveBlockedBy(ctx, number, blocker); err != nil {
		return err
	}
	return a.reportMutation(ctx, number, "unblocked #%d from #%d\n", number, blocker)
}

// EpicCreateOpts are the epic create inputs. The body paths match create:
// section flags, --body-file, or --edit.
type EpicCreateOpts struct {
	Title    string
	Children []int
	Sections conventions.Sections
	BodyFile string
	Edit     bool
}

// EpicCreate files a parent issue and attaches existing children. Epics
// get the cosmetic title prefix and a priority label but no type — they
// are containers, not work.
func (a *App) EpicCreate(ctx context.Context, opts EpicCreateOpts) error {
	if opts.Title == "" {
		return usageErr("--title is required")
	}
	if err := validateBodySource(opts.Sections, opts.BodyFile, opts.Edit); err != nil {
		return err
	}
	title := opts.Title
	if !strings.HasPrefix(title, conventions.EpicTitlePrefix) {
		title = conventions.EpicTitlePrefix + title
	}
	body, err := a.composeBody(bodySource{
		sections: opts.Sections, bodyFile: opts.BodyFile, edit: opts.Edit,
	})
	if err != nil {
		return err
	}
	created, err := a.Client.CreateIssue(ctx, title, body, []string{model.DefaultPriority.String()})
	if err != nil {
		return err
	}
	for _, child := range opts.Children {
		if err := a.Client.AddSubIssue(ctx, created.Number, child, false); err != nil {
			return fmt.Errorf("created epic #%d but attaching #%d failed: %w", created.Number, child, err)
		}
	}
	return a.reportMutation(ctx, created.Number, "created epic #%d: %s (%d children)\n", created.Number, title, len(opts.Children))
}

// initResult is what `init` reports under --json, on either backend: the
// backend that answered, where it keeps the issues (empty on GitHub), and the
// convention labels it had to create.
type initResult struct {
	Backend       string   `json:"backend"`
	Path          string   `json:"path"`
	CreatedLabels []string `json:"createdLabels"`
}

// Init bootstraps the convention labels in the repo and prints the
// CLAUDE.md snippet. On the local backend there are no labels to bootstrap:
// the store carries them, so init reports the tracker instead of pretending a
// local repo is GitHub.
func (a *App) Init(ctx context.Context) error {
	if a.Local {
		return a.InitLocal("")
	}
	existing, err := a.Client.ListLabels(ctx)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, l := range existing {
		have[l.Name] = true
	}
	var created []string
	for _, l := range conventions.Labels {
		if have[l.Name] {
			continue
		}
		if err := a.Client.CreateLabel(ctx, gh.Label{Name: l.Name, Color: l.Color, Description: l.Description}); err != nil {
			return fmt.Errorf("creating label %q: %w", l.Name, err)
		}
		created = append(created, l.Name)
	}
	if created == nil {
		created = []string{}
	}
	return a.emitResult(initResult{Backend: local.BackendGitHub, CreatedLabels: created}, func() {
		if len(created) == 0 {
			a.printf("all convention labels already exist in %s\n", a.Repo)
		} else {
			a.printf("created labels: %s\n", strings.Join(created, ", "))
		}
		a.printClaudeSnippet(a.claudeSnippet())
	})
}

// claudeSnippet is the CLAUDE.md text for the backend this App talks to.
func (a *App) claudeSnippet() string {
	if a.Local {
		return conventions.ClaudeSnippetLocal
	}
	return conventions.ClaudeSnippet
}

func (a *App) printClaudeSnippet(snippet string) {
	a.printf("\nAdd to CLAUDE.md:\n\n%s\n", snippet)
	a.printf("\nOr let a hook inject the primer automatically: hew hooks install <claude|codex|cursor|opencode>\n")
}

// InitLocal creates the local tracker for the checkout containing dir (the
// working directory when empty) and prints the matching CLAUDE.md snippet. The
// tracker lives at the main checkout, so running it from a linked worktree
// sets up the one every worktree shares. It is safe to repeat, and refuses a
// repo whose tracker has moved to another backend rather than reporting a
// local one that no command would use.
func (a *App) InitLocal(dir string) error {
	tracker, err := local.MainDir(dir)
	if git.IsNotRepo(err) {
		return genericErr("the local tracker needs a git checkout (run `git init` first): %v", err)
	}
	if err != nil {
		return genericErr("%v", err)
	}
	// Read before writing: a repo that has moved to another backend must be
	// refused without touching its archived tracker.
	fresh := false
	switch cfg, err := local.ReadConfig(tracker); {
	case err == nil && cfg.Backend != local.BackendLocal:
		return genericErr("%s already tracks issues in %q, not locally", tracker, cfg.Backend)
	case errors.Is(err, fs.ErrNotExist):
		fresh = true
	case err != nil:
		return genericErr("%v", err)
	}
	if err := local.Init(tracker); err != nil {
		return genericErr("creating %s: %v", tracker, err)
	}
	if fresh {
		// Nothing outside .hew records that this repo is local, so a deleted
		// .hew (git clean -fdx) is indistinguishable from a repo that never
		// opted in, and hew then targets the git remote's GitHub issues. Said
		// once, when the tracker is created, and on stderr so --json callers see it.
		a.warnf("%s is untracked and is the only marker that this repo is local; if it is deleted (for example by `git clean -fdx` at the main checkout), hew falls back to the git remote's GitHub issues, if there is one", tracker)
	}
	return a.emitResult(initResult{Backend: local.BackendLocal, Path: tracker, CreatedLabels: []string{}}, func() {
		a.printf("local tracker ready in %s\n", tracker)
		a.printClaudeSnippet(conventions.ClaudeSnippetLocal)
	})
}

// swapPriority enforces the one-priority-label invariant: remove the
// others, add the target if absent.
// undoClaim undoes an atomic claim back to the state before: issue is the
// snapshot start read before claiming. It reports its own failure so the
// caller can show both, since a failed rollback leaves the claim in place.
func (a *App) undoClaim(ctx context.Context, issue model.Issue, viewer string, force bool) error {
	var errs []error
	if !slices.Contains(issue.Assignees, viewer) {
		errs = append(errs, a.Client.RemoveAssignees(ctx, issue.Number, []string{viewer}))
	}
	if force {
		// The forced claim displaced the earlier claimants; put them back.
		for _, who := range issue.Assignees {
			if who != viewer {
				errs = append(errs, a.Client.AddAssignee(ctx, issue.Number, who))
			}
		}
	}
	if !issue.InProgress() {
		errs = append(errs, a.Client.RemoveLabel(ctx, issue.Number, model.InProgressLabel))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("could not release the claim on #%d: %w", issue.Number, err)
	}
	return nil
}

func (a *App) swapPriority(ctx context.Context, issue model.Issue, p model.Priority) error {
	target := p.String()
	for _, l := range issue.Labels {
		if _, ok := model.ParsePriority(l); ok && l != target {
			if err := a.Client.RemoveLabel(ctx, issue.Number, l); err != nil {
				return err
			}
		}
	}
	if !slices.Contains(issue.Labels, target) {
		return a.Client.AddLabels(ctx, issue.Number, []string{target})
	}
	return nil
}

// reportNoOp is reportMutation for a write that changed nothing: text, or a
// re-fetch so --json carries the same full detail a changed write does. Block
// needs it because its issue comes from the open-issue listing, which is
// leaner than GetIssue's.
func (a *App) reportNoOp(ctx context.Context, number int, format string, args ...any) error {
	if !a.JSON {
		a.printf(format, args...)
		return nil
	}
	after, err := a.Client.GetIssue(ctx, number)
	if err != nil {
		return fmt.Errorf("#%d needed no change, but fetching it for --json failed: %w", number, err)
	}
	return a.emitNoOp(after, format, args...)
}

// reportMutation prints the text confirmation, or re-fetches for the full
// flat schema when --json is on.
func (a *App) reportMutation(ctx context.Context, number int, format string, args ...any) error {
	if !a.JSON {
		a.printf(format, args...)
		return nil
	}
	after, err := a.Client.GetIssue(ctx, number)
	if err != nil {
		// The mutation itself succeeded; only the confirmation re-fetch
		// failed. Say so, so a caller doesn't read a non-zero exit as
		// "the change didn't happen" and retry into a duplicate.
		return fmt.Errorf("#%d was updated, but fetching the result for --json failed: %w", number, err)
	}
	return a.emitMutation(after, format, args...)
}
