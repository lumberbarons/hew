package cli

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/lumberbarons/hew/internal/conventions"
	"github.com/lumberbarons/hew/internal/gh"
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
	a.warnUnmarkedCode(body, "hew set <n> --body-file")
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
	who := "assigned to @" + strings.Join(issue.Assignees, " @")
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

// Start claims an issue: assign @me plus the in-progress label. The guard
// refuses issues that are already assigned or in-progress — exit 3 for
// someone else's claim so an agent loop moves on to the next ready item,
// exit 5 when the claim is already yours; --force steals. Claiming an
// untriaged issue requires --priority — claiming forces triage.
func (a *App) Start(ctx context.Context, number int, priorityFlag string, force bool) error {
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

	viewer, err := a.Client.Viewer(ctx)
	if err != nil {
		return err
	}
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
	// GitHub has no conditional writes, so the guard is check-then-act:
	// re-read and make sure we're the only claimant.
	after, err := a.Client.GetIssue(ctx, number)
	if err != nil {
		return err
	}
	if len(after.Assignees) != 1 || after.Assignees[0] != viewer {
		a.warnf("claim may have raced: #%d now assigned to @%s", number, strings.Join(after.Assignees, " @"))
	}
	return a.emitMutation(after, "started #%d: %s\n", number, issue.Title)
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
		a.printf("#%d is already open\n", number)
		return nil
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
	return "@" + strings.Join(issue.Assignees, " and @") + "'s stale claim", nil
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
		a.printf("#%d is already blocked by #%d\n", number, blocker)
		return nil
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
		a.printf("#%d is not blocked by #%d\n", number, blocker)
		return nil
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

// Init bootstraps the convention labels in the repo and prints the
// CLAUDE.md snippet.
func (a *App) Init(ctx context.Context) error {
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
	return a.emitResult(map[string]any{"createdLabels": created}, func() {
		if len(created) == 0 {
			a.printf("all convention labels already exist in %s\n", a.Repo)
		} else {
			a.printf("created labels: %s\n", strings.Join(created, ", "))
		}
		a.printf("\nAdd to CLAUDE.md:\n\n%s\n", conventions.ClaudeSnippet)
		a.printf("\nOr let a hook inject the primer automatically: hew hooks install <claude|codex|cursor|opencode>\n")
	})
}

// swapPriority enforces the one-priority-label invariant: remove the
// others, add the target if absent.
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
