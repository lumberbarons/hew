package tracker

import (
	"path/filepath"

	"github.com/lumberbarons/hew/internal/git"
	"github.com/lumberbarons/hew/internal/local"
)

// Backend names the tracker implementation that owns a repository.
type Backend string

const (
	// BackendLocal is the JSONL tracker in .hew at the main checkout.
	BackendLocal Backend = "local"
	// BackendGitHub is GitHub Issues for the repository's remote.
	BackendGitHub Backend = "github"
)

// Target is where a repository's issues live.
type Target struct {
	// Backend is the implementation that owns the repository.
	Backend Backend
	// Dir is the local tracker directory (the .hew directory) when Backend is
	// BackendLocal; empty for GitHub.
	Dir string
	// MainCheckout is the repository's main working tree, empty when the
	// directory is not inside a git checkout.
	MainCheckout string
}

// Detect reports which backend owns the repository containing dir and where
// its state lives. The worktree is dir, never the process working directory.
// It reads the filesystem and git; git's own environment (GIT_DIR and
// friends) is inherited, as it is for any git command.
func Detect(dir string) (Target, error) {
	if dir == "" {
		return Target{}, &InvalidInputError{Field: "dir", Reason: "must name the directory to inspect"}
	}
	trackerDir, _, isLocal, err := local.LocateConfig(dir)
	if err != nil {
		return Target{}, err
	}
	if isLocal {
		// LocateConfig puts the tracker directly under the main checkout, so
		// its parent is that checkout without a second git call.
		return Target{Backend: BackendLocal, Dir: trackerDir, MainCheckout: filepath.Dir(trackerDir)}, nil
	}
	main, err := git.MainCheckout(dir)
	switch {
	case git.IsNotRepo(err):
		return Target{Backend: BackendGitHub}, nil
	case err != nil:
		return Target{}, err
	}
	return Target{Backend: BackendGitHub, MainCheckout: main}, nil
}
