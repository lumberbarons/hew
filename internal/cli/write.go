package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

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
func closeState(issue model.Issue) string { return closedState(issue.StateReason) }

// closedState is closeState for a raw backend state reason, such as the one a
// tracker.ClosedError carries.
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

// Release gives up a claim without assigning it to another owner.
func (a *App) Release(ctx context.Context, number int, force bool) error {
	issue, err := a.Client.GetIssue(ctx, number)
	if err != nil {
		return err
	}
	if !issue.Claimed() {
		return genericErr("#%d is not claimed", number)
	}
	var viewer string
	if !force {
		viewer, err = a.Client.Viewer(ctx)
		if err != nil {
			return err
		}
		if len(issue.Assignees) != 1 || issue.Assignees[0] != viewer {
			return genericErr("#%d is not claimed solely by you; pass --force to release it", number)
		}
	}
	if releaser, ok := a.Client.(gh.ClaimReleaser); ok {
		// Recheck ownership and clear both halves under one lock. A concurrent
		// forced start must not have its new claim cleared by this stale read.
		if err := releaser.ReleaseIssueClaim(ctx, number, viewer, force); err != nil {
			return err
		}
	} else if _, err := a.releaseClaim(ctx, issue); err != nil {
		return fmt.Errorf("releasing #%d failed (its claim may be partially cleared; inspect with hew show %d before retrying): %w", number, number, err)
	}
	return a.reportMutation(ctx, number, "released #%d\n", number)
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
// Count characters rather than bytes so non-ASCII titles get the same limit.
func validateTitle(title string) error {
	if strings.TrimSpace(title) == "" {
		return usageErr("--title must not be empty")
	}
	if strings.ContainsAny(title, "\r\n\v\f\u0085\u2028\u2029") {
		return usageErr("--title must be a single line")
	}
	if utf8.RuneCountInString(title) > 256 {
		return usageErr("--title must be at most 256 characters (including the Epic: prefix for epics)")
	}
	return nil
}

// normalizeAreas returns the area names trimmed of surrounding whitespace and
// de-duplicated, or a usage error. A name that is blank after trimming is
// refused: GitHub rejects a label with no visible name, and the local store
// would otherwise keep one that renders as `( )` and breaks a later
// `migrate local`. Everything else is checked against the trimmed name, so
// " P0 " is the priority label it names.
//
// The checks enforce GitHub's label length limit and reserve convention
// labels: areas must neither change priority/type nor manufacture a claim. A
// comma is refused too: the text renderer joins areas with it, so an area named
// "x,y" would be indistinguishable from the two areas "x" and "y". The area
// flags split on commas before this runs, so the check guards the callers that
// hand over a name verbatim — `apply` plans and direct API use — while
// `--area x,y` stays two areas. Removal is checked by validateAreaLabels, which
// has no comma rule.
func normalizeAreas(flag string, areas []string) ([]string, error) {
	trimmed := make([]string, 0, len(areas))
	for _, area := range areas {
		area = strings.TrimSpace(area)
		if area == "" {
			return nil, usageErr("%s must not be blank", flag)
		}
		// Trimming can make two spellings one name; keep the first.
		if !slices.Contains(trimmed, area) {
			trimmed = append(trimmed, area)
		}
	}
	for _, area := range trimmed {
		if utf8.RuneCountInString(area) > 50 {
			return nil, usageErr("%s must be at most 50 characters", flag)
		}
		if strings.Contains(area, ",") {
			return nil, usageErr("%s %q must not contain a comma; commas separate areas", flag, area)
		}
	}
	return trimmed, validateAreaLabels(flag, trimmed)
}

func validateAreaLabels(flag string, areas []string) error {
	for _, area := range areas {
		if _, ok := model.LabelPriority(area); ok {
			return usageErr("%s %q is a priority label; use --priority", flag, area)
		}
		if _, ok := model.LabelType(area); ok {
			return usageErr("%s %q is a type label; use --type", flag, area)
		}
		if model.IsInProgressLabel(area) {
			return usageErr("%s %q is a reserved convention label; use hew start to claim an issue", flag, area)
		}
		if model.IsEpicLabel(area) {
			return usageErr("%s %q is a reserved convention label; use hew epic create to make an epic", flag, area)
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

// Block adds native dependencies after a transitive client-side cycle
// check — GitHub itself only rejects self-blocks and direct two-issue
// cycles. It refuses a closed target unless allowClosed is set; a closed
// blocker is refused outright, since closed blockers don't block.
//
// Several blockers are validated together and written only once every one has
// passed, so a refusal never leaves a half-applied set. Each is checked against
// the graph as it stands: every new edge starts at number, so a cycle through
// two of them would revisit number and is already a cycle through one.
func (a *App) Block(ctx context.Context, number int, blockers []int, allowClosed bool) error {
	if len(blockers) == 0 {
		return usageErr("usage: hew block <n> --on <m>")
	}
	blockers = uniqueInts(blockers)
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
	var add, existing []int
	for _, blocker := range blockers {
		if _, ok := byNum[blocker]; !ok {
			return genericErr("#%d is not an open issue in %s; closed blockers don't block", blocker, a.Repo)
		}
		if slices.Contains(issue.OpenBlockers(), blocker) {
			existing = append(existing, blocker)
			continue
		}
		check := model.CheckBlockedBy(issues, number, blocker)
		if check.Cycle != nil {
			return genericErr("refusing: would create dependency cycle %s", render.FormatCycle(check.Cycle))
		}
		if !check.Verifiable {
			return genericErr("refusing: cannot verify #%d → #%d is cycle-free because some issues have more blockers than were fetched; reduce blockers on the issues involved and retry", number, blocker)
		}
		add = append(add, blocker)
	}
	if len(add) == 0 {
		return a.reportNoOp(ctx, number, "#%d is already blocked by %s\n", number, commaRefs(existing))
	}
	for i, blocker := range add {
		if err := a.Client.AddBlockedBy(ctx, number, blocker); err != nil {
			return partialWriteError(err, "already blocked #%d on", "blocking on", number, add, i)
		}
	}
	note := ""
	if len(existing) > 0 {
		note = "; already blocked by " + commaRefs(existing)
	}
	return a.reportMutation(ctx, number, "blocked #%d on %s%s\n", number, commaRefs(add), note)
}

// Unblock removes dependencies. It refuses a closed target unless allowClosed
// is set. A blocker that is not there is skipped and named rather than
// failing the call, as a single one is reported as a no-op.
func (a *App) Unblock(ctx context.Context, number int, blockers []int, allowClosed bool) error {
	if len(blockers) == 0 {
		return usageErr("usage: hew unblock <n> --from <m>")
	}
	blockers = uniqueInts(blockers)
	issue, err := a.Client.GetIssue(ctx, number)
	if err != nil {
		return err
	}
	if err := guardClosed(issue, allowClosed); err != nil {
		return err
	}
	var remove, missing []int
	for _, blocker := range blockers {
		if slices.ContainsFunc(issue.BlockedBy, func(b model.Ref) bool { return b.Number == blocker }) {
			remove = append(remove, blocker)
		} else {
			missing = append(missing, blocker)
		}
	}
	if len(remove) == 0 {
		return a.emitNoOp(issue, "#%d is not blocked by %s\n", number, commaRefs(missing))
	}
	for i, blocker := range remove {
		if err := a.Client.RemoveBlockedBy(ctx, number, blocker); err != nil {
			return partialWriteError(err, "already unblocked #%d from", "unblocking from", number, remove, i)
		}
	}
	note := ""
	if len(missing) > 0 {
		note = "; not blocked by " + commaRefs(missing)
	}
	return a.reportMutation(ctx, number, "unblocked #%d from %s%s\n", number, commaRefs(remove), note)
}

// partialWriteError reports the write at index failed in a run of edge writes.
// When it was the first nothing has changed and err stands as it is; otherwise
// the caller must be told which edges did land, because neither retrying them
// nor assuming the rest exist is safe. landed is a format taking the issue
// number and ending in the preposition ("already blocked #%d on").
func partialWriteError(err error, landed, doing string, number int, writes []int, index int) error {
	if index == 0 {
		return err
	}
	head := fmt.Sprintf(landed+" %s, but %s #%d failed", number, commaRefs(writes[:index]), doing, writes[index])
	tail := ""
	if rest := writes[index+1:]; len(rest) > 0 {
		tail = "; not attempted: " + commaRefs(rest)
	}
	return fmt.Errorf("%s: %w%s", head, err, tail)
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
