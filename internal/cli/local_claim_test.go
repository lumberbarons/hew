package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/local"
	"github.com/lumberbarons/hew/internal/model"
)

// newLocalApp wires an App to a local store, the backend a local-only repo
// uses. PRs stays nil because the local store has no pull requests.
func newLocalApp(store gh.IssueStore) (*App, *bytes.Buffer, *bytes.Buffer) {
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	return &App{
		Client: store,
		Repo:   gh.Repo{Owner: "local", Name: "local"},
		Out:    out,
		ErrOut: errOut,
	}, out, errOut
}

// Two agents under one OS account, in separate worktrees: the first claims,
// and the second must be told to pick something else (exit 3), not to resume
// what it never claimed (exit 5).
func TestStartTellsWorktreesApartThroughTheLocalStore(t *testing.T) {
	main, first := gitRepoWithWorktree(t)
	second := filepath.Join(t.TempDir(), "agent-two")
	gitIn(t, main, "worktree", "add", "-b", "feat/two", second)

	dir := filepath.Join(main, local.DirName)
	if err := local.Init(dir); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HEW_USER", "")
	t.Setenv("USER", "jim")
	t.Chdir(first)
	agentOne, err := local.Open(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(second)
	agentTwo, err := local.Open(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(main)

	created, err := agentOne.CreateIssue(ctx, "Contested work", "", []string{"P1", "task"})
	if err != nil {
		t.Fatal(err)
	}
	appOne, _, _ := newLocalApp(agentOne)
	if err := appOne.Start(ctx, created.Number, StartOpts{}); err != nil {
		t.Fatalf("first start: %v", err)
	}

	appTwo, _, _ := newLocalApp(agentTwo)
	err = appTwo.Start(ctx, created.Number, StartOpts{})
	exitCode(t, err, ExitClaimed)
	if !strings.Contains(err.Error(), "already claimed") {
		t.Errorf("message = %q, want a claim refusal", err)
	}

	// --force steals atomically, and leaves exactly the stealer's claim.
	if err := appTwo.Start(ctx, created.Number, StartOpts{Force: true}); err != nil {
		t.Fatalf("forced start: %v", err)
	}
	got, err := agentTwo.GetIssue(ctx, created.Number)
	if err != nil {
		t.Fatal(err)
	}
	stealer, err := agentTwo.Viewer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Assignees) != 1 || got.Assignees[0] != stealer || !got.InProgress() {
		t.Errorf("after force: assignees %v labels %v, want only the stealer's claim %q", got.Assignees, got.Labels, stealer)
	}
}

// racingStore wins the claim as rival just before delegating, standing in for
// another agent whose claim lands after Start's read — the window an atomic
// claim exists to close.
type racingStore struct {
	*local.Store
	rival string
}

func (r *racingStore) ClaimIssue(ctx context.Context, number int, login string, force bool) error {
	_ = r.Store.ClaimIssue(ctx, number, r.rival, false)
	return r.Store.ClaimIssue(ctx, number, login, force)
}

func TestStartMapsALostClaimRaceToTheClaimCodes(t *testing.T) {
	cases := []struct {
		name  string
		login string
		rival string
		code  int
		want  string
	}{
		{"another agent won", "agent-b", "agent-a", ExitClaimed, "already claimed"},
		{"a sibling session with the same identity won", "agent-a", "agent-a", ExitClaimedByYou, "already claimed by you"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), local.DirName)
			if err := local.Init(dir); err != nil {
				t.Fatal(err)
			}
			store, err := local.Open(dir, tc.login)
			if err != nil {
				t.Fatal(err)
			}
			created, err := store.CreateIssue(ctx, "Contested work", "", []string{"P1", "task"})
			if err != nil {
				t.Fatal(err)
			}

			app, _, _ := newLocalApp(&racingStore{Store: store, rival: tc.rival})
			err = app.Start(ctx, created.Number, StartOpts{})
			exitCode(t, err, tc.code)
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("message = %q, want it to contain %q", err, tc.want)
			}
			got, err := store.GetIssue(ctx, created.Number)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Assignees) != 1 || got.Assignees[0] != tc.rival {
				t.Errorf("assignees = %v, want only the race winner %q", got.Assignees, tc.rival)
			}
		})
	}
}

// swapFailingStore fails the priority label write that follows a successful
// claim, so a test can see what start leaves behind.
type swapFailingStore struct {
	*local.Store
	failRemove bool
}

func (s *swapFailingStore) AddLabels(ctx context.Context, number int, labels []string) error {
	for _, l := range labels {
		if _, ok := model.ParsePriority(l); ok {
			return errors.New("disk full")
		}
	}
	return s.Store.AddLabels(ctx, number, labels)
}

func (s *swapFailingStore) RemoveAssignees(ctx context.Context, number int, logins []string) error {
	if s.failRemove {
		return errors.New("lock timeout")
	}
	return s.Store.RemoveAssignees(ctx, number, logins)
}

// Claiming is triage — an untriaged issue gets its priority from start — so a failed priority swap must not leave the issue
// claimed: the retry would be told the claim is already its own (exit 5)
// instead of finishing the triage.
func TestStartReleasesTheClaimWhenThePrioritySwapFails(t *testing.T) {
	for _, tc := range []struct {
		name      string
		prior     []string // earlier claimants, only reachable with --force
		force     bool
		wantAfter []string
	}{
		{"fresh claim", nil, false, nil},
		{"forced claim restores the displaced claimant", []string{"agent-a"}, true, []string{"agent-a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), local.DirName)
			if err := local.Init(dir); err != nil {
				t.Fatal(err)
			}
			store, err := local.Open(dir, "agent-b")
			if err != nil {
				t.Fatal(err)
			}
			created, err := store.CreateIssue(ctx, "Triage on claim", "", []string{"task"})
			if err != nil {
				t.Fatal(err)
			}
			for _, who := range tc.prior {
				if err := store.ClaimIssue(ctx, created.Number, who, false); err != nil {
					t.Fatal(err)
				}
			}

			app, _, _ := newLocalApp(&swapFailingStore{Store: store})
			err = app.Start(ctx, created.Number, StartOpts{Priority: "P1", Force: tc.force})
			if err == nil || !strings.Contains(err.Error(), "disk full") {
				t.Fatalf("err = %v, want the swap failure", err)
			}
			got, err := store.GetIssue(ctx, created.Number)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got.Assignees, tc.wantAfter) {
				t.Errorf("assignees = %v, want %v", got.Assignees, tc.wantAfter)
			}
			if got.InProgress() != (len(tc.prior) > 0) {
				t.Errorf("in-progress = %v with prior claimants %v", got.InProgress(), tc.prior)
			}
			if p, _ := got.Priority(); p != model.PriorityUnknown {
				t.Errorf("priority = %v, want the issue still untriaged", p)
			}
		})
	}
}

func TestStartReportsAFailedRollbackAlongsideTheSwapFailure(t *testing.T) {
	dir := filepath.Join(t.TempDir(), local.DirName)
	if err := local.Init(dir); err != nil {
		t.Fatal(err)
	}
	store, err := local.Open(dir, "agent-b")
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateIssue(ctx, "Triage on claim", "", []string{"task"})
	if err != nil {
		t.Fatal(err)
	}
	app, _, _ := newLocalApp(&swapFailingStore{Store: store, failRemove: true})
	err = app.Start(ctx, created.Number, StartOpts{Priority: "P1"})
	if err == nil || !strings.Contains(err.Error(), "disk full") || !strings.Contains(err.Error(), "could not release the claim") {
		t.Errorf("err = %v, want both the swap failure and the failed release", err)
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// A removed worktree leaves an unreachable identity, but its claim remains in
// the shared tracker until explicitly released from a surviving checkout.
func TestReleaseRecoversClaimAfterWorktreeRemove(t *testing.T) {
	main, worktree := gitRepoWithWorktree(t)
	dir := filepath.Join(main, local.DirName)
	if err := local.Init(dir); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HEW_USER", "")
	t.Setenv("USER", "jim")
	t.Chdir(worktree)
	owner, err := local.Open(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	created, err := owner.CreateIssue(ctx, "Stranded work", "", []string{"P2", "task"})
	if err != nil {
		t.Fatal(err)
	}
	ownerApp, _, _ := newLocalApp(owner)
	if err := ownerApp.Start(ctx, created.Number, StartOpts{}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(main)
	gitIn(t, main, "worktree", "remove", worktree)
	survivor, err := local.Open(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	app, out, _ := newLocalApp(survivor)
	exitCode(t, app.Start(ctx, created.Number, StartOpts{}), ExitClaimed)
	if err := app.Release(ctx, created.Number, false); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("unforced release: %v", err)
	}
	if err := app.Release(ctx, created.Number, true); err != nil {
		t.Fatal(err)
	}
	got, err := survivor.GetIssue(ctx, created.Number)
	if err != nil {
		t.Fatal(err)
	}
	if got.Claimed() {
		t.Fatalf("still claimed: %+v", got)
	}
	out.Reset()
	if err := app.Ready(ctx, ReadyOpts{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Stranded work") {
		t.Fatalf("not ready: %s", out)
	}
	if err := app.Start(ctx, created.Number, StartOpts{}); err != nil {
		t.Fatalf("fresh claim: %v", err)
	}
}

type releaseRacingStore struct {
	*local.Store
	clear bool
}

func (s *releaseRacingStore) ReleaseIssueClaim(ctx context.Context, number int, login string, force bool) error {
	if s.clear {
		if err := s.Store.ReleaseIssueClaim(ctx, number, login, true); err != nil {
			return err
		}
	} else if err := s.ClaimIssue(ctx, number, "rival", true); err != nil {
		return err
	}
	return s.Store.ReleaseIssueClaim(ctx, number, login, force)
}

func TestReleaseRechecksLocalClaimUnderLock(t *testing.T) {
	for _, clear := range []bool{false, true} {
		t.Run(fmt.Sprintf("already released=%t", clear), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), local.DirName)
			if err := local.Init(dir); err != nil {
				t.Fatal(err)
			}
			store, err := local.Open(dir, "me")
			if err != nil {
				t.Fatal(err)
			}
			created, err := store.CreateIssue(ctx, "Work", "", []string{"P2", "task"})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.ClaimIssue(ctx, created.Number, "me", false); err != nil {
				t.Fatal(err)
			}
			app, out, _ := newLocalApp(&releaseRacingStore{Store: store, clear: clear})
			err = app.Release(ctx, created.Number, false)
			if err == nil || out.Len() != 0 {
				t.Fatalf("error = %v, output = %s", err, out)
			}
			got, err := store.GetIssue(ctx, created.Number)
			if err != nil {
				t.Fatal(err)
			}
			if clear {
				if got.Claimed() {
					t.Fatalf("claim recreated: %+v", got)
				}
			} else if len(got.Assignees) != 1 || got.Assignees[0] != "rival" || !got.InProgress() {
				t.Fatalf("concurrent claim disturbed: %+v", got)
			}
		})
	}
}
