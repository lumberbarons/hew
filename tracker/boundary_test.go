package tracker

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/local"
	"github.com/lumberbarons/hew/internal/model"
)

// injection makes one store method fail. At is the 1-based call number to fail
// on, so a multi-edge write can fail after earlier edges landed; 0 fails every
// call.
type injection struct {
	err error
	at  int
}

// memoryStore is a minimal in-memory gh.IssueStore with per-method failure
// injection. It implements only the base interface, so Start and Release take
// their check-then-act paths; atomicMemoryStore adds the optional capabilities.
type memoryStore struct {
	gh.IssueStore
	issues []model.Issue
	fail   map[string]injection
	calls  map[string]int
}

func newMemoryStore(issues ...model.Issue) *memoryStore {
	return &memoryStore{issues: issues, calls: map[string]int{}}
}

// inject stores a failure for method: every call, or the entry-th one.
func (s *memoryStore) inject(method string, err error, at int) *memoryStore {
	if s.fail == nil {
		s.fail = map[string]injection{}
	}
	s.fail[method] = injection{err: err, at: at}
	return s
}

func (s *memoryStore) take(method string) error {
	s.calls[method]++
	in, ok := s.fail[method]
	if !ok || (in.at != 0 && in.at != s.calls[method]) {
		return nil
	}
	return in.err
}

func (s *memoryStore) Viewer(context.Context) (string, error) {
	if err := s.take("Viewer"); err != nil {
		return "", err
	}
	return "actor-a", nil
}

func (s *memoryStore) ListIssues(_ context.Context, states []gh.IssueState) ([]model.Issue, error) {
	if err := s.take("ListIssues"); err != nil {
		return nil, err
	}
	var out []model.Issue
	for _, i := range s.issues {
		if slices.Contains(states, gh.IssueState(i.State)) {
			out = append(out, i)
		}
	}
	return out, nil
}

func (s *memoryStore) GetIssue(_ context.Context, number int) (model.Issue, error) {
	if err := s.take("GetIssue"); err != nil {
		return model.Issue{}, err
	}
	for _, i := range s.issues {
		if i.Number == number {
			return i, nil
		}
	}
	return model.Issue{}, &gh.NotFoundError{Issue: number}
}

func (s *memoryStore) AddLabels(_ context.Context, _ int, _ []string) error {
	return s.take("AddLabels")
}

func (s *memoryStore) RemoveLabel(_ context.Context, _ int, _ string) error {
	return s.take("RemoveLabel")
}

func (s *memoryStore) AddAssignee(_ context.Context, _ int, _ string) error {
	return s.take("AddAssignee")
}

func (s *memoryStore) RemoveAssignees(_ context.Context, _ int, _ []string) error {
	return s.take("RemoveAssignees")
}

func (s *memoryStore) Comment(_ context.Context, _ int, _ string) error {
	return s.take("Comment")
}

func (s *memoryStore) CloseIssue(_ context.Context, _ int, _ gh.CloseReason) error {
	return s.take("CloseIssue")
}

func (s *memoryStore) AddBlockedBy(_ context.Context, _, _ int) error {
	return s.take("AddBlockedBy")
}

func (s *memoryStore) RemoveBlockedBy(_ context.Context, _, _ int) error {
	return s.take("RemoveBlockedBy")
}

// atomicMemoryStore adds the optional write capabilities, so Start and Release
// take the atomic paths over the same injections.
type atomicMemoryStore struct{ *memoryStore }

func (s *atomicMemoryStore) ClaimIssue(context.Context, int, string, bool) error {
	return s.take("ClaimIssue")
}

func (s *atomicMemoryStore) ReleaseIssueClaim(context.Context, int, string, bool) error {
	return s.take("ReleaseIssueClaim")
}

// TestAuthFailuresAtEveryMutationStageArePublicAuthErrors is the mutation-side
// half of the error contract: whichever stage the backend fails at — the read
// before, the write itself, the read after, or the identity lookup — the
// caller matches the public AuthError, and the backend cause stays reachable.
func TestAuthFailuresAtEveryMutationStageArePublicAuthErrors(t *testing.T) {
	cause := errors.New("token expired")
	authErr := &gh.AuthError{Err: cause}
	ctx := context.Background()

	blocker := fixture(2, "Blocker", "P2", "bug")
	second := fixture(3, "Second blocker", "P2", "bug")
	withEdges := fixture(4, "With edges", "P2", "bug")
	withEdges.BlockedBy = []model.Ref{{Number: 2, State: "OPEN"}, {Number: 3, State: "OPEN"}}

	for _, tc := range []struct {
		name   string
		store  gh.IssueStore
		noRead bool // WrapAs with no actor: the operation has to resolve one
		run    func(context.Context, *Tracker, *Writer) error
	}{
		{
			name:  "start pre-read",
			store: newMemoryStore(fixture(1, "Work", "P2", "task")).inject("GetIssue", authErr, 0),
			run: func(ctx context.Context, tr *Tracker, _ *Writer) error {
				_, err := tr.Start(ctx, 1, StartOptions{})
				return err
			},
		},
		{
			name:  "start post-read",
			store: newMemoryStore(fixture(1, "Work", "P2", "task")).inject("GetIssue", authErr, 2),
			run: func(ctx context.Context, tr *Tracker, _ *Writer) error {
				_, err := tr.Start(ctx, 1, StartOptions{})
				return err
			},
		},
		{
			name:   "start identity lookup",
			store:  newMemoryStore(fixture(1, "Work", "P2", "task")).inject("Viewer", authErr, 0),
			noRead: true,
			run: func(ctx context.Context, tr *Tracker, _ *Writer) error {
				_, err := tr.Start(ctx, 1, StartOptions{})
				return err
			},
		},
		{
			name:  "start claim write",
			store: newMemoryStore(fixture(1, "Work", "P2", "task")).inject("AddLabels", authErr, 0),
			run: func(ctx context.Context, tr *Tracker, _ *Writer) error {
				_, err := tr.Start(ctx, 1, StartOptions{})
				return err
			},
		},
		{
			name:  "start assignee write",
			store: newMemoryStore(fixture(1, "Work", "P2", "task")).inject("AddAssignee", authErr, 0),
			run: func(ctx context.Context, tr *Tracker, _ *Writer) error {
				_, err := tr.Start(ctx, 1, StartOptions{})
				return err
			},
		},
		{
			name:  "start atomic claim",
			store: &atomicMemoryStore{newMemoryStore(fixture(1, "Work", "P2", "task")).inject("ClaimIssue", authErr, 0)},
			run: func(ctx context.Context, tr *Tracker, _ *Writer) error {
				_, err := tr.Start(ctx, 1, StartOptions{})
				return err
			},
		},
		{
			name:  "start priority swap after an atomic claim",
			store: &atomicMemoryStore{newMemoryStore(fixture(1, "Work", "P1", "task")).inject("RemoveLabel", authErr, 0)},
			run: func(ctx context.Context, tr *Tracker, _ *Writer) error {
				_, err := tr.Start(ctx, 1, StartOptions{Priority: "P2"})
				return err
			},
		},
		{
			name:   "start claim refinement lookup",
			store:  newMemoryStore(claimedBy(1, "actor-b")).inject("Viewer", authErr, 0),
			noRead: true,
			run: func(ctx context.Context, tr *Tracker, _ *Writer) error {
				_, err := tr.Start(ctx, 1, StartOptions{})
				return err
			},
		},
		{
			name:  "release pre-read",
			store: newMemoryStore(claimedBy(1, "actor-a")).inject("GetIssue", authErr, 0),
			run: func(ctx context.Context, tr *Tracker, _ *Writer) error {
				return tr.Release(ctx, 1, ReleaseOptions{})
			},
		},
		{
			name:   "release identity lookup",
			store:  newMemoryStore(claimedBy(1, "actor-a")).inject("Viewer", authErr, 0),
			noRead: true,
			run: func(ctx context.Context, tr *Tracker, _ *Writer) error {
				return tr.Release(ctx, 1, ReleaseOptions{})
			},
		},
		{
			name:  "release claim write",
			store: newMemoryStore(claimedBy(1, "actor-a")).inject("RemoveLabel", authErr, 0),
			run: func(ctx context.Context, tr *Tracker, _ *Writer) error {
				return tr.Release(ctx, 1, ReleaseOptions{})
			},
		},
		{
			name:  "release atomic claim write",
			store: &atomicMemoryStore{newMemoryStore(claimedBy(1, "actor-a")).inject("ReleaseIssueClaim", authErr, 0)},
			run: func(ctx context.Context, tr *Tracker, _ *Writer) error {
				return tr.Release(ctx, 1, ReleaseOptions{})
			},
		},
		{
			name:  "close pre-read",
			store: newMemoryStore(fixture(1, "Work", "P2", "bug")).inject("GetIssue", authErr, 0),
			run: func(ctx context.Context, tr *Tracker, _ *Writer) error {
				_, err := tr.Close(ctx, 1, CloseOptions{Reason: "done"})
				return err
			},
		},
		{
			name:  "close reason comment",
			store: newMemoryStore(fixture(1, "Work", "P2", "bug")).inject("Comment", authErr, 0),
			run: func(ctx context.Context, tr *Tracker, _ *Writer) error {
				_, err := tr.Close(ctx, 1, CloseOptions{Reason: "done"})
				return err
			},
		},
		{
			name:  "close state change",
			store: newMemoryStore(fixture(1, "Work", "P2", "bug")).inject("CloseIssue", authErr, 0),
			run: func(ctx context.Context, tr *Tracker, _ *Writer) error {
				_, err := tr.Close(ctx, 1, CloseOptions{Reason: "done"})
				return err
			},
		},
		{
			name:  "block open-issue list",
			store: newMemoryStore(fixture(1, "Work", "P2", "bug"), blocker).inject("ListIssues", authErr, 0),
			run: func(ctx context.Context, _ *Tracker, w *Writer) error {
				_, err := w.Block(ctx, 1, []int{2}, BlockOptions{})
				return err
			},
		},
		{
			name:  "block target re-read",
			store: newMemoryStore(blocker).inject("GetIssue", authErr, 0),
			run: func(ctx context.Context, _ *Tracker, w *Writer) error {
				_, err := w.Block(ctx, 99, []int{2}, BlockOptions{})
				return err
			},
		},
		{
			name:  "block edge write",
			store: newMemoryStore(fixture(1, "Work", "P2", "bug"), blocker).inject("AddBlockedBy", authErr, 0),
			run: func(ctx context.Context, _ *Tracker, w *Writer) error {
				_, err := w.Block(ctx, 1, []int{2}, BlockOptions{})
				return err
			},
		},
		{
			name:  "block edge write after an earlier edge landed",
			store: newMemoryStore(fixture(1, "Work", "P2", "bug"), blocker, second).inject("AddBlockedBy", authErr, 2),
			run: func(ctx context.Context, _ *Tracker, w *Writer) error {
				_, err := w.Block(ctx, 1, []int{2, 3}, BlockOptions{})
				return err
			},
		},
		{
			name:  "unblock pre-read",
			store: newMemoryStore(withEdges).inject("GetIssue", authErr, 0),
			run: func(ctx context.Context, _ *Tracker, w *Writer) error {
				_, err := w.Unblock(ctx, 4, []int{2}, BlockOptions{})
				return err
			},
		},
		{
			name:  "unblock edge write",
			store: newMemoryStore(withEdges).inject("RemoveBlockedBy", authErr, 0),
			run: func(ctx context.Context, _ *Tracker, w *Writer) error {
				_, err := w.Unblock(ctx, 4, []int{2}, BlockOptions{})
				return err
			},
		},
		{
			name:  "unblock edge write after an earlier edge was removed",
			store: newMemoryStore(withEdges).inject("RemoveBlockedBy", authErr, 2),
			run: func(ctx context.Context, _ *Tracker, w *Writer) error {
				_, err := w.Unblock(ctx, 4, []int{2, 3}, BlockOptions{})
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actor := "actor-a"
			if tc.noRead {
				actor = ""
			}
			tr := WrapAs(tc.store, BackendGitHub, actor)
			err := tc.run(ctx, tr, WrapWriter(tc.store))
			if err == nil {
				t.Fatal("expected an auth failure")
			}
			var auth *AuthError
			if !errors.As(err, &auth) {
				t.Fatalf("err = %T (%v), want *tracker.AuthError", err, err)
			}
			if !errors.Is(err, cause) {
				t.Errorf("err = %v, want the backend cause retained for errors.Is", err)
			}
		})
	}
}

// TestBlockPropagatesANonNotFoundTargetReadFailure keeps absence and failure
// apart: a read that failed says so with its cause, rather than sending the
// caller hunting for a typo that does not exist.
func TestBlockPropagatesANonNotFoundTargetReadFailure(t *testing.T) {
	cause := errors.New("connection reset by peer")
	store := newMemoryStore(fixture(2, "Blocker", "P2", "bug")).
		inject("GetIssue", fmt.Errorf("reading #99: %w", cause), 0)
	_, err := WrapWriter(store).Block(context.Background(), 99, []int{2}, BlockOptions{})
	if !errors.Is(err, cause) {
		t.Fatalf("err = %v, want the read failure's cause reachable with errors.Is", err)
	}
	var notFound *NotFoundError
	if errors.As(err, &notFound) {
		t.Errorf("err = %v, reported absence for a read that failed", err)
	}
}

// A cancelled read is a cancellation, not a missing issue and not an opaque
// string: an orchestrator branches on whether to retry.
func TestBlockKeepsCancellationIdentities(t *testing.T) {
	for name, cause := range map[string]error{
		"cancelled": context.Canceled,
		"deadline":  context.DeadlineExceeded,
	} {
		t.Run(name, func(t *testing.T) {
			store := newMemoryStore(fixture(2, "Blocker", "P2", "bug")).
				inject("GetIssue", fmt.Errorf("reading #99: %w", cause), 0)
			_, err := WrapWriter(store).Block(context.Background(), 99, []int{2}, BlockOptions{})
			if !errors.Is(err, cause) {
				t.Fatalf("err = %v, want errors.Is(err, %s)", err, name)
			}
			var notFound *NotFoundError
			if errors.As(err, &notFound) {
				t.Errorf("err = %v, reported absence for a %s read", err, name)
			}
		})
	}
}

// TestLocalRefusalsReachThePublicTypes is the local backend's half: its
// refusals are structured, and the tracker boundary maps them rather than
// letting a formatted string through.
func TestLocalRefusalsReachThePublicTypes(t *testing.T) {
	t.Run("a write after migration is a MigratedError", func(t *testing.T) {
		ctx, store, dir, issue := localFixture(t)
		frozen, err := local.Freeze(ctx, dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := frozen.Seal("owner/repo", map[int]int{issue.Number: 1}); err != nil {
			t.Fatal(err)
		}
		frozen.Release()

		// The store was opened before the seal, which is exactly the race the
		// archive guard exists for.
		_, err = WrapAs(store, BackendLocal, "actor-a").Start(ctx, issue.Number, StartOptions{})
		var migrated *MigratedError
		if !errors.As(err, &migrated) {
			t.Fatalf("err = %T (%v), want *tracker.MigratedError", err, err)
		}
		if migrated.Path != dir || migrated.Repo != "owner/repo" {
			t.Errorf("MigratedError = %+v, want the tracker directory and the repository it moved to", migrated)
		}
		if !strings.Contains(err.Error(), "not locally") {
			t.Errorf("err = %v, want the store's own explanation", err)
		}
	})

	t.Run("a lock timeout is a LockTimeoutError", func(t *testing.T) {
		ctx, _, dir, issue := localFixture(t)
		frozen, err := local.Freeze(ctx, dir)
		if err != nil {
			t.Fatal(err)
		}
		defer frozen.Release()

		slow, err := local.OpenWithOptions(dir, local.Options{
			Viewer:      "actor-a",
			LockTimeout: 20 * time.Millisecond,
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = WrapAs(slow, BackendLocal, "actor-a").Start(ctx, issue.Number, StartOptions{})
		var timeout *LockTimeoutError
		if !errors.As(err, &timeout) {
			t.Fatalf("err = %T (%v), want *tracker.LockTimeoutError", err, err)
		}
		if timeout.Timeout != 20*time.Millisecond || timeout.Path == "" {
			t.Errorf("LockTimeoutError = %+v, want the wait and the lock path", timeout)
		}
		// The store's own explanation, naming the migration holding the lock,
		// is what a person can act on.
		if !strings.Contains(err.Error(), "migrate") {
			t.Errorf("err = %v, want the holder named", err)
		}
	})

	t.Run("a lock wait bounded by the caller's context keeps its identity", func(t *testing.T) {
		ctx, store, dir, issue := localFixture(t)
		frozen, err := local.Freeze(ctx, dir)
		if err != nil {
			t.Fatal(err)
		}
		defer frozen.Release()

		waitCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancel()
		_, err = WrapAs(store, BackendLocal, "actor-a").Start(waitCtx, issue.Number, StartOptions{})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want the caller's deadline, not the store's timeout", err)
		}
		var timeout *LockTimeoutError
		if errors.As(err, &timeout) {
			t.Errorf("err = %v, reported the store's lock timeout for a cancelled wait", err)
		}
	})
}

// TestReleaseOwnershipRaceIsDecidedUnderTheLock covers the window between
// Release's preflight read and the locked mutation: the lock is the authority,
// and its answer must arrive as the public type with the new owner's claim
// intact.
func TestReleaseOwnershipRaceIsDecidedUnderTheLock(t *testing.T) {
	for _, tc := range []struct {
		name      string
		current   string
		wantOwner bool
	}{
		{name: "another actor won the claim", current: "actor-b", wantOwner: true},
		{name: "the claim was released underneath us"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, store, _, issue := localFixture(t)
			if tc.current != "" {
				if err := store.ClaimIssue(ctx, issue.Number, tc.current, false); err != nil {
					t.Fatal(err)
				}
			}
			stale := claimedBy(issue.Number, "actor-a")
			tr := WrapAs(&staleRead{IssueStore: store, stale: stale}, BackendLocal, "actor-a")

			err := tr.Release(ctx, issue.Number, ReleaseOptions{})
			if tc.wantOwner {
				var notOwner *NotOwnerError
				if !errors.As(err, &notOwner) {
					t.Fatalf("err = %T (%v), want *tracker.NotOwnerError", err, err)
				}
				if notOwner.Actor != "actor-a" || !slices.Equal(notOwner.Assignees, []string{"actor-b"}) {
					t.Errorf("NotOwnerError = %+v, want the claimant the lock saw", notOwner)
				}
			} else {
				var notClaimed *NotClaimedError
				if !errors.As(err, &notClaimed) {
					t.Fatalf("err = %T (%v), want *tracker.NotClaimedError", err, err)
				}
			}

			after, readErr := store.GetIssue(ctx, issue.Number)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if tc.current != "" {
				if !slices.Equal(after.Assignees, []string{tc.current}) || !after.InProgress() {
					t.Errorf("after the refused release: assignees %v, in-progress %v; the new owner's claim must survive",
						after.Assignees, after.InProgress())
				}
			} else if after.Claimed() {
				t.Errorf("after the refused release: still claimed (%v)", after.Assignees)
			}
		})
	}
}

// staleRead serves one issue snapshot from before a concurrent change, then
// delegates: the read a check-then-act release makes, over a store that has
// since moved on. It forwards the atomic release so Release takes the locked
// path.
type staleRead struct {
	gh.IssueStore
	stale  model.Issue
	served bool
}

func (s *staleRead) GetIssue(ctx context.Context, number int) (model.Issue, error) {
	if !s.served {
		s.served = true
		return s.stale, nil
	}
	return s.IssueStore.GetIssue(ctx, number)
}

func (s *staleRead) ReleaseIssueClaim(ctx context.Context, number int, login string, force bool) error {
	return s.IssueStore.(gh.ClaimReleaser).ReleaseIssueClaim(ctx, number, login, force)
}

func claimedBy(number int, actor string) model.Issue {
	issue := fixture(number, "Work", "P2", "task", "in-progress")
	issue.Assignees = []string{actor}
	return issue
}

// localFixture is startFixture plus the tracker directory the local-store
// refusals name, so a test can freeze or seal the tracker it holds a store for.
func localFixture(t *testing.T) (context.Context, *local.Store, string, model.Issue) {
	t.Helper()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), local.DirName)
	if err := local.Init(dir); err != nil {
		t.Fatal(err)
	}
	store, err := local.Open(dir, "actor-a")
	if err != nil {
		t.Fatal(err)
	}
	issue, err := store.CreateIssue(ctx, "Work", "", []string{"P2", "task"})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, store, dir, issue
}
