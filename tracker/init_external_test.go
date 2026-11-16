package tracker_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/tracker"
)

// This file is a consumer's test in the same sense as public_errors_test.go: it
// imports only the public package, so nothing here depends on an internal
// implementation. It does not use a hew binary, GitHub credentials or the
// network: initialization and the work that follows run in-process against a
// real local tracker.

// externalCheckout makes a plain git checkout with no tracker yet, isolated
// from the developer's git environment.
func externalCheckout(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "GIT_") {
			t.Setenv(k, os.Getenv(k)) // registers the restore
			if err := os.Unsetenv(k); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "--initial-branch=main").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	// macOS's temp dir is a symlink; git reports the real path, so compare
	// against it.
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// TestExternalCallerInitializesLocalTrackerAndCreatesWork is done-when's first
// item: a consumer initializes a real local tracker, opens it and creates work
// with the public package alone.
func TestExternalCallerInitializesLocalTrackerAndCreatesWork(t *testing.T) {
	root := externalCheckout(t)
	ctx := context.Background()

	res, err := tracker.Init(ctx, tracker.InitConfig{Worktree: root, Backend: tracker.BackendLocal})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if res.Backend != tracker.BackendLocal || res.Path != filepath.Join(root, ".hew") {
		t.Fatalf("Init = {%q, %q}, want the local tracker at %s", res.Backend, res.Path, filepath.Join(root, ".hew"))
	}
	if len(res.CreatedLabels) != 0 {
		t.Errorf("CreatedLabels = %v; a local tracker creates no labels", res.CreatedLabels)
	}
	if len(res.Notices) != 1 || !strings.Contains(res.Notices[0], "git clean -fdx") {
		t.Errorf("Notices = %v; want the untracked-tracker warning as data", res.Notices)
	}

	tr, err := tracker.Open(ctx, tracker.Config{Worktree: root, Actor: "consumer"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if tr.Backend() != tracker.BackendLocal || tr.Identity() != "consumer" {
		t.Fatalf("tracker = {%q, %q}, want the local tracker as consumer", tr.Backend(), tr.Identity())
	}
	applied, err := tr.ApplyPlan(ctx, []tracker.PlanEntry{
		{ID: "work", Title: "Consumer-created work", Type: "task", Priority: "P1"},
	}, tracker.ApplyOptions{})
	if err != nil {
		t.Fatalf("ApplyPlan: %v", err)
	}
	number := applied.Mapping["work"]
	if number == 0 {
		t.Fatalf("ApplyPlan mapping = %v", applied.Mapping)
	}
	got, err := tr.Get(ctx, number)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Title != "Consumer-created work" || got.Priority == nil || *got.Priority != "P1" {
		t.Errorf("issue = %+v, want the created work", got)
	}

	// Initialization is idempotent: a second run changes nothing and keeps the
	// work the first one made room for.
	again, err := tracker.Init(ctx, tracker.InitConfig{Worktree: root, Backend: tracker.BackendLocal})
	if err != nil {
		t.Fatalf("second Init: %v", err)
	}
	if len(again.Notices) != 0 {
		t.Errorf("a rerun noticed %v", again.Notices)
	}
	if _, err := tr.Get(ctx, number); err != nil {
		t.Errorf("a rerun lost the issue: %v", err)
	}
}

// TestExternalCallerIsRefusedAfterTheTrackerMoved is the refusal half of
// done-when's second item: a repo that moved to GitHub refuses local
// initialization instead of reporting a tracker no command would read.
func TestExternalCallerIsRefusedAfterTheTrackerMoved(t *testing.T) {
	root := externalCheckout(t)
	ctx := context.Background()
	if _, err := tracker.Init(ctx, tracker.InitConfig{Worktree: root, Backend: tracker.BackendLocal}); err != nil {
		t.Fatal(err)
	}
	// A migrated repo's config says github over the archive; write it the way
	// the migration leaves it.
	config := filepath.Join(root, ".hew", "config.json")
	if err := os.WriteFile(config, []byte(`{"backend":"github","version":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := tracker.Init(ctx, tracker.InitConfig{Worktree: root, Backend: tracker.BackendLocal})
	if err == nil {
		t.Fatal("Init accepted a repo whose tracker moved to GitHub")
	}
	if !strings.Contains(err.Error(), "not locally") {
		t.Errorf("err = %v, want the refusal to name the backend", err)
	}
}
