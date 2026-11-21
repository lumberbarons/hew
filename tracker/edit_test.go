package tracker

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/local"
	"github.com/lumberbarons/hew/internal/model"
)

// point returns a pointer to v: EditRequest's optional fields carry presence,
// which is what keeps "leave alone" apart from an explicitly invalid empty
// value.
func point[T any](v T) *T { return &v }

// assertUnchanged fails when a refused call moved the stored issue. Every
// refusal in Edit and Reopen happens before the first write, so the issue has
// to be exactly as it was.
func assertUnchanged(t *testing.T, before, after model.Issue) {
	t.Helper()
	if !reflect.DeepEqual(before, after) {
		t.Errorf("refused call changed the issue:\nbefore %+v\nafter  %+v", before, after)
	}
}

// TestEditChangesEveryEditableField is the command's whole surface through the
// library: title, body, priority, type, areas and a parent move in one call.
// Exactly one priority and one type label survive, and the move only rewires
// the edited issue's own parent link — its children and its old parent's other
// children stay where they were.
func TestEditChangesEveryEditableField(t *testing.T) {
	ctx, store, issue := startFixture(t, "P3", "bug", "old-area")
	first, err := store.CreateIssue(ctx, "Epic: first", "", []string{"P2", "epic"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateIssue(ctx, "Epic: second", "", []string{"P2", "epic"})
	if err != nil {
		t.Fatal(err)
	}
	sibling, err := store.CreateIssue(ctx, "Sibling", "", []string{"P2", "task"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := store.CreateIssue(ctx, "Child", "", []string{"P2", "task"})
	if err != nil {
		t.Fatal(err)
	}
	for _, attach := range [][2]int{
		{first.Number, issue.Number},
		{first.Number, sibling.Number},
		{issue.Number, child.Number},
	} {
		if err := store.AddSubIssue(ctx, attach[0], attach[1], false); err != nil {
			t.Fatal(err)
		}
	}

	title, body, priority, typ := "Renamed", "### Goal\n\nnew\n", "P1", "task"
	result, err := WrapAs(store, BackendLocal, "actor-a").Edit(ctx, EditRequest{
		Issue:       issue.Number,
		Title:       &title,
		Body:        &body,
		Priority:    &priority,
		Type:        &typ,
		AddAreas:    []string{"new-area"},
		RemoveAreas: []string{"old-area"},
		Parent:      &second.Number,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantApplied := []string{"priority", "type", "addAreas", "removeAreas", "title", "body", "parent"}
	if !slices.Equal(result.Applied, wantApplied) {
		t.Errorf("Applied = %v, want %v", result.Applied, wantApplied)
	}

	got := storeIssue(t, ctx, store, issue.Number)
	if got.Title != title {
		t.Errorf("title = %q, want %q", got.Title, title)
	}
	if got.Body != body {
		t.Errorf("body = %q, want %q", got.Body, body)
	}
	if p, multi := got.Priority(); p != model.P1 || multi {
		t.Errorf("priority = %v (multi %v), want one P1", p, multi)
	}
	if typ, multi := got.Type(); typ != "task" || multi {
		t.Errorf("type = %q (multi %v), want one task", typ, multi)
	}
	if !slices.Equal(got.Areas(), []string{"new-area"}) {
		t.Errorf("areas = %v, want [new-area]", got.Areas())
	}
	if got.Parent == nil || got.Parent.Number != second.Number {
		t.Errorf("parent = %v, want #%d", got.Parent, second.Number)
	}
	if sub := storeIssue(t, ctx, store, first.Number).SubIssues; len(sub) != 1 || sub[0].Number != sibling.Number {
		t.Errorf("old parent's children = %v, want only #%d", sub, sibling.Number)
	}
	if sub := storeIssue(t, ctx, store, second.Number).SubIssues; len(sub) != 1 || sub[0].Number != issue.Number {
		t.Errorf("new parent's children = %v, want only #%d", sub, issue.Number)
	}
	if sub := got.SubIssues; len(sub) != 1 || sub[0].Number != child.Number {
		t.Errorf("edited issue's own children = %v, want #%d still attached", sub, child.Number)
	}
}

// Idea is a type swap like the others: the old type label goes, exactly one
// type label remains, and the priority and areas are untouched.
func TestEditSwapsToIdeaAndBack(t *testing.T) {
	ctx, store, issue := startFixture(t, "P1", "task", "area")
	tr := WrapAs(store, BackendLocal, "actor-a")
	idea, task := model.IdeaType, "task"

	if _, err := tr.Edit(ctx, EditRequest{Issue: issue.Number, Type: &idea}); err != nil {
		t.Fatal(err)
	}
	got := storeIssue(t, ctx, store, issue.Number)
	if typ, multi := got.Type(); typ != model.IdeaType || multi {
		t.Errorf("type = %q (multi %v), want one idea", typ, multi)
	}
	if p, multi := got.Priority(); p != model.P1 || multi {
		t.Errorf("priority = %v (multi %v), want one P1", p, multi)
	}
	if !got.HasLabel(model.IdeaType) || got.HasLabel("task") {
		t.Errorf("labels = %v, want idea alone as the type", got.Labels)
	}
	if !slices.Equal(got.Areas(), []string{"area"}) {
		t.Errorf("areas = %v, want the area untouched", got.Areas())
	}

	if _, err := tr.Edit(ctx, EditRequest{Issue: issue.Number, Type: &task}); err != nil {
		t.Fatal(err)
	}
	got = storeIssue(t, ctx, store, issue.Number)
	if typ, multi := got.Type(); typ != "task" || multi {
		t.Errorf("type = %q (multi %v), want one task", typ, multi)
	}
	if got.HasLabel(model.IdeaType) {
		t.Errorf("labels = %v, want the idea type label gone", got.Labels)
	}
}

// A type edit is a classification change, not a claim change: retyping a
// claimed task to an idea and a claimed idea to a task both leave the assignee
// and the in-progress label exactly where they were. Selection follows the new
// type under the usual rule for claimed work — it stays out of ready either
// way because the claim, not the type, excludes it.
func TestEditRetypesAClaimedIssueWithoutMovingTheClaim(t *testing.T) {
	for _, tc := range []struct{ from, to string }{
		{"task", model.IdeaType},
		{model.IdeaType, "task"},
	} {
		t.Run(tc.from+"->"+tc.to, func(t *testing.T) {
			ctx, store, issue := startFixture(t, "P2", tc.from)
			tr := WrapAs(store, BackendLocal, "actor-a")
			if _, err := tr.Start(ctx, issue.Number, StartOptions{ForDiscovery: tc.from == model.IdeaType}); err != nil {
				t.Fatalf("claim before retype: %v", err)
			}

			typ := tc.to
			if _, err := tr.Edit(ctx, EditRequest{Issue: issue.Number, Type: &typ}); err != nil {
				t.Fatalf("Edit to %s: %v", tc.to, err)
			}
			got := storeIssue(t, ctx, store, issue.Number)
			if read, multi := got.Type(); read != tc.to || multi {
				t.Errorf("type = %q (multi %v), want one %s", read, multi, tc.to)
			}
			if !got.HasLabel(tc.to) || got.HasLabel(tc.from) {
				t.Errorf("labels = %v, want %s alone as the type", got.Labels, tc.to)
			}
			if !slices.Equal(got.Assignees, []string{"actor-a"}) {
				t.Errorf("assignees = %v, want the claim preserved", got.Assignees)
			}
			if !got.InProgress() {
				t.Errorf("labels = %v, want in-progress preserved", got.Labels)
			}

			snapshot, err := tr.Snapshot(ctx, SnapshotOptions{})
			if err != nil {
				t.Fatal(err)
			}
			for _, ready := range snapshot.Ready() {
				if ready.Number == issue.Number {
					t.Errorf("#%d is in ready while claimed", issue.Number)
				}
			}
		})
	}
}

// An omitted request field is not a request to clear it: only the title moves.
func TestEditLeavesOmittedFieldsAlone(t *testing.T) {
	ctx, store, issue := startFixture(t, "P3", "bug", "area")
	const body = "### Goal\n\nstays\n"
	if err := store.EditBody(ctx, issue.Number, body); err != nil {
		t.Fatal(err)
	}
	parent, err := store.CreateIssue(ctx, "Epic: parent", "", []string{"P2", "epic"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddSubIssue(ctx, parent.Number, issue.Number, false); err != nil {
		t.Fatal(err)
	}
	before := storeIssue(t, ctx, store, issue.Number)

	title := "Only the title"
	result, err := WrapAs(store, BackendLocal, "actor-a").Edit(ctx, EditRequest{Issue: issue.Number, Title: &title})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.Applied, []string{"title"}) {
		t.Errorf("Applied = %v, want [title]", result.Applied)
	}
	after := storeIssue(t, ctx, store, issue.Number)
	if after.Title != title {
		t.Errorf("title = %q, want %q", after.Title, title)
	}
	if !slices.Equal(after.Labels, before.Labels) {
		t.Errorf("labels = %v, want %v", after.Labels, before.Labels)
	}
	if after.Body != before.Body {
		t.Errorf("body = %q, want %q", after.Body, before.Body)
	}
	if !reflect.DeepEqual(after.Parent, before.Parent) {
		t.Errorf("parent = %v, want %v", after.Parent, before.Parent)
	}
}

// TestEditRefusesInvalidInputBeforeWriting is the presence contract: an
// explicitly empty value is a mistake to report, never the same thing as an
// omitted field, and every refusal lands before the first write.
func TestEditRefusesInvalidInputBeforeWriting(t *testing.T) {
	for _, tc := range []struct {
		name  string
		req   func(issue int, parent int) EditRequest
		field string
		why   string
	}{
		{
			name:  "nothing to change",
			req:   func(issue, _ int) EditRequest { return EditRequest{Issue: issue} },
			field: "edit",
			why:   "nothing to change",
		},
		{
			name:  "explicitly empty title",
			req:   func(issue, _ int) EditRequest { return EditRequest{Issue: issue, Title: point("")} },
			field: "title",
			why:   "must not be empty",
		},
		{
			name:  "whitespace-only title",
			req:   func(issue, _ int) EditRequest { return EditRequest{Issue: issue, Title: point("  \t ")} },
			field: "title",
			why:   "must not be empty",
		},
		{
			name:  "multiline title",
			req:   func(issue, _ int) EditRequest { return EditRequest{Issue: issue, Title: point("two\nlines")} },
			field: "title",
			why:   "must be a single line",
		},
		{
			name:  "explicitly empty body",
			req:   func(issue, _ int) EditRequest { return EditRequest{Issue: issue, Body: point("")} },
			field: "body",
			why:   "must not be empty",
		},
		{
			name:  "whitespace-only body",
			req:   func(issue, _ int) EditRequest { return EditRequest{Issue: issue, Body: point(" \n\n ")} },
			field: "body",
			why:   "must not be empty",
		},
		{
			name:  "invalid priority",
			req:   func(issue, _ int) EditRequest { return EditRequest{Issue: issue, Priority: point("P9")} },
			field: "priority",
			why:   "must be P0..P4",
		},
		{
			name:  "explicitly empty priority",
			req:   func(issue, _ int) EditRequest { return EditRequest{Issue: issue, Priority: point("")} },
			field: "priority",
			why:   "must be P0..P4",
		},
		{
			name:  "invalid type",
			req:   func(issue, _ int) EditRequest { return EditRequest{Issue: issue, Type: point("story")} },
			field: "type",
			why:   "must be one of",
		},
		{
			name:  "explicitly empty type",
			req:   func(issue, _ int) EditRequest { return EditRequest{Issue: issue, Type: point("")} },
			field: "type",
			why:   "must be one of",
		},
		{
			name:  "blank area",
			req:   func(issue, _ int) EditRequest { return EditRequest{Issue: issue, AddAreas: []string{" "}} },
			field: "addAreas",
			why:   "must not be blank",
		},
		{
			name:  "area that is a priority label",
			req:   func(issue, _ int) EditRequest { return EditRequest{Issue: issue, AddAreas: []string{"P0"}} },
			field: "addAreas",
			why:   "priority label",
		},
		{
			name:  "area containing a comma",
			req:   func(issue, _ int) EditRequest { return EditRequest{Issue: issue, AddAreas: []string{"a,b"}} },
			field: "addAreas",
			why:   "comma",
		},
		{
			name:  "removed area that is a type label",
			req:   func(issue, _ int) EditRequest { return EditRequest{Issue: issue, RemoveAreas: []string{"bug"}} },
			field: "removeAreas",
			why:   "type label",
		},
		{
			name: "parent and no-parent together",
			req: func(issue, parent int) EditRequest {
				return EditRequest{Issue: issue, Parent: &parent, NoParent: true}
			},
			field: "parent",
			why:   "noParent",
		},
		{
			name:  "issue as its own parent",
			req:   func(issue, _ int) EditRequest { return EditRequest{Issue: issue, Parent: &issue} },
			field: "parent",
			why:   "is the issue being edited",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, store, issue := startFixture(t, "P3", "bug", "old-area")
			parent, err := store.CreateIssue(ctx, "Epic: parent", "", []string{"P2", "epic"})
			if err != nil {
				t.Fatal(err)
			}
			before := storeIssue(t, ctx, store, issue.Number)

			_, err = WrapAs(store, BackendLocal, "actor-a").Edit(ctx, tc.req(issue.Number, parent.Number))
			var invalid *InvalidInputError
			if !errors.As(err, &invalid) || invalid.Field != tc.field {
				t.Fatalf("err = %v, want *InvalidInputError on %s", err, tc.field)
			}
			if !strings.Contains(invalid.Reason, tc.why) {
				t.Errorf("reason = %q, want it to mention %q", invalid.Reason, tc.why)
			}
			assertUnchanged(t, before, storeIssue(t, ctx, store, issue.Number))
		})
	}
}

// A closed target is stale state, not intent: it is refused with the close
// reason unless the caller overrides it.
func TestEditRefusesAClosedIssueUnlessAllowed(t *testing.T) {
	ctx, store, issue := startFixture(t, "P3", "bug")
	if err := store.CloseIssue(ctx, issue.Number, gh.CloseCompleted); err != nil {
		t.Fatal(err)
	}
	priority := "P1"
	tr := WrapAs(store, BackendLocal, "actor-a")

	var closed *ClosedError
	if _, err := tr.Edit(ctx, EditRequest{Issue: issue.Number, Priority: &priority}); !errors.As(err, &closed) ||
		closed.Issue != issue.Number || closed.Reason != "COMPLETED" {
		t.Fatalf("err = %v, want a ClosedError carrying the close reason", err)
	}
	if got := storeIssue(t, ctx, store, issue.Number); got.HasLabel("P1") {
		t.Error("refused edit mutated the closed issue")
	}

	if _, err := tr.Edit(ctx, EditRequest{Issue: issue.Number, Priority: &priority, AllowClosed: true}); err != nil {
		t.Fatalf("edit with the closed override: %v", err)
	}
	if got := storeIssue(t, ctx, store, issue.Number); !got.HasLabel("P1") {
		t.Errorf("override edit did not apply: %v", got.Labels)
	}
}

func TestEditMissingTargetsAreNotFound(t *testing.T) {
	title := "Renamed"
	t.Run("issue", func(t *testing.T) {
		ctx, store, _ := startFixture(t)
		var notFound *NotFoundError
		_, err := WrapAs(store, BackendLocal, "actor-a").Edit(ctx, EditRequest{Issue: 99, Title: &title})
		if !errors.As(err, &notFound) || notFound.Issue != 99 {
			t.Fatalf("err = %v, want a NotFoundError for #99", err)
		}
	})
	t.Run("parent", func(t *testing.T) {
		ctx, store, issue := startFixture(t)
		before := storeIssue(t, ctx, store, issue.Number)
		priority := "P1"
		var notFound *NotFoundError
		_, err := WrapAs(store, BackendLocal, "actor-a").Edit(ctx, EditRequest{
			Issue: issue.Number, Priority: &priority, Parent: point(99),
		})
		if !errors.As(err, &notFound) || notFound.Issue != 99 {
			t.Fatalf("err = %v, want a NotFoundError for the parent #99", err)
		}
		// The parent is resolved before the first write, so the priority that
		// preceded it in request order did not land either.
		assertUnchanged(t, before, storeIssue(t, ctx, store, issue.Number))
	})
}

// titleFailingStore fails the title write, so the priority write before it has
// already landed: the state a partial edit has to describe.
type titleFailingStore struct {
	*local.Store
	err error
}

func (s titleFailingStore) EditTitle(context.Context, int, string) error { return s.err }

func TestEditReportsPartialApplication(t *testing.T) {
	ctx, store, issue := startFixture(t)
	boom := errors.New("title boom")
	title := "Renamed"
	priority := "P1"
	_, err := WrapAs(titleFailingStore{Store: store, err: boom}, BackendLocal, "actor-a").Edit(ctx, EditRequest{
		Issue: issue.Number, Priority: &priority, Title: &title,
	})
	var partial *PartialEditError
	if !errors.As(err, &partial) {
		t.Fatalf("err = %v, want a PartialEditError", err)
	}
	if partial.Issue != issue.Number || partial.Failed != "title" {
		t.Errorf("PartialEditError = %+v, want the issue and the failed field", partial)
	}
	if !slices.Equal(partial.Applied, []string{"priority"}) {
		t.Errorf("Applied = %v, want [priority]", partial.Applied)
	}
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the backend cause retained for errors.Is", err)
	}
	got := storeIssue(t, ctx, store, issue.Number)
	if !got.HasLabel("P1") || got.Title == title {
		t.Errorf("after the failed edit: priority labels %v, title %q; want P1 and the old title", got.Labels, got.Title)
	}
}

// Cancellation keeps its identity through the partial-edit wrapper: an
// orchestrator branches on whether to retry.
func TestEditPartialKeepsCancellationIdentity(t *testing.T) {
	ctx, store, issue := startFixture(t)
	priority, title := "P1", "Renamed"
	_, err := WrapAs(titleFailingStore{Store: store, err: context.Canceled}, BackendLocal, "actor-a").Edit(ctx, EditRequest{
		Issue: issue.Number, Priority: &priority, Title: &title,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want errors.Is(err, context.Canceled)", err)
	}
	var partial *PartialEditError
	if !errors.As(err, &partial) {
		t.Errorf("err = %v, want the partial-edit context too", err)
	}
}

// labelFailingStore fails RemoveLabel for the named labels, the first half of
// a type swap.
type labelFailingStore struct {
	*local.Store
	labels []string
	err    error
}

func (s labelFailingStore) RemoveLabel(ctx context.Context, number int, label string) error {
	if slices.ContainsFunc(s.labels, func(l string) bool { return strings.EqualFold(l, label) }) {
		return s.err
	}
	return s.Store.RemoveLabel(ctx, number, label)
}

// A failed type swap after the priority landed is a partial edit at the type
// field, and a failed swap with nothing before it is the plain backend error.
func TestEditReportsAFailedTypeSwap(t *testing.T) {
	ctx, store, issue := startFixture(t, "P3", "bug", "enhancement")
	boom := errors.New("label boom")
	failing := labelFailingStore{Store: store, labels: []string{"bug", "enhancement"}, err: boom}
	priority, typ := "P1", "task"
	_, err := WrapAs(failing, BackendLocal, "actor-a").Edit(ctx, EditRequest{
		Issue: issue.Number, Priority: &priority, Type: &typ,
	})
	var partial *PartialEditError
	if !errors.As(err, &partial) || partial.Failed != "type" || !slices.Equal(partial.Applied, []string{"priority"}) {
		t.Fatalf("err = %v, want a PartialEditError at the type field after the priority", err)
	}
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the backend cause retained", err)
	}

	_, err = WrapAs(failing, BackendLocal, "actor-a").Edit(ctx, EditRequest{
		Issue: issue.Number, Type: &typ,
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the plain backend error when nothing landed first", err)
	}
	if errors.As(err, &partial) {
		t.Errorf("err = %v, reported a partial edit when nothing landed", err)
	}
}

// A failed area removal after the priority landed names the removal field and
// the priority that is already applied.
func TestEditReportsAFailedAreaRemoval(t *testing.T) {
	ctx, store, issue := startFixture(t, "P2", "bug", "old-area")
	boom := errors.New("area boom")
	failing := labelFailingStore{Store: store, labels: []string{"old-area"}, err: boom}
	priority := "P1"
	_, err := WrapAs(failing, BackendLocal, "actor-a").Edit(ctx, EditRequest{
		Issue: issue.Number, Priority: &priority, RemoveAreas: []string{"old-area"},
	})
	var partial *PartialEditError
	if !errors.As(err, &partial) || partial.Failed != "removeAreas" || !slices.Equal(partial.Applied, []string{"priority"}) {
		t.Fatalf("err = %v, want a PartialEditError at removeAreas after the priority", err)
	}
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the backend cause retained", err)
	}
}

// The partial errors are what a caller prints; their message names the state
// that survived rather than only the backend failure.
func TestPartialEditMessageNamesWhatSurvived(t *testing.T) {
	msg := (&PartialEditError{Issue: 7, Applied: []string{"priority", "type"}, Failed: "title", Err: errors.New("boom")}).Error()
	for _, want := range []string{"#7", "priority, type", "title", "boom"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message = %q, want it to contain %q", msg, want)
		}
	}
}

// An issue carrying contradicting labels is repaired to exactly one priority
// and one type, not stacked with another pair.
func TestEditKeepsExactlyOnePriorityAndTypeLabel(t *testing.T) {
	ctx, store, issue := startFixture(t, "P1", "P3", "bug", "enhancement", "area")
	priority, typ := "P2", "task"
	if _, err := WrapAs(store, BackendLocal, "actor-a").Edit(ctx, EditRequest{
		Issue: issue.Number, Priority: &priority, Type: &typ,
	}); err != nil {
		t.Fatal(err)
	}
	got := storeIssue(t, ctx, store, issue.Number)
	if p, multi := got.Priority(); p != model.P2 || multi {
		t.Errorf("priority = %v (multi %v), want one P2", p, multi)
	}
	if typ, multi := got.Type(); typ != "task" || multi {
		t.Errorf("type = %q (multi %v), want one task", typ, multi)
	}
	if !slices.Equal(got.Areas(), []string{"area"}) {
		t.Errorf("areas = %v, want the unrelated area untouched", got.Areas())
	}
}

// Stray-case convention labels are the same label, so re-asserting them must
// neither lose the label nor stack a second one.
func TestEditTakesStrayCaseConventionLabelsAsTheSameLabel(t *testing.T) {
	ctx, store, issue := startFixture(t, "p2", "Bug", "task")
	priority, typ := "P2", "bug"
	if _, err := WrapAs(store, BackendLocal, "actor-a").Edit(ctx, EditRequest{
		Issue: issue.Number, Priority: &priority, Type: &typ,
	}); err != nil {
		t.Fatal(err)
	}
	got := storeIssue(t, ctx, store, issue.Number)
	if p, multi := got.Priority(); p != model.P2 || multi {
		t.Errorf("priority = %v (multi %v), want one P2", p, multi)
	}
	if typ, multi := got.Type(); typ != "bug" || multi {
		t.Errorf("type = %q (multi %v), want one bug", typ, multi)
	}
}

func TestEditNoParent(t *testing.T) {
	t.Run("removes an existing parent", func(t *testing.T) {
		ctx, store, issue := startFixture(t)
		parent, err := store.CreateIssue(ctx, "Epic: parent", "", []string{"P2", "epic"})
		if err != nil {
			t.Fatal(err)
		}
		sibling, err := store.CreateIssue(ctx, "Sibling", "", []string{"P2", "task"})
		if err != nil {
			t.Fatal(err)
		}
		for _, child := range []int{issue.Number, sibling.Number} {
			if err := store.AddSubIssue(ctx, parent.Number, child, false); err != nil {
				t.Fatal(err)
			}
		}

		result, err := WrapAs(store, BackendLocal, "actor-a").Edit(ctx, EditRequest{Issue: issue.Number, NoParent: true})
		if err != nil {
			t.Fatal(err)
		}
		if result.ParentNoop {
			t.Error("ParentNoop = true for an issue that had a parent")
		}
		if !slices.Equal(result.Applied, []string{"noParent"}) {
			t.Errorf("Applied = %v, want [noParent]", result.Applied)
		}
		if got := storeIssue(t, ctx, store, issue.Number); got.Parent != nil {
			t.Errorf("parent = %v, want it removed", got.Parent)
		}
		if sub := storeIssue(t, ctx, store, parent.Number).SubIssues; len(sub) != 1 || sub[0].Number != sibling.Number {
			t.Errorf("old parent's children = %v, want only the sibling", sub)
		}
	})

	t.Run("is a no-op without one", func(t *testing.T) {
		ctx, store, issue := startFixture(t)
		before := storeIssue(t, ctx, store, issue.Number)
		result, err := WrapAs(store, BackendLocal, "actor-a").Edit(ctx, EditRequest{Issue: issue.Number, NoParent: true})
		if err != nil {
			t.Fatal(err)
		}
		if !result.ParentNoop {
			t.Error("ParentNoop = false for an issue with no parent")
		}
		if len(result.Applied) != 0 {
			t.Errorf("Applied = %v, want nothing written", result.Applied)
		}
		assertUnchanged(t, before, storeIssue(t, ctx, store, issue.Number))
	})
}

// A hierarchy cannot be retyped as an idea while the parent, children or epic
// marker that contradicts the type would remain. Each refusal happens before
// the first write, so the issue is untouched.
func TestEditRefusesToRetypeAHierarchyAsIdea(t *testing.T) {
	t.Run("a child of an epic", func(t *testing.T) {
		ctx, store, issue := startFixture(t)
		parent, err := store.CreateIssue(ctx, "Epic: parent", "", []string{"P2", model.EpicLabel})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.AddSubIssue(ctx, parent.Number, issue.Number, false); err != nil {
			t.Fatal(err)
		}
		before := storeIssue(t, ctx, store, issue.Number)
		idea := model.IdeaType
		var invalid *InvalidInputError
		if _, err := WrapAs(store, BackendLocal, "actor-a").Edit(ctx, EditRequest{Issue: issue.Number, Type: &idea}); !errors.As(err, &invalid) {
			t.Fatalf("Edit = %v, want an *InvalidInputError", err)
		}
		if invalid.Field != "type" || !strings.Contains(invalid.Reason, "child of #") {
			t.Errorf("InvalidInputError = %+v, want a type refusal naming the parent", invalid)
		}
		assertUnchanged(t, before, storeIssue(t, ctx, store, issue.Number))
	})

	t.Run("an issue with children", func(t *testing.T) {
		ctx, store, issue := startFixture(t)
		child, err := store.CreateIssue(ctx, "Child", "", []string{"P2", "task"})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.AddSubIssue(ctx, issue.Number, child.Number, false); err != nil {
			t.Fatal(err)
		}
		before := storeIssue(t, ctx, store, issue.Number)
		idea := model.IdeaType
		var invalid *InvalidInputError
		if _, err := WrapAs(store, BackendLocal, "actor-a").Edit(ctx, EditRequest{Issue: issue.Number, Type: &idea}); !errors.As(err, &invalid) {
			t.Fatalf("Edit = %v, want an *InvalidInputError", err)
		}
		if invalid.Field != "type" || !strings.Contains(invalid.Reason, "sub-issues") {
			t.Errorf("InvalidInputError = %+v, want a type refusal naming the children", invalid)
		}
		assertUnchanged(t, before, storeIssue(t, ctx, store, issue.Number))
	})

	t.Run("a marked epic", func(t *testing.T) {
		ctx, store, issue := startFixture(t, "P2", "task", model.EpicLabel)
		before := storeIssue(t, ctx, store, issue.Number)
		idea := model.IdeaType
		var invalid *InvalidInputError
		if _, err := WrapAs(store, BackendLocal, "actor-a").Edit(ctx, EditRequest{Issue: issue.Number, Type: &idea}); !errors.As(err, &invalid) {
			t.Fatalf("Edit = %v, want an *InvalidInputError", err)
		}
		if invalid.Field != "type" || !strings.Contains(invalid.Reason, "epic label") {
			t.Errorf("InvalidInputError = %+v, want a type refusal naming the marker", invalid)
		}
		assertUnchanged(t, before, storeIssue(t, ctx, store, issue.Number))
	})
}

// A maintainer resolves an existing overlap the same way the read path keeps
// it visible: detach the relationship or change the type.
func TestEditResolvesAnIdeaOverlap(t *testing.T) {
	t.Run("detaching the parent lets the child become an idea", func(t *testing.T) {
		ctx, store, issue := startFixture(t)
		parent, err := store.CreateIssue(ctx, "Epic: parent", "", []string{"P2", model.EpicLabel})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.AddSubIssue(ctx, parent.Number, issue.Number, false); err != nil {
			t.Fatal(err)
		}
		idea := model.IdeaType
		if _, err := WrapAs(store, BackendLocal, "actor-a").Edit(ctx, EditRequest{
			Issue: issue.Number, Type: &idea, NoParent: true,
		}); err != nil {
			t.Fatalf("Edit: %v", err)
		}
		got := storeIssue(t, ctx, store, issue.Number)
		if typ, _ := got.Type(); typ != model.IdeaType {
			t.Errorf("type = %q, want idea", typ)
		}
		if got.Parent != nil {
			t.Errorf("parent = %+v, want it detached", got.Parent)
		}
	})

	t.Run("changing a marked idea to task keeps the marker and clears the conflict", func(t *testing.T) {
		ctx, store, issue := startFixture(t, "P2", model.IdeaType, model.EpicLabel)
		task := "task"
		if _, err := WrapAs(store, BackendLocal, "actor-a").Edit(ctx, EditRequest{Issue: issue.Number, Type: &task}); err != nil {
			t.Fatalf("Edit: %v", err)
		}
		got := storeIssue(t, ctx, store, issue.Number)
		if typ, _ := got.Type(); typ != "task" {
			t.Errorf("type = %q, want task", typ)
		}
		if !got.IsEpic() || !got.HasEpicMarker() {
			t.Error("the epic marker was not kept, want the hierarchy still visible")
		}
	})
}

// A new parent relationship is refused when either end is an idea: an idea is
// associated with an epic through references, not the child hierarchy.
func TestEditRefusesNewIdeaParentRelationships(t *testing.T) {
	t.Run("an idea as the child", func(t *testing.T) {
		ctx, store, issue := startFixture(t, "P2", model.IdeaType)
		parent, err := store.CreateIssue(ctx, "Epic: parent", "", []string{"P2", model.EpicLabel})
		if err != nil {
			t.Fatal(err)
		}
		before := storeIssue(t, ctx, store, issue.Number)
		var invalid *InvalidInputError
		if _, err := WrapAs(store, BackendLocal, "actor-a").Edit(ctx, EditRequest{
			Issue: issue.Number, Parent: &parent.Number,
		}); !errors.As(err, &invalid) {
			t.Fatalf("Edit = %v, want an *InvalidInputError", err)
		}
		if invalid.Field != "parent" || !strings.Contains(invalid.Reason, "idea cannot be an epic child") {
			t.Errorf("InvalidInputError = %+v, want a parent refusal naming the idea rule", invalid)
		}
		assertUnchanged(t, before, storeIssue(t, ctx, store, issue.Number))
	})

	t.Run("an idea as the parent", func(t *testing.T) {
		ctx, store, issue := startFixture(t)
		parent, err := store.CreateIssue(ctx, "Consider a cache", "", []string{"P2", model.IdeaType})
		if err != nil {
			t.Fatal(err)
		}
		before := storeIssue(t, ctx, store, issue.Number)
		var invalid *InvalidInputError
		if _, err := WrapAs(store, BackendLocal, "actor-a").Edit(ctx, EditRequest{
			Issue: issue.Number, Parent: &parent.Number,
		}); !errors.As(err, &invalid) {
			t.Fatalf("Edit = %v, want an *InvalidInputError", err)
		}
		if invalid.Field != "parent" || !strings.Contains(invalid.Reason, "cannot be an epic parent") {
			t.Errorf("InvalidInputError = %+v, want a parent refusal naming the idea parent", invalid)
		}
		assertUnchanged(t, before, storeIssue(t, ctx, store, issue.Number))
	})
}

// An idea attached outside hew is repaired through the ordinary detach: reads
// keep showing the overlap, and the edit that removes it is allowed.
func TestEditDetachesAnExternallyAttachedIdeaChild(t *testing.T) {
	ctx, store, epic := startFixture(t, "P2", model.EpicLabel)
	idea, err := store.CreateIssue(ctx, "Consider a cache", "", []string{"P1", model.IdeaType})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddSubIssue(ctx, epic.Number, idea.Number, false); err != nil {
		t.Fatal(err)
	}
	if _, err := WrapAs(store, BackendLocal, "actor-a").Edit(ctx, EditRequest{Issue: idea.Number, NoParent: true}); err != nil {
		t.Fatalf("Edit NoParent on an idea child: %v", err)
	}
	got := storeIssue(t, ctx, store, idea.Number)
	if got.Parent != nil {
		t.Errorf("parent = %+v, want the external attachment removed", got.Parent)
	}
	if typ, _ := got.Type(); typ != model.IdeaType {
		t.Errorf("type = %q, want the idea kept", typ)
	}
}

// A parent the issue is already under is the requested state: the write is a
// no-op the backend accepts, and the remaining edits still land.
func TestEditToTheSameParent(t *testing.T) {
	ctx, store, issue := startFixture(t)
	parent, err := store.CreateIssue(ctx, "Epic: parent", "", []string{"P2", "epic"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddSubIssue(ctx, parent.Number, issue.Number, false); err != nil {
		t.Fatal(err)
	}
	title := "Renamed"
	if _, err := WrapAs(store, BackendLocal, "actor-a").Edit(ctx, EditRequest{
		Issue: issue.Number, Parent: &parent.Number, Title: &title,
	}); err != nil {
		t.Fatal(err)
	}
	got := storeIssue(t, ctx, store, issue.Number)
	if got.Parent == nil || got.Parent.Number != parent.Number {
		t.Errorf("parent = %v, want #%d", got.Parent, parent.Number)
	}
	if got.Title != title {
		t.Errorf("title = %q, want %q", got.Title, title)
	}
}
