package tracker

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/local"
	"github.com/lumberbarons/hew/internal/model"
)

// claimedFixture is startFixture with the issue claimed by each of owners.
func claimedFixture(t *testing.T, owners ...string) (context.Context, *local.Store, model.Issue) {
	t.Helper()
	ctx, store, issue := startFixture(t)
	for _, who := range owners {
		if err := store.AddAssignee(ctx, issue.Number, who); err != nil {
			t.Fatal(err)
		}
	}
	if len(owners) > 0 {
		if err := store.AddLabels(ctx, issue.Number, []string{model.InProgressLabel}); err != nil {
			t.Fatal(err)
		}
	}
	return ctx, store, issue
}

func assertReleased(t *testing.T, ctx context.Context, store gh.IssueStore, number int) {
	t.Helper()
	got, err := store.GetIssue(ctx, number)
	if err != nil {
		t.Fatal(err)
	}
	if got.Claimed() || !got.IsOpen() {
		t.Errorf("after release: assignees %v, in-progress %v, state %s", got.Assignees, got.InProgress(), got.State)
	}
	if !slices.Contains(got.Labels, "P2") || !slices.Contains(got.Labels, "task") {
		t.Errorf("release touched other labels: %v", got.Labels)
	}
}

// Both halves of a claim go, on the store that releases atomically and on the
// one that does not.
func TestReleaseClearsTheActorsOwnClaim(t *testing.T) {
	for _, tc := range []struct {
		name  string
		store func(*local.Store) gh.IssueStore
	}{
		{"atomic release", func(s *local.Store) gh.IssueStore { return s }},
		{"check-then-act", func(s *local.Store) gh.IssueStore { return storeOnly{s} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, store, issue := claimedFixture(t, "actor-a")
			tr := WrapAs(tc.store(store), BackendLocal, "actor-a")
			if err := tr.Release(ctx, issue.Number, ReleaseOptions{}); err != nil {
				t.Fatal(err)
			}
			assertReleased(t, ctx, store, issue.Number)
		})
	}
}

func TestReleaseForceClearsAnyClaim(t *testing.T) {
	for _, tc := range []struct {
		name   string
		owners []string
		label  bool
	}{
		{"someone else's", []string{"actor-z"}, false},
		{"shared", []string{"actor-a", "actor-z"}, false},
		{"label only", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, store, issue := claimedFixture(t, tc.owners...)
			if tc.label {
				if err := store.AddLabels(ctx, issue.Number, []string{model.InProgressLabel}); err != nil {
					t.Fatal(err)
				}
			}
			tr := WrapAs(storeOnly{store}, BackendLocal, "actor-a")
			if err := tr.Release(ctx, issue.Number, ReleaseOptions{Force: true}); err != nil {
				t.Fatal(err)
			}
			assertReleased(t, ctx, store, issue.Number)
		})
	}
}

// Releasing what nobody holds is a refusal even when forced: there is nothing
// to force.
func TestReleaseRefusesAnUnclaimedIssue(t *testing.T) {
	for _, force := range []bool{false, true} {
		ctx, store, issue := startFixture(t)
		tr := WrapAs(store, BackendLocal, "actor-a")
		var notClaimed *NotClaimedError
		err := tr.Release(ctx, issue.Number, ReleaseOptions{Force: force})
		if !errors.As(err, &notClaimed) || notClaimed.Issue != issue.Number {
			t.Errorf("force=%v: release = %v, want a NotClaimedError for #%d", force, err, issue.Number)
		}
	}
}

func TestReleaseRefusesAClaimThatIsNotSolelyTheActors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		owners []string
		label  bool
		want   []string
	}{
		{"someone else's", []string{"actor-z"}, false, []string{"actor-z"}},
		{"shared", []string{"actor-a", "actor-z"}, false, []string{"actor-a", "actor-z"}},
		{"label only", nil, true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, store, issue := claimedFixture(t, tc.owners...)
			if tc.label {
				if err := store.AddLabels(ctx, issue.Number, []string{model.InProgressLabel}); err != nil {
					t.Fatal(err)
				}
			}
			tr := WrapAs(store, BackendLocal, "actor-a")
			var notOwner *NotOwnerError
			err := tr.Release(ctx, issue.Number, ReleaseOptions{})
			if !errors.As(err, &notOwner) {
				t.Fatalf("release = %v, want a NotOwnerError", err)
			}
			if notOwner.Issue != issue.Number || notOwner.Actor != "actor-a" || !slices.Equal(notOwner.Assignees, tc.want) {
				t.Errorf("NotOwnerError = %+v", notOwner)
			}
			got, err := store.GetIssue(ctx, issue.Number)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Claimed() {
				t.Error("refused release still cleared the claim")
			}
		})
	}
}

// A deferred identity is resolved through the store's Viewer, so the claim is
// compared with whoever the store says is acting.
func TestReleaseResolvesADeferredIdentity(t *testing.T) {
	ctx, store, issue := claimedFixture(t, "actor-a")
	if err := WrapAs(store, BackendLocal, "").Release(ctx, issue.Number, ReleaseOptions{}); err != nil {
		t.Fatal(err)
	}
	assertReleased(t, ctx, store, issue.Number)
}

func TestReleaseOfAMissingIssueIsNotFound(t *testing.T) {
	ctx, store, _ := startFixture(t)
	var notFound *NotFoundError
	if err := WrapAs(store, BackendLocal, "actor-a").Release(ctx, 99, ReleaseOptions{}); !errors.As(err, &notFound) || notFound.Issue != 99 {
		t.Fatalf("release = %v, want a NotFoundError for #99", err)
	}
}

// On a store without atomic release the label goes first, so a failure on the
// assignee removal leaves a claim that is visibly still held; the error says
// it may be half cleared and keeps the cause.
func TestReleaseReportsAPartiallyClearedClaim(t *testing.T) {
	ctx, store, issue := claimedFixture(t, "actor-a")
	if err := store.AddLabels(ctx, issue.Number, []string{model.InProgressLabel}); err != nil {
		t.Fatal(err)
	}
	failing := storeOnly{&swapFailingStore{Store: store, failRemove: true}}
	err := WrapAs(failing, BackendLocal, "actor-a").Release(ctx, issue.Number, ReleaseOptions{})
	var partial *PartialReleaseError
	if !errors.As(err, &partial) || partial.Issue != issue.Number {
		t.Fatalf("release = %v, want a PartialReleaseError", err)
	}
	if partial.Err == nil || partial.Err.Error() != "lock timeout" || !errors.Is(err, partial.Err) {
		t.Errorf("cause = %v", partial.Err)
	}
	got, err := store.GetIssue(ctx, issue.Number)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Claimed() {
		t.Error("claim fully cleared despite the failed assignee removal")
	}
}
