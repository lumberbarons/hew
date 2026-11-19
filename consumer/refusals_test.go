// This file is done-when's typed-refusal item for the local backend: claims
// and ownership, blockers and cycles, and a closed target all come back as the
// public tracker types a consumer branches on, from real calls against a real
// tracker.
package consumer_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/lumberbarons/hew/tracker"
)

func TestExternalConsumerMatchesClaimAndOwnershipRefusals(t *testing.T) {
	tr := externalTracker(t)
	ctx := context.Background()

	issue, err := tr.Create(ctx, tracker.CreateRequest{Title: "Owned", Type: "task"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	number := issue.Issue.Number

	// Nobody holds it yet, so a release is refused as unclaimed.
	err = tr.Release(ctx, number, tracker.ReleaseOptions{})
	var unclaimed *tracker.NotClaimedError
	if !errors.As(err, &unclaimed) || unclaimed.Issue != number {
		t.Fatalf("Release of an unclaimed issue = %T (%v), want *tracker.NotClaimedError", err, err)
	}

	if _, err := tr.As("owner").Start(ctx, number, tracker.StartOptions{}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// The claim outcome is the library's classification, not the consumer's
	// own reading of ByActor: another actor is told to pick the next item,
	// the owner to resume.
	_, err = tr.As("intruder").Start(ctx, number, tracker.StartOptions{})
	if outcome, ok := tracker.StartOutcomeOf(err); !ok || outcome != tracker.StartClaimedByOther {
		t.Fatalf("Start by another actor = %q, %t; want %q", outcome, ok, tracker.StartClaimedByOther)
	}
	_, err = tr.As("owner").Start(ctx, number, tracker.StartOptions{})
	if outcome, ok := tracker.StartOutcomeOf(err); !ok || outcome != tracker.StartClaimedByYou {
		t.Fatalf("Start by the claimant = %q, %t; want %q", outcome, ok, tracker.StartClaimedByYou)
	}

	err = tr.As("intruder").Release(ctx, number, tracker.ReleaseOptions{})
	var notOwner *tracker.NotOwnerError
	if !errors.As(err, &notOwner) {
		t.Fatalf("Release by another actor = %T (%v), want *tracker.NotOwnerError", err, err)
	}
	if notOwner.Issue != number || notOwner.Actor != "intruder" || !slices.Equal(notOwner.Assignees, []string{"owner"}) {
		t.Errorf("NotOwnerError = %+v, want the issue, actor and owner", notOwner)
	}

	// The claim is still the owner's after the refused release.
	read, err := tr.Get(ctx, number)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(read.Assignees, []string{"owner"}) {
		t.Errorf("assignees = %v, want the owner's claim intact", read.Assignees)
	}
}

func TestExternalConsumerMatchesBlockerAndCycleRefusals(t *testing.T) {
	tr := externalTracker(t)
	ctx := context.Background()

	blocker, err := tr.Create(ctx, tracker.CreateRequest{Title: "Blocker", Type: "bug"})
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := tr.Create(ctx, tracker.CreateRequest{Title: "Blocked", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Block(ctx, blocked.Issue.Number, []int{blocker.Issue.Number}, tracker.BlockOptions{}); err != nil {
		t.Fatalf("Block: %v", err)
	}

	_, err = tr.Start(ctx, blocked.Issue.Number, tracker.StartOptions{})
	var openBlockers *tracker.BlockedError
	if !errors.As(err, &openBlockers) {
		t.Fatalf("Start behind a blocker = %T (%v), want *tracker.BlockedError", err, err)
	}
	if !slices.Equal(openBlockers.OpenBlockers, []int{blocker.Issue.Number}) {
		t.Errorf("OpenBlockers = %v", openBlockers.OpenBlockers)
	}
	if outcome, ok := tracker.StartOutcomeOf(err); !ok || outcome != tracker.StartBlocked {
		t.Errorf("Start behind a blocker = %q, %t; want %q", outcome, ok, tracker.StartBlocked)
	}
	// A refusal that is not one of the three decisions is reported as itself.
	if outcome, ok := tracker.StartOutcomeOf(&tracker.ClosedError{Issue: 1}); ok {
		t.Errorf("a closed target classified as start outcome %q", outcome)
	}

	// The cycle check is client-side and mandatory, so the reverse edge is
	// refused with the ordered path rather than reaching the backend.
	_, err = tr.Block(ctx, blocker.Issue.Number, []int{blocked.Issue.Number}, tracker.BlockOptions{})
	var cycle *tracker.CycleError
	if !errors.As(err, &cycle) {
		t.Fatalf("Block forming a cycle = %T (%v), want *tracker.CycleError", err, err)
	}
	if len(cycle.Cycle) < 2 || cycle.Cycle[0] != cycle.Cycle[len(cycle.Cycle)-1] {
		t.Errorf("Cycle = %v, want an ordered path closing on itself", cycle.Cycle)
	}
}

func TestExternalConsumerMatchesAClosedTargetRefusal(t *testing.T) {
	tr := externalTracker(t)
	ctx := context.Background()

	issue, err := tr.Create(ctx, tracker.CreateRequest{Title: "Finished", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Close(ctx, issue.Issue.Number, tracker.CloseOptions{Reason: "done", Completed: true}); err != nil {
		t.Fatalf("Close: %v", err)
	}

	title := "Renamed"
	_, err = tr.Edit(ctx, tracker.EditRequest{Issue: issue.Issue.Number, Title: &title})
	var closed *tracker.ClosedError
	if !errors.As(err, &closed) || closed.Issue != issue.Issue.Number {
		t.Fatalf("Edit on a closed issue = %T (%v), want *tracker.ClosedError", err, err)
	}
	if closed.Reason != "COMPLETED" {
		t.Errorf("ClosedError.Reason = %q, want the backend's close reason", closed.Reason)
	}

	// AllowClosed is the documented override, and it is the caller's choice.
	if _, err := tr.Edit(ctx, tracker.EditRequest{Issue: issue.Issue.Number, Title: &title, AllowClosed: true}); err != nil {
		t.Errorf("Edit with AllowClosed: %v", err)
	}
}
