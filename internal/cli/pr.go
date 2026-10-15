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
	// For overrides issue inference; 0 means infer.
	For      int
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
	issue, err := a.linkedIssue(issues, head, viewer, opts.For)
	if err != nil {
		return err
	}

	prCtx, err := a.Client.PullRequestContext(ctx, head)
	if err != nil {
		return err
	}
	if opts.Amend {
		return a.amendPR(ctx, opts, issue, head, prCtx.Existing)
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
	if len(issue.Assignees) > 0 && !slices.Contains(issue.Assignees, viewer) {
		a.warnf("#%d is claimed by @%s, not you", issue.Number, render.SanitizeInline(strings.Join(issue.Assignees, " @")))
	}

	body, err := a.composePRBody(opts, issue)
	if err != nil {
		return err
	}
	// An explicit --title is the author's, prefix or not; only the derived
	// default carries the type's conventional-commit prefix.
	title := opts.Title
	if title == "" {
		issueType, _ := issue.Type()
		title = conventions.PRTitle(issueType, issue.Title)
	}

	created, err := a.Client.CreatePullRequest(ctx, gh.NewPullRequest{
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
		Fixes: issue.Number, Head: head, Base: base, Title: title,
	}, func() {
		// The summary an agent branches on, then the URL a human clicks; the
		// create response always carries one, so it is not conditional. title
		// may carry the issue's own (GitHub-derived) text via PRTitle, so it
		// needs the same neutralizing the JSON/API copy does not.
		a.printf("created %s #%d for #%d: %s\n", kind, created.Number, issue.Number, render.SanitizeInline(title))
		a.printf("%s\n", created.URL)
	})
}

// prJSON is the pr command's --json result: flat, and enough for an agent
// to follow up without a second call.
type prJSON struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	Draft  bool   `json:"draft"`
	Fixes  int    `json:"fixes"`
	Head   string `json:"head"`
	Base   string `json:"base"`
	Title  string `json:"title"`
}

// linkedIssue decides which issue the PR closes. The claim is the primary
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
	parts := make([]string, len(issues))
	for i, issue := range issues {
		parts[i] = fmt.Sprintf("#%d", issue.Number)
	}
	return strings.Join(parts, " ")
}

// composePRBody builds the body and enforces the one-Fixes invariant: the
// composed path always writes exactly one, and a --body-file that already
// closes the right issue is left alone rather than given a second link.
func (a *App) composePRBody(opts PROpts, issue model.Issue) (string, error) {
	if opts.BodyFile != "" {
		return a.prBodyFile(opts.BodyFile, issue)
	}
	sections := opts.Sections
	// Creation defaults to the issue; amendments default to the PR itself.
	if sections.What == "" {
		sections.What = conventions.FirstSection(issue.Body, "Fix", "Approach")
	}
	if sections.Why == "" {
		sections.Why = conventions.FirstSection(issue.Body, "Problem", "Goal")
	}
	return a.finishPRSections(sections, issue)
}

func (a *App) amendPR(ctx context.Context, opts PROpts, issue model.Issue, head string, existing *gh.PullRequest) error {
	if existing == nil {
		return genericErr("#%d has no open PR from %s; omit --amend to create one", issue.Number, head)
	}
	var body string
	var err error
	switch {
	case opts.BodyFile != "":
		body, err = a.prBodyFile(opts.BodyFile, issue)
	case opts.Sections.IsZero() && len(opts.SectionFlags) == 0:
		// A title-only edit preserves even a freeform body.
		body, err = a.finishPRBody(existing.Body, issue, "PR body")
	default:
		content := existing.Body
		// Section reads to the next heading, so detach our generated trailer
		// block before reading the final section. Other closing links remain
		// in the content and are checked along with the replacement sections.
		trailers := prTrailers(issue)
		for _, suffix := range []string{trailers.Render(), conventions.FixesLine(issue.Number)} {
			content = strings.TrimSpace(content)
			if content == suffix {
				content = ""
			} else {
				content = strings.TrimSuffix(content, "\n\n"+suffix)
			}
		}
		sections := opts.Sections
		if sections.What == "" && !slices.Contains(opts.SectionFlags, "what") {
			sections.What = conventions.Section(content, "What")
		}
		if sections.Why == "" && !slices.Contains(opts.SectionFlags, "why") {
			sections.Why = conventions.Section(content, "Why")
		}
		if sections.Testing == "" && !slices.Contains(opts.SectionFlags, "testing") {
			sections.Testing = conventions.Section(content, "Testing")
		}
		body, err = a.finishPRSections(sections, issue)
	}
	if err != nil {
		return err
	}
	title := opts.Title
	if title == "" {
		title = existing.Title
	}
	if err := a.Client.UpdatePullRequest(ctx, existing.Number, title, body); err != nil {
		return err
	}
	return a.emitResult(prJSON{
		Number: existing.Number, URL: existing.URL, Draft: existing.Draft,
		Fixes: issue.Number, Head: head, Base: existing.Base, Title: title,
	}, func() {
		a.printf("updated PR #%d for #%d: %s\n", existing.Number, issue.Number, render.SanitizeInline(title))
		a.printf("%s\n", existing.URL)
	})
}

func prTrailers(issue model.Issue) conventions.PRTrailers {
	trailers := conventions.PRTrailers{Fixes: issue.Number}
	if issue.Parent != nil {
		trailers.Epic = issue.Parent.Number
	}
	return trailers
}

func (a *App) finishPRSections(sections conventions.PRSections, issue model.Issue) (string, error) {
	if strings.TrimSpace(sections.Testing) == "" {
		a.warnf("no --testing section: say how this was verified")
	}
	return a.finishPRBody(sections.Compose(conventions.PRTrailers{}), issue, "PR body")
}

func (a *App) prBodyFile(path string, issue model.Issue) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", genericErr("cannot read --body-file: %v", err)
	}
	return a.finishPRBody(string(raw), issue, "--body-file")
}

// Both paths enforce exactly one closing reference and append only missing
// trailers, so a section flag cannot bypass the body-file guard either.
func (a *App) finishPRBody(raw string, issue model.Issue, source string) (string, error) {
	trailers := prTrailers(issue)
	body := strings.TrimRight(raw, "\n")
	switch refs := conventions.FixesReferences(body); {
	case len(refs) > 1:
		return "", usageErr("%s closes %d issues; a PR closes exactly one", source, len(refs))
	case len(refs) == 1 && refs[0] != issue.Number:
		return "", usageErr("%s closes #%d but this PR is for #%d", source, refs[0], issue.Number)
	case len(refs) == 1:
		trailers.Fixes = 0
	}
	if trailers.Epic > 0 && strings.Contains(body, conventions.PartOfLine(trailers.Epic)) {
		trailers.Epic = 0
	}
	if missing := trailers.Render(); missing != "" {
		if body != "" {
			body += "\n\n"
		}
		body += missing
	}
	a.warnUnmarkedCode(body, "hew pr --amend")
	return body, nil
}
