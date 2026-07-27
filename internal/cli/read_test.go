package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/internal/model"
)

var ctx = context.Background()

func TestReadyListsSorted(t *testing.T) {
	f := newFake(
		issue(1, "Normal work", "P2", "bug"),
		issue(2, "Urgent", "P0", "task"),
		issue(3, "Claimed", "P1", "bug", "in-progress"),
	)
	app, out, _ := newApp(f)
	if err := app.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "#2") || !strings.HasPrefix(lines[1], "#1") {
		t.Errorf("Ready output:\n%s", out.String())
	}
}

func TestReadyNoWork(t *testing.T) {
	f := newFake(issue(3, "Claimed", "P1", "bug", "in-progress"))
	app, out, _ := newApp(f)
	if err := app.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	if out.String() != "no ready work\n" {
		t.Errorf("output = %q", out.String())
	}
}

func TestReadyWarnsOnCycle(t *testing.T) {
	a := issue(1, "A", "P2", "bug")
	a.BlockedBy = []model.Ref{{Number: 2, State: "OPEN"}}
	b := issue(2, "B", "P2", "bug")
	b.BlockedBy = []model.Ref{{Number: 1, State: "OPEN"}}
	f := newFake(a, b)
	app, out, errOut := newApp(f)
	if err := app.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "dependency cycle #1 → #2 → #1") {
		t.Errorf("stderr = %q", errOut.String())
	}
	if out.String() != "no ready work\n" {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestReadyWarnsOnTruncatedBlockers(t *testing.T) {
	// The fetched blocker is closed (so #1 looks ready), but the server says
	// there are more blockers than were fetched — ready may be wrong, and the
	// command must say so.
	a := issue(1, "A", "P2", "bug")
	a.BlockedBy = []model.Ref{{Number: 2, State: "CLOSED"}}
	a.BlockedByTotal = 25
	f := newFake(a)
	app, _, errOut := newApp(f)
	if err := app.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "#1 has 25 blockers, only 1 fetched") {
		t.Errorf("stderr = %q", errOut.String())
	}
}

func TestReadyJSON(t *testing.T) {
	f := newFake(issue(1, "Work", "P2", "bug"))
	app, out, _ := newApp(f)
	app.JSON = true
	if err := app.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 NDJSON line, got %d:\n%s", len(lines), out.String())
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("invalid NDJSON line: %v\n%s", err, lines[0])
	}
	if got["number"].(float64) != 1 || got["priority"] != "P2" {
		t.Errorf("JSON = %s", out.String())
	}
}

func TestJSONFramingIsConsistent(t *testing.T) {
	// The contract: a collection is NDJSON (one object per line); a single
	// issue is one object. Lock both so neither drifts to the other's shape.
	f := newFake(issue(1, "One", "P2", "bug"), issue(2, "Two", "P1", "bug"))

	listApp, listOut, _ := newApp(f)
	listApp.JSON = true
	if err := listApp.List(ctx, ListOpts{}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(listOut.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("list --json should be 2 NDJSON lines, got %d:\n%s", len(lines), listOut.String())
	}
	for _, ln := range lines {
		var obj map[string]any
		if err := json.Unmarshal([]byte(ln), &obj); err != nil {
			t.Fatalf("list line is not a JSON object: %v\n%s", err, ln)
		}
	}

	showApp, showOut, _ := newApp(f)
	showApp.JSON = true
	if err := showApp.Show(ctx, 1); err != nil {
		t.Fatal(err)
	}
	// A single object parses whole and is not line-delimited NDJSON.
	var one map[string]any
	if err := json.Unmarshal(showOut.Bytes(), &one); err != nil {
		t.Fatalf("show --json should be one JSON object: %v\n%s", err, showOut.String())
	}
	if one["number"].(float64) != 1 {
		t.Errorf("show --json = %s", showOut.String())
	}
}

func TestListFilters(t *testing.T) {
	epicIssue := issue(10, "Epic: big", "P2")
	child := issue(11, "Child", "P2", "task")
	child.Parent = &model.Ref{Number: 10, State: "OPEN"}
	epicIssue.SubIssues = []model.Ref{{Number: 11, State: "OPEN"}}
	f := newFake(
		issue(1, "Bug A", "P2", "bug", "tests"),
		issue(2, "Feature B", "P1", "enhancement"),
		epicIssue, child,
	)

	app, out, _ := newApp(f)
	if err := app.List(ctx, ListOpts{Label: "tests"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "#1") || strings.Contains(out.String(), "#2") {
		t.Errorf("label filter output:\n%s", out.String())
	}

	out.Reset()
	if err := app.List(ctx, ListOpts{Epic: 10}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "#11") || strings.Contains(out.String(), "#1 ") {
		t.Errorf("epic filter output:\n%s", out.String())
	}
}

func TestListGroupsReadyFirst(t *testing.T) {
	epicIssue := issue(10, "Epic: big", "P0")
	epicIssue.SubIssues = []model.Ref{{Number: 11, State: "OPEN"}}
	blockedChild := issue(11, "Child", "P0", "task")
	blockedChild.Parent = &model.Ref{Number: 10, State: "OPEN"}
	blockedChild.BlockedBy = []model.Ref{{Number: 12, State: "OPEN"}}
	claimed := issue(12, "Claimed", "P0", "bug", "in-progress")
	f := newFake(epicIssue, blockedChild, claimed, issue(13, "Plain ready", "P4", "bug"))
	app, out, _ := newApp(f)
	if err := app.List(ctx, ListOpts{}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	wantOrder := []string{"#13", "#12", "#11", "#10"} // ready, claimed, blocked, epic
	for i, prefix := range wantOrder {
		if !strings.HasPrefix(lines[i], prefix) {
			t.Fatalf("line %d = %q, want prefix %q\n%s", i, lines[i], prefix, out.String())
		}
	}
	for _, want := range []string{"[blocked by #12]", "[in progress]", "[epic 0/1]"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing annotation %q:\n%s", want, out.String())
		}
	}
}

func TestListClosed(t *testing.T) {
	closed := issue(5, "Done", "P2", "bug")
	closed.State = "CLOSED"
	f := newFake(issue(1, "Open", "P2", "bug"), closed)
	app, out, _ := newApp(f)
	if err := app.List(ctx, ListOpts{Closed: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "#5") || strings.Contains(out.String(), "#1") {
		t.Errorf("closed output:\n%s", out.String())
	}
}

func TestListBodiesJSON(t *testing.T) {
	withBody := issue(1, "Has body", "P2", "bug")
	withBody.Body = "### Goal\n\nDedup."
	f := newFake(withBody, issue(2, "No body", "P1", "bug"))
	app, out, _ := newApp(f)
	app.JSON = true
	if err := app.List(ctx, ListOpts{Bodies: true}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 NDJSON lines, got %d:\n%s", len(lines), out.String())
	}
	bodies := map[float64]any{}
	for _, ln := range lines {
		var got map[string]any
		if err := json.Unmarshal([]byte(ln), &got); err != nil {
			t.Fatalf("invalid NDJSON line: %v\n%s", err, ln)
		}
		body, ok := got["body"]
		if !ok {
			t.Fatalf("line missing body: %s", ln)
		}
		bodies[got["number"].(float64)] = body
	}
	if bodies[1] != "### Goal\n\nDedup." || bodies[2] != "" {
		t.Errorf("bodies = %v", bodies)
	}
}

func TestListWithoutBodiesFlagOmitsBodies(t *testing.T) {
	withBody := issue(1, "Has body", "P2", "bug")
	withBody.Body = "secret payload"
	f := newFake(withBody)
	app, out, _ := newApp(f)
	app.JSON = true
	if err := app.List(ctx, ListOpts{}); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["body"]; ok {
		t.Errorf("body leaked without --bodies: %s", out.String())
	}
}

func TestListBodiesRequiresJSON(t *testing.T) {
	f := newFake(issue(1, "Work", "P2", "bug"))
	app, _, _ := newApp(f)
	exitCode(t, app.List(ctx, ListOpts{Bodies: true}), ExitUsage)
}

func TestListEmpty(t *testing.T) {
	f := newFake()
	app, out, _ := newApp(f)
	if err := app.List(ctx, ListOpts{}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "no issues\n" {
		t.Errorf("output = %q", out.String())
	}
}

func TestShow(t *testing.T) {
	i := issue(7, "Detailed", "P1", "bug")
	i.Body = "the body"
	f := newFake(i)
	app, out, _ := newApp(f)
	if err := app.Show(ctx, 7); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"#7 P1 bug  Detailed", "the body"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("Show missing %q:\n%s", want, out.String())
		}
	}
}

func TestShowNotFound(t *testing.T) {
	app, _, _ := newApp(newFake())
	err := app.Show(ctx, 99)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v", err)
	}
}

func TestShowJSON(t *testing.T) {
	i := issue(7, "Detailed", "P1", "bug")
	i.Body = "the body"
	f := newFake(i)
	app, out, _ := newApp(f)
	app.JSON = true
	if err := app.Show(ctx, 7); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["body"] != "the body" {
		t.Errorf("JSON = %s", out.String())
	}
}

func TestTriage(t *testing.T) {
	f := newFake(
		issue(3, "No labels at all"),
		issue(1, "Only priority", "P2"),
		issue(2, "Fully triaged", "P2", "bug"),
	)
	app, out, _ := newApp(f)
	if err := app.Triage(ctx); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "#1") || !strings.HasPrefix(lines[1], "#3") {
		t.Errorf("Triage output:\n%s", out.String())
	}
}

func TestTriageClean(t *testing.T) {
	f := newFake(issue(2, "Fully triaged", "P2", "bug"))
	app, out, _ := newApp(f)
	if err := app.Triage(ctx); err != nil {
		t.Fatal(err)
	}
	if out.String() != "no untriaged issues\n" {
		t.Errorf("output = %q", out.String())
	}
}

func TestPrime(t *testing.T) {
	inProg := issue(4, "Working", "P2", "bug", "in-progress")
	inProg.Assignees = []string{"me"}
	epicIssue := issue(10, "Epic: big", "P2")
	epicIssue.SubIssues = []model.Ref{{Number: 11, State: "OPEN"}}
	child := issue(11, "Child", "P2", "task")
	child.Parent = &model.Ref{Number: 10, State: "OPEN"}
	f := newFake(
		issue(1, "Ready one", "P2", "bug"),
		issue(3, "Untriaged one"),
		inProg, epicIssue, child,
	)
	app, out, _ := newApp(f)
	if err := app.Prime(ctx); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"# hew primer — o/r",
		"Workflow: hew ready",
		"## Ready (3 of 5 open)",
		"## In progress (1)",
		"@me",
		"## Epics",
		"#10 P2  Epic: big  0/1",
		"1 untriaged → hew triage",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Prime missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "## Warnings") {
		t.Errorf("clean repo grew warnings:\n%s", got)
	}
}

func TestPrimeWarnings(t *testing.T) {
	// A dependency cycle silently empties ready; prime is where an agent
	// learns about it, so the warnings wiring must actually be connected.
	a := issue(1, "A", "P2", "bug")
	a.BlockedBy = []model.Ref{{Number: 2, State: "OPEN"}}
	b := issue(2, "B", "P2", "bug")
	b.BlockedBy = []model.Ref{{Number: 1, State: "OPEN"}}
	f := newFake(a, b)
	app, out, _ := newApp(f)
	if err := app.Prime(ctx); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "## Warnings") || !strings.Contains(got, "dependency cycle #1 → #2 → #1") {
		t.Errorf("Prime missing cycle warning:\n%s", got)
	}
}

func TestPrimeCapsReady(t *testing.T) {
	var issues []*model.Issue
	for n := 1; n <= 15; n++ {
		issues = append(issues, issue(n, "Work", "P2", "bug"))
	}
	f := newFake(issues...)
	app, out, _ := newApp(f)
	if err := app.Prime(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "… 5 more: hew ready") {
		t.Errorf("Prime cap:\n%s", out.String())
	}
}

func TestPrimeJSON(t *testing.T) {
	f := newFake(issue(1, "Ready one", "P2", "bug"))
	app, out, _ := newApp(f)
	app.JSON = true
	if err := app.Prime(ctx); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["readyTotal"].(float64) != 1 || got["repo"] != "o/r" {
		t.Errorf("JSON = %s", out.String())
	}
}

func TestEpicStatusAll(t *testing.T) {
	epicIssue := issue(10, "Epic: big", "P2")
	epicIssue.SubIssues = []model.Ref{{Number: 11, State: "OPEN"}, {Number: 12, State: "OPEN"}}
	child := issue(11, "Child", "P2", "task")
	child.Parent = &model.Ref{Number: 10, State: "OPEN"}
	done := issue(12, "Done child", "P2", "task")
	done.State = "CLOSED"
	done.Parent = &model.Ref{Number: 10, State: "OPEN"}
	f := newFake(epicIssue, child, done)
	app, out, _ := newApp(f)
	if err := app.EpicStatus(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "#10 P2  Epic: big  1/2") {
		t.Errorf("EpicStatus all:\n%s", out.String())
	}
}

func TestEpicStatusOne(t *testing.T) {
	epicIssue := issue(10, "Epic: big", "P2")
	epicIssue.SubIssues = []model.Ref{{Number: 11, State: "OPEN"}, {Number: 12, State: "OPEN"}}
	child := issue(11, "Child", "P2", "task")
	child.Parent = &model.Ref{Number: 10, State: "OPEN"}
	done := issue(12, "Done child", "P2", "task")
	done.State = "CLOSED"
	done.Parent = &model.Ref{Number: 10, State: "OPEN"}
	f := newFake(epicIssue, child, done)
	app, out, _ := newApp(f)
	if err := app.EpicStatus(ctx, 10); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"1/2", "○ #11", "✓ #12"} {
		if !strings.Contains(got, want) {
			t.Errorf("EpicStatus missing %q:\n%s", want, got)
		}
	}
}

func TestEpicStatusUsesBacklinksNotCappedSubIssues(t *testing.T) {
	// The epic's sub-issue connection was capped: it lists only #11, but the
	// server total is higher. #12 reaches the view via its parent backlink,
	// not the (incomplete) SubIssues refs.
	epicIssue := issue(10, "Epic: big", "P2")
	epicIssue.SubIssues = []model.Ref{{Number: 11, State: "OPEN"}}
	epicIssue.SubIssuesTotal = 2
	child := issue(11, "Child", "P2", "task")
	child.Parent = &model.Ref{Number: 10, State: "OPEN"}
	missing := issue(12, "Uncapped child", "P2", "task")
	missing.Parent = &model.Ref{Number: 10, State: "OPEN"}
	f := newFake(epicIssue, child, missing)
	app, out, _ := newApp(f)
	if err := app.EpicStatus(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "#12") {
		t.Errorf("child absent from capped SubIssues was dropped:\n%s", out.String())
	}
}

func TestEpicStatusNotAnEpic(t *testing.T) {
	f := newFake(issue(1, "Plain", "P2", "bug"))
	app, _, _ := newApp(f)
	err := app.EpicStatus(ctx, 1)
	if err == nil || !strings.Contains(err.Error(), "not an epic") {
		t.Errorf("err = %v", err)
	}
	if err := app.EpicStatus(ctx, 99); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("missing-issue err = %v", err)
	}
}

func TestEpicStatusNoEpics(t *testing.T) {
	f := newFake(issue(1, "Plain", "P2", "bug"))
	app, out, _ := newApp(f)
	if err := app.EpicStatus(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if out.String() != "no epics\n" {
		t.Errorf("output = %q", out.String())
	}
}

func TestEpicStatusOneJSON(t *testing.T) {
	epicIssue := issue(10, "Epic: big", "P2")
	epicIssue.SubIssues = []model.Ref{{Number: 11, State: "OPEN"}}
	child := issue(11, "Child", "P2", "task")
	child.Parent = &model.Ref{Number: 10, State: "OPEN"}
	f := newFake(epicIssue, child)
	app, out, _ := newApp(f)
	app.JSON = true
	if err := app.EpicStatus(ctx, 10); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Epic     map[string]any   `json:"epic"`
		Children []map[string]any `json:"children"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Epic["number"].(float64) != 10 || len(got.Children) != 1 {
		t.Errorf("JSON = %s", out.String())
	}
}
