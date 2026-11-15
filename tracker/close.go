package tracker

import (
	"context"
	"fmt"
	"strings"

	"github.com/lumberbarons/hew/internal/gh"
)

// CloseResult reports a completed closure. StateReason is the backend's close
// reason: COMPLETED, NOT_PLANNED or DUPLICATE.
type CloseResult struct {
	StateReason string
}

// Close comments opts.Reason on an issue and closes it: not planned unless
// opts.Completed or opts.DuplicateOf says otherwise. Closing through a merged
// pull request is the norm; this is for work that was dropped, duplicated or
// finished out of band.
//
// A duplicate closure with no Reason comments "Duplicate of #n", and a custom
// Reason that does not already name the original gets that line appended, so
// the original stays findable from the closed duplicate — GitHub stores it
// nowhere but the comment.
//
// Every refusal happens before the comment is posted, so a refused close
// leaves the issue untouched: InvalidInputError for a missing Reason or
// contradictory options, ClosedError for an issue already closed (its Reason
// says whether it was completed or dropped), OpenChildrenError for an epic
// whose sub-issues are still open unless opts.Force, and NotFoundError for a
// DuplicateOf that names no issue.
//
// The comment and the close are two writes. If the close fails after the
// comment posted, the error is a PartialCloseError: a retry will comment
// again.
func (t *Tracker) Close(ctx context.Context, number int, opts CloseOptions) (CloseResult, error) {
	if opts.Completed && opts.DuplicateOf > 0 {
		return CloseResult{}, &InvalidInputError{Field: "completed", Reason: "cannot be combined with duplicateOf"}
	}
	if opts.DuplicateOf == number {
		return CloseResult{}, &InvalidInputError{Field: "duplicateOf", Reason: "is the issue being closed"}
	}
	reason := opts.Reason
	stateReason := gh.CloseNotPlanned
	switch {
	case opts.Completed:
		stateReason = gh.CloseCompleted
	case opts.DuplicateOf > 0:
		stateReason = gh.CloseDuplicate
		if reason == "" {
			reason = fmt.Sprintf("Duplicate of #%d", opts.DuplicateOf)
		}
	}
	if reason == "" {
		return CloseResult{}, &InvalidInputError{Field: "reason", Reason: "is required"}
	}
	issue, err := t.store.GetIssue(ctx, number)
	if err != nil {
		return CloseResult{}, asReadError(err)
	}
	if !issue.IsOpen() {
		// No override: re-closing a closed issue has no effect to authorize.
		return CloseResult{}, &ClosedError{Issue: number, Reason: issue.StateReason}
	}
	// The count is the server-side summary (total minus completed), so a
	// capped sub-issue connection cannot hide a child from the guard.
	if open := issue.SubIssuesTotal - issue.SubIssuesCompleted; issue.IsEpic() && open > 0 && !opts.Force {
		return CloseResult{}, &OpenChildrenError{Issue: number, Open: open}
	}
	if opts.DuplicateOf > 0 {
		if _, err := t.store.GetIssue(ctx, opts.DuplicateOf); err != nil {
			return CloseResult{}, asReadError(err)
		}
		if !mentionsIssue(reason, opts.DuplicateOf) {
			reason = strings.TrimRight(reason, " \t\r\n") + fmt.Sprintf("\n\nDuplicate of #%d", opts.DuplicateOf)
		}
	}
	if err := t.store.Comment(ctx, number, reason); err != nil {
		return CloseResult{}, err
	}
	if err := t.store.CloseIssue(ctx, number, stateReason); err != nil {
		return CloseResult{}, &PartialCloseError{Issue: number, Err: err}
	}
	return CloseResult{StateReason: string(stateReason)}, nil
}

// mentionsIssue reports whether text already names #number as a reference of
// this repository: not a longer number that merely starts with it, and not
// the tail of another repository's owner/repo#number.
func mentionsIssue(text string, number int) bool {
	ref := fmt.Sprintf("#%d", number)
	for from := 0; ; {
		i := strings.Index(text[from:], ref)
		if i < 0 {
			return false
		}
		start, end := from+i, from+i+len(ref)
		from = end
		if start > 0 && (text[start-1] == '/' || isWordByte(text[start-1])) {
			continue
		}
		if end < len(text) && isWordByte(text[end]) {
			continue
		}
		return true
	}
}

func isWordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}
