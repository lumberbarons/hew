// Package gh is the thin GitHub API layer: one interface the commands are
// written against, and a go-gh-backed implementation that reuses gh's
// stored credentials.
package gh

import (
	"context"
	"errors"
	"fmt"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/lumberbarons/hew/internal/model"
)

// Repo identifies the target repository.
type Repo struct {
	Owner string
	Name  string
}

func (r Repo) String() string { return r.Owner + "/" + r.Name }

// Label mirrors a repository label for init's bootstrap pass.
type Label struct {
	Name        string
	Color       string
	Description string
}

// IssueState is a GitHub issue state, used to filter list queries. Typed so
// callers can't misspell the wire enum.
type IssueState string

const (
	StateOpen   IssueState = "OPEN"
	StateClosed IssueState = "CLOSED"
)

// CloseReason is why an issue is closed, matching GitHub's
// IssueClosedStateReason enum.
type CloseReason string

const (
	CloseCompleted  CloseReason = "COMPLETED"
	CloseNotPlanned CloseReason = "NOT_PLANNED"
	CloseDuplicate  CloseReason = "DUPLICATE"
)

// PullRequest carries the metadata and body needed to create or revise a PR.
type PullRequest struct {
	Number int
	URL    string
	Draft  bool
	Title  string
	Body   string
	Base   string
	// HeadRepository is owner/name; empty means the head repository is unknown.
	HeadRepository string
}

// NewPullRequest is the pr command's creation payload.
type NewPullRequest struct {
	Title string
	Body  string
	// Head and Base are branch names in this repo; cross-fork heads are out
	// of scope (the workflow branches in place).
	Head  string
	Base  string
	Draft bool
}

// PRContext is the repo-side state needed before creating or revising a PR.
// Existing is only a sample: amendment also requires OpenCount == 1 and a
// matching head repository. Creation can refuse on any existing match.
type PRContext struct {
	DefaultBranch string
	Existing      *PullRequest
	// OpenCount includes all open PRs with this head name, including fork heads.
	OpenCount int
}

// Client is the full GitHub API surface; the tests fake it. It is the union
// of the two halves below so a backend with no pull requests (the local
// store) can implement IssueStore alone.
type Client interface {
	IssueStore
	PullRequests
}

// IssueStore is the issue-tracking half of the API: everything every command
// but `pr` needs.
type IssueStore interface {
	// Viewer returns the identity this backend attributes new work to: the
	// authenticated login on GitHub, the agent's local identity in a local
	// store (see local.DefaultViewer). It is what `start` assigns and what
	// `pr` looks for when it infers the claimed issue.
	Viewer(ctx context.Context) (string, error)
	// ListIssues fetches all issues in the given states with body, labels,
	// assignees, parent, sub-issues, and blockers, paginating the outer
	// connection. Nested connections are capped (see query). Relationships,
	// totals and labels are part of this view, not just GetIssue's: the
	// list-shaped commands read nothing else.
	ListIssues(ctx context.Context, states []IssueState) ([]model.Issue, error)
	// GetIssue fetches one issue in any state, including body, creation
	// time and recent comments.
	GetIssue(ctx context.Context, number int) (model.Issue, error)
	// SearchIssues runs a repo-scoped text search over issues in both
	// states. Results are in the backend's relevance order — GitHub's
	// best-match, the local store's title-first — and callers must not
	// depend on a more specific order. Results are capped (see searchCap);
	// total is the backend's full match count, or len(issues) when it has
	// no server-side count, so a caller warns about truncation only when
	// total exceeds what came back.
	SearchIssues(ctx context.Context, terms string) (issues []model.Issue, total int, err error)
	CreateIssue(ctx context.Context, title, body string, labels []string) (model.Issue, error)
	EditTitle(ctx context.Context, number int, title string) error
	EditBody(ctx context.Context, number int, body string) error
	AddLabels(ctx context.Context, number int, labels []string) error
	RemoveLabel(ctx context.Context, number int, label string) error
	// AddAssignee records login as given. Whether the login resolves to a
	// real user is a server concern — GitHub validates it, a local store has
	// no user directory — so it is not part of this contract.
	AddAssignee(ctx context.Context, number int, login string) error
	RemoveAssignees(ctx context.Context, number int, logins []string) error
	Comment(ctx context.Context, number int, body string) error
	// Every method below identifies issues by number; the GraphQL-backed
	// implementations resolve node IDs internally, so callers never juggle
	// two identifier families.
	CloseIssue(ctx context.Context, number int, reason CloseReason) error
	// ReopenIssue reopens a closed issue. There is no reason enum to pass:
	// GitHub records the reopen itself as the state reason.
	ReopenIssue(ctx context.Context, number int) error
	// AddBlockedBy marks number as blocked by blockingNumber.
	AddBlockedBy(ctx context.Context, number, blockingNumber int) error
	RemoveBlockedBy(ctx context.Context, number, blockingNumber int) error
	AddSubIssue(ctx context.Context, parentNumber, childNumber int, replaceParent bool) error
	RemoveSubIssue(ctx context.Context, parentNumber, childNumber int) error
	ListLabels(ctx context.Context) ([]Label, error)
	CreateLabel(ctx context.Context, label Label) error
}

// ErrClaimConflict marks a Claimer's refusal because another writer claimed
// the issue first. Start turns it into the same exit codes as a claim the
// pre-read saw: 3 when someone else holds it, 5 when the identity matches.
var ErrClaimConflict = errors.New("issue is already claimed")

// Claimer is an optional IssueStore capability: an atomic claim, for a
// backend that can make the write conditional. GitHub has no conditional
// writes, so its client does not implement it and Start keeps the
// check-then-act guard with a re-read after claiming; the local store holds
// an exclusive lock, so it can decide and write in one step.
type Claimer interface {
	// ClaimIssue assigns login and adds the in-progress label in one locked
	// write. force replaces an existing claim. It fails with
	// ErrClaimConflict, leaving the issue unchanged, when another claimant
	// won between Start's read and this write.
	ClaimIssue(ctx context.Context, number int, login string, force bool) error
}

// ClaimReleaser is an optional atomic release capability. GitHub uses
// check-then-act; local stores can guard ownership and clear the claim together.
type ClaimReleaser interface {
	// ReleaseIssueClaim removes all assignees and in-progress labels. Without
	// force, login must be the sole assignee at the time of the write. An
	// unclaimed issue is an error, including with force. A refusal changes nothing.
	ReleaseIssueClaim(ctx context.Context, number int, login string, force bool) error
}

// PullRequests is the pull-request half of the API, used only by `pr`.
type PullRequests interface {
	// PullRequestContext fetches the default branch and any open pull
	// request whose head is the given branch.
	PullRequestContext(ctx context.Context, head string) (PRContext, error)
	CreatePullRequest(ctx context.Context, pr NewPullRequest) (PullRequest, error)
	// UpdatePullRequest changes only the title and body, preserving draft/base state.
	UpdatePullRequest(ctx context.Context, number int, title, body string) error
}

// AuthError marks failures that mean "run gh auth login", mapped to a
// distinct exit code by main.
type AuthError struct{ Err error }

func (e *AuthError) Error() string {
	return fmt.Sprintf("not authenticated (run `gh auth login`): %v", e.Err)
}

func (e *AuthError) Unwrap() error { return e.Err }

// wrapErr converts 401s into AuthError so main can exit 4.
func wrapErr(err error) error {
	if err == nil {
		return nil
	}
	var httpErr *api.HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == 401 {
		return &AuthError{Err: err}
	}
	return err
}
