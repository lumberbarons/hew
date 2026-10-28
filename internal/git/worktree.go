package git

import (
	"fmt"
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
		return "", fmt.Errorf("cannot find the main checkout: %w", err)
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
