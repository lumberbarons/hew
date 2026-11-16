package tracker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/local"
)

// repoWithWorktrees builds a real main checkout and one linked worktree per
// name, with the developer's git environment isolated so a hook's GIT_DIR or a
// global init.defaultBranch cannot change what the test sees.
func repoWithWorktrees(t *testing.T, names ...string) (main string, worktrees []string) {
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
	var err error
	if main, err = filepath.EvalSymlinks(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	gitRun(t, main, "init", "--initial-branch=main")
	gitRun(t, main, "-c", "user.email=t@example.com", "-c", "user.name=t", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "seed")
	for _, name := range names {
		wt := filepath.Join(t.TempDir(), name)
		gitRun(t, main, "worktree", "add", "-b", "feat/"+name, wt)
		if wt, err = filepath.EvalSymlinks(wt); err != nil {
			t.Fatal(err)
		}
		worktrees = append(worktrees, wt)
	}
	return main, worktrees
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func initLocalTracker(t *testing.T, main string) string {
	t.Helper()
	dir := filepath.Join(main, local.DirName)
	if err := local.Init(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

// shortDigest is the path tag the identity carries, spelled out independently
// of local's scheme so a change to it is a deliberate test change.
func shortDigest(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:3])
}

// The library's backend names are its own strings, so its docs and API do not
// name an internal package; this keeps them in step with the store's.
func TestBackendNamesMatchTheLocalStore(t *testing.T) {
	if string(BackendLocal) != local.BackendLocal || string(BackendGitHub) != local.BackendGitHub {
		t.Errorf("backend names = %q, %q; local store uses %q, %q", BackendLocal, BackendGitHub, local.BackendLocal, local.BackendGitHub)
	}
}

func TestDetectFindsTheSharedLocalTrackerFromEitherCheckout(t *testing.T) {
	main, worktrees := repoWithWorktrees(t, "agent-one")
	dir := initLocalTracker(t, main)
	for name, path := range map[string]string{"main": main, "worktree": worktrees[0]} {
		got, err := Detect(path)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.Backend != BackendLocal || got.Dir != dir || got.MainCheckout != main {
			t.Errorf("%s: Detect = %+v, want local %s in %s", name, got, dir, main)
		}
	}
}

func TestDetectFallsBackToGitHubWithoutALocalTracker(t *testing.T) {
	main, _ := repoWithWorktrees(t)
	got, err := Detect(main)
	if err != nil {
		t.Fatal(err)
	}
	if got.Backend != BackendGitHub || got.MainCheckout != main || got.Dir != "" {
		t.Errorf("Detect = %+v, want GitHub in %s", got, main)
	}
	// Outside any checkout the answer is still GitHub, with no main checkout
	// to read a remote from.
	got, err = Detect(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got.Backend != BackendGitHub || got.MainCheckout != "" {
		t.Errorf("Detect outside = %+v, want GitHub with no checkout", got)
	}
}

func TestDetectReportsRepositoryAndStableLocalID(t *testing.T) {
	// A local tracker's report includes the id its checkpoints bind to, so a
	// consumer can tell one checkout's tracker from another's.
	main, _ := repoWithWorktrees(t)
	dir := initLocalTracker(t, main)
	cfg, err := local.ReadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Detect(main)
	if err != nil {
		t.Fatal(err)
	}
	if got.Backend != BackendLocal || got.Dir != dir || got.MainCheckout != main {
		t.Errorf("Detect = %+v, want local %s in %s", got, dir, main)
	}
	if got.ID != cfg.ID || cfg.ID == "" {
		t.Errorf("ID = %q, want the config's stable id %q", got.ID, cfg.ID)
	}
	if got.Repo != "" {
		t.Errorf("Repo = %q, want empty for a local tracker", got.Repo)
	}

	// A GitHub checkout reports the origin remote's owner/name.
	githubMain, _ := repoWithWorktrees(t)
	gitRun(t, githubMain, "remote", "add", "origin", "git@github.com:acme/hew.git")
	got, err = Detect(githubMain)
	if err != nil {
		t.Fatal(err)
	}
	if got.Backend != BackendGitHub || got.Repo != "acme/hew" || got.MainCheckout != githubMain || got.Dir != "" || got.ID != "" {
		t.Errorf("Detect = %+v, want GitHub acme/hew in %s", got, githubMain)
	}

	// A remote Detect cannot read as a github.com repository leaves Repo empty
	// instead of failing backend detection; Open is what must refuse it.
	other, _ := repoWithWorktrees(t)
	gitRun(t, other, "remote", "add", "origin", "https://gitlab.com/acme/hew.git")
	got, err = Detect(other)
	if err != nil {
		t.Fatal(err)
	}
	if got.Backend != BackendGitHub || got.Repo != "" {
		t.Errorf("Detect = %+v, want GitHub with no repository for a non-GitHub remote", got)
	}
}

func TestDetectTreatsAMigratedTrackerAsGitHub(t *testing.T) {
	main, _ := repoWithWorktrees(t)
	dir := initLocalTracker(t, main)
	cfg, err := local.ReadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Backend = local.BackendGitHub
	if err := local.WriteConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Detect(main)
	if err != nil {
		t.Fatal(err)
	}
	if got.Backend != BackendGitHub || got.Dir != "" {
		t.Errorf("Detect = %+v, want GitHub for a migrated tracker", got)
	}
}

func TestDetectReportsADamagedTrackerConfig(t *testing.T) {
	main, _ := repoWithWorktrees(t)
	dir := initLocalTracker(t, main)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Detect(main); err == nil {
		t.Error("expected the damaged config to be reported, not read as GitHub")
	}
}

// Open answers every path question from Worktree: the process may be anywhere,
// and HEW_USER is the CLI's override, not the library's.
func TestOpenUsesTheWorktreeNotTheWorkingDirectoryOrHEWUser(t *testing.T) {
	main, worktrees := repoWithWorktrees(t, "agent-two")
	initLocalTracker(t, main)
	t.Setenv("HEW_USER", "env-actor")
	t.Setenv("USER", "jim")
	t.Chdir(t.TempDir())
	ctx := context.Background()

	tr, err := Open(ctx, Config{Worktree: worktrees[0]})
	if err != nil {
		t.Fatal(err)
	}
	want := "jim-agent-two-" + shortDigest(worktrees[0])
	if got := tr.Identity(); got != want {
		t.Errorf("Identity = %q, want %q (HEW_USER must not decide it)", got, want)
	}
	if got, _ := tr.store.(*local.Store).Viewer(ctx); got != want {
		t.Errorf("store viewer = %q, want %q; the tracker and its store must agree", got, want)
	}
	if tr.Backend() != BackendLocal {
		t.Errorf("Backend = %q, want local", tr.Backend())
	}

	explicit, err := Open(context.Background(), Config{Worktree: worktrees[0], Actor: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	if got := explicit.Identity(); got != "worker" {
		t.Errorf("explicit Identity = %q, want worker", got)
	}
	if got, _ := explicit.store.(*local.Store).Viewer(ctx); got != "worker" {
		t.Errorf("explicit store viewer = %q, want worker", got)
	}
}

// Two worktrees under one OS account are two claimants: the first claim wins
// and the second is refused as someone else's.
func TestTwoWorktreesAreDistinctClaimants(t *testing.T) {
	main, worktrees := repoWithWorktrees(t, "agent-one", "agent-two")
	initLocalTracker(t, main)
	t.Setenv("HEW_USER", "")
	t.Setenv("USER", "jim")
	ctx := context.Background()

	one, err := Open(ctx, Config{Worktree: worktrees[0]})
	if err != nil {
		t.Fatal(err)
	}
	two, err := Open(ctx, Config{Worktree: worktrees[1]})
	if err != nil {
		t.Fatal(err)
	}
	if one.Identity() == two.Identity() {
		t.Fatalf("both worktrees are %q, want distinct claimants", one.Identity())
	}

	storeOne := one.store.(*local.Store)
	storeTwo := two.store.(*local.Store)
	actorOne, err := storeOne.Viewer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	actorTwo, err := storeTwo.Viewer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if actorOne != one.Identity() || actorTwo != two.Identity() {
		t.Errorf("store viewers = %q, %q; tracker identities = %q, %q", actorOne, actorTwo, one.Identity(), two.Identity())
	}
	issue, err := storeOne.CreateIssue(ctx, "Contended", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := storeOne.ClaimIssue(ctx, issue.Number, actorOne, false); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if err := storeTwo.ClaimIssue(ctx, issue.Number, actorTwo, false); !errors.Is(err, gh.ErrClaimConflict) {
		t.Fatalf("second claim = %v, want a claim conflict", err)
	}
	read, err := storeTwo.GetIssue(ctx, issue.Number)
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Assignees) != 1 || read.Assignees[0] != actorOne {
		t.Errorf("assignees = %v, want the first claimant %q", read.Assignees, actorOne)
	}
}

func TestAsIsACheapCloneSharingTheStore(t *testing.T) {
	main, _ := repoWithWorktrees(t)
	initLocalTracker(t, main)
	ctx := context.Background()
	base, err := Open(ctx, Config{Worktree: main, Actor: "base"})
	if err != nil {
		t.Fatal(err)
	}
	x, y := base.As("worker-x"), base.As("worker-y")
	if x.Identity() != "worker-x" || y.Identity() != "worker-y" || base.Identity() != "base" {
		t.Errorf("identities = %q, %q, %q; want worker-x, worker-y, base", x.Identity(), y.Identity(), base.Identity())
	}
	for name, tc := range map[string]struct {
		tracker *Tracker
		want    string
	}{
		"base":     {base, "base"},
		"worker-x": {x, "worker-x"},
		"worker-y": {y, "worker-y"},
	} {
		if got, _ := tc.tracker.store.(*local.Store).Viewer(ctx); got != tc.want {
			t.Errorf("%s store viewer = %q, want %q", name, got, tc.want)
		}
	}
	if x.store.(*local.Store).ID() != base.store.(*local.Store).ID() {
		t.Error("As opened a different tracker, want the shared one")
	}
	// A write through the clone is visible through the receiver, attributed to
	// the clone's claimant rather than the receiver's.
	issue, err := x.store.(*local.Store).CreateIssue(ctx, "Shared", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := x.store.(*local.Store).Comment(ctx, issue.Number, "from worker-x"); err != nil {
		t.Fatal(err)
	}
	read, err := base.store.(*local.Store).GetIssue(ctx, issue.Number)
	if err != nil {
		t.Fatalf("receiver cannot see the clone's issue: %v", err)
	}
	if len(read.Comments) != 1 || read.Comments[0].Author != "worker-x" {
		t.Errorf("comments = %+v, want the clone's claimant as author", read.Comments)
	}
	if base.As("") != base || base.As("base") != base {
		t.Error("As with no change should return the receiver")
	}
}

// noticeWriter hands each notice to the test as it is written, so the test can
// release the lock only after the notice it is asserting on arrived.
type noticeWriter chan string

func (w noticeWriter) Write(p []byte) (int, error) {
	w <- string(p)
	return len(p), nil
}

// captureStderr runs fn with os.Stderr redirected to a file and returns what
// was written there.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	f, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = f
	defer func() { os.Stderr = orig }()
	fn()
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// A migration notice must reach the writer the caller injected and never
// stderr: a long-lived process's output is its own.
func TestOpenSendsMigrationNoticesToTheInjectedWriter(t *testing.T) {
	main, _ := repoWithWorktrees(t)
	dir := initLocalTracker(t, main)
	// A tracker without an id backfills one under the lock, so Open is the
	// writer that can wait behind a migration.
	cfg, err := local.ReadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ID = ""
	if err := local.WriteConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	frozen, err := local.Freeze(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer frozen.Release()

	notices := make(chan string, 1)
	var tr *Tracker
	var openErr error
	done := make(chan struct{})
	stderr := captureStderr(t, func() {
		go func() {
			tr, openErr = Open(context.Background(), Config{Worktree: main, Notice: noticeWriter(notices)})
			close(done)
		}()
		select {
		case got := <-notices:
			if !strings.Contains(got, "migration is in progress") {
				t.Errorf("notice = %q, want the migration", got)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no migration notice")
		}
		frozen.Release()
		<-done
	})
	if openErr != nil {
		t.Fatalf("Open: %v", openErr)
	}
	if tr.Backend() != BackendLocal {
		t.Errorf("Backend = %q, want local", tr.Backend())
	}
	if strings.Contains(stderr, "migration is in progress") {
		t.Errorf("notice reached stderr: %q", stderr)
	}
}

func TestOpenRequiresAWorktree(t *testing.T) {
	_, err := Open(context.Background(), Config{})
	var invalid *InvalidInputError
	if !errors.As(err, &invalid) || invalid.Field != "worktree" {
		t.Errorf("err = %v, want an InvalidInputError for worktree", err)
	}
}

// Open forwards its context to the local store's initial lock wait, so a
// caller's cancelled context does not sit behind a 15s lock timeout.
func TestOpenHonoursACancelledContext(t *testing.T) {
	main, _ := repoWithWorktrees(t)
	dir := initLocalTracker(t, main)
	cfg, err := local.ReadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ID = ""
	if err := local.WriteConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	frozen, err := local.Freeze(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer frozen.Release()

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, err = Open(cancelled, Config{Worktree: main})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("Open took %v, want it to abandon the lock wait", took)
	}
}

func TestDetectRequiresADirectory(t *testing.T) {
	_, err := Detect("")
	var invalid *InvalidInputError
	if !errors.As(err, &invalid) || invalid.Field != "dir" {
		t.Errorf("err = %v, want an InvalidInputError for dir", err)
	}
}

func TestOpenRejectsAnUnknownBackend(t *testing.T) {
	_, err := Open(context.Background(), Config{Worktree: t.TempDir(), Backend: "svn"})
	var invalid *InvalidInputError
	if !errors.As(err, &invalid) || invalid.Field != "backend" {
		t.Errorf("err = %v, want an InvalidInputError for backend", err)
	}
}

// --repo always means GitHub, even in a checkout with a local tracker.
func TestOpenForcesGitHubOverALocalTracker(t *testing.T) {
	main, _ := repoWithWorktrees(t)
	initLocalTracker(t, main)
	gitRun(t, main, "remote", "add", "origin", "git@github.com:owner/name.git")
	var got gh.Repo
	replaceGitHub(t, func(repo gh.Repo) (gh.IssueStore, error) {
		got = repo
		return &fakeStore{login: "octocat"}, nil
	})
	tr, err := Open(context.Background(), Config{Worktree: main, Backend: BackendGitHub})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Backend() != BackendGitHub || got.String() != "owner/name" {
		t.Errorf("tracker = {%q, %s}, want GitHub owner/name", tr.Backend(), got)
	}
}

// An explicit GitHub target skips detection entirely, so a damaged local
// config cannot stop --repo from reaching GitHub.
func TestOpenForcedGitHubSkipsDetection(t *testing.T) {
	main, _ := repoWithWorktrees(t)
	dir := initLocalTracker(t, main)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	replaceGitHub(t, func(gh.Repo) (gh.IssueStore, error) { return &fakeStore{login: "octocat"}, nil })
	tr, err := Open(context.Background(), Config{Worktree: main, Backend: BackendGitHub, Repo: "owner/name"})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Backend() != BackendGitHub {
		t.Errorf("Backend = %q, want GitHub", tr.Backend())
	}
}

// A caller that pinned local must be told the tracker is gone, not handed the
// repository's GitHub issues.
func TestOpenForcedLocalRefusesToFallBack(t *testing.T) {
	main, _ := repoWithWorktrees(t)
	_, err := Open(context.Background(), Config{Worktree: main, Backend: BackendLocal})
	var invalid *InvalidInputError
	if !errors.As(err, &invalid) || invalid.Field != "backend" {
		t.Errorf("err = %v, want an InvalidInputError for backend", err)
	}
}

func TestOpenGitHubRejectsANonGitHubRemote(t *testing.T) {
	for name, remote := range map[string]string{
		"another host": "https://gitlab.com/acme/widgets.git",
		"a local path": "../upstream/widgets",
	} {
		t.Run(name, func(t *testing.T) {
			main, _ := repoWithWorktrees(t)
			gitRun(t, main, "remote", "add", "origin", remote)
			_, err := Open(context.Background(), Config{Worktree: main})
			var invalid *InvalidInputError
			if !errors.As(err, &invalid) || invalid.Field != "repo" {
				t.Errorf("err = %v, want an InvalidInputError for repo", err)
			}
		})
	}
}

// fakeStore satisfies gh.IssueStore for the GitHub branch without credentials
// or the network; only Viewer is reached.
type fakeStore struct {
	gh.IssueStore
	login string
	err   error
}

func (f *fakeStore) Viewer(context.Context) (string, error) { return f.login, f.err }

func replaceGitHub(t *testing.T, build func(gh.Repo) (gh.IssueStore, error)) {
	t.Helper()
	old := newGitHub
	t.Cleanup(func() { newGitHub = old })
	newGitHub = build
}

func TestOpenGitHubUsesTheExplicitRepoAndLogin(t *testing.T) {
	var got gh.Repo
	replaceGitHub(t, func(repo gh.Repo) (gh.IssueStore, error) {
		got = repo
		return &fakeStore{login: "octocat"}, nil
	})
	tr, err := Open(context.Background(), Config{Worktree: t.TempDir(), Repo: "owner/name", Actor: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "owner/name" {
		t.Errorf("repo = %s, want owner/name", got)
	}
	if tr.Backend() != BackendGitHub || tr.Identity() != "octocat" {
		t.Errorf("tracker = {%q, %q}, want the GitHub login, not the requested actor", tr.Backend(), tr.Identity())
	}
	// As is a no-op on GitHub: the claim identity is the authenticated login.
	if tr.As("worker").Identity() != "octocat" {
		t.Error("As changed the GitHub identity, want the login")
	}
}

func TestOpenGitHubDetectsTheRepoFromTheOriginRemote(t *testing.T) {
	main, _ := repoWithWorktrees(t)
	gitRun(t, main, "remote", "add", "origin", "git@github.com:owner/name.git")
	var got gh.Repo
	replaceGitHub(t, func(repo gh.Repo) (gh.IssueStore, error) {
		got = repo
		return &fakeStore{login: "octocat"}, nil
	})
	if _, err := Open(context.Background(), Config{Worktree: main}); err != nil {
		t.Fatal(err)
	}
	if got.String() != "owner/name" {
		t.Errorf("repo = %s, want owner/name from the origin remote", got)
	}
}

func TestOpenGitHubRejectsAnUnusableRepo(t *testing.T) {
	for name, cfg := range map[string]Config{
		"malformed":      {Worktree: t.TempDir(), Repo: "nope"},
		"not a checkout": {Worktree: t.TempDir()},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Open(context.Background(), cfg)
			var invalid *InvalidInputError
			if !errors.As(err, &invalid) || invalid.Field != "repo" {
				t.Errorf("err = %v, want an InvalidInputError for repo", err)
			}
		})
	}
}

// A real checkout with no origin remote is a different branch from a
// directory that is not a checkout at all.
func TestOpenGitHubRejectsARepositoryWithNoOrigin(t *testing.T) {
	main, _ := repoWithWorktrees(t)
	_, err := Open(context.Background(), Config{Worktree: main})
	var invalid *InvalidInputError
	if !errors.As(err, &invalid) || invalid.Field != "repo" || !strings.Contains(err.Error(), "origin") {
		t.Errorf("err = %v, want an InvalidInputError naming the missing origin remote", err)
	}
}

func TestOpenGitHubMapsAuthFailures(t *testing.T) {
	cause := errors.New("credentials expired")
	replaceGitHub(t, func(gh.Repo) (gh.IssueStore, error) {
		return nil, &gh.AuthError{Err: cause}
	})
	_, err := Open(context.Background(), Config{Worktree: t.TempDir(), Repo: "owner/name"})
	var auth *AuthError
	if !errors.As(err, &auth) {
		t.Fatalf("err = %v, want the library's AuthError", err)
	}
	if !errors.Is(err, cause) {
		t.Errorf("err = %v, want the backend cause retained", err)
	}
}

// The Viewer call is the second place an auth failure can surface; it must be
// mapped the same way as client construction.
func TestOpenGitHubMapsViewerAuthFailures(t *testing.T) {
	cause := errors.New("token expired")
	replaceGitHub(t, func(gh.Repo) (gh.IssueStore, error) {
		return &fakeStore{err: &gh.AuthError{Err: cause}}, nil
	})
	_, err := Open(context.Background(), Config{Worktree: t.TempDir(), Repo: "owner/name"})
	var auth *AuthError
	if !errors.As(err, &auth) {
		t.Fatalf("err = %v, want the library's AuthError", err)
	}
	if !errors.Is(err, cause) {
		t.Errorf("err = %v, want the Viewer cause retained", err)
	}
}

func TestOpenGitHubPassesThroughOtherErrors(t *testing.T) {
	boom := errors.New("client construction failed")
	replaceGitHub(t, func(gh.Repo) (gh.IssueStore, error) { return nil, boom })
	_, err := Open(context.Background(), Config{Worktree: t.TempDir(), Repo: "owner/name"})
	if err != boom {
		t.Errorf("err = %v, want the client failure unchanged", err)
	}
}

func TestOpenGitHubPassesThroughViewerErrors(t *testing.T) {
	boom := errors.New("viewer failed")
	replaceGitHub(t, func(gh.Repo) (gh.IssueStore, error) {
		return &fakeStore{err: boom}, nil
	})
	_, err := Open(context.Background(), Config{Worktree: t.TempDir(), Repo: "owner/name"})
	if err != boom {
		t.Errorf("err = %v, want the Viewer failure unchanged", err)
	}
}

func TestOpenGitHubRejectsAnUnparseableRemote(t *testing.T) {
	main, _ := repoWithWorktrees(t)
	gitRun(t, main, "remote", "add", "origin", "/not/a/repository/url")
	_, err := Open(context.Background(), Config{Worktree: main})
	var invalid *InvalidInputError
	if !errors.As(err, &invalid) || invalid.Field != "repo" {
		t.Errorf("err = %v, want an InvalidInputError for repo", err)
	}
}

func TestOpenReportsADamagedTrackerConfig(t *testing.T) {
	main, _ := repoWithWorktrees(t)
	dir := initLocalTracker(t, main)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), Config{Worktree: main}); err == nil {
		t.Error("expected the damaged config to be reported")
	}
}

func TestOpenLocalReportsALostIssuesFile(t *testing.T) {
	main, _ := repoWithWorktrees(t)
	dir := initLocalTracker(t, main)
	if err := os.Remove(filepath.Join(dir, "issues.jsonl")); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), Config{Worktree: main}); err == nil {
		t.Error("expected a tracker that lost its issues to be refused")
	}
}

func TestDefaultActorReportsAGitFailure(t *testing.T) {
	if _, err := defaultActor(t.TempDir()); err == nil {
		t.Error("expected an identity outside a checkout to be refused, not guessed")
	}
}
