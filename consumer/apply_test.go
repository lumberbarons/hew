// This file is the plan scenario on the local backend: validation before any
// write, a dry run that writes nothing, and a checkpoint that makes a rerun a
// no-op.
package consumer_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lumberbarons/hew/tracker"
)

func TestExternalConsumerValidatesAPlanBeforeWriting(t *testing.T) {
	tr := externalTracker(t)
	ctx := context.Background()

	for _, tc := range []struct {
		name    string
		entries []tracker.PlanEntry
	}{
		{
			name: "dependency cycle",
			entries: []tracker.PlanEntry{
				{ID: "a", Title: "A", Type: "task", BlockedBy: []tracker.PlanRef{{ID: "b"}}},
				{ID: "b", Title: "B", Type: "task", BlockedBy: []tracker.PlanRef{{ID: "a"}}},
			},
		},
		{
			name: "dangling reference",
			entries: []tracker.PlanEntry{
				{ID: "a", Title: "A", Type: "task", Parent: &tracker.PlanRef{ID: "missing"}},
			},
		},
		{
			name: "invalid priority",
			entries: []tracker.PlanEntry{
				{ID: "a", Title: "A", Type: "task", Priority: "high"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tr.ApplyPlan(ctx, tc.entries, tracker.ApplyOptions{})
			var invalid *tracker.InvalidInputError
			if !errors.As(err, &invalid) {
				t.Fatalf("err = %T (%v), want *tracker.InvalidInputError", err, err)
			}
		})
	}

	snapshot, err := tr.Snapshot(ctx, tracker.SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(snapshot.Issues()); got != 0 {
		t.Errorf("a rejected plan left %d issue(s) behind", got)
	}
}

func TestExternalConsumerRerunOfACompletedPlanIsANoOp(t *testing.T) {
	tr := externalTracker(t)
	ctx := context.Background()

	entries := []tracker.PlanEntry{
		{ID: "one", Title: "One", Type: "task", Priority: "P2"},
		{ID: "two", Title: "Two", Type: "bug", Priority: "P1"},
	}
	checkpoint := tracker.NewMemoryCheckpoint()
	first, err := tr.ApplyPlan(ctx, entries, tracker.ApplyOptions{Checkpoint: checkpoint})
	if err != nil {
		t.Fatalf("ApplyPlan: %v", err)
	}
	if first.Created != 2 {
		t.Fatalf("first run created %d, want 2", first.Created)
	}

	second, err := tr.ApplyPlan(ctx, entries, tracker.ApplyOptions{Checkpoint: checkpoint})
	if err != nil {
		t.Fatalf("ApplyPlan rerun: %v", err)
	}
	if second.Created != 0 {
		t.Errorf("rerun created %d, want 0", second.Created)
	}
	for _, entry := range second.Applied {
		if entry.Created {
			t.Errorf("rerun created %+v", entry)
		}
	}

	snapshot, err := tr.Snapshot(ctx, tracker.SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(snapshot.Issues()); got != 2 {
		t.Errorf("tracker holds %d issues after two runs, want 2", got)
	}
}
