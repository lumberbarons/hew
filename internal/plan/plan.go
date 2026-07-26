// Package plan parses the JSONL plan files consumed by `issues apply`: one
// entry per line, each describing an issue to create. Entries may carry a
// local id so other lines can reference them (parent, blocked-by) before
// issue numbers exist — the same way migrate resolves bead IDs. Parsing and
// validation are pure; the cli layer does the writing.
package plan

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/lumberbarons/issues/internal/conventions"
	"github.com/lumberbarons/issues/internal/model"
)

// TypeEpic is the plan-only type marking a parent issue: no type label, the
// cosmetic Epic title prefix, and (usually) other entries pointing at it.
const TypeEpic = "epic"

// Ref points at another issue: a plan entry by local id, or an existing
// issue by number. Exactly one field is set.
type Ref struct {
	ID     string
	Number int
}

func (r Ref) String() string {
	if r.ID != "" {
		return r.ID
	}
	return fmt.Sprintf("#%d", r.Number)
}

// UnmarshalJSON accepts a JSON string (a local id) or number (an existing
// issue number) — the JSON type is what disambiguates the two.
func (r *Ref) UnmarshalJSON(data []byte) error {
	var n int
	if err := json.Unmarshal(data, &n); err == nil {
		if n <= 0 {
			return fmt.Errorf("issue number must be positive (got %d)", n)
		}
		*r = Ref{Number: n}
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		if s == "" {
			return fmt.Errorf("reference id cannot be empty")
		}
		*r = Ref{ID: s}
		return nil
	}
	return fmt.Errorf("reference must be a local id (string) or an issue number")
}

// Entry is one issue to create.
type Entry struct {
	ID             string
	Title          string
	Type           string // bug|enhancement|task, or TypeEpic
	Priority       model.Priority
	Areas          []string
	Body           string
	Sections       conventions.Sections
	Parent         *Ref
	BlockedBy      []Ref
	DiscoveredFrom int
	// Line is the entry's 1-based line in the plan file.
	Line int
}

// Key identifies the entry in checkpoint state and progress output: its id,
// or its line for id-less entries.
func (e Entry) Key() string {
	if e.ID != "" {
		return e.ID
	}
	return fmt.Sprintf("line:%d", e.Line)
}

// IsEpic reports whether the entry declares a parent issue.
func (e Entry) IsEpic() bool { return e.Type == TypeEpic }

// EdgeKind names the two dependency edges an entry can declare. The strings
// are the words the wiring step reports edges by, and — once resolved to
// issue numbers — part of the checkpoint key that keeps a resume from
// re-attempting an edge it already wired.
type EdgeKind string

const (
	ParentEdge    EdgeKind = "parent"
	BlockedByEdge EdgeKind = "blocked-by"
)

// Edge is one dependency the entry declares, still unresolved: the caller
// maps the reference to an issue number.
type Edge struct {
	Kind EdgeKind
	To   Ref
}

// Edges returns the entry's dependency edges in wiring order — parent
// first, then blockers. Both are the same operation apart from the API call
// they make, so stating them as one list lets the wiring step treat them
// alike: resolve, skip if already wired, warn on failure.
func (e Entry) Edges() []Edge {
	var out []Edge
	if e.Parent != nil {
		out = append(out, Edge{Kind: ParentEdge, To: *e.Parent})
	}
	for _, b := range e.BlockedBy {
		out = append(out, Edge{Kind: BlockedByEdge, To: b})
	}
	return out
}

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
	Parent         *Ref     `json:"parent"`
	BlockedBy      []Ref    `json:"blocked-by"`
	DiscoveredFrom int      `json:"discovered-from"`
}

// Parse reads and validates a JSONL plan. Blank lines are skipped; unknown
// fields are errors (a typoed "blockedby" silently dropping dependencies is
// exactly the failure mode a batch tool must not have). References may point
// forward — creation happens before wiring — but every local reference must
// resolve, and dependency or parent cycles between entries are rejected.
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
	if err := validate(entries); err != nil {
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
	if re.Title == "" {
		return Entry{}, fmt.Errorf("title is required")
	}
	if re.Type != TypeEpic && !model.IsType(re.Type) {
		return Entry{}, fmt.Errorf("type must be one of %s|%s (got %q)", strings.Join(model.Types, "|"), TypeEpic, re.Type)
	}
	priority := model.DefaultPriority
	if re.Priority != "" {
		p, ok := model.ParsePriority(re.Priority)
		if !ok {
			return Entry{}, fmt.Errorf("priority must be P0..P4 (got %q)", re.Priority)
		}
		priority = p
	}
	for _, area := range re.Areas {
		if _, ok := model.ParsePriority(area); ok || model.IsType(area) || area == model.InProgressLabel {
			return Entry{}, fmt.Errorf("area %q collides with a convention label", area)
		}
	}
	if re.DiscoveredFrom < 0 {
		return Entry{}, fmt.Errorf("discovered-from must be an issue number (got %d)", re.DiscoveredFrom)
	}
	sections := conventions.Sections{
		Where: re.Where, Problem: re.Problem, Goal: re.Goal,
		Fix: re.Fix, Approach: re.Approach, DoneWhen: re.DoneWhen,
	}
	// The section fields follow the create flags' rules: wording pairs pick
	// one, and structured sections don't mix with a raw body.
	if re.Problem != "" && re.Goal != "" {
		return Entry{}, fmt.Errorf("problem and goal are mutually exclusive")
	}
	if re.Fix != "" && re.Approach != "" {
		return Entry{}, fmt.Errorf("fix and approach are mutually exclusive")
	}
	for _, item := range re.DoneWhen {
		if strings.TrimSpace(item) == "" {
			return Entry{}, fmt.Errorf("done-when items cannot be empty")
		}
	}
	if re.Body != "" && !sections.IsZero() {
		return Entry{}, fmt.Errorf("body and section fields (where/problem/goal/fix/approach/done-when) are mutually exclusive")
	}
	return Entry{
		ID: re.ID, Title: re.Title, Type: re.Type, Priority: priority,
		Areas: re.Areas, Body: re.Body, Sections: sections, Parent: re.Parent,
		BlockedBy: re.BlockedBy, DiscoveredFrom: re.DiscoveredFrom,
	}, nil
}

func validate(entries []Entry) error {
	ids := map[string]int{} // id → line, for duplicate reporting
	for _, e := range entries {
		if e.ID == "" {
			continue
		}
		if strings.HasPrefix(e.ID, "line:") {
			return fmt.Errorf("line %d: id %q is reserved (line:N identifies id-less entries)", e.Line, e.ID)
		}
		if prev, ok := ids[e.ID]; ok {
			return fmt.Errorf("line %d: duplicate id %q (first used on line %d)", e.Line, e.ID, prev)
		}
		ids[e.ID] = e.Line
	}
	for _, e := range entries {
		if e.Parent != nil {
			if err := checkRef(*e.Parent, e, ids, "parent"); err != nil {
				return err
			}
		}
		seen := map[string]bool{}
		for _, b := range e.BlockedBy {
			if err := checkRef(b, e, ids, "blocked-by"); err != nil {
				return err
			}
			if seen[b.String()] {
				return fmt.Errorf("line %d: duplicate blocked-by %s", e.Line, b)
			}
			seen[b.String()] = true
		}
	}
	if cyc := findCycle(entries, blockedByEdges); cyc != nil {
		return fmt.Errorf("dependency cycle in plan: %s", strings.Join(cyc, " → "))
	}
	if cyc := findCycle(entries, parentEdges); cyc != nil {
		return fmt.Errorf("parent cycle in plan: %s", strings.Join(cyc, " → "))
	}
	return nil
}

// checkRef validates a single reference. Numeric refs pass — they point at
// existing issues the API will verify at wire time.
func checkRef(r Ref, e Entry, ids map[string]int, kind string) error {
	if r.ID == "" {
		return nil
	}
	if r.ID == e.ID {
		return fmt.Errorf("line %d: entry cannot be its own %s", e.Line, kind)
	}
	if _, ok := ids[r.ID]; !ok {
		return fmt.Errorf("line %d: %s %q does not match any entry id", e.Line, kind, r.ID)
	}
	return nil
}

func blockedByEdges(e Entry) []string {
	var out []string
	for _, b := range e.BlockedBy {
		if b.ID != "" {
			out = append(out, b.ID)
		}
	}
	return out
}

func parentEdges(e Entry) []string {
	if e.Parent != nil && e.Parent.ID != "" {
		return []string{e.Parent.ID}
	}
	return nil
}

// findCycle looks for a cycle among plan-local edges, returned in edge order
// with the closing member repeated (a → b → a), or nil. Checking only the
// plan's own edges is a complete check: an issue that exists before the plan
// runs cannot reference an entry that doesn't exist yet, so no cycle can pass
// through an existing issue. The check matters because GitHub silently
// accepts dependency cycles longer than two issues (see DESIGN.md).
func findCycle(entries []Entry, edges func(Entry) []string) []string {
	adj := map[string][]string{}
	for _, e := range entries {
		if e.ID != "" {
			adj[e.ID] = edges(e)
		}
	}

	const (
		white = 0 // unvisited
		gray  = 1 // on current DFS path
		black = 2 // done
	)
	color := map[string]int{}
	var path []string
	onPath := map[string]int{} // id → index in path
	var found []string

	var dfs func(n string) bool
	dfs = func(n string) bool {
		color[n] = gray
		onPath[n] = len(path)
		path = append(path, n)
		for _, m := range adj[n] {
			switch color[m] {
			case white:
				if dfs(m) {
					return true
				}
			case gray:
				found = append(found, path[onPath[m]:]...)
				found = append(found, m)
				return true
			}
		}
		path = path[:len(path)-1]
		delete(onPath, n)
		color[n] = black
		return false
	}
	// Iterate in file order for deterministic output.
	for _, e := range entries {
		if e.ID != "" && color[e.ID] == white {
			if dfs(e.ID) {
				return found
			}
		}
	}
	return nil
}
