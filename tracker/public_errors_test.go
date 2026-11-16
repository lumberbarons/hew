package tracker_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/tracker"
)

// This file is an external consumer's test: it imports only the public
// package, opens a tracker the way a program outside this module does, and
// matches what comes back with the public types. A test inside package
// tracker could satisfy the same assertions while an internal error still
// leaked, because it can name the internal type; this one cannot.

// externalCheckout opens a local tracker in a throwaway checkout using the
// documented on-disk shape, so nothing here depends on an internal package.
func externalCheckout(t *testing.T) (string, *tracker.Tracker) {
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
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", root, "init", "--initial-branch=main").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	dir := filepath.Join(root, ".hew")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		".gitignore":   "*\n",
		"config.json":  `{"backend":"local","version":1,"id":"external-consumer"}` + "\n",
		"issues.jsonl": `{"_type":"meta","version":1}` + "\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tr, err := tracker.Open(context.Background(), tracker.Config{Worktree: root})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return root, tr
}

func externalTracker(t *testing.T) *tracker.Tracker {
	t.Helper()
	_, tr := externalCheckout(t)
	return tr
}

// TestMissingTargetsArePublicNotFoundErrors is the consumer's contract: every
// operation that takes an issue number answers a number the tracker does not
// have with the public NotFoundError, whichever stage discovers it.
func TestMissingTargetsArePublicNotFoundErrors(t *testing.T) {
	tr := externalTracker(t)
	ctx := context.Background()
	const missing = 999

	ops := map[string]func() error{
		"Get": func() error {
			_, err := tr.Get(ctx, missing)
			return err
		},
		"Start": func() error {
			_, err := tr.Start(ctx, missing, tracker.StartOptions{})
			return err
		},
		"Release": func() error {
			return tr.Release(ctx, missing, tracker.ReleaseOptions{})
		},
		"Close": func() error {
			_, err := tr.Close(ctx, missing, tracker.CloseOptions{Reason: "done"})
			return err
		},
		"Edit": func() error {
			title := "Renamed"
			_, err := tr.Edit(ctx, tracker.EditRequest{Issue: missing, Title: &title})
			return err
		},
		"Reopen": func() error {
			_, err := tr.Reopen(ctx, missing, tracker.ReopenOptions{Reason: "r"})
			return err
		},
		"Block": func() error {
			_, err := tr.Block(ctx, missing, []int{1}, tracker.BlockOptions{})
			return err
		},
		"Unblock": func() error {
			_, err := tr.Unblock(ctx, missing, []int{1}, tracker.BlockOptions{})
			return err
		},
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			err := op()
			if err == nil {
				t.Fatalf("%s on a missing issue returned no error", name)
			}
			var notFound *tracker.NotFoundError
			if !errors.As(err, &notFound) {
				t.Fatalf("%s error = %T (%v), want *tracker.NotFoundError", name, err, err)
			}
			if notFound.Issue != missing {
				t.Errorf("%s Issue = %d, want %d", name, notFound.Issue, missing)
			}
			// The concrete type has to live in the public package: an internal
			// type that merely satisfies the same shape is not matchable by a
			// consumer that cannot import it.
			typ := reflect.TypeOf(err)
			if typ.Kind() == reflect.Pointer {
				typ = typ.Elem()
			}
			if got := typ.PkgPath(); got != "github.com/lumberbarons/hew/tracker" {
				t.Errorf("%s error type is %T from %q, want a type from the public package", name, err, got)
			}
		})
	}
}

// TestExternalCallerEditsAndReopens is done-when's first item from outside the
// module: a consumer that can name only the public package edits every field,
// moves and removes a parent, and reopens closed work with a reason.
func TestExternalCallerEditsAndReopens(t *testing.T) {
	tr := externalTracker(t)
	ctx := context.Background()

	applied, err := tr.ApplyPlan(ctx, []tracker.PlanEntry{
		{ID: "epic", Title: "Epic: parent", Type: tracker.PlanTypeEpic},
		{ID: "other", Title: "Epic: other", Type: tracker.PlanTypeEpic},
		{ID: "work", Title: "Work", Type: "task"},
	}, tracker.ApplyOptions{})
	if err != nil {
		t.Fatalf("ApplyPlan: %v", err)
	}
	number := applied.Mapping["work"]
	parent := applied.Mapping["epic"]
	other := applied.Mapping["other"]
	if number == 0 || parent == 0 || other == 0 {
		t.Fatalf("ApplyPlan mapping = %v", applied.Mapping)
	}

	title, body, priority, typ := "Renamed", "### Goal\n\nrewritten\n", "P1", "bug"
	edit, err := tr.Edit(ctx, tracker.EditRequest{
		Issue:    number,
		Title:    &title,
		Body:     &body,
		Priority: &priority,
		Type:     &typ,
		AddAreas: []string{"api"},
		Parent:   &parent,
	})
	if err != nil {
		t.Fatalf("Edit: %v", err)
	}
	wantApplied := []string{"priority", "type", "addAreas", "title", "body", "parent"}
	if !slices.Equal(edit.Applied, wantApplied) {
		t.Errorf("Applied = %v, want %v", edit.Applied, wantApplied)
	}

	got, err := tr.Get(ctx, number)
	if err != nil {
		t.Fatalf("Get after edit: %v", err)
	}
	if got.Title != title {
		t.Errorf("title = %q, want %q", got.Title, title)
	}
	if got.Body == nil || *got.Body != body {
		t.Errorf("body = %v, want %q", got.Body, body)
	}
	if got.Priority == nil || *got.Priority != priority {
		t.Errorf("priority = %v, want %q", got.Priority, priority)
	}
	if got.Type == nil || *got.Type != typ {
		t.Errorf("type = %v, want %q", got.Type, typ)
	}
	if !slices.Equal(got.Areas, []string{"api"}) {
		t.Errorf("areas = %v, want [api]", got.Areas)
	}
	if got.Parent == nil || *got.Parent != parent {
		t.Errorf("parent = %v, want #%d", got.Parent, parent)
	}

	if _, err := tr.Edit(ctx, tracker.EditRequest{Issue: number, Parent: &other}); err != nil {
		t.Fatalf("Edit (move parent): %v", err)
	}
	moved, err := tr.Get(ctx, number)
	if err != nil {
		t.Fatal(err)
	}
	if moved.Parent == nil || *moved.Parent != other {
		t.Errorf("parent after move = %v, want #%d", moved.Parent, other)
	}

	if _, err := tr.Close(ctx, number, tracker.CloseOptions{Reason: "done", Completed: true}); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := tr.Reopen(ctx, number, tracker.ReopenOptions{Reason: "regressed in v1.2"})
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if !reopened.Reopened {
		t.Error("Reopen reported the issue was already open")
	}
	after, err := tr.Get(ctx, number)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != "open" {
		t.Errorf("state = %q, want open", after.State)
	}
	if len(after.Comments) == 0 || !strings.Contains(after.Comments[len(after.Comments)-1].Body, "regressed in v1.2") {
		t.Errorf("comments = %+v, want the reopen reason", after.Comments)
	}

	if _, err := tr.Edit(ctx, tracker.EditRequest{Issue: number, NoParent: true}); err != nil {
		t.Fatalf("Edit (remove parent): %v", err)
	}
	detached, err := tr.Get(ctx, number)
	if err != nil {
		t.Fatal(err)
	}
	if detached.Parent != nil {
		t.Errorf("parent = %v, want it removed", detached.Parent)
	}
}
