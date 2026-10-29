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

// gitCheckoutAt makes a plain checkout at parent/name, with no tracker. The
// caller picks the name, which is what lets two unrelated repositories look
// alike to a path-derived identity.
func gitCheckoutAt(t *testing.T, parent, name string) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "init", "--initial-branch=main")
	runGit(t, dir, "-c", "user.email=t@example.com", "-c", "user.name=t", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "seed")
	return dir
}

// localIdentity is what a command run from dir binds its checkpoints to.
func localIdentity(t *testing.T, dir string) string {
	t.Helper()
	t.Chdir(dir)
	app := root()
	identity := ""
	findCommand(t, app, "list").Action = func(_ context.Context, cmd *ucli.Command) error {
		built, err := buildApp(cmd)
		if err != nil {
			return err
		}
		if !built.Local {
			t.Errorf("running in %s selected the GitHub backend", dir)
		}
		identity = built.RepoKey
		return nil
	}
	if err := app.Run(context.Background(), []string{"hew", "list"}); err != nil {
		t.Fatal(err)
	}
	return identity
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

// migrate local reads the tracker at the main checkout but always writes to
// GitHub, whatever the checkout's config says. A checkpoint bound to another
// repository is refused before GitHub is touched, and the refusal names the
// repository the command resolved and the state file it found, which shows both
// the target (--repo, not the local store) and the tracker directory (the main
// checkout's .hew, not the worktree's).
func TestMigrateLocalTargetsGitHubFromTheMainCheckoutsTracker(t *testing.T) {
	main, wt := localRepo(t)
	t.Chdir(wt)
	t.Setenv("GH_TOKEN", "test-token")
	state := filepath.Join(main, local.DirName, "migration-state.json")
	foreign := `{"version":1,"repo":"someone/else","digest":"sha256:x","mapping":{"1":7}}`
	if err := os.WriteFile(state, []byte(foreign), 0o644); err != nil {
		t.Fatal(err)
	}

	for name, args := range map[string][]string{
		"before subcommand": {"--repo", "octo/hello", "migrate", "local", "--dry-run"},
		"after subcommand":  {"migrate", "local", "--dry-run", "--repo", "octo/hello"},
		// Without --repo a local tracker would select the local store for any
		// other command; migrate local must still resolve GitHub's repository.
		"repository from the environment": {"migrate", "local", "--dry-run"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("GH_REPO", "octo/hello")
			_, err := capture(t, args...)
			if err == nil {
				t.Fatal("a checkpoint for another repository was accepted")
			}
			for _, want := range []string{state, `belongs to repository "someone/else"`, `not "octo/hello"`} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %v, want it to mention %q", err, want)
				}
			}
		})
	}
	if _, err := os.Stat(filepath.Join(wt, local.DirName)); !os.IsNotExist(err) {
		t.Errorf("a tracker appeared inside the worktree: %v", err)
	}
}

// --state redirects the checkpoint, and --dry-run reaches the migration: a
// dry run against an empty checkpoint prints its plan and changes nothing.
func TestMigrateLocalDryRunHonoursStateAndLeavesTheTrackerLocal(t *testing.T) {
	main, wt := localRepo(t)
	t.Chdir(wt)
	t.Setenv("GH_TOKEN", "test-token")
	state := filepath.Join(t.TempDir(), "elsewhere.json")

	out, err := capture(t, "migrate", "local", "--dry-run", "--state", state, "--repo", "octo/hello")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "create: 1 as P1 task") || !strings.Contains(out, "dry run: 1 local issues would be migrated") {
		t.Errorf("plan = %q", out)
	}
	if cfg, err := local.ReadConfig(filepath.Join(main, local.DirName)); err != nil || cfg.Backend != local.BackendLocal {
		t.Errorf("config = %+v, %v; a dry run must leave the tracker local", cfg, err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Errorf("a dry run wrote the checkpoint: %v", err)
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

// A checkpoint has to tell one local tracker from another. The directory name
// it used to use cannot: two unrelated checkouts called solar-controller are
// ordinary, and a rename is not supposed to invalidate a resume.
func TestLocalCheckpointIdentityIsUniqueAndSurvivesARename(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	one := gitCheckoutAt(t, t.TempDir(), "solar-controller")
	two := gitCheckoutAt(t, t.TempDir(), "solar-controller")
	for _, dir := range []string{one, two} {
		if err := local.Init(filepath.Join(dir, local.DirName)); err != nil {
			t.Fatal(err)
		}
	}

	oneID, twoID := localIdentity(t, one), localIdentity(t, two)
	if oneID == "" || twoID == "" {
		t.Fatalf("checkpoint identities = %q and %q; want one per tracker", oneID, twoID)
	}
	if oneID == twoID {
		t.Errorf("two checkouts with the same directory name share the identity %q", oneID)
	}

	renamed := filepath.Join(filepath.Dir(one), "solar-controller-renamed")
	if err := os.Rename(one, renamed); err != nil {
		t.Fatal(err)
	}
	if got := localIdentity(t, renamed); got != oneID {
		t.Errorf("identity after rename = %q, want %q", got, oneID)
	}
}

// Backend selection runs before every command, and a checkout that never
// opted in has no .hew for it to find. Deciding that must not cost a git
// subprocess: the GitHub path's own repository detection is the only one the
// command needs, and GH_REPO makes even that free.
func TestGitHubOnlyRepoSkipsGitForBackendSelection(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	main := gitCheckoutAt(t, t.TempDir(), "github-only")

	bin := t.TempDir()
	shim := "#!/bin/sh\necho \"unexpected git invocation: $*\" >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GH_REPO", "octo/hello")
	t.Setenv("GH_TOKEN", "test-token")
	t.Chdir(main)

	app := root()
	findCommand(t, app, "list").Action = func(_ context.Context, cmd *ucli.Command) error {
		built, err := buildApp(cmd)
		if err != nil {
			return err
		}
		if built.Local {
			t.Error("a repo with no .hew was read as local")
		}
		return nil
	}
	if err := app.Run(context.Background(), []string{"hew", "list"}); err != nil {
		t.Fatal(err)
	}
}
