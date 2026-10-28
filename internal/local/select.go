package local

import (
	"errors"
	"io/fs"
	"path/filepath"

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
// repo keeps its local issues as an archive). A directory git cannot place in
// a checkout has no tracker either, so that is also "not local" and left for
// the GitHub path to report in its own terms. A config that exists but cannot
// be read is an error: guessing GitHub there would send writes to the wrong
// tracker.
func Locate(dir string) (trackerDir string, ok bool, err error) {
	return locate(git.MainCheckout, dir)
}

func locate(mainCheckout func(string) (string, error), dir string) (string, bool, error) {
	tracker, err := mainDir(mainCheckout, dir)
	if err != nil {
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
