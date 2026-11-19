package pr

import "encoding/json"

// The result values are part of the wire contract a consumer serves: they
// marshal with camelCase keys and never a null collection, matching
// tracker.Issue's schema. A nil slice becomes [], so a server can return a
// result straight to its clients.

// MarshalJSON renders the fixed issues, warnings and findings as arrays even
// when empty.
func (r CreateResult) MarshalJSON() ([]byte, error) {
	type plain CreateResult
	out := plain(r)
	out.FixedIssues = emptyNotNull(out.FixedIssues)
	out.Warnings = emptyNotNull(out.Warnings)
	out.Findings = emptyNotNull(out.Findings)
	return json.Marshal(out)
}

// MarshalJSON renders every set a warning can carry as an array even when the
// warning's kind does not use it.
func (w Warning) MarshalJSON() ([]byte, error) {
	type plain Warning
	out := plain(w)
	out.Issues = emptyNotNull(out.Issues)
	out.Assignees = emptyNotNull(out.Assignees)
	out.Groups = emptyNotNull(out.Groups)
	return json.Marshal(out)
}

// emptyNotNull keeps a nil slice from marshaling as null.
func emptyNotNull[T any](values []T) []T {
	if values == nil {
		return []T{}
	}
	return values
}
