package pr

import (
	"context"

	"github.com/lumberbarons/hew/internal/conventions"
	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/tracker"
)

// CreateRequest configures Create. Every body input is optional: the point of
// the workflow is that tracker state fills the gaps.
type CreateRequest struct {
	// For declares the issues the pull request closes, primary first. Empty
	// infers exactly one from the branch and the acting identity's claim.
	// Several issues are only ever declared, never inferred.
	For []int
	// Title overrides the derived default. Empty derives
	// `<prefix>: <issue title>` from the primary issue's type.
	Title string
	// Body supplies the body: structured sections, or text the caller already
	// has. The closing references are resolved from For; any set on Body
	// itself is replaced.
	Body Body
	// Ready opens the pull request for review instead of as a draft.
	Ready bool
	// Base overrides the target branch. Empty uses the repository's default.
	Base string
}

// CreateResult is the pull request a create produced, with the advisory
// warnings and findings that did not fail it.
type CreateResult struct {
	Number      int
	URL         string
	Draft       bool
	Fixes       int
	FixedIssues []int
	Head        string
	Base        string
	Title       string
	Warnings    []Warning
	Findings    []tracker.Finding
}

// Create opens the draft pull request the workflow prescribes: the linked
// issue inferred from the claim and branch, exactly one Fixes reference per
// declared issue, and the repository's default branch as the base.
func (c *Client) Create(ctx context.Context, req CreateRequest) (CreateResult, error) {
	p, err := c.prepare(ctx, req.For)
	if err != nil {
		return CreateResult{}, err
	}
	primary := p.linked[0]
	if p.prCtx.Existing != nil {
		return CreateResult{}, &ExistingPullRequestError{
			Issue: primary.Number, Head: p.head, Number: p.prCtx.Existing.Number,
		}
	}
	base := req.Base
	if base == "" {
		base = p.prCtx.DefaultBranch
	}
	if base == "" {
		return CreateResult{}, invalid(FieldBase, "cannot determine the base branch for %s", c.repoName())
	}
	if p.head == base {
		return CreateResult{}, invalid(FieldHead, "on %s: a PR needs a branch", base)
	}

	var warnings []Warning
	if !conventions.IsConventionalBranch(p.head) {
		warnings = append(warnings, Warning{Kind: WarnBranchPrefix, Branch: p.head})
	}
	warnings = append(warnings, claimWarnings(p.linked, p.actor)...)

	composed, composeWarnings, err := composeCreate(p.linked, req.Body)
	if err != nil {
		return CreateResult{}, err
	}
	warnings = append(warnings, composeWarnings...)
	warnings = append(warnings, bodyWarnings(composed.Body, req.Ready)...)

	// An explicit Title is the author's, prefix or not; only the derived
	// default carries the type's conventional-commit prefix.
	title := req.Title
	if title == "" {
		title = prTitle(primary)
		if len(p.linked) > 1 {
			warnings = append(warnings, Warning{Kind: WarnPrimaryTitle, Issue: primary.Number})
		}
	}

	created, err := c.prs.CreatePullRequest(ctx, gh.NewPullRequest{
		Title: title, Body: composed.Body,
		Head: p.head, Base: base, Draft: !req.Ready,
	})
	if err != nil {
		// The composition succeeded and its advice is still useful to whoever
		// inspects the failure.
		return CreateResult{Warnings: warnings, Findings: composed.Findings}, asAuthError(err)
	}
	return CreateResult{
		Number: created.Number, URL: created.URL, Draft: created.Draft,
		Fixes: primary.Number, FixedIssues: issueNumbers(p.linked),
		Head: p.head, Base: base, Title: title,
		Warnings: warnings, Findings: composed.Findings,
	}, nil
}
