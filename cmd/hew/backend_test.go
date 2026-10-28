package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	ucli "github.com/urfave/cli/v3"

	appcli "github.com/lumberbarons/hew/internal/cli"
	"github.com/lumberbarons/hew/internal/local"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// localRepo is a main checkout holding a local tracker with one ready issue,
// plus a linked worktree of it. The tracker lives only at the main checkout.
func localRepo(t *testing.T) (main, worktree string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	// Pin the identity so the claim assertions don't depend on the machine.
	t.Setenv("HEW_USER", "tester")
	main, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, main, "init", "--initial-branch=main")
	runGit(t, main, "-c", "user.email=t@example.com", "-c", "user.name=t", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "seed")
	worktree = filepath.Join(t.TempDir(), "wt")
	runGit(t, main, "worktree", "add", "-b", "feat/wt", worktree)

	tracker := filepath.Join(main, local.DirName)
	if err := local.Init(tracker); err != nil {
		t.Fatal(err)
	}
	store, err := local.Open(tracker, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateIssue(context.Background(), "Shared across worktrees", "", []string{"P1", "task"}); err != nil {
		t.Fatal(err)
	}
	return main, worktree
}

// capture runs hew with args and returns what it wrote to stdout. buildApp
// binds the real os.Stdout, so the test swaps it for a file.
func capture(t *testing.T, args ...string) (string, error) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = f
	defer func() { os.Stdout = orig }()
	runErr := root().Run(context.Background(), append([]string{"hew"}, args...))
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(raw), runErr
}

// The point of putting the tracker at the main checkout: an agent in a linked
// worktree sees, and writes to, the same issues as every other worktree.
func TestLocalTrackerIsSharedWithLinkedWorktrees(t *testing.T) {
	main, wt := localRepo(t)
	t.Chdir(wt)

	out, err := capture(t, "ready")
	if err != nil {
		t.Fatalf("ready: %v", err)
	}
	if !strings.Contains(out, "#1 P1 task") || !strings.Contains(out, "Shared across worktrees") {
		t.Errorf("ready from the worktree did not read the main checkout's tracker:\n%s", out)
	}

	if _, err := capture(t, "create", "--type", "task", "--title", "Filed from the worktree", "--goal", "g", "--done-when", "d"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, local.DirName)); !os.IsNotExist(err) {
		t.Errorf("a tracker appeared inside the worktree: %v", err)
	}
	t.Chdir(main)
	out, err = capture(t, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "Filed from the worktree") {
		t.Errorf("the main checkout does not see the worktree's issue:\n%s", out)
	}
}

func TestLocalPrimeUsesTheLocalPrimer(t *testing.T) {
	_, wt := localRepo(t)
	t.Chdir(wt)
	out, err := capture(t, "prime")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# hew primer — local/", "hew close <n> --completed", "Shared across worktrees"} {
		if !strings.Contains(out, want) {
			t.Errorf("prime missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "pr [--for") {
		t.Errorf("local prime documents pr:\n%s", out)
	}
}

func TestLocalBackendHasNoPullRequests(t *testing.T) {
	_, wt := localRepo(t)
	t.Chdir(wt)
	_, err := capture(t, "pr")
	if err == nil || !strings.Contains(err.Error(), "tracks issues locally") {
		t.Errorf("pr err = %v, want the local-backend refusal", err)
	}
}

// --repo is the explicit way back to GitHub: it must win over a local tracker.
func TestRepoFlagForcesGitHubOverALocalTracker(t *testing.T) {
	_, wt := localRepo(t)
	t.Chdir(wt)
	t.Setenv("GH_TOKEN", "test-token")

	for name, args := range map[string][]string{
		"before subcommand": {"hew", "--repo", "octo/hello", "list"},
		"after subcommand":  {"hew", "list", "--repo", "octo/hello"},
	} {
		t.Run(name, func(t *testing.T) {
			app := root()
			var got *appcli.App
			findCommand(t, app, "list").Action = func(_ context.Context, cmd *ucli.Command) error {
				var err error
				got, err = buildApp(cmd)
				return err
			}
			if err := app.Run(context.Background(), args); err != nil {
				t.Fatal(err)
			}
			if got.Local || got.PRs == nil || got.Repo.String() != "octo/hello" {
				t.Errorf("app = {Local: %v, PRs set: %v, Repo: %s}, want the GitHub backend for octo/hello", got.Local, got.PRs != nil, got.Repo)
			}
		})
	}
}

func TestMigratedTrackerFallsBackToGitHub(t *testing.T) {
	main, wt := localRepo(t)
	if err := local.WriteConfig(filepath.Join(main, local.DirName), local.Config{Backend: local.BackendGitHub}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(wt)
	t.Setenv("GH_TOKEN", "test-token")
	t.Setenv("GH_REPO", "")

	app := root()
	var got *appcli.App
	findCommand(t, app, "list").Action = func(_ context.Context, cmd *ucli.Command) error {
		var err error
		got, err = buildApp(cmd)
		return err
	}
	// The temp repo has no GitHub remote, so detection fails the way it does
	// for any non-GitHub checkout — proof the local store was not opened.
	err := app.Run(context.Background(), []string{"hew", "list"})
	if err == nil || !strings.Contains(err.Error(), "cannot detect repository") {
		t.Errorf("err = %v (app %+v), want GitHub repo detection to run", err, got)
	}
}

func TestInitLocalFromALinkedWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	main, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, main, "init", "--initial-branch=main")
	runGit(t, main, "-c", "user.email=t@example.com", "-c", "user.name=t", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "seed")
	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, main, "worktree", "add", "-b", "feat/wt", wt)
	t.Chdir(wt)

	out, err := capture(t, "init", "--local", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Backend, Path string }
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	if want := filepath.Join(main, local.DirName); got.Backend != "local" || got.Path != want {
		t.Errorf("init --local = %+v, want a local tracker at %s", got, want)
	}
	// And the next command in that worktree now runs against it.
	if out, err := capture(t, "ready"); err != nil || !strings.Contains(out, "no ready work") {
		t.Errorf("ready after init --local: out = %q, err = %v", out, err)
	}
}

func TestInitLocalRejectsRepo(t *testing.T) {
	err := root().Run(context.Background(), []string{"hew", "init", "--local", "--repo", "octo/hello"})
	if appcli.ExitCode(err) != appcli.ExitUsage {
		t.Errorf("exit = %d, err = %v; want a usage error", appcli.ExitCode(err), err)
	}
}
