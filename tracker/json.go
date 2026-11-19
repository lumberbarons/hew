package tracker

import "encoding/json"

// The result values are part of the wire contract a consumer serves: they
// marshal with the same camelCase keys tracker.Issue carries, and a collection
// is never null. A nil slice becomes [] and a nil map becomes {}, so a server
// can hand a result straight to its clients instead of re-mapping every type
// into a shape of its own.
//
// The MarshalJSON methods below restate only the collections. Everything else
// — field names, nesting, the types of the values — is the struct's own json
// tags, so a new field is tagged once and carried everywhere.

// MarshalJSON renders the edges and findings as arrays even when empty.
func (r CreateResult) MarshalJSON() ([]byte, error) {
	type plain CreateResult
	out := plain(r)
	out.Edges = emptyNotNull(out.Edges)
	out.Findings = emptyNotNull(out.Findings)
	return json.Marshal(out)
}

// MarshalJSON renders the applied field names as an array even when empty.
func (r EditResult) MarshalJSON() ([]byte, error) {
	type plain EditResult
	out := plain(r)
	out.Applied = emptyNotNull(out.Applied)
	return json.Marshal(out)
}

// MarshalJSON renders the released assignees as an array even when empty.
func (r ReopenResult) MarshalJSON() ([]byte, error) {
	type plain ReopenResult
	out := plain(r)
	out.ReleasedAssignees = emptyNotNull(out.ReleasedAssignees)
	return json.Marshal(out)
}

// MarshalJSON renders both blocker sets as arrays even when empty.
func (r BlockResult) MarshalJSON() ([]byte, error) {
	type plain BlockResult
	out := plain(r)
	out.Added = emptyNotNull(out.Added)
	out.Existing = emptyNotNull(out.Existing)
	return json.Marshal(out)
}

// MarshalJSON renders both blocker sets as arrays even when empty.
func (r UnblockResult) MarshalJSON() ([]byte, error) {
	type plain UnblockResult
	out := plain(r)
	out.Removed = emptyNotNull(out.Removed)
	out.Missing = emptyNotNull(out.Missing)
	return json.Marshal(out)
}

// MarshalJSON renders the created labels and notices as arrays even when empty.
func (r InitResult) MarshalJSON() ([]byte, error) {
	type plain InitResult
	out := plain(r)
	out.CreatedLabels = emptyNotNull(out.CreatedLabels)
	out.Notices = emptyNotNull(out.Notices)
	return json.Marshal(out)
}

// MarshalJSON renders the run's collections as empty arrays and objects rather
// than null, so a consumer never has to distinguish an absent collection from
// an empty one.
func (r ApplyResult) MarshalJSON() ([]byte, error) {
	type plain ApplyResult
	out := plain(r)
	out.Warnings = emptyNotNull(out.Warnings)
	out.Mapping = emptyNotNullMap(out.Mapping)
	out.Applied = emptyNotNull(out.Applied)
	out.WouldCreate = emptyNotNull(out.WouldCreate)
	out.AlreadyCreated = emptyNotNullMap(out.AlreadyCreated)
	return json.Marshal(out)
}

// MarshalJSON renders the planned entry's areas and blockers as arrays even
// when empty; the dry-run plan is the one result the CLI emits verbatim.
func (p PlannedEntry) MarshalJSON() ([]byte, error) {
	type plain PlannedEntry
	out := plain(p)
	out.Areas = emptyNotNull(out.Areas)
	out.BlockedBy = emptyNotNull(out.BlockedBy)
	return json.Marshal(out)
}

// MarshalJSON renders an epic's children, findings and warnings as arrays even
// when empty; next stays null for a non-epic or a dry sequence.
func (d IssueDetail) MarshalJSON() ([]byte, error) {
	type plain IssueDetail
	out := plain(d)
	out.Children = emptyNotNull(out.Children)
	out.TextFindings = emptyNotNull(out.TextFindings)
	out.Warnings = emptyNotNull(out.Warnings)
	return json.Marshal(out)
}

// MarshalJSON renders the matches as an array even when empty.
func (r SearchResult) MarshalJSON() ([]byte, error) {
	type plain SearchResult
	out := plain(r)
	out.Issues = emptyNotNull(out.Issues)
	return json.Marshal(out)
}

// MarshalJSON renders the matches and their findings as arrays even when empty.
func (r TriageResult) MarshalJSON() ([]byte, error) {
	type plain TriageResult
	out := plain(r)
	out.Issues = emptyNotNull(out.Issues)
	out.TextFindings = emptyNotNull(out.TextFindings)
	return json.Marshal(out)
}

// MarshalJSON renders every section and the warnings as arrays even when empty.
func (p PrimerResult) MarshalJSON() ([]byte, error) {
	type plain PrimerResult
	out := plain(p)
	out.Ready = emptyNotNull(out.Ready)
	out.InProgress = emptyNotNull(out.InProgress)
	out.Epics = emptyNotNull(out.Epics)
	out.Warnings = emptyNotNull(out.Warnings)
	return json.Marshal(out)
}

// MarshalJSON renders the findings as an array even when empty.
func (b ComposedBody) MarshalJSON() ([]byte, error) {
	type plain ComposedBody
	out := plain(b)
	out.Findings = emptyNotNull(out.Findings)
	return json.Marshal(out)
}

// MarshalJSON renders the findings as an array even when empty.
func (b BodyCheck) MarshalJSON() ([]byte, error) {
	type plain BodyCheck
	out := plain(b)
	out.Findings = emptyNotNull(out.Findings)
	return json.Marshal(out)
}

// MarshalJSON renders the cycle as an array even when the warning is not one.
func (w Warning) MarshalJSON() ([]byte, error) {
	type plain Warning
	out := plain(w)
	out.Cycle = emptyNotNull(out.Cycle)
	return json.Marshal(out)
}

// MarshalJSON renders the offending tokens as an array even when empty.
func (f Finding) MarshalJSON() ([]byte, error) {
	type plain Finding
	out := plain(f)
	out.Tokens = emptyNotNull(out.Tokens)
	return json.Marshal(out)
}

// emptyNotNullMap is emptyNotNull for maps: a nil result map is {} on the wire.
func emptyNotNullMap[K comparable, V any](m map[K]V) map[K]V {
	if m == nil {
		return map[K]V{}
	}
	return m
}
