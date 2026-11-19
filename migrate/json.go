package migrate

import "encoding/json"

// The result values are part of the wire contract a consumer serves: they
// marshal with camelCase keys and never a null collection, matching
// tracker.Issue's schema. A nil slice becomes [] and a nil map {}, so a server
// can return a result straight to its clients.

// MarshalJSON renders the run's collections as empty arrays and objects rather
// than null, so a consumer never has to distinguish an absent collection from
// an empty one.
func (r Result) MarshalJSON() ([]byte, error) {
	type plain Result
	out := plain(r)
	out.Warnings = emptyNotNull(out.Warnings)
	out.Mapping = emptyNotNullMap(out.Mapping)
	out.Plan = emptyNotNull(out.Plan)
	out.WouldCreate = emptyNotNull(out.WouldCreate)
	out.AlreadyCreated = emptyNotNullMap(out.AlreadyCreated)
	return json.Marshal(out)
}

// MarshalJSON renders the planned blockers as an array even when empty.
func (s PlanStep) MarshalJSON() ([]byte, error) {
	type plain PlanStep
	out := plain(s)
	out.BlockedBy = emptyNotNull(out.BlockedBy)
	return json.Marshal(out)
}

// emptyNotNull keeps a nil slice from marshaling as null.
func emptyNotNull[T any](values []T) []T {
	if values == nil {
		return []T{}
	}
	return values
}

// emptyNotNullMap is emptyNotNull for maps: a nil result map is {} on the wire.
func emptyNotNullMap[K comparable, V any](m map[K]V) map[K]V {
	if m == nil {
		return map[K]V{}
	}
	return m
}
