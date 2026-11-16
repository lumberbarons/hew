package tracker

import "context"

// ReopenResult reports what Reopen changed. Reopened is false when the issue
// was already open: the requested state held, so nothing was written.
// ReleasedLabel and ReleasedAssignees name the stale claim the reopen cleared
// — the in-progress label and the assignees that carried it. Either half can
// be empty when the claim had only one.
type ReopenResult struct {
	Reopened          bool
	ReleasedLabel     bool
	ReleasedAssignees []string
}

// Reopen is Close's inverse: comment opts.Reason on a closed issue, reopen it,
// and clear the stale claim the close left behind.
//
// Closing comments routinely encode the condition to reopen on, so acting on
// one keeps close's comment-plus-state-change shape rather than degrading to a
// bare state flip. There is no retriage: the issue rejoins the read path with
// the priority, type and area labels it already had.
//
// The claim is the one thing reopen clears. Neither close nor a PR merge
// removes the in-progress label or the assignee, so most issues closed in the
// normal workflow still carry the claim that produced the fix; left in place,
// it makes the claim guard lie about who is working. Whoever wants the work
// back runs Start.
//
// Reopening an issue that is already open is not an error: the requested state
// holds, the call reports Reopened false and writes nothing, so an idempotent
// retry does not look like a failure and no comment documents a reopen that
// never happened.
//
// The refusals are typed: InvalidInputError for a missing Reason and
// NotFoundError for an issue the tracker does not have. A failure after the
// first write is a PartialReopenError whose Stage says what survived.
func (t *Tracker) Reopen(ctx context.Context, number int, opts ReopenOptions) (ReopenResult, error) {
	if opts.Reason == "" {
		return ReopenResult{}, &InvalidInputError{Field: "reason", Reason: "is required"}
	}
	issue, err := t.store.GetIssue(ctx, number)
	if err != nil {
		return ReopenResult{}, asReadError(err)
	}
	if issue.IsOpen() {
		return ReopenResult{}, nil
	}
	if err := t.store.Comment(ctx, number, opts.Reason); err != nil {
		return ReopenResult{}, asWriteError(err)
	}
	if err := t.store.ReopenIssue(ctx, number); err != nil {
		return ReopenResult{}, &PartialReopenError{Issue: number, Stage: ReopenStageComment, Err: asWriteError(err)}
	}
	released, err := clearClaim(ctx, t.store, issue)
	if err != nil {
		return ReopenResult{}, &PartialReopenError{Issue: number, Stage: ReopenStageClaim, Err: asWriteError(err)}
	}
	return ReopenResult{
		Reopened:          true,
		ReleasedLabel:     released.Label,
		ReleasedAssignees: released.Assignees,
	}, nil
}
