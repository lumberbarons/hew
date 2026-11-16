package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/internal/model"
)

func outputLines(out string) []string {
	return strings.Split(strings.TrimSpace(out), "\n")
}

func threeIssues() *fakeClient {
	return newFake(
		issue(1, "Normal work", "P2", "bug"),
		issue(2, "Urgent", "P0", "task"),
		issue(3, "Middling", "P1", "bug"),
	)
}

func TestListLimitCapsAfterSortingAndWarns(t *testing.T) {
	app, out, errOut := newApp(threeIssues())
	if err := app.List(ctx, ListOpts{Limit: 2}); err != nil {
		t.Fatal(err)
	}
	lines := outputLines(out.String())
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "#2") || !strings.HasPrefix(lines[1], "#3") {
		t.Errorf("List output, want the top two of list order:\n%s", out.String())
	}
	if !strings.Contains(errOut.String(), "showing 2 of 3 issues; --limit 0 for all") {
		t.Errorf("stderr = %q", errOut.String())
	}
}

func TestListLimitZeroAndExactLimitPrintEverythingSilently(t *testing.T) {
	for _, limit := range []int{0, 3} {
		app, out, errOut := newApp(threeIssues())
		if err := app.List(ctx, ListOpts{Limit: limit}); err != nil {
			t.Fatal(err)
		}
		if got := len(outputLines(out.String())); got != 3 {
			t.Errorf("limit %d printed %d lines, want 3", limit, got)
		}
		if errOut.String() != "" {
			t.Errorf("limit %d: nothing was truncated; stderr = %q", limit, errOut.String())
		}
	}
}

func TestListLimitWarnsOnStderrUnderJSON(t *testing.T) {
	app, out, errOut := newApp(threeIssues())
	app.JSON = true
	if err := app.List(ctx, ListOpts{Limit: 1}); err != nil {
		t.Fatal(err)
	}
	lines := outputLines(out.String())
	var got map[string]any
	if len(lines) != 1 || json.Unmarshal([]byte(lines[0]), &got) != nil || got["number"].(float64) != 2 {
		t.Fatalf("want one NDJSON line for #2, got:\n%s", out.String())
	}
	if !strings.Contains(errOut.String(), "showing 1 of 3") {
		t.Errorf("stderr = %q", errOut.String())
	}
}

func TestListAndSearchRejectNegativeLimit(t *testing.T) {
	app, out, _ := newApp(threeIssues())
	usageError(t, app.List(ctx, ListOpts{Limit: -1}), "--limit must be 0 or greater")
	usageError(t, app.Search(ctx, SearchOpts{Terms: "work", Limit: -1}), "--limit must be 0 or greater")
	if out.String() != "" {
		t.Errorf("nothing should be printed; stdout = %q", out.String())
	}
}

func searchFake() *fakeClient {
	closed := issue(3, "Retry storm fixed", "P2", "bug")
	closed.State = "CLOSED"
	return newFake(
		issue(1, "retry helper", "P4", "task"),
		issue(2, "retry loop bug", "P0", "bug"),
		closed,
	)
}

func TestSearchLimitKeepsTheBestMatchesAndWarns(t *testing.T) {
	app, out, errOut := newApp(searchFake())
	if err := app.Search(ctx, SearchOpts{Terms: "retry", Limit: 2}); err != nil {
		t.Fatal(err)
	}
	lines := outputLines(out.String())
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "#1") || !strings.HasPrefix(lines[1], "#2") {
		t.Errorf("Search output, want the first two matches in relevance order:\n%s", out.String())
	}
	if !strings.Contains(errOut.String(), "showing 2 of 3 matches; --limit 0 for all") {
		t.Errorf("stderr = %q", errOut.String())
	}
}

func TestSearchLimitDoesNotPromiseEverythingWhenTheBackendCapped(t *testing.T) {
	f := searchFake()
	f.searchTotal = 50 // the backend found 50 and returned only these three
	app, _, errOut := newApp(f)
	if err := app.Search(ctx, SearchOpts{Terms: "retry", Limit: 2}); err != nil {
		t.Fatal(err)
	}
	got := errOut.String()
	if !strings.Contains(got, "showing 3 of 50 matches; refine the terms") || !strings.Contains(got, "showing 2 of 3 matches") {
		t.Errorf("stderr = %q, want both the backend-cap and the limit warnings", got)
	}
	if strings.Contains(got, "--limit 0") {
		t.Errorf("stderr = %q: --limit 0 would not show all 50, so it must not be suggested", got)
	}
}

func TestSearchLimitZeroAndExactLimitDoNotWarn(t *testing.T) {
	for _, limit := range []int{0, 3} {
		app, out, errOut := newApp(searchFake())
		if err := app.Search(ctx, SearchOpts{Terms: "retry", Limit: limit}); err != nil {
			t.Fatal(err)
		}
		if got := len(outputLines(out.String())); got != 3 {
			t.Errorf("limit %d printed %d lines, want 3", limit, got)
		}
		if errOut.String() != "" {
			t.Errorf("limit %d: stderr = %q", limit, errOut.String())
		}
	}
}

func TestSearchStateNarrowsTheBackendQuery(t *testing.T) {
	for _, tc := range []struct {
		state string
		want  []string
		omit  []string
	}{
		{"", []string{"#1", "#2", "#3"}, nil},
		{stateAll, []string{"#1", "#2", "#3"}, nil},
		{stateOpen, []string{"#1", "#2"}, []string{"#3"}},
		{stateClosed, []string{"#3"}, []string{"#1", "#2"}},
	} {
		app, out, _ := newApp(searchFake())
		if err := app.Search(ctx, SearchOpts{Terms: "retry", State: tc.state}); err != nil {
			t.Fatalf("state %q: %v", tc.state, err)
		}
		for _, w := range tc.want {
			if !strings.Contains(out.String(), w) {
				t.Errorf("state %q: missing %s:\n%s", tc.state, w, out.String())
			}
		}
		for _, o := range tc.omit {
			if strings.Contains(out.String(), o) {
				t.Errorf("state %q: %s leaked in:\n%s", tc.state, o, out.String())
			}
		}
	}
}

func TestSearchRejectsAnUnknownState(t *testing.T) {
	f := searchFake()
	app, _, _ := newApp(f)
	usageError(t, app.Search(ctx, SearchOpts{Terms: "retry", State: "OPEN"}), "--state must be open, closed, or all")
	for _, call := range f.calls {
		if strings.HasPrefix(call, "SearchIssues") {
			t.Errorf("backend called for a bad state: %v", f.calls)
		}
	}
}

func epicFake() *fakeClient {
	open := issue(10, "Epic: open one", "P2")
	open.SubIssues = []model.Ref{{Number: 11, State: "OPEN"}}
	child := issue(11, "Child", "P2", "task")
	child.Parent = &model.Ref{Number: 10, State: "OPEN"}
	done := issue(20, "Epic: finished", "P2")
	done.State = "CLOSED"
	done.SubIssues = []model.Ref{{Number: 21, State: "CLOSED"}}
	doneChild := issue(21, "Done child", "P2", "task")
	doneChild.State = "CLOSED"
	doneChild.Parent = &model.Ref{Number: 20, State: "CLOSED"}
	return newFake(open, child, done, doneChild)
}

func TestEpicStatusStateSelectsWhichEpicsRollUp(t *testing.T) {
	for _, tc := range []struct {
		state      string
		wantOpen   bool
		wantClosed bool
	}{
		{"", true, false},
		{stateOpen, true, false},
		{stateClosed, false, true},
		{stateAll, true, true},
	} {
		app, out, _ := newApp(epicFake())
		if err := app.EpicStatus(ctx, EpicStatusOpts{State: tc.state}); err != nil {
			t.Fatalf("state %q: %v", tc.state, err)
		}
		got := out.String()
		if has := strings.Contains(got, "#10"); has != tc.wantOpen {
			t.Errorf("state %q: open epic listed = %v, want %v:\n%s", tc.state, has, tc.wantOpen, got)
		}
		if has := strings.Contains(got, "#20"); has != tc.wantClosed {
			t.Errorf("state %q: closed epic listed = %v, want %v:\n%s", tc.state, has, tc.wantClosed, got)
		}
	}
}

func TestEpicStatusStateRejectedForOneEpicAndForUnknownValues(t *testing.T) {
	app, out, _ := newApp(epicFake())
	usageError(t, app.EpicStatus(ctx, EpicStatusOpts{Number: 10, State: stateAll}), "does not apply to one epic's children")
	usageError(t, app.EpicStatus(ctx, EpicStatusOpts{State: "done"}), "--state must be open, closed, or all")
	if out.String() != "" {
		t.Errorf("nothing should be printed; stdout = %q", out.String())
	}
}
