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
		{&MigratedError{Path: "/repo/.hew", Repo: "owner/repo"}, "local tracker /repo/.hew was migrated to GitHub repository owner/repo"},
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
