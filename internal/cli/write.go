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
	return body, nil
}

// Start claims an issue: assign @me plus the in-progress label. The guard
// refuses issues that are already assigned or in-progress (exit 3) so an
// agent loop moves on to the next ready item; --force steals. Claiming an
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
		who := "in-progress"
		if len(issue.Assignees) > 0 {
			who = "assigned to @" + strings.Join(issue.Assignees, " @")
		}
		return &ExitError{Code: ExitClaimed, Message: fmt.Sprintf("#%d already claimed (%s); pick the next ready item or --force", number, who)}
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
}

func (o SetOpts) empty() bool {
	return o.Priority == "" && o.Type == "" && len(o.AddAreas) == 0 &&
		len(o.RemoveAreas) == 0 && o.Parent == 0 && !o.NoParent && o.Title == "" &&
		o.BodyFile == ""
}

// Set retriages or edits within the conventions, swapping the old
// priority/type label rather than stacking a second one.
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
		return genericErr("#%d is already closed", number)
	}
	if err := a.Client.Comment(ctx, number, reason); err != nil {
		return err
	}
	if err := a.Client.CloseIssue(ctx, number, stateReason); err != nil {
		// The reason comment already posted; flag it so a retry isn't read as
		// a clean redo — re-running would post the comment a second time.
		return fmt.Errorf("posted the reason comment on #%d but closing it failed (a retry will comment again): %w", number, err)
	}
	return a.reportMutation(ctx, number, "closed #%d (%s)\n", number, strings.ToLower(strings.ReplaceAll(string(stateReason), "_", " ")))
}

// Block adds a native dependency after a transitive client-side cycle
// check — GitHub itself only rejects self-blocks and direct two-issue
// cycles.
func (a *App) Block(ctx context.Context, number, blocker int) error {
	issues, err := a.Client.ListIssues(ctx, openStates)
	if err != nil {
		return err
	}
	byNum := model.ByNumber(issues)
	issue, ok := byNum[number]
	if !ok {
		return genericErr("#%d is not an open issue in %s", number, a.Repo)
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

// Unblock removes a dependency.
func (a *App) Unblock(ctx context.Context, number, blocker int) error {
	issue, err := a.Client.GetIssue(ctx, number)
	if err != nil {
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
		a.printf("\nOr let a hook inject the primer automatically: hew hooks install\n")
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
