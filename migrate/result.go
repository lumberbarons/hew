package migrate

import "fmt"

// Result is what the passes did, or — under DryRun — what they would do.
//
// Failed counts the steps that failed and would be tried again by a rerun, so
// a caller can tell a migration that is whole from one that needs another run.
// Warnings about things a rerun cannot change (an edge dropped for a cycle, a
// body a person edited) are listed there but are not failures.
type Result struct {
	// DryRun reports which mode ran.
	DryRun bool
	// Created, Wired, Closed and Relinked count the run's writes.
	Created  int
	Wired    int
	Closed   int
	Relinked int
	// Failed is the number of steps a rerun will attempt again.
	Failed int
	// Warnings names the edges that did not land, the steps that failed, the
	// bodies a person edited first, and anything else a rerun cannot change.
	Warnings []Warning
	// Mapping is every key → issue number after the run.
	Mapping map[string]int
	// WouldCreate and AlreadyCreated are a dry run's plan and the items its
	// checkpoint already accounts for. They are empty otherwise.
	WouldCreate    []PlannedItem
	AlreadyCreated map[string]int
}

// PlannedItem is one issue a dry run would create, in the source's order.
type PlannedItem struct {
	Key   string
	Title string
}

// note records a warning a rerun cannot change.
func (r *Result) note(kind WarningKind, key string, issue int, format string, args ...any) {
	r.Warnings = append(r.Warnings, Warning{
		Kind:    kind,
		Key:     key,
		Issue:   issue,
		Message: fmt.Sprintf(format, args...),
	})
}

// fail records a warning for a step a rerun will try again.
func (r *Result) fail(kind WarningKind, key string, issue int, format string, args ...any) {
	r.note(kind, key, issue, format, args...)
	r.Failed++
}
