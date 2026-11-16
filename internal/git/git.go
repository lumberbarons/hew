// Package git reads the local branch state the pr command needs. It shells
// out to git rather than linking a git library: the CLI already assumes a
// checkout with a remote (that's how the repo is detected), and the two
// facts needed here are one command each.
package git

import (
	"fmt"
	"os/exec"
	"strings"
)

// State is the local branch state a pull request is opened from.
type State struct {
	// Branch is the current branch name, empty on a detached HEAD.
	Branch string
	// Upstream is the branch name on the remote, empty when the branch
	// tracks nothing there. It is not always the local name: see Head.
	Upstream string
	// Pushed reports whether the branch has a remote-tracking counterpart.
	// GitHub can only open a pull request for a ref it can already see, so
	// this is a precondition, not a nicety.
	Pushed bool
}

// Head is the branch a pull request is opened from — the upstream name when
// there is one, the local name otherwise. The two differ whenever a checkout
// is made under a generated local name: an agent harness working in a git
// worktree ends up on worktree-feat+x tracking origin/feat/x, and GitHub has
// never heard of the local name, so a PR opened from it is rejected outright.
func (s State) Head() string {
	if s.Upstream != "" {
		return s.Upstream
	}
	return s.Branch
}

// runner runs a command and returns its trimmed stdout; injected by tests.
type runner func(name string, args ...string) (string, error)

func run(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return strings.TrimSpace(string(out)), err
}

// Current reads the state of the checkout in the working directory.
func Current() (State, error) { return current(run) }

// CurrentFor reads the state of the checkout at dir, never the process's
// working directory: the public `pr` workflow answers every checkout question
// from the worktree its caller named.
func CurrentFor(dir string) (State, error) { return currentFor(run, dir) }

func current(r runner) (State, error) { return currentFor(r, "") }

func currentFor(r runner, dir string) (State, error) {
	branch, err := r("git", gitArgs(dir, "rev-parse", "--abbrev-ref", "HEAD")...)
	if err != nil {
		return State{}, fmt.Errorf("cannot read the current git branch: %w", err)
	}
	if branch == "" || branch == "HEAD" {
		return State{}, fmt.Errorf("HEAD is detached; check out a branch first")
	}
	// A failure here means "no upstream", which is a state, not an error:
	// git exits non-zero for an unset upstream exactly as it does for a
	// broken repo, and the repo can't be broken — the branch just resolved.
	upstream, err := r("git", gitArgs(dir, "rev-parse", "--abbrev-ref", "@{upstream}")...)
	if err != nil || upstream == "" {
		return State{Branch: branch}, nil
	}
	return State{Branch: branch, Upstream: remoteBranch(r, dir, branch, upstream), Pushed: true}, nil
}

// gitArgs prefixes a git invocation with the directory to run it in.
func gitArgs(dir string, args ...string) []string {
	if dir == "" {
		return args
	}
	return append([]string{"-C", dir}, args...)
}

// remoteBranch strips the remote from an upstream ref like origin/feat/x,
// returning "" when the branch tracks no remote at all. The remote name is
// read rather than assumed to be the first path segment, because branch
// names contain slashes too: cutting blindly at the first one turns a branch
// tracking the local feat/x into a PR opened from x.
func remoteBranch(r runner, dir, branch, upstream string) string {
	remote, err := r("git", gitArgs(dir, "config", "--get", "branch."+branch+".remote")...)
	// "." is git's spelling of "the local repository" — an upstream that
	// lives here rather than on a remote, and so no name GitHub knows.
	if err != nil || remote == "" || remote == "." {
		return ""
	}
	return strings.TrimPrefix(upstream, remote+"/")
}
