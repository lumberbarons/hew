package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/internal/conventions"
	"github.com/lumberbarons/hew/internal/local"
)

// gitRepoWithWorktree makes a main checkout and one linked worktree, both
// resolved past macOS's temp-dir symlink so paths compare against git's output.
func gitRepoWithWorktree(t *testing.T) (main, worktree string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	// A developer's global config (signing, hooks, identity) must not reach
	// the throwaway repo.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	main, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gitIn(t, main, "init", "--initial-branch=main")
	gitIn(t, main, "-c", "user.email=t@example.com", "-c", "user.name=t", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "seed")
	worktree = filepath.Join(t.TempDir(), "wt")
	gitIn(t, main, "worktree", "add", "-b", "feat/wt", worktree)
	return main, worktree
}

func isolateGitConfig(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
}

func TestPrimeOnTheLocalBackendDropsThePullRequestStep(t *testing.T) {
	app, out, _ := newApp(newFake(issue(1, "Ready one", "P2", "bug")))
	app.Local = true
	if err := app.Prime(ctx, PrimeOpts{}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"Workflow: hew ready", "hew close <n> --completed", "## Ready (1 of 1 open)"} {
		if !strings.Contains(got, want) {
			t.Errorf("local Prime missing %q:\n%s", want, got)
		}
	}
	for _, banned := range []string{"pr [--for", "Sandboxed sessions"} {
		if strings.Contains(got, banned) {
			t.Errorf("local Prime still mentions %q:\n%s", banned, got)
		}
	}
}

func TestPrimeCursorHookCarriesTheLocalPrimer(t *testing.T) {
	app, out, _ := newApp(newFake())
	app.Local = true
	if err := app.Prime(ctx, PrimeOpts{HookFormat: "cursor"}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		AdditionalContext string `json:"additional_context"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.AdditionalContext, "hew close <n> --completed") || strings.Contains(got.AdditionalContext, "pr [--for") {
		t.Errorf("cursor hook context is not the local primer:\n%s", got.AdditionalContext)
	}
}

func TestInitOnTheLocalBackendPrintsTheLocalSnippet(t *testing.T) {
	app, out, _ := newApp(newFake())
	app.Local = true
	if err := app.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), conventions.ClaudeSnippetLocal) {
		t.Errorf("output missing the local snippet:\n%s", out.String())
	}
	if strings.Contains(out.String(), "GitHub Issues") {
		t.Errorf("local init still points at GitHub Issues:\n%s", out.String())
	}
}

// From a linked worktree the tracker must land in the main checkout — the one
// place every worktree can see — and not in the worktree that happened to run
// init.
func TestInitLocalCreatesTheTrackerAtTheMainCheckout(t *testing.T) {
	main, wt := gitRepoWithWorktree(t)
	app, out, _ := newApp(newFake())

	if err := app.InitLocal(wt); err != nil {
		t.Fatal(err)
	}
	tracker := filepath.Join(main, local.DirName)
	cfg, err := local.ReadConfig(tracker)
	if err != nil || cfg.Backend != local.BackendLocal {
		t.Fatalf("config at the main checkout = %+v, %v; want a local backend", cfg, err)
	}
	if _, err := os.Stat(filepath.Join(wt, local.DirName)); !os.IsNotExist(err) {
		t.Errorf("tracker was created inside the worktree: stat err = %v", err)
	}
	for _, want := range []string{tracker, conventions.ClaudeSnippetLocal, "hew hooks install"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestInitLocalIsIdempotentAndKeepsIssues(t *testing.T) {
	main, _ := gitRepoWithWorktree(t)
	app, _, _ := newApp(newFake())
	if err := app.InitLocal(main); err != nil {
		t.Fatal(err)
	}
	store, err := local.Open(filepath.Join(main, local.DirName), "me")
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateIssue(ctx, "Keep me", "", []string{"P2", "task"})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.InitLocal(main); err != nil {
		t.Fatalf("second init: %v", err)
	}
	if _, err := store.GetIssue(ctx, created.Number); err != nil {
		t.Errorf("re-running init lost the issue: %v", err)
	}
}

func TestInitLocalJSON(t *testing.T) {
	main, _ := gitRepoWithWorktree(t)
	app, out, _ := newApp(newFake())
	app.JSON = true
	if err := app.InitLocal(main); err != nil {
		t.Fatal(err)
	}
	var got struct{ Backend, Path string }
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("%v:\n%s", err, out.String())
	}
	if got.Backend != local.BackendLocal || got.Path != filepath.Join(main, local.DirName) {
		t.Errorf("JSON = %+v", got)
	}
}

// After migration the config says github and the local issues are an archive;
// reporting a ready local tracker there would describe one no command uses.
func TestInitLocalRefusesAMigratedRepo(t *testing.T) {
	main, _ := gitRepoWithWorktree(t)
	tracker := filepath.Join(main, local.DirName)
	if err := local.Init(tracker); err != nil {
		t.Fatal(err)
	}
	if err := local.WriteConfig(tracker, local.Config{Backend: local.BackendGitHub}); err != nil {
		t.Fatal(err)
	}
	// Damage the archive so a refusal that still ran Init would show: Init
	// recreates a missing .gitignore.
	ignore := filepath.Join(tracker, ".gitignore")
	if err := os.Remove(ignore); err != nil {
		t.Fatal(err)
	}
	app, out, _ := newApp(newFake())
	err := app.InitLocal(main)
	exitCode(t, err, ExitGeneric)
	if _, statErr := os.Stat(ignore); !os.IsNotExist(statErr) {
		t.Errorf("refused init still wrote into the archived tracker: %v", statErr)
	}
	if !strings.Contains(err.Error(), "github") || out.Len() != 0 {
		t.Errorf("err = %v, out = %q; want a refusal naming the backend and no snippet", err, out.String())
	}
}

func TestInitLocalOutsideAGitCheckoutFails(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	isolateGitConfig(t)
	// Keep git from walking up into a checkout that happens to enclose the
	// temp directory.
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	app, _, _ := newApp(newFake())
	err := app.InitLocal(dir)
	exitCode(t, err, ExitGeneric)
	if !strings.Contains(err.Error(), "main checkout") {
		t.Errorf("err = %v, want the checkout failure", err)
	}
}
