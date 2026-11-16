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

// closedFixture is an issue closed as completed, the state most reopened work
// comes from: a merged PR leaves the claim behind.
func closedFixture(t *testing.T, labels ...string) (context.Context, *local.Store, model.Issue) {
	t.Helper()
	ctx, store, issue := startFixture(t, labels...)
	if err := store.CloseIssue(ctx, issue.Number, gh.CloseCompleted); err != nil {
		t.Fatal(err)
	}
	return ctx, store, issue
}

// TestReopenCommentsAndReopens is close's inverse: the reason comment and the
// state change, in that order, with no retriage.
func TestReopenCommentsAndReopens(t *testing.T) {
	ctx, store, issue := closedFixture(t, "P3", "bug")
	const reason = "#24 stalled; picking this back up"
	result, err := WrapAs(store, BackendLocal, "actor-a").Reopen(ctx, issue.Number, ReopenOptions{Reason: reason})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reopened {
		t.Error("Reopened = false for a closed issue")
	}
	if result.ReleasedLabel || len(result.ReleasedAssignees) != 0 {
		t.Errorf("Released = %+v, want no claim released", result)
	}
	got := storeIssue(t, ctx, store, issue.Number)
	if !got.IsOpen() || got.StateReason != "REOPENED" {
		t.Errorf("state = %s %s, want OPEN REOPENED", got.State, got.StateReason)
	}
	if !slices.Equal(commentBodies(got), []string{reason}) {
		t.Errorf("comments = %v, want the reason", commentBodies(got))
	}
	// Reopening is not retriage: the priority and type the issue closed with
	// are still what the read path shows.
	if !slices.Equal(got.Labels, []string{"P3", "bug"}) {
		t.Errorf("labels = %v, want them untouched", got.Labels)
	}
}

// Reopening an issue that is already open is the state the caller asked for:
// it reports that and writes nothing, so a retried call is not a failure and
// no stray comment documents a reopen that never happened.
func TestReopenOpenIssueIsANoOp(t *testing.T) {
	ctx, store, issue := startFixture(t, "P2", "bug")
	before := storeIssue(t, ctx, store, issue.Number)
	result, err := WrapAs(store, BackendLocal, "actor-a").Reopen(ctx, issue.Number, ReopenOptions{Reason: "r"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Reopened {
		t.Error("Reopened = true for an open issue")
	}
	after := storeIssue(t, ctx, store, issue.Number)
	if len(after.Comments) != 0 {
		t.Errorf("no-op reopen commented: %v", commentBodies(after))
	}
	assertUnchanged(t, before, after)
}

func TestReopenRequiresAReason(t *testing.T) {
	ctx, store, issue := closedFixture(t)
	before := storeIssue(t, ctx, store, issue.Number)
	_, err := WrapAs(store, BackendLocal, "actor-a").Reopen(ctx, issue.Number, ReopenOptions{})
	var invalid *InvalidInputError
	if !errors.As(err, &invalid) || invalid.Field != "reason" {
		t.Fatalf("err = %v, want *InvalidInputError on reason", err)
	}
	assertUnchanged(t, before, storeIssue(t, ctx, store, issue.Number))
}

// A claim is the one thing reopen clears: neither close nor a PR merge removes
// it, and left in place it makes the claim guard lie about who is working.
func TestReopenReleasesStaleClaim(t *testing.T) {
	for _, tc := range []struct {
		name      string
		owners    []string
		labelOnly bool
		wantLabel bool
	}{
		{name: "assignee and label", owners: []string{"alice"}, labelOnly: true, wantLabel: true},
		{name: "label only", labelOnly: true, wantLabel: true},
		{name: "assignee only", owners: []string{"alice"}},
		{name: "unclaimed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, store, issue := startFixture(t, "P2", "bug")
			for _, who := range tc.owners {
				if err := store.AddAssignee(ctx, issue.Number, who); err != nil {
					t.Fatal(err)
				}
			}
			if tc.labelOnly {
				if err := store.AddLabels(ctx, issue.Number, []string{model.InProgressLabel}); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.CloseIssue(ctx, issue.Number, gh.CloseCompleted); err != nil {
				t.Fatal(err)
			}

			result, err := WrapAs(store, BackendLocal, "actor-a").Reopen(ctx, issue.Number, ReopenOptions{Reason: "regressed"})
			if err != nil {
				t.Fatal(err)
			}
			if !result.Reopened {
				t.Error("Reopened = false")
			}
			if result.ReleasedLabel != tc.wantLabel {
				t.Errorf("ReleasedLabel = %v, want %v", result.ReleasedLabel, tc.wantLabel)
			}
			if !slices.Equal(result.ReleasedAssignees, tc.owners) {
				t.Errorf("ReleasedAssignees = %v, want %v", result.ReleasedAssignees, tc.owners)
			}
			got := storeIssue(t, ctx, store, issue.Number)
			if !got.IsOpen() {
				t.Error("issue is still closed")
			}
			if got.Claimed() {
				t.Errorf("reopened issue still claimed: labels %v, assignees %v", got.Labels, got.Assignees)
			}
			if !got.HasLabel("P2") || !got.HasLabel("bug") {
				t.Errorf("triage labels disturbed: %v", got.Labels)
			}
		})
	}
}

// reopenFailingStore fails the state change after the reason comment posted.
type reopenFailingStore struct {
	*local.Store
	err error
}

func (s reopenFailingStore) ReopenIssue(context.Context, int) error { return s.err }

// The comment posts before the state change, so a failure between them says so:
// a retry is not a clean redo, it comments again.
func TestReopenReportsTheCommentWithoutTheStateChange(t *testing.T) {
	ctx, store, issue := closedFixture(t, "P2", "bug")
	boom := errors.New("api unavailable")
	_, err := WrapAs(reopenFailingStore{Store: store, err: boom}, BackendLocal, "actor-a").
		Reopen(ctx, issue.Number, ReopenOptions{Reason: "back on"})
	var partial *PartialReopenError
	if !errors.As(err, &partial) || partial.Stage != ReopenStageComment || partial.Issue != issue.Number {
		t.Fatalf("err = %v, want a PartialReopenError at the comment stage", err)
	}
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the backend cause retained for errors.Is", err)
	}
	got := storeIssue(t, ctx, store, issue.Number)
	if got.IsOpen() {
		t.Error("issue reopened despite the failed state change")
	}
	if !slices.Equal(commentBodies(got), []string{"back on"}) {
		t.Errorf("comments = %v, want the posted reason", commentBodies(got))
	}
}

// claimFailingStore fails the claim release after the reopen landed.
type claimFailingStore struct {
	*local.Store
	labelErr    error
	assigneeErr error
}

func (s claimFailingStore) RemoveLabel(ctx context.Context, number int, label string) error {
	if s.labelErr != nil {
		return s.labelErr
	}
	return s.Store.RemoveLabel(ctx, number, label)
}

func (s claimFailingStore) RemoveAssignees(ctx context.Context, number int, assignees []string) error {
	if s.assigneeErr != nil {
		return s.assigneeErr
	}
	return s.Store.RemoveAssignees(ctx, number, assignees)
}

// The reopen has landed by the time the claim release runs, so a failure there
// says the issue is open and still claimed — a retry is a no-op reopen that
// will not clear it.
func TestReopenReportsAClaimLeftBehind(t *testing.T) {
	t.Run("assignee removal", func(t *testing.T) {
		ctx, store, issue := claimedFixture(t, "alice")
		if err := store.CloseIssue(ctx, issue.Number, gh.CloseCompleted); err != nil {
			t.Fatal(err)
		}
		boom := errors.New("assignee boom")
		_, err := WrapAs(claimFailingStore{Store: store, assigneeErr: boom}, BackendLocal, "actor-a").
			Reopen(ctx, issue.Number, ReopenOptions{Reason: "r"})
		var partial *PartialReopenError
		if !errors.As(err, &partial) || partial.Stage != ReopenStageClaim {
			t.Fatalf("err = %v, want a PartialReopenError at the claim stage", err)
		}
		if !errors.Is(err, boom) {
			t.Errorf("err = %v, want the backend cause retained for errors.Is", err)
		}
		got := storeIssue(t, ctx, store, issue.Number)
		if !got.IsOpen() || len(got.Assignees) != 1 {
			t.Errorf("state = %s assignees %v; want open and still claimed", got.State, got.Assignees)
		}
	})

	t.Run("label removal", func(t *testing.T) {
		ctx, store, issue := claimedFixture(t, "alice")
		if err := store.CloseIssue(ctx, issue.Number, gh.CloseCompleted); err != nil {
			t.Fatal(err)
		}
		boom := errors.New("label boom")
		_, err := WrapAs(claimFailingStore{Store: store, labelErr: boom}, BackendLocal, "actor-a").
			Reopen(ctx, issue.Number, ReopenOptions{Reason: "r"})
		var partial *PartialReopenError
		if !errors.As(err, &partial) || partial.Stage != ReopenStageClaim {
			t.Fatalf("err = %v, want a PartialReopenError at the claim stage", err)
		}
		// The label goes first, so a failure there leaves both halves intact
		// and the assignee removal never runs.
		got := storeIssue(t, ctx, store, issue.Number)
		if !got.InProgress() || len(got.Assignees) != 1 {
			t.Errorf("claim partially released: labels %v assignees %v", got.Labels, got.Assignees)
		}
	})
}

func TestReopenMissingIssueIsNotFound(t *testing.T) {
	ctx, store, _ := startFixture(t)
	var notFound *NotFoundError
	_, err := WrapAs(store, BackendLocal, "actor-a").Reopen(ctx, 99, ReopenOptions{Reason: "r"})
	if !errors.As(err, &notFound) || notFound.Issue != 99 {
		t.Fatalf("err = %v, want a NotFoundError for #99", err)
	}
}

// Cancellation keeps its identity through a partial reopen.
func TestReopenPartialKeepsCancellationIdentity(t *testing.T) {
	ctx, store, issue := closedFixture(t)
	_, err := WrapAs(reopenFailingStore{Store: store, err: context.DeadlineExceeded}, BackendLocal, "actor-a").
		Reopen(ctx, issue.Number, ReopenOptions{Reason: "r"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want errors.Is(err, context.DeadlineExceeded)", err)
	}
}

// The partial errors are what a caller prints; their messages name the state
// that survived rather than only the backend failure.
func TestPartialReopenMessagesNameWhatSurvived(t *testing.T) {
	comment := (&PartialReopenError{Issue: 7, Stage: ReopenStageComment, Err: errors.New("boom")}).Error()
	if !strings.Contains(comment, "comment") || !strings.Contains(comment, "#7") {
		t.Errorf("comment-stage message = %q", comment)
	}
	claim := (&PartialReopenError{Issue: 7, Stage: ReopenStageClaim, Err: errors.New("boom")}).Error()
	if !strings.Contains(claim, "still claimed") || !strings.Contains(claim, "#7") {
		t.Errorf("claim-stage message = %q", claim)
	}
}
