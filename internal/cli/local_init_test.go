package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

// A bare init in a local repo does what `init --local` does: it reports the
// tracker, instead of bootstrapping labels the local store already carries.
func TestInitOnTheLocalBackendIsInitLocal(t *testing.T) {
	main, _ := gitRepoWithWorktree(t)
	t.Chdir(main)

	bare, bareOut, _ := newApp(newFake())
	bare.Local = true
	if err := bare.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bareOut.String(), conventions.ClaudeSnippetLocal) {
		t.Errorf("output missing the local snippet:\n%s", bareOut.String())
	}
	for _, banned := range []string{"GitHub Issues", "convention labels"} {
		if strings.Contains(bareOut.String(), banned) {
			t.Errorf("local init still prints %q:\n%s", banned, bareOut.String())
		}
	}

	direct, directOut, _ := newApp(newFake())
	if err := direct.InitLocal(""); err != nil {
		t.Fatal(err)
	}
	if bareOut.String() != directOut.String() {
		t.Errorf("bare init and init --local disagree:\nbare: %q\ndirect: %q", bareOut.String(), directOut.String())
	}
}

// `init --json` is one shape on either backend: the same keys, carrying what
// that backend created.
func TestInitJSONHasOneShapeOnBothBackends(t *testing.T) {
	github, ghOut, _ := newApp(newFake())
	github.JSON = true
	if err := github.Init(ctx); err != nil {
		t.Fatal(err)
	}

	main, _ := gitRepoWithWorktree(t)
	localApp, localOut, _ := newApp(newFake())
	localApp.JSON = true
	if err := localApp.InitLocal(main); err != nil {
		t.Fatal(err)
	}

	keys := func(t *testing.T, raw []byte) []string {
		t.Helper()
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			t.Fatalf("%v:\n%s", err, raw)
		}
		got := make([]string, 0, len(obj))
		for key := range obj {
			got = append(got, key)
		}
		slices.Sort(got)
		return got
	}
	if ghKeys, localKeys := keys(t, ghOut.Bytes()), keys(t, localOut.Bytes()); !slices.Equal(ghKeys, localKeys) {
		t.Errorf("init --json shape differs: github has %v, local has %v", ghKeys, localKeys)
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

// A repo whose .hew is deleted looks exactly like one that never had a local
// tracker, so hew falls back to GitHub; init is where that is said, once, on
// stderr so --json callers see it too.
func TestInitLocalWarnsOnceThatADeletedTrackerFallsBackToGitHub(t *testing.T) {
	main, _ := gitRepoWithWorktree(t)
	app, out, errOut := newApp(newFake())
	app.JSON = true
	if err := app.InitLocal(main); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"git clean -fdx", "GitHub"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("stderr missing %q:\n%s", want, errOut.String())
		}
	}
	if strings.Contains(out.String(), "git clean") {
		t.Errorf("the warning leaked into stdout:\n%s", out.String())
	}

	again, _, againErr := newApp(newFake())
	if err := again.InitLocal(main); err != nil {
		t.Fatal(err)
	}
	if againErr.Len() != 0 {
		t.Errorf("a rerun repeated the warning:\n%s", againErr.String())
	}
}

// Config present but issues.jsonl gone is data loss, not a fresh start. init
// still recreates the file (it is the documented way out), but says so, so the
// empty tracker is not mistaken for a working one.
func TestInitLocalWarnsWhenItRecreatesAMissingIssuesFile(t *testing.T) {
	main, _ := gitRepoWithWorktree(t)
	tracker := filepath.Join(main, local.DirName)
	app, _, _ := newApp(newFake())
	if err := app.InitLocal(main); err != nil {
		t.Fatal(err)
	}
	issues := filepath.Join(tracker, "issues.jsonl")
	if err := os.Remove(issues); err != nil {
		t.Fatal(err)
	}

	again, out, errOut := newApp(newFake())
	if err := again.InitLocal(main); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{issues, "empty", "backup"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("stderr missing %q:\n%s", want, errOut.String())
		}
	}
	if strings.Contains(out.String(), "backup") {
		t.Errorf("the warning leaked into stdout:\n%s", out.String())
	}
	if _, err := os.Stat(issues); err != nil {
		t.Errorf("issues.jsonl was not recreated: %v", err)
	}

	quiet, _, quietErr := newApp(newFake())
	if err := quiet.InitLocal(main); err != nil {
		t.Fatal(err)
	}
	if quietErr.Len() != 0 {
		t.Errorf("a rerun over an intact tracker warned:\n%s", quietErr.String())
	}
}

// A config that cannot be read is refused with the path and the way out, and
// nothing is written over it.
func TestInitLocalRefusesACorruptConfigWithARecoveryHint(t *testing.T) {
	main, _ := gitRepoWithWorktree(t)
	tracker := filepath.Join(main, local.DirName)
	app, _, _ := newApp(newFake())
	if err := app.InitLocal(main); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(tracker, "config.json")
	if err := os.WriteFile(cfgPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	again, _, _ := newApp(newFake())
	err := again.InitLocal(main)
	if err == nil || !strings.Contains(err.Error(), cfgPath) || strings.Contains(err.Error(), "unexpected end of JSON") {
		t.Errorf("err = %v, want the path and no decoder text", err)
	}
}

// The recovery the refusals promise: init --local on a .hew that lost its
// config restores it, keeps the issues, and selection works again.
func TestInitLocalRecoversATrackerThatLostItsConfig(t *testing.T) {
	main, _ := gitRepoWithWorktree(t)
	tracker := filepath.Join(main, local.DirName)
	app, _, _ := newApp(newFake())
	if err := app.InitLocal(main); err != nil {
		t.Fatal(err)
	}
	store, err := local.Open(tracker, "me")
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateIssue(ctx, "survivor", "", []string{"P2", "task"})
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(filepath.Join(tracker, "config.json")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := local.Locate(main); err == nil {
		t.Fatal("Locate accepted a tracker without a config")
	}
	recover, _, _ := newApp(newFake())
	if err := recover.InitLocal(main); err != nil {
		t.Fatalf("init --local on a tracker without a config: %v", err)
	}
	if _, ok, err := local.Locate(main); err != nil || !ok {
		t.Fatalf("Locate after recovery = %v, %v; want the local tracker", ok, err)
	}
	reopened, err := local.Open(tracker, "me")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := reopened.GetIssue(ctx, created.Number); err != nil || got.Title != "survivor" {
		t.Errorf("issue after recovery = %+v, %v; want the survivor", got, err)
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
	var got struct {
		Backend       string
		Path          string
		CreatedLabels []string `json:"createdLabels"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("%v:\n%s", err, out.String())
	}
	if got.Backend != local.BackendLocal || got.Path != filepath.Join(main, local.DirName) {
		t.Errorf("JSON = %+v", got)
	}
	if len(got.CreatedLabels) != 0 {
		t.Errorf("createdLabels = %v; the local backend creates no labels", got.CreatedLabels)
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

// Local mode needs git, and the failure must say what git said rather than
// the bare exit status.
func TestInitLocalOutsideAGitCheckoutSurfacesGitsMessage(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	isolateGitConfig(t)
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	app, _, _ := newApp(newFake())
	err := app.InitLocal(dir)
	exitCode(t, err, ExitGeneric)
	msg := err.Error()
	if !strings.Contains(msg, "not a git repository") {
		t.Errorf("err = %q, want git's own message", msg)
	}
	if strings.Contains(msg, "exit status") || strings.Contains(msg, "\n") {
		t.Errorf("err = %q, want one line without the bare exit status", msg)
	}
	if !strings.Contains(msg, "git init") {
		t.Errorf("err = %q, want the requirement named with a way forward", msg)
	}
}
