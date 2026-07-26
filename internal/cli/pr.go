package cli

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/lumberbarons/issues/internal/conventions"
	"github.com/lumberbarons/issues/internal/gh"
	"github.com/lumberbarons/issues/internal/model"
)

// PROpts are the pr command's inputs. Every section is optional: the point
// of the command is that tracker state fills the gaps.
type PROpts struct {
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

// PR opens the pull request that closes the claim lifecycle. It is
// deliberately thin: infer which issue this branch is for, compose a body
// from what the issue already says, and refuse the states that leave an
// issue claimed-but-orphaned after a merge. Reviews, merges and checks stay
// with `gh`.
func (a *App) PR(ctx context.Context, opts PROpts) error {
	if opts.BodyFile != "" && !opts.Sections.IsZero() {
		return usageErr("section flags (--what/--why/--testing) and --body-file are mutually exclusive")
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

	issues, err := a.Client.ListIssues(ctx, openStates)
	if err != nil {
		return err
	}
	viewer, err := a.Client.Viewer(ctx)
	if err != nil {
		return err
	}
	issue, err := a.linkedIssue(issues, state.Branch, viewer, opts.For)
	if err != nil {
		return err
	}

	prCtx, err := a.Client.PullRequestContext(ctx, state.Branch)
	if err != nil {
		return err
	}
	if prCtx.Existing != nil {
		return genericErr("#%d already has an open PR from %s: #%d — push to the branch instead",
			issue.Number, state.Branch, prCtx.Existing.Number)
	}
	base := opts.Base
	if base == "" {
		base = prCtx.DefaultBranch
	}
	if base == "" {
		return genericErr("cannot determine the base branch for %s (use --base)", a.Repo)
	}
	if state.Branch == base {
		return genericErr("on %s: a PR needs a branch (git checkout -b feat/…)", base)
	}
	if !conventions.IsConventionalBranch(state.Branch) {
		a.warnf("branch %s has no %s prefix", state.Branch, strings.Join(conventions.BranchPrefixes, "|"))
	}
	if len(issue.Assignees) > 0 && !slices.Contains(issue.Assignees, viewer) {
		a.warnf("#%d is claimed by @%s, not you", issue.Number, strings.Join(issue.Assignees, " @"))
	}

	body, err := a.composePRBody(opts, issue)
	if err != nil {
		return err
	}
	title := opts.Title
	if title == "" {
		title = issue.Title
	}

	created, err := a.Client.CreatePullRequest(ctx, gh.NewPullRequest{
		Title: title, Body: body,
		Head: state.Branch, Base: base, Draft: !opts.Ready,
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
		Fixes: issue.Number, Head: state.Branch, Base: base, Title: title,
	}, func() {
		// The summary an agent branches on, then the URL a human clicks; the
		// create response always carries one, so it is not conditional.
		a.printf("created %s #%d for #%d: %s\n", kind, created.Number, issue.Number, title)
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
	trailers := conventions.PRTrailers{Fixes: issue.Number}
	if issue.Parent != nil {
		trailers.Epic = issue.Parent.Number
	}
	if opts.BodyFile == "" {
		sections := opts.Sections
		// The issue already says what the change does and why; carry it over
		// so the PR isn't a retyping exercise (or, more often, empty).
		if sections.What == "" {
			sections.What = conventions.FirstSection(issue.Body, "Fix", "Approach")
		}
		if sections.Why == "" {
			sections.Why = conventions.FirstSection(issue.Body, "Problem", "Goal")
		}
		if sections.Testing == "" {
			a.warnf("no --testing section: say how this was verified")
		}
		return sections.Compose(trailers), nil
	}

	raw, err := os.ReadFile(opts.BodyFile)
	if err != nil {
		return "", genericErr("cannot read --body-file: %v", err)
	}
	// A hand-written body keeps whatever links it already makes; the rest are
	// appended, so the escape hatch ends up with the same set of links the
	// template would have written — never a second copy of one.
	body := strings.TrimRight(string(raw), "\n")
	switch refs := conventions.FixesReferences(body); {
	case len(refs) > 1:
		return "", usageErr("--body-file closes %d issues; a PR closes exactly one", len(refs))
	case len(refs) == 1 && refs[0] != issue.Number:
		return "", usageErr("--body-file closes #%d but this PR is for #%d", refs[0], issue.Number)
	case len(refs) == 1:
		trailers.Fixes = 0
	}
	if trailers.Epic > 0 && strings.Contains(body, conventions.PartOfLine(trailers.Epic)) {
		trailers.Epic = 0
	}
	if missing := trailers.Render(); missing != "" {
		body += "\n\n" + missing
	}
	return body, nil
}
