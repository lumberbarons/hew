package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/lumberbarons/hew/pr"
	"github.com/lumberbarons/hew/tracker"
)

// TestClassifyMapsEveryTypedError pins the one place the CLI's exit-code
// contract is translated for HTTP clients: each library error gets a stable
// kind and status, including when a caller wrapped it with %w.
func TestClassifyMapsEveryTypedError(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		kind   string
	}{
		{"canceled", context.Canceled, 499, "canceled"},
		{"deadline", context.DeadlineExceeded, http.StatusGatewayTimeout, "timeout"},
		{"claimed by other", &tracker.ClaimedError{Issue: 1, Assignees: []string{"bob"}, Actor: "alice"}, 409, "claimed_by_other"},
		{"claimed by you", &tracker.ClaimedError{Issue: 1, Assignees: []string{"alice"}, Actor: "alice", ByActor: true}, 409, "claimed_by_you"},
		{"claimed, nobody named", &tracker.ClaimedError{Issue: 1}, 409, "claimed_by_other"},
		{"blocked", &tracker.BlockedError{Issue: 2, OpenBlockers: []int{1}}, 409, "blocked"},
		{"invalid input", &tracker.InvalidInputError{Field: "title", Reason: "empty"}, 400, "invalid_input"},
		{"untriaged", &tracker.UntriagedError{Issue: 3}, 400, "untriaged"},
		{"auth", &tracker.AuthError{}, 401, "auth"},
		{"not found", &tracker.NotFoundError{Issue: 9}, 404, "not_found"},
		{"closed", &tracker.ClosedError{Issue: 1, Reason: "completed"}, 409, "closed"},
		{"epic", &tracker.EpicError{Issue: 4}, 409, "epic"},
		{"not claimed", &tracker.NotClaimedError{Issue: 1}, 409, "not_claimed"},
		{"not owner", &tracker.NotOwnerError{Issue: 1, Assignees: []string{"bob"}, Actor: "alice"}, 409, "not_owner"},
		{"open children", &tracker.OpenChildrenError{Issue: 4, Open: 2}, 409, "open_children"},
		{"cycle", &tracker.CycleError{Cycle: []int{1, 2, 1}}, 409, "cycle"},
		{"unverifiable cycle", &tracker.UnverifiableCycleError{Issue: 1, Blocker: 2}, 409, "unverifiable_cycle"},
		{"not open", &tracker.NotOpenError{Issue: 1, Blocker: 2}, 409, "not_open"},
		{"already parented", &tracker.ParentedError{Issue: 1, Parent: 4}, 409, "already_parented"},
		{"migrated", &tracker.MigratedError{}, 409, "migrated"},
		{"lock timeout", &tracker.LockTimeoutError{Path: "/x"}, 503, "lock_timeout"},
		{"partial write", &tracker.PartialWriteError{Issue: 1, Landed: []int{2}, Failed: 3}, 500, "partial_write"},
		{"partial create", &tracker.PartialCreateError{Issue: tracker.Issue{Number: 5}, Stage: tracker.CreateStageParent}, 500, "partial_create"},
		{"partial edit", &tracker.PartialEditError{Issue: 1, Applied: []string{"title"}, Failed: "priority"}, 500, "partial_edit"},
		{"partial release", &tracker.PartialReleaseError{Issue: 1}, 500, "partial_release"},
		{"partial close", &tracker.PartialCloseError{Issue: 1}, 500, "partial_close"},
		{"partial reopen", &tracker.PartialReopenError{Issue: 1}, 500, "partial_reopen"},
		{"no pull requests", &pr.NoPullRequestsError{}, http.StatusNotImplemented, "no_pull_requests"},
		{"no upstream", &pr.NoUpstreamError{Branch: "feat/x"}, 409, "no_upstream"},
		{"issue inference", &pr.IssueInferenceError{Branch: "feat/x", Claimed: []int{1, 2}}, 400, "issue_inference"},
		{"existing pull request", &pr.ExistingPullRequestError{Issue: 1, Number: 7}, 409, "existing_pull_request"},
		{"no open pull request", &pr.NoOpenPullRequestError{Issue: 1}, 404, "no_open_pull_request"},
		{"ambiguous pull request", &pr.AmbiguousPullRequestError{Head: "feat/x", OpenCount: 2}, 409, "ambiguous_pull_request"},
		{"unclassified", errors.New("disk on fire"), 500, "internal"},
	}
	for _, tc := range cases {
		for _, wrapped := range []bool{false, true} {
			err := tc.err
			label := tc.name
			if wrapped {
				err, label = fmt.Errorf("while serving: %w", tc.err), tc.name+" (wrapped)"
			}
			status, p := classify(err)
			if status != tc.status || p.Kind != tc.kind {
				t.Errorf("%s: classify = %d %q, want %d %q", label, status, p.Kind, tc.status, tc.kind)
			}
			if p.Message == "" {
				t.Errorf("%s: empty message", label)
			}
		}
	}
}

// TestClassifyPrefersTheSpecificWrapper covers the ordering the switch relies
// on: a claim that could not name its claimant wraps an auth failure, and it
// must still be reported as a claim, not as an authentication problem.
func TestClassifyPrefersTheSpecificWrapper(t *testing.T) {
	err := &tracker.ClaimedError{Issue: 1, Err: &tracker.AuthError{}}
	if status, p := classify(err); status != 409 || p.Kind != "claimed_by_other" {
		t.Errorf("classify = %d %q, want 409 claimed_by_other", status, p.Kind)
	}
}

func TestOrEmptyKeepsNilFromMarshalingAsNull(t *testing.T) {
	if got := orEmpty[int](nil); got == nil || len(got) != 0 {
		t.Errorf("orEmpty(nil) = %#v, want an empty non-nil slice", got)
	}
	if got := orEmpty([]int{1}); len(got) != 1 {
		t.Errorf("orEmpty([1]) = %v", got)
	}
}
