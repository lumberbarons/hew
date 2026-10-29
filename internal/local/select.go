package local

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/lumberbarons/hew/internal/git"
)

// MainDir is where the tracker lives for the checkout containing dir (the
// working directory when empty): the `.hew` directory at the main checkout, so
// every linked worktree of one repository shares it.
func MainDir(dir string) (string, error) { return mainDir(git.MainCheckout, dir) }

func mainDir(mainCheckout func(string) (string, error), dir string) (string, error) {
	main, err := mainCheckout(dir)
	if err != nil {
		return "", err
	}
	return filepath.Join(main, DirName), nil
}

// Locate reports the tracker directory when the repository around dir is
// configured to track issues locally. No `.hew/config.json` means the repo is
// not local, and neither does a config that names another backend (a migrated
// repo keeps its local issues as an archive), a `.hew` that is not a
// directory, or a directory git says is not in a checkout at all — those are
// left for the GitHub path to report in its own terms. Anything else that goes
// wrong is an error: git failing for another reason, or a config that exists
// but cannot be read. Guessing GitHub there would send writes to the wrong
// tracker.
func Locate(dir string) (trackerDir string, ok bool, err error) {
	// Backend selection runs before every command, so answer it from the
	// filesystem when the filesystem already knows there is nothing to find:
	// a checkout whose main working tree has no .hew cannot be local. Git
	// stays the authority for what this cannot read, and whenever a .hew does
	// exist, because a wrong answer there would hide a real tracker.
	if main, known := mainCheckoutWithoutGit(dir); known {
		if info, statErr := os.Stat(filepath.Join(main, DirName)); statErr != nil || !info.IsDir() {
			return "", false, nil
		}
	}
	return locate(git.MainCheckout, dir)
}

// mainCheckoutWithoutGit finds the main checkout from the filesystem alone. A
// false result means git has to answer: dir is not inside a checkout, or the
// checkout uses a layout this does not understand (a GIT_DIR or work-tree
// override, a submodule, a separate git dir, an unreadable .git). Unknown is
// always preferred to a guess.
func mainCheckoutWithoutGit(dir string) (string, bool) {
	if os.Getenv("GIT_DIR") != "" || os.Getenv("GIT_WORK_TREE") != "" || os.Getenv("GIT_COMMON_DIR") != "" || os.Getenv("GIT_CEILING_DIRECTORIES") != "" {
		return "", false
	}
	if dir == "" {
		var err error
		if dir, err = os.Getwd(); err != nil {
			return "", false
		}
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	for {
		info, err := os.Lstat(filepath.Join(root, ".git"))
		switch {
		case err == nil && info.IsDir():
			return root, true
		case err == nil && info.Mode().IsRegular():
			return mainCheckoutFromGitFile(root)
		case err == nil:
			return "", false
		case !errors.Is(err, fs.ErrNotExist):
			return "", false
		}
		parent := filepath.Dir(root)
		if parent == root {
			return "", false
		}
		root = parent
	}
}

// mainCheckoutFromGitFile reads a linked worktree's .git file, which names
// <main>/.git/worktrees/<name>. Anything else — a submodule's gitdir under
// .git/modules, a separate git dir — is left to git.
func mainCheckoutFromGitFile(root string) (string, bool) {
	raw, err := os.ReadFile(filepath.Join(root, ".git"))
	if err != nil {
		return "", false
	}
	line, ok := strings.CutPrefix(strings.TrimSpace(string(raw)), "gitdir:")
	if !ok {
		return "", false
	}
	gitdir := strings.TrimSpace(line)
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(root, gitdir)
	}
	gitdir = filepath.Clean(gitdir)
	marker := string(filepath.Separator) + ".git" + string(filepath.Separator) + "worktrees" + string(filepath.Separator)
	at := strings.LastIndex(gitdir, marker)
	if at < 0 {
		return "", false
	}
	main := gitdir[:at]
	if main == "" {
		return "", false
	}
	// A standard linked worktree's gitdir is under the main checkout's .git.
	// Checking that catches a --separate-git-dir layout the pattern would
	// otherwise misread.
	if info, err := os.Stat(filepath.Join(main, ".git")); err != nil || !info.IsDir() {
		return "", false
	}
	return main, true
}

func locate(mainCheckout func(string) (string, error), dir string) (string, bool, error) {
	tracker, err := mainDir(mainCheckout, dir)
	switch {
	case git.IsNotRepo(err):
		return "", false, nil
	case err != nil:
		return "", false, err
	}
	if info, err := os.Stat(tracker); err != nil || !info.IsDir() {
		// Absent, or something else named .hew: no opt-in to local either way.
		return "", false, nil
	}
	cfg, err := ReadConfig(tracker)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", false, nil
	case err != nil:
		return "", false, err
	}
	if cfg.Backend != BackendLocal {
		return "", false, nil
	}
	return tracker, true, nil
}
