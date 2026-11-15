package tracker

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/local"
	"github.com/lumberbarons/hew/internal/model"
)

// fakeClient is the read half of a store: the queries only need ListIssues
// and GetIssue, so embedding the interface keeps the rest out of the way.
type fakeClient struct {
	gh.IssueStore
	issues []model.Issue
}

func (f *fakeClient) ListIssues(_ context.Context, states []gh.IssueState) ([]model.Issue, error) {
	var out []model.Issue
	for _, i := range f.issues {
		if slices.Contains(states, gh.IssueState(i.State)) {
			out = append(out, i)
		}
	}
	return out, nil
}

func (f *fakeClient) GetIssue(_ context.Context, number int) (model.Issue, error) {
	for _, i := range f.issues {
		if i.Number == number {
			return i, nil
		}
	}
	return model.Issue{}, fmt.Errorf("issue #%d not found", number)
}

func fixture(number int, title string, labels ...string) model.Issue {
	return model.Issue{
		Number:    number,
		Title:     title,
		Body:      "### Goal\n\n" + title,
		State:     "OPEN",
		CreatedAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		Labels:    labels,
	}
}

func numbers(issues []Issue) []int {
	out := make([]int, len(issues))
	for n, i := range issues {
		out[n] = i.Number
	}
	return out
}

func TestSnapshotQueriesAndWarningsAgainstFakeClient(t *testing.T) {
	ready := fixture(1, "Ready one", "P2", "bug")
	claimed := fixture(2, "Claimed", "P1", "bug", "in-progress")
	claimed.Assignees = []string{"alice"}
	blocked := fixture(3, "Blocked", "P2", "bug")
	blocked.BlockedBy = []model.Ref{{Number: 1, State: "OPEN"}}

	epic := fixture(4, "Epic", "P2", "task", "epic")
	epic.SubIssues = []model.Ref{{Number: 5, State: "OPEN"}}
	epic.SubIssuesTotal = 1
	child := fixture(5, "Child", "P2", "task")
	child.Parent = &model.Ref{Number: 4, State: "OPEN"}

	closed := fixture(6, "Closed", "P2", "bug")
	closed.State = "CLOSED"

	capped := fixture(7, "Capped blockers", "P3", "bug")
	capped.BlockedBy = []model.Ref{{Number: 1, State: "OPEN"}}
	capped.BlockedByTotal = 4

	cycleA := fixture(8, "Cycle A", "P2", "bug")
	cycleA.BlockedBy = []model.Ref{{Number: 9, State: "OPEN"}}
	cycleB := fixture(9, "Cycle B", "P2", "bug")
	cycleB.BlockedBy = []model.Ref{{Number: 8, State: "OPEN"}}

	cappedSubs := fixture(10, "Capped sub-issues", "P2", "task", "epic")
	cappedSubs.SubIssuesTotal = 3

	store := &fakeClient{issues: []model.Issue{
		ready, claimed, blocked, epic, child, closed, capped, cycleA, cycleB, cappedSubs,
	}}
	snapshot, err := Wrap(store).Snapshot(context.Background(), SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}

	if got, want := numbers(snapshot.Ready()), []int{1, 5}; !slices.Equal(got, want) {
		t.Errorf("Ready = %v, want %v", got, want)
	}
	if got, want := numbers(snapshot.Claimed()), []int{2}; !slices.Equal(got, want) {
		t.Errorf("Claimed = %v, want %v", got, want)
	}
	if got, want := numbers(snapshot.Blocked()), []int{3, 8, 9, 7}; !slices.Equal(got, want) {
		t.Errorf("Blocked = %v, want %v", got, want)
	}
	if got, want := numbers(snapshot.Epics()), []int{4, 10}; !slices.Equal(got, want) {
		t.Errorf("Epics = %v, want %v", got, want)
	}
	if got, want := numbers(snapshot.Children(4)), []int{5}; !slices.Equal(got, want) {
		t.Errorf("Children(4) = %v, want %v", got, want)
	}
	if _, ok := snapshot.Get(6); ok {
		t.Error("Get(6) found a closed issue in an open-only snapshot")
	}

	// The list view carries every fetched issue in list order: ready first,
	// then claimed, blocked, epics and closed.
	if got, want := numbers(snapshot.Issues()), []int{1, 5, 2, 3, 8, 9, 7, 4, 10}; !slices.Equal(got, want) {
		t.Errorf("Issues = %v, want %v", got, want)
	}

	warnings := snapshot.Warnings()
	if len(warnings) != 3 {
		t.Fatalf("Warnings = %+v, want a cycle and two capped connections", warnings)
	}
	if w := warnings[0]; w.Kind != WarnDependencyCycle || !slices.Equal(w.Cycle, []int{8, 9, 8}) {
		t.Errorf("cycle warning = %+v", w)
	}
	if w := warnings[1]; w.Kind != WarnBlockersCapped || w.Issue != 7 || w.Total != 4 || w.Fetched != 1 {
		t.Errorf("blockers-capped warning = %+v", w)
	}
	if w := warnings[2]; w.Kind != WarnSubIssuesCapped || w.Issue != 10 || w.Total != 3 || w.Fetched != 0 {
		t.Errorf("sub-issues-capped warning = %+v", w)
	}
}

func TestSnapshotIncludesClosedOnRequest(t *testing.T) {
	open := fixture(1, "Open", "P2", "bug")
	closed := fixture(2, "Closed", "P2", "bug")
	closed.State = "CLOSED"
	closed.StateReason = "completed"
	store := &fakeClient{issues: []model.Issue{open, closed}}

	snapshot, err := Wrap(store).Snapshot(context.Background(), SnapshotOptions{States: []State{StateOpen, StateClosed}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := numbers(snapshot.Issues()), []int{1, 2}; !slices.Equal(got, want) {
		t.Errorf("Issues = %v, want %v", got, want)
	}
	if got, want := snapshot.States(), []State{StateOpen, StateClosed}; !slices.Equal(got, want) {
		t.Errorf("States = %v, want %v", got, want)
	}
	got, ok := snapshot.Get(2)
	if !ok {
		t.Fatal("closed issue missing from a two-state snapshot")
	}
	if got.State != "closed" || got.StateReason() != "completed" {
		t.Errorf("Get(2) = %+v, want closed/completed", got)
	}
}

func TestGetCarriesBodyAndComments(t *testing.T) {
	issue := fixture(7, "Detailed", "P1", "bug", "tests")
	issue.StateReason = "completed"
	issue.Parent = &model.Ref{Number: 3, State: "OPEN"}
	issue.ParentTitle = "Parent"
	issue.BlockedBy = []model.Ref{{Number: 4, State: "CLOSED"}, {Number: 5, State: "OPEN"}}
	issue.SubIssues = []model.Ref{{Number: 6, State: "CLOSED"}}
	issue.SubIssuesTotal = 2
	issue.SubIssuesCompleted = 1
	issue.Comments = []model.Comment{{Author: "alice", CreatedAt: time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC), Body: "hello"}}
	issue.CommentsTotal = 4
	store := &fakeClient{issues: []model.Issue{issue}}

	got, err := Wrap(store).Get(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if got.Body == nil || *got.Body != issue.Body {
		t.Errorf("Body = %v, want %q", got.Body, issue.Body)
	}
	if len(got.Comments) != 1 || got.Comments[0].Author != "alice" || got.CommentsTotal != 4 {
		t.Errorf("comments = %+v total %d", got.Comments, got.CommentsTotal)
	}
	// The flat schema hides the data rendering needs; the accessors keep it.
	if !slices.Equal(got.Labels(), []string{"P1", "bug", "tests"}) {
		t.Errorf("Labels = %v", got.Labels())
	}
	if !got.HasLabel("BUG") {
		t.Error("HasLabel is not case-insensitive")
	}
	if got.ParentTitle() != "Parent" || got.StateReason() != "completed" {
		t.Errorf("ParentTitle/StateReason = %q/%q", got.ParentTitle(), got.StateReason())
	}
	if refs := got.BlockedByRefs(); len(refs) != 2 || refs[0].State != "CLOSED" || refs[1].State != "OPEN" {
		t.Errorf("BlockedByRefs = %+v", refs)
	}
	if refs := got.SubIssueRefs(); len(refs) != 1 || refs[0].Number != 6 || refs[0].State != "CLOSED" {
		t.Errorf("SubIssueRefs = %+v", refs)
	}
}

// The real local store is the backend the first consumer uses, so Snapshot
// must answer the same questions over a locked JSONL tracker as over a fake.
func TestSnapshotAgainstLocalStore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := local.Init(dir); err != nil {
		t.Fatal(err)
	}
	store, err := local.Open(dir, "me")
	if err != nil {
		t.Fatal(err)
	}
	create := func(title string, labels ...string) model.Issue {
		t.Helper()
		i, err := store.CreateIssue(ctx, title, "body of "+title, labels)
		if err != nil {
			t.Fatal(err)
		}
		return i
	}
	blocker := create("Blocker", "P2", "bug")
	blocked := create("Blocked", "P2", "bug")
	if err := store.AddBlockedBy(ctx, blocked.Number, blocker.Number); err != nil {
		t.Fatal(err)
	}
	claimed := create("Claimed", "P1", "bug")
	if err := store.AddAssignee(ctx, claimed.Number, "worker"); err != nil {
		t.Fatal(err)
	}
	if err := store.AddLabels(ctx, claimed.Number, []string{"in-progress"}); err != nil {
		t.Fatal(err)
	}
	epic := create("Epic", "P2", "task", "epic")
	child := create("Child", "P2", "task")
	if err := store.AddSubIssue(ctx, epic.Number, child.Number, false); err != nil {
		t.Fatal(err)
	}
	if err := store.Comment(ctx, blocker.Number, "a comment"); err != nil {
		t.Fatal(err)
	}

	snapshot, err := (&Tracker{store: store}).Snapshot(ctx, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := numbers(snapshot.Ready()), []int{blocker.Number, child.Number}; !slices.Equal(got, want) {
		t.Errorf("Ready = %v, want %v", got, want)
	}
	if got := numbers(snapshot.Blocked()); !slices.Equal(got, []int{blocked.Number}) {
		t.Errorf("Blocked = %v, want [%d]", got, blocked.Number)
	}
	if got := numbers(snapshot.Claimed()); !slices.Equal(got, []int{claimed.Number}) {
		t.Errorf("Claimed = %v, want [%d]", got, claimed.Number)
	}
	if got := numbers(snapshot.Epics()); !slices.Equal(got, []int{epic.Number}) {
		t.Errorf("Epics = %v, want [%d]", got, epic.Number)
	}
	if got := numbers(snapshot.Children(epic.Number)); !slices.Equal(got, []int{child.Number}) {
		t.Errorf("Children = %v, want [%d]", got, child.Number)
	}
	if warnings := snapshot.Warnings(); len(warnings) != 0 {
		t.Errorf("Warnings = %+v, want none", warnings)
	}

	got, err := (&Tracker{store: store}).Get(ctx, blocker.Number)
	if err != nil {
		t.Fatal(err)
	}
	if got.Body == nil || *got.Body != "body of Blocker" {
		t.Errorf("Body = %v", got.Body)
	}
	if len(got.Comments) != 1 || got.Comments[0].Body != "a comment" {
		t.Errorf("Comments = %+v", got.Comments)
	}
}

func TestSnapshotStatesScopeTheRead(t *testing.T) {
	open := fixture(1, "Open", "P2", "bug")
	closed := fixture(2, "Closed", "P2", "bug")
	closed.State = "CLOSED"
	store := &fakeClient{issues: []model.Issue{open, closed}}

	snapshot, err := Wrap(store).Snapshot(context.Background(), SnapshotOptions{States: []State{StateClosed}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := numbers(snapshot.Issues()), []int{2}; !slices.Equal(got, want) {
		t.Errorf("Issues = %v, want %v", got, want)
	}
	if got, want := snapshot.States(), []State{StateClosed}; !slices.Equal(got, want) {
		t.Errorf("States = %v, want %v", got, want)
	}
	if _, ok := snapshot.Get(1); ok {
		t.Error("Get(1) found an open issue in a closed-only snapshot")
	}

	if _, err := Wrap(store).Snapshot(context.Background(), SnapshotOptions{States: []State{"bogus"}}); err == nil {
		t.Error("expected an invalid-input error for an unknown state")
	} else {
		var invalid *InvalidInputError
		if !errors.As(err, &invalid) || invalid.Field != "states" {
			t.Errorf("error = %v, want InvalidInputError for states", err)
		}
	}
}

// Every query result is a copy: mutating what a caller is given must not
// change what the next query answers.
func TestSnapshotResultsDoNotAliasStoredState(t *testing.T) {
	issue := fixture(1, "Labeled", "P2", "bug", "area")
	issue.Assignees = []string{"alice"}
	blocker := fixture(2, "Blocker", "P2", "bug")
	blocker.BlockedBy = []model.Ref{{Number: 1, State: "OPEN"}}
	cycleA := fixture(3, "Cycle A", "P2", "bug")
	cycleA.BlockedBy = []model.Ref{{Number: 4, State: "OPEN"}}
	cycleB := fixture(4, "Cycle B", "P2", "bug")
	cycleB.BlockedBy = []model.Ref{{Number: 3, State: "OPEN"}}
	store := &fakeClient{issues: []model.Issue{issue, blocker, cycleA, cycleB}}

	snapshot, err := Wrap(store).Snapshot(context.Background(), SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := snapshot.Claimed()
	if len(got) != 1 {
		t.Fatalf("Claimed = %v", numbers(got))
	}
	got[0].Labels()[0] = "P0"
	got[0].Assignees[0] = "mallory"

	again := snapshot.Claimed()
	if again[0].Labels()[0] != "P2" {
		t.Errorf("labels changed through a query result: %v", again[0].Labels())
	}
	if again[0].Assignees[0] != "alice" {
		t.Errorf("assignees changed through a query result: %v", again[0].Assignees)
	}

	warnings := snapshot.Warnings()
	if len(warnings) != 1 || len(warnings[0].Cycle) != 3 {
		t.Fatalf("Warnings = %+v", warnings)
	}
	warnings[0].Cycle[0] = 99
	if cycle := snapshot.Warnings()[0].Cycle; cycle[0] != 3 {
		t.Errorf("warning cycle changed through a query result: %v", cycle)
	}
}
