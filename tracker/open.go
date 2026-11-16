package tracker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/cli/go-gh/v2/pkg/repository"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/git"
	"github.com/lumberbarons/hew/internal/local"
)

// Config configures Open.
type Config struct {
	// Worktree is the directory the caller works from. Open answers every
	// path and worktree question from it and never reads the process working
	// directory.
	Worktree string
	// Backend optionally pins the backend. Empty means use the backend Detect
	// finds. BackendGitHub opens GitHub even when the worktree has a local
	// tracker (the CLI's --repo); BackendLocal refuses to fall back to GitHub
	// when the tracker is gone.
	Backend Backend
	// Repo is the GitHub repository as owner/name. Empty means detect it from
	// a github.com origin remote. Ignored by a local tracker.
	Repo string
	// Actor is the claimant identity. Empty means the backend's default: the
	// OS login qualified by the worktree on a local tracker, the authenticated
	// login on GitHub. Ignored on GitHub, where every claim is the
	// authenticated login; read Identity to see the effective claimant.
	// As(actor) changes it after Open on a local tracker.
	Actor string
	// Notice receives lock-wait and migration notices. Nil discards them; the
	// tracker never writes to stderr.
	Notice io.Writer
}

// Tracker is an open tracker: one backend, one worktree, and the identity
// claims are made as. As shares the open store; a Tracker is otherwise not
// copied.
type Tracker struct {
	backend Backend
	store   gh.IssueStore
	actor   string
}

// Open opens the tracker for cfg.Worktree. The backend is detected once, from
// that directory, unless cfg.Backend pins it, and is fixed for the life of the
// Tracker: a local tracker deleted underneath a long-lived process is not
// silently replaced by the repository's GitHub issues.
func Open(ctx context.Context, cfg Config) (*Tracker, error) {
	if cfg.Worktree == "" {
		return nil, &InvalidInputError{Field: "worktree", Reason: "must name the directory to work from"}
	}
	switch cfg.Backend {
	case "", BackendLocal, BackendGitHub:
	default:
		return nil, &InvalidInputError{Field: "backend", Reason: fmt.Sprintf("unknown backend %q", cfg.Backend)}
	}
	if cfg.Backend == BackendGitHub && cfg.Repo != "" {
		// An explicit GitHub target needs no detection: --repo always means
		// GitHub, even in a checkout with a local tracker or a damaged config.
		return openGitHub(ctx, cfg, Target{Backend: BackendGitHub})
	}
	target, err := Detect(cfg.Worktree)
	if err != nil {
		return nil, err
	}
	backend := cfg.Backend
	if backend == "" {
		backend = target.Backend
	}
	if backend == BackendLocal {
		if target.Backend != BackendLocal {
			return nil, &InvalidInputError{Field: "backend", Reason: fmt.Sprintf("no local tracker in %s", cfg.Worktree)}
		}
		return openLocal(ctx, cfg, target)
	}
	return openGitHub(ctx, cfg, target)
}

// WrapAs makes a Tracker around an already-open store, as actor: the seam
// hew's own CLI uses for writes and for the reads it delegates to Tracker,
// where it opens the store once for every command and already knows which
// backend it opened. External callers use Open, which also resolves the
// worktree and the backend. A read never resolves the actor, so it may be
// empty here.
//
// An empty actor defers to the store's Viewer, read when an operation first
// needs the identity rather than at WrapAs. That is what keeps the CLI's
// identity rules — HEW_USER on a local tracker, the authenticated login on
// GitHub — outside this package while still letting a command refuse a claim
// before it has paid for the lookup: a refusal can name the claimant without
// the acting identity, and only a claim has to know who is acting. Identity
// reports the actor given, empty when it was deferred.
func WrapAs(store gh.IssueStore, backend Backend, actor string) *Tracker {
	return &Tracker{backend: backend, store: store, actor: actor}
}

// openLocal opens the tracker with the actor the caller named, or the OS login
// qualified by the worktree when none was given. The identity never comes from
// HEW_USER: that is the CLI's spelling of the same thing.
func openLocal(ctx context.Context, cfg Config, target Target) (*Tracker, error) {
	actor := cfg.Actor
	if actor == "" {
		var err error
		if actor, err = defaultActor(cfg.Worktree); err != nil {
			return nil, err
		}
	}
	store, err := local.OpenWithOptions(target.Dir, local.Options{
		Viewer:  actor,
		Notice:  cfg.Notice,
		Context: ctx,
	})
	if err != nil {
		return nil, asWriteError(err)
	}
	return &Tracker{backend: BackendLocal, store: store, actor: actor}, nil
}

// openGitHub builds the client and reads the authenticated login, which is the
// only claim identity GitHub has. cfg.Actor is deliberately ignored.
func openGitHub(ctx context.Context, cfg Config, target Target) (*Tracker, error) {
	repo, err := cfg.githubRepo(target)
	if err != nil {
		return nil, err
	}
	store, err := newGitHub(repo)
	if err != nil {
		return nil, asAuthError(err)
	}
	login, err := store.Viewer(ctx)
	if err != nil {
		return nil, asAuthError(err)
	}
	return &Tracker{backend: BackendGitHub, store: store, actor: login}, nil
}

// newGitHub builds the GitHub client. Tests replace it to avoid credentials
// and the network.
var newGitHub = func(repo gh.Repo) (gh.IssueStore, error) { return gh.New(repo) }

// githubRepo resolves the GitHub repository from the explicit spec, or from
// the origin remote of the worktree's main checkout.
func (c Config) githubRepo(target Target) (gh.Repo, error) {
	if c.Repo != "" {
		owner, name, ok := strings.Cut(c.Repo, "/")
		if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
			return gh.Repo{}, &InvalidInputError{Field: "repo", Reason: `must be "owner/name"`}
		}
		return gh.Repo{Owner: owner, Name: name}, nil
	}
	// Detect already read the origin remote for its report; reuse it rather
	// than asking git twice.
	if target.Repo != "" {
		owner, name, ok := strings.Cut(target.Repo, "/")
		if ok && owner != "" && name != "" {
			return gh.Repo{Owner: owner, Name: name}, nil
		}
	}
	if target.MainCheckout == "" {
		return gh.Repo{}, &InvalidInputError{Field: "repo", Reason: "must be owner/name when the worktree is not a git checkout"}
	}
	remote, err := git.RemoteURL(target.MainCheckout)
	if err != nil {
		return gh.Repo{}, err
	}
	if remote == "" {
		return gh.Repo{}, &InvalidInputError{Field: "repo", Reason: "must be owner/name when the repository has no origin remote"}
	}
	if !remoteIsURL(remote) {
		return gh.Repo{}, &InvalidInputError{Field: "repo", Reason: fmt.Sprintf("origin %q is not a URL; set Repo explicitly", remote)}
	}
	parsed, err := repository.Parse(remote)
	if err != nil {
		return gh.Repo{}, &InvalidInputError{Field: "repo", Reason: fmt.Sprintf("cannot read an owner/name from the origin remote %q", remote)}
	}
	if parsed.Host != "github.com" {
		return gh.Repo{}, &InvalidInputError{Field: "repo", Reason: fmt.Sprintf("origin %q is a %q remote, not github.com; set Repo explicitly", remote, parsed.Host)}
	}
	return gh.Repo{Owner: parsed.Owner, Name: parsed.Name}, nil
}

// remoteIsURL reports whether a git remote is a URL rather than a path on
// disk. repository.Parse accepts a bare owner/name as a spec, which would turn
// a local-path remote into a repository the caller never named.
func remoteIsURL(remote string) bool {
	if strings.HasPrefix(remote, "git@") {
		return true
	}
	for _, scheme := range []string{"ssh:", "git+ssh:", "git:", "http:", "git+https:", "https:"} {
		if strings.HasPrefix(remote, scheme) {
			return true
		}
	}
	return false
}

// defaultActor is the identity rule with the worktree explicit: the OS login,
// qualified by the linked worktree containing dir. HEW_USER is not consulted.
func defaultActor(dir string) (string, error) {
	root, err := git.LinkedWorktree(dir)
	if err != nil {
		return "", err
	}
	return local.ViewerFor(os.Getenv("USER"), root), nil
}

// asAuthError turns the GitHub client's auth failure into the library's typed
// error, so a consumer matches one type on either side of the boundary. The
// store's error is the cause, keeping the message that names `gh auth login`
// and the credential failure behind it.
func asAuthError(err error) error {
	var auth *gh.AuthError
	if errors.As(err, &auth) {
		return &AuthError{Err: auth}
	}
	return err
}

// Backend reports the backend Open selected.
func (t *Tracker) Backend() Backend { return t.backend }

// Identity reports the claimant identity claims are made as: the actor given
// to Open or As on a local tracker, the authenticated login on GitHub.
func (t *Tracker) Identity() string { return t.actor }

// As returns a tracker whose claims are made as actor, sharing this tracker's
// open store. It is a cheap clone; the receiver is unchanged. On GitHub the
// claim identity is the authenticated login, so As returns a tracker whose
// Identity is still that login. An empty actor, or the current identity,
// returns the receiver.
func (t *Tracker) As(actor string) *Tracker {
	if actor == "" || actor == t.actor {
		return t
	}
	clone := *t
	if store, ok := t.store.(*local.Store); ok {
		clone.store = store.As(actor)
		clone.actor = actor
	}
	return &clone
}
