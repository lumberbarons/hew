package pr

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/local"
	"github.com/lumberbarons/hew/tracker"
)

// gitCheckout is a bare repository checkout with no tracker configuration: the
// backend detection a local tracker is added to.
func gitCheckout(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(key, "GIT_") {
			t.Setenv(key, os.Getenv(key))
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "--initial-branch=feat/x")
	run("-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "--allow-empty", "-m", "seed")
	return dir
}

// A local tracker has no pull requests: the refusal is the workflow's, typed,
// and it happens at Open rather than on the first operation.
func TestOpenRefusesALocalTracker(t *testing.T) {
	dir := gitCheckout(t)
	if err := local.Init(filepath.Join(dir, local.DirName)); err != nil {
		t.Fatal(err)
	}
	_, err := Open(t.Context(), Config{Worktree: dir})
	var noPRs *NoPullRequestsError
	if !errors.As(err, &noPRs) {
		t.Fatalf("err = %T (%v), want *NoPullRequestsError", err, err)
	}
}

// An Open without a worktree is invalid input, not an empty directory read.
func TestOpenRequiresAWorktree(t *testing.T) {
	_, err := Open(t.Context(), Config{})
	var invalid *tracker.InvalidInputError
	if !errors.As(err, &invalid) || invalid.Field != "worktree" {
		t.Fatalf("err = %T (%v), want an InvalidInputError for worktree", err, err)
	}
}

func TestParseRepo(t *testing.T) {
	repo, err := parseRepo("owner/name")
	if err != nil {
		t.Fatal(err)
	}
	if repo != (gh.Repo{Owner: "owner", Name: "name"}) {
		t.Errorf("repo = %+v", repo)
	}
	for _, spec := range []string{"", "nope", "/name", "owner/", "owner/name/extra"} {
		if _, err := parseRepo(spec); err == nil {
			t.Errorf("parseRepo(%q) accepted a malformed target", spec)
		}
	}
}
