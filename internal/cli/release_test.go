package cli

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/internal/model"
)

func TestReleaseClaims(t *testing.T) {
	for _, tc := range []struct {
		name      string
		assignees []string
		label     string
		force     bool
	}{
		{"owner", []string{"me"}, "in-progress", false},
		{"assignee only", []string{"me"}, "", false},
		{"another owner forced", []string{"other"}, "In-Progress", true},
		{"label only forced", nil, "in-progress", true},
		{"multiple owners forced", []string{"me", "other"}, "in-progress", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := issue(1, "Work", "P2", "task", "area")
			if tc.label != "" {
				i.Labels = append(i.Labels, tc.label)
			}
			i.Assignees = tc.assignees
			i.Body = "Keep the body"
			f := newFake(i)
			app, out, _ := newApp(f)
			if err := app.Release(ctx, 1, tc.force); err != nil {
				t.Fatal(err)
			}
			got := f.byNumber(1)
			if got.Claimed() || got.State != "OPEN" || got.Body != "Keep the body" || !reflect.DeepEqual(got.Labels, []string{"P2", "task", "area"}) {
				t.Fatalf("after release: %+v", got)
			}
			if out.String() != "released #1\n" {
				t.Errorf("output = %q", out.String())
			}
			out.Reset()
			if err := app.Ready(ctx, ReadyOpts{}); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "#1") {
				t.Fatalf("not ready after release: %s", out)
			}
		})
	}
}

func TestReleaseRefusesWithoutMutation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		assignees []string
		label     string
		force     bool
		want      string
	}{
		{"unclaimed", nil, "", false, "not claimed"},
		{"unclaimed forced", nil, "", true, "not claimed"},
		{"another owner", []string{"other"}, "in-progress", false, "--force"},
		{"multiple owners", []string{"me", "other"}, "in-progress", false, "--force"},
		{"label only", nil, "in-progress", false, "--force"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := issue(1, "Work", "P2", "task")
			i.Assignees = tc.assignees
			if tc.label != "" {
				i.Labels = append(i.Labels, tc.label)
			}
			f := newFake(i)
			app, _, _ := newApp(f)
			err := app.Release(ctx, 1, tc.force)
			exitCode(t, err, ExitGeneric)
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want %s", err, tc.want)
			}
			assertOnlyReads(t, f, "Viewer")
		})
	}
}

func TestReleaseJSON(t *testing.T) {
	i := issue(1, "Work", "P2", "task", "in-progress")
	i.Assignees = []string{"me"}
	f := newFake(i)
	app, out, _ := newApp(f)
	app.JSON = true
	if err := app.Release(ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Number     int
		InProgress bool
		Assignees  []string
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Number != 1 || got.InProgress || len(got.Assignees) != 0 {
		t.Fatalf("JSON = %s", out)
	}
}

func TestReleaseFailures(t *testing.T) {
	for _, method := range []string{"GetIssue", "Viewer", "RemoveLabel", "RemoveAssignees"} {
		t.Run(method, func(t *testing.T) {
			i := issue(1, "Work", "P2", "task", model.InProgressLabel)
			i.Assignees = []string{"me"}
			f := newFake(i)
			f.failOn[method] = errors.New("injected failure")
			app, out, _ := newApp(f)
			err := app.Release(ctx, 1, false)
			if err == nil || !strings.Contains(err.Error(), "injected failure") || out.Len() != 0 {
				t.Fatalf("error = %v, output = %q", err, out)
			}
			if method == "GetIssue" || method == "Viewer" {
				assertOnlyReads(t, f, "Viewer")
			}
			if method == "RemoveLabel" && !i.InProgress() {
				t.Fatal("label changed after failure")
			}
		})
	}
}
