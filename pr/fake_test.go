package pr

import (
	"context"
	"fmt"
	"time"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/git"
	"github.com/lumberbarons/hew/internal/model"
)

// fakeStore is an in-memory gh.IssueStore limited to what a pull-request
// workflow reads: the open issue set and the acting identity. The embedded nil
// interface supplies the rest of the contract and panics if a test starts
// depending on a write.
type fakeStore struct {
	gh.IssueStore
	issues    []model.Issue
	viewer    string
	listErr   error
	viewerErr error
}

func (f *fakeStore) Viewer(context.Context) (string, error) {
	if f.viewerErr != nil {
		return "", f.viewerErr
	}
	return f.viewer, nil
}

func (f *fakeStore) ListIssues(_ context.Context, states []gh.IssueState) ([]model.Issue, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []model.Issue
	for _, issue := range f.issues {
		for _, state := range states {
			if string(state) == issue.State {
				out = append(out, issue)
				break
			}
		}
	}
	return out, nil
}

// update records an amendment's write.
type update struct {
	number int
	title  string
	body   string
}

// fakePRs is an in-memory gh.PullRequests: the repository's default branch and
// existing pull request, plus every create and update the workflow made.
type fakePRs struct {
	defaultBranch string
	existing      *gh.PullRequest
	openCount     int
	countSet      bool
	contextErr    error
	createErr     error
	updateErr     error
	contexts      []string
	created       []gh.NewPullRequest
	updated       []update
}

func (f *fakePRs) PullRequestContext(_ context.Context, head string) (gh.PRContext, error) {
	f.contexts = append(f.contexts, head)
	if f.contextErr != nil {
		return gh.PRContext{}, f.contextErr
	}
	count := f.openCount
	if !f.countSet && count == 0 && f.existing != nil {
		count = 1
	}
	return gh.PRContext{DefaultBranch: f.defaultBranch, Existing: f.existing, OpenCount: count}, nil
}

func (f *fakePRs) CreatePullRequest(_ context.Context, req gh.NewPullRequest) (gh.PullRequest, error) {
	if f.createErr != nil {
		return gh.PullRequest{}, f.createErr
	}
	f.created = append(f.created, req)
	return gh.PullRequest{
		Number: 501, URL: "https://github.com/o/r/pull/501",
		Draft: req.Draft, Title: req.Title, Body: req.Body, Base: req.Base,
	}, nil
}

func (f *fakePRs) UpdatePullRequest(_ context.Context, number int, title, body string) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	f.updated = append(f.updated, update{number: number, title: title, body: body})
	if f.existing != nil && f.existing.Number == number {
		// Mutations really mutate, so a second amendment sees the first one's
		// body exactly as the API would return it.
		f.existing.Title, f.existing.Body = title, body
	}
	return nil
}

// fixture is a triaged open issue. The CLI fake's shape is mirrored here so a
// scenario reads the same on both sides of the package boundary.
func fixture(n int, title string, labels ...string) model.Issue {
	return model.Issue{
		ID: fmt.Sprintf("ID%d", n), Number: n, Title: title, State: "OPEN",
		Labels: labels, CreatedAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
	}
}

// claimed is a fixture the fake's viewer holds.
func claimed(n int, title string, labels ...string) model.Issue {
	issue := fixture(n, title, append(labels, model.InProgressLabel)...)
	issue.Assignees = []string{"me"}
	return issue
}

func subIssueOf(issue model.Issue, epic int) model.Issue {
	issue.Parent = &model.Ref{Number: epic, State: "OPEN"}
	return issue
}

// wrapped builds a Client over the fake halves, on a pushed conventional
// branch unless the test replaces the state.
func wrapped(issues ...model.Issue) (*Client, *fakeStore, *fakePRs) {
	store := &fakeStore{issues: issues, viewer: "me"}
	prs := &fakePRs{defaultBranch: "main"}
	client := Wrap(store, prs, gh.Repo{Owner: "o", Name: "r"},
		git.State{Branch: "feat/x", Pushed: true})
	return client, store, prs
}

// sections is the common Testing-only body input.
func sections(what, why, testing string) Sections {
	return Sections{What: what, Why: why, Testing: testing}
}

func ptr(value string) *string { return &value }

// epicFixture is an epic as the read path sees it: the server-side sub-issue
// total is what marks it a container, exactly as model.IsEpic reads it.
func epicFixture(n int, title string) model.Issue {
	epic := fixture(n, title, model.EpicLabel)
	epic.SubIssuesTotal = 1
	return epic
}

// claimedEpic is an epic somebody claimed, which is never a pull-request
// target.
func claimedEpic(n int, title string) model.Issue {
	epic := epicFixture(n, title)
	epic.Assignees = []string{"me"}
	epic.Labels = append(epic.Labels, model.InProgressLabel)
	return epic
}
