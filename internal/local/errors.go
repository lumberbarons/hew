package local

import (
	"fmt"
	"strings"
	"time"
)

// MigratedError reports a write refused because the tracker no longer says
// local: a finished migration left issues.jsonl as an archive. Repo is the
// GitHub repository the migration record names, empty when that record is
// missing or unreadable. The tracker package maps it to its public
// MigratedError, so a caller outside this module never names this type.
type MigratedError struct {
	Dir     string
	Backend string
	Repo    string
}

func (e *MigratedError) Error() string {
	if e.Repo != "" {
		return fmt.Sprintf("%s tracks issues in %q, not locally: it was already migrated to %s", e.Dir, e.Backend, e.Repo)
	}
	return fmt.Sprintf("%s tracks issues in %q, not locally", e.Dir, e.Backend)
}

// LockTimeoutError reports that a writer waited out its lock timeout. Detail
// says what was holding the lock, when the store could tell — a running
// migration, or another hew process — and is empty when it could not. The
// tracker package maps it to its public LockTimeoutError.
type LockTimeoutError struct {
	Path    string
	Timeout time.Duration
	Detail  string
}

func (e *LockTimeoutError) Error() string {
	msg := fmt.Sprintf("timed out after %s waiting for the lock %s", e.Timeout, e.Path)
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

// NotClaimedError reports a release of an issue nobody holds, found under the
// store's lock rather than in the caller's earlier read. The tracker package
// maps it to its public NotClaimedError.
type NotClaimedError struct{ Issue int }

func (e *NotClaimedError) Error() string {
	return fmt.Sprintf("#%d is not claimed", e.Issue)
}

// NotOwnerError reports a release the acting identity may not make: under the
// lock the claim is not solely theirs. Assignees is empty for a label-only
// claim; Actor is the identity the claim was compared with. The tracker
// package maps it to its public NotOwnerError.
type NotOwnerError struct {
	Issue     int
	Assignees []string
	Actor     string
}

func (e *NotOwnerError) Error() string {
	if len(e.Assignees) == 0 {
		return fmt.Sprintf("#%d is not claimed solely by %s: it carries an in-progress label and no assignee", e.Issue, e.Actor)
	}
	return fmt.Sprintf("#%d is not claimed solely by %s: assigned to %s", e.Issue, e.Actor, strings.Join(e.Assignees, ", "))
}
