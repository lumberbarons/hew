// Package conformance holds the behavior every gh.IssueStore must share, so
// the in-memory fake the command tests run on and the real backends are
// checked against one definition rather than drifting apart. It only uses the
// interface: a backend-specific quirk belongs in that backend's own tests.
package conformance

import (
	"context"
	"errors"
	"reflect"
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
		{"list carries relationships, totals and labels", listRelationships},
		{"edit title and body", editTitleAndBody},
		{"labels add once and remove", labels},
		{"label names are case-insensitive", labelCase},
		{"assignees add once and remove", assignees},
		{"an assignee need not be a known user", unknownAssignee},
		{"close and reopen", closeAndReopen},
		{"comments show on get", comments},
		{"blocked-by refs follow the blocker's state", blockedBy},
		{"duplicate and missing edges are idempotent", idempotentEdges},
		{"blocked-by refuses self and direct cycles", blockedByCycles},
		{"sub-issues, rollup and reparenting", subIssues},
		{"mutating a missing issue fails without changing state", missingIssue},
		{"search matches title and body in both states", search},
		{"created labels are listed", labelRegistry},
		{"a claim is atomic where supported", claim},
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

// allStates is every issue the store holds, whichever state it is in.
var allStates = []gh.IssueState{gh.StateOpen, gh.StateClosed}

// list reads the same view `ready`, `prime` and `list` use. Relationships are
// part of it: a backend that only fills them in GetIssue is broken for every
// list-shaped command.
func list(t *testing.T, s gh.IssueStore) map[int]model.Issue {
	t.Helper()
	issues, err := s.ListIssues(ctx, allStates)
	must(t, err)
	return model.ByNumber(issues)
}

// snapshot records everything a refused mutation could have changed: the list
// view, relationships included, and each issue's detail view. Sorted by number
// so two reads compare directly.
func snapshot(t *testing.T, s gh.IssueStore) []model.Issue {
	t.Helper()
	issues, err := s.ListIssues(ctx, allStates)
	must(t, err)
	slices.SortFunc(issues, func(a, b model.Issue) int { return a.Number - b.Number })
	out := make([]model.Issue, len(issues))
	for i, issue := range issues {
		out[i] = get(t, s, issue.Number)
	}
	return out
}

func assertUnchanged(t *testing.T, s gh.IssueStore, before []model.Issue, when string) {
	t.Helper()
	if after := snapshot(t, s); !reflect.DeepEqual(after, before) {
		t.Errorf("%s changed the store:\nbefore: %+v\nafter:  %+v", when, before, after)
	}
}

func createAndGet(t *testing.T, s gh.IssueStore) {
	created := create(t, s, "first", "P1", "bug")
	// CreateIssue's result is only what the create call itself returns: on
	// GitHub that is ID, number, title, state and labels — no body, no
	// creation time. Body and CreatedAt are GetIssue's contract.
	if created.Number <= 0 || created.State != "OPEN" || created.Title != "first" {
		t.Errorf("created issue = %+v, want a positive number, OPEN, and its title", created)
	}
	if !slices.Equal(created.Labels, []string{"P1", "bug"}) {
		t.Errorf("created labels = %v, want [P1 bug]", created.Labels)
	}
	got := get(t, s, created.Number)
	if got.Title != "first" || got.Body != "body of first" || got.CreatedAt.IsZero() {
		t.Errorf("got title %q body %q createdAt %v", got.Title, got.Body, got.CreatedAt)
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

// ListIssues is the read path for ready, prime and list, so it must carry the
// same relationships, totals and labels as GetIssue.
func listRelationships(t *testing.T, s gh.IssueStore) {
	epic := create(t, s, "epic")
	child := create(t, s, "child", "P1", "bug")
	blocker := create(t, s, "blocker")
	blocked := create(t, s, "blocked")
	must(t, s.AddSubIssue(ctx, epic.Number, child.Number, false))
	must(t, s.AddBlockedBy(ctx, blocked.Number, blocker.Number))
	must(t, s.AddLabels(ctx, child.Number, []string{"area"}))
	must(t, s.CloseIssue(ctx, child.Number, gh.CloseCompleted))
	must(t, s.CloseIssue(ctx, blocker.Number, gh.CloseCompleted))

	issues := list(t, s)
	if got, ok := issues[epic.Number]; !ok {
		t.Errorf("epic #%d missing from ListIssues", epic.Number)
	} else if got.SubIssuesTotal != 1 || got.SubIssuesCompleted != 1 || len(got.SubIssues) != 1 ||
		got.SubIssues[0].Number != child.Number || got.SubIssues[0].IsOpen() {
		t.Errorf("epic sub-issues = %+v (total %d, completed %d), want #%d closed and counted",
			got.SubIssues, got.SubIssuesTotal, got.SubIssuesCompleted, child.Number)
	}

	if got, ok := issues[child.Number]; !ok {
		t.Errorf("closed child #%d missing from ListIssues", child.Number)
	} else {
		if got.Parent == nil || got.Parent.Number != epic.Number || !got.Parent.IsOpen() {
			t.Errorf("child parent = %+v, want #%d open", got.Parent, epic.Number)
		}
		if got.ParentTitle != "epic" {
			t.Errorf("child parent title = %q, want the epic's title", got.ParentTitle)
		}
		if !slices.Equal(got.Labels, []string{"P1", "bug", "area"}) {
			t.Errorf("child labels = %v, want [P1 bug area]", got.Labels)
		}
	}

	if got, ok := issues[blocked.Number]; !ok {
		t.Errorf("blocked issue #%d missing from ListIssues", blocked.Number)
	} else if got.BlockedByTotal != 1 || len(got.BlockedBy) != 1 ||
		got.BlockedBy[0].Number != blocker.Number || got.BlockedBy[0].IsOpen() {
		t.Errorf("blocked-by = %+v (total %d), want #%d closed and counted",
			got.BlockedBy, got.BlockedByTotal, blocker.Number)
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

// GitHub label names are case-insensitive: adding an existing label under a
// different spelling attaches the existing label, and removal matches the same
// way. The spelling already in use is the canonical one.
func labelCase(t *testing.T, s gh.IssueStore) {
	i := create(t, s, "x", "bug", "P2")
	must(t, s.AddLabels(ctx, i.Number, []string{"BUG", "p2"}))
	if got := get(t, s, i.Number).Labels; !slices.Equal(got, []string{"bug", "P2"}) {
		t.Errorf("labels = %v after adding BUG and p2, want the existing [bug P2]", got)
	}

	// Labels are repo-scoped: a later issue adopts a spelling already in use.
	if got := create(t, s, "other", "BUG").Labels; !slices.Equal(got, []string{"bug"}) {
		t.Errorf("created labels = %v, want the repo's existing [bug]", got)
	}

	// A genuinely new label keeps the spelling it was given.
	must(t, s.AddLabels(ctx, i.Number, []string{"Needs-Info"}))
	must(t, s.RemoveLabel(ctx, i.Number, "bUg"))
	if got := get(t, s, i.Number).Labels; !slices.Equal(got, []string{"P2", "Needs-Info"}) {
		t.Errorf("labels = %v after removing bUg, want [P2 Needs-Info]", got)
	}
	must(t, s.RemoveLabel(ctx, i.Number, "NEEDS-INFO"))
	if got := get(t, s, i.Number).Labels; !slices.Equal(got, []string{"P2"}) {
		t.Errorf("labels = %v after removing NEEDS-INFO, want [P2]", got)
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

// AddAssignee records the login it is handed. Whether a login resolves to a
// real user is a server concern — GitHub validates it, a local store has no
// user directory — so it is deliberately not part of this contract.
func unknownAssignee(t *testing.T, s gh.IssueStore) {
	i := create(t, s, "x")
	const ghost = "no-such-user-42"
	must(t, s.AddAssignee(ctx, i.Number, ghost))
	if got := get(t, s, i.Number).Assignees; !slices.Equal(got, []string{ghost}) {
		t.Errorf("assignees = %v, want the login as given", got)
	}
	if got, ok := list(t, s)[i.Number]; !ok || !slices.Equal(got.Assignees, []string{ghost}) {
		t.Errorf("listed assignees = %v, want the login as given", got.Assignees)
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
	if len(got.BlockedBy) != 1 || got.BlockedBy[0].Number != blocker.Number || !got.BlockedBy[0].IsOpen() || got.BlockedByTotal != 1 {
		t.Fatalf("blocked-by = %+v (total %d), want one open ref to #%d", got.BlockedBy, got.BlockedByTotal, blocker.Number)
	}
	must(t, s.CloseIssue(ctx, blocker.Number, gh.CloseCompleted))
	if got := get(t, s, blocked.Number); got.BlockedBy[0].IsOpen() {
		t.Errorf("blocker closed but its ref still reads open: %+v", got.BlockedBy)
	}
	must(t, s.RemoveBlockedBy(ctx, blocked.Number, blocker.Number))
	if got := get(t, s, blocked.Number); len(got.BlockedBy) != 0 || got.BlockedByTotal != 0 {
		t.Errorf("blocked-by = %+v (total %d) after removal", got.BlockedBy, got.BlockedByTotal)
	}
}

// The edge mutations are idempotent, as GitHub's are: adding an edge that
// already exists and removing one that does not both succeed, and neither
// changes the graph.
func idempotentEdges(t *testing.T, s gh.IssueStore) {
	a, b := create(t, s, "a"), create(t, s, "b")
	epic, child := create(t, s, "epic"), create(t, s, "child")

	must(t, s.AddBlockedBy(ctx, a.Number, b.Number))
	must(t, s.AddBlockedBy(ctx, a.Number, b.Number))
	if got := get(t, s, a.Number); len(got.BlockedBy) != 1 || got.BlockedByTotal != 1 {
		t.Errorf("blocked-by = %+v (total %d) after adding the same edge twice, want one", got.BlockedBy, got.BlockedByTotal)
	}

	must(t, s.AddSubIssue(ctx, epic.Number, child.Number, false))
	must(t, s.AddSubIssue(ctx, epic.Number, child.Number, false))
	must(t, s.AddSubIssue(ctx, epic.Number, child.Number, true))
	if got := get(t, s, epic.Number); len(got.SubIssues) != 1 || got.SubIssuesTotal != 1 {
		t.Errorf("sub-issues = %+v (total %d) after re-adding the same parent, want one", got.SubIssues, got.SubIssuesTotal)
	}

	must(t, s.RemoveBlockedBy(ctx, a.Number, b.Number))
	must(t, s.RemoveBlockedBy(ctx, a.Number, b.Number))
	if got := get(t, s, a.Number); len(got.BlockedBy) != 0 || got.BlockedByTotal != 0 {
		t.Errorf("blocked-by = %+v (total %d) after removing the same edge twice", got.BlockedBy, got.BlockedByTotal)
	}
}

func blockedByCycles(t *testing.T, s gh.IssueStore) {
	a, b := create(t, s, "a"), create(t, s, "b")
	before := snapshot(t, s)
	if err := s.AddBlockedBy(ctx, a.Number, a.Number); err == nil {
		t.Error("an issue was allowed to block itself")
	}
	assertUnchanged(t, s, before, "a refused self-block")

	must(t, s.AddBlockedBy(ctx, a.Number, b.Number))
	before = snapshot(t, s)
	if err := s.AddBlockedBy(ctx, b.Number, a.Number); err == nil {
		t.Error("a direct two-issue cycle was allowed")
	}
	assertUnchanged(t, s, before, "a refused two-issue cycle")
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
	before := snapshot(t, s)
	checks := map[string]error{
		"get":           func() error { _, err := s.GetIssue(ctx, none); return err }(),
		"edit title":    s.EditTitle(ctx, none, "x"),
		"edit body":     s.EditBody(ctx, none, "x"),
		"add labels":    s.AddLabels(ctx, none, []string{"x"}),
		"remove":        s.RemoveLabel(ctx, none, "x"),
		"assign":        s.AddAssignee(ctx, none, "x"),
		"unassign":      s.RemoveAssignees(ctx, none, []string{"x"}),
		"comment":       s.Comment(ctx, none, "x"),
		"close":         s.CloseIssue(ctx, none, gh.CloseCompleted),
		"reopen":        s.ReopenIssue(ctx, none),
		"block":         s.AddBlockedBy(ctx, none, real.Number),
		"block by":      s.AddBlockedBy(ctx, real.Number, none),
		"unblock":       s.RemoveBlockedBy(ctx, none, real.Number),
		"sub-issue":     s.AddSubIssue(ctx, none, real.Number, false),
		"as sub-iss.":   s.AddSubIssue(ctx, real.Number, none, false),
		"remove child":  s.RemoveSubIssue(ctx, none, real.Number),
		"remove parent": s.RemoveSubIssue(ctx, real.Number, none),
	}
	for name, err := range checks {
		if err == nil {
			t.Errorf("%s: no error for a missing issue", name)
		}
	}
	assertUnchanged(t, s, before, "a refused mutation on a missing issue")
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
	if len(found) == 0 {
		t.Fatal("search retry found nothing")
	}
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

// Claimer is optional — GitHub has no conditional issue write — so this runs
// only against a backend that offers it. It pins what the capability
// promises: one winner, the loser sees ErrClaimConflict with the issue
// untouched, force steals, and a closed issue cannot be claimed.
func claim(t *testing.T, s gh.IssueStore) {
	claimer, ok := s.(gh.Claimer)
	if !ok {
		t.Skip("backend has no atomic claim")
	}
	i := create(t, s, "x", "P2", "bug")
	must(t, claimer.ClaimIssue(ctx, i.Number, "ann", false))
	got := get(t, s, i.Number)
	if !slices.Equal(got.Assignees, []string{"ann"}) || !got.InProgress() {
		t.Fatalf("claim = assignees %v labels %v, want ann and in-progress", got.Assignees, got.Labels)
	}

	before := snapshot(t, s)
	if err := claimer.ClaimIssue(ctx, i.Number, "bob", false); !errors.Is(err, gh.ErrClaimConflict) {
		t.Errorf("second claim err = %v, want ErrClaimConflict", err)
	}
	assertUnchanged(t, s, before, "a refused claim")

	must(t, claimer.ClaimIssue(ctx, i.Number, "bob", true))
	if got := get(t, s, i.Number); !slices.Equal(got.Assignees, []string{"bob"}) {
		t.Errorf("forced claim assignees = %v, want [bob]", got.Assignees)
	}

	closed := create(t, s, "closed")
	must(t, s.CloseIssue(ctx, closed.Number, gh.CloseCompleted))
	before = snapshot(t, s)
	if err := claimer.ClaimIssue(ctx, closed.Number, "ann", true); err == nil {
		t.Error("a closed issue was claimed")
	}
	assertUnchanged(t, s, before, "a refused claim on a closed issue")
}
