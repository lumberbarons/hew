package git

import (
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
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

func TestLinkedWorktreeIsEmptyInTheMainCheckoutAndItsPathInALinkedOne(t *testing.T) {
	r := func(_ string, args ...string) (string, error) {
		switch {
		case slices.Contains(args, "--show-toplevel"):
			if args[0] == "-C" && args[1] == "/work/wt" {
				return "/work/wt", nil
			}
			return "/work/main", nil
		case slices.Contains(args, "list"):
			return "worktree /work/main\nHEAD abc\n", nil
		}
		return "", errors.New("unexpected command")
	}
	if name, err := linkedWorktree(r, ""); err != nil || name != "" {
		t.Errorf("main checkout = %q, %v; want no name", name, err)
	}
	if name, err := linkedWorktree(r, "/work/wt"); err != nil || name != "/work/wt" {
		t.Errorf("linked worktree = %q, %v; want /work/wt", name, err)
	}
}

func TestLinkedWorktreeReportsGitFailure(t *testing.T) {
	failing := func(string, ...string) (string, error) { return "", errors.New("not a git repository") }
	if _, err := linkedWorktree(failing, ""); err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("err = %v, want the git failure wrapped", err)
	}
	empty := func(string, ...string) (string, error) { return "", nil }
	if _, err := linkedWorktree(empty, ""); err == nil {
		t.Error("expected an error when git returns no path")
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
	if wt, err = filepath.EvalSymlinks(wt); err != nil {
		t.Fatal(err)
	}

	for name, dir := range map[string]string{"main": main, "linked": wt} {
		got, err := MainCheckout(dir)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != main {
			t.Errorf("%s: MainCheckout = %q, want %q", name, got, main)
		}
		wantName := ""
		if name == "linked" {
			wantName = wt
		}
		if got, err := LinkedWorktree(dir); err != nil || got != wantName {
			t.Errorf("%s: LinkedWorktree = %q, %v; want %q", name, got, err, wantName)
		}
	}
}
