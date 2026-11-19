package tracker

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestTypedErrorsPreserveDetailsWhenWrapped(t *testing.T) {
	for _, tc := range []struct {
		err     error
		message string
	}{
		{&ClaimedError{Issue: 42, Assignees: []string{"alice", "bob"}, Actor: "worker"}, "issue #42 is already claimed by alice, bob"},
		{&ClaimedError{Issue: 42, Assignees: []string{"worker"}, Actor: "worker", ByActor: true}, "issue #42 is already claimed by acting identity worker"},
		{&ClaimedError{Issue: 42, Actor: "worker"}, "issue #42 is already claimed"}, // label-only claim
		{&BlockedError{Issue: 42, OpenBlockers: []int{7, 8}}, "issue #42 has open blockers: #7, #8"},
		{&AuthError{Err: errors.New("credentials expired")}, "authentication failed: credentials expired"},
		{&AuthError{}, "authentication failed"},
		{&NotFoundError{Issue: 42}, "issue #42 not found"},
		{&ClosedError{Issue: 42, Reason: "completed"}, "issue #42 is closed (completed)"},
		{&ClosedError{Issue: 42}, "issue #42 is closed"},
		{&EpicError{Issue: 42}, "issue #42 is an epic; epics are never worked directly"},
		{&UntriagedError{Issue: 42}, "issue #42 is untriaged; a priority is required to claim it"},
		{&CycleError{Cycle: []int{42, 7, 8, 42}}, "dependency cycle: #42 -> #7 -> #8 -> #42"},
		{&CycleError{Cycle: []int{42, 42}}, "dependency cycle: #42 -> #42"},
		{&UnverifiableCycleError{Issue: 42, Blocker: 7}, "cannot verify dependency #42 -> #7 is cycle-free: blocker connections were capped"},
		{&NotOpenError{Issue: 42, Blocker: 7}, "issue #7 is not an open issue, so it cannot block #42"},
		{&NotClaimedError{Issue: 42}, "issue #42 is not claimed"},
		{&NotOwnerError{Issue: 42, Assignees: []string{"alice"}, Actor: "worker"}, "issue #42 is not claimed solely by acting identity worker"},
		{&PartialReleaseError{Issue: 42, Err: errors.New("lock timeout")}, "releasing #42 failed; its claim may be partially cleared: lock timeout"},
		{&OpenChildrenError{Issue: 42, Open: 3}, "issue #42 is an epic with 3 open sub-issues"},
		{&PartialCloseError{Issue: 42, Err: errors.New("api unavailable")}, "posted the reason comment on #42 but closing it failed: api unavailable"},
		{&MigratedError{Path: "/repo/.hew", Repo: "owner/repo"}, "local tracker /repo/.hew was migrated to GitHub repository owner/repo"},
		{&MigratedError{Path: "/repo/.hew"}, "local tracker /repo/.hew was migrated to GitHub and is an archive now"},
		{&LockTimeoutError{Path: "/repo/.hew/lock", Timeout: 30 * time.Second}, "timed out after 30s waiting for tracker lock /repo/.hew/lock"},
		{&InvalidInputError{Field: "priority", Reason: "must be P0..P4"}, "invalid priority: must be P0..P4"},
	} {
		original := tc.err
		t.Run(fmt.Sprintf("%T/%s", original, tc.message), func(t *testing.T) {
			if got := original.Error(); got != tc.message {
				t.Errorf("Error() = %q, want %q", got, tc.message)
			}
			wrapped := fmt.Errorf("operation failed: %w", original)
			target := reflect.New(reflect.TypeOf(original))
			if !errors.As(wrapped, target.Interface()) {
				t.Fatalf("errors.As did not match %T", original)
			}
			if target.Elem().Interface() != original {
				t.Fatal("errors.As lost the original payload")
			}
			if message := original.Error(); message == "" || strings.Contains(message, "--") || strings.Contains(message, "gh auth login") {
				t.Errorf("error must describe the failure without CLI instructions: %q", message)
			}
		})
	}
}

// TestPartialWriteErrorNamesTheEdges pins the message and the cause chain a
// caller needs to neither retry an edge that landed nor assume the rest
// exist. It is not in the table above because its payload is uncomparable.
func TestPartialWriteErrorNamesTheEdges(t *testing.T) {
	cause := errors.New("boom")
	original := &PartialWriteError{Issue: 1, Landed: []int{2}, Failed: 3, NotAttempted: []int{4}, Err: cause}
	err := fmt.Errorf("wrapped: %w", original)
	var partial *PartialWriteError
	if !errors.As(err, &partial) {
		t.Fatalf("errors.As did not match %T", original)
	}
	if partial != original {
		t.Fatal("errors.As lost the original payload")
	}
	if !errors.Is(err, cause) {
		t.Fatal("partial write error lost its cause")
	}
	want := "writing a dependency edge for #1 failed at #3 after #2: boom; not attempted: #4"
	if got := partial.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if got := (&PartialWriteError{Issue: 1, Landed: []int{2}, Failed: 3, Err: cause}).Error(); strings.Contains(got, "not attempted") {
		t.Errorf("Error() = %q, want no unattempted clause", got)
	}
}

func TestAuthErrorUnwrapsCause(t *testing.T) {
	cause := errors.New("permission denied")
	err := fmt.Errorf("opening tracker: %w", &AuthError{Err: cause})
	if !errors.Is(err, cause) {
		t.Fatal("authentication error lost its cause")
	}
}

// TestTranslatedErrorsKeepTheirCause is the mirrored half: an error the
// boundary built from a store refusal carries that refusal, so a caller can
// still match the cause and prints the message that names the tracker rather
// than a generic retelling.
func TestTranslatedErrorsKeepTheirCause(t *testing.T) {
	cause := errors.New("/repo/.hew tracks issues in \"github\", not locally")
	for _, err := range []error{
		&MigratedError{Path: "/repo/.hew", Repo: "owner/repo", Err: cause},
		&LockTimeoutError{Path: "/repo/.hew/lock", Timeout: time.Second, Err: cause},
	} {
		if !errors.Is(err, cause) {
			t.Errorf("%T lost its cause", err)
		}
		if got := err.Error(); got != cause.Error() {
			t.Errorf("%T.Error() = %q, want the store's own message", err, got)
		}
	}
	// A claim whose refinement lookup failed is still about the claim, but the
	// auth failure behind it has to be matchable: an orchestrator re-authenticates
	// rather than treating every worker as a rival.
	claim := &ClaimedError{Issue: 7, Assignees: []string{"alice"}, Err: &AuthError{Err: errors.New("401")}}
	var auth *AuthError
	if !errors.As(claim, &auth) {
		t.Error("ClaimedError did not expose the auth failure behind it")
	}
}

// TestStartOutcomeOfNamesTheThreeDecisions pins the classification an agent
// loop branches on: the typed errors keep the facts, and this names the
// decision they ask for. Everything else a Start can refuse with is reported
// as itself instead of being folded into one of the three.
func TestStartOutcomeOfNamesTheThreeDecisions(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		outcome StartOutcome
		ok      bool
	}{
		{"somebody else's claim", &ClaimedError{Issue: 1, Assignees: []string{"bob"}, Actor: "alice"}, StartClaimedByOther, true},
		{"the acting identity's claim", &ClaimedError{Issue: 1, Assignees: []string{"alice"}, Actor: "alice", ByActor: true}, StartClaimedByYou, true},
		{"label-only claim", &ClaimedError{Issue: 1}, StartClaimedByOther, true},
		{"claimant could not be read", &ClaimedError{Issue: 1, Assignees: []string{"bob"}, Err: errors.New("no viewer")}, StartClaimedByOther, true},
		{"open blockers", &BlockedError{Issue: 1, OpenBlockers: []int{2}}, StartBlocked, true},
		{"closed target", &ClosedError{Issue: 1}, "", false},
		{"epic target", &EpicError{Issue: 1}, "", false},
		{"untriaged target", &UntriagedError{Issue: 1}, "", false},
		{"invalid input", &InvalidInputError{Field: "priority"}, "", false},
		{"an unrelated failure", errors.New("disk on fire"), "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Refusals arrive wrapped in the operation that produced them.
			wrapped := fmt.Errorf("start failed: %w", tc.err)
			outcome, ok := StartOutcomeOf(wrapped)
			if outcome != tc.outcome || ok != tc.ok {
				t.Errorf("StartOutcomeOf(%v) = %q, %t; want %q, %t", tc.err, outcome, ok, tc.outcome, tc.ok)
			}
		})
	}
	if outcome, ok := StartOutcomeOf(nil); outcome != "" || ok {
		t.Errorf("StartOutcomeOf(nil) = %q, %t; want no outcome", outcome, ok)
	}
}

// TestStartOutcomeIsAStableName keeps the values the CLI, the daemon and a
// consumer key on from drifting: they are the recorded names of the three
// decisions, not just whatever the constants happen to hold.
func TestStartOutcomeIsAStableName(t *testing.T) {
	for outcome, want := range map[StartOutcome]string{
		StartClaimedByOther: "claimed_by_other",
		StartClaimedByYou:   "claimed_by_you",
		StartBlocked:        "blocked",
	} {
		if string(outcome) != want {
			t.Errorf("StartOutcome = %q, want %q", outcome, want)
		}
	}
}
