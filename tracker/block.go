package tracker

import (
	"context"
	"slices"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/model"
)

// Writer carries the dependency operations over an already-open store. Block
// and Unblock need neither the backend nor the acting identity, only the
// store, so Writer is the write-side seam hew's own CLI wraps its resolved
// store with. External consumers use the same methods on the Tracker Open
// returns.
type Writer struct{ store gh.IssueStore }

// WrapWriter makes a Writer from an already-open store. A consumer outside
// this module cannot name gh.IssueStore and uses Open instead.
func WrapWriter(store gh.IssueStore) *Writer { return &Writer{store: store} }

// BlockResult reports what Block changed. Added and Existing are the
// requested blockers in request order; an empty Added means the call was a
// no-op.
type BlockResult struct {
	Added    []int
	Existing []int
}

// UnblockResult reports what Unblock changed. Removed and Missing are the
// requested blockers in request order; an empty Removed means the call was a
// no-op.
type UnblockResult struct {
	Removed []int
	Missing []int
}

// Block adds native dependencies after a transitive client-side cycle check
// — GitHub itself only rejects self-blocks and direct two-issue cycles. It
// refuses a closed target unless opts.AllowClosed is set; a closed blocker is
// refused outright, since closed blockers don't block.
//
// Several blockers are validated together and written only once every one has
// passed, so a refusal never leaves a half-applied set. Each is checked
// against the graph as it stands: every new edge starts at issue, so a cycle
// through two of them would revisit issue and is already a cycle through one.
func (w *Writer) Block(ctx context.Context, number int, blockers []int, opts BlockOptions) (BlockResult, error) {
	return block(ctx, w.store, number, blockers, opts)
}

// Block is Writer.Block over the tracker Open returned.
func (t *Tracker) Block(ctx context.Context, number int, blockers []int, opts BlockOptions) (BlockResult, error) {
	return block(ctx, t.store, number, blockers, opts)
}

func block(ctx context.Context, store gh.IssueStore, number int, blockers []int, opts BlockOptions) (BlockResult, error) {
	if len(blockers) == 0 {
		return BlockResult{}, &InvalidInputError{Field: "blockers", Reason: "at least one is required"}
	}
	blockers = uniqueInts(blockers)
	issues, err := store.ListIssues(ctx, []gh.IssueState{gh.StateOpen})
	if err != nil {
		return BlockResult{}, err
	}
	byNum := model.ByNumber(issues)
	issue, ok := byNum[number]
	if !ok {
		// Absent from the open set means closed or nonexistent. Re-read to
		// tell them apart; only this path pays for the extra call.
		target, getErr := store.GetIssue(ctx, number)
		if getErr != nil {
			return BlockResult{}, &NotFoundError{Issue: number}
		}
		if err := closedGuard(target, opts.AllowClosed); err != nil {
			return BlockResult{}, err
		}
		// Overridden: a closed issue is absent from the open-issue graph, so
		// no open blocked-by edge can reach it and the cycle check is
		// vacuously safe for it.
		issue = target
	}
	checker := model.NewChecker(issues)
	var result BlockResult
	for _, blocker := range blockers {
		if _, ok := byNum[blocker]; !ok {
			return BlockResult{}, &NotOpenError{Issue: number, Blocker: blocker}
		}
		if slices.Contains(issue.OpenBlockers(), blocker) {
			result.Existing = append(result.Existing, blocker)
			continue
		}
		check := checker.Check(number, blocker)
		if check.Cycle != nil {
			return BlockResult{}, &CycleError{Cycle: check.Cycle}
		}
		if !check.Verifiable {
			return BlockResult{}, &UnverifiableCycleError{Issue: number, Blocker: blocker}
		}
		result.Added = append(result.Added, blocker)
	}
	for i, blocker := range result.Added {
		if err := store.AddBlockedBy(ctx, number, blocker); err != nil {
			return BlockResult{}, partialWriteError(err, number, result.Added[:i], blocker, result.Added[i+1:])
		}
	}
	return result, nil
}

// Unblock removes dependencies. It refuses a closed target unless
// opts.AllowClosed is set. A blocker that is not there is skipped and named
// rather than failing the call.
func (w *Writer) Unblock(ctx context.Context, number int, blockers []int, opts BlockOptions) (UnblockResult, error) {
	return unblock(ctx, w.store, number, blockers, opts)
}

// Unblock is Writer.Unblock over the tracker Open returned.
func (t *Tracker) Unblock(ctx context.Context, number int, blockers []int, opts BlockOptions) (UnblockResult, error) {
	return unblock(ctx, t.store, number, blockers, opts)
}

func unblock(ctx context.Context, store gh.IssueStore, number int, blockers []int, opts BlockOptions) (UnblockResult, error) {
	if len(blockers) == 0 {
		return UnblockResult{}, &InvalidInputError{Field: "blockers", Reason: "at least one is required"}
	}
	blockers = uniqueInts(blockers)
	issue, err := store.GetIssue(ctx, number)
	if err != nil {
		return UnblockResult{}, err
	}
	if err := closedGuard(issue, opts.AllowClosed); err != nil {
		return UnblockResult{}, err
	}
	var result UnblockResult
	for _, blocker := range blockers {
		if slices.ContainsFunc(issue.BlockedBy, func(b model.Ref) bool { return b.Number == blocker }) {
			result.Removed = append(result.Removed, blocker)
		} else {
			result.Missing = append(result.Missing, blocker)
		}
	}
	for i, blocker := range result.Removed {
		if err := store.RemoveBlockedBy(ctx, number, blocker); err != nil {
			return UnblockResult{}, partialWriteError(err, number, result.Removed[:i], blocker, result.Removed[i+1:])
		}
	}
	return result, nil
}

// closedGuard refuses a dependency write aimed at a closed issue. The close
// reason rides in the error so a caller can say whether the target was
// completed or dropped, which is what separates stale state from a typo.
func closedGuard(issue model.Issue, allowClosed bool) error {
	if issue.IsOpen() || allowClosed {
		return nil
	}
	return &ClosedError{Issue: issue.Number, Reason: issue.StateReason}
}

// partialWriteError frames a failure after earlier edges in the same call
// landed. When the first write failed nothing changed, so the backend error
// stands as it is.
func partialWriteError(err error, number int, landed []int, failed int, rest []int) error {
	if len(landed) == 0 {
		return err
	}
	return &PartialWriteError{
		Issue:        number,
		Landed:       slices.Clone(landed),
		Failed:       failed,
		NotAttempted: slices.Clone(rest),
		Err:          err,
	}
}

// uniqueInts drops repeated numbers, keeping first-seen order, so a request
// naming a blocker twice writes the edge once.
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
