package cli

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/model"
	"github.com/lumberbarons/hew/tracker"
)

// getOnlyStore returns one fixture from GetIssue; the round trip only needs
// the read the tracker package performs.
type getOnlyStore struct {
	gh.IssueStore
	issue model.Issue
}

func (s getOnlyStore) GetIssue(context.Context, int) (model.Issue, error) {
	return s.issue, nil
}

// issueFromTracker must be total: every field a renderer can read survives the
// trip through tracker. Populating every model field except ID means a field
// added to model.Issue fails here until the fixture and the carrier both
// learn about it, rather than silently rendering its zero value.
func TestTrackerIssueRoundTripsEveryModelField(t *testing.T) {
	fixture := model.Issue{
		Number:             7,
		Title:              "Title",
		Body:               "### Goal\n\nBody",
		State:              "CLOSED",
		StateReason:        "completed",
		CreatedAt:          time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC),
		Labels:             []string{"P1", "bug", "tests"},
		Assignees:          []string{"alice"},
		Parent:             &model.Ref{Number: 3, State: "OPEN"},
		ParentTitle:        "Parent",
		SubIssues:          []model.Ref{{Number: 8, State: "CLOSED"}, {Number: 9, State: "OPEN"}},
		SubIssuesTotal:     2,
		SubIssuesCompleted: 1,
		BlockedBy:          []model.Ref{{Number: 4, State: "CLOSED"}, {Number: 5, State: "OPEN"}},
		BlockedByTotal:     3,
		Comments: []model.Comment{{
			Author:    "bob",
			CreatedAt: time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC),
			Body:      "comment",
		}},
		CommentsTotal: 2,
	}

	fields := reflect.ValueOf(fixture)
	for n := range fields.NumField() {
		name := fields.Type().Field(n).Name
		// ID is the one field reads deliberately do not carry: the flat
		// schema has no node id, and no render path reads it.
		if name != "ID" && fields.Field(n).IsZero() {
			t.Fatalf("fixture does not populate model.Issue.%s; extend the round-trip fixture and the tracker carrier", name)
		}
	}

	got, err := tracker.Wrap(getOnlyStore{issue: fixture}).Get(context.Background(), fixture.Number)
	if err != nil {
		t.Fatal(err)
	}
	if round := issueFromTracker(got); !reflect.DeepEqual(round, fixture) {
		t.Errorf("round trip lost data:\ngot  %+v\nwant %+v", round, fixture)
	}
}
