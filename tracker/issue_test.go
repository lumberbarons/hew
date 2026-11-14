package tracker

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/lumberbarons/hew/internal/model"
	"github.com/lumberbarons/hew/internal/render"
)

func TestIssueJSONMatchesRenderer(t *testing.T) {
	// Both conversions receive the same domain fixtures, including absent
	// metadata, contradictory labels, capped connections and empty detail.
	fixtures := []model.Issue{
		{},
		{Number: 9, Title: "Untriaged", State: "OPEN"},
		{Number: 55, Title: "Closed", State: "CLOSED", Labels: []string{"P3", "task"}},
		{
			Number: 117, Title: "Claimed & blocked", State: "OPEN",
			CreatedAt: time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC),
			Labels:    []string{"p3", "P1", "Task", "BUG", "tests", "in-progress"},
			Assignees: []string{"alice", "bob"}, Parent: &model.Ref{Number: 137, State: "OPEN"},
			BlockedBy:      []model.Ref{{Number: 9, State: "OPEN"}, {Number: 55, State: "CLOSED"}},
			BlockedByTotal: 25, Body: "### Goal\n\nA body.",
			Comments:      []model.Comment{{Author: "alice", CreatedAt: time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC), Body: "Comment"}},
			CommentsTotal: 30,
		},
		{Number: 137, State: "OPEN", Labels: []string{"P2", "epic"}},
		{Number: 138, State: "OPEN", SubIssues: []model.Ref{{Number: 55, State: "CLOSED"}}, SubIssuesTotal: 60, SubIssuesCompleted: 12},
	}
	for _, detail := range []bool{false, true} {
		for _, fixture := range fixtures {
			got, err := json.Marshal(issueFromModel(fixture, detail))
			if err != nil {
				t.Fatal(err)
			}
			want, err := json.Marshal(render.ToJSON(fixture, detail))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Errorf("issue #%d, detail %v:\ngot  %s\nwant %s", fixture.Number, detail, got, want)
			}
		}
	}
}

func TestIssueSchemaMatchesRenderer(t *testing.T) {
	// Check optional fields such as Next even when ToJSON does not set them.
	check := func(got, want reflect.Type) {
		t.Helper()
		if got.NumField() != want.NumField() {
			t.Fatalf("%s has %d fields, want %d", got, got.NumField(), want.NumField())
		}
		for n := range got.NumField() {
			g, w := got.Field(n), want.Field(n)
			// Comments deliberately use package-specific names; their fields
			// are checked separately below. Every other type must match exactly.
			typesMatch := g.Type == w.Type ||
				(g.Type == reflect.TypeFor[[]Comment]() && w.Type == reflect.TypeFor[[]render.CommentJSON]())
			if g.Name != w.Name || g.Tag != w.Tag || !typesMatch {
				t.Errorf("field %d: %v, want %v", n, g, w)
			}
		}
	}
	check(reflect.TypeFor[Issue](), reflect.TypeFor[render.IssueJSON]())
	check(reflect.TypeFor[Comment](), reflect.TypeFor[render.CommentJSON]())
}
