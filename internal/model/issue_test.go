package model

import (
	"reflect"
	"testing"
)

func TestPriorityString(t *testing.T) {
	cases := map[Priority]string{P0: "P0", P2: "P2", P4: "P4", PriorityUnknown: "P?"}
	for p, want := range cases {
		if got := p.String(); got != want {
			t.Errorf("Priority(%d).String() = %q, want %q", p, got, want)
		}
	}
}

func TestParsePriority(t *testing.T) {
	for _, s := range []string{"P0", "P1", "P2", "P3", "P4"} {
		p, ok := ParsePriority(s)
		if !ok || p.String() != s {
			t.Errorf("ParsePriority(%q) = %v, %v", s, p, ok)
		}
	}
	for _, s := range []string{"p0", "P5", "", "priority", "P2 "} {
		if _, ok := ParsePriority(s); ok {
			t.Errorf("ParsePriority(%q) accepted", s)
		}
	}
}

func TestIssuePriority(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   Priority
		multi  bool
	}{
		{"none", []string{"bug"}, PriorityUnknown, false},
		{"single", []string{"P3", "bug"}, P3, false},
		{"multiple highest wins", []string{"P3", "P1"}, P1, true},
		{"duplicate-ish", []string{"P0", "P4", "P2"}, P0, true},
	}
	for _, tt := range tests {
		p, multi := Issue{Labels: tt.labels}.Priority()
		if p != tt.want || multi != tt.multi {
			t.Errorf("%s: Priority() = %v, %v; want %v, %v", tt.name, p, multi, tt.want, tt.multi)
		}
	}
}

func TestIssueType(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   string
		multi  bool
	}{
		{"none", []string{"P2"}, "", false},
		{"single", []string{"task", "P2"}, "task", false},
		{"multiple first of order wins", []string{"task", "bug"}, "bug", true},
		{"enhancement+task", []string{"task", "enhancement"}, "enhancement", true},
	}
	for _, tt := range tests {
		typ, multi := Issue{Labels: tt.labels}.Type()
		if typ != tt.want || multi != tt.multi {
			t.Errorf("%s: Type() = %q, %v; want %q, %v", tt.name, typ, multi, tt.want, tt.multi)
		}
	}
}

func TestAreas(t *testing.T) {
	i := Issue{Labels: []string{"P1", "bug", "tests", "in-progress", "web-ui"}}
	if got := i.Areas(); !reflect.DeepEqual(got, []string{"tests", "web-ui"}) {
		t.Errorf("Areas() = %v", got)
	}
	if got := (Issue{Labels: []string{"P1", "bug"}}).Areas(); got != nil {
		t.Errorf("Areas() = %v, want nil", got)
	}
}

func TestEpicInProgressOpen(t *testing.T) {
	epic := Issue{State: "OPEN", SubIssuesTotal: 2}
	if !epic.IsEpic() || !epic.IsOpen() {
		t.Error("epic should be epic and open")
	}
	if (Issue{State: "CLOSED"}).IsOpen() {
		t.Error("closed issue reported open")
	}
	if !(Issue{Labels: []string{"in-progress"}}).InProgress() {
		t.Error("in-progress label not detected")
	}
	if (Issue{Labels: []string{"bug"}}).InProgress() {
		t.Error("false in-progress")
	}
}

func TestRefIsOpen(t *testing.T) {
	if !(Ref{State: "OPEN"}).IsOpen() {
		t.Error("open ref reported closed")
	}
	if (Ref{State: "CLOSED"}).IsOpen() {
		t.Error("closed ref reported open")
	}
}

func TestClaimed(t *testing.T) {
	tests := []struct {
		name string
		i    Issue
		want bool
	}{
		{"unclaimed", Issue{Labels: []string{"bug"}}, false},
		{"in-progress label", Issue{Labels: []string{"in-progress"}}, true},
		{"assignee only", Issue{Assignees: []string{"alice"}}, true},
	}
	for _, tt := range tests {
		if got := tt.i.Claimed(); got != tt.want {
			t.Errorf("%s: Claimed() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestOpenBlockers(t *testing.T) {
	i := Issue{BlockedBy: []Ref{{Number: 1, State: "OPEN"}, {Number: 2, State: "CLOSED"}, {Number: 3, State: "OPEN"}}}
	if got := i.OpenBlockers(); !reflect.DeepEqual(got, []int{1, 3}) {
		t.Errorf("OpenBlockers() = %v", got)
	}
}

func TestUntriaged(t *testing.T) {
	tests := []struct {
		name string
		i    Issue
		want bool
	}{
		{"fully labeled", Issue{Labels: []string{"P2", "bug"}}, false},
		{"missing type", Issue{Labels: []string{"P2"}}, true},
		{"missing priority", Issue{Labels: []string{"bug"}}, true},
		{"missing both", Issue{}, true},
		{"epic exempt", Issue{SubIssuesTotal: 1}, false},
	}
	for _, tt := range tests {
		if got := tt.i.Untriaged(); got != tt.want {
			t.Errorf("%s: Untriaged() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func open(n int, labels ...string) Issue {
	return Issue{Number: n, State: "OPEN", Labels: labels}
}

func TestReady(t *testing.T) {
	issues := []Issue{
		open(1, "P2", "bug"),  // ready
		open(2, "P0", "task"), // ready, sorts first
		{Number: 3, State: "CLOSED", Labels: []string{"P0", "bug"}},                          // closed
		func() Issue { i := open(4, "P1", "bug"); i.SubIssuesTotal = 3; return i }(),         // epic
		open(5, "P1", "bug", "in-progress"),                                                  // claimed via label
		func() Issue { i := open(6, "P1", "bug"); i.Assignees = []string{"me"}; return i }(), // assigned
		func() Issue { // open blocker
			i := open(7, "P1", "bug")
			i.BlockedBy = []Ref{{Number: 1, State: "OPEN"}}
			return i
		}(),
		func() Issue { // closed blocker doesn't block
			i := open(8, "P3", "bug")
			i.BlockedBy = []Ref{{Number: 3, State: "CLOSED"}}
			return i
		}(),
		open(9),                // untriaged, excluded
		open(10, "P2", "task"), // same priority as #1, higher number, sorts after
	}
	got := Ready(issues)
	var nums []int
	for _, i := range got {
		nums = append(nums, i.Number)
	}
	want := []int{2, 1, 10, 8}
	if !reflect.DeepEqual(nums, want) {
		t.Errorf("Ready() = %v, want %v", nums, want)
	}
}

func TestReadyExcludesUntriaged(t *testing.T) {
	// Untriaged issues are the population anyone can create on a public
	// repo, and ready feeds automatic agent paths. Label presence is
	// permission-verified by GitHub, which is what makes triage state a
	// forgery-resistant gate.
	issues := []Issue{
		open(1, "P2", "bug"),  // triaged, ready
		open(2, "P1"),         // no type
		open(3, "bug"),        // no priority
		open(4),               // neither
		open(5, "P3", "task"), // triaged, ready
	}
	var nums []int
	for _, i := range Ready(issues) {
		nums = append(nums, i.Number)
	}
	if !reflect.DeepEqual(nums, []int{1, 5}) {
		t.Errorf("Ready() = %v, want [1 5]", nums)
	}
}

func TestReadyExcludesUnlabeledEpic(t *testing.T) {
	// Untriaged() exempts epics, so the untriaged gate is not what keeps an
	// unlabeled epic out of ready — the epic exclusion still has to.
	epic := open(1)
	epic.SubIssuesTotal = 1
	if got := Ready([]Issue{epic}); len(got) != 0 {
		t.Errorf("Ready() = %v, want empty", got)
	}
}

func TestInProgressEpicsUntriagedLists(t *testing.T) {
	epic := open(2, "P1")
	epic.SubIssuesTotal = 2
	closedEpic := Issue{Number: 5, State: "CLOSED", SubIssuesTotal: 1}
	issues := []Issue{
		open(1, "P2", "bug", "in-progress"),
		epic,
		closedEpic,
		open(3), // untriaged
		open(4, "P0", "bug"),
	}
	if got := InProgressIssues(issues); len(got) != 1 || got[0].Number != 1 {
		t.Errorf("InProgressIssues() = %v", got)
	}
	if got := Epics(issues); len(got) != 1 || got[0].Number != 2 {
		t.Errorf("Epics() = %v", got)
	}
	if got := UntriagedIssues(issues); len(got) != 1 || got[0].Number != 3 {
		t.Errorf("UntriagedIssues() = %v", got)
	}
}

func TestUntriagedIssuesOldestFirst(t *testing.T) {
	issues := []Issue{open(9), open(3), open(7, "P1", "bug")}
	got := UntriagedIssues(issues)
	if len(got) != 2 || got[0].Number != 3 || got[1].Number != 9 {
		t.Errorf("UntriagedIssues() = %v", got)
	}
}

func TestChildren(t *testing.T) {
	epicNum := 10
	c1 := Issue{Number: 12, State: "OPEN", Parent: &Ref{Number: epicNum}}
	c2 := Issue{Number: 11, State: "CLOSED", Parent: &Ref{Number: epicNum}}
	other := Issue{Number: 13, State: "OPEN", Parent: &Ref{Number: 99}}
	orphan := Issue{Number: 14, State: "OPEN"}
	got := Children([]Issue{c1, c2, other, orphan}, epicNum)
	if len(got) != 2 || got[0].Number != 11 || got[1].Number != 12 {
		t.Errorf("Children() = %v, want #11 then #12", got)
	}
}

func TestByNumber(t *testing.T) {
	m := ByNumber([]Issue{open(1), open(2)})
	if len(m) != 2 || m[2].Number != 2 {
		t.Errorf("ByNumber() = %v", m)
	}
}

func TestSortForList(t *testing.T) {
	epic := open(1, "P0")
	epic.SubIssuesTotal = 2
	claimed := open(2, "P3", "bug", "in-progress")
	blocked := open(3, "P0", "bug")
	blocked.BlockedBy = []Ref{{Number: 9, State: "OPEN"}}
	closed := Issue{Number: 4, State: "CLOSED", Labels: []string{"P0", "bug"}}
	ready := open(5, "P4", "bug")
	readyUrgent := open(6, "P1", "bug")
	issues := []Issue{epic, claimed, blocked, closed, ready, readyUrgent}
	SortForList(issues)
	var nums []int
	for _, i := range issues {
		nums = append(nums, i.Number)
	}
	// ready (P1 then P4), claimed, blocked, epic, closed
	want := []int{6, 5, 2, 3, 1, 4}
	if !reflect.DeepEqual(nums, want) {
		t.Errorf("SortForList order = %v, want %v", nums, want)
	}
}

func TestSortByPriorityStable(t *testing.T) {
	issues := []Issue{open(30), open(20, "P4", "bug"), open(10, "P0", "bug")}
	SortByPriority(issues)
	if issues[0].Number != 10 || issues[1].Number != 20 || issues[2].Number != 30 {
		t.Errorf("SortByPriority order: %v %v %v", issues[0].Number, issues[1].Number, issues[2].Number)
	}
}
