// Package plan parses the JSONL plan files consumed by `hew apply`: one
// entry per line, each describing an issue to create. Entries may carry a
// local id so other lines can reference them (parent, blocked-by) before
// issue numbers exist — the same way migrate resolves bead IDs. The entry
// model, local-id resolution and cycle rejection live in tracker, where the
// same typed entries are accepted from callers that build plans in code;
// this package is only the file format.
package plan

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/lumberbarons/hew/internal/model"
	"github.com/lumberbarons/hew/tracker"
)

// TypeEpic is the plan-only type marking a parent issue.
const TypeEpic = tracker.PlanTypeEpic

// Entry is one issue to create.
type Entry = tracker.PlanEntry

// Ref points at another issue: a plan entry by local id, or an existing
// issue by number. Exactly one field is set.
type Ref = tracker.PlanRef

// EdgeKind names the two dependency edges an entry can declare.
type EdgeKind = tracker.EdgeKind

const (
	// ParentEdge is the sub-issue edge.
	ParentEdge = tracker.ParentEdge
	// BlockedByEdge is the dependency edge.
	BlockedByEdge = tracker.BlockedByEdge
)

// Edge is one unresolved dependency an entry declares.
type Edge = tracker.Edge

// ExistingRef is a numeric reference to an issue outside the plan.
type ExistingRef = tracker.ExistingRef

// rawEntry is the wire shape of one line; field names match the create
// command's flags.
type rawEntry struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Type           string   `json:"type"`
	Priority       string   `json:"priority"`
	Areas          []string `json:"areas"`
	Body           string   `json:"body"`
	Where          string   `json:"where"`
	Problem        string   `json:"problem"`
	Goal           string   `json:"goal"`
	Fix            string   `json:"fix"`
	Approach       string   `json:"approach"`
	DoneWhen       []string `json:"done-when"`
	Parent         *rawRef  `json:"parent"`
	BlockedBy      []rawRef `json:"blocked-by"`
	DiscoveredFrom int      `json:"discovered-from"`
}

// rawRef accepts a JSON string (a local id) or number (an existing issue
// number) — the JSON type is what disambiguates the two.
type rawRef struct {
	Ref Ref
}

func (r *rawRef) UnmarshalJSON(data []byte) error {
	var n int
	if err := json.Unmarshal(data, &n); err == nil {
		if n <= 0 {
			return fmt.Errorf("issue number must be positive (got %d)", n)
		}
		r.Ref = Ref{Number: n}
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		if s == "" {
			return fmt.Errorf("reference id cannot be empty")
		}
		r.Ref = Ref{ID: s}
		return nil
	}
	return fmt.Errorf("reference must be a local id (string) or an issue number")
}

// Parse reads and validates a JSONL plan. Blank lines are skipped; unknown
// fields are errors (a typoed "blockedby" silently dropping dependencies is
// exactly the failure mode a batch tool must not have). Validation is
// tracker.ValidatePlan's: every local reference must resolve, and dependency
// or parent cycles between entries are rejected.
//
// The idea type's area reservation is the one rule Parse cannot apply: a plan
// written before `idea` was a type may carry it as an area on an entry a
// pre-upgrade checkpoint already created. tracker.ApplyPlan enforces the
// reservation on pending entries, which is every entry of a fresh plan.
func Parse(r io.Reader) ([]Entry, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	var entries []Entry
	line := 0
	for scanner.Scan() {
		line++
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		e, err := parseLine(raw)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		e.Line = line
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if err := tracker.ValidatePlan(entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func parseLine(raw []byte) (Entry, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var re rawEntry
	if err := dec.Decode(&re); err != nil {
		return Entry{}, err
	}
	priority := re.Priority
	if priority == "" {
		priority = model.DefaultPriority.String()
	}
	entry := Entry{
		ID:       re.ID,
		Title:    re.Title,
		Type:     re.Type,
		Priority: priority,
		Areas:    re.Areas,
		Body:     re.Body,
		Sections: tracker.Sections{
			Where: re.Where, Problem: re.Problem, Goal: re.Goal,
			Fix: re.Fix, Approach: re.Approach, DoneWhen: re.DoneWhen,
		},
		DiscoveredFrom: re.DiscoveredFrom,
	}
	if re.Parent != nil {
		parent := re.Parent.Ref
		entry.Parent = &parent
	}
	for _, r := range re.BlockedBy {
		entry.BlockedBy = append(entry.BlockedBy, r.Ref)
	}
	return entry, nil
}
