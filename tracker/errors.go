package tracker

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lumberbarons/hew/internal/gh"
)

// ClaimedError reports an existing claim. Assignees may be empty for a
// label-only claim. Actor is the effective acting identity; on GitHub it is
// the authenticated login. ByActor means that identity owns the claim.
//
// Err is set when the acting identity could not be read while answering: Actor
// is then empty and ByActor false, so a caller can tell "someone else holds
// it" from "cannot say who holds it" instead of reading the empty fields as a
// positive answer.
type ClaimedError struct {
	Issue     int
	Assignees []string
	Actor     string
	ByActor   bool
	Err       error
}

func (e *ClaimedError) Error() string {
	if e.ByActor {
		return fmt.Sprintf("issue #%d is already claimed by acting identity %s", e.Issue, e.Actor)
	}
	if len(e.Assignees) > 0 {
		return fmt.Sprintf("issue #%d is already claimed by %s", e.Issue, strings.Join(e.Assignees, ", "))
	}
	return fmt.Sprintf("issue #%d is already claimed", e.Issue)
}

// BlockedError reports the open blockers preventing a claim.
type BlockedError struct {
	Issue        int
	OpenBlockers []int
}

func (e *BlockedError) Error() string {
	return fmt.Sprintf("issue #%d has open blockers: %s", e.Issue, formatNumbers(e.OpenBlockers, ", "))
}

// AuthError reports an authentication failure, retaining the backend cause.
type AuthError struct{ Err error }

func (e *AuthError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("authentication failed: %v", e.Err)
	}
	return "authentication failed"
}
func (e *AuthError) Unwrap() error { return e.Err }

// NotFoundError reports an issue absent from the tracker. Err carries the
// store's own not-found error when the boundary translated one, so the
// backend's message — which may name the repository — is what a caller prints.
type NotFoundError struct {
	Issue int
	Err   error
}

func (e *NotFoundError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return fmt.Sprintf("issue #%d not found", e.Issue)
}

func (e *NotFoundError) Unwrap() error { return e.Err }

// ClosedError reports an operation refused on a closed target. Reason is
// the backend's close reason, when available.
type ClosedError struct {
	Issue  int
	Reason string
}

func (e *ClosedError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("issue #%d is closed (%s)", e.Issue, e.Reason)
	}
	return fmt.Sprintf("issue #%d is closed", e.Issue)
}

// EpicError reports a claim on an epic. Epics are containers: their children
// are the workable issues.
type EpicError struct{ Issue int }

func (e *EpicError) Error() string {
	return fmt.Sprintf("issue #%d is an epic; epics are never worked directly", e.Issue)
}

// UntriagedError reports a claim on an issue with no priority. Claiming an
// untriaged issue is triage, so the caller has to supply the priority.
type UntriagedError struct{ Issue int }

func (e *UntriagedError) Error() string {
	return fmt.Sprintf("issue #%d is untriaged; a priority is required to claim it", e.Issue)
}

// CycleError reports a dependency cycle, including self-blocks. Cycle is
// the ordered issue-number path, ending with its first member again.
type CycleError struct{ Cycle []int }

func (e *CycleError) Error() string {
	return "dependency cycle: " + formatNumbers(e.Cycle, " -> ")
}

// UnverifiableCycleError reports a proposed edge whose cycle check could
// not be completed because blocker connections were capped.
type UnverifiableCycleError struct {
	Issue   int
	Blocker int
}

func (e *UnverifiableCycleError) Error() string {
	return fmt.Sprintf("cannot verify dependency #%d -> #%d is cycle-free: blocker connections were capped", e.Issue, e.Blocker)
}

// NotOpenError reports a dependency reference that is not an open issue:
// closed or absent from the tracker, and either way not something that
// blocks. Blocker names the reference; Issue names the edge's source.
type NotOpenError struct {
	Issue   int
	Blocker int
}

func (e *NotOpenError) Error() string {
	return fmt.Sprintf("issue #%d is not an open issue, so it cannot block #%d", e.Blocker, e.Issue)
}

// PartialWriteError reports a run of dependency-edge writes that stopped
// partway: the edges in Landed were written, Failed is the edge whose write
// failed, and NotAttempted never ran. Err is the backend cause, retained for
// errors.Is and errors.As.
type PartialWriteError struct {
	Issue        int
	Landed       []int
	Failed       int
	NotAttempted []int
	Err          error
}

func (e *PartialWriteError) Error() string {
	msg := fmt.Sprintf("writing a dependency edge for #%d failed at #%d after %s: %v",
		e.Issue, e.Failed, formatNumbers(e.Landed, ", "), e.Err)
	if len(e.NotAttempted) > 0 {
		msg += "; not attempted: " + formatNumbers(e.NotAttempted, ", ")
	}
	return msg
}

func (e *PartialWriteError) Unwrap() error { return e.Err }

// NotClaimedError reports a release of an issue nobody holds.
type NotClaimedError struct{ Issue int }

func (e *NotClaimedError) Error() string {
	return fmt.Sprintf("issue #%d is not claimed", e.Issue)
}

// NotOwnerError reports a release the acting identity may not make: the claim
// is not solely theirs. Assignees is empty for a label-only claim. Actor is the
// acting identity the claim was compared with.
type NotOwnerError struct {
	Issue     int
	Assignees []string
	Actor     string
}

func (e *NotOwnerError) Error() string {
	return fmt.Sprintf("issue #%d is not claimed solely by acting identity %s", e.Issue, e.Actor)
}

// PartialReleaseError reports a release that failed after it began clearing
// the claim, so the claim may be partly cleared. Err is the backend cause.
type PartialReleaseError struct {
	Issue int
	Err   error
}

func (e *PartialReleaseError) Error() string {
	return fmt.Sprintf("releasing #%d failed; its claim may be partially cleared: %v", e.Issue, e.Err)
}

func (e *PartialReleaseError) Unwrap() error { return e.Err }

// OpenChildrenError reports a close of an epic that still has Open open
// sub-issues, which would be stranded under a closed parent.
type OpenChildrenError struct {
	Issue int
	Open  int
}

func (e *OpenChildrenError) Error() string {
	return fmt.Sprintf("issue #%d is an epic with %d open sub-issues", e.Issue, e.Open)
}

// PartialCloseError reports a close whose reason comment posted but whose
// state change failed. A retry will comment again. Err is the backend cause.
type PartialCloseError struct {
	Issue int
	Err   error
}

func (e *PartialCloseError) Error() string {
	return fmt.Sprintf("posted the reason comment on #%d but closing it failed: %v", e.Issue, e.Err)
}

func (e *PartialCloseError) Unwrap() error { return e.Err }

// MigratedError reports that a local tracker has moved to a GitHub repo.
type MigratedError struct {
	Path string
	Repo string
}

func (e *MigratedError) Error() string {
	return fmt.Sprintf("local tracker %s was migrated to GitHub repository %s", e.Path, e.Repo)
}

// LockTimeoutError reports an expired wait for a local tracker's lock.
type LockTimeoutError struct {
	Path    string
	Timeout time.Duration
}

func (e *LockTimeoutError) Error() string {
	return fmt.Sprintf("timed out after %s waiting for tracker lock %s", e.Timeout, e.Path)
}

// InvalidInputError reports an invalid argument or plan entry field.
// Field names a library input, rather than a CLI flag.
type InvalidInputError struct {
	Field  string
	Reason string
}

func (e *InvalidInputError) Error() string {
	return fmt.Sprintf("invalid %s: %s", e.Field, e.Reason)
}

// asReadError types a store failure at the tracker boundary: an absent issue
// becomes NotFoundError, an authentication failure AuthError. Each keeps the
// store's error as the cause, so the message and errors.Is retain what the
// backend actually reported rather than a generic retelling.
func asReadError(err error) error {
	if err == nil {
		return nil
	}
	var notFound *gh.NotFoundError
	if errors.As(err, &notFound) {
		return &NotFoundError{Issue: notFound.Issue, Err: notFound}
	}
	return asAuthError(err)
}

func formatNumbers(numbers []int, separator string) string {
	refs := make([]string, len(numbers))
	for n, number := range numbers {
		refs[n] = fmt.Sprintf("#%d", number)
	}
	return strings.Join(refs, separator)
}
