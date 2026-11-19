package tracker

import (
	"path/filepath"

	"github.com/cli/go-gh/v2/pkg/repository"

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

// Target is where a repository's issues live: the read-only backend report a
// caller gets before opening anything.
type Target struct {
	// Backend is the implementation that owns the repository.
	Backend Backend `json:"backend"`
	// Repo is the GitHub repository as owner/name, resolved from the
	// checkout's origin remote. It is empty for a local tracker and when the
	// checkout has no github.com origin to read.
	Repo string `json:"repo"`
	// Dir is the local tracker directory (the .hew directory) when Backend is
	// BackendLocal; empty for GitHub.
	Dir string `json:"dir"`
	// MainCheckout is the repository's main working tree, empty when the
	// directory is not inside a git checkout.
	MainCheckout string `json:"mainCheckout"`
	// ID is the local tracker's stable identity from its config, the value
	// batch checkpoints bind to. Empty on GitHub.
	ID string `json:"id"`
}

// Detect reports which backend owns the repository containing dir and where
// its state lives. The worktree is dir, never the process working directory.
// It reads the filesystem and git; git's own environment (GIT_DIR and
// friends) is inherited, as it is for any git command.
//
// Detect is read-only and opens no store or client. Repo is left empty rather
// than guessed when the origin remote is missing or not a github.com URL;
// Open, which has to name one, reports that in its own terms.
func Detect(dir string) (Target, error) {
	if dir == "" {
		return Target{}, &InvalidInputError{Field: "dir", Reason: "must name the directory to inspect"}
	}
	trackerDir, cfg, isLocal, err := local.LocateConfig(dir)
	if err != nil {
		return Target{}, err
	}
	if isLocal {
		// LocateConfig puts the tracker directly under the main checkout, so
		// its parent is that checkout without a second git call.
		return Target{
			Backend:      BackendLocal,
			Dir:          trackerDir,
			MainCheckout: filepath.Dir(trackerDir),
			ID:           cfg.ID,
		}, nil
	}
	main, err := git.MainCheckout(dir)
	switch {
	case git.IsNotRepo(err):
		return Target{Backend: BackendGitHub}, nil
	case err != nil:
		return Target{}, err
	}
	return Target{Backend: BackendGitHub, Repo: githubRepoAt(main), MainCheckout: main}, nil
}

// githubRepoAt reads owner/name from a checkout's origin remote without
// turning an unreadable or non-GitHub remote into a detection failure: the
// backend is still GitHub, and Open is the operation that must refuse a
// missing repository.
func githubRepoAt(main string) string {
	remote, err := git.RemoteURL(main)
	if err != nil || remote == "" || !remoteIsURL(remote) {
		return ""
	}
	parsed, err := repository.Parse(remote)
	if err != nil || parsed.Host != "github.com" || parsed.Owner == "" || parsed.Name == "" {
		return ""
	}
	return parsed.Owner + "/" + parsed.Name
}
