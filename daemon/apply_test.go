package daemon_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/lumberbarons/hew/daemon"
	"github.com/lumberbarons/hew/tracker"
)

// flakyCheckpoint records every create, then reports a failure to persist on
// the failAt-th one, which makes ApplyPlan stop partway with the issues so far
// recorded: a storage fault in the middle of a plan.
type flakyCheckpoint struct {
	*tracker.MemoryCheckpoint
	failAt, calls int
}

func (f *flakyCheckpoint) RecordCreated(key string, number int) error {
	err := f.MemoryCheckpoint.RecordCreated(key, number)
	f.calls++
	if f.calls == f.failAt {
		return errors.New("persisting the checkpoint failed")
	}
	return err
}

// applyEnv serves a tracker whose first apply plan fails after two creates.
func applyEnv(t *testing.T) *env {
	t.Helper()
	root, target := checkout(t, false)
	tr, err := tracker.Open(context.Background(), tracker.Config{Worktree: root, Actor: "daemon"})
	if err != nil {
		t.Fatal(err)
	}
	runs := 0
	srv := daemon.New(tr, nil, target).WithCheckpoints(func() tracker.Checkpoint {
		runs++
		fail := 0
		if runs == 1 {
			fail = 2
		}
		return &flakyCheckpoint{MemoryCheckpoint: tracker.NewMemoryCheckpoint(), failAt: fail}
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &env{t: t, url: ts.URL, root: root}
}

func threeEntryPlan() map[string]any {
	return map[string]any{"entries": []map[string]any{
		{"id": "a", "title": "First", "type": "task"},
		{"id": "b", "title": "Second", "type": "task"},
		{"id": "c", "title": "Third", "type": "task", "blockedBy": []map[string]any{{"id": "a"}}},
	}}
}

func titles(e *env) map[string]int {
	e.t.Helper()
	_, res := e.call("GET", "/api/issues?state=all", "", nil)
	out := map[string]int{}
	for _, v := range res["_list"].([]any) {
		out[v.(map[string]any)["title"].(string)]++
	}
	return out
}

// TestFailedApplyResumesInsteadOfDuplicating is the retry hazard: a plan that
// stops partway has created some issues, the error toast invites a second
// click, and a second click must not create the first entries again.
func TestFailedApplyResumesInsteadOfDuplicating(t *testing.T) {
	e := applyEnv(t)
	plan := threeEntryPlan()

	status, res := e.call("POST", "/api/apply", "", plan)
	if status != 500 {
		t.Fatalf("first apply = %d %v, want the injected failure", status, res)
	}
	// The failure says what it already made, so the person can see it is part-done.
	created, _ := res["fields"].(map[string]any)["created"].(map[string]any)
	if len(created) != 2 || created["a"] == nil || created["b"] == nil {
		t.Fatalf("failed apply reports created = %v, want a and b", res["fields"])
	}

	status, res = e.call("POST", "/api/apply", "", plan)
	if status != 200 || res["created"] != float64(1) {
		t.Fatalf("retry = %d %v, want only the missing entry created", status, res)
	}
	for title, n := range titles(e) {
		if n != 1 {
			t.Errorf("%q exists %d times after the retry, want once", title, n)
		}
	}
	if got := len(titles(e)); got != 3 {
		t.Errorf("%d distinct issues after the retry, want 3", got)
	}

	// A plan that finished is forgotten: sending it again is a new plan.
	if status, res = e.call("POST", "/api/apply", "", plan); status != 200 || res["created"] != float64(3) {
		t.Errorf("a finished plan sent again = %d %v, want a fresh set of 3", status, res)
	}
}

func TestDryRunNeverRemembersAPlan(t *testing.T) {
	e := applyEnv(t)
	plan := threeEntryPlan()
	for range 2 {
		if status, res := e.call("POST", "/api/apply?dryRun=1", "", plan); status != 200 || res["created"] != float64(0) {
			t.Fatalf("dry run = %d %v", status, res)
		}
	}
	if got := len(titles(e)); got != 0 {
		t.Errorf("dry runs created %d issues", got)
	}
}
