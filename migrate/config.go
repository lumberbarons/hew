package migrate

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/local"
	"github.com/lumberbarons/hew/tracker"
)

// Config configures Open, and carries the workflow options Wrap cannot resolve
// for itself: where a checkpoint lives, what identity it binds to, and how the
// run narrates and paces itself.
//
// The target fields are Open's alone; a caller that wraps an already-open
// Target ignores them and supplies Repo and RepoKey instead. The checkpoint
// fields are shared: an already-open Checkpoint wins over CheckpointPath.
type Config struct {
	// Worktree is the directory the caller works from. Open resolves the
	// target backend and the GitHub repository from it, and never reads the
	// process's working directory or environment.
	Worktree string
	// Backend optionally pins the backend the migration writes to. Empty means
	// use the backend tracker.Detect finds in Worktree. A local tracker is a
	// valid target for Beads; Local always writes to GitHub.
	Backend tracker.Backend
	// Repo is the GitHub repository as owner/name. Empty means detect it from
	// the worktree's origin remote. A dry run with no resolveable repository
	// plans the source alone (see the workflow's dry-run rule).
	Repo string
	// Actor is the claimant identity on a local target: the login an
	// in-progress item's claim is preserved as. It is required when the target
	// is local, because the library never reads the process's environment to
	// guess one; GitHub claims are always made as the authenticated login,
	// which the backend resolves lazily.
	Actor string
	// Notice receives lock-wait notices from the local tracker. Nil discards
	// them; no library code prints.
	Notice io.Writer
	// Transport carries every GitHub API request, so a consumer's tests run a
	// migration against an isolated test server with neither the network nor a
	// `gh` process. Nil uses go-gh's default.
	Transport http.RoundTripper
	// Checkpoint is an already-open resume state. A caller that wrapped its own
	// target uses it directly; Open leaves it nil and opens CheckpointPath per
	// workflow, since the digest binds to source bytes that are only read then.
	Checkpoint Checkpoint
	// CheckpointPath is the resume-state file a workflow opens when Checkpoint
	// is nil. It is required for a real run, and for a dry run that has a
	// target to verify an existing file against.
	CheckpointPath string
	// RepoKey is the identity a checkpoint binds to, when it differs from Repo:
	// a local target binds to the tracker's stable id (`local/<id>`), because a
	// checkout's directory name is neither unique nor stable. Empty means Repo.
	RepoKey string
	// Throttle is waited between writes to stay under a backend's rate limits.
	// The wait is interruptible by the run's context.
	Throttle time.Duration
	// DryRun validates the checkpoint and the items and reports what a real run
	// would do, without writing anywhere.
	DryRun bool
	// Progress receives one line of running narration per write, for a caller
	// that renders it. Nil is silent; the workflow never prints.
	Progress func(format string, args ...any)
}

// Open opens the tracker a migration writes to and returns a Migrator for the
// beads and local-to-GitHub workflows. The backend is resolved once, from
// cfg.Worktree unless cfg.Backend pins it, and fixed for the life of the
// Migrator.
//
// A dry run is allowed to open without a target when the backend is GitHub and
// no repository can be resolved (no cfg.Repo, no github.com origin remote): the
// plan then comes from the source alone. A workflow asked to verify an existing
// checkpoint in that state refuses instead of listing work a real run would
// skip. Every other unresolved target is an InvalidInputError, and an unusable
// credential is AuthError.
func Open(ctx context.Context, cfg Config) (*Migrator, error) {
	if cfg.Worktree == "" {
		return nil, &tracker.InvalidInputError{Field: "worktree", Reason: "must name the directory to work from"}
	}
	switch cfg.Backend {
	case "", tracker.BackendLocal, tracker.BackendGitHub:
	default:
		return nil, &tracker.InvalidInputError{Field: "backend", Reason: fmt.Sprintf("unknown backend %q", cfg.Backend)}
	}

	// An explicit repository always means GitHub, as it does for every command:
	// no detection, and no local tracker consulted.
	if cfg.Backend == tracker.BackendGitHub && cfg.Repo != "" {
		return openMigrator(ctx, cfg, tracker.Target{Backend: tracker.BackendGitHub}, tracker.BackendGitHub)
	}
	target, err := tracker.Detect(cfg.Worktree)
	if err != nil {
		return nil, err
	}
	backend := cfg.Backend
	if backend == "" {
		backend = target.Backend
	}
	if backend == tracker.BackendLocal && target.Backend != tracker.BackendLocal {
		return nil, &tracker.InvalidInputError{Field: "backend", Reason: fmt.Sprintf("no local tracker in %s", cfg.Worktree)}
	}
	return openMigrator(ctx, cfg, target, backend)
}

// openMigrator completes Open for one detected target: it opens the store the
// migration writes through and records the identities the checkpoint and the
// local seal need.
func openMigrator(ctx context.Context, cfg Config, target tracker.Target, backend tracker.Backend) (*Migrator, error) {
	m := Wrap(nil, cfg)
	m.worktree = cfg.Worktree
	m.backend = backend
	repoName := cfg.Repo
	switch {
	case backend == tracker.BackendLocal:
		if cfg.Actor == "" {
			return nil, &tracker.InvalidInputError{Field: "actor", Reason: "must name the claimant identity when the target is a local tracker"}
		}
		m.backend = tracker.BackendLocal
		store, err := local.OpenWithOptions(target.Dir, local.Options{Viewer: cfg.Actor, Notice: cfg.Notice, Context: ctx})
		if err != nil {
			return nil, err
		}
		m.target = storeTarget{client: store}
		m.repoKey = "local/" + target.ID
		m.repoName = ""
		return m, nil
	case repoName == "":
		repoName = target.Repo
	}
	if repoName == "" {
		if cfg.DryRun {
			// No target: the plan comes from the source alone. A workflow that
			// has a checkpoint to verify refuses rather than guessing.
			m.repoKey = cfg.RepoKey
			return m, nil
		}
		return nil, &tracker.InvalidInputError{Field: "repo", Reason: "must be owner/name when the worktree has no github.com origin remote"}
	}
	repo, err := parseRepo(repoName)
	if err != nil {
		return nil, err
	}
	store, err := openGitHubStore(repo, cfg.Transport)
	if err != nil {
		return nil, &tracker.AuthError{Err: err}
	}
	m.target = storeTarget{client: store}
	m.repoName = repo.String()
	m.repoKey = repo.String()
	return m, nil
}

// parseRepo splits an owner/name spec.
func parseRepo(spec string) (gh.Repo, error) {
	owner, name, ok := strings.Cut(spec, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return gh.Repo{}, &tracker.InvalidInputError{Field: "repo", Reason: `must be "owner/name"`}
	}
	return gh.Repo{Owner: owner, Name: name}, nil
}

// openGitHubStore builds the GitHub client the migration writes through.
var openGitHubStore = func(repo gh.Repo, transport http.RoundTripper) (gh.IssueStore, error) {
	return gh.NewWithOptions(repo, api.ClientOptions{Transport: transport})
}

// Backend reports the backend the migration writes to. It is empty for a
// wrapped Migrator and for a targetless dry run, which resolves none.
func (m *Migrator) Backend() tracker.Backend { return m.backend }

// Repo reports the GitHub repository a Local migration will seal to, as
// owner/name. It is empty on a local target and on a targetless dry run.
func (m *Migrator) Repo() string { return m.repoName }
