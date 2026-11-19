package tracker_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"

	"github.com/lumberbarons/hew/tracker"
)

// newLocalTracker opens a throwaway local tracker in a fresh git checkout, so
// the examples run without GitHub, gh credentials or a network.
func newLocalTracker(ctx context.Context) (*tracker.Tracker, func()) {
	dir, err := os.MkdirTemp("", "hew-example-")
	if err != nil {
		log.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", dir, "init", "--quiet").CombinedOutput(); err != nil {
		log.Fatalf("git init: %v: %s", err, out)
	}
	if _, err := tracker.Init(ctx, tracker.InitConfig{Worktree: dir, Backend: tracker.BackendLocal}); err != nil {
		log.Fatal(err)
	}
	// Open takes the worktree and, on a local tracker, the claimant as
	// arguments; it never reads the process's working directory.
	t, err := tracker.Open(ctx, tracker.Config{Worktree: dir, Actor: "worker-1"})
	if err != nil {
		log.Fatal(err) // *tracker.AuthError, *tracker.InvalidInputError, ...
	}
	return t, func() { _ = os.RemoveAll(dir) }
}

func create(ctx context.Context, t *tracker.Tracker, title string) int {
	res, err := t.Create(ctx, tracker.CreateRequest{
		Title: title,
		Type:  "task",
		Body: tracker.Body{Sections: tracker.Sections{
			Goal:     title,
			Approach: "Do it.",
			DoneWhen: []string{"It is done"},
		}},
	})
	if err != nil {
		log.Fatal(err)
	}
	return res.Issue.Number
}

// Reads: one fetch of the open issues, queried client-side, then a single
// issue's full view.
func Example_reads() {
	ctx := context.Background()
	t, cleanup := newLocalTracker(ctx)
	defer cleanup()

	parse := create(ctx, t, "Parse the config")
	create(ctx, t, "Render the report")

	snap, err := t.Snapshot(ctx, tracker.SnapshotOptions{}) // open issues, one fetch
	if err != nil {
		log.Fatal(err)
	}
	for _, issue := range snap.Ready() {
		fmt.Println(issue.Number, issue.Title)
	}

	// A single issue's view carries its body, comments and advisory text findings.
	detail, err := t.Detail(ctx, parse)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(detail.Issue.Number, detail.Issue.Title, len(detail.TextFindings))

	// Output:
	// 1 Parse the config
	// 2 Render the report
	// 1 Parse the config 0
}

// Dependency writes are cycle-checked client-side, exactly as `hew block` is,
// because GitHub accepts cycles longer than two issues.
func Example_block() {
	ctx := context.Background()
	t, cleanup := newLocalTracker(ctx)
	defer cleanup()

	a := create(ctx, t, "First")
	b := create(ctx, t, "Second")

	if _, err := t.Block(ctx, b, []int{a}, tracker.BlockOptions{}); err != nil {
		log.Fatal(err)
	}
	_, err := t.Block(ctx, a, []int{b}, tracker.BlockOptions{})
	var cycle *tracker.CycleError
	if errors.As(err, &cycle) {
		fmt.Println(cycle.Cycle) // the ordered path
	}

	// Output:
	// [1 2 1]
}

// A local tracker carries one claimant per worker. As shares the open store;
// on GitHub every claim is the authenticated login and As changes nothing.
func Example_claims() {
	ctx := context.Background()
	t, cleanup := newLocalTracker(ctx)
	defer cleanup()

	n := create(ctx, t, "Shared work")

	worker3 := t.As("worker-3")
	fmt.Println("claiming as", worker3.Identity())
	if _, err := worker3.Start(ctx, n, tracker.StartOptions{}); err != nil {
		log.Fatal(err)
	}

	// A second claimant is refused with a typed error, and StartOutcomeOf
	// names the decision a work loop acts on.
	_, err := t.Start(ctx, n, tracker.StartOptions{})
	if outcome, ok := tracker.StartOutcomeOf(err); ok {
		fmt.Println(outcome)
	}

	// Output:
	// claiming as worker-3
	// claimed_by_other
}
