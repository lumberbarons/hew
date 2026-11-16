package migrate

import (
	"context"
	"fmt"
	"slices"
)

// fakeTarget is a stateful in-memory Target: mutations really mutate, so the
// engine's resume and verification passes see what an earlier run wrote. It
// records every call and can fail a chosen method on a chosen call.
type fakeTarget struct {
	issues map[int]*fakeIssue
	// labels is every label the fake knows about, for a test that checks the
	// engine never bootstraps them itself.
	labels map[string]bool
	// comments is issue number → comment bodies, in posted order.
	comments map[int][]string
	// edges records "kind from to" for every wired dependency.
	edges      []string
	calls      []string
	callCounts map[string]int
	failOn     map[string]error
	failAfter  map[string]failPoint
	viewer     string
	next       int
	// onCreate runs after a create succeeds, so a test can cancel a run
	// between two writes.
	onCreate func(number int)
	// blockCreate makes Create wait for the context instead of returning, so
	// a test can prove a backend call is interruptible.
	blockCreate bool
}

type fakeIssue struct {
	Number      int
	Title       string
	Body        string
	State       string
	StateReason string
	Labels      []string
	Assignees   []string
}

// failPoint is a failAfter entry: calls successful calls, then err.
type failPoint struct {
	calls int
	err   error
}

func newFakeTarget() *fakeTarget {
	return &fakeTarget{
		issues:     map[int]*fakeIssue{},
		labels:     map[string]bool{},
		comments:   map[int][]string{},
		failOn:     map[string]error{},
		failAfter:  map[string]failPoint{},
		callCounts: map[string]int{},
		viewer:     "me",
		next:       100,
	}
}

// addIssue seeds an issue the fake reports as already on the target.
func (f *fakeTarget) addIssue(number int, title, body string) *fakeIssue {
	issue := &fakeIssue{Number: number, Title: title, Body: body, State: "OPEN"}
	f.issues[number] = issue
	return issue
}

func (f *fakeTarget) byNumber(number int) *fakeIssue { return f.issues[number] }

// call records one method invocation and answers the injected failure, if
// this call is the one it targets.
func (f *fakeTarget) call(method, detail string) error {
	if detail == "" {
		f.calls = append(f.calls, method)
	} else {
		f.calls = append(f.calls, method+" "+detail)
	}
	f.callCounts[method]++
	if fp, ok := f.failAfter[method]; ok && f.callCounts[method] > fp.calls {
		return fp.err
	}
	return f.failOn[method]
}

func (f *fakeTarget) Create(ctx context.Context, req CreateRequest) (int, error) {
	if f.blockCreate {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	if err := f.call("CreateIssue", ""); err != nil {
		return 0, err
	}
	f.next++
	f.issues[f.next] = &fakeIssue{
		Number: f.next, Title: req.Title, Body: req.Body, State: "OPEN", Labels: slices.Clone(req.Labels),
	}
	if f.onCreate != nil {
		f.onCreate(f.next)
	}
	return f.next, nil
}

func (f *fakeTarget) Get(_ context.Context, number int) (Issue, error) {
	if err := f.call("GetIssue", fmt.Sprint(number)); err != nil {
		return Issue{}, err
	}
	issue, ok := f.issues[number]
	if !ok {
		return Issue{}, fmt.Errorf("issue #%d not found", number)
	}
	return Issue{Number: issue.Number, Title: issue.Title, Body: issue.Body, State: issue.State}, nil
}

func (f *fakeTarget) EditTitle(_ context.Context, number int, title string) error {
	if err := f.call("EditTitle", fmt.Sprint(number)); err != nil {
		return err
	}
	issue, ok := f.issues[number]
	if !ok {
		return fmt.Errorf("issue #%d not found", number)
	}
	issue.Title = title
	return nil
}

func (f *fakeTarget) EditBody(_ context.Context, number int, body string) error {
	if err := f.call("EditBody", fmt.Sprint(number)); err != nil {
		return err
	}
	issue, ok := f.issues[number]
	if !ok {
		return fmt.Errorf("issue #%d not found", number)
	}
	issue.Body = body
	return nil
}

func (f *fakeTarget) Comment(_ context.Context, number int, body string) error {
	if err := f.call("Comment", fmt.Sprint(number)); err != nil {
		return err
	}
	if _, ok := f.issues[number]; !ok {
		return fmt.Errorf("issue #%d not found", number)
	}
	f.comments[number] = append(f.comments[number], body)
	return nil
}

func (f *fakeTarget) Close(_ context.Context, number int, reason CloseReason) error {
	if err := f.call("CloseIssue", fmt.Sprint(number)); err != nil {
		return err
	}
	issue, ok := f.issues[number]
	if !ok {
		return fmt.Errorf("issue #%d not found", number)
	}
	issue.State = "CLOSED"
	issue.StateReason = string(reason)
	return nil
}

func (f *fakeTarget) AddSubIssue(_ context.Context, parent, child int) error {
	if err := f.call("AddSubIssue", fmt.Sprintf("%d %d", parent, child)); err != nil {
		return err
	}
	f.edges = append(f.edges, fmt.Sprintf("parent %d %d", parent, child))
	return nil
}

func (f *fakeTarget) AddBlockedBy(_ context.Context, issue, blocker int) error {
	if err := f.call("AddBlockedBy", fmt.Sprintf("%d %d", issue, blocker)); err != nil {
		return err
	}
	f.edges = append(f.edges, fmt.Sprintf("blocked-by %d %d", issue, blocker))
	return nil
}

func (f *fakeTarget) AddAssignee(_ context.Context, number int, login string) error {
	if err := f.call("AddAssignee", fmt.Sprintf("%d %s", number, login)); err != nil {
		return err
	}
	issue, ok := f.issues[number]
	if !ok {
		return fmt.Errorf("issue #%d not found", number)
	}
	issue.Assignees = append(issue.Assignees, login)
	return nil
}

func (f *fakeTarget) Viewer(_ context.Context) (string, error) {
	if err := f.call("Viewer", ""); err != nil {
		return "", err
	}
	return f.viewer, nil
}
