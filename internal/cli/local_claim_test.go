package cli

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/local"
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
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	main, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gitIn(t, main, "init", "--initial-branch=main")
	gitIn(t, main, "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "--allow-empty", "-m", "seed")
	first := filepath.Join(t.TempDir(), "agent-one")
	second := filepath.Join(t.TempDir(), "agent-two")
	gitIn(t, main, "worktree", "add", "-b", "feat/one", first)
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
	if len(got.Assignees) != 1 || got.Assignees[0] != "jim-agent-two" || !got.InProgress() {
		t.Errorf("after force: assignees %v labels %v, want only the stealer's claim", got.Assignees, got.Labels)
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

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}
