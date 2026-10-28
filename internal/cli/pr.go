package cli

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/lumberbarons/hew/internal/conventions"
	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/model"
	"github.com/lumberbarons/hew/internal/render"
)

// PROpts are the pr command's inputs. Every section is optional: the point
// of the command is that tracker state fills the gaps.
type PROpts struct {
	// Amend revises the open PR on this branch instead of creating one.
	Amend bool
	// SectionFlags records explicitly supplied flags, including empty values.
	SectionFlags []string
	// For overrides issue inference; empty means infer. Several values are a
	// deliberate multi-issue PR: only declared issues are ever combined,
	// never inferred ones.
	For      []int
	Title    string
	Sections conventions.PRSections
	BodyFile string
	// Ready opens the PR for review instead of as a draft.
	Ready bool
	// Base overrides the target branch; empty means the repo default.
	Base string
}

// PR opens or revises the pull request that closes the claim lifecycle. It is
// deliberately thin: infer which issue this branch is for, compose a body
// from what the issue already says, and refuse the states that leave an
// issue claimed-but-orphaned after a merge. Reviews, merges and checks stay
// with `gh`.
func (a *App) PR(ctx context.Context, opts PROpts) error {
	if opts.BodyFile != "" && (!opts.Sections.IsZero() || len(opts.SectionFlags) > 0) {
		return usageErr("section flags (--what/--why/--testing) and --body-file are mutually exclusive")
	}
	if opts.Amend && (opts.Ready || opts.Base != "") {
		return usageErr("--amend cannot be combined with --ready or --base; it edits the title and body only")
	}
	if a.PRs == nil {
		return genericErr("pr needs the GitHub backend; this repo tracks issues locally")
	}
	if a.Git == nil {
		return genericErr("pr needs a git checkout; it is not available here")
	}
	state, err := a.Git()
	if err != nil {
		return genericErr("%v", err)
	}
	if !state.Pushed {
		return genericErr("branch %s has no upstream; push it first: git push -u origin %s", state.Branch, state.Branch)
	}
	// Everything from here on is about the branch GitHub can see, which is
	// not always the one checked out locally — the head it rejects, the head
	// it matches an existing PR by, and the name a reviewer reads.
	head := state.Head()

	issues, err := a.Client.ListIssues(ctx, openStates)
	if err != nil {
		return err
	}
	viewer, err := a.Client.Viewer(ctx)
	if err != nil {
		return err
	}
	linked, err := a.linkedIssues(issues, head, viewer, opts.For)
	if err != nil {
		return err
	}
	// The first issue is the primary: it names the PR in messages and seeds
	// the default title.
	issue := linked[0]

	prCtx, err := a.PRs.PullRequestContext(ctx, head)
	if err != nil {
		return err
	}
	if opts.Amend {
		return a.amendPR(ctx, opts, linked, head, prCtx)
	}
	if prCtx.Existing != nil {
		return genericErr("#%d already has an open PR from %s: #%d — push to the branch or use --amend to revise its title/body",
			issue.Number, head, prCtx.Existing.Number)
	}
	base := opts.Base
	if base == "" {
		base = prCtx.DefaultBranch
	}
	if base == "" {
		return genericErr("cannot determine the base branch for %s (use --base)", a.Repo)
	}
	if head == base {
		return genericErr("on %s: a PR needs a branch (git checkout -b feat/…)", base)
	}
	if !conventions.IsConventionalBranch(head) {
		a.warnf("branch %s has no %s prefix", head, strings.Join(conventions.BranchPrefixes, "|"))
	}
	a.warnPRClaims(linked, viewer)

	body, err := a.composePRBody(opts, linked)
	if err != nil {
		return err
	}
	a.warnPRSections(body, opts.Ready)
	// An explicit --title is the author's, prefix or not; only the derived
	// default carries the type's conventional-commit prefix.
	title := opts.Title
	if title == "" {
		issueType, _ := issue.Type()
		title = conventions.PRTitle(issueType, issue.Title)
		if len(linked) > 1 {
			a.warnf("title comes from #%d alone; pass --title to name the whole change", issue.Number)
		}
	}

	created, err := a.PRs.CreatePullRequest(ctx, gh.NewPullRequest{
		Title: title, Body: body,
		Head: head, Base: base, Draft: !opts.Ready,
	})
	if err != nil {
		return err
	}
	kind := "draft PR"
	if opts.Ready {
		kind = "PR"
	}
	return a.emitResult(prJSON{
		Number: created.Number, URL: created.URL, Draft: created.Draft,
		Fixes: issue.Number, FixedIssues: issueNumbers(linked), Head: head, Base: base, Title: title,
	}, func() {
		// The summary an agent branches on, then the URL a human clicks; the
		// create response always carries one, so it is not conditional. title
		// may carry the issue's own (GitHub-derived) text via PRTitle, so it
		// needs the same neutralizing the JSON/API copy does not.
		a.printf("created %s #%d for %s: %s\n", kind, created.Number, formatNumbers(linked), render.SanitizeInline(title))
		a.printf("%s\n", created.URL)
	})
}

// prJSON is the pr command's --json result: flat, and enough for an agent
// to follow up without a second call.
type prJSON struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	Draft  bool   `json:"draft"`
	// Fixes is the primary issue and stays a scalar so existing consumers
	// keep working; FixedIssues is the whole set the PR closes, primary first.
	Fixes       int    `json:"fixes"`
	FixedIssues []int  `json:"fixedIssues"`
	Head        string `json:"head"`
	Base        string `json:"base"`
	Title       string `json:"title"`
}

// linkedIssues resolves the set of issues the PR closes. Inference only ever
// yields one issue; several come solely from repeated --for, because a wrong
// Fixes line closes the wrong issue on merge and an inferred second one
// would do it silently. A repeated number is collapsed.
func (a *App) linkedIssues(issues []model.Issue, branch, viewer string, forNumbers []int) ([]model.Issue, error) {
	if len(forNumbers) == 0 {
		issue, err := a.linkedIssue(issues, branch, viewer, 0)
		if err != nil {
			return nil, err
		}
		return []model.Issue{issue}, nil
	}
	var linked []model.Issue
	for _, n := range forNumbers {
		if slices.ContainsFunc(linked, func(i model.Issue) bool { return i.Number == n }) {
			continue
		}
		issue, err := a.linkedIssue(issues, branch, viewer, n)
		if err != nil {
			return nil, err
		}
		linked = append(linked, issue)
	}
	return linked, nil
}

// warnPRClaims flags every linked issue the viewer does not hold, and a set
// whose members share nothing. Neither refuses: whether a combined PR stays
// reviewable is a judgement hew cannot make.
func (a *App) warnPRClaims(linked []model.Issue, viewer string) {
	for _, issue := range linked {
		switch {
		case len(issue.Assignees) == 0:
			a.warnf("#%d is not claimed by you (hew start %d)", issue.Number, issue.Number)
		case !slices.Contains(issue.Assignees, viewer):
			a.warnf("#%d is claimed by @%s, not you", issue.Number, render.SanitizeInline(strings.Join(issue.Assignees, " @")))
		}
	}
	if groups := model.RelatedGroups(linked); len(groups) > 1 {
		parts := make([]string, len(groups))
		for i, g := range groups {
			parts[i] = formatNumbers(g)
		}
		a.warnf("issues %s look unrelated (no shared parent, area label or dependency); keep a combined PR reviewable as one change",
			strings.Join(parts, " | "))
	}
}

// linkedIssue decides which single issue a PR closes. The claim is the primary
// signal — it's tracker state, not a naming guess — and a number in the
// branch name breaks ties, covers the unclaimed case, and vetoes a lone
// claim it contradicts. Ambiguity is an error naming the candidates rather
// than a coin flip: a wrong Fixes line closes the wrong issue on merge.
func (a *App) linkedIssue(issues []model.Issue, branch, viewer string, forNumber int) (model.Issue, error) {
	byNum := model.ByNumber(issues)
	if forNumber > 0 {
		issue, ok := byNum[forNumber]
		if !ok {
			return model.Issue{}, genericErr("#%d is not an open issue in %s", forNumber, a.Repo)
		}
		if issue.IsEpic() {
			return model.Issue{}, genericErr("#%d is an epic; PRs close sub-issues, never the epic", forNumber)
		}
		return issue, nil
	}

	var claimed []model.Issue
	for _, i := range issues {
		if !i.IsEpic() && slices.Contains(i.Assignees, viewer) {
			claimed = append(claimed, i)
		}
	}
	branchNum, hasBranchNum := branchIssueNumber(branch)
	switch {
	case len(claimed) == 1:
		// The claim is the stronger signal, but when the branch name points
		// at a different open issue the two disagree and one of them is
		// wrong. Preferring the claim silently writes a Fixes line that
		// closes the other issue on merge, and nothing in the output says so
		// (#45), so make the disagreement the user's to resolve.
		if other, ok := byNum[branchNum]; hasBranchNum && ok &&
			other.Number != claimed[0].Number && !other.IsEpic() {
			return model.Issue{}, usageErr(
				"branch %s names #%d but you have #%d claimed; say which with --for <n>",
				branch, other.Number, claimed[0].Number)
		}
		return claimed[0], nil
	case len(claimed) > 1:
		if hasBranchNum {
			for _, i := range claimed {
				if i.Number == branchNum {
					return i, nil
				}
			}
		}
		return model.Issue{}, usageErr("you have %d issues claimed (%s); say which with --for <n>",
			len(claimed), formatNumbers(claimed))
	default:
		if hasBranchNum {
			if issue, ok := byNum[branchNum]; ok && !issue.IsEpic() {
				return issue, nil
			}
		}
		return model.Issue{}, usageErr("cannot tell which issue %s is for: nothing claimed by @%s; pass --for <n>", branch, viewer)
	}
}

// branchIssueNumber pulls an issue number out of a branch name, matching
// the digit run in segments like feat/30-pr-command or fix/issue-42. Only a
// segment that is entirely digits counts, so fix/500-error stays a name and
// not a link to #500 — the branch is a hint, and a hint that guesses wrong
// closes the wrong issue.
func branchIssueNumber(branch string) (int, bool) {
	for _, part := range strings.FieldsFunc(branch, func(r rune) bool {
		return r == '/' || r == '-' || r == '_' || r == '.'
	}) {
		if n, err := strconv.Atoi(part); err == nil && n > 0 {
			return n, true
		}
	}
	return 0, false
}

func formatNumbers(issues []model.Issue) string {
	return formatRefs(issueNumbers(issues))
}

func formatRefs(numbers []int) string {
	parts := make([]string, len(numbers))
	for i, n := range numbers {
		parts[i] = fmt.Sprintf("#%d", n)
	}
	return strings.Join(parts, " ")
}

func issueNumbers(issues []model.Issue) []int {
	numbers := make([]int, len(issues))
	for i, issue := range issues {
		numbers[i] = issue.Number
	}
	return numbers
}

// composePRBody builds the body and enforces the closing-set invariant: the
// composed path writes exactly the declared set (one issue, unless --for was
// repeated), and a --body-file that already closes that set is left alone
// rather than given a second link.
func (a *App) composePRBody(opts PROpts, linked []model.Issue) (string, error) {
	if opts.BodyFile != "" {
		return a.prBodyFile(opts.BodyFile, linked)
	}
	sections := opts.Sections
	// Creation defaults to the issue; amendments default to the PR itself.
	// One issue's Fix says nothing true about a PR that closes several, so a
	// set gets no defaults and is asked for its own text.
	if len(linked) == 1 {
		issue := linked[0]
		if sections.What == "" {
			sections.What = conventions.FirstSection(issue.Body, "Fix", "Approach")
		}
		if sections.Why == "" {
			sections.Why = conventions.FirstSection(issue.Body, "Problem", "Goal")
		}
	} else if sections.What == "" || sections.Why == "" {
		a.warnf("%s share one PR, so What/Why have no default: pass --what and --why", formatNumbers(linked))
	}
	return a.finishPRSections(sections, linked)
}

func (a *App) amendPR(ctx context.Context, opts PROpts, linked []model.Issue, head string, prCtx gh.PRContext) error {
	issue := linked[0]
	existing := prCtx.Existing
	if existing == nil {
		return genericErr("#%d has no open PR from %s; omit --amend to create one", issue.Number, head)
	}
	// A head name can match several base branches or a fork. The first
	// node is sufficient to refuse creation, but mutation needs proof that
	// this is the only match and its head belongs to our repository.
	if prCtx.OpenCount != 1 {
		return genericErr("cannot select an unambiguous PR from %s: found %d open matches; use gh pr edit with an explicit PR number", head, prCtx.OpenCount)
	}
	if !strings.EqualFold(existing.HeadRepository, a.Repo.String()) {
		return genericErr("cannot amend PR #%d: head repository %q does not identify %s; use gh pr edit with an explicit PR number", existing.Number, existing.HeadRepository, a.Repo)
	}
	edited := true
	var body string
	var err error
	switch {
	case opts.BodyFile != "":
		body, err = a.prBodyFile(opts.BodyFile, linked)
	case opts.Sections.IsZero() && len(opts.SectionFlags) == 0:
		// A title-only edit preserves even a freeform body.
		edited = false
		body, err = a.finishPRBody(existing.Body, linked, "PR body")
	default:
		content := existing.Body
		// Section reads to the next heading, so detach our generated trailer
		// block before reading the final section. Other closing links remain
		// in the content and are checked along with the replacement sections.
		trailers := prTrailers(linked)
		fixesOnly := conventions.PRTrailers{Fixes: trailers.Fixes}
		for _, suffix := range []string{trailers.Render(), fixesOnly.Render()} {
			content = strings.TrimSpace(content)
			if content == suffix {
				content = ""
			} else {
				content = strings.TrimSuffix(content, "\n\n"+suffix)
			}
		}
		preserved := conventions.PRSections{
			What:    conventions.Section(content, "What"),
			Why:     conventions.Section(content, "Why"),
			Testing: conventions.Section(content, "Testing"),
			Risk:    conventions.Section(content, "Risk"),
		}
		// Section is a simple template reader, not a Markdown parser. Require
		// an exact round trip before editing, and refuse fences even when an
		// example's headings happen to look like the canonical template.
		safe := preserved.Compose(conventions.PRTrailers{}) == content
		for _, line := range strings.Split(content, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
				safe = false
			}
		}
		if !safe {
			return usageErr("cannot safely parse this PR body for section edits; use --amend --body-file to replace the whole body")
		}
		sections := opts.Sections
		if sections.What == "" && !slices.Contains(opts.SectionFlags, "what") {
			sections.What = preserved.What
		}
		if sections.Why == "" && !slices.Contains(opts.SectionFlags, "why") {
			sections.Why = preserved.Why
		}
		if sections.Testing == "" && !slices.Contains(opts.SectionFlags, "testing") {
			sections.Testing = preserved.Testing
		}
		if sections.Risk == "" && !slices.Contains(opts.SectionFlags, "risk") {
			sections.Risk = preserved.Risk
		}
		body, err = a.finishPRSections(sections, linked)
	}
	if err != nil {
		return err
	}
	if edited {
		// A PR already open for review is the moment to ask for the merge
		// claim; a draft may legitimately be incomplete.
		a.warnPRSections(body, !existing.Draft)
	}
	title := opts.Title
	if title == "" {
		title = existing.Title
	}
	if err := a.PRs.UpdatePullRequest(ctx, existing.Number, title, body); err != nil {
		return err
	}
	return a.emitResult(prJSON{
		Number: existing.Number, URL: existing.URL, Draft: existing.Draft,
		Fixes: issue.Number, FixedIssues: issueNumbers(linked), Head: head, Base: existing.Base, Title: title,
	}, func() {
		a.printf("updated PR #%d for %s: %s\n", existing.Number, formatNumbers(linked), render.SanitizeInline(title))
		a.printf("%s\n", existing.URL)
	})
}

func prTrailers(linked []model.Issue) conventions.PRTrailers {
	// Sorted so the rendered block is the same however --for was ordered,
	// which --amend relies on to find and detach the trailers it wrote.
	trailers := conventions.PRTrailers{Fixes: slices.Sorted(slices.Values(issueNumbers(linked)))}
	for _, issue := range linked {
		if issue.Parent != nil && !slices.Contains(trailers.Epics, issue.Parent.Number) {
			trailers.Epics = append(trailers.Epics, issue.Parent.Number)
		}
	}
	return trailers
}

func (a *App) finishPRSections(sections conventions.PRSections, linked []model.Issue) (string, error) {
	return a.finishPRBody(sections.Compose(conventions.PRTrailers{}), linked, "PR body")
}

// warnPRSections reports the body contract at composition time. Help is read
// by someone who goes looking; the agent composing a PR generally does not, so
// the sections a reviewer needs are checked on every path that writes a body —
// flags, --body-file, and --amend. Risk is required only once the PR is ready
// for review: a draft may legitimately be incomplete. The body may be
// free-form and already carries its trailers, so sections are read with
// ReviewSection, which treats the trailers as the end of the author's text.
func (a *App) warnPRSections(body string, requireRisk bool) {
	if conventions.ReviewSection(body, "Testing") == "" {
		a.warnf("no Testing section: %s", conventions.PRTestingGuidance)
	}
	if requireRisk && conventions.ReviewSection(body, "Risk") == "" {
		a.warnf("no Risk section: %s", conventions.PRRiskGuidance)
	}
}

func (a *App) prBodyFile(path string, linked []model.Issue) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", genericErr("cannot read --body-file: %v", err)
	}
	return a.finishPRBody(string(raw), linked, "--body-file")
}

// Both paths enforce that the body closes exactly the declared set, no more
// and no less, and append only missing trailers, so a section flag cannot
// bypass the body-file guard either. A body with no closing reference at all
// is given the whole set.
func (a *App) finishPRBody(raw string, linked []model.Issue, source string) (string, error) {
	trailers := prTrailers(linked)
	body := strings.TrimRight(raw, "\n")
	if refs := conventions.FixesReferences(body); len(refs) > 0 {
		if !sameClosingSet(refs, trailers.Fixes) {
			return "", closingSetErr(source, refs, trailers.Fixes)
		}
		trailers.Fixes = nil
	}
	trailers.Epics = slices.DeleteFunc(trailers.Epics, func(epic int) bool {
		return strings.Contains(body, conventions.PartOfLine(epic))
	})
	if missing := trailers.Render(); missing != "" {
		if body != "" {
			body += "\n\n"
		}
		body += missing
	}
	a.warnUnmarkedCode(body, "hew pr --amend")
	return body, nil
}

// sameClosingSet compares as sets but refuses a repeated reference: the
// body that ships should read as exactly the declared issues.
func sameClosingSet(refs, want []int) bool {
	return len(refs) == len(want) && slices.Equal(slices.Sorted(slices.Values(refs)), slices.Sorted(slices.Values(want)))
}

func closingSetErr(source string, refs, want []int) error {
	if len(want) == 1 && len(refs) > 1 {
		if distinct := slices.Compact(slices.Sorted(slices.Values(refs))); len(distinct) == 1 {
			return usageErr("%s closes #%d more than once; write the closing reference once", source, distinct[0])
		}
		return usageErr("%s closes %d issues; a PR closes exactly one (declare a set with repeated --for)", source, len(refs))
	}
	return usageErr("%s closes %s but this PR is for %s", source, formatRefs(refs), formatRefs(want))
}
