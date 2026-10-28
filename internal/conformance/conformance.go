// Package conformance holds the behavior every gh.IssueStore must share, so
// the in-memory fake the command tests run on and the real backends are
// checked against one definition rather than drifting apart. It only uses the
// interface: a backend-specific quirk belongs in that backend's own tests.
package conformance

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/model"
)

// Run executes the suite. newStore must return a fresh, empty store each call.
func Run(t *testing.T, newStore func(t *testing.T) gh.IssueStore) {
	t.Helper()
	cases := []struct {
		name string
		fn   func(t *testing.T, s gh.IssueStore)
	}{
		{"create and get", createAndGet},
		{"create numbers are distinct and increasing", createNumbers},
		{"list filters by state", listByState},
		{"edit title and body", editTitleAndBody},
		{"labels add once and remove", labels},
		{"assignees add once and remove", assignees},
		{"close and reopen", closeAndReopen},
		{"comments show on get", comments},
		{"blocked-by refs follow the blocker's state", blockedBy},
		{"blocked-by refuses self and direct cycles", blockedByCycles},
		{"sub-issues, rollup and reparenting", subIssues},
		{"mutating a missing issue fails", missingIssue},
		{"search matches title and body in both states", search},
		{"created labels are listed", labelRegistry},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.fn(t, newStore(t)) })
	}
}

var ctx = context.Background()

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func create(t *testing.T, s gh.IssueStore, title string, labels ...string) model.Issue {
	t.Helper()
	i, err := s.CreateIssue(ctx, title, "body of "+title, labels)
	must(t, err)
	return i
}

func get(t *testing.T, s gh.IssueStore, n int) model.Issue {
	t.Helper()
	i, err := s.GetIssue(ctx, n)
	must(t, err)
	return i
}

func createAndGet(t *testing.T, s gh.IssueStore) {
	created := create(t, s, "first", "P1", "bug")
	if created.Number <= 0 || created.State != "OPEN" || created.CreatedAt.IsZero() {
		t.Errorf("created issue = %+v, want a positive number, OPEN, and a creation time", created)
	}
	got := get(t, s, created.Number)
	if got.Title != "first" || got.Body != "body of first" {
		t.Errorf("got title %q body %q", got.Title, got.Body)
	}
	if !slices.Equal(got.Labels, []string{"P1", "bug"}) {
		t.Errorf("labels = %v, want [P1 bug]", got.Labels)
	}
}

func createNumbers(t *testing.T, s gh.IssueStore) {
	a, b := create(t, s, "a"), create(t, s, "b")
	if b.Number <= a.Number {
		t.Errorf("numbers %d then %d, want increasing", a.Number, b.Number)
	}
}

func listByState(t *testing.T, s gh.IssueStore) {
	open, done := create(t, s, "open one"), create(t, s, "done one")
	must(t, s.CloseIssue(ctx, done.Number, gh.CloseCompleted))

	numbers := func(states ...gh.IssueState) []int {
		list, err := s.ListIssues(ctx, states)
		must(t, err)
		var out []int
		for _, i := range list {
			out = append(out, i.Number)
		}
		slices.Sort(out)
		return out
	}
	if got := numbers(gh.StateOpen); !slices.Equal(got, []int{open.Number}) {
		t.Errorf("open = %v, want [%d]", got, open.Number)
	}
	if got := numbers(gh.StateClosed); !slices.Equal(got, []int{done.Number}) {
		t.Errorf("closed = %v, want [%d]", got, done.Number)
	}
	if got := numbers(gh.StateOpen, gh.StateClosed); len(got) != 2 {
		t.Errorf("all = %v, want both", got)
	}
}

func editTitleAndBody(t *testing.T, s gh.IssueStore) {
	i := create(t, s, "old")
	must(t, s.EditTitle(ctx, i.Number, "new"))
	must(t, s.EditBody(ctx, i.Number, "new body"))
	if got := get(t, s, i.Number); got.Title != "new" || got.Body != "new body" {
		t.Errorf("got %q / %q", got.Title, got.Body)
	}
}

func labels(t *testing.T, s gh.IssueStore) {
	i := create(t, s, "x", "P2")
	must(t, s.AddLabels(ctx, i.Number, []string{"bug", "P2"}))
	if got := get(t, s, i.Number).Labels; !slices.Equal(got, []string{"P2", "bug"}) {
		t.Errorf("labels = %v, want [P2 bug]: P2 must not be added twice", got)
	}
	must(t, s.RemoveLabel(ctx, i.Number, "P2"))
	must(t, s.RemoveLabel(ctx, i.Number, "never-had-it"))
	if got := get(t, s, i.Number).Labels; !slices.Equal(got, []string{"bug"}) {
		t.Errorf("labels = %v, want [bug]", got)
	}
}

func assignees(t *testing.T, s gh.IssueStore) {
	i := create(t, s, "x")
	must(t, s.AddAssignee(ctx, i.Number, "ann"))
	must(t, s.AddAssignee(ctx, i.Number, "ann"))
	must(t, s.AddAssignee(ctx, i.Number, "bob"))
	if got := get(t, s, i.Number).Assignees; !slices.Equal(got, []string{"ann", "bob"}) {
		t.Errorf("assignees = %v, want [ann bob]", got)
	}
	must(t, s.RemoveAssignees(ctx, i.Number, []string{"ann"}))
	if got := get(t, s, i.Number).Assignees; !slices.Equal(got, []string{"bob"}) {
		t.Errorf("assignees = %v, want [bob]", got)
	}
}

func closeAndReopen(t *testing.T, s gh.IssueStore) {
	i := create(t, s, "x")
	must(t, s.CloseIssue(ctx, i.Number, gh.CloseNotPlanned))
	if got := get(t, s, i.Number); got.State != "CLOSED" || got.StateReason != string(gh.CloseNotPlanned) {
		t.Errorf("after close: state %q reason %q", got.State, got.StateReason)
	}
	must(t, s.ReopenIssue(ctx, i.Number))
	if got := get(t, s, i.Number); got.State != "OPEN" || got.StateReason != "REOPENED" {
		t.Errorf("after reopen: state %q reason %q, want OPEN/REOPENED", got.State, got.StateReason)
	}
}

func comments(t *testing.T, s gh.IssueStore) {
	i := create(t, s, "x")
	must(t, s.Comment(ctx, i.Number, "first"))
	must(t, s.Comment(ctx, i.Number, "second"))
	got := get(t, s, i.Number)
	if got.CommentsTotal != 2 || len(got.Comments) != 2 {
		t.Fatalf("comments = %d shown of %d, want 2 of 2", len(got.Comments), got.CommentsTotal)
	}
	if got.Comments[0].Body != "first" || got.Comments[1].Body != "second" {
		t.Errorf("comments out of order: %+v", got.Comments)
	}
}

func blockedBy(t *testing.T, s gh.IssueStore) {
	blocked, blocker := create(t, s, "blocked"), create(t, s, "blocker")
	must(t, s.AddBlockedBy(ctx, blocked.Number, blocker.Number))
	got := get(t, s, blocked.Number)
	if len(got.BlockedBy) != 1 || got.BlockedBy[0].Number != blocker.Number || !got.BlockedBy[0].IsOpen() {
		t.Fatalf("blocked-by = %+v, want one open ref to #%d", got.BlockedBy, blocker.Number)
	}
	must(t, s.CloseIssue(ctx, blocker.Number, gh.CloseCompleted))
	if got := get(t, s, blocked.Number); got.BlockedBy[0].IsOpen() {
		t.Errorf("blocker closed but its ref still reads open: %+v", got.BlockedBy)
	}
	must(t, s.RemoveBlockedBy(ctx, blocked.Number, blocker.Number))
	if got := get(t, s, blocked.Number); len(got.BlockedBy) != 0 {
		t.Errorf("blocked-by = %+v after removal", got.BlockedBy)
	}
}

func blockedByCycles(t *testing.T, s gh.IssueStore) {
	a, b := create(t, s, "a"), create(t, s, "b")
	if err := s.AddBlockedBy(ctx, a.Number, a.Number); err == nil {
		t.Error("an issue was allowed to block itself")
	}
	must(t, s.AddBlockedBy(ctx, a.Number, b.Number))
	if err := s.AddBlockedBy(ctx, b.Number, a.Number); err == nil {
		t.Error("a direct two-issue cycle was allowed")
	}
}

func subIssues(t *testing.T, s gh.IssueStore) {
	epic1, epic2, child := create(t, s, "epic 1"), create(t, s, "epic 2"), create(t, s, "child")
	must(t, s.AddSubIssue(ctx, epic1.Number, child.Number, false))

	if got := get(t, s, child.Number); got.Parent == nil || got.Parent.Number != epic1.Number {
		t.Fatalf("parent = %+v, want #%d", got.Parent, epic1.Number)
	}
	if got := get(t, s, epic1.Number); !got.IsEpic() || got.SubIssuesTotal != 1 || got.SubIssuesCompleted != 0 {
		t.Errorf("epic = total %d completed %d, want 1/0", got.SubIssuesTotal, got.SubIssuesCompleted)
	}
	must(t, s.CloseIssue(ctx, child.Number, gh.CloseCompleted))
	if got := get(t, s, epic1.Number); got.SubIssuesCompleted != 1 {
		t.Errorf("completed = %d after closing the child, want 1", got.SubIssuesCompleted)
	}

	if err := s.AddSubIssue(ctx, epic2.Number, child.Number, false); err == nil {
		t.Error("a second parent was allowed without replaceParent")
	}
	must(t, s.AddSubIssue(ctx, epic2.Number, child.Number, true))
	if got := get(t, s, epic1.Number); got.IsEpic() {
		t.Errorf("old parent still lists %d sub-issues after the move", got.SubIssuesTotal)
	}
	if got := get(t, s, child.Number); got.Parent == nil || got.Parent.Number != epic2.Number {
		t.Errorf("parent = %+v, want #%d", got.Parent, epic2.Number)
	}

	must(t, s.RemoveSubIssue(ctx, epic2.Number, child.Number))
	if got := get(t, s, child.Number); got.Parent != nil {
		t.Errorf("parent = %+v after removal", got.Parent)
	}
}

func missingIssue(t *testing.T, s gh.IssueStore) {
	const none = 9999
	real := create(t, s, "real")
	checks := map[string]error{
		"get":         func() error { _, err := s.GetIssue(ctx, none); return err }(),
		"edit title":  s.EditTitle(ctx, none, "x"),
		"edit body":   s.EditBody(ctx, none, "x"),
		"add labels":  s.AddLabels(ctx, none, []string{"x"}),
		"remove":      s.RemoveLabel(ctx, none, "x"),
		"assign":      s.AddAssignee(ctx, none, "x"),
		"unassign":    s.RemoveAssignees(ctx, none, []string{"x"}),
		"comment":     s.Comment(ctx, none, "x"),
		"close":       s.CloseIssue(ctx, none, gh.CloseCompleted),
		"reopen":      s.ReopenIssue(ctx, none),
		"block":       s.AddBlockedBy(ctx, none, real.Number),
		"block by":    s.AddBlockedBy(ctx, real.Number, none),
		"sub-issue":   s.AddSubIssue(ctx, none, real.Number, false),
		"as sub-iss.": s.AddSubIssue(ctx, real.Number, none, false),
	}
	for name, err := range checks {
		if err == nil {
			t.Errorf("%s: no error for a missing issue", name)
		}
	}
}

func search(t *testing.T, s gh.IssueStore) {
	inTitle := create(t, s, "Flaky retry logic")
	inBody, err := s.CreateIssue(ctx, "Other", "the retry loop spins", nil)
	must(t, err)
	closed := create(t, s, "Old retry bug")
	must(t, s.CloseIssue(ctx, closed.Number, gh.CloseCompleted))
	create(t, s, "Unrelated")

	found, total, err := s.SearchIssues(ctx, "retry")
	must(t, err)
	var got []int
	for _, i := range found {
		got = append(got, i.Number)
	}
	slices.Sort(got)
	want := []int{inTitle.Number, inBody.Number, closed.Number}
	slices.Sort(want)
	if !slices.Equal(got, want) || total != 3 {
		t.Errorf("search retry = %v (total %d), want %v (total 3)", got, total, want)
	}

	none, total, err := s.SearchIssues(ctx, "zzzz-not-there")
	must(t, err)
	if len(none) != 0 || total != 0 {
		t.Errorf("search for nothing = %d results, total %d", len(none), total)
	}
	if !strings.Contains(strings.ToLower(found[0].Title+found[0].Body), "retry") {
		t.Errorf("first result %q does not contain the term", found[0].Title)
	}
}

func labelRegistry(t *testing.T, s gh.IssueStore) {
	must(t, s.CreateLabel(ctx, gh.Label{Name: "P1", Color: "ff0000", Description: "high"}))
	list, err := s.ListLabels(ctx)
	must(t, err)
	if !slices.ContainsFunc(list, func(l gh.Label) bool { return l.Name == "P1" && l.Color == "ff0000" }) {
		t.Errorf("labels = %+v, want P1 with its color", list)
	}
}
