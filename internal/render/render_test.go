package render

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lumberbarons/hew/internal/model"
)

var update = flag.Bool("update", false, "rewrite golden files")

func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden (run go test ./internal/render -update): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("output mismatch for %s\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func ts(day int) time.Time {
	return time.Date(2026, 7, day, 12, 0, 0, 0, time.UTC)
}

func fixtureIssues() []model.Issue {
	return []model.Issue{
		{
			Number: 120, Title: "Voltgo BLE battery controller: scaffold, client, collector",
			State: "OPEN", CreatedAt: ts(1), Labels: []string{"P2", "enhancement"},
		},
		{
			Number: 117, Title: "Tautological assertions on state the code cannot modify",
			State: "OPEN", CreatedAt: ts(2), Labels: []string{"P1", "bug", "tests"},
		},
		{
			Number: 9, Title: "Drive-by report with no labels",
			State: "OPEN", CreatedAt: ts(3),
		},
		{
			Number: 55, Title: "Closed one",
			State: "CLOSED", CreatedAt: ts(4), Labels: []string{"P3", "task"},
		},
	}
}

func TestList(t *testing.T) {
	issues := fixtureIssues()
	blocked := model.Issue{
		Number: 121, Title: "Voltgo client for tests", State: "OPEN", CreatedAt: ts(9),
		Labels:    []string{"P2", "enhancement"},
		BlockedBy: []model.Ref{{Number: 120, State: "OPEN"}, {Number: 8, State: "CLOSED"}},
	}
	epic := model.Issue{
		Number: 137, Title: "Epic: Voltgo", State: "OPEN", CreatedAt: ts(5),
		Labels:         []string{"P2"},
		SubIssuesTotal: 6, SubIssuesCompleted: 2,
		SubIssues: []model.Ref{{Number: 120, State: "OPEN"}},
	}
	claimed := model.Issue{
		Number: 124, Title: "Claimed one", State: "OPEN", CreatedAt: ts(8),
		Labels: []string{"P2", "bug", "in-progress"}, Assignees: []string{"lumberbarons"},
	}
	var buf bytes.Buffer
	List(&buf, append(issues, blocked, epic, claimed), Style{})
	checkGolden(t, "list", buf.Bytes())
}

func TestListWithAssignees(t *testing.T) {
	issues := fixtureIssues()[:2]
	issues[0].Assignees = []string{"lumberbarons"}
	issues[0].Labels = append(issues[0].Labels, "in-progress")
	var buf bytes.Buffer
	ListWithAssignees(&buf, issues, Style{})
	checkGolden(t, "list_assignees", buf.Bytes())
}

func TestEpicList(t *testing.T) {
	epic := model.Issue{
		Number: 137, Title: "Epic: Voltgo BLE battery controller support",
		State: "OPEN", CreatedAt: ts(5), Labels: []string{"P2"},
		SubIssuesTotal: 6, SubIssuesCompleted: 2,
		SubIssues: []model.Ref{{Number: 120, State: "OPEN"}},
	}
	var buf bytes.Buffer
	EpicList(&buf, []model.Issue{epic}, Style{})
	checkGolden(t, "epic_list", buf.Bytes())
}

func TestShow(t *testing.T) {
	i := model.Issue{
		Number: 42, Title: "Fix the frobnicator", State: "OPEN", CreatedAt: ts(6),
		Labels:    []string{"P1", "bug", "tests"},
		Assignees: []string{"lumberbarons"},
		Parent:    &model.Ref{Number: 137, State: "OPEN"}, ParentTitle: "Epic: Voltgo",
		BlockedBy: []model.Ref{{Number: 7, State: "OPEN"}, {Number: 8, State: "CLOSED"}},
		Body:      "### Where\n\ninternal/frob\n\n### Problem\n\nIt wobbles.",
		Comments: []model.Comment{
			{Author: "alice", CreatedAt: ts(7), Body: "repro attached"},
		},
		CommentsTotal: 12,
	}
	var buf bytes.Buffer
	Show(&buf, i, Style{})
	checkGolden(t, "show", buf.Bytes())
}

func TestShowClosedMinimal(t *testing.T) {
	i := model.Issue{
		Number: 55, Title: "Closed one", State: "CLOSED", StateReason: "NOT_PLANNED",
		CreatedAt: ts(4), Labels: []string{"P3", "task"},
	}
	var buf bytes.Buffer
	Show(&buf, i, Style{})
	checkGolden(t, "show_closed", buf.Bytes())
}

func TestShowEpic(t *testing.T) {
	i := model.Issue{
		Number: 137, Title: "Epic: Voltgo", State: "OPEN", CreatedAt: ts(5),
		Labels:         []string{"P2"},
		SubIssuesTotal: 2, SubIssuesCompleted: 1,
		SubIssues: []model.Ref{{Number: 120, State: "OPEN"}, {Number: 121, State: "CLOSED"}},
	}
	var buf bytes.Buffer
	Show(&buf, i, Style{})
	checkGolden(t, "show_epic", buf.Bytes())
}

func TestEpicStatus(t *testing.T) {
	epic := model.Issue{
		Number: 137, Title: "Epic: Voltgo", State: "OPEN", CreatedAt: ts(5),
		Labels:         []string{"P2"},
		SubIssuesTotal: 3, SubIssuesCompleted: 1,
	}
	children := []model.Issue{
		{Number: 120, Title: "Open child", State: "OPEN", Labels: []string{"P1", "bug"}},
		{Number: 121, Title: "Done child", State: "CLOSED", Labels: []string{"P2", "task"}},
	}
	var buf bytes.Buffer
	EpicStatus(&buf, epic, children, Style{})
	checkGolden(t, "epic_status", buf.Bytes())
}

func TestPrime(t *testing.T) {
	issues := fixtureIssues()
	inProgress := model.Issue{
		Number: 124, Title: "/api/info verified by substring matching",
		State: "OPEN", CreatedAt: ts(8),
		Labels:    []string{"P2", "bug", "tests", "in-progress"},
		Assignees: []string{"lumberbarons"},
	}
	epic := model.Issue{
		Number: 137, Title: "Voltgo BLE battery controller support",
		State: "OPEN", CreatedAt: ts(5), Labels: []string{"P2"},
		SubIssuesTotal: 6, SubIssuesCompleted: 0,
		SubIssues: []model.Ref{{Number: 120, State: "OPEN"}},
	}
	d := PrimeData{
		Repo:            "lumberbarons/solar-controller",
		Ready:           model.Ready(issues),
		ReadyTotal:      2, // the fixture's third ready-shaped issue is untriaged
		OpenTotal:       14,
		InProgress:      []model.Issue{inProgress},
		InProgressTotal: 1,
		Epics:           []model.Issue{epic},
		EpicsTotal:      1,
		Warnings:        []model.Warning{{Kind: model.WarnMultiPriority, Issue: 42}},
		Untriaged:       7,
	}
	var buf bytes.Buffer
	Prime(&buf, "Workflow: hew ready → hew start <n>.", d, Style{})
	checkGolden(t, "prime", buf.Bytes())
}

func TestPrimeEmptySectionsOmitted(t *testing.T) {
	d := PrimeData{Repo: "o/r", OpenTotal: 0, ReadyTotal: 0}
	var buf bytes.Buffer
	Prime(&buf, "static", d, Style{})
	out := buf.String()
	if strings.Contains(out, "In progress") || strings.Contains(out, "Epics") ||
		strings.Contains(out, "Warnings") || strings.Contains(out, "untriaged") {
		t.Errorf("empty sections rendered:\n%s", out)
	}
	if !strings.Contains(out, "no ready work") {
		t.Errorf("missing no-ready placeholder:\n%s", out)
	}
}

func TestPrimeMoreLine(t *testing.T) {
	issues := fixtureIssues()
	d := PrimeData{Repo: "o/r", Ready: model.Ready(issues)[:1], ReadyTotal: 3, OpenTotal: 5}
	var buf bytes.Buffer
	Prime(&buf, "static", d, Style{})
	if !strings.Contains(buf.String(), "… 2 more: hew ready") {
		t.Errorf("missing more line:\n%s", buf.String())
	}
}

func TestJSONList(t *testing.T) {
	issues := fixtureIssues()
	issues[1].BlockedBy = []model.Ref{{Number: 9, State: "OPEN"}, {Number: 55, State: "CLOSED"}}
	issues[1].Parent = &model.Ref{Number: 137, State: "OPEN"}
	var buf bytes.Buffer
	if err := JSONList(&buf, issues, false); err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "list_json", buf.Bytes())
}

func TestJSONListBodies(t *testing.T) {
	issues := fixtureIssues()
	issues[0].Body = "### Goal\n\nDedup in one call."
	issues[1].Body = "" // empty body still gets the field: consumers key on it
	var buf bytes.Buffer
	if err := JSONList(&buf, issues, true); err != nil {
		t.Fatal(err)
	}
	bodies := map[float64]any{}
	for ln := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
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
	if len(bodies) != len(issues) {
		t.Fatalf("bodies on %d of %d lines", len(bodies), len(issues))
	}
	// Exact values, not just key presence: an empty body must arrive as ""
	// — null or a dropped field would break consumers keying on it.
	if bodies[120] != "### Goal\n\nDedup in one call." || bodies[117] != "" {
		t.Errorf("bodies = %#v", bodies)
	}
}

func TestJSONIssue(t *testing.T) {
	i := model.Issue{
		Number: 42, Title: "T", State: "OPEN", CreatedAt: ts(6),
		Labels:        []string{"P1", "bug"},
		Body:          "body here",
		Comments:      []model.Comment{{Author: "alice", CreatedAt: ts(7), Body: "hi"}},
		CommentsTotal: 3,
	}
	var buf bytes.Buffer
	if err := JSONIssue(&buf, i); err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "show_json", buf.Bytes())
}

func TestJSONPrime(t *testing.T) {
	d := PrimeData{
		Repo: "o/r", OpenTotal: 2, ReadyTotal: 1,
		Ready:     model.Ready(fixtureIssues())[:1],
		Untriaged: 1,
	}
	var buf bytes.Buffer
	if err := JSONPrime(&buf, d); err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "prime_json", buf.Bytes())
}

func TestJSONPrimeWarningsAreStructured(t *testing.T) {
	d := PrimeData{
		Repo: "o/r", OpenTotal: 1, ReadyTotal: 0,
		Warnings: []model.Warning{
			{Kind: model.WarnBlockersCapped, Issue: 7, Total: 25, Fetched: 1},
			{Kind: model.WarnDependencyCycle, Cycle: []int{8, 9, 8}},
		},
	}
	var buf bytes.Buffer
	if err := JSONPrime(&buf, d); err != nil {
		t.Fatal(err)
	}
	var got PrimeJSON
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Warnings) != 2 {
		t.Fatalf("warnings = %v", got.Warnings)
	}
	if got.Warnings[0].Kind != "blockers-truncated" || got.Warnings[0].Issue != 7 ||
		got.Warnings[0].Total != 25 || got.Warnings[0].Fetched != 1 {
		t.Errorf("blocker warning not machine-readable: %+v", got.Warnings[0])
	}
	if got.Warnings[1].Kind != "dependency-cycle" || !reflect.DeepEqual(got.Warnings[1].Cycle, []int{8, 9, 8}) {
		t.Errorf("cycle warning not machine-readable: %+v", got.Warnings[1])
	}
	if got.Warnings[1].Message == "" {
		t.Error("warning missing human-readable message")
	}
}

func TestJSONEpicStatus(t *testing.T) {
	epic := model.Issue{
		Number: 137, Title: "Epic: Voltgo", State: "OPEN", CreatedAt: ts(5),
		Labels:         []string{"P2"},
		SubIssuesTotal: 2, SubIssuesCompleted: 1,
	}
	children := []model.Issue{
		{Number: 120, Title: "Open child", State: "OPEN", CreatedAt: ts(4), Labels: []string{"P1", "bug"}},
		{Number: 121, Title: "Done child", State: "CLOSED", CreatedAt: ts(3), Labels: []string{"P2", "task"}},
	}
	var buf bytes.Buffer
	if err := JSONEpicStatus(&buf, epic, children); err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "epic_status_json", buf.Bytes())
}

func TestLine(t *testing.T) {
	i := model.Issue{Number: 7, Title: "T", State: "OPEN", Labels: []string{"bug"}}
	if got := Line(i, Style{}); got != "#7 P? bug  T" {
		t.Errorf("Line() = %q", got)
	}
}
