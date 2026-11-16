package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/lumberbarons/hew/internal/conventions"
	"github.com/lumberbarons/hew/internal/render"
	"github.com/lumberbarons/hew/tracker"
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
// dependencies and parent links. The decisions live in tracker.Create; what
// stays here is the flag-level body input, the flag spelling of its refusals,
// the markup warning, and rendering the result.
func (a *App) Create(ctx context.Context, opts CreateOpts) error {
	if opts.Type == "epic" {
		return usageErr("--type epic is not supported here; use hew epic create")
	}
	if err := validateBodySource(opts.Sections, opts.BodyFile, opts.Edit); err != nil {
		return err
	}
	tr := tracker.WrapAs(a.Client, a.backend(), "")
	req := tracker.CreateRequest{
		Title:          opts.Title,
		Type:           opts.Type,
		Priority:       opts.Priority,
		Areas:          opts.Areas,
		Parent:         opts.Parent,
		BlockedBy:      opts.BlockedBy,
		DiscoveredFrom: opts.DiscoveredFrom,
	}
	if opts.Edit {
		// A typo in a flag or a reference must not cost the author the body they
		// are about to type, so everything Create would refuse is refused before
		// the editor opens.
		if err := tr.CheckCreate(ctx, req); err != nil {
			return createError(opts, err)
		}
	}
	body, err := a.bodyInput(bodySource{
		issueType: opts.Type, sections: opts.Sections,
		bodyFile: opts.BodyFile, edit: opts.Edit,
	})
	if err != nil {
		return err
	}
	req.Body = body
	result, err := tr.Create(ctx, req)
	if err != nil {
		return createError(opts, err)
	}
	a.warnFindings(result.Findings, "hew set <n> --body-file")
	return a.reportMutation(ctx, result.Issue.Number, "created #%d: %s\n", result.Issue.Number, result.Issue.Title)
}

// creationInputError spells a library field rejection in the create flags. The
// rule is shared; the flag names belong to the command surface.
func creationInputError(invalid *tracker.InvalidInputError) error {
	switch invalid.Field {
	case "title":
		if invalid.Reason == "must not be empty" {
			return usageErr("--title is required")
		}
		return usageErr("--title %s", invalid.Reason)
	case "type":
		return usageErr("--type %s", invalid.Reason)
	case "priority":
		return usageErr("--priority %s", invalid.Reason)
	case "areas":
		return usageErr("--area %s", invalid.Reason)
	case "problem":
		return usageErr("--problem and --goal are mutually exclusive; pick one wording")
	case "fix":
		return usageErr("--fix and --approach are mutually exclusive; pick one wording")
	case "done-when":
		return usageErr("--done-when items cannot be empty")
	}
	return usageErr("%s", invalid.Reason)
}

// createError maps tracker.Create's typed refusals onto the create command's
// flags and exit codes. A missing reference names the flag whose number it was,
// in the order Create reads them; the partial-create error names the issue that
// already exists so the operator can finish by hand.
func createError(req CreateOpts, err error) error {
	var invalid *tracker.InvalidInputError
	if errors.As(err, &invalid) {
		return creationInputError(invalid)
	}
	var notFound *tracker.NotFoundError
	if errors.As(err, &notFound) {
		switch {
		case req.DiscoveredFrom > 0 && notFound.Issue == req.DiscoveredFrom:
			return fmt.Errorf("--discovered-from: %w", err)
		case slices.Contains(req.BlockedBy, notFound.Issue):
			return fmt.Errorf("--blocked-by: %w", err)
		case req.Parent > 0 && notFound.Issue == req.Parent:
			return fmt.Errorf("--parent: %w", err)
		}
		return err
	}
	var closed *tracker.ClosedError
	if errors.As(err, &closed) {
		return genericErr("--blocked-by: #%d is closed%s; closed blockers don't block", closed.Issue, closedState(closed.Reason))
	}
	var partial *tracker.PartialCreateError
	if errors.As(err, &partial) {
		switch partial.Stage {
		case tracker.CreateStageBlockedBy:
			return fmt.Errorf("created #%d but --blocked-by %d failed: %w", partial.Issue.Number, partial.Failed.To, partial.Err)
		case tracker.CreateStageParent:
			return fmt.Errorf("created #%d but --parent %d failed: %w", partial.Issue.Number, partial.Failed.To, partial.Err)
		case tracker.CreateStageAssignees:
			return fmt.Errorf("created #%d but claiming it for its assignees failed: %w", partial.Issue.Number, partial.Err)
		}
		return fmt.Errorf("created #%d but finishing it failed: %w", partial.Issue.Number, partial.Err)
	}
	return err
}

// validateBodySource enforces the body paths' exclusivity: section flags
// compose the template, --body-file supplies long-form text, --edit opens
// the editor — one at a time. The section rules themselves live in tracker;
// this renders their rejections in flag terms.
func validateBodySource(s conventions.Sections, bodyFile string, edit bool) error {
	if err := validateSections(s); err != nil {
		return err
	}
	if bodyFile != "" && edit {
		return usageErr("--body-file and --edit are mutually exclusive")
	}
	if !s.IsZero() && (bodyFile != "" || edit) {
		return usageErr("section flags (--where/--problem/--goal/--fix/--approach/--done-when) and --body-file/--edit are mutually exclusive")
	}
	return nil
}

// validateSections runs the library's section rules and spells the rejection
// with the flags that carried the input: the rule is shared, the flag spelling
// belongs to the command surface.
func validateSections(s conventions.Sections) error {
	err := tracker.ValidateSections(tracker.Sections(s))
	if err == nil {
		return nil
	}
	var invalid *tracker.InvalidInputError
	if !errors.As(err, &invalid) {
		return err
	}
	switch invalid.Field {
	case "problem":
		return usageErr("--problem and --goal are mutually exclusive; pick one wording")
	case "fix":
		return usageErr("--fix and --approach are mutually exclusive; pick one wording")
	case "done-when":
		return usageErr("--done-when items cannot be empty")
	}
	return usageErr("%s", invalid.Reason)
}

// bodySource is the body input shared by create and epic create.
type bodySource struct {
	issueType string // seeds the --edit skeleton; empty means goal/approach wording
	sections  conventions.Sections
	bodyFile  string
	edit      bool
}

func (a *App) composeBody(src bodySource) (string, error) {
	composed, err := a.renderBody(src)
	if err != nil {
		return "", err
	}
	a.warnFindings(composed.Findings, "hew set <n> --body-file")
	return composed.Body, nil
}

// renderBody is composeBody without the markup warning, so the local draft
// preflight shares the exact composition create publishes and supplies its
// own remedy. Reading the chosen file or launching the editor is the
// adapter's; composing and checking the bytes is the library's.
func (a *App) renderBody(src bodySource) (tracker.ComposedBody, error) {
	body, err := a.bodyInput(src)
	if err != nil {
		return tracker.ComposedBody{}, err
	}
	composed, err := tracker.ComposeBody(body)
	if err != nil {
		return tracker.ComposedBody{}, usageErr("%s", bodyErrorReason(err))
	}
	return composed, nil
}

// bodyInput reads the body source the flags selected without composing it:
// section flags hand the structured sections over, --body-file supplies the
// text, and --edit supplies the editor's output. The library operation
// composes and validates the body and reports its findings, so a create and a
// direct library caller share one implementation.
func (a *App) bodyInput(src bodySource) (tracker.Body, error) {
	body := tracker.Body{Sections: tracker.Sections(src.sections)}
	switch {
	case src.sections.IsZero() && src.bodyFile != "":
		b, err := os.ReadFile(src.bodyFile)
		if err != nil {
			return tracker.Body{}, genericErr("cannot read --body-file: %v", err)
		}
		body.Text = string(b)
	case src.sections.IsZero() && src.edit:
		if a.Edit == nil {
			return tracker.Body{}, genericErr("--edit is not available here")
		}
		edited, err := a.Edit(conventions.TemplateSkeleton(src.issueType))
		if err != nil {
			return tracker.Body{}, genericErr("editor failed: %v", err)
		}
		body.Text = conventions.StripEmptySections(edited)
	}
	return body, nil
}

// bodyErrorReason renders a composition refusal, which is always the
// library's typed invalid input: a conflict validateBodySource would have
// caught is the backstop, not a separate failure mode.
func bodyErrorReason(err error) string {
	var invalid *tracker.InvalidInputError
	if errors.As(err, &invalid) {
		return invalid.Reason
	}
	return err.Error()
}

// warnFindings reports advisory body findings in the command's words. The
// library keeps the fact (an unmarked code token); the sentence and remedy are
// the CLI's. The convention is stated in help text, but help text is read by
// someone who goes looking and the agent composing a body generally does not,
// so the tool says it at the moment the body is written.
func (a *App) warnFindings(findings []tracker.Finding, remedy string) {
	for _, finding := range findings {
		if finding.Kind != tracker.FindingUnmarkedCode {
			continue
		}
		a.warnf("not in code spans: %s — wrap them (and any command around them) in backticks: %s",
			conventions.FormatUnmarkedCodeText(finding.Tokens), remedy)
	}
}

// unmarkedCode is the --json view of the findings: the code-shaped tokens in
// order, and an empty array rather than null when there are none.
func unmarkedCode(findings []tracker.Finding) []string {
	tokens := []string{}
	for _, finding := range findings {
		if finding.Kind == tracker.FindingUnmarkedCode {
			tokens = append(tokens, finding.Tokens...)
		}
	}
	return tokens
}

// formatStateReason renders GitHub's close-reason enum (COMPLETED,
// NOT_PLANNED, DUPLICATE) as the prose the CLI prints.
func formatStateReason(reason string) string {
	return strings.ToLower(strings.ReplaceAll(reason, "_", " "))
}

// closedState renders " (completed)" for a raw backend state reason, such as the one a
// typed tracker error carries.
func closedState(reason string) string {
	if reason == "" {
		return ""
	}
	return " (" + formatStateReason(reason) + ")"
}

// startRefusal maps tracker's typed start refusals back to the exit codes
// agent loops branch on and to the message text the CLI has always printed:
// someone else's claim is exit 3 (pick the next ready item), your own is exit
// 5 (resume the work you already claimed), open blockers are exit 6 (pick the
// next ready item). Everything else keeps the exit code it had: closed and
// epic are generic, a missing priority is usage.
func (a *App) startRefusal(err error) error {
	var claimed *tracker.ClaimedError
	var blocked *tracker.BlockedError
	var closed *tracker.ClosedError
	var epic *tracker.EpicError
	var untriaged *tracker.UntriagedError
	var invalid *tracker.InvalidInputError
	switch {
	case errors.As(err, &claimed):
		if claimed.Err != nil {
			// The refusal stands regardless of whose claim it is; only the
			// refinement is lost, so degrade to the conservative signal.
			a.warnf("cannot resolve the authenticated user (%v); reporting #%d as someone else's claim", claimed.Err, claimed.Issue)
		}
		if len(claimed.Assignees) == 0 {
			// In-progress with nobody assigned: no claimant to attribute.
			return &ExitError{Code: ExitClaimed, Message: fmt.Sprintf("#%d already claimed (in-progress); pick the next ready item or --force", claimed.Issue)}
		}
		who := "assigned to @" + render.SanitizeInline(strings.Join(claimed.Assignees, " @"))
		if claimed.ByActor {
			return &ExitError{Code: ExitClaimedByYou, Message: fmt.Sprintf("#%d is already claimed by you (%s); resume that work or --force to re-claim", claimed.Issue, who)}
		}
		return &ExitError{Code: ExitClaimed, Message: fmt.Sprintf("#%d already claimed (%s); pick the next ready item or --force", claimed.Issue, who)}
	case errors.As(err, &blocked):
		refs := make([]string, len(blocked.OpenBlockers))
		for i, n := range blocked.OpenBlockers {
			refs[i] = fmt.Sprintf("#%d", n)
		}
		return &ExitError{Code: ExitBlocked, Message: fmt.Sprintf("#%d is blocked by open %s; pick the next ready item or --force", blocked.Issue, strings.Join(refs, ", "))}
	case errors.As(err, &closed):
		return genericErr("#%d is closed%s", closed.Issue, closedState(closed.Reason))
	case errors.As(err, &epic):
		return genericErr("#%d is an epic; epics are never worked directly — start one of its sub-issues", epic.Issue)
	case errors.As(err, &untriaged):
		return usageErr("#%d is untriaged; start requires --priority (claiming is triage)", untriaged.Issue)
	case errors.As(err, &invalid) && invalid.Field == "priority":
		return usageErr("--priority must be P0..P4")
	}
	return err
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

// Start claims an issue: assign @me plus the in-progress label. The decision
// lives in tracker.Start; what stays here is the CLI contract it cannot carry:
// the guard's refusals map to exit 3 for someone else's claim (pick the next
// ready item), exit 5 when the claim is already yours (resume), and exit 6 for
// open blockers ready would have hidden; --force overrides both. Claiming an
// untriaged issue requires --priority — claiming forces triage.
func (a *App) Start(ctx context.Context, number int, opts StartOpts) error {
	// No actor: tracker.Start resolves the store's Viewer when the claim needs
	// it, so a closed or already-claimed target is refused without paying for
	// the lookup.
	result, err := tracker.WrapAs(a.Client, a.backend(), "").Start(ctx, number, tracker.StartOptions{
		Priority: opts.Priority,
		Force:    opts.Force,
	})
	if err != nil {
		return a.startRefusal(err)
	}
	after := issueFromTracker(result.Issue)
	// A capped blocker list can hide an open blocker, so the guard could not
	// vouch for it; say so once the claim has gone through.
	if !opts.Force && after.BlockedByTotal > len(after.BlockedBy) {
		a.warnf("#%d has %d blockers, only %d fetched; an open blocker may be hidden", number, after.BlockedByTotal, len(after.BlockedBy))
	}
	if len(after.Assignees) != 1 || after.Assignees[0] != result.Claimant {
		a.warnf("claim may have raced: #%d now assigned to @%s", number, render.SanitizeInline(strings.Join(after.Assignees, " @")))
	}
	if opts.Show && !a.JSON {
		// The detail view leads with the issue's own line, title included,
		// so the confirmation shrinks to the claim itself.
		a.printf("started #%d\n", number)
		return a.emitIssue(after, nil)
	}
	return a.emitMutation(after, "started #%d: %s\n", number, render.SanitizeInline(after.Title))
}

// backend names the store the App is wired to, for the tracker seam.
func (a *App) backend() tracker.Backend {
	if a.Local {
		return tracker.BackendLocal
	}
	return tracker.BackendGitHub
}

// Release gives up a claim without assigning it to another owner. The
// decision lives in tracker.Release; what stays here is the CLI's wording for
// its refusals.
func (a *App) Release(ctx context.Context, number int, force bool) error {
	if err := tracker.WrapAs(a.Client, a.backend(), "").Release(ctx, number, tracker.ReleaseOptions{Force: force}); err != nil {
		return a.releaseError(number, err)
	}
	return a.reportMutation(ctx, number, "released #%d\n", number)
}

// releaseError maps tracker's release refusals onto the command's text. Every
// refusal stays a generic failure; a partial release keeps its cause in the
// chain and names the remedy.
func (a *App) releaseError(number int, err error) error {
	var notClaimed *tracker.NotClaimedError
	var notOwner *tracker.NotOwnerError
	var partial *tracker.PartialReleaseError
	switch {
	case errors.As(err, &notClaimed):
		return genericErr("#%d is not claimed", number)
	case errors.As(err, &notOwner):
		return genericErr("#%d is not claimed solely by you; pass --force to release it", number)
	case errors.As(err, &partial):
		return fmt.Errorf("releasing #%d failed (its claim may be partially cleared; inspect with hew show %d before retrying): %w", number, number, partial.Err)
	}
	return err
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
	// TitleSet records that --title was passed, so an explicitly empty one is
	// an error rather than the same thing as leaving the title alone.
	TitleSet    bool
	BodyFile    string
	AllowClosed bool
}

// Set retriages or edits within the conventions. tracker.Edit owns the field
// validation, the priority/type swaps, the parent move and the partial-failure
// report; what stays here is reading the body file, the flag spelling of a
// refusal and the exit codes.
func (a *App) Set(ctx context.Context, number int, opts SetOpts) error {
	if opts.Parent > 0 && opts.NoParent {
		return usageErr("--parent and --no-parent are mutually exclusive")
	}
	req := tracker.EditRequest{
		Issue:       number,
		AddAreas:    opts.AddAreas,
		RemoveAreas: opts.RemoveAreas,
		NoParent:    opts.NoParent,
		AllowClosed: opts.AllowClosed,
	}
	if opts.Priority != "" {
		req.Priority = &opts.Priority
	}
	if opts.Type != "" {
		req.Type = &opts.Type
	}
	if opts.TitleSet || opts.Title != "" {
		req.Title = &opts.Title
	}
	if opts.Parent > 0 {
		req.Parent = &opts.Parent
	}
	// The replacement body is adapter input: read and compose it before the
	// library call, so a --body-file typo cannot land after the label swaps.
	if opts.BodyFile != "" {
		body, err := a.composeBody(bodySource{bodyFile: opts.BodyFile})
		if err != nil {
			return err
		}
		if strings.TrimSpace(body) == "" {
			return usageErr("--body-file %s is empty; set does not blank an issue body", opts.BodyFile)
		}
		req.Body = &body
	}

	result, err := tracker.WrapAs(a.Client, a.backend(), "").Edit(ctx, req)
	if err != nil {
		return setError(err)
	}
	if result.ParentNoop {
		a.warnf("#%d has no parent; --no-parent is a no-op", number)
	}
	return a.reportMutation(ctx, number, "updated #%d\n", number)
}

// setError maps tracker.Edit's typed refusals onto the command's flags and
// exit codes: the field mistakes are usage errors, a closed target names its
// override, and a partial edit keeps the applied-field report.
func setError(err error) error {
	var invalid *tracker.InvalidInputError
	if errors.As(err, &invalid) {
		switch invalid.Field {
		case "edit":
			return usageErr("nothing to change; pass --priority, --type, --add-area, --remove-area, --parent, --no-parent, --title, or --body-file")
		case "priority", "type", "title":
			return usageErr("--%s %s", invalid.Field, invalid.Reason)
		case "addAreas":
			return usageErr("--add-area %s", invalid.Reason)
		case "removeAreas":
			return usageErr("--remove-area %s", invalid.Reason)
		case "body":
			return usageErr("--body-file %s", invalid.Reason)
		default:
			return usageErr("--%s %s", invalid.Field, invalid.Reason)
		}
	}
	var closed *tracker.ClosedError
	if errors.As(err, &closed) {
		return genericErr("#%d is closed%s — pass --closed to edit anyway", closed.Issue, closedState(closed.Reason))
	}
	var partial *tracker.PartialEditError
	if errors.As(err, &partial) {
		applied := make([]string, len(partial.Applied))
		for i, field := range partial.Applied {
			applied[i] = editFieldFlag(field)
		}
		return fmt.Errorf("#%d partially updated (applied %s); %s failed: %w",
			partial.Issue, strings.Join(applied, ", "), editFieldFlag(partial.Failed), partial.Err)
	}
	return err
}

// editFieldFlag spells a tracker.EditRequest field the way the command's flags
// do, so a partial-update report names options a caller can actually retry.
func editFieldFlag(field string) string {
	switch field {
	case "addAreas":
		return "add-area"
	case "removeAreas":
		return "remove-area"
	case "noParent":
		return "no-parent"
	}
	return field
}

// Close comments the reason and closes: not-planned unless --completed or
// --duplicate-of. Closing via PR is the norm; this is for wontfix/duplicate.
// An epic with open sub-issues is refused without force: closing one strands
// the children under a closed parent, and the guard names the flag rather
// than trusting a warning to be read. The decisions live in tracker.Close;
// what stays here is the CLI's wording and exit codes for its refusals.
func (a *App) Close(ctx context.Context, number int, reason string, completed bool, duplicateOf int, force bool) error {
	result, err := tracker.WrapAs(a.Client, a.backend(), "").Close(ctx, number, tracker.CloseOptions{
		Reason:      reason,
		Completed:   completed,
		DuplicateOf: duplicateOf,
		Force:       force,
	})
	if err != nil {
		return closeError(number, duplicateOf, err)
	}
	return a.reportMutation(ctx, number, "closed #%d (%s)\n", number, formatStateReason(result.StateReason))
}

// closeError maps tracker's close refusals onto the command's exit codes and
// text: the option mistakes are usage errors, the rest generic failures. A
// duplicate target that does not exist is named with its flag, as it was when
// the command did the lookup itself.
func closeError(number, duplicateOf int, err error) error {
	var invalid *tracker.InvalidInputError
	var closed *tracker.ClosedError
	var children *tracker.OpenChildrenError
	var notFound *tracker.NotFoundError
	var partial *tracker.PartialCloseError
	switch {
	case errors.As(err, &invalid):
		switch invalid.Field {
		case "completed":
			return usageErr("--completed and --duplicate-of are mutually exclusive")
		case "duplicateOf":
			if duplicateOf == number {
				return usageErr("--duplicate-of #%d is the issue being closed", number)
			}
			return usageErr("--duplicate-of must be a positive issue number")
		case "reason":
			return usageErr("--reason is required")
		}
	case errors.As(err, &closed):
		// Naming the existing state is the whole remedy: it says whether the
		// issue was completed or dropped, which decides if this call was a
		// mistake.
		return genericErr("#%d is already closed%s", closed.Issue, closedState(closed.Reason))
	case errors.As(err, &children):
		unit := "sub-issues"
		if children.Open == 1 {
			unit = "sub-issue"
		}
		return genericErr("#%d is an epic with %d open %s; pass --force to close it anyway", children.Issue, children.Open, unit)
	case errors.As(err, &notFound) && duplicateOf > 0 && notFound.Issue == duplicateOf:
		return fmt.Errorf("--duplicate-of: %w", err)
	case errors.As(err, &partial):
		// The reason comment already posted; flag it so a retry isn't read as
		// a clean redo — re-running would post the comment a second time.
		return fmt.Errorf("posted the reason comment on #%d but closing it failed (a retry will comment again): %w", number, partial.Err)
	}
	return err
}

// Reopen is close's inverse: comment the reason, reopen the issue. The
// decisions — the comment-plus-state-change shape, the no-op on an already open
// issue, and clearing the stale claim the close left behind — live in
// tracker.Reopen. What stays here is the command's wording for what was
// released and for its typed refusals.
func (a *App) Reopen(ctx context.Context, number int, reason string) error {
	result, err := tracker.WrapAs(a.Client, a.backend(), "").Reopen(ctx, number, tracker.ReopenOptions{Reason: reason})
	if err != nil {
		return reopenError(number, err)
	}
	if !result.Reopened {
		// The requested state already holds, so this is not an error to
		// report: erroring would make an idempotent retry look like a
		// failure. Commenting is skipped with it — the note would document a
		// reopen that never happened.
		return a.reportNoOp(ctx, number, "#%d is already open\n", number)
	}
	if !result.ReleasedLabel && len(result.ReleasedAssignees) == 0 {
		return a.reportMutation(ctx, number, "reopened #%d\n", number)
	}
	return a.reportMutation(ctx, number, "reopened #%d (released %s)\n", number, releasedClaim(result))
}

// releasedClaim renders the halves tracker.Reopen cleared in the command's
// wording; sanitizing a backend-supplied login stays with the renderer.
func releasedClaim(result tracker.ReopenResult) string {
	if len(result.ReleasedAssignees) == 0 {
		return "stale in-progress label"
	}
	return "@" + render.SanitizeInline(strings.Join(result.ReleasedAssignees, " and @")) + "'s stale claim"
}

// reopenError maps tracker.Reopen's typed outcomes onto the command's text:
// the missing reason is a usage error, and each partial stage names the state
// that survived so a retry is not read as a clean redo.
func reopenError(number int, err error) error {
	var invalid *tracker.InvalidInputError
	if errors.As(err, &invalid) && invalid.Field == "reason" {
		return usageErr("--reason is required")
	}
	var partial *tracker.PartialReopenError
	if errors.As(err, &partial) {
		if partial.Stage == tracker.ReopenStageClaim {
			return fmt.Errorf("reopened #%d but releasing its stale claim failed (it is open and still claimed; hew release %d --force clears it): %w", number, number, partial.Err)
		}
		return fmt.Errorf("posted the reason comment on #%d but reopening it failed (a retry will comment again): %w", number, partial.Err)
	}
	return err
}

// Block adds native dependencies through tracker, which owns the transitive
// client-side cycle check — GitHub itself only rejects self-blocks and direct
// two-issue cycles. The typed refusal is rendered here in the command's own
// text; the library call is the same one any in-process consumer makes.
func (a *App) Block(ctx context.Context, number int, blockers []int, allowClosed bool) error {
	if len(blockers) == 0 {
		return usageErr("usage: hew block <n> --on <m>")
	}
	result, err := tracker.WrapWriter(a.Client).Block(ctx, number, blockers, tracker.BlockOptions{AllowClosed: allowClosed})
	if err != nil {
		return a.blockError(err)
	}
	if len(result.Added) == 0 {
		return a.reportNoOp(ctx, number, "#%d is already blocked by %s\n", number, commaRefs(result.Existing))
	}
	note := ""
	if len(result.Existing) > 0 {
		note = "; already blocked by " + commaRefs(result.Existing)
	}
	return a.reportMutation(ctx, number, "blocked #%d on %s%s\n", number, commaRefs(result.Added), note)
}

// blockError maps tracker's dependency refusals onto the command's existing
// text. The numbers come from the typed error, so a consumer and the CLI read
// the same fields instead of parsing a message.
func (a *App) blockError(err error) error {
	var cycle *tracker.CycleError
	if errors.As(err, &cycle) {
		return genericErr("refusing: would create dependency cycle %s", render.FormatCycle(cycle.Cycle))
	}
	var unverifiable *tracker.UnverifiableCycleError
	if errors.As(err, &unverifiable) {
		return genericErr("refusing: cannot verify #%d → #%d is cycle-free because some issues have more blockers than were fetched; reduce blockers on the issues involved and retry", unverifiable.Issue, unverifiable.Blocker)
	}
	var closed *tracker.ClosedError
	if errors.As(err, &closed) {
		return genericErr("#%d is closed%s — pass --closed to edit anyway", closed.Issue, closedState(closed.Reason))
	}
	var notOpen *tracker.NotOpenError
	if errors.As(err, &notOpen) {
		return genericErr("#%d is not an open issue in %s; closed blockers don't block", notOpen.Blocker, a.Repo)
	}
	var notFound *tracker.NotFoundError
	if errors.As(err, &notFound) {
		return genericErr("#%d is not an open issue in %s", notFound.Issue, a.Repo)
	}
	var partial *tracker.PartialWriteError
	if errors.As(err, &partial) {
		return dependencyPartialError(partial, "blocked", "on", "blocking on")
	}
	return err
}

// Unblock removes dependencies through tracker. It refuses a closed target
// unless allowClosed is set; a blocker that is not there is reported as a
// no-op or named alongside the ones that were removed.
func (a *App) Unblock(ctx context.Context, number int, blockers []int, allowClosed bool) error {
	if len(blockers) == 0 {
		return usageErr("usage: hew unblock <n> --from <m>")
	}
	result, err := tracker.WrapWriter(a.Client).Unblock(ctx, number, blockers, tracker.BlockOptions{AllowClosed: allowClosed})
	if err != nil {
		return a.unblockError(err)
	}
	if len(result.Removed) == 0 {
		return a.reportNoOp(ctx, number, "#%d is not blocked by %s\n", number, commaRefs(result.Missing))
	}
	note := ""
	if len(result.Missing) > 0 {
		note = "; not blocked by " + commaRefs(result.Missing)
	}
	return a.reportMutation(ctx, number, "unblocked #%d from %s%s\n", number, commaRefs(result.Removed), note)
}

// unblockError is blockError's Unblock half: the refusals the library adds
// here are the closed target and a partial write.
func (a *App) unblockError(err error) error {
	var closed *tracker.ClosedError
	if errors.As(err, &closed) {
		return genericErr("#%d is closed%s — pass --closed to edit anyway", closed.Issue, closedState(closed.Reason))
	}
	var partial *tracker.PartialWriteError
	if errors.As(err, &partial) {
		return dependencyPartialError(partial, "unblocked", "from", "unblocking from")
	}
	return err
}

// dependencyPartialError reports the edge write that failed after earlier
// edges in the same call landed, so a caller neither retries edges that exist
// nor assumes the missing ones do not. The verbs differ by operation; the
// shape is the same.
func dependencyPartialError(err *tracker.PartialWriteError, landed, preposition, doing string) error {
	head := fmt.Sprintf("already %s #%d %s %s, but %s #%d failed", landed, err.Issue, preposition, commaRefs(err.Landed), doing, err.Failed)
	tail := ""
	if len(err.NotAttempted) > 0 {
		tail = "; not attempted: " + commaRefs(err.NotAttempted)
	}
	return fmt.Errorf("%s: %w%s", head, err.Err, tail)
}

// commaRefs formats issue numbers as "#2, #3" for a sentence.
func commaRefs(numbers []int) string {
	parts := make([]string, len(numbers))
	for i, n := range numbers {
		parts[i] = fmt.Sprintf("#%d", n)
	}
	return strings.Join(parts, ", ")
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
// get the cosmetic title prefix, a priority label and the epic label but no
// type — they are containers, not work. The epic label is what keeps a
// childless epic triaged, listed and unworkable until its first child lands.
// The decisions live in tracker.CreateEpic; what stays here is the body input,
// the flag spelling of its refusals, and rendering the result.
func (a *App) EpicCreate(ctx context.Context, opts EpicCreateOpts) error {
	if err := validateBodySource(opts.Sections, opts.BodyFile, opts.Edit); err != nil {
		return err
	}
	tr := tracker.WrapAs(a.Client, a.backend(), "")
	req := tracker.CreateEpicRequest{Title: opts.Title, Children: opts.Children}
	if opts.Edit {
		// As in Create: refuse a missing or already-parented child before the
		// editor opens, not after the body has been typed.
		if err := tr.CheckCreateEpic(ctx, req); err != nil {
			return epicCreateError(opts, err)
		}
	}
	body, err := a.bodyInput(bodySource{
		sections: opts.Sections, bodyFile: opts.BodyFile, edit: opts.Edit,
	})
	if err != nil {
		return err
	}
	req.Body = body
	result, err := tr.CreateEpic(ctx, req)
	if err != nil {
		return epicCreateError(opts, err)
	}
	a.warnFindings(result.Findings, "hew set <n> --body-file")
	return a.reportMutation(ctx, result.Issue.Number, "created epic #%d: %s (%d children)\n",
		result.Issue.Number, result.Issue.Title, len(result.Edges))
}

// epicCreateError maps tracker.CreateEpic's typed refusals onto the epic
// create command's flags. A child that already has a parent names the remedy,
// because detaching is the caller's deliberate next step rather than something
// the command should do for them.
func epicCreateError(req EpicCreateOpts, err error) error {
	var invalid *tracker.InvalidInputError
	if errors.As(err, &invalid) {
		return creationInputError(invalid)
	}
	var notFound *tracker.NotFoundError
	if errors.As(err, &notFound) {
		if slices.Contains(req.Children, notFound.Issue) {
			return fmt.Errorf("--children: %w", err)
		}
		return err
	}
	var parented *tracker.ParentedError
	if errors.As(err, &parented) {
		return genericErr("--children: #%d already has a parent (#%d); detach it first with `hew set %d --no-parent`",
			parented.Issue, parented.Parent, parented.Issue)
	}
	var partial *tracker.PartialCreateError
	if errors.As(err, &partial) && partial.Stage == tracker.CreateStageChildren {
		return fmt.Errorf("created epic #%d but attaching #%d failed: %w", partial.Issue.Number, partial.Failed.To, partial.Err)
	}
	return err
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
// tracker.Init reports the tracker instead of pretending a local repo is
// GitHub, and the adapter keeps only the snippet and the rendering.
func (a *App) Init(ctx context.Context) error {
	if a.Local {
		return a.InitLocal(ctx, "")
	}
	res, err := tracker.WrapAs(a.Client, tracker.BackendGitHub, "").Init(ctx)
	if err != nil {
		return initError(err)
	}
	return a.emitResult(initResult{
		Backend:       string(res.Backend),
		Path:          res.Path,
		CreatedLabels: res.CreatedLabels,
	}, func() {
		if len(res.CreatedLabels) == 0 {
			a.printf("all convention labels already exist in %s\n", a.Repo)
		} else {
			a.printf("created labels: %s\n", strings.Join(res.CreatedLabels, ", "))
		}
		a.printClaudeSnippet(a.claudeSnippet())
	})
}

// initError maps tracker.Init's typed refusals onto the messages and exit codes
// init has always used. An authentication failure passes through untouched so
// ExitCode can still see it; a tracker that belongs to another backend keeps its
// wording; a worktree or backend the library rejects stays a generic failure,
// because init's target is a repository state rather than a flag value.
func initError(err error) error {
	var auth *tracker.AuthError
	if errors.As(err, &auth) {
		return err
	}
	var elsewhere *tracker.InitializedElsewhereError
	if errors.As(err, &elsewhere) {
		return genericErr("%s already tracks issues in %q, not locally", elsewhere.Path, string(elsewhere.Backend))
	}
	var invalid *tracker.InvalidInputError
	if errors.As(err, &invalid) {
		return genericErr("%s", invalid.Reason)
	}
	return genericErr("%v", err)
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
// local one that no command would use. tracker.Init owns the workflow; the
// adapter keeps resolving the working directory, the warnings and the snippet.
func (a *App) InitLocal(ctx context.Context, dir string) error {
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return genericErr("cannot determine the working directory: %v", err)
		}
		dir = wd
	}
	res, err := tracker.Init(ctx, tracker.InitConfig{Worktree: dir, Backend: tracker.BackendLocal})
	if err != nil {
		return initError(err)
	}
	for _, notice := range res.Notices {
		a.warnf("%s", notice)
	}
	return a.emitResult(initResult{
		Backend:       string(res.Backend),
		Path:          res.Path,
		CreatedLabels: res.CreatedLabels,
	}, func() {
		a.printf("local tracker ready in %s\n", res.Path)
		a.printClaudeSnippet(conventions.ClaudeSnippetLocal)
	})
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
