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

// PullRequest is a pull request, reduced to what the pr command reports.
type PullRequest struct {
	Number int
	URL    string
	Draft  bool
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

// PRContext is the repo-side state pr needs before creating: the branch to
// target, and the open pull request already on the head branch, if any.
// Both come from one query — a second PR on a branch is the failure the
// command must name, not discover from a 422.
type PRContext struct {
	DefaultBranch string
	Existing      *PullRequest
}

// Client is the API surface the commands use; the tests fake it.
type Client interface {
	// Viewer returns the authenticated user's login.
	Viewer(ctx context.Context) (string, error)
	// ListIssues fetches all issues in the given states with body, labels,
	// assignees, parent, sub-issues, and blockers, paginating the outer
	// connection. Nested connections are capped (see query).
	ListIssues(ctx context.Context, states []IssueState) ([]model.Issue, error)
	// GetIssue fetches one issue in any state, including body and recent
	// comments.
	GetIssue(ctx context.Context, number int) (model.Issue, error)
	// SearchIssues runs a repo-scoped text search over issues in both
	// states, in the API's best-match order. Results are capped (see
	// searchCap); total is the server's full match count so callers can
	// warn on truncation.
	SearchIssues(ctx context.Context, terms string) (issues []model.Issue, total int, err error)
	CreateIssue(ctx context.Context, title, body string, labels []string) (model.Issue, error)
	EditTitle(ctx context.Context, number int, title string) error
	EditBody(ctx context.Context, number int, body string) error
	AddLabels(ctx context.Context, number int, labels []string) error
	RemoveLabel(ctx context.Context, number int, label string) error
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
	// PullRequestContext fetches the default branch and any open pull
	// request whose head is the given branch.
	PullRequestContext(ctx context.Context, head string) (PRContext, error)
	CreatePullRequest(ctx context.Context, pr NewPullRequest) (PullRequest, error)
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
