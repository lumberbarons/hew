package tracker

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/lumberbarons/hew/internal/conventions"
	"github.com/lumberbarons/hew/internal/model"
)

// The write path's field limits: GitHub's title limit and its 50-character
// label limit, both counted in characters rather than bytes.
const (
	maxTitleRunes = 256
	maxAreaRunes  = 50
)

// PlanTypeEpic is the plan-only type marking a parent issue: no type label,
// the epic label, the cosmetic Epic title prefix, and (usually) other entries
// pointing at it.
const PlanTypeEpic = "epic"

// PlanRef points at another issue: a plan entry by local id, or an existing
// issue by number. Exactly one field is set.
type PlanRef struct {
	ID     string
	Number int
}

func (r PlanRef) String() string {
	if r.ID != "" {
		return r.ID
	}
	return fmt.Sprintf("#%d", r.Number)
}

// Sections is the structured body of a plan entry. ApplyPlan composes it into
// the issue template; callers that already have a body set PlanEntry.Body
// instead, never both.
type Sections struct {
	Where    string
	Problem  string
	Goal     string
	Fix      string
	Approach string
	// OpenQuestions is the optional discovery section. The json tag omits an
	// unset list so adding the field does not change the derived digest of a
	// plan whose provenance markers were written before it existed.
	OpenQuestions []string `json:"OpenQuestions,omitempty"`
	DoneWhen      []string
}

// IsZero reports whether no section was provided.
func (s Sections) IsZero() bool { return conventions.Sections(s).IsZero() }

// Compose renders the sections in template order, provided sections only,
// with Done when as a checklist — the same body `hew create` writes.
func (s Sections) Compose() string { return conventions.Sections(s).Compose() }

// PlanEntry is one issue ApplyPlan creates: the typed counterpart of a JSONL
// plan line, for callers that build plans in code. A zero Priority means P2.
type PlanEntry struct {
	// ID names the entry so other entries can reference it before issue
	// numbers exist. Empty means the entry is keyed by its Line.
	ID       string
	Title    string
	Type     string // bug|enhancement|task|idea, or PlanTypeEpic
	Priority string // P0..P4; empty means P2
	Areas    []string
	// Body is the issue body. It is mutually exclusive with Sections.
	Body      string
	Sections  Sections
	Parent    *PlanRef
	BlockedBy []PlanRef
	// DiscoveredFrom links the created issue to the issue that surfaced it.
	DiscoveredFrom int
	// Line is the entry's 1-based source line when the plan came from a file.
	// ApplyPlan uses the entry's position when it is zero, so id-less entries
	// built in code still get distinct keys.
	Line int
}

// Key identifies the entry in checkpoint state and progress output: its id,
// or its line for id-less entries.
func (e PlanEntry) Key() string {
	if e.ID != "" {
		return e.ID
	}
	return fmt.Sprintf("line:%d", e.Line)
}

// IsEpic reports whether the entry declares a parent issue.
func (e PlanEntry) IsEpic() bool { return e.Type == PlanTypeEpic }

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
	To   PlanRef
}

// Edges returns the entry's dependency edges in wiring order — parent first,
// then blockers. Both are the same operation apart from the API call they
// make, so stating them as one list lets the wiring step treat them alike:
// resolve, skip if already wired, warn on failure.
func (e PlanEntry) Edges() []Edge {
	var out []Edge
	if e.Parent != nil {
		out = append(out, Edge{Kind: ParentEdge, To: *e.Parent})
	}
	for _, b := range e.BlockedBy {
		out = append(out, Edge{Kind: BlockedByEdge, To: b})
	}
	return out
}

// ExistingRef is a numeric reference in a plan entry: an issue that must
// already exist, which validation cannot verify.
type ExistingRef struct {
	// Line is the 1-based line of the entry carrying the reference.
	Line int
	// Field is the plan field it came from: parent, blocked-by or
	// discovered-from.
	Field  string
	Number int
}

// ExistingRefs returns the entry's references to issues outside the plan, in
// field order: parent, blockers, then discovered-from. Local ids are left out
// — validation already proved they match an entry — so what remains is
// exactly what ApplyPlan has to look up before writing anything, since a
// number that names no issue is only discovered by the store.
func (e PlanEntry) ExistingRefs() []ExistingRef {
	var out []ExistingRef
	if e.Parent != nil && e.Parent.ID == "" {
		out = append(out, ExistingRef{Line: e.Line, Field: "parent", Number: e.Parent.Number})
	}
	for _, b := range e.BlockedBy {
		if b.ID == "" {
			out = append(out, ExistingRef{Line: e.Line, Field: "blocked-by", Number: b.Number})
		}
	}
	if e.DiscoveredFrom > 0 {
		out = append(out, ExistingRef{Line: e.Line, Field: "discovered-from", Number: e.DiscoveredFrom})
	}
	return out
}

// ValidatePlan checks typed plan entries the way Parse checks a JSONL file:
// every entry's own fields, then local-id resolution, dangling references and
// dependency or parent cycles. References may point forward — creation
// happens before wiring — but every local reference must resolve. Errors name
// the source line when the entry carries one, its position otherwise.
//
// One area rule is not checked here: a plan built before `idea` was a type may
// carry that label as an area on an entry a checkpoint already created, and a
// resume has to accept it. ApplyPlan enforces the idea reservation on every
// pending entry — which is every entry of a fresh plan — before its first
// write (see validatePendingPlanFields).
//
// A rejection is an *InvalidInputError whose Reason names the entry and the
// rule, so a caller branches on the type rather than the text.
func ValidatePlan(entries []PlanEntry) error {
	for i, e := range entries {
		if err := validatePlanEntry(e, i); err != nil {
			return &InvalidInputError{Field: "plan", Reason: err.Error()}
		}
	}
	if err := validatePlanRefs(entries); err != nil {
		return &InvalidInputError{Field: "plan", Reason: err.Error()}
	}
	return nil
}

// ValidateTitle enforces the write path's title rules — non-blank, a single
// line, at most 256 characters counted in runes, including any cosmetic Epic
// prefix. Every writer shares it, so a library caller cannot store a title the
// CLI would refuse. A rejection is an *InvalidInputError whose Field is
// "title".
func ValidateTitle(title string) error {
	if strings.TrimSpace(title) == "" {
		return &InvalidInputError{Field: "title", Reason: "must not be empty"}
	}
	if strings.ContainsAny(title, "\r\n\v\f\u0085\u2028\u2029") {
		return &InvalidInputError{Field: "title", Reason: "must be a single line"}
	}
	if utf8.RuneCountInString(title) > maxTitleRunes {
		return &InvalidInputError{Field: "title", Reason: fmt.Sprintf("must be at most %d characters (including the Epic: prefix for epics)", maxTitleRunes)}
	}
	return nil
}

// NormalizeAreas returns area names trimmed of surrounding whitespace and
// de-duplicated, or an *InvalidInputError naming the first rule broken. A name
// blank after trimming is refused — GitHub rejects a label with no visible
// name, and a local store would keep one that renders as `( )` and breaks a
// later `migrate local`. A name is also refused when it is longer than 50
// characters, contains a comma, or collides with a priority, type, in-progress
// or epic label: the text renderer joins areas with commas, and convention
// labels would change priority, type or claim.
//
// The CLI's area flags split on commas before calling this, so the comma rule
// guards the callers that hand over a name verbatim — `apply` plans and direct
// library use — while `--area x,y` stays two areas.
func NormalizeAreas(areas []string) ([]string, error) {
	trimmed := make([]string, 0, len(areas))
	for _, area := range areas {
		area = strings.TrimSpace(area)
		if area == "" {
			return nil, &InvalidInputError{Field: "areas", Reason: "must not be blank"}
		}
		// Trimming can make two spellings one name; keep the first.
		if !slices.Contains(trimmed, area) {
			trimmed = append(trimmed, area)
		}
	}
	for _, area := range trimmed {
		if utf8.RuneCountInString(area) > maxAreaRunes {
			return nil, &InvalidInputError{Field: "areas", Reason: fmt.Sprintf("must be at most %d characters", maxAreaRunes)}
		}
		if strings.Contains(area, ",") {
			return nil, &InvalidInputError{Field: "areas", Reason: fmt.Sprintf("%q must not contain a comma; commas separate areas", area)}
		}
		if err := ValidateAreaLabel(area); err != nil {
			return nil, err
		}
	}
	return trimmed, nil
}

// ValidateAreaLabel refuses an area that names a convention label, which would
// otherwise change priority or type, or manufacture a claim. It checks the
// name only, without NormalizeAreas's trim, length or comma rules, so a
// removal can still name an oversized area an older tracker stored.
func ValidateAreaLabel(area string) error {
	if _, ok := model.LabelPriority(area); ok {
		return &InvalidInputError{Field: "areas", Reason: fmt.Sprintf("%q is a priority label; use --priority", area)}
	}
	if _, ok := model.LabelType(area); ok {
		return &InvalidInputError{Field: "areas", Reason: fmt.Sprintf("%q is a type label; use --type", area)}
	}
	if model.IsInProgressLabel(area) {
		return &InvalidInputError{Field: "areas", Reason: fmt.Sprintf("%q is a reserved convention label; use hew start to claim an issue", area)}
	}
	if model.IsEpicLabel(area) {
		return &InvalidInputError{Field: "areas", Reason: fmt.Sprintf("%q is a reserved convention label; use hew epic create to make an epic", area)}
	}
	return nil
}

// validatePendingPlanFields applies the write path's title and area rules to
// the entries a checkpoint has not created yet. Already-created entries are
// skipped: their source was checked when they were created, and re-gating it
// would strand a checkpoint whose source digest cannot change to repair it.
// The returned entries carry the normalized areas the create pass uses.
//
// This is where the idea reservation is enforced. ValidatePlan cannot check it
// — it runs before the checkpoint — so an area an earlier build stored as
// `idea` survives only on an entry the checkpoint already created; every
// pending entry goes through NormalizeAreas, and a fresh plan has no created
// entries at all.
func validatePendingPlanFields(entries []PlanEntry, checkpoint Checkpoint) ([]PlanEntry, error) {
	for i, e := range entries {
		if _, created := checkpoint.Created(e.Key()); created {
			continue
		}
		if err := ValidateTitle(PlanTitle(e)); err != nil {
			return nil, planFieldError(e, i, err)
		}
		areas, err := NormalizeAreas(e.Areas)
		if err != nil {
			return nil, planFieldError(e, i, err)
		}
		entries[i].Areas = areas
	}
	return entries, nil
}

// planFieldError names the entry before the rule, so a caller fixing a plan
// reads the line rather than a bare field name.
func planFieldError(e PlanEntry, index int, err error) error {
	reason := err.Error()
	var invalid *InvalidInputError
	if errors.As(err, &invalid) {
		reason = invalid.Field + " " + invalid.Reason
	}
	return &InvalidInputError{Field: "plan", Reason: entryError(e, index, "%s", reason).Error()}
}

// isLegacyConventionLabel reports a convention label that predates the idea
// type. The idea label is reserved like the others, but only ApplyPlan can
// apply that rule: ValidatePlan has no checkpoint and so cannot tell a pending
// entry from one an earlier release already created with `idea` as an area.
func isLegacyConventionLabel(name string) bool {
	return model.IsConventionLabel(name) && !strings.EqualFold(name, model.IdeaType)
}

func validatePlanEntry(e PlanEntry, index int) error {
	if e.Title == "" {
		return entryError(e, index, "title is required")
	}
	if e.Type != PlanTypeEpic && !model.IsType(e.Type) {
		return entryError(e, index, "type must be one of %s|%s (got %q)", strings.Join(model.Types, "|"), PlanTypeEpic, e.Type)
	}
	if e.Priority != "" {
		if _, ok := model.ParsePriority(e.Priority); !ok {
			return entryError(e, index, "priority must be P0..P4 (got %q)", e.Priority)
		}
	}
	for _, area := range e.Areas {
		if isLegacyConventionLabel(area) {
			return entryError(e, index, "area %q collides with a convention label", area)
		}
	}
	if e.DiscoveredFrom < 0 {
		return entryError(e, index, "discovered-from must be an issue number (got %d)", e.DiscoveredFrom)
	}
	// The section fields follow the create flags' rules: wording pairs pick
	// one, and structured sections don't mix with a raw body. The rules live
	// in the same composition a direct caller uses, so a plan cannot carry a
	// body ComposeBody would refuse.
	if err := validateBody(Body{Sections: e.Sections, Text: e.Body}); err != nil {
		var invalid *InvalidInputError
		if errors.As(err, &invalid) {
			return entryError(e, index, "%s", invalid.Reason)
		}
		return err
	}
	return nil
}

func validatePlanRefs(entries []PlanEntry) error {
	positions := map[string]string{} // id → where it was first declared
	for i, e := range entries {
		if e.ID == "" {
			continue
		}
		if strings.HasPrefix(e.ID, "line:") {
			return entryError(e, i, "id %q is reserved (line:N identifies id-less entries)", e.ID)
		}
		if prev, ok := positions[e.ID]; ok {
			return entryError(e, i, "duplicate id %q (first used on %s)", e.ID, prev)
		}
		positions[e.ID] = entryPosition(e, i)
	}
	for i, e := range entries {
		if e.Parent != nil {
			if err := checkPlanRef(*e.Parent, e, i, positions, "parent"); err != nil {
				return err
			}
		}
		seen := map[string]bool{}
		for _, b := range e.BlockedBy {
			if err := checkPlanRef(b, e, i, positions, "blocked-by"); err != nil {
				return err
			}
			if seen[b.String()] {
				return entryError(e, i, "duplicate blocked-by %s", b)
			}
			seen[b.String()] = true
		}
	}
	if cyc := findPlanCycle(entries, blockedByEdges); cyc != nil {
		return fmt.Errorf("dependency cycle in plan: %s", strings.Join(cyc, " → "))
	}
	if cyc := findPlanCycle(entries, parentEdges); cyc != nil {
		return fmt.Errorf("parent cycle in plan: %s", strings.Join(cyc, " → "))
	}
	return nil
}

// checkPlanRef validates a single reference. Numeric refs pass — they point
// at existing issues only the store can vouch for (see PlanEntry.ExistingRefs).
func checkPlanRef(r PlanRef, e PlanEntry, index int, positions map[string]string, kind string) error {
	if r.ID == "" {
		return nil
	}
	if r.ID == e.ID {
		return entryError(e, index, "entry cannot be its own %s", kind)
	}
	if _, ok := positions[r.ID]; !ok {
		return entryError(e, index, "%s %q does not match any entry id", kind, r.ID)
	}
	return nil
}

// entryPosition names where an entry came from: its source line when it was
// parsed from a file, its 1-based position otherwise.
func entryPosition(e PlanEntry, index int) string {
	if e.Line > 0 {
		return fmt.Sprintf("line %d", e.Line)
	}
	return fmt.Sprintf("entry %d", index+1)
}

func entryError(e PlanEntry, index int, format string, args ...any) error {
	return fmt.Errorf("%s: %s", entryPosition(e, index), fmt.Sprintf(format, args...))
}

func blockedByEdges(e PlanEntry) []string {
	var out []string
	for _, b := range e.BlockedBy {
		if b.ID != "" {
			out = append(out, b.ID)
		}
	}
	return out
}

func parentEdges(e PlanEntry) []string {
	if e.Parent != nil && e.Parent.ID != "" {
		return []string{e.Parent.ID}
	}
	return nil
}

// findPlanCycle looks for a cycle among plan-local edges, returned in edge
// order with the closing member repeated (a → b → a), or nil. Checking only
// the plan's own edges is a complete check: an issue that exists before the
// plan runs cannot reference an entry that doesn't exist yet, so no cycle can
// pass through an existing issue. The check matters because GitHub silently
// accepts dependency cycles longer than two issues (see DESIGN.md).
func findPlanCycle(entries []PlanEntry, edges func(PlanEntry) []string) []string {
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

// PlanTitle is the title ApplyPlan creates for an entry: epic entries carry
// the cosmetic prefix exactly once.
func PlanTitle(e PlanEntry) string {
	if e.IsEpic() && !strings.HasPrefix(e.Title, conventions.EpicTitlePrefix) {
		return conventions.EpicTitlePrefix + e.Title
	}
	return e.Title
}

// PlanBody is the body ApplyPlan creates for an entry before the provenance
// marker: the entry's body, or its composed sections, plus the
// discovered-from link when it has one. It goes through the same composition a
// direct caller uses, so a plan and the create command cannot drift apart.
func PlanBody(e PlanEntry) string {
	body, err := composeBodyText(Body{Sections: e.Sections, Text: e.Body, DiscoveredFrom: e.DiscoveredFrom})
	if err != nil {
		// ValidatePlan refuses a conflicting entry before any write; a body
		// asked for anyway keeps the caller's own text.
		return e.Body
	}
	return body
}

// PlanBodyWithProvenance is the body actually written: PlanBody plus the
// marker binding the issue to this entry and this plan digest, which is what
// lets a later run tell an issue it created from one a checkpoint merely
// points at.
func PlanBodyWithProvenance(e PlanEntry, digest string) string {
	marker := conventions.ProvenanceMarker(conventions.ProvenanceApply, e.Key(), digest)
	body := PlanBody(e)
	if body == "" {
		return marker
	}
	return strings.TrimRight(body, "\n") + "\n\n" + marker
}
