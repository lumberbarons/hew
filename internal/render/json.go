package render

import (
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/lumberbarons/hew/internal/model"
)

// IssueJSON is the stable flat schema for --json output: deps as number
// arrays, GraphQL shapes hidden.
type IssueJSON struct {
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
	// Body is a pointer so presence tracks the mode (show, list --bodies)
	// rather than emptiness: modes that carry it emit the field on every
	// issue, empty or not, and plain list lines stay byte-for-byte body-free.
	Body          *string       `json:"body,omitempty"`
	Comments      []CommentJSON `json:"comments,omitempty"`
	CommentsTotal int           `json:"commentsTotal,omitempty"`
}

// CommentJSON is one recent comment in show --json.
type CommentJSON struct {
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"createdAt"`
	Body      string    `json:"body"`
}

// ToJSON converts an issue to the flat schema. Body and comments are only
// carried when withDetail is set (show).
func ToJSON(i model.Issue, withDetail bool) IssueJSON {
	out := IssueJSON{
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
		OpenBlockers:       emptyIntNotNull(i.OpenBlockers()),
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
			out.Comments = append(out.Comments, CommentJSON{Author: c.Author, CreatedAt: c.CreatedAt, Body: c.Body})
		}
	}
	return out
}

// JSONList writes issues as NDJSON — one compact object per line. Unlike
// an array, it stays parseable under head/grep and agent output
// truncation, which is how list output actually gets consumed. withBodies
// carries each issue's body (list --bodies), turning dedup over the triaged
// tracker into a single call instead of a show per candidate.
func JSONList(w io.Writer, issues []model.Issue, withBodies bool) error {
	enc := json.NewEncoder(w)
	for _, i := range issues {
		out := ToJSON(i, false)
		if withBodies {
			out.Body = &i.Body
		}
		if err := enc.Encode(out); err != nil {
			return err
		}
	}
	return nil
}

// JSONIssue writes one issue with full detail.
func JSONIssue(w io.Writer, i model.Issue) error {
	return writeJSON(w, ToJSON(i, true))
}

// WarningJSON is one structured warning: a machine-readable kind plus the
// numbers involved, alongside the rendered human message. Consumers branch
// on kind rather than parsing message prose.
type WarningJSON struct {
	Kind    string `json:"kind"`
	Issue   int    `json:"issue,omitempty"`
	Cycle   []int  `json:"cycle,omitempty"`
	Total   int    `json:"total,omitempty"`
	Fetched int    `json:"fetched,omitempty"`
	Message string `json:"message"`
}

// PrimeJSON is the structured form of the primer's live state.
type PrimeJSON struct {
	Repo            string        `json:"repo"`
	OpenTotal       int           `json:"openTotal"`
	ReadyTotal      int           `json:"readyTotal"`
	Ready           []IssueJSON   `json:"ready"`
	InProgress      []IssueJSON   `json:"inProgress"`
	InProgressTotal int           `json:"inProgressTotal"`
	Epics           []IssueJSON   `json:"epics"`
	EpicsTotal      int           `json:"epicsTotal"`
	Untriaged       int           `json:"untriaged"`
	Warnings        []WarningJSON `json:"warnings"`
}

// warningsJSON maps structured warnings to their JSON form, attaching the
// rendered message so both machine and human consumers are served.
func warningsJSON(ws []model.Warning) []WarningJSON {
	out := make([]WarningJSON, 0, len(ws))
	for _, w := range ws {
		out = append(out, WarningJSON{
			Kind:    string(w.Kind),
			Issue:   w.Issue,
			Cycle:   w.Cycle,
			Total:   w.Total,
			Fetched: w.Fetched,
			Message: FormatWarning(w),
		})
	}
	return out
}

// JSONPrime writes the primer's live state as JSON (the static primer text
// is for context injection, not machine consumption).
func JSONPrime(w io.Writer, d PrimeData) error {
	out := PrimeJSON{
		Repo:            d.Repo,
		OpenTotal:       d.OpenTotal,
		ReadyTotal:      d.ReadyTotal,
		Ready:           toJSONList(d.Ready),
		InProgress:      toJSONList(d.InProgress),
		InProgressTotal: d.InProgressTotal,
		Epics:           toJSONList(d.Epics),
		EpicsTotal:      d.EpicsTotal,
		Untriaged:       d.Untriaged,
		Warnings:        warningsJSON(d.Warnings),
	}
	return writeJSON(w, out)
}

// CursorHookJSON writes the primer as the JSON object Cursor's sessionStart
// hook reads: its stdout must be valid JSON — bare primer text fails the
// parse — and the context travels in additional_context. The spelling is
// snake_case on purpose: Cursor ignores Claude Code's camelCase
// additionalContext field.
func CursorHookJSON(w io.Writer, primer string) error {
	return json.NewEncoder(w).Encode(map[string]string{"additional_context": primer})
}

// EpicStatusJSON pairs an epic with its resolved children.
type EpicStatusJSON struct {
	Epic     IssueJSON   `json:"epic"`
	Children []IssueJSON `json:"children"`
}

// JSONEpicStatus writes one epic and its children. Children are the epic's
// full parent-backlinked set, so every entry is a real fetched issue — no
// fabricated placeholders — and the list is complete even when the epic's
// sub-issue connection was capped.
func JSONEpicStatus(w io.Writer, epic model.Issue, children []model.Issue) error {
	out := EpicStatusJSON{Epic: ToJSON(epic, false), Children: []IssueJSON{}}
	for _, child := range children {
		out.Children = append(out.Children, ToJSON(child, false))
	}
	return writeJSON(w, out)
}

func toJSONList(issues []model.Issue) []IssueJSON {
	out := make([]IssueJSON, len(issues))
	for idx, i := range issues {
		out[idx] = ToJSON(i, false)
	}
	return out
}

func refNumbers(refs []model.Ref) []int {
	out := make([]int, len(refs))
	for idx, r := range refs {
		out[idx] = r.Number
	}
	return out
}

func emptyNotNull(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func emptyIntNotNull(s []int) []int {
	if s == nil {
		return []int{}
	}
	return s
}

// WriteJSON writes any value in the standard indented form; for the odd
// command whose output isn't issue-shaped.
func WriteJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func writeJSON(w io.Writer, v any) error { return WriteJSON(w, v) }
