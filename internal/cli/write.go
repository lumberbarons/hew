package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/lumberbarons/hew/internal/conventions"
	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/git"
	"github.com/lumberbarons/hew/internal/local"
	"github.com/lumberbarons/hew/internal/model"
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
// dependencies and parent links.
func (a *App) Create(ctx context.Context, opts CreateOpts) error {
	if opts.Type == "epic" {
		return usageErr("--type epic is not supported here; use hew epic create")
	}
	if strings.TrimSpace(opts.Title) == "" {
		return usageErr("--title is required")
	}
	if err := validateTitle(opts.Title); err != nil {
		return err
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
	areas, err := normalizeAreas("--area", opts.Areas)
	if err != nil {
		return err
	}

	// Every reference is resolved before the editor opens or the issue is
	// created, so a typo leaves nothing behind. A closed issue is still a
	// valid --discovered-from or --parent reference: discovery follows a close.
	// A closed blocker is refused, as it is by block: it blocks nothing. A
	// reference can still vanish between this read and the mutation — GitHub
	// has no transaction spanning them — so the write paths keep naming the
	// created issue on that residual failure.
	if opts.DiscoveredFrom > 0 {
		if _, err := a.requireIssues(ctx, "--discovered-from", []int{opts.DiscoveredFrom}); err != nil {
			return err
		}
	}
	blockedBy := uniqueInts(opts.BlockedBy)
	blockers, err := a.requireIssues(ctx, "--blocked-by", blockedBy)
	if err != nil {
		return err
	}
	for _, blocker := range blockers {
		if !blocker.IsOpen() {
			return genericErr("--blocked-by: #%d is closed%s; closed blockers don't block", blocker.Number, closeState(blocker))
		}
	}
	if opts.Parent > 0 {
		if _, err := a.requireIssues(ctx, "--parent", []int{opts.Parent}); err != nil {
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

	labels := append([]string{priority.String(), opts.Type}, areas...)
	created, err := a.Client.CreateIssue(ctx, opts.Title, body, labels)
	if err != nil {
		return err
	}
	// A brand-new issue has no dependents, so --blocked-by can't create a
	// cycle; no transitive check needed on this path.
	for _, blocker := range blockedBy {
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
	issueType      string // seeds the --edit skeleton; empty means goal/approach wording
	sections       conventions.Sections
	bodyFile       string
	edit           bool
	discoveredFrom int
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
	body := tracker.Body{
		Sections:       tracker.Sections(src.sections),
		DiscoveredFrom: src.discoveredFrom,
	}
	switch {
	case src.sections.IsZero() && src.bodyFile != "":
		b, err := os.ReadFile(src.bodyFile)
		if err != nil {
			return tracker.ComposedBody{}, genericErr("cannot read --body-file: %v", err)
		}
		body.Text = string(b)
	case src.sections.IsZero() && src.edit:
		if a.Edit == nil {
			return tracker.ComposedBody{}, genericErr("--edit is not available here")
		}
		edited, err := a.Edit(conventions.TemplateSkeleton(src.issueType))
		if err != nil {
			return tracker.ComposedBody{}, genericErr("editor failed: %v", err)
		}
		body.Text = conventions.StripEmptySections(edited)
	}
	composed, err := tracker.ComposeBody(body)
	if err != nil {
		return tracker.ComposedBody{}, usageErr("%s", bodyErrorReason(err))
	}
	return composed, nil
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

// closeState renders " (completed)" for an issue whose close reason GitHub
// recorded, and nothing for one closed before that field existed — "#20 is
// closed ()" reads as a bug in the tool rather than a gap in the data.
func closeState(issue model.Issue) string { return closedState(issue.StateReason) }

// closedState is closeState for a raw backend state reason, such as the one a
// typed tracker error carries.
func closedState(reason string) string {
	if reason == "" {
		return ""
	}
	return " (" + formatStateReason(reason) + ")"
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

func (o SetOpts) empty() bool {
	return o.Priority == "" && o.Type == "" && len(o.AddAreas) == 0 &&
		len(o.RemoveAreas) == 0 && o.Parent == 0 && !o.NoParent && o.Title == "" && !o.TitleSet &&
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
	if opts.TitleSet || opts.Title != "" {
		if err := validateTitle(opts.Title); err != nil {
			return err
		}
	}
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
	addAreas, err := normalizeAreas("--add-area", opts.AddAreas)
	if err != nil {
		return err
	}
	// Removal must remain available for oversized labels older local
	// trackers accepted, so they can be repaired before migrating to GitHub.
	if err := validateAreaLabels("--remove-area", opts.RemoveAreas); err != nil {
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
	if len(addAreas) > 0 {
		if err := step("add-area", a.Client.AddLabels(ctx, number, addAreas)); err != nil {
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

// validateTitle checks the stored title, including any cosmetic epic prefix.
// The rule lives in tracker so every writer shares it; this maps the library's
// typed rejection onto the command's flag-named usage error.
func validateTitle(title string) error {
	if err := tracker.ValidateTitle(title); err != nil {
		var invalid *tracker.InvalidInputError
		if errors.As(err, &invalid) {
			return usageErr("--title %s", invalid.Reason)
		}
		return err
	}
	return nil
}

// normalizeAreas returns the area names trimmed of surrounding whitespace and
// de-duplicated, or a usage error. The rules — no blank name, at most 50
// characters, no comma, no convention label — live in tracker, so a plan or a
// direct library caller cannot store an area the CLI would refuse. The area
// flags split on commas before this runs, so the comma rule guards the callers
// that hand over a name verbatim — `apply` plans and direct API use — while
// `--area x,y` stays two areas.
func normalizeAreas(flag string, areas []string) ([]string, error) {
	trimmed, err := tracker.NormalizeAreas(areas)
	if err != nil {
		var invalid *tracker.InvalidInputError
		if errors.As(err, &invalid) {
			return nil, usageErr("%s %s", flag, invalid.Reason)
		}
		return nil, err
	}
	return trimmed, nil
}

// validateAreaLabels checks names that must not be convention labels, without
// the trim, length or comma rules normalizeAreas applies: a removal has to be
// able to name an oversized area an older tracker stored, so it can be
// repaired before a local migration.
func validateAreaLabels(flag string, areas []string) error {
	for _, area := range areas {
		if err := tracker.ValidateAreaLabel(area); err != nil {
			var invalid *tracker.InvalidInputError
			if errors.As(err, &invalid) {
				return usageErr("%s %s", flag, invalid.Reason)
			}
			return err
		}
	}
	return nil
}

// swapType enforces the one-type-label invariant: remove the others, add the
// target if absent.
func (a *App) swapType(ctx context.Context, issue model.Issue, typ string) error {
	for _, l := range issue.Labels {
		if _, ok := model.LabelType(l); ok && !strings.EqualFold(l, typ) {
			if err := a.Client.RemoveLabel(ctx, issue.Number, l); err != nil {
				return err
			}
		}
	}
	if !issue.HasLabel(typ) {
		return a.Client.AddLabels(ctx, issue.Number, []string{typ})
	}
	return nil
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

// requireIssues resolves the issues a reference flag names, in order, so a
// typo fails before anything is written rather than leaving a reference that
// points nowhere. The resolved issues are returned so a caller can apply a
// rule beyond existence. Keeping the loop here is what stops the next
// reference flag from repeating #232: the flag stays an argument instead of a
// pre-check each command has to remember.
func (a *App) requireIssues(ctx context.Context, flag string, numbers []int) ([]model.Issue, error) {
	issues := make([]model.Issue, 0, len(numbers))
	for _, number := range numbers {
		issue, err := a.requireIssue(ctx, number, flag)
		if err != nil {
			return nil, err
		}
		issues = append(issues, issue)
	}
	return issues, nil
}

// uniqueInts drops repeated numbers, keeping first-seen order, so a reference
// flag that names an issue twice neither fetches it twice nor attaches it
// twice — a second attach of the same edge is the failure GitHub may reject
// after the issue is already filed.
func uniqueInts(numbers []int) []int {
	seen := make(map[int]bool, len(numbers))
	out := make([]int, 0, len(numbers))
	for _, n := range numbers {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
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
		return fmt.Errorf("reopened #%d but releasing its stale claim failed (it is open and still claimed; hew release %d --force clears it): %w", number, number, err)
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
	// Remove each in-progress label by the spelling the issue carries, so the
	// removal does not depend on the backend resolving case.
	for _, l := range issue.Labels {
		if model.IsInProgressLabel(l) {
			if err := a.Client.RemoveLabel(ctx, issue.Number, l); err != nil {
				return "", err
			}
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
func (a *App) EpicCreate(ctx context.Context, opts EpicCreateOpts) error {
	if strings.TrimSpace(opts.Title) == "" {
		return usageErr("--title is required")
	}
	if err := validateBodySource(opts.Sections, opts.BodyFile, opts.Edit); err != nil {
		return err
	}
	title := opts.Title
	if !strings.HasPrefix(title, conventions.EpicTitlePrefix) {
		title = conventions.EpicTitlePrefix + title
	}
	if err := validateTitle(title); err != nil {
		return err
	}
	// Check the children before the editor opens or the epic is created: a
	// typo must not leave a childless epic that looks like a triage mistake.
	// A child already in another epic is the same kind of bad reference —
	// AddSubIssue refuses it, so catching it here is what keeps the epic from
	// being filed first. A child that disappears before the attach keeps the
	// partial-failure message below, which names the epic already filed.
	childNumbers := uniqueInts(opts.Children)
	children, err := a.requireIssues(ctx, "--children", childNumbers)
	if err != nil {
		return err
	}
	for _, child := range children {
		if child.Parent != nil {
			return genericErr("--children: #%d already has a parent (#%d); detach it first with `hew set %d --no-parent`", child.Number, child.Parent.Number, child.Number)
		}
	}
	body, err := a.composeBody(bodySource{
		sections: opts.Sections, bodyFile: opts.BodyFile, edit: opts.Edit,
	})
	if err != nil {
		return err
	}
	created, err := a.Client.CreateIssue(ctx, title, body, []string{model.DefaultPriority.String(), model.EpicLabel})
	if err != nil {
		return err
	}
	for _, child := range childNumbers {
		if err := a.Client.AddSubIssue(ctx, created.Number, child, false); err != nil {
			return fmt.Errorf("created epic #%d but attaching #%d failed: %w", created.Number, child, err)
		}
	}
	return a.reportMutation(ctx, created.Number, "created epic #%d: %s (%d children)\n", created.Number, title, len(childNumbers))
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
	// A config beside no issues.jsonl is a tracker that lost its issues; init
	// is the documented way out, so it recreates the file but says so.
	res, err := local.InitTracker(tracker)
	if err != nil {
		return genericErr("creating %s: %v", tracker, err)
	}
	if res.IssuesRecreated {
		a.warnf("%s", local.RecreatedIssuesNotice(filepath.Join(tracker, "issues.jsonl")))
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
func (a *App) swapPriority(ctx context.Context, issue model.Issue, p model.Priority) error {
	target := p.String()
	for _, l := range issue.Labels {
		if _, ok := model.LabelPriority(l); ok && !strings.EqualFold(l, target) {
			if err := a.Client.RemoveLabel(ctx, issue.Number, l); err != nil {
				return err
			}
		}
	}
	if !issue.HasLabel(target) {
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
