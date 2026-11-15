package tracker

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/local"
	"github.com/lumberbarons/hew/internal/model"
)

func commentBodies(issue model.Issue) []string {
	var out []string
	for _, c := range issue.Comments {
		out = append(out, c.Body)
	}
	return out
}

func storeIssue(t *testing.T, ctx context.Context, store gh.IssueStore, number int) model.Issue {
	t.Helper()
	issue, err := store.GetIssue(ctx, number)
	if err != nil {
		t.Fatal(err)
	}
	return issue
}

func TestCloseRecordsTheReasonAndTheStateReason(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts CloseOptions
		want string
	}{
		{"not planned by default", CloseOptions{Reason: "wontfix"}, "NOT_PLANNED"},
		{"completed", CloseOptions{Reason: "wontfix", Completed: true}, "COMPLETED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, store, issue := startFixture(t)
			result, err := WrapAs(store, BackendLocal, "actor-a").Close(ctx, issue.Number, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if result.StateReason != tc.want {
				t.Errorf("result = %+v, want %s", result, tc.want)
			}
			got := storeIssue(t, ctx, store, issue.Number)
			if got.IsOpen() || got.StateReason != tc.want {
				t.Errorf("state = %s %s", got.State, got.StateReason)
			}
			if !slices.Equal(commentBodies(got), []string{"wontfix"}) {
				t.Errorf("comments = %v", commentBodies(got))
			}
		})
	}
}

func TestCloseDuplicateKeepsTheOriginalFindable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reason  string
		comment string
	}{
		{"no reason", "", "Duplicate of #2"},
		{"custom reason", "same bug\n", "same bug\n\nDuplicate of #2"},
		{"reason already naming it", "same as #2", "same as #2"},
		// #22 merely starts with the digits, and other/repo#2 is another
		// repository's issue: neither names this one.
		{"reason naming a longer number", "see #22", "see #22\n\nDuplicate of #2"},
		{"reason naming another repository", "see other/repo#2", "see other/repo#2\n\nDuplicate of #2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, store, issue := startFixture(t)
			if _, err := store.CreateIssue(ctx, "Original", "", []string{"P2", "task"}); err != nil {
				t.Fatal(err)
			}
			result, err := WrapAs(store, BackendLocal, "actor-a").Close(ctx, issue.Number, CloseOptions{Reason: tc.reason, DuplicateOf: 2})
			if err != nil {
				t.Fatal(err)
			}
			if result.StateReason != "DUPLICATE" {
				t.Errorf("state reason = %s", result.StateReason)
			}
			if got := commentBodies(storeIssue(t, ctx, store, issue.Number)); !slices.Equal(got, []string{tc.comment}) {
				t.Errorf("comments = %q, want %q", got, tc.comment)
			}
		})
	}
}

// Every refusal that precedes the comment leaves the issue exactly as it was.
func TestCloseRefusalsLeaveTheIssueUntouched(t *testing.T) {
	for _, tc := range []struct {
		name  string
		opts  CloseOptions
		check func(t *testing.T, err error)
	}{
		{"no reason", CloseOptions{}, func(t *testing.T, err error) {
			var invalid *InvalidInputError
			if !errors.As(err, &invalid) || invalid.Field != "reason" {
				t.Errorf("err = %v, want invalid input on reason", err)
			}
		}},
		{"completed duplicate", CloseOptions{Reason: "r", Completed: true, DuplicateOf: 5}, func(t *testing.T, err error) {
			var invalid *InvalidInputError
			if !errors.As(err, &invalid) || invalid.Field != "completed" {
				t.Errorf("err = %v, want invalid input on completed", err)
			}
		}},
		{"duplicate of itself", CloseOptions{DuplicateOf: 1}, func(t *testing.T, err error) {
			var invalid *InvalidInputError
			if !errors.As(err, &invalid) || invalid.Field != "duplicateOf" {
				t.Errorf("err = %v, want invalid input on duplicateOf", err)
			}
		}},
		{"duplicate of a missing issue", CloseOptions{DuplicateOf: 99}, func(t *testing.T, err error) {
			var notFound *NotFoundError
			if !errors.As(err, &notFound) || notFound.Issue != 99 {
				t.Errorf("err = %v, want a NotFoundError for #99", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, store, issue := startFixture(t)
			_, err := WrapAs(store, BackendLocal, "actor-a").Close(ctx, issue.Number, tc.opts)
			if err == nil {
				t.Fatal("close was accepted")
			}
			tc.check(t, err)
			got := storeIssue(t, ctx, store, issue.Number)
			if !got.IsOpen() || len(got.Comments) != 0 {
				t.Errorf("refused close changed the issue: state %s, comments %v", got.State, commentBodies(got))
			}
		})
	}
}

func TestCloseRefusesAClosedIssueWithoutCommenting(t *testing.T) {
	ctx, store, issue := startFixture(t)
	if err := store.CloseIssue(ctx, issue.Number, gh.CloseCompleted); err != nil {
		t.Fatal(err)
	}
	var closed *ClosedError
	_, err := WrapAs(store, BackendLocal, "actor-a").Close(ctx, issue.Number, CloseOptions{Reason: "again", Force: true})
	if !errors.As(err, &closed) || closed.Issue != issue.Number || closed.Reason != "COMPLETED" {
		t.Fatalf("close = %v, want a ClosedError carrying the close reason", err)
	}
	if got := storeIssue(t, ctx, store, issue.Number); len(got.Comments) != 0 {
		t.Errorf("re-close commented: %v", commentBodies(got))
	}
}

func TestCloseOfAMissingIssueIsNotFound(t *testing.T) {
	ctx, store, _ := startFixture(t)
	var notFound *NotFoundError
	_, err := WrapAs(store, BackendLocal, "actor-a").Close(ctx, 99, CloseOptions{Reason: "r"})
	if !errors.As(err, &notFound) || notFound.Issue != 99 {
		t.Fatalf("close = %v, want a NotFoundError for #99", err)
	}
}

// Closing an epic strands its open children under a closed parent, so it takes
// Force; an epic whose children are done closes like any issue.
func TestCloseEpicWithOpenChildren(t *testing.T) {
	ctx, store, _ := startFixture(t)
	epic, err := store.CreateIssue(ctx, "Epic", "", []string{"P2", "task", "epic"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := store.CreateIssue(ctx, "Child", "", []string{"P2", "task"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddSubIssue(ctx, epic.Number, child.Number, false); err != nil {
		t.Fatal(err)
	}
	tr := WrapAs(store, BackendLocal, "actor-a")

	var open *OpenChildrenError
	if _, err := tr.Close(ctx, epic.Number, CloseOptions{Reason: "abandoned"}); !errors.As(err, &open) || open.Issue != epic.Number || open.Open != 1 {
		t.Fatalf("close = %v, want an OpenChildrenError with 1 open", err)
	}
	if got := storeIssue(t, ctx, store, epic.Number); !got.IsOpen() || len(got.Comments) != 0 {
		t.Fatalf("refused close changed the epic: %s, %v", got.State, commentBodies(got))
	}

	if _, err := tr.Close(ctx, epic.Number, CloseOptions{Reason: "abandoned", Force: true}); err != nil {
		t.Fatalf("forced close: %v", err)
	}
	if storeIssue(t, ctx, store, epic.Number).IsOpen() {
		t.Error("forced close left the epic open")
	}
}

func TestCloseEpicWithNoOpenChildrenCloses(t *testing.T) {
	ctx, store, _ := startFixture(t)
	epic, err := store.CreateIssue(ctx, "Epic", "", []string{"P2", "task", "epic"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := store.CreateIssue(ctx, "Child", "", []string{"P2", "task"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddSubIssue(ctx, epic.Number, child.Number, false); err != nil {
		t.Fatal(err)
	}
	if err := store.CloseIssue(ctx, child.Number, gh.CloseCompleted); err != nil {
		t.Fatal(err)
	}
	if _, err := WrapAs(store, BackendLocal, "actor-a").Close(ctx, epic.Number, CloseOptions{Reason: "done", Completed: true}); err != nil {
		t.Fatal(err)
	}
}

// Close does not touch the claim: a claimed issue closed by hand keeps it.
func TestCloseKeepsTheClaim(t *testing.T) {
	ctx, store, issue := claimedFixture(t, "actor-a")
	if _, err := WrapAs(store, BackendLocal, "actor-a").Close(ctx, issue.Number, CloseOptions{Reason: "r"}); err != nil {
		t.Fatal(err)
	}
	if got := storeIssue(t, ctx, store, issue.Number); !got.InProgress() || !slices.Equal(got.Assignees, []string{"actor-a"}) {
		t.Errorf("close changed the claim: assignees %v, in-progress %v", got.Assignees, got.InProgress())
	}
}

// closeFailingStore fails the state change after the comment has posted.
type closeFailingStore struct{ *local.Store }

func (s closeFailingStore) CloseIssue(context.Context, int, gh.CloseReason) error {
	return errors.New("api unavailable")
}

// The comment is already on the issue when the close fails, so the error says
// so: a retry is not a clean redo, it comments again.
func TestCloseReportsACommentWithoutAClose(t *testing.T) {
	ctx, store, issue := startFixture(t)
	_, err := WrapAs(closeFailingStore{store}, BackendLocal, "actor-a").Close(ctx, issue.Number, CloseOptions{Reason: "wontfix"})
	var partial *PartialCloseError
	if !errors.As(err, &partial) || partial.Issue != issue.Number {
		t.Fatalf("close = %v, want a PartialCloseError", err)
	}
	if partial.Err == nil || partial.Err.Error() != "api unavailable" || !errors.Is(err, partial.Err) {
		t.Errorf("cause = %v", partial.Err)
	}
	if !strings.Contains(err.Error(), "api unavailable") {
		t.Errorf("message drops the cause: %v", err)
	}
	got := storeIssue(t, ctx, store, issue.Number)
	if !got.IsOpen() || !slices.Equal(commentBodies(got), []string{"wontfix"}) {
		t.Errorf("state %s, comments %v: want open with the posted comment", got.State, commentBodies(got))
	}
}

// A comment that fails to post is a plain backend error: nothing was written,
// so a retry is a clean redo.
func TestCloseCommentFailureIsNotPartial(t *testing.T) {
	ctx, store, issue := startFixture(t)
	boom := errors.New("comment boom")
	_, err := WrapAs(commentFailingStore{store, boom}, BackendLocal, "actor-a").Close(ctx, issue.Number, CloseOptions{Reason: "r"})
	var partial *PartialCloseError
	if !errors.Is(err, boom) || errors.As(err, &partial) {
		t.Fatalf("close = %v, want the comment error unwrapped", err)
	}
	if !storeIssue(t, ctx, store, issue.Number).IsOpen() {
		t.Error("issue closed despite the failed comment")
	}
}

type commentFailingStore struct {
	*local.Store
	err error
}

func (s commentFailingStore) Comment(context.Context, int, string) error { return s.err }
