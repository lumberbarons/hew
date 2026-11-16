package consumer_test

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/tracker"
)

// This file is an external consumer's test of creation: it imports only the
// public package, opens a real local tracker the way a program outside this
// module does, and files issues and epics with the typed API alone. A test
// inside package tracker could pass while an internal type still leaked into
// the signatures; this one could not compile if it did.

// TestCreateIsUsableFromAnExternalPackage is the consumer's creation workflow:
// a normal issue and a childless epic, children and dependencies attached, and
// every result read back through the public API.
func TestCreateIsUsableFromAnExternalPackage(t *testing.T) {
	tr := externalTracker(t)
	ctx := context.Background()

	blocker, err := tr.Create(ctx, tracker.CreateRequest{Title: "Blocker", Type: "bug"})
	if err != nil {
		t.Fatalf("Create blocker: %v", err)
	}
	childOfEpic, err := tr.Create(ctx, tracker.CreateRequest{Title: "Child", Type: "task"})
	if err != nil {
		t.Fatalf("Create child: %v", err)
	}

	epic, err := tr.CreateEpic(ctx, tracker.CreateEpicRequest{Title: "Big feature"})
	if err != nil {
		t.Fatalf("CreateEpic: %v", err)
	}
	if epic.Issue.Title != "Epic: Big feature" || !epic.Issue.HasLabel("epic") {
		t.Errorf("epic = %+v, want the prefix and the epic marker", epic.Issue)
	}
	if epic.Issue.Type != nil {
		t.Errorf("epic Type = %v, want none", *epic.Issue.Type)
	}

	issue, err := tr.Create(ctx, tracker.CreateRequest{
		Title:    "Consumer work",
		Type:     "enhancement",
		Priority: "P1",
		Areas:    []string{"api"},
		Body: tracker.Body{Sections: tracker.Sections{
			Goal:     "ship it",
			DoneWhen: []string{"it works"},
		}},
		Parent:         epic.Issue.Number,
		BlockedBy:      []int{blocker.Issue.Number},
		DiscoveredFrom: childOfEpic.Issue.Number,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if issue.Issue.Priority == nil || *issue.Issue.Priority != "P1" {
		t.Errorf("Priority = %v", issue.Issue.Priority)
	}
	if issue.Issue.Type == nil || *issue.Issue.Type != "enhancement" {
		t.Errorf("Type = %v", issue.Issue.Type)
	}
	if got, want := issue.Issue.Areas, []string{"api"}; !slices.Equal(got, want) {
		t.Errorf("Areas = %v, want %v", got, want)
	}
	wantEdges := []tracker.CreatedEdge{
		{Kind: tracker.BlockedByEdge, To: blocker.Issue.Number},
		{Kind: tracker.ParentEdge, To: epic.Issue.Number},
	}
	if !slices.Equal(issue.Edges, wantEdges) {
		t.Errorf("Edges = %v, want %v", issue.Edges, wantEdges)
	}

	// Attaching children is the epic's typed result: the second call files the
	// epic, then reads back show the tree from both ends.
	withChildren, err := tr.CreateEpic(ctx, tracker.CreateEpicRequest{
		Title:    "With children",
		Children: []int{childOfEpic.Issue.Number},
	})
	if err != nil {
		t.Fatalf("CreateEpic with children: %v", err)
	}
	epicRead, err := tr.Get(ctx, withChildren.Issue.Number)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(epicRead.SubIssues, []int{childOfEpic.Issue.Number}) {
		t.Errorf("epic SubIssues = %v", epicRead.SubIssues)
	}
	childRead, err := tr.Get(ctx, childOfEpic.Issue.Number)
	if err != nil {
		t.Fatal(err)
	}
	if childRead.Parent == nil || *childRead.Parent != withChildren.Issue.Number {
		t.Errorf("child Parent = %v, want #%d", childRead.Parent, withChildren.Issue.Number)
	}

	// The created issue reads back with every write the call made.
	filed, err := tr.Get(ctx, issue.Issue.Number)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(filed.BlockedBy, []int{blocker.Issue.Number}) {
		t.Errorf("BlockedBy = %v", filed.BlockedBy)
	}
	if filed.Parent == nil || *filed.Parent != epic.Issue.Number {
		t.Errorf("Parent = %v", filed.Parent)
	}
	if filed.Body == nil || !strings.Contains(*filed.Body, "### Goal") || !strings.HasSuffix(*filed.Body, "Discovered while working on #"+strconv.Itoa(childOfEpic.Issue.Number)) {
		t.Errorf("Body = %v, want the template and the discovered-from link", filed.Body)
	}
}

// TestCreateRefusalsArePublicTypes is the refusal half: a consumer branches on
// typed errors, so every invalid or unresolvable request must come back as one
// of the public types and leave nothing behind.
func TestCreateRefusalsArePublicTypes(t *testing.T) {
	tr := externalTracker(t)
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		run  func() error
		as   func(error) bool
	}{
		{
			name: "invalid title",
			run: func() error {
				_, err := tr.Create(ctx, tracker.CreateRequest{Title: " ", Type: "bug"})
				return err
			},
			as: func(err error) bool {
				var invalid *tracker.InvalidInputError
				return errors.As(err, &invalid) && invalid.Field == "title"
			},
		},
		{
			name: "invalid area",
			run: func() error {
				_, err := tr.Create(ctx, tracker.CreateRequest{Title: "T", Type: "bug", Areas: []string{"P0"}})
				return err
			},
			as: func(err error) bool {
				var invalid *tracker.InvalidInputError
				return errors.As(err, &invalid) && invalid.Field == "areas"
			},
		},
		{
			name: "invalid type",
			run: func() error {
				_, err := tr.Create(ctx, tracker.CreateRequest{Title: "T", Type: "epic"})
				return err
			},
			as: func(err error) bool {
				var invalid *tracker.InvalidInputError
				return errors.As(err, &invalid) && invalid.Field == "type"
			},
		},
		{
			name: "invalid priority",
			run: func() error {
				_, err := tr.Create(ctx, tracker.CreateRequest{Title: "T", Type: "bug", Priority: "high"})
				return err
			},
			as: func(err error) bool {
				var invalid *tracker.InvalidInputError
				return errors.As(err, &invalid) && invalid.Field == "priority"
			},
		},
		{
			name: "missing parent",
			run: func() error {
				_, err := tr.Create(ctx, tracker.CreateRequest{Title: "T", Type: "bug", Parent: 999})
				return err
			},
			as: func(err error) bool {
				var notFound *tracker.NotFoundError
				return errors.As(err, &notFound) && notFound.Issue == 999
			},
		},
		{
			name: "missing discovered-from",
			run: func() error {
				_, err := tr.Create(ctx, tracker.CreateRequest{Title: "T", Type: "bug", DiscoveredFrom: 998})
				return err
			},
			as: func(err error) bool {
				var notFound *tracker.NotFoundError
				return errors.As(err, &notFound) && notFound.Issue == 998
			},
		},
		{
			name: "missing child",
			run: func() error {
				_, err := tr.CreateEpic(ctx, tracker.CreateEpicRequest{Title: "Big", Children: []int{997}})
				return err
			},
			as: func(err error) bool {
				var notFound *tracker.NotFoundError
				return errors.As(err, &notFound) && notFound.Issue == 997
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil {
				t.Fatal("refused creation returned no error")
			}
			if !tc.as(err) {
				t.Fatalf("err = %T (%v), want the documented public refusal", err, err)
			}
		})
	}

	issues, err := tr.Snapshot(ctx, tracker.SnapshotOptions{States: []tracker.State{tracker.StateOpen, tracker.StateClosed}})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(issues.Issues()); got != 0 {
		t.Errorf("refused creations left %d issue(s) behind", got)
	}
}
