// This file is done-when's concurrency item: independent actors and a shared
// client are exercised under the race detector, through public operations only.
package consumer_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/lumberbarons/hew/tracker"
)

// TestExternalConsumerRunsConcurrentLocalActors claims one issue per actor
// from a shared local tracker and races two actors for one issue. The local
// store's atomic claim is what makes exactly one of them win.
func TestExternalConsumerRunsConcurrentLocalActors(t *testing.T) {
	tr := externalTracker(t)
	ctx := context.Background()

	const actors = 8
	entries := make([]tracker.PlanEntry, 0, actors+1)
	for i := range actors {
		entries = append(entries, tracker.PlanEntry{
			ID: fmt.Sprintf("work-%d", i), Title: fmt.Sprintf("Work %d", i), Type: "task", Priority: "P2",
		})
	}
	applied, err := tr.ApplyPlan(ctx, entries, tracker.ApplyOptions{})
	if err != nil {
		t.Fatalf("ApplyPlan: %v", err)
	}

	var wg sync.WaitGroup
	errs := make([]error, actors)
	for i := range actors {
		wg.Add(1)
		go func() {
			defer wg.Done()
			number := applied.Mapping[fmt.Sprintf("work-%d", i)]
			_, errs[i] = tr.As(fmt.Sprintf("worker-%d", i)).Start(ctx, number, tracker.StartOptions{})
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("worker-%d Start: %v", i, err)
		}
	}

	// Two actors race the same unclaimed issue: the atomic claim has one
	// winner and the loser gets the public claim refusal, never both.
	contested, err := tr.Create(ctx, tracker.CreateRequest{Title: "Contested", Type: "task"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	var won, lost int
	var mu sync.Mutex
	wg = sync.WaitGroup{}
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := tr.As(fmt.Sprintf("racer-%d", i)).Start(ctx, contested.Issue.Number, tracker.StartOptions{})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				won++
				return
			}
			var claimed *tracker.ClaimedError
			if !errors.As(err, &claimed) {
				t.Errorf("racer-%d err = %T (%v), want *tracker.ClaimedError", i, err, err)
				return
			}
			lost++
		}()
	}
	wg.Wait()
	if won != 1 || lost != 1 {
		t.Errorf("contested claim = %d winners, %d losers; want exactly one of each", won, lost)
	}

	// Reads of one snapshot are pure and safe to run in parallel.
	snapshot, err := tr.Snapshot(ctx, tracker.SnapshotOptions{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	for range actors {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if len(snapshot.Ready()) == 0 && len(snapshot.List(tracker.ListFilter{})) == 0 {
				t.Error("concurrent read returned nothing")
			}
		}()
	}
	wg.Wait()
}

// TestExternalConsumerSharesOneGitHubClient runs reads and writes from several
// goroutines on one open tracker. The client's node-ID cache, viewer cache and
// budget are synchronized, so the race detector is the assertion; the final
// count checks that every concurrent write landed.
func TestExternalConsumerSharesOneGitHubClient(t *testing.T) {
	fake := newGitHubFake()
	tr := openGitHubTracker(t, fake)
	ctx := context.Background()

	for i := range 6 {
		if _, err := tr.Create(ctx, tracker.CreateRequest{
			Title: fmt.Sprintf("Shared %d", i), Type: "task", Priority: "P2",
		}); err != nil {
			t.Fatalf("seed create %d: %v", i, err)
		}
	}

	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch i % 6 {
			case 0:
				if _, err := tr.Snapshot(ctx, tracker.SnapshotOptions{}); err != nil {
					t.Errorf("Snapshot: %v", err)
				}
			case 1:
				if _, err := tr.Detail(ctx, 101); err != nil {
					t.Errorf("Detail: %v", err)
				}
			case 2:
				if _, err := tr.Search(ctx, tracker.SearchRequest{Terms: "Shared"}); err != nil {
					t.Errorf("Search: %v", err)
				}
			case 3:
				if _, err := tr.Primer(ctx); err != nil {
					t.Errorf("Primer: %v", err)
				}
			case 4:
				if _, err := tr.Create(ctx, tracker.CreateRequest{
					Title: fmt.Sprintf("Concurrent %d", i), Type: "task", Priority: "P2",
				}); err != nil {
					t.Errorf("Create: %v", err)
				}
			case 5:
				title := fmt.Sprintf("Edited %d", i)
				if _, err := tr.Edit(ctx, tracker.EditRequest{Issue: 101 + i%6, Title: &title}); err != nil {
					t.Errorf("Edit: %v", err)
				}
			}
		}()
	}
	wg.Wait()

	// The six seeded issues plus the two concurrent creates all landed.
	snapshot, err := tr.Snapshot(ctx, tracker.SnapshotOptions{})
	if err != nil {
		t.Fatalf("final Snapshot: %v", err)
	}
	if got := len(snapshot.List(tracker.ListFilter{})); got != 8 {
		t.Errorf("tracker holds %d issues after the concurrent run, want 8", got)
	}
}
