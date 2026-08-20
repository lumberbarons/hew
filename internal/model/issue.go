// Package model holds the domain types and the pure read-path logic:
// label normalization, ready-work detection, and dependency-cycle detection.
package model

import (
	"slices"
	"sort"
	"time"
)

// Priority is the P0 (critical) to P4 (backlog) scale, plus Unknown for
// issues with no priority label. Lower values are more urgent.
type Priority int

const (
	P0 Priority = iota
	P1
	P2
	P3
	P4
	// PriorityUnknown sorts after P4; rendered as "P?".
	PriorityUnknown
)

// DefaultPriority is applied by the write path when none is given.
const DefaultPriority = P2

var priorityNames = [...]string{"P0", "P1", "P2", "P3", "P4"}

func (p Priority) String() string {
	if p >= P0 && p <= P4 {
		return priorityNames[p]
	}
	return "P?"
}

// ParsePriority parses "P0".."P4" (exact match, as used in labels and flags).
func ParsePriority(s string) (Priority, bool) {
	for i, name := range priorityNames {
		if s == name {
			return Priority(i), true
		}
	}
	return PriorityUnknown, false
}

// Types lists the type labels in normalization precedence order: when an
// issue carries more than one, the first of these wins.
var Types = []string{"bug", "enhancement", "task"}

// IsType reports whether the label name is one of the type labels.
func IsType(label string) bool {
	return slices.Contains(Types, label)
}

// InProgressLabel marks claimed issues; the assignee is the claim, the label
// is the visibility.
const InProgressLabel = "in-progress"

// LabelVocabulary returns every label name the tool assigns meaning to, in a
// stable order: priorities, then types, then the in-progress label. It is the
// single source of truth for label names — conventions attaches the cosmetics
// (color, description) keyed by these names.
func LabelVocabulary() []string {
	out := make([]string, 0, len(priorityNames)+len(Types)+1)
	out = append(out, priorityNames[:]...)
	out = append(out, Types...)
	out = append(out, InProgressLabel)
	return out
}

// stateOpen is GitHub's issue state enum for an open issue, as it appears
// on the wire.
const stateOpen = "OPEN"

// stateIsOpen is the single definition of issue openness; every State
// comparison goes through it so casing/polarity live in one place.
func stateIsOpen(state string) bool {
	return state == stateOpen
}

// Ref is a lightweight reference to another issue, as returned inside
// nested connections (blockers, sub-issues).
type Ref struct {
	Number int
	State  string // "OPEN" or "CLOSED"
}

// IsOpen reports whether the referenced issue is open.
func (r Ref) IsOpen() bool {
	return stateIsOpen(r.State)
}

// Comment is a recent issue comment (populated by show only).
type Comment struct {
	Author    string
	CreatedAt time.Time
	Body      string
}

// Issue is the normalized domain shape of a GitHub issue. Raw labels are
// kept as-is; priority/type/areas are derived by the methods below.
type Issue struct {
	ID          string // GraphQL node ID, needed for mutations
	Number      int
	Title       string
	Body        string
	State       string // "OPEN" or "CLOSED"
	StateReason string
	CreatedAt   time.Time
	Labels      []string
	Assignees   []string
	Parent      *Ref
	ParentTitle string
	SubIssues   []Ref
	// SubIssuesTotal is the server-side total; may exceed len(SubIssues)
	// when the nested connection was capped.
	SubIssuesTotal     int
	SubIssuesCompleted int
	BlockedBy          []Ref
	// BlockedByTotal is the server-side total; may exceed len(BlockedBy)
	// when the nested connection was capped.
	BlockedByTotal int
	Comments       []Comment
	// CommentsTotal is the server-side total; may exceed len(Comments) when
	// only the most recent were fetched.
	CommentsTotal int
}

// Priority returns the effective priority. When an issue carries multiple
// priority labels the highest urgency wins and multi is true (a
// contradiction the warnings pass reports).
func (i Issue) Priority() (p Priority, multi bool) {
	p = PriorityUnknown
	n := 0
	for _, l := range i.Labels {
		if v, ok := ParsePriority(l); ok {
			n++
			if v < p {
				p = v
			}
		}
	}
	return p, n > 1
}

// Type returns the effective type ("" when untyped). When an issue carries
// multiple type labels the first of bug|enhancement|task wins and multi is
// true.
func (i Issue) Type() (typ string, multi bool) {
	n := 0
	for _, t := range Types {
		for _, l := range i.Labels {
			if l == t {
				n++
				if typ == "" {
					typ = t
				}
			}
		}
	}
	return typ, n > 1
}

// Areas returns all labels that are not priority, type, or in-progress.
func (i Issue) Areas() []string {
	var areas []string
	for _, l := range i.Labels {
		if _, ok := ParsePriority(l); ok {
			continue
		}
		if IsType(l) || l == InProgressLabel {
			continue
		}
		areas = append(areas, l)
	}
	return areas
}

// IsEpic reports whether the issue has sub-issues. The "Epic: " title
// prefix is cosmetic; sub-issues are the truth.
func (i Issue) IsEpic() bool {
	return i.SubIssuesTotal > 0
}

// InProgress reports whether the issue carries the in-progress label.
func (i Issue) InProgress() bool {
	return slices.Contains(i.Labels, InProgressLabel)
}

// IsOpen reports whether the issue is open.
func (i Issue) IsOpen() bool {
	return stateIsOpen(i.State)
}

// Claimed reports whether someone has taken the issue: the assignee is the
// claim, the in-progress label is the visibility. Either half counts.
func (i Issue) Claimed() bool {
	return i.InProgress() || len(i.Assignees) > 0
}

// OpenBlockers returns the numbers of open blocking issues; closed blockers
// are non-blocking.
func (i Issue) OpenBlockers() []int {
	var out []int
	for _, b := range i.BlockedBy {
		if b.IsOpen() {
			out = append(out, b.Number)
		}
	}
	return out
}

// Untriaged reports whether a non-epic issue is missing its priority or
// type label — a normal state for issues filed outside the tool, never a
// defect. Epics are containers and exempt.
func (i Issue) Untriaged() bool {
	if i.IsEpic() {
		return false
	}
	p, _ := i.Priority()
	t, _ := i.Type()
	return p == PriorityUnknown || t == ""
}

// SortByPriority orders issues P0→P4 then P?, oldest (lowest number) first
// within a priority. Stable and deterministic.
func SortByPriority(issues []Issue) {
	sort.SliceStable(issues, func(a, b int) bool {
		pa, _ := issues[a].Priority()
		pb, _ := issues[b].Priority()
		if pa != pb {
			return pa < pb
		}
		return issues[a].Number < issues[b].Number
	})
}

// listBucket groups issues for list output: actionable work first, then
// claimed, then blocked, then epics, then closed. The bucket answers "why
// isn't this ready?" by position alone.
func listBucket(i Issue) int {
	switch {
	case !i.IsOpen():
		return 4
	case i.IsEpic():
		return 3
	case i.Claimed():
		return 1
	case len(i.OpenBlockers()) > 0:
		return 2
	default:
		return 0
	}
}

// SortForList orders hew ready-first (each bucket P0→P4 then P?, oldest
// first), so one `hew list` answers both "what's actionable" and "what
// exists but is stuck".
func SortForList(issues []Issue) {
	sort.SliceStable(issues, func(a, b int) bool {
		ba, bb := listBucket(issues[a]), listBucket(issues[b])
		if ba != bb {
			return ba < bb
		}
		pa, _ := issues[a].Priority()
		pb, _ := issues[b].Priority()
		if pa != pb {
			return pa < pb
		}
		return issues[a].Number < issues[b].Number
	})
}

// Ready returns open, non-epic, unclaimed, triaged issues with zero open
// blockers, sorted by priority.
//
// Untriaged issues are excluded because ready feeds the automatic paths —
// `hew ready` and the primer — where issue text reaches an agent with no
// human in between. Anyone can open an issue on a public repo, but GitHub
// drops labels from non-collaborators, so a priority and type label is
// evidence that a maintainer saw the issue. That makes triage state a
// forgery-resistant gate, and it costs hew-created issues nothing: the write
// path labels them by construction. The work is not hidden — `hew triage`
// and `hew list` still show it, and the primer carries the count.
func Ready(issues []Issue) []Issue {
	var out []Issue
	for _, i := range issues {
		if !i.IsOpen() || i.IsEpic() || i.Claimed() || i.Untriaged() {
			continue
		}
		if len(i.OpenBlockers()) > 0 {
			continue
		}
		out = append(out, i)
	}
	SortByPriority(out)
	return out
}

// InProgressIssues returns open issues carrying the in-progress label,
// sorted by priority.
func InProgressIssues(issues []Issue) []Issue {
	var out []Issue
	for _, i := range issues {
		if i.IsOpen() && i.InProgress() {
			out = append(out, i)
		}
	}
	SortByPriority(out)
	return out
}

// Epics returns open issues that have sub-issues, sorted by priority.
func Epics(issues []Issue) []Issue {
	var out []Issue
	for _, i := range issues {
		if i.IsOpen() && i.IsEpic() {
			out = append(out, i)
		}
	}
	SortByPriority(out)
	return out
}

// UntriagedIssues returns open untriaged issues, oldest first.
func UntriagedIssues(issues []Issue) []Issue {
	var out []Issue
	for _, i := range issues {
		if i.IsOpen() && i.Untriaged() {
			out = append(out, i)
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Number < out[b].Number })
	return out
}

// Children returns the issues whose parent is the given epic, oldest first.
// It is derived from parent backlinks over the full fetched set, so it stays
// complete even when the epic's sub-issue connection was capped — unlike the
// epic's own SubIssues refs.
func Children(issues []Issue, epic int) []Issue {
	var out []Issue
	for _, i := range issues {
		if i.Parent != nil && i.Parent.Number == epic {
			out = append(out, i)
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Number < out[b].Number })
	return out
}

// ByNumber indexes issues by number.
func ByNumber(issues []Issue) map[int]Issue {
	m := make(map[int]Issue, len(issues))
	for _, i := range issues {
		m[i.Number] = i
	}
	return m
}
