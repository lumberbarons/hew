package tracker_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/tracker"
)

// This file is an external consumer's test: it imports only the public
// package, opens a tracker the way a program outside this module does, and
// matches what comes back with the public types. A test inside package
// tracker could satisfy the same assertions while an internal error still
// leaked, because it can name the internal type; this one cannot.

// externalTracker opens a local tracker in a throwaway checkout using the
// documented on-disk shape, so nothing here depends on an internal package.
func externalTracker(t *testing.T) *tracker.Tracker {
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
