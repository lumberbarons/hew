// Package pr provides hew's pull-request workflow in-process: resolve the
// issue a branch is for, compose the body the conventions prescribe, and create
// or amend the pull request through an isolated transport. Its API is
// experimental until hew 1.0.
//
// A Client is created with Open, which resolves the worktree, backend,
// repository and local branch state from the caller's inputs — never the
// process's working directory or environment — and carries typed requests,
// results, warnings and errors. Nothing here prints, exits, or imports the CLI.
package pr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/git"
	"github.com/lumberbarons/hew/tracker"
)

// Config configures Open. Its inputs mirror tracker.Config, because the pull
// request is opened against the tracker the same caller reads.
type Config struct {
	// Worktree is the directory the caller works from. Open reads its current
	// branch and upstream from here and never reads the process's working
	// directory.
	Worktree string
	// Backend optionally pins the backend. Empty means use the backend Detect
	// finds; a local tracker has no pull requests and Open refuses it with
	// NoPullRequestsError.
	Backend tracker.Backend
	// Repo is the GitHub repository as owner/name. Empty means detect it from
	// a github.com origin remote.
	Repo string
	// Actor is the identity the workflow reasons about. On GitHub the acting
	// identity is the authenticated login, so Open reads it there and this is
	// ignored.
	Actor string
	// Notice receives lock-wait and migration notices from the tracker read.
	// Nil discards them; the workflow never writes warnings itself — they ride
	// on the result.
	Notice io.Writer
	// Transport carries every GitHub API request, reads and writes alike. Nil
	// means the default HTTP client; a consumer's tests supply an httptest
	// server's transport to answer GitHub without the network or a `gh`
	// process.
	Transport http.RoundTripper
}

// Client is an open pull-request workflow for one checkout and repository. It
// is safe for concurrent use.
type Client struct {
	tracker *tracker.Tracker
	store   gh.IssueStore
	prs     gh.PullRequests
	repo    gh.Repo
	// worktree is the checkout a Client built by Open reads branch state from;
	// empty on a wrapped Client, whose caller supplied state itself.
	worktree string
	state    git.State
}

// Open opens the tracker a pull request is composed against and the GitHub
// client it is created through. The backend is resolved once and fixed for the
// life of the Client; a local tracker has no pull requests, so Open refuses it
// rather than deferring the failure to the first operation.
func Open(ctx context.Context, cfg Config) (*Client, error) {
	tr, err := tracker.Open(ctx, tracker.Config{
		Worktree:  cfg.Worktree,
		Backend:   cfg.Backend,
		Repo:      cfg.Repo,
		Actor:     cfg.Actor,
		Notice:    cfg.Notice,
		Transport: cfg.Transport,
	})
	if err != nil {
		return nil, err
	}
	return openClient(ctx, tr, cfg.Worktree, cfg.Transport)
}

// Wrap makes a Client from an already-open store and pull-request client: the
// seam hew's own CLI uses, where it has opened the store once and already knows
// the branch state. External callers use Open, which resolves all of that.
func Wrap(store gh.IssueStore, prs gh.PullRequests, repo gh.Repo, state git.State) *Client {
	return &Client{
		tracker: tracker.WrapAs(store, tracker.BackendGitHub, ""),
		store:   store,
		prs:     prs,
		repo:    repo,
		state:   state,
	}
}

// openClient completes an Open from an already-opened tracker: the same backend
// decision and repository target, plus the pull-request half of the GitHub
// client and the branch the request is opened from.
func openClient(ctx context.Context, tr *tracker.Tracker, worktree string, transport http.RoundTripper) (*Client, error) {
	if tr.Backend() != tracker.BackendGitHub {
		return nil, &NoPullRequestsError{}
	}
	repo, err := parseRepo(tr.Repo())
	if err != nil {
		return nil, err
	}
	prs, err := gh.NewWithTransport(repo, transport)
	if err != nil {
		return nil, asAuthError(err)
	}
	state, err := git.CurrentFor(worktree)
	if err != nil {
		return nil, err
	}
	return &Client{tracker: tr, prs: prs, repo: repo, worktree: worktree, state: state}, nil
}

// asAuthError turns the GitHub client's auth failure into the tracker
// vocabulary every package shares, so a consumer matches one type on either
// side of the boundary.
func asAuthError(err error) error {
	var auth *gh.AuthError
	if errors.As(err, &auth) {
		return &tracker.AuthError{Err: auth}
	}
	return err
}

// parseRepo splits the resolved owner/name target into the API layer's
// repository value.
func parseRepo(spec string) (gh.Repo, error) {
	owner, name, ok := strings.Cut(spec, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return gh.Repo{}, &tracker.InvalidInputError{Field: "repo", Reason: `must be "owner/name"`}
	}
	return gh.Repo{Owner: owner, Name: name}, nil
}

// identity is the acting identity the workflow reasons about: the tracker's
// when Open resolved one, the store's viewer when the CLI wrapped a store.
func (c *Client) identity(ctx context.Context) (string, error) {
	if actor := c.tracker.Identity(); actor != "" {
		return actor, nil
	}
	if c.store == nil {
		return "", nil
	}
	actor, err := c.store.Viewer(ctx)
	if err != nil {
		return "", asAuthError(err)
	}
	return actor, nil
}

// prepared carries the shared preconditions every operation needs: the head
// GitHub knows, the issues the pull request closes, the acting identity and the
// repository-side state of the head.
type prepared struct {
	head   string
	actor  string
	linked []tracker.Issue
	prCtx  gh.PRContext
}

// prepare resolves the branch, the linked issues and the open pull request on
// the head. The order is the workflow's: a branch GitHub cannot see is refused
// before a single issue is read, and the closing set is settled before the
// repository is asked about an existing pull request.
func (c *Client) prepare(ctx context.Context, requested []int) (prepared, error) {
	if c.prs == nil {
		return prepared{}, &NoPullRequestsError{}
	}
	state, err := c.branchState()
	if err != nil {
		return prepared{}, err
	}
	if !state.Pushed {
		return prepared{}, &NoUpstreamError{Branch: state.Branch}
	}
	// Everything from here on is about the branch GitHub can see, which is not
	// always the one checked out locally — the head it rejects, the head it
	// matches an existing pull request by, and the name a reviewer reads.
	head := state.Head()

	// The composed body quotes the linked issue's What/Why sections, so this
	// snapshot asks for the text that list-shaped reads omit by default.
	snapshot, err := c.tracker.Snapshot(ctx, tracker.SnapshotOptions{Bodies: true})
	if err != nil {
		return prepared{}, err
	}
	actor, err := c.identity(ctx)
	if err != nil {
		return prepared{}, err
	}
	linked, err := linkedIssues(snapshot.Issues(), head, actor, requested)
	if err != nil {
		return prepared{}, err
	}
	prCtx, err := c.prs.PullRequestContext(ctx, head)
	if err != nil {
		return prepared{}, asAuthError(err)
	}
	return prepared{head: head, actor: actor, linked: linked, prCtx: prCtx}, nil
}

// branchState is the checkout's branch state for one operation. A Client built
// by Open lives as long as its caller keeps it, and the branch it was opened on
// can be pushed or replaced in the meantime, so the state is read again from
// the worktree rather than trusted from Open. A wrapped Client carries the
// state its caller supplied.
func (c *Client) branchState() (git.State, error) {
	if c.worktree == "" {
		return c.state, nil
	}
	return git.CurrentFor(c.worktree)
}

// repoName is the repository as owner/name, the form messages and the
// head-repository comparison use.
func (c *Client) repoName() string { return c.repo.String() }

// invalid is the library's typed refusal for a request field the caller got
// wrong, with the field named in library rather than flag terms.
func invalid(field, format string, args ...any) error {
	return &tracker.InvalidInputError{Field: field, Reason: fmt.Sprintf(format, args...)}
}
