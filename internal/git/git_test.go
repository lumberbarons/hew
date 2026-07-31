package git

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRunner answers each git invocation from a table keyed by the joined
// arguments, so a test states exactly what git said and nothing else.
func fakeRunner(t *testing.T, answers map[string]string, failures map[string]error) runner {
	t.Helper()
	return func(name string, args ...string) (string, error) {
		if name != "git" {
			t.Fatalf("ran %q, want git", name)
		}
		key := strings.Join(args, " ")
		if err, ok := failures[key]; ok {
			return "", err
		}
		out, ok := answers[key]
		if !ok {
			t.Fatalf("unexpected git invocation: git %s", key)
		}
		return out, nil
	}
}

func TestCurrentReportsBranchAndUpstream(t *testing.T) {
	got, err := current(fakeRunner(t, map[string]string{
		"rev-parse --abbrev-ref HEAD":                "feat/pr-command",
		"rev-parse --abbrev-ref @{upstream}":         "origin/feat/pr-command",
		"config --get branch.feat/pr-command.remote": "origin",
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got.Branch != "feat/pr-command" {
		t.Errorf("Branch = %q, want feat/pr-command", got.Branch)
	}
	if !got.Pushed {
		t.Error("Pushed = false, want true when the branch has an upstream")
	}
	if got.Head() != "feat/pr-command" {
		t.Errorf("Head() = %q, want feat/pr-command", got.Head())
	}
}

// The bug this guards: an agent harness checks the worktree out under a
// generated local name while the branch it tracks keeps the real one. GitHub
// only knows the upstream name, so a PR opened from the local name is
// rejected as an invalid head (#65).
func TestCurrentResolvesAWorktreeLocalNameToItsUpstream(t *testing.T) {
	got, err := current(fakeRunner(t, map[string]string{
		"rev-parse --abbrev-ref HEAD":                     "worktree-fix+pr-head",
		"rev-parse --abbrev-ref @{upstream}":              "origin/fix/pr-head",
		"config --get branch.worktree-fix+pr-head.remote": "origin",
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got.Upstream != "fix/pr-head" {
		t.Errorf("Upstream = %q, want fix/pr-head", got.Upstream)
	}
	if got.Head() != "fix/pr-head" {
		t.Errorf("Head() = %q, want the upstream name, not the local one", got.Head())
	}
}

// The remote is read from config rather than assumed to be origin, so a
// branch tracking any other remote still resolves to a bare branch name.
func TestCurrentStripsTheConfiguredRemoteNotOrigin(t *testing.T) {
	got, err := current(fakeRunner(t, map[string]string{
		"rev-parse --abbrev-ref HEAD":        "feat/x",
		"rev-parse --abbrev-ref @{upstream}": "fork/feat/x",
		"config --get branch.feat/x.remote":  "fork",
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got.Head() != "feat/x" {
		t.Errorf("Head() = %q, want feat/x", got.Head())
	}
}

// "." is git's spelling of an upstream in this same repository. There is no
// remote name to strip, and stripping the first path segment anyway would
// turn feat/x into a PR opened from x — so the local name stands.
func TestCurrentKeepsTheLocalNameForAnUpstreamInThisRepo(t *testing.T) {
	got, err := current(fakeRunner(t, map[string]string{
		"rev-parse --abbrev-ref HEAD":        "feat/x",
		"rev-parse --abbrev-ref @{upstream}": "main",
		"config --get branch.feat/x.remote":  ".",
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got.Upstream != "" {
		t.Errorf("Upstream = %q, want empty for an upstream that is not on a remote", got.Upstream)
	}
	if got.Head() != "feat/x" {
		t.Errorf("Head() = %q, want the local branch name", got.Head())
	}
}

// An upstream ref with no configured remote (git exits non-zero for the
// unset key) falls back rather than guessing at the ref's shape.
func TestCurrentFallsBackWhenTheRemoteIsUnreadable(t *testing.T) {
	got, err := current(fakeRunner(t,
		map[string]string{
			"rev-parse --abbrev-ref HEAD":        "feat/x",
			"rev-parse --abbrev-ref @{upstream}": "origin/feat/x",
		},
		map[string]error{"config --get branch.feat/x.remote": errors.New("exit status 1")},
	))
	if err != nil {
		t.Fatal(err)
	}
	if got.Head() != "feat/x" {
		t.Errorf("Head() = %q, want the local branch name", got.Head())
	}
}

// An unset upstream is a state, not a failure: git exits non-zero for it,
// and Current must report Pushed=false rather than propagating an error —
// the pr command turns it into "push it first".
func TestCurrentUnsetUpstreamIsNotAnError(t *testing.T) {
	got, err := current(fakeRunner(t,
		map[string]string{"rev-parse --abbrev-ref HEAD": "feat/local-only"},
		map[string]error{"rev-parse --abbrev-ref @{upstream}": errors.New("exit status 128")},
	))
	if err != nil {
		t.Fatalf("current() errored on an unpushed branch: %v", err)
	}
	if got.Branch != "feat/local-only" || got.Pushed {
		t.Errorf("current() = %+v, want an unpushed feat/local-only", got)
	}
	if got.Head() != "feat/local-only" {
		t.Errorf("Head() = %q, want the local branch name", got.Head())
	}
}

// git prints "HEAD" for the branch name on a detached HEAD, which is not a
// branch a PR can be opened from.
func TestCurrentRejectsDetachedHead(t *testing.T) {
	_, err := current(fakeRunner(t, map[string]string{
		"rev-parse --abbrev-ref HEAD": "HEAD",
	}, nil))
	if err == nil || !strings.Contains(err.Error(), "detached") {
		t.Fatalf("err = %v, want a detached-HEAD error", err)
	}
}

// An empty branch name is the other shape of "not on a branch", and earns
// the same actionable message as a detached HEAD rather than a bare failure.
func TestCurrentRejectsEmptyBranch(t *testing.T) {
	_, err := current(fakeRunner(t, map[string]string{
		"rev-parse --abbrev-ref HEAD": "",
	}, nil))
	if err == nil {
		t.Fatal("current() succeeded outside a branch")
	}
	if !strings.Contains(err.Error(), "check out a branch first") {
		t.Errorf("err = %q, want it to say what to do about it", err)
	}
}

func TestCurrentReportsUnreadableRepo(t *testing.T) {
	_, err := current(fakeRunner(t, nil, map[string]error{
		"rev-parse --abbrev-ref HEAD": errors.New("not a git repository"),
	}))
	if err == nil || !strings.Contains(err.Error(), "cannot read the current git branch") {
		t.Fatalf("err = %v, want a branch-read failure", err)
	}
}

// TestCurrentAgainstRealGit drives the exported Current — the real exec
// wiring — against a throwaway repository, so a change to the argument
// strings that the fake runner would happily accept still fails here.
func TestCurrentAgainstRealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "--initial-branch=feat/real"},
		{"-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "--allow-empty", "-m", "seed"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v failed: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "marker"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	got, err := Current()
	if err != nil {
		t.Fatal(err)
	}
	if got.Branch != "feat/real" {
		t.Errorf("Branch = %q, want feat/real", got.Branch)
	}
	if got.Pushed {
		t.Error("Pushed = true for a repo with no remote")
	}
}

// The same wiring against a branch whose upstream carries a different name,
// which is the worktree case reduced to what git actually stores. A fake
// runner accepts whatever argument strings it is given; real git does not.
func TestCurrentAgainstRealGitResolvesARenamedUpstream(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v failed: %v: %s", args, err, out)
		}
	}
	remote := t.TempDir()
	git(remote, "init", "--bare", "--initial-branch=main")

	dir := t.TempDir()
	git(dir, "init", "--initial-branch=worktree-feat+real")
	git(dir, "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "--allow-empty", "-m", "seed")
	git(dir, "remote", "add", "origin", remote)
	// The local name and the name on the remote deliberately differ.
	git(dir, "push", "-u", "origin", "worktree-feat+real:feat/real")
	t.Chdir(dir)

	got, err := Current()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Pushed {
		t.Error("Pushed = false for a branch with an upstream")
	}
	if got.Branch != "worktree-feat+real" {
		t.Errorf("Branch = %q, want the local name worktree-feat+real", got.Branch)
	}
	if got.Head() != "feat/real" {
		t.Errorf("Head() = %q, want the name the remote knows, feat/real", got.Head())
	}
}
