package git

import (
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMainCheckoutTakesTheFirstListedWorktree(t *testing.T) {
	var got []string
	r := func(name string, args ...string) (string, error) {
		got = append([]string{name}, args...)
		return "worktree /work/main\nHEAD abc\nbranch refs/heads/main\n\nworktree /work/wt\nHEAD def\nbranch refs/heads/feat", nil
	}
	path, err := mainCheckout(r, "/work/wt")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/work/main" {
		t.Errorf("path = %q, want /work/main", path)
	}
	want := []string{"git", "-C", "/work/wt", "worktree", "list", "--porcelain"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("command = %v, want %v", got, want)
	}
}

func TestMainCheckoutOmitsDashCForTheWorkingDirectory(t *testing.T) {
	var got []string
	r := func(name string, args ...string) (string, error) {
		got = args
		return "worktree /work/main", nil
	}
	if _, err := mainCheckout(r, ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(got, " "), "-C") {
		t.Errorf("args = %v, want no -C", got)
	}
}

func TestMainCheckoutReportsGitFailureAndOddOutput(t *testing.T) {
	failing := func(string, ...string) (string, error) { return "", errors.New("not a git repository") }
	if _, err := mainCheckout(failing, ""); err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("err = %v, want the git failure wrapped", err)
	}
	odd := func(string, ...string) (string, error) { return "something else", nil }
	if _, err := mainCheckout(odd, ""); err == nil {
		t.Error("expected an error for output that does not start with a worktree line")
	}
}

// The reason this exists: from a linked worktree, the answer must be the
// main checkout, not the worktree. Real git, because a fake runner accepts any
// argument string.
func TestMainCheckoutFromALinkedWorktreeFindsTheMainCheckout(t *testing.T) {
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
	// macOS temp dirs sit behind a symlink and git reports the real path.
	main, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git(main, "init", "--initial-branch=main")
	git(main, "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "--allow-empty", "-m", "seed")
	wt := filepath.Join(t.TempDir(), "wt")
	git(main, "worktree", "add", "-b", "feat/x", wt)

	for name, dir := range map[string]string{"main": main, "linked": wt} {
		got, err := MainCheckout(dir)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != main {
			t.Errorf("%s: MainCheckout = %q, want %q", name, got, main)
		}
	}
}
