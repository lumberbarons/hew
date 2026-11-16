// This file is done-when's lock-timeout and cancellation item: both are
// errors a consumer matches, reached through a real public call against a real
// local tracker.
package consumer_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/lumberbarons/hew/tracker"
)

// holdLock takes the tracker's exclusive lock the way another hew process
// would, and returns the release.
func holdLock(t *testing.T, root string) func() {
	t.Helper()
	path := filepath.Join(root, ".hew", "lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		t.Fatalf("flock %s: %v", path, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}
}

// TestExternalConsumerTimesOutOnALockedTracker waits out the documented lock
// timeout — the only way to reach it is to let the default bound expire — and
// matches the public error. It is deliberately slow; the bound is what makes
// reaching it mean the holder is hung rather than busy.
func TestExternalConsumerTimesOutOnALockedTracker(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the real lock timeout")
	}
	root, tr := externalTrackerCheckout(t)
	release := holdLock(t, root)
	defer release()

	_, err := tr.Create(context.Background(), tracker.CreateRequest{Title: "Waited", Type: "task"})
	var timeout *tracker.LockTimeoutError
	if !errors.As(err, &timeout) {
		t.Fatalf("err = %T (%v), want *tracker.LockTimeoutError", err, err)
	}
	if timeout.Path != filepath.Join(root, ".hew", "lock") || timeout.Timeout <= 0 {
		t.Errorf("LockTimeoutError = %+v, want the lock path and the bound", timeout)
	}
}

// TestExternalConsumerCancelsALockWait is the cancellation half: a caller's
// context ends the wait immediately, and the standard error stays matchable
// through the tracker's wrapping.
func TestExternalConsumerCancelsALockWait(t *testing.T) {
	root, tr := externalTrackerCheckout(t)
	release := holdLock(t, root)
	defer release()

	t.Run("deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_, err := tr.Create(ctx, tracker.CreateRequest{Title: "Late", Type: "task"})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %T (%v), want context.DeadlineExceeded matchable", err, err)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := tr.Create(ctx, tracker.CreateRequest{Title: "Cancelled", Type: "task"})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %T (%v), want context.Canceled matchable", err, err)
		}
	})
}
