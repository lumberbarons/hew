package local

import (
	"reflect"
	"strings"
	"testing"
)

func TestReleaseIssueClaim(t *testing.T) {
	for _, tc := range []struct {
		name      string
		assignees []string
		label     string
		login     string
		force     bool
		want      string
	}{
		{"owner", []string{"me"}, "in-progress", "me", false, ""},
		{"assignee only", []string{"me"}, "", "me", false, ""},
		{"foreign owner", []string{"other"}, "in-progress", "me", false, "--force"},
		{"multiple owners", []string{"me", "other"}, "in-progress", "me", false, "--force"},
		{"label only", nil, "in-progress", "me", false, "--force"},
		{"forced", []string{"other"}, "In-Progress", "me", true, ""},
		{"forced label only", nil, "In-Progress", "", true, ""},
		{"unclaimed", nil, "", "me", false, "not claimed"},
		{"unclaimed forced", nil, "", "me", true, "not claimed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStore(t)
			labels := []string{"P2", "task", "area"}
			if tc.label != "" {
				labels = append(labels, tc.label)
			}
			created, err := s.CreateIssue(ctx, "Work", "Body", labels)
			must(t, err)
			for _, login := range tc.assignees {
				must(t, s.AddAssignee(ctx, created.Number, login))
			}
			before, err := s.GetIssue(ctx, created.Number)
			must(t, err)
			err = s.ReleaseIssueClaim(ctx, created.Number, tc.login, tc.force)
			got, readErr := s.GetIssue(ctx, created.Number)
			must(t, readErr)
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("error = %v, want %q", err, tc.want)
				}
				if !reflect.DeepEqual(before, got) {
					t.Fatalf("refusal mutated issue: %+v", got)
				}
			} else {
				must(t, err)
				if got.Claimed() || got.State != before.State || got.Body != before.Body || !reflect.DeepEqual(got.Labels, []string{"P2", "task", "area"}) {
					t.Fatalf("after release: %+v", got)
				}
			}
		})
	}
}
