package git

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// MainCheckout returns the path of the repository's main working tree as seen
// from dir (the working directory when empty). In a linked worktree it is not
// the worktree itself but the checkout the worktrees hang off, which is where
// state shared by every worktree has to live. For a bare repository there is
// no working tree, so it is the repository directory.
func MainCheckout(dir string) (string, error) { return mainCheckout(run, dir) }

func mainCheckout(r runner, dir string) (string, error) {
	args := []string{"worktree", "list", "--porcelain"}
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	out, err := r("git", args...)
	if err != nil {
		return "", fmt.Errorf("cannot find the main checkout: %w", describeFailure(err))
	}
	// git lists the main worktree first, whichever worktree it is run from.
	// This is preferred to deriving it from --git-common-dir, which names the
	// directory holding the repository data: for a submodule that is a
	// folder under the superproject's .git, not a place anyone works.
	first, _, _ := strings.Cut(out, "\n")
	path, ok := strings.CutPrefix(first, "worktree ")
	if !ok || path == "" {
		return "", fmt.Errorf("cannot find the main checkout: unexpected `git worktree list` output %q", first)
	}
	return path, nil
}

// failure is a failed git command reported in git's own words. An exit error
// prints as "exit status 128", which says nothing; what git printed on stderr
// is the message a person can act on. Unwrap keeps the exit error reachable
// for IsNotRepo.
type failure struct {
	msg string
	err error
}

func (f *failure) Error() string { return f.msg }
func (f *failure) Unwrap() error { return f.err }

// describeFailure returns err as the line of git's stderr that states the
// cause, and err unchanged when there is none. That is the first "fatal:" line
// (or the first line at all): git's remedy and hint lines follow the cause, and
// on their own — a bare `git config --global --add safe.directory …` — they
// read as the failure.
func describeFailure(err error) error {
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return err
	}
	var first, fatal string
	for _, line := range strings.Split(string(exit.Stderr), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if first == "" {
			first = line
		}
		if strings.HasPrefix(line, "fatal:") {
			fatal = line
			break
		}
	}
	msg := cmp.Or(fatal, first)
	if msg == "" {
		return err
	}
	return &failure{msg: msg, err: err}
}

// LinkedWorktree returns the root path of the checkout containing dir when it
// is a linked worktree, which is what tells concurrent agents in one
// repository apart. The full path rather than the directory name, because two
// worktrees can share a name under different parents. It is empty for the main
// checkout, so a lone user in the primary checkout keeps the bare login, and an
// error when dir is not in a git checkout at all.
func LinkedWorktree(dir string) (string, error) { return linkedWorktree(run, dir) }

func linkedWorktree(r runner, dir string) (string, error) {
	args := []string{"rev-parse", "--show-toplevel"}
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	root, err := r("git", args...)
	if err != nil {
		return "", fmt.Errorf("cannot find the working tree: %w", describeFailure(err))
	}
	if root == "" {
		return "", fmt.Errorf("cannot find the working tree: git returned no path")
	}
	main, err := mainCheckout(r, dir)
	if err != nil {
		return "", err
	}
	if filepath.Clean(root) == filepath.Clean(main) {
		return "", nil
	}
	return filepath.Clean(root), nil
}

// IsNotRepo reports whether err is git saying the directory is not inside a
// repository, as opposed to git being missing or failing for another reason.
// Callers that treat "no checkout" as an ordinary answer use it to avoid also
// swallowing the failures that would make the answer wrong.
func IsNotRepo(err error) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && bytes.Contains(exit.Stderr, []byte("not a git repository"))
}
