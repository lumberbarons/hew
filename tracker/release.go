package tracker

import (
	"context"
	"slices"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/model"
)

// Release gives up a claim: it clears the in-progress label and every
// assignee, leaving the issue open and unassigned. The zero ReleaseOptions
// releases only a claim held solely by the acting identity; opts.Force
// releases any claim.
//
// The refusals are typed: NotClaimedError for an issue nobody holds (with or
// without Force), and NotOwnerError when the claim is not solely the acting
// identity's — which includes a label-only claim and a shared one. A refusal
// changes nothing. A failure after the first write is a PartialReleaseError,
// because the claim may then be half cleared.
//
// Where the store can release atomically (the local store), ownership is
// rechecked and both halves cleared under one lock, so a concurrent forced
// start's new claim is never cleared by this call's stale read. Elsewhere it
// is check-then-act.
func (t *Tracker) Release(ctx context.Context, number int, opts ReleaseOptions) error {
	issue, err := t.store.GetIssue(ctx, number)
	if err != nil {
		return asReadError(err)
	}
	if !issue.Claimed() {
		return &NotClaimedError{Issue: number}
	}
	var actor string
	if !opts.Force {
		if actor, err = t.identity(ctx); err != nil {
			return asWriteError(err)
		}
		if len(issue.Assignees) != 1 || issue.Assignees[0] != actor {
			return &NotOwnerError{Issue: number, Assignees: slices.Clone(issue.Assignees), Actor: actor}
		}
	}
	if releaser, ok := t.store.(gh.ClaimReleaser); ok {
		return asReleaseError(releaser.ReleaseIssueClaim(ctx, number, actor, opts.Force))
	}
	if _, err := clearClaim(ctx, t.store, issue); err != nil {
		return &PartialReleaseError{Issue: number, Err: asWriteError(err)}
	}
	return nil
}

// clearedClaim reports the halves of a claim clearClaim removed.
type clearedClaim struct {
	Label     bool
	Assignees []string
}

// clearClaim drops the in-progress label and every assignee from issue. The
// label goes first: if the assignee removal then fails the issue is still
// claimed either way, and the caller reports exactly that.
func clearClaim(ctx context.Context, store gh.IssueStore, issue model.Issue) (clearedClaim, error) {
	var cleared clearedClaim
	// Remove each in-progress label by the spelling the issue carries, so the
	// removal does not depend on the backend resolving case.
	for _, l := range issue.Labels {
		if model.IsInProgressLabel(l) {
			if err := store.RemoveLabel(ctx, issue.Number, l); err != nil {
				return cleared, err
			}
			cleared.Label = true
		}
	}
	if len(issue.Assignees) == 0 {
		return cleared, nil
	}
	if err := store.RemoveAssignees(ctx, issue.Number, issue.Assignees); err != nil {
		return cleared, err
	}
	cleared.Assignees = slices.Clone(issue.Assignees)
	return cleared, nil
}
