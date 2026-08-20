package cli

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/model"
)

// fakeClient is a stateful in-memory gh.Client: mutations really mutate,
// so guarded flows (claim, re-read) behave like the API.
type fakeClient struct {
	viewer   string
	issues   []*model.Issue
	labels   []gh.Label
	comments map[int][]string
	calls    []string
	failOn   map[string]error
	// failAfter fails a method once its call count exceeds the threshold,
	// letting a flow's earlier calls of the same method succeed — e.g.
	// migrate creating one issue before the API dies.
	failAfter  map[string]failPoint
	callCounts map[string]int
	nextNum    int
	// rivalOnAssign simulates a same-user claim race: every AddAssignee
	// also lands this login, as if another session won inside the guard
	// window.
	rivalOnAssign string
	// searchTotal, when larger than the match count, simulates a capped
	// search response where the server reports more matches than returned.
	searchTotal int
	// onGetIssue runs after a GetIssue has captured its result, so a test can
	// change the issue behind the tool's back — the concurrent edit that a
	// re-check between two passes exists to catch.
	onGetIssue func(number int)
	// defaultBranch and openPRs are the pull-request half of the fake:
	// created PRs land in openPRs keyed by head branch, so a second `pr` on
	// the same branch sees the first one exactly as the API would.
	defaultBranch string
	openPRs       map[string]gh.PullRequest
	nextPR        int
}

// failPoint is a failAfter entry: calls successful calls, then err.
type failPoint struct {
	calls int
	err   error
}

func newFake(issues ...*model.Issue) *fakeClient {
	f := &fakeClient{
		viewer: "me", comments: map[int][]string{},
		failOn: map[string]error{}, failAfter: map[string]failPoint{},
		callCounts: map[string]int{}, nextNum: 100,
		defaultBranch: "main", openPRs: map[string]gh.PullRequest{}, nextPR: 500,
	}
	for _, i := range issues {
		if i.ID == "" {
			i.ID = fmt.Sprintf("ID%d", i.Number)
		}
		if i.State == "" {
			i.State = "OPEN"
		}
		if i.CreatedAt.IsZero() {
			i.CreatedAt = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
		}
		f.issues = append(f.issues, i)
	}
	return f
}

func (f *fakeClient) record(call string) error {
	f.calls = append(f.calls, call)
	method := strings.SplitN(call, " ", 2)[0]
	f.callCounts[method]++
	if fp, ok := f.failAfter[method]; ok && f.callCounts[method] > fp.calls {
		return fp.err
	}
	return f.failOn[method]
}

func (f *fakeClient) byNumber(n int) *model.Issue {
	for _, i := range f.issues {
		if i.Number == n {
			return i
		}
	}
	return nil
}

// requireIssue mirrors the real client, where a mutation on a missing issue
// fails at node-ID resolution rather than panicking.
func (f *fakeClient) requireIssue(n int) (*model.Issue, error) {
	if i := f.byNumber(n); i != nil {
		return i, nil
	}
	return nil, fmt.Errorf("issue #%d not found in o/r", n)
}

// refreshRefs recomputes Ref states and epic rollups after any mutation.
func (f *fakeClient) refreshRefs() {
	state := map[int]string{}
	for _, i := range f.issues {
		state[i.Number] = i.State
	}
	for _, i := range f.issues {
		for idx, r := range i.BlockedBy {
			if s, ok := state[r.Number]; ok {
				i.BlockedBy[idx].State = s
			}
		}
		completed := 0
		for idx, r := range i.SubIssues {
			if s, ok := state[r.Number]; ok {
				i.SubIssues[idx].State = s
				if s == "CLOSED" {
					completed++
				}
			}
		}
		i.SubIssuesCompleted = completed
		// Default the totals to the fetched length, but preserve a larger
		// total a test set deliberately to simulate a capped connection.
		if i.SubIssuesTotal < len(i.SubIssues) {
			i.SubIssuesTotal = len(i.SubIssues)
		}
		if i.BlockedByTotal < len(i.BlockedBy) {
			i.BlockedByTotal = len(i.BlockedBy)
		}
	}
}

func (f *fakeClient) Viewer(ctx context.Context) (string, error) {
	if err := f.record("Viewer"); err != nil {
		return "", err
	}
	return f.viewer, nil
}

func (f *fakeClient) ListIssues(ctx context.Context, states []gh.IssueState) ([]model.Issue, error) {
	labels := make([]string, len(states))
	for i, s := range states {
		labels[i] = string(s)
	}
	if err := f.record("ListIssues " + strings.Join(labels, ",")); err != nil {
		return nil, err
	}
	f.refreshRefs()
	var out []model.Issue
	for _, i := range f.issues {
		if slices.Contains(states, gh.IssueState(i.State)) {
			out = append(out, *i)
		}
	}
	return out, nil
}

func (f *fakeClient) GetIssue(ctx context.Context, number int) (model.Issue, error) {
	if err := f.record(fmt.Sprintf("GetIssue %d", number)); err != nil {
		return model.Issue{}, err
	}
	f.refreshRefs()
	i := f.byNumber(number)
	if i == nil {
		return model.Issue{}, fmt.Errorf("issue #%d not found in o/r", number)
	}
	out := *i
	if f.onGetIssue != nil {
		f.onGetIssue(number)
	}
	return out, nil
}

// SearchIssues fakes best-match search as a case-insensitive substring
// match over title and body, both states.
func (f *fakeClient) SearchIssues(ctx context.Context, terms string) ([]model.Issue, int, error) {
	if err := f.record("SearchIssues " + terms); err != nil {
		return nil, 0, err
	}
	f.refreshRefs()
	needle := strings.ToLower(terms)
	var out []model.Issue
	for _, i := range f.issues {
		if strings.Contains(strings.ToLower(i.Title+" "+i.Body), needle) {
			out = append(out, *i)
		}
	}
	total := max(len(out), f.searchTotal)
	return out, total, nil
}

func (f *fakeClient) CreateIssue(ctx context.Context, title, body string, labels []string) (model.Issue, error) {
	if err := f.record(fmt.Sprintf("CreateIssue %q labels=%s body=%q", title, strings.Join(labels, ","), body)); err != nil {
		return model.Issue{}, err
	}
	f.nextNum++
	i := &model.Issue{
		ID: fmt.Sprintf("ID%d", f.nextNum), Number: f.nextNum, Title: title, Body: body,
		State: "OPEN", Labels: slices.Clone(labels),
		CreatedAt: time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC),
	}
	f.issues = append(f.issues, i)
	return *i, nil
}

func (f *fakeClient) EditTitle(ctx context.Context, number int, title string) error {
	if err := f.record(fmt.Sprintf("EditTitle %d %q", number, title)); err != nil {
		return err
	}
	i, err := f.requireIssue(number)
	if err != nil {
		return err
	}
	i.Title = title
	return nil
}

func (f *fakeClient) EditBody(ctx context.Context, number int, body string) error {
	if err := f.record(fmt.Sprintf("EditBody %d %q", number, body)); err != nil {
		return err
	}
	i, err := f.requireIssue(number)
	if err != nil {
		return err
	}
	i.Body = body
	return nil
}

func (f *fakeClient) AddLabels(ctx context.Context, number int, labels []string) error {
	if err := f.record(fmt.Sprintf("AddLabels %d %s", number, strings.Join(labels, ","))); err != nil {
		return err
	}
	i, err := f.requireIssue(number)
	if err != nil {
		return err
	}
	for _, l := range labels {
		if !slices.Contains(i.Labels, l) {
			i.Labels = append(i.Labels, l)
		}
	}
	return nil
}

func (f *fakeClient) RemoveLabel(ctx context.Context, number int, label string) error {
	if err := f.record(fmt.Sprintf("RemoveLabel %d %s", number, label)); err != nil {
		return err
	}
	i, err := f.requireIssue(number)
	if err != nil {
		return err
	}
	i.Labels = slices.DeleteFunc(slices.Clone(i.Labels), func(l string) bool { return l == label })
	return nil
}

func (f *fakeClient) AddAssignee(ctx context.Context, number int, login string) error {
	if err := f.record(fmt.Sprintf("AddAssignee %d %s", number, login)); err != nil {
		return err
	}
	i, err := f.requireIssue(number)
	if err != nil {
		return err
	}
	if !slices.Contains(i.Assignees, login) {
		i.Assignees = append(i.Assignees, login)
	}
	if f.rivalOnAssign != "" && !slices.Contains(i.Assignees, f.rivalOnAssign) {
		i.Assignees = append(i.Assignees, f.rivalOnAssign)
	}
	return nil
}

func (f *fakeClient) RemoveAssignees(ctx context.Context, number int, logins []string) error {
	if err := f.record(fmt.Sprintf("RemoveAssignees %d %s", number, strings.Join(logins, ","))); err != nil {
		return err
	}
	i, err := f.requireIssue(number)
	if err != nil {
		return err
	}
	i.Assignees = slices.DeleteFunc(slices.Clone(i.Assignees), func(l string) bool { return slices.Contains(logins, l) })
	return nil
}

func (f *fakeClient) Comment(ctx context.Context, number int, body string) error {
	if err := f.record(fmt.Sprintf("Comment %d %q", number, body)); err != nil {
		return err
	}
	if _, err := f.requireIssue(number); err != nil {
		return err
	}
	f.comments[number] = append(f.comments[number], body)
	return nil
}

func (f *fakeClient) CloseIssue(ctx context.Context, number int, reason gh.CloseReason) error {
	if err := f.record(fmt.Sprintf("CloseIssue %d %s", number, reason)); err != nil {
		return err
	}
	i, err := f.requireIssue(number)
	if err != nil {
		return err
	}
	i.State = "CLOSED"
	i.StateReason = string(reason)
	return nil
}

func (f *fakeClient) AddBlockedBy(ctx context.Context, number, blockingNumber int) error {
	if err := f.record(fmt.Sprintf("AddBlockedBy %d %d", number, blockingNumber)); err != nil {
		return err
	}
	i, err := f.requireIssue(number)
	if err != nil {
		return err
	}
	b, err := f.requireIssue(blockingNumber)
	if err != nil {
		return err
	}
	// The real API rejects self-blocks and direct two-issue cycles (and
	// only those — longer cycles are silently accepted, per the DESIGN.md
	// spike), so the fake must too: a client-side check that regressed to
	// miss these must not look like success here.
	if number == blockingNumber {
		return fmt.Errorf("issue #%d cannot block itself", number)
	}
	if slices.ContainsFunc(b.BlockedBy, func(r model.Ref) bool { return r.Number == number }) {
		return fmt.Errorf("issues #%d and #%d would block each other", number, blockingNumber)
	}
	i.BlockedBy = append(i.BlockedBy, model.Ref{Number: b.Number, State: b.State})
	return nil
}

func (f *fakeClient) RemoveBlockedBy(ctx context.Context, number, blockingNumber int) error {
	if err := f.record(fmt.Sprintf("RemoveBlockedBy %d %d", number, blockingNumber)); err != nil {
		return err
	}
	i, err := f.requireIssue(number)
	if err != nil {
		return err
	}
	b, err := f.requireIssue(blockingNumber)
	if err != nil {
		return err
	}
	i.BlockedBy = slices.DeleteFunc(slices.Clone(i.BlockedBy), func(r model.Ref) bool { return r.Number == b.Number })
	return nil
}

func (f *fakeClient) AddSubIssue(ctx context.Context, parentNumber, childNumber int, replaceParent bool) error {
	if err := f.record(fmt.Sprintf("AddSubIssue %d %d replace=%v", parentNumber, childNumber, replaceParent)); err != nil {
		return err
	}
	parent, err := f.requireIssue(parentNumber)
	if err != nil {
		return err
	}
	child, err := f.requireIssue(childNumber)
	if err != nil {
		return err
	}
	if child.Parent != nil {
		if !replaceParent {
			return fmt.Errorf("#%d already has a parent", child.Number)
		}
		old := f.byNumber(child.Parent.Number)
		old.SubIssues = slices.DeleteFunc(slices.Clone(old.SubIssues), func(r model.Ref) bool { return r.Number == child.Number })
	}
	child.Parent = &model.Ref{Number: parent.Number, State: parent.State}
	parent.SubIssues = append(parent.SubIssues, model.Ref{Number: child.Number, State: child.State})
	return nil
}

func (f *fakeClient) RemoveSubIssue(ctx context.Context, parentNumber, childNumber int) error {
	if err := f.record(fmt.Sprintf("RemoveSubIssue %d %d", parentNumber, childNumber)); err != nil {
		return err
	}
	parent, err := f.requireIssue(parentNumber)
	if err != nil {
		return err
	}
	child, err := f.requireIssue(childNumber)
	if err != nil {
		return err
	}
	child.Parent = nil
	parent.SubIssues = slices.DeleteFunc(slices.Clone(parent.SubIssues), func(r model.Ref) bool { return r.Number == child.Number })
	return nil
}

func (f *fakeClient) ListLabels(ctx context.Context) ([]gh.Label, error) {
	if err := f.record("ListLabels"); err != nil {
		return nil, err
	}
	return slices.Clone(f.labels), nil
}

func (f *fakeClient) CreateLabel(ctx context.Context, label gh.Label) error {
	if err := f.record("CreateLabel " + label.Name); err != nil {
		return err
	}
	f.labels = append(f.labels, label)
	return nil
}

func (f *fakeClient) PullRequestContext(ctx context.Context, head string) (gh.PRContext, error) {
	if err := f.record("PullRequestContext " + head); err != nil {
		return gh.PRContext{}, err
	}
	out := gh.PRContext{DefaultBranch: f.defaultBranch}
	if pr, ok := f.openPRs[head]; ok {
		out.Existing = &pr
	}
	return out, nil
}

func (f *fakeClient) CreatePullRequest(ctx context.Context, pr gh.NewPullRequest) (gh.PullRequest, error) {
	if err := f.record(fmt.Sprintf("CreatePullRequest %s→%s draft=%v %q body=%q",
		pr.Head, pr.Base, pr.Draft, pr.Title, pr.Body)); err != nil {
		return gh.PullRequest{}, err
	}
	f.nextPR++
	created := gh.PullRequest{
		Number: f.nextPR,
		URL:    fmt.Sprintf("https://github.com/o/r/pull/%d", f.nextPR),
		Draft:  pr.Draft,
	}
	f.openPRs[pr.Head] = created
	return created, nil
}

// newApp wires an App to the fake with captured output.
func newApp(f *fakeClient) (*App, *bytes.Buffer, *bytes.Buffer) {
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	return &App{
		Client: f,
		Repo:   gh.Repo{Owner: "o", Name: "r"},
		Out:    out,
		ErrOut: errOut,
	}, out, errOut
}

func issue(n int, title string, labels ...string) *model.Issue {
	return &model.Issue{Number: n, Title: title, Labels: labels}
}
