package tracker

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/model"
)

// StartResult is a completed claim: the issue as re-read after the claim and
// the identity the claim was made as. Claimant is what a caller compares the
// resulting assignees with to notice a racing claim; it is also the identity a
// consumer records for the work, without a second backend lookup.
type StartResult struct {
	Issue    Issue  `json:"issue"`
	Claimant string `json:"claimant"`
}

// Start claims an issue as the tracker's acting identity: assign that identity
// and add the in-progress label, swapping the priority label when one was
// given. The zero StartOptions preserves the existing priority and refuses an
// existing claim or open blockers.
//
// The refusals are typed: ClosedError and EpicError for a target that cannot
// be worked, DiscoveryIntentError for an idea claimed without declaring
// investigation, ClaimedError carrying the claimants and whether the acting
// identity is one of them, BlockedError carrying the open blockers, and
// UntriagedError when a claim would need a priority the caller did not give
// (claiming an untriaged issue is triage). Start claims atomically on a
// backend that supports it, so simultaneous claims have exactly one winner,
// and re-reads afterwards where the backend only offers check-then-act.
func (t *Tracker) Start(ctx context.Context, number int, opts StartOptions) (StartResult, error) {
	issue, err := t.store.GetIssue(ctx, number)
	if err != nil {
		return StartResult{}, asReadError(err)
	}
	if !issue.IsOpen() {
		return StartResult{}, &ClosedError{Issue: number, Reason: issue.StateReason}
	}
	// Epic classification wins over the type: an issue that qualifies as both
	// stays a container even for an explicit discovery claim.
	if issue.IsEpic() {
		return StartResult{}, &EpicError{Issue: number}
	}
	// An idea is captured for later investigation, so claiming it is a
	// deliberate act. The gate sits before the claim and priority writes and
	// ignores Force: stealing an idea is no less a decision to investigate, and
	// a forced start must not be a way around the intent.
	if typ, _ := issue.Type(); typ == model.IdeaType && !opts.ForDiscovery {
		return StartResult{}, &DiscoveryIntentError{Issue: number}
	}
	if (len(issue.Assignees) > 0 || issue.InProgress()) && !opts.Force {
		return StartResult{}, claimError(ctx, t.store, issue, t.actor)
	}
	if open := issue.OpenBlockers(); len(open) > 0 && !opts.Force {
		return StartResult{}, &BlockedError{Issue: number, OpenBlockers: open}
	}
	priority, _ := issue.Priority()
	if opts.Priority != "" {
		p, ok := model.ParsePriority(opts.Priority)
		if !ok {
			return StartResult{}, &InvalidInputError{Field: "priority", Reason: "must be P0..P4"}
		}
		priority = p
	} else if priority == model.PriorityUnknown {
		return StartResult{}, &UntriagedError{Issue: number}
	}

	actor, err := t.identity(ctx)
	if err != nil {
		return StartResult{}, asWriteError(err)
	}
	// The local store can make the claim conditional: one locked write checks
	// and claims, so simultaneous starts have exactly one winner. Claim before
	// the priority swap so a loser has not already changed the issue. GitHub
	// has no conditional writes, so it falls through to the check-then-act
	// path and the re-read after claiming.
	if claimer, ok := t.store.(gh.Claimer); ok {
		if err := claimer.ClaimIssue(ctx, number, actor, opts.Force); err != nil {
			if errors.Is(err, gh.ErrClaimConflict) {
				current, readErr := t.store.GetIssue(ctx, number)
				if readErr != nil {
					return StartResult{}, asReadError(readErr)
				}
				return StartResult{}, claimError(ctx, t.store, current, actor)
			}
			return StartResult{}, asWriteError(err)
		}
		if err := swapPriority(ctx, t.store, issue, priority); err != nil {
			// Claiming is triage: a claim without the priority would leave the
			// issue half-started, and a retry would then be told it is already
			// the caller's instead of finishing the job.
			return StartResult{}, errors.Join(asWriteError(err), undoClaim(ctx, t.store, issue, actor, opts.Force))
		}
	} else {
		if err := swapPriority(ctx, t.store, issue, priority); err != nil {
			return StartResult{}, asWriteError(err)
		}
		if opts.Force && len(issue.Assignees) > 0 {
			if err := t.store.RemoveAssignees(ctx, number, issue.Assignees); err != nil {
				return StartResult{}, asWriteError(err)
			}
		}
		if !issue.InProgress() {
			if err := t.store.AddLabels(ctx, number, []string{model.InProgressLabel}); err != nil {
				return StartResult{}, asWriteError(err)
			}
		}
		if err := t.store.AddAssignee(ctx, number, actor); err != nil {
			return StartResult{}, asWriteError(err)
		}
	}
	after, err := t.store.GetIssue(ctx, number)
	if err != nil {
		return StartResult{}, asReadError(err)
	}
	return StartResult{Issue: issueFromModel(after, true), Claimant: actor}, nil
}

// identity is the actor claims are made as: the tracker's own, or the store's
// Viewer when WrapAs deferred it.
func (t *Tracker) identity(ctx context.Context) (string, error) {
	if t.actor != "" {
		return t.actor, nil
	}
	return t.store.Viewer(ctx)
}

// claimError builds the typed refusal for an existing claim. actor is the
// effective acting identity, or empty when it could not be read yet: then
// ByActor stays false and Err carries the lookup failure, so a caller can tell
// "someone else holds it" from "cannot say who holds it". A label-only claim
// has no claimant to name and needs no lookup.
func claimError(ctx context.Context, store gh.IssueStore, issue model.Issue, actor string) *ClaimedError {
	refusal := &ClaimedError{Issue: issue.Number, Assignees: slices.Clone(issue.Assignees)}
	if len(refusal.Assignees) == 0 {
		return refusal
	}
	if actor == "" {
		var err error
		if actor, err = store.Viewer(ctx); err != nil {
			refusal.Err = asAuthError(err)
			return refusal
		}
	}
	refusal.Actor = actor
	refusal.ByActor = slices.Contains(refusal.Assignees, actor)
	return refusal
}

// swapPriority enforces the one-priority-label invariant: remove the others,
// add the target if absent.
func swapPriority(ctx context.Context, store gh.IssueStore, issue model.Issue, p model.Priority) error {
	target := p.String()
	for _, l := range issue.Labels {
		if _, ok := model.LabelPriority(l); ok && !strings.EqualFold(l, target) {
			if err := store.RemoveLabel(ctx, issue.Number, l); err != nil {
				return err
			}
		}
	}
	if !issue.HasLabel(target) {
		return store.AddLabels(ctx, issue.Number, []string{target})
	}
	return nil
}

// undoClaim undoes an atomic claim back to the state before: issue is the
// snapshot Start read before claiming. It reports its own failure so the
// caller can show both, since a failed rollback leaves the claim in place.
func undoClaim(ctx context.Context, store gh.IssueStore, issue model.Issue, actor string, force bool) error {
	var errs []error
	if !slices.Contains(issue.Assignees, actor) {
		errs = append(errs, asWriteError(store.RemoveAssignees(ctx, issue.Number, []string{actor})))
	}
	if force {
		// The forced claim displaced the earlier claimants; put them back.
		for _, who := range issue.Assignees {
			if who != actor {
				errs = append(errs, asWriteError(store.AddAssignee(ctx, issue.Number, who)))
			}
		}
	}
	if !issue.InProgress() {
		errs = append(errs, asWriteError(store.RemoveLabel(ctx, issue.Number, model.InProgressLabel)))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("could not release the claim on #%d: %w", issue.Number, err)
	}
	return nil
}
