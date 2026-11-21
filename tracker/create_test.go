package tracker

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/local"
	"github.com/lumberbarons/hew/internal/model"
)

// createTracker opens an empty local tracker: the real store is what exercises
// the edge writes, label handling and reads a consumer gets.
func createTracker(t *testing.T) (context.Context, *Tracker, *local.Store) {
	t.Helper()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), local.DirName)
	if err := local.Init(dir); err != nil {
		t.Fatal(err)
	}
	store, err := local.Open(dir, "actor-a")
	if err != nil {
		t.Fatal(err)
	}
	return ctx, WrapAs(store, BackendLocal, "actor-a"), store
}

// seed files an issue directly through the store, so a test starts from state
// Create did not produce.
func seed(t *testing.T, store *local.Store, title string, labels ...string) model.Issue {
	t.Helper()
	issue, err := store.CreateIssue(context.Background(), title, "", labels)
	if err != nil {
		t.Fatal(err)
	}
	return issue
}

// allIssues counts every issue in either state, so "nothing was written" means
// nothing at all.
func allIssues(t *testing.T, store *local.Store) []model.Issue {
	t.Helper()
	all, err := store.ListIssues(context.Background(), []gh.IssueState{gh.StateOpen, gh.StateClosed})
	if err != nil {
		t.Fatal(err)
	}
	return all
}

// faultStore delegates to a real local store and fails selected methods, so a
// partial write lands in the tracker and is inspectable afterwards. At is the
// 1-based call to fail on; 0 fails every call.
type faultStore struct {
	*local.Store
	fail  map[string]injection
	calls map[string]int
}

func newFaultStore(t *testing.T) *faultStore {
	t.Helper()
	_, _, store := createTracker(t)
	return &faultStore{Store: store, calls: map[string]int{}}
}

func (s *faultStore) inject(method string, err error, at int) *faultStore {
	if s.fail == nil {
		s.fail = map[string]injection{}
	}
	s.fail[method] = injection{err: err, at: at}
	return s
}

func (s *faultStore) take(method string) error {
	s.calls[method]++
	in, ok := s.fail[method]
	if !ok || (in.at != 0 && in.at != s.calls[method]) {
		return nil
	}
	return in.err
}

func (s *faultStore) GetIssue(ctx context.Context, number int) (model.Issue, error) {
	if err := s.take("GetIssue"); err != nil {
		return model.Issue{}, err
	}
	return s.Store.GetIssue(ctx, number)
}

func (s *faultStore) CreateIssue(ctx context.Context, title, body string, labels []string) (model.Issue, error) {
	if err := s.take("CreateIssue"); err != nil {
		return model.Issue{}, err
	}
	return s.Store.CreateIssue(ctx, title, body, labels)
}

func (s *faultStore) AddAssignee(ctx context.Context, number int, login string) error {
	if err := s.take("AddAssignee"); err != nil {
		return err
	}
	return s.Store.AddAssignee(ctx, number, login)
}

func (s *faultStore) AddBlockedBy(ctx context.Context, number, blocking int) error {
	if err := s.take("AddBlockedBy"); err != nil {
		return err
	}
	return s.Store.AddBlockedBy(ctx, number, blocking)
}

func (s *faultStore) AddSubIssue(ctx context.Context, parent, child int, replace bool) error {
	if err := s.take("AddSubIssue"); err != nil {
		return err
	}
	return s.Store.AddSubIssue(ctx, parent, child, replace)
}

// An idea is created through the same path as the implementation types: one
// priority label, one type label, and a read-back that agrees. The default
// priority applies when the request names none.
func TestCreateFilesAnIdea(t *testing.T) {
	ctx, tr, _ := createTracker(t)
	result, err := tr.Create(ctx, CreateRequest{Title: "Consider a cache", Type: model.IdeaType})
	if err != nil {
		t.Fatal(err)
	}
	if result.Issue.Priority == nil || *result.Issue.Priority != "P2" ||
		result.Issue.Type == nil || *result.Issue.Type != model.IdeaType {
		t.Fatalf("Issue = %+v, want P2/idea", result.Issue)
	}
	if got, want := result.Issue.Labels(), []string{"P2", model.IdeaType}; !slices.Equal(got, want) {
		t.Errorf("Labels = %v, want exactly %v", got, want)
	}

	read, err := tr.Get(ctx, result.Issue.Number)
	if err != nil {
		t.Fatal(err)
	}
	if read.Type == nil || *read.Type != model.IdeaType || read.Priority == nil || *read.Priority != "P2" {
		t.Errorf("read back = %v/%v, want idea/P2", read.Type, read.Priority)
	}
	if got, want := read.Labels(), []string{"P2", model.IdeaType}; !slices.Equal(got, want) {
		t.Errorf("read labels = %v, want exactly %v", got, want)
	}
	if len(read.Areas) != 0 {
		t.Errorf("Areas = %v, want none: idea is a type, not an area", read.Areas)
	}
}

// Capture cannot claim the issue it files: an idea create carrying an assignee
// is refused before the first write, through both the create path and the
// no-write pre-flight, so no issue exists to release.
func TestCreateRefusesAnAssigneeOnAnIdea(t *testing.T) {
	ctx, tr, store := createTracker(t)
	req := CreateRequest{Title: "Consider a cache", Type: model.IdeaType, Assignees: []string{"octocat"}}

	var invalid *InvalidInputError
	if _, err := tr.Create(ctx, req); !errors.As(err, &invalid) {
		t.Fatalf("create = %v, want an InvalidInputError", err)
	}
	if invalid.Field != "assignees" || !strings.Contains(invalid.Reason, "idea") {
		t.Errorf("InvalidInputError = %+v, want the assignees field refused for an idea", invalid)
	}
	if got := allIssues(t, store); len(got) != 0 {
		t.Errorf("refused create wrote %d issue(s)", len(got))
	}

	invalid = nil
	if err := tr.CheckCreate(ctx, req); !errors.As(err, &invalid) || invalid.Field != "assignees" {
		t.Errorf("CheckCreate = %v, want the same assignees refusal", err)
	}

	// The same assignee on a task is an ordinary claim at creation.
	if _, err := tr.Create(ctx, CreateRequest{Title: "Do the thing", Type: "task", Assignees: []string{"octocat"}}); err != nil {
		t.Errorf("task create with an assignee = %v, want it accepted", err)
	}
}

// The new type is a reserved area name on every write, so a create cannot
// manufacture a second type out of its areas.
func TestCreateRefusesIdeaAsAnArea(t *testing.T) {
	ctx, tr, store := createTracker(t)
	_, err := tr.Create(ctx, CreateRequest{Title: "T", Type: "task", Areas: []string{model.IdeaType}})
	var invalid *InvalidInputError
	if !errors.As(err, &invalid) || invalid.Field != "areas" || !strings.Contains(invalid.Reason, "type label") {
		t.Fatalf("err = %v, want an areas refusal naming the type label", err)
	}
	if got := allIssues(t, store); len(got) != 0 {
		t.Errorf("refused create wrote %d issue(s)", len(got))
	}
}

func TestCreateFilesAnIssueWithTheConventions(t *testing.T) {
	ctx, tr, store := createTracker(t)
	composed, err := ComposeBody(Body{Sections: Sections{
		Goal:     "ship it",
		DoneWhen: []string{"tests pass"},
	}})
	if err != nil {
		t.Fatal(err)
	}

	result, err := tr.Create(ctx, CreateRequest{
		Title:    "Typed create",
		Type:     "enhancement",
		Priority: "P1",
		Areas:    []string{" render ", "api", "render"},
		Body: Body{Sections: Sections{
			Goal:     "ship it",
			DoneWhen: []string{"tests pass"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if result.Issue.Number == 0 || result.Issue.Title != "Typed create" || result.Issue.State != "open" {
		t.Errorf("Issue = %+v, want the filed issue", result.Issue)
	}
	if result.Issue.Priority == nil || *result.Issue.Priority != "P1" {
		t.Errorf("Priority = %v from %v", result.Issue.Priority, result.Issue.Labels())
	}
	if result.Issue.Type == nil || *result.Issue.Type != "enhancement" {
		t.Errorf("Type = %v", result.Issue.Type)
	}
	if got, want := result.Issue.Areas, []string{"render", "api"}; !slices.Equal(got, want) {
		t.Errorf("Areas = %v, want %v", got, want)
	}
	if result.Issue.Body == nil || *result.Issue.Body != composed.Body {
		t.Errorf("Body = %v, want %q", result.Issue.Body, composed.Body)
	}
	if len(result.Edges) != 0 {
		t.Errorf("Edges = %v, want none", result.Edges)
	}
	if len(result.Findings) != 0 {
		t.Errorf("Findings = %v, want none", result.Findings)
	}

	filed, err := tr.Get(ctx, result.Issue.Number)
	if err != nil {
		t.Fatal(err)
	}
	if filed.Title != "Typed create" || filed.Body == nil || *filed.Body != composed.Body {
		t.Errorf("Get = %+v, want the created issue", filed)
	}
	if len(allIssues(t, store)) != 1 {
		t.Errorf("tracker holds %d issues, want one", len(allIssues(t, store)))
	}
}

func TestCreateAttachesRelationshipsAndPreservesAssignees(t *testing.T) {
	ctx, tr, store := createTracker(t)
	blocker := seed(t, store, "Blocker", "P2", "bug")
	secondBlocker := seed(t, store, "Second blocker", "P2", "bug")
	parent := seed(t, store, "Epic: parent", "P2", "epic")
	source := seed(t, store, "Source", "P2", "task")

	result, err := tr.Create(ctx, CreateRequest{
		Title:          "Child work",
		Type:           "task",
		BlockedBy:      []int{blocker.Number, secondBlocker.Number},
		Parent:         parent.Number,
		DiscoveredFrom: source.Number,
		Assignees:      []string{"alice", "bob"},
	})
	if err != nil {
		t.Fatal(err)
	}

	wantEdges := []CreatedEdge{
		{Kind: BlockedByEdge, To: blocker.Number},
		{Kind: BlockedByEdge, To: secondBlocker.Number},
		{Kind: ParentEdge, To: parent.Number},
	}
	if !slices.Equal(result.Edges, wantEdges) {
		t.Errorf("Edges = %v, want %v", result.Edges, wantEdges)
	}
	if got, want := result.Issue.BlockedBy, []int{blocker.Number, secondBlocker.Number}; !slices.Equal(got, want) {
		t.Errorf("BlockedBy = %v, want %v", got, want)
	}
	if result.Issue.Parent == nil || *result.Issue.Parent != parent.Number {
		t.Errorf("Parent = %v", result.Issue.Parent)
	}
	if got, want := result.Issue.Assignees, []string{"alice", "bob"}; !slices.Equal(got, want) {
		t.Errorf("Assignees = %v, want %v", got, want)
	}
	// Assignees is a claim: both halves are what the claim guard reads.
	if !result.Issue.HasLabel(model.InProgressLabel) || !result.Issue.InProgress {
		t.Errorf("Labels = %v, want the in-progress label for an assigned issue", result.Issue.Labels())
	}
	if result.Issue.ParentTitle() != parent.Title {
		t.Errorf("ParentTitle = %q, want %q", result.Issue.ParentTitle(), parent.Title)
	}
	if result.Issue.Body == nil || !strings.HasSuffix(*result.Issue.Body, "Discovered while working on #"+strconv.Itoa(source.Number)) {
		t.Errorf("Body = %v, want the discovered-from link", result.Issue.Body)
	}

	filed, err := tr.Get(ctx, result.Issue.Number)
	if err != nil {
		t.Fatal(err)
	}
	if len(filed.BlockedBy) != 2 || filed.Parent == nil || *filed.Parent != parent.Number {
		t.Errorf("Get = %+v, want both blockers and the parent", filed)
	}
	if got, want := filed.Assignees, []string{"alice", "bob"}; !slices.Equal(got, want) {
		t.Errorf("Get assignees = %v, want %v", got, want)
	}
}

func TestCreateAcceptsAClosedParentAndRejectsAClosedBlocker(t *testing.T) {
	ctx, tr, store := createTracker(t)
	closed := seed(t, store, "Done", "P2", "bug")
	if err := store.CloseIssue(ctx, closed.Number, gh.CloseNotPlanned); err != nil {
		t.Fatal(err)
	}

	if _, err := tr.Create(ctx, CreateRequest{Title: "T", Type: "bug", Parent: closed.Number, DiscoveredFrom: closed.Number}); err != nil {
		t.Fatalf("closed parent and discovered-from refused: %v", err)
	}

	before := len(allIssues(t, store))
	_, err := tr.Create(ctx, CreateRequest{Title: "T", Type: "bug", BlockedBy: []int{closed.Number}})
	var closedErr *ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("err = %T (%v), want *tracker.ClosedError", err, err)
	}
	if closedErr.Issue != closed.Number || closedErr.Reason != string(gh.CloseNotPlanned) {
		t.Errorf("ClosedError = %+v, want the blocker and its close reason", closedErr)
	}
	if got := len(allIssues(t, store)); got != before {
		t.Errorf("refused create filed %d issue(s)", got-before)
	}
}

func TestCreateRefusesInvalidInputBeforeWriting(t *testing.T) {
	ctx, tr, store := createTracker(t)
	for _, tc := range []struct {
		name  string
		req   CreateRequest
		field string
	}{
		{name: "blank title", req: CreateRequest{Title: "  ", Type: "bug"}, field: "title"},
		{name: "multi-line title", req: CreateRequest{Title: "a\nb", Type: "bug"}, field: "title"},
		{name: "missing type", req: CreateRequest{Title: "T"}, field: "type"},
		{name: "unknown type", req: CreateRequest{Title: "T", Type: "story"}, field: "type"},
		{name: "epic type", req: CreateRequest{Title: "T", Type: PlanTypeEpic}, field: "type"},
		{name: "bad priority", req: CreateRequest{Title: "T", Type: "bug", Priority: "P9"}, field: "priority"},
		{name: "blank area", req: CreateRequest{Title: "T", Type: "bug", Areas: []string{" "}}, field: "areas"},
		{name: "comma area", req: CreateRequest{Title: "T", Type: "bug", Areas: []string{"x,y"}}, field: "areas"},
		{name: "convention area", req: CreateRequest{Title: "T", Type: "bug", Areas: []string{"P0"}}, field: "areas"},
		{name: "blank assignee", req: CreateRequest{Title: "T", Type: "bug", Assignees: []string{" "}}, field: "assignees"},
		{name: "negative parent", req: CreateRequest{Title: "T", Type: "bug", Parent: -1}, field: "parent"},
		{name: "negative discovered-from", req: CreateRequest{Title: "T", Type: "bug", DiscoveredFrom: -1}, field: "discoveredFrom"},
		{name: "non-positive blocker", req: CreateRequest{Title: "T", Type: "bug", BlockedBy: []int{0}}, field: "blockedBy"},
		{
			name:  "conflicting body sources",
			req:   CreateRequest{Title: "T", Type: "bug", Body: Body{Sections: Sections{Goal: "g"}, Text: "text"}},
			field: "body",
		},
		{
			name:  "conflicting discovered-from",
			req:   CreateRequest{Title: "T", Type: "bug", DiscoveredFrom: 1, Body: Body{DiscoveredFrom: 2}},
			field: "discoveredFrom",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tr.Create(ctx, tc.req)
			var invalid *InvalidInputError
			if !errors.As(err, &invalid) {
				t.Fatalf("err = %T (%v), want *tracker.InvalidInputError", err, err)
			}
			if invalid.Field != tc.field {
				t.Errorf("Field = %q, want %q (%v)", invalid.Field, tc.field, err)
			}
			if got := allIssues(t, store); len(got) != 0 {
				t.Errorf("refused create wrote %d issue(s)", len(got))
			}
		})
	}
}

func TestCreateRefusesMissingReferencesBeforeWriting(t *testing.T) {
	ctx, tr, store := createTracker(t)
	const missing = 999
	for _, tc := range []struct {
		name string
		req  CreateRequest
	}{
		{name: "parent", req: CreateRequest{Title: "T", Type: "bug", Parent: missing}},
		{name: "discovered-from", req: CreateRequest{Title: "T", Type: "bug", DiscoveredFrom: missing}},
		{name: "blocked-by", req: CreateRequest{Title: "T", Type: "bug", BlockedBy: []int{missing}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tr.Create(ctx, tc.req)
			var notFound *NotFoundError
			if !errors.As(err, &notFound) {
				t.Fatalf("err = %T (%v), want *tracker.NotFoundError", err, err)
			}
			if notFound.Issue != missing {
				t.Errorf("Issue = %d, want %d", notFound.Issue, missing)
			}
			if got := allIssues(t, store); len(got) != 0 {
				t.Errorf("refused create wrote %d issue(s)", len(got))
			}
		})
	}
}

func TestCreateDeduplicatesReferences(t *testing.T) {
	ctx, tr, store := createTracker(t)
	blocker := seed(t, store, "Blocker", "P2", "bug")

	result, err := tr.Create(ctx, CreateRequest{
		Title:     "T",
		Type:      "bug",
		BlockedBy: []int{blocker.Number, blocker.Number},
		Assignees: []string{"alice", " alice ", "bob"},
		Areas:     []string{"api", " api "},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Edges) != 1 {
		t.Errorf("Edges = %v, want one blocker edge", result.Edges)
	}
	if got := result.Issue.BlockedBy; !slices.Equal(got, []int{blocker.Number}) {
		t.Errorf("BlockedBy = %v", got)
	}
	if got := result.Issue.Assignees; !slices.Equal(got, []string{"alice", "bob"}) {
		t.Errorf("Assignees = %v, want deduplicated logins", got)
	}
	if got := result.Issue.Areas; !slices.Equal(got, []string{"api"}) {
		t.Errorf("Areas = %v, want one area", got)
	}
}

func TestCreateReportsPartialFailures(t *testing.T) {
	ctx := context.Background()

	t.Run("assignee", func(t *testing.T) {
		store := newFaultStore(t)
		store.inject("AddAssignee", errors.New("boom"), 2)
		tr := WrapAs(store, BackendLocal, "actor-a")

		_, err := tr.Create(ctx, CreateRequest{Title: "T", Type: "bug", Assignees: []string{"alice", "bob"}})
		var partial *PartialCreateError
		if !errors.As(err, &partial) {
			t.Fatalf("err = %T (%v), want *tracker.PartialCreateError", err, err)
		}
		if partial.Stage != CreateStageAssignees || partial.Issue.Number == 0 {
			t.Errorf("PartialCreateError = %+v, want the assignee stage and the created issue", partial)
		}
		if partial.Err == nil {
			t.Errorf("Err = %v, want the backend cause", partial.Err)
		}
		filed, getErr := tr.Get(ctx, partial.Issue.Number)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if got, want := filed.Assignees, []string{"alice"}; !slices.Equal(got, want) {
			t.Errorf("Assignees = %v, want %v", got, want)
		}
	})

	t.Run("blocked-by", func(t *testing.T) {
		store := newFaultStore(t)
		first := seed(t, store.Store, "First", "P2", "bug")
		second := seed(t, store.Store, "Second", "P2", "bug")
		third := seed(t, store.Store, "Third", "P2", "bug")
		store.inject("AddBlockedBy", errors.New("boom"), 2)
		tr := WrapAs(store, BackendLocal, "actor-a")

		_, err := tr.Create(ctx, CreateRequest{
			Title: "T", Type: "bug",
			BlockedBy: []int{first.Number, second.Number, third.Number},
		})
		var partial *PartialCreateError
		if !errors.As(err, &partial) {
			t.Fatalf("err = %T (%v), want *tracker.PartialCreateError", err, err)
		}
		if partial.Stage != CreateStageBlockedBy {
			t.Errorf("Stage = %q, want %q", partial.Stage, CreateStageBlockedBy)
		}
		if !slices.Equal(partial.Applied, []CreatedEdge{{Kind: BlockedByEdge, To: first.Number}}) {
			t.Errorf("Applied = %v, want the first blocker", partial.Applied)
		}
		if partial.Failed != (CreatedEdge{Kind: BlockedByEdge, To: second.Number}) {
			t.Errorf("Failed = %+v, want the second blocker", partial.Failed)
		}
		if !slices.Equal(partial.NotAttempted, []CreatedEdge{{Kind: BlockedByEdge, To: third.Number}}) {
			t.Errorf("NotAttempted = %v, want the third blocker", partial.NotAttempted)
		}
		filed, getErr := tr.Get(ctx, partial.Issue.Number)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if len(filed.BlockedBy) != 1 || filed.BlockedBy[0] != first.Number {
			t.Errorf("BlockedBy = %v, want only the first blocker", filed.BlockedBy)
		}
	})

	t.Run("parent after blockers landed", func(t *testing.T) {
		store := newFaultStore(t)
		blocker := seed(t, store.Store, "Blocker", "P2", "bug")
		parent := seed(t, store.Store, "Epic: parent", "P2", "epic")
		store.inject("AddSubIssue", errors.New("boom"), 0)
		tr := WrapAs(store, BackendLocal, "actor-a")

		_, err := tr.Create(ctx, CreateRequest{
			Title: "T", Type: "bug", BlockedBy: []int{blocker.Number}, Parent: parent.Number,
		})
		var partial *PartialCreateError
		if !errors.As(err, &partial) {
			t.Fatalf("err = %T (%v), want *tracker.PartialCreateError", err, err)
		}
		if partial.Stage != CreateStageParent || partial.Failed.To != parent.Number {
			t.Errorf("PartialCreateError = %+v, want the parent stage and target", partial)
		}
		if !slices.Equal(partial.Applied, []CreatedEdge{{Kind: BlockedByEdge, To: blocker.Number}}) {
			t.Errorf("Applied = %v, want the blocker that landed", partial.Applied)
		}
		filed, getErr := tr.Get(ctx, partial.Issue.Number)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if filed.Parent != nil {
			t.Errorf("Parent = %v, want none", filed.Parent)
		}
		if len(filed.BlockedBy) != 1 {
			t.Errorf("BlockedBy = %v, want the blocker that landed", filed.BlockedBy)
		}
	})
}

func TestCreateReportsACreateFailureWithoutIssuingAPartial(t *testing.T) {
	cause := errors.New("boom")
	store := newFaultStore(t).inject("CreateIssue", cause, 0)
	_, err := WrapAs(store, BackendLocal, "actor-a").Create(context.Background(), CreateRequest{Title: "T", Type: "bug"})
	if !errors.Is(err, cause) {
		t.Fatalf("err = %v, want the backend cause", err)
	}
	var partial *PartialCreateError
	if errors.As(err, &partial) {
		t.Errorf("err = %v, reported a partial create when nothing was created", err)
	}
}

func TestCreateMapsBackendFailures(t *testing.T) {
	cause := errors.New("token expired")
	authErr := &gh.AuthError{Err: cause}
	ctx := context.Background()

	for name, tc := range map[string]struct {
		store *faultStore
		run   func(*Tracker) error
	}{
		"create write": {
			store: newFaultStore(t).inject("CreateIssue", authErr, 0),
			run: func(tr *Tracker) error {
				_, err := tr.Create(ctx, CreateRequest{Title: "T", Type: "bug"})
				return err
			},
		},
		"reference read": {
			store: newFaultStore(t).inject("GetIssue", authErr, 0),
			run: func(tr *Tracker) error {
				_, err := tr.Create(ctx, CreateRequest{Title: "T", Type: "bug", Parent: 1})
				return err
			},
		},
		"assignee write": {
			store: newFaultStore(t).inject("AddAssignee", authErr, 0),
			run: func(tr *Tracker) error {
				_, err := tr.Create(ctx, CreateRequest{Title: "T", Type: "bug", Assignees: []string{"alice"}})
				return err
			},
		},
		"blocker wire-up": {
			store: newFaultStore(t).inject("AddBlockedBy", authErr, 0),
			run: func(tr *Tracker) error {
				blocker, err := tr.Create(ctx, CreateRequest{Title: "Blocker", Type: "bug"})
				if err != nil {
					t.Fatal(err)
				}
				_, err = tr.Create(ctx, CreateRequest{Title: "T", Type: "bug", BlockedBy: []int{blocker.Issue.Number}})
				return err
			},
		},
		"epic create write": {
			store: newFaultStore(t).inject("CreateIssue", authErr, 0),
			run: func(tr *Tracker) error {
				_, err := tr.CreateEpic(ctx, CreateEpicRequest{Title: "Big"})
				return err
			},
		},
		"epic child attach": {
			store: newFaultStore(t).inject("AddSubIssue", authErr, 0),
			run: func(tr *Tracker) error {
				child, err := tr.Create(ctx, CreateRequest{Title: "Child", Type: "task"})
				if err != nil {
					t.Fatal(err)
				}
				_, err = tr.CreateEpic(ctx, CreateEpicRequest{Title: "Big", Children: []int{child.Issue.Number}})
				return err
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := tc.run(WrapAs(tc.store, BackendLocal, "actor-a"))
			var auth *AuthError
			if !errors.As(err, &auth) {
				t.Fatalf("err = %T (%v), want *tracker.AuthError", err, err)
			}
			if !errors.Is(err, cause) {
				t.Errorf("err = %v, want the backend cause retained", err)
			}
		})
	}
}

// TestCreateErrorMessage covers the prose a consumer or the command prints for
// a partial create: the issue that exists, the step that failed and the cause
// all have to be readable from the error itself.
func TestCreateErrorMessage(t *testing.T) {
	cause := errors.New("boom")
	for _, tc := range []struct {
		name string
		err  *PartialCreateError
		want []string
	}{
		{
			name: "blocked-by after an edge landed",
			err: &PartialCreateError{
				Issue: Issue{Number: 12}, Stage: CreateStageBlockedBy,
				Applied: []CreatedEdge{{Kind: BlockedByEdge, To: 2}},
				Failed:  CreatedEdge{Kind: BlockedByEdge, To: 3},
				Err:     cause,
			},
			want: []string{"created #12", "attaching blocked-by #3 failed", "after blocked-by #2", "boom"},
		},
		{
			name: "parent",
			err:  &PartialCreateError{Issue: Issue{Number: 12}, Stage: CreateStageParent, Failed: CreatedEdge{Kind: ParentEdge, To: 7}, Err: cause},
			want: []string{"attaching parent #7 failed", "boom"},
		},
		{
			name: "children",
			err:  &PartialCreateError{Issue: Issue{Number: 12}, Stage: CreateStageChildren, Failed: CreatedEdge{Kind: ParentEdge, To: 7}, Err: cause},
			want: []string{"attaching child #7 failed", "boom"},
		},
		{
			name: "assignees",
			err:  &PartialCreateError{Issue: Issue{Number: 12}, Stage: CreateStageAssignees, Err: cause},
			want: []string{"assigning its assignees failed", "boom"},
		},
		{
			name: "unknown stage",
			err:  &PartialCreateError{Issue: Issue{Number: 12}, Stage: "mystery"},
			want: []string{"finishing it failed"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := tc.err.Error()
			for _, want := range tc.want {
				if !strings.Contains(msg, want) {
					t.Errorf("Error() = %q, want %q", msg, want)
				}
			}
		})
	}
	if !errors.Is(&PartialCreateError{Err: cause}, cause) {
		t.Error("PartialCreateError does not unwrap to its cause")
	}
}

func TestParentedErrorMessage(t *testing.T) {
	msg := (&ParentedError{Issue: 3, Parent: 2}).Error()
	if msg != "issue #3 already has a parent (#2)" {
		t.Errorf("Error() = %q", msg)
	}
}

// TestCreatedIssueDefaultsTheStateAndLabels: a backend that reports neither a
// state nor a normalized label set on the create response still yields a
// result that carries the request's labels and reads as open, while a backend
// that returns its own spelling keeps it.
func TestCreatedIssueDefaultsTheStateAndLabels(t *testing.T) {
	issue := createdIssue(model.Issue{Number: 1}, "T", "body", []string{"P2", "bug"})
	if issue.State != "OPEN" || issue.Title != "T" || issue.Body != "body" || !slices.Equal(issue.Labels, []string{"P2", "bug"}) {
		t.Errorf("createdIssue = %+v", issue)
	}
	normalized := createdIssue(model.Issue{Number: 2, State: "OPEN", Labels: []string{"p2", "Bug"}}, "T", "body", []string{"P2", "bug"})
	if !slices.Equal(normalized.Labels, []string{"p2", "Bug"}) {
		t.Errorf("Labels = %v, want the backend's spelling", normalized.Labels)
	}
}

func TestCreateReportsBodyFindingsWithoutRefusing(t *testing.T) {
	ctx, tr, _ := createTracker(t)
	result, err := tr.Create(ctx, CreateRequest{
		Title: "T", Type: "bug",
		Body: Body{Sections: Sections{Where: "internal/cli/write.go"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 1 || result.Findings[0].Kind != FindingUnmarkedCode {
		t.Fatalf("Findings = %+v, want one unmarked-code finding", result.Findings)
	}
}

func TestCreateEpicFilesAChildlessEpic(t *testing.T) {
	ctx, tr, store := createTracker(t)
	result, err := tr.CreateEpic(ctx, CreateEpicRequest{Title: "Big feature"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Issue.Title != "Epic: Big feature" {
		t.Errorf("Title = %q", result.Issue.Title)
	}
	if result.Issue.Priority == nil || *result.Issue.Priority != model.DefaultPriority.String() {
		t.Errorf("Priority = %v", result.Issue.Priority)
	}
	if result.Issue.Type != nil {
		t.Errorf("Type = %v, want none on an epic", *result.Issue.Type)
	}
	if !result.Issue.HasLabel(model.EpicLabel) || !result.Issue.Epic {
		t.Errorf("Labels = %v, want the epic marker", result.Issue.Labels())
	}
	if len(result.Edges) != 0 || len(result.Issue.SubIssues) != 0 {
		t.Errorf("Edges = %v, SubIssues = %v, want a childless epic", result.Edges, result.Issue.SubIssues)
	}
	filed, err := tr.Get(ctx, result.Issue.Number)
	if err != nil {
		t.Fatal(err)
	}
	if !filed.Epic {
		t.Errorf("Get = %+v, want an epic", filed)
	}
	if len(allIssues(t, store)) != 1 {
		t.Errorf("tracker holds %d issues, want one", len(allIssues(t, store)))
	}
}

func TestCreateEpicAttachesChildren(t *testing.T) {
	ctx, tr, store := createTracker(t)
	first := seed(t, store, "Child A", "P2", "task")
	second := seed(t, store, "Child B", "P2", "task")

	result, err := tr.CreateEpic(ctx, CreateEpicRequest{
		Title:    "Big feature",
		Children: []int{first.Number, second.Number, first.Number},
		Body:     Body{Sections: Sections{Goal: "the narrative"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantEdges := []CreatedEdge{
		{Kind: ParentEdge, To: first.Number},
		{Kind: ParentEdge, To: second.Number},
	}
	if !slices.Equal(result.Edges, wantEdges) {
		t.Errorf("Edges = %v, want %v", result.Edges, wantEdges)
	}
	if got, want := result.Issue.SubIssues, []int{first.Number, second.Number}; !slices.Equal(got, want) {
		t.Errorf("SubIssues = %v, want %v", got, want)
	}
	if result.Issue.Body == nil || !strings.Contains(*result.Issue.Body, "the narrative") {
		t.Errorf("Body = %v", result.Issue.Body)
	}

	epic, err := tr.Get(ctx, result.Issue.Number)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(epic.SubIssues, []int{first.Number, second.Number}) {
		t.Errorf("Get SubIssues = %v", epic.SubIssues)
	}
	for _, child := range []model.Issue{first, second} {
		filed, err := tr.Get(ctx, child.Number)
		if err != nil {
			t.Fatal(err)
		}
		if filed.Parent == nil || *filed.Parent != result.Issue.Number {
			t.Errorf("#%d Parent = %v, want #%d", child.Number, filed.Parent, result.Issue.Number)
		}
	}
}

func TestCreateEpicKeepsAnExistingPrefix(t *testing.T) {
	ctx, tr, _ := createTracker(t)
	result, err := tr.CreateEpic(ctx, CreateEpicRequest{Title: "Epic: already prefixed"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Issue.Title != "Epic: already prefixed" {
		t.Errorf("Title = %q", result.Issue.Title)
	}
}

func TestCreateEpicRefusesInvalidInputBeforeWriting(t *testing.T) {
	ctx, tr, store := createTracker(t)
	for _, tc := range []struct {
		name  string
		req   CreateEpicRequest
		field string
	}{
		{name: "blank title", req: CreateEpicRequest{Title: "  "}, field: "title"},
		{name: "multi-line title", req: CreateEpicRequest{Title: "a\nb"}, field: "title"},
		{name: "over-long title", req: CreateEpicRequest{Title: strings.Repeat("x", maxTitleRunes)}, field: "title"},
		{name: "over-long prefixed title", req: CreateEpicRequest{Title: strings.Repeat("x", maxTitleRunes-3)}, field: "title"},
		{name: "non-positive child", req: CreateEpicRequest{Title: "Big", Children: []int{0}}, field: "children"},
		{
			name:  "conflicting body sources",
			req:   CreateEpicRequest{Title: "Big", Body: Body{Sections: Sections{Goal: "g"}, Text: "text"}},
			field: "body",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tr.CreateEpic(ctx, tc.req)
			var invalid *InvalidInputError
			if !errors.As(err, &invalid) {
				t.Fatalf("err = %T (%v), want *tracker.InvalidInputError", err, err)
			}
			if invalid.Field != tc.field {
				t.Errorf("Field = %q, want %q (%v)", invalid.Field, tc.field, err)
			}
			if got := allIssues(t, store); len(got) != 0 {
				t.Errorf("refused epic create wrote %d issue(s)", len(got))
			}
		})
	}
}

func TestCreateEpicRefusesMissingChildrenBeforeWriting(t *testing.T) {
	ctx, tr, store := createTracker(t)
	for name, req := range map[string]CreateEpicRequest{
		"child":           {Title: "Big", Children: []int{999}},
		"discovered-from": {Title: "Big", Body: Body{DiscoveredFrom: 999}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := tr.CreateEpic(ctx, req)
			var notFound *NotFoundError
			if !errors.As(err, &notFound) || notFound.Issue != 999 {
				t.Fatalf("err = %T (%v), want NotFoundError for #999", err, err)
			}
			if got := allIssues(t, store); len(got) != 0 {
				t.Errorf("refused epic create wrote %d issue(s)", len(got))
			}
		})
	}
}

func TestCreateEpicRefusesAChildThatAlreadyHasAParent(t *testing.T) {
	ctx, tr, store := createTracker(t)
	parent := seed(t, store, "Epic: existing", "P2", "epic")
	child := seed(t, store, "Child", "P2", "task")
	if err := store.AddSubIssue(ctx, parent.Number, child.Number, false); err != nil {
		t.Fatal(err)
	}
	before := len(allIssues(t, store))

	_, err := tr.CreateEpic(ctx, CreateEpicRequest{Title: "Big", Children: []int{child.Number}})
	var parented *ParentedError
	if !errors.As(err, &parented) {
		t.Fatalf("err = %T (%v), want *tracker.ParentedError", err, err)
	}
	if parented.Issue != child.Number || parented.Parent != parent.Number {
		t.Errorf("ParentedError = %+v, want the child and its existing parent", parented)
	}
	if got := len(allIssues(t, store)); got != before {
		t.Errorf("refused epic create filed %d issue(s)", got-before)
	}
}

func TestCreateEpicReportsAPartialFailure(t *testing.T) {
	store := newFaultStore(t)
	first := seed(t, store.Store, "Child A", "P2", "task")
	second := seed(t, store.Store, "Child B", "P2", "task")
	third := seed(t, store.Store, "Child C", "P2", "task")
	store.inject("AddSubIssue", errors.New("boom"), 2)
	tr := WrapAs(store, BackendLocal, "actor-a")

	_, err := tr.CreateEpic(context.Background(), CreateEpicRequest{
		Title: "Big", Children: []int{first.Number, second.Number, third.Number},
	})
	var partial *PartialCreateError
	if !errors.As(err, &partial) {
		t.Fatalf("err = %T (%v), want *tracker.PartialCreateError", err, err)
	}
	if partial.Stage != CreateStageChildren {
		t.Errorf("Stage = %q, want %q", partial.Stage, CreateStageChildren)
	}
	if !slices.Equal(partial.Applied, []CreatedEdge{{Kind: ParentEdge, To: first.Number}}) {
		t.Errorf("Applied = %v, want the first child", partial.Applied)
	}
	if partial.Failed != (CreatedEdge{Kind: ParentEdge, To: second.Number}) {
		t.Errorf("Failed = %+v, want the second child", partial.Failed)
	}
	if !slices.Equal(partial.NotAttempted, []CreatedEdge{{Kind: ParentEdge, To: third.Number}}) {
		t.Errorf("NotAttempted = %v, want the third child", partial.NotAttempted)
	}
	epic, getErr := tr.Get(context.Background(), partial.Issue.Number)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if !slices.Equal(epic.SubIssues, []int{first.Number}) {
		t.Errorf("SubIssues = %v, want only the first child", epic.SubIssues)
	}
}

// TestCheckCreateAgreesWithCreate pins that the pre-flight and the creation
// share one validation: every request Create refuses, CheckCreate refuses with
// the same error, and neither writes.
func TestCheckCreateAgreesWithCreate(t *testing.T) {
	ctx, tr, store := createTracker(t)
	open := seed(t, store, "Open", "P2", "task")
	closed := seed(t, store, "Done", "P2", "task")
	if err := store.CloseIssue(ctx, closed.Number, "COMPLETED"); err != nil {
		t.Fatal(err)
	}
	before := len(allIssues(t, store))

	for name, req := range map[string]CreateRequest{
		"invalid priority":        {Title: "T", Type: "bug", Priority: "P9"},
		"invalid type":            {Title: "T", Type: "story"},
		"blank title":             {Title: " ", Type: "bug"},
		"missing parent":          {Title: "T", Type: "bug", Parent: 999},
		"missing blocker":         {Title: "T", Type: "bug", BlockedBy: []int{open.Number, 999}},
		"closed blocker":          {Title: "T", Type: "bug", BlockedBy: []int{closed.Number}},
		"missing discovered-from": {Title: "T", Type: "bug", DiscoveredFrom: 999},
	} {
		t.Run(name, func(t *testing.T) {
			checkErr := tr.CheckCreate(ctx, req)
			_, createErr := tr.Create(ctx, req)
			if checkErr == nil || createErr == nil {
				t.Fatalf("CheckCreate = %v, Create = %v; both should refuse", checkErr, createErr)
			}
			if checkErr.Error() != createErr.Error() {
				t.Errorf("CheckCreate = %q, Create = %q", checkErr, createErr)
			}
			if got := len(allIssues(t, store)); got != before {
				t.Errorf("refused request wrote %d issue(s)", got-before)
			}
		})
	}
}

// TestCheckCreateWritesNothingForAValidRequest is the other half: a request that
// passes the pre-flight leaves the tracker untouched, since the adapter still
// has to collect the body before Create files it.
func TestCheckCreateWritesNothingForAValidRequest(t *testing.T) {
	ctx, tr, store := createTracker(t)
	open := seed(t, store, "Open", "P2", "task")
	before := len(allIssues(t, store))

	req := CreateRequest{Title: "T", Type: "bug", Parent: open.Number, BlockedBy: []int{open.Number}, DiscoveredFrom: open.Number}
	if err := tr.CheckCreate(ctx, req); err != nil {
		t.Fatalf("CheckCreate = %v", err)
	}
	if got := len(allIssues(t, store)); got != before {
		t.Errorf("CheckCreate wrote %d issue(s)", got-before)
	}
}

func TestCheckCreateEpicAgreesWithCreateEpic(t *testing.T) {
	ctx, tr, store := createTracker(t)
	parent := seed(t, store, "Epic: existing", "P2", "epic")
	child := seed(t, store, "Child", "P2", "task")
	if err := store.AddSubIssue(ctx, parent.Number, child.Number, false); err != nil {
		t.Fatal(err)
	}
	before := len(allIssues(t, store))

	for name, req := range map[string]CreateEpicRequest{
		"blank title":     {Title: " "},
		"missing child":   {Title: "Big", Children: []int{999}},
		"parented child":  {Title: "Big", Children: []int{child.Number}},
		"discovered-from": {Title: "Big", Body: Body{DiscoveredFrom: 999}},
	} {
		t.Run(name, func(t *testing.T) {
			checkErr := tr.CheckCreateEpic(ctx, req)
			_, createErr := tr.CreateEpic(ctx, req)
			if checkErr == nil || createErr == nil {
				t.Fatalf("CheckCreateEpic = %v, CreateEpic = %v; both should refuse", checkErr, createErr)
			}
			if checkErr.Error() != createErr.Error() {
				t.Errorf("CheckCreateEpic = %q, CreateEpic = %q", checkErr, createErr)
			}
			if got := len(allIssues(t, store)); got != before {
				t.Errorf("refused request wrote %d issue(s)", got-before)
			}
		})
	}
	if err := tr.CheckCreateEpic(ctx, CreateEpicRequest{Title: "Big"}); err != nil {
		t.Errorf("CheckCreateEpic on a valid request = %v", err)
	}
	if got := len(allIssues(t, store)); got != before {
		t.Errorf("CheckCreateEpic wrote %d issue(s)", got-before)
	}
}

// A new idea cannot be filed as an epic child, and no new issue can be
// attached under an idea: both ends are validated in preparation, so the
// pre-flight and the creation refuse together and nothing is written.
func TestCreateRefusesIdeaEpicRelationships(t *testing.T) {
	ctx, tr, store := createTracker(t)
	ideaParent := seed(t, store, "Consider a cache", "P2", model.IdeaType)
	before := len(allIssues(t, store))

	tests := map[string]struct {
		req   CreateRequest
		field string
		want  string
	}{
		"idea as child": {
			CreateRequest{Title: "Child idea", Type: model.IdeaType, Parent: ideaParent.Number},
			"parent", "idea cannot be an epic child",
		},
		"idea as parent": {
			CreateRequest{Title: "Work", Type: "task", Parent: ideaParent.Number},
			"parent", "cannot be an epic parent",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var invalid *InvalidInputError
			if _, err := tr.Create(ctx, tc.req); !errors.As(err, &invalid) {
				t.Fatalf("Create = %v, want an *InvalidInputError", err)
			}
			if invalid.Field != tc.field || !strings.Contains(invalid.Reason, tc.want) {
				t.Errorf("InvalidInputError = %+v, want field %q naming %q", invalid, tc.field, tc.want)
			}
			checkErr := tr.CheckCreate(ctx, tc.req)
			if checkErr == nil || checkErr.Error() != invalid.Error() {
				t.Errorf("CheckCreate = %v, want the same refusal as Create (%v)", checkErr, invalid)
			}
			if got := len(allIssues(t, store)); got != before {
				t.Errorf("refused request wrote %d issue(s)", got-before)
			}
		})
	}
}

// An epic creation refuses an idea child before filing the epic itself, so a
// rejected request cannot leave a childless epic behind.
func TestCreateEpicRefusesAnIdeaChild(t *testing.T) {
	ctx, tr, store := createTracker(t)
	idea := seed(t, store, "Consider a cache", "P2", model.IdeaType)
	before := len(allIssues(t, store))

	req := CreateEpicRequest{Title: "Voltgo support", Children: []int{idea.Number}}
	var invalid *InvalidInputError
	if _, err := tr.CreateEpic(ctx, req); !errors.As(err, &invalid) {
		t.Fatalf("CreateEpic = %v, want an *InvalidInputError", err)
	}
	if invalid.Field != "children" || !strings.Contains(invalid.Reason, "idea cannot be an epic child") {
		t.Errorf("InvalidInputError = %+v, want a children refusal naming the idea rule", invalid)
	}
	checkErr := tr.CheckCreateEpic(ctx, req)
	if checkErr == nil || checkErr.Error() != invalid.Error() {
		t.Errorf("CheckCreateEpic = %v, want the same refusal as CreateEpic (%v)", checkErr, invalid)
	}
	if got := len(allIssues(t, store)); got != before {
		t.Errorf("refused epic creation wrote %d issue(s)", got-before)
	}
}
