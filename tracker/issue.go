// Package tracker provides an in-process API for hew's tracker: open a local
// or GitHub tracker with an explicit worktree and actor, and read structured
// results and typed errors. Its API is experimental until hew 1.0.
package tracker

import (
	"strings"
	"time"

	"github.com/lumberbarons/hew/internal/model"
)

// Issue is a tracker issue in the CLI's stable flat JSON schema:
// dependencies are number arrays and backend-specific shapes stay hidden.
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
	// Body is a pointer so presence tracks the mode (show, list --bodies)
	// rather than emptiness: modes that carry it emit the field on every
	// issue, empty or not, and plain list lines stay byte-for-byte body-free.
	Body          *string   `json:"body,omitempty"`
	Comments      []Comment `json:"comments,omitempty"`
	CommentsTotal int       `json:"commentsTotal,omitempty"`
}

// Comment is one recent issue comment.
type Comment struct {
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"createdAt"`
	Body      string    `json:"body"`
}

// issueFromModel converts an issue to the flat schema. Body and comments are only
// carried when withDetail is set (show).
func issueFromModel(i model.Issue, withDetail bool) Issue {
	out := Issue{
		Number:             i.Number,
		Title:              i.Title,
		State:              strings.ToLower(i.State),
		Areas:              emptyNotNull(i.Areas()),
		Assignees:          emptyNotNull(i.Assignees),
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

func emptyNotNull[T any](values []T) []T {
	if values == nil {
		return []T{}
	}
	return values
}
