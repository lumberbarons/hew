package tracker

import (
	"slices"
	"strings"
	"time"

	"github.com/lumberbarons/hew/internal/model"
)

// Issue is a tracker issue in the CLI's stable flat JSON schema:
// dependencies are number arrays and backend-specific shapes stay hidden.
//
// A library read also carries what that schema cannot: raw labels, reference
// states, the parent's state and title, and the close reason. A value rebuilt
// by unmarshaling the schema carries the exported fields only, so the
// accessors below report that extra data as absent; use a Reader or Tracker
// read when it matters.
type Issue struct {
	Number             int       `json:"number"`
	Title              string    `json:"title"`
	State              string    `json:"state"`
	Priority           *string   `json:"priority"`
	Type               *string   `json:"type"`
	Areas              []string  `json:"areas"`
	Assignees          []string  `json:"assignees"`
	Epic               bool      `json:"epic"`
	InProgress         bool      `json:"inProgress"`
	Untriaged          bool      `json:"untriaged"`
	Parent             *int      `json:"parent"`
	BlockedBy          []int     `json:"blockedBy"`
	BlockedByTotal     int       `json:"blockedByTotal"`
	OpenBlockers       []int     `json:"openBlockers"`
	SubIssues          []int     `json:"subIssues"`
	SubIssuesTotal     int       `json:"subIssuesTotal"`
	SubIssuesCompleted int       `json:"subIssuesCompleted"`
	CreatedAt          time.Time `json:"createdAt"`
	// Next is set only for an epic's detail view (show --json): the next
	// workable child's number. Absent, not null, for non-epics and when the
	// sequence is dry — consumers key on the epic field for the difference.
	Next *int `json:"next,omitempty"`
	// Body is a pointer so presence tracks the mode (show, list --bodies, a
	// snapshot read) rather than emptiness: modes that carry it emit the
	// field on every issue, empty or not, and plain list lines stay
	// byte-for-byte body-free.
	Body          *string   `json:"body,omitempty"`
	Comments      []Comment `json:"comments,omitempty"`
	CommentsTotal int       `json:"commentsTotal,omitempty"`

	// The flat schema intentionally drops the raw data a full read carries:
	// labels, reference and parent states, the parent title and the close
	// reason. They ride along unexported so marshaling stays byte-for-byte
	// the --json schema, and the accessors below expose them without making
	// internal/model part of this package's API.
	labels      []string
	stateReason string
	parentTitle string
	parent      *Ref
	blockedBy   []Ref
	subIssues   []Ref
}

// Comment is one recent issue comment.
type Comment struct {
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"createdAt"`
	Body      string    `json:"body"`
}

// Ref is a dependency or sub-issue reference: the issue's number and state.
type Ref struct {
	Number int
	State  string // "OPEN" or "CLOSED"
}

// Labels returns the issue's raw labels, in backend order. The flat schema
// derives priority, type and areas from them; a caller that wants the whole
// vocabulary reads them here instead.
func (i Issue) Labels() []string { return slices.Clone(i.labels) }

// HasLabel reports whether the issue carries the label, case-insensitively —
// the same match the CLI applies to `--label`, so a library caller and a
// command agree on membership.
func (i Issue) HasLabel(name string) bool {
	return slices.ContainsFunc(i.labels, func(l string) bool { return strings.EqualFold(l, name) })
}

// StateReason is the backend's close reason when it has one (for example
// "completed" or "not planned"); empty while the issue is open.
func (i Issue) StateReason() string { return i.stateReason }

// ParentTitle is the parent issue's title, when the issue has a parent.
func (i Issue) ParentTitle() string { return i.parentTitle }

// ParentRef returns the parent with its state. The flat schema's Parent
// carries only the number; a value unmarshaled from it has no state.
func (i Issue) ParentRef() (Ref, bool) {
	if i.parent == nil {
		return Ref{}, false
	}
	return *i.parent, true
}

// BlockedByRefs returns every blocker with its state, including closed ones —
// the flat schema's BlockedBy carries only numbers and OpenBlockers only the
// open ones.
func (i Issue) BlockedByRefs() []Ref { return i.blockedBy }

// SubIssueRefs returns the fetched sub-issues with their states. SubIssuesTotal
// may still exceed what was fetched when the connection was capped.
func (i Issue) SubIssueRefs() []Ref { return i.subIssues }

// issueFromModel converts an issue to the flat schema. Body and comments are only
// carried when withDetail is set: the single-issue reads (Get, Detail and the
// write results) and a snapshot's list reads when SnapshotOptions.Bodies asked
// for them.
func issueFromModel(i model.Issue, withDetail bool) Issue {
	out := Issue{
		Number:             i.Number,
		Title:              i.Title,
		State:              strings.ToLower(i.State),
		Areas:              emptyNotNull(i.Areas()),
		Assignees:          emptyNotNull(slices.Clone(i.Assignees)),
		Epic:               i.IsEpic(),
		InProgress:         i.InProgress(),
		Untriaged:          i.Untriaged(),
		BlockedBy:          refNumbers(i.BlockedBy),
		BlockedByTotal:     i.BlockedByTotal,
		OpenBlockers:       emptyNotNull(i.OpenBlockers()),
		SubIssues:          refNumbers(i.SubIssues),
		SubIssuesTotal:     i.SubIssuesTotal,
		SubIssuesCompleted: i.SubIssuesCompleted,
		CreatedAt:          i.CreatedAt,

		labels:      slices.Clone(i.Labels),
		stateReason: i.StateReason,
		parentTitle: i.ParentTitle,
		blockedBy:   refsFromModel(i.BlockedBy),
		subIssues:   refsFromModel(i.SubIssues),
	}
	if i.Parent != nil {
		parent := Ref{Number: i.Parent.Number, State: i.Parent.State}
		out.parent = &parent
	}
	if p, _ := i.Priority(); p != model.PriorityUnknown {
		s := p.String()
		out.Priority = &s
	}
	if t, _ := i.Type(); t != "" {
		out.Type = &t
	}
	if i.Parent != nil {
		n := i.Parent.Number
		out.Parent = &n
	}
	if withDetail {
		out.Body = &i.Body
		out.CommentsTotal = i.CommentsTotal
		for _, c := range i.Comments {
			out.Comments = append(out.Comments, Comment{Author: c.Author, CreatedAt: c.CreatedAt, Body: c.Body})
		}
	}
	return out
}

func refNumbers(refs []model.Ref) []int {
	out := make([]int, len(refs))
	for n, ref := range refs {
		out[n] = ref.Number
	}
	return out
}

// refsFromModel keeps the reference states the flat schema flattens away.
func refsFromModel(refs []model.Ref) []Ref {
	if refs == nil {
		return nil
	}
	out := make([]Ref, len(refs))
	for n, ref := range refs {
		out[n] = Ref{Number: ref.Number, State: ref.State}
	}
	return out
}

func emptyNotNull[T any](values []T) []T {
	if values == nil {
		return []T{}
	}
	return values
}
