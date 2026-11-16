package migrate

import (
	"fmt"

	"github.com/lumberbarons/hew/tracker"
)

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
	// Selected is how many source records the run selected to migrate.
	// A run that selects none reports it here, so a caller can render an empty
	// plan without confusing it with a fully resumed run, whose Selected is
	// non-zero and Created is zero.
	Selected int
	// Skipped counts source records the run's selection left out: the closed
	// beads a run without IncludeClosed does not migrate.
	Skipped int
	// Backend is the tracker's backend after the run, when a workflow changed
	// it: the local-to-GitHub migration seals and flips to BackendGitHub.
	// Empty means the workflow did not change it.
	Backend tracker.Backend
	// Warnings names the edges that did not land, the steps that failed, the
	// bodies a person edited first, the source labels a migration renamed, and
	// anything else a rerun cannot change.
	Warnings []Warning
	// Mapping is every key → issue number after the run.
	Mapping map[string]int
	// Plan is a dry run's steps in source order: the items the checkpoint
	// already accounts for and the ones a real run would create, interleaved as
	// the source lists them. It is empty otherwise.
	Plan []PlanStep
	// WouldCreate and AlreadyCreated are Plan split by kind: the items a real
	// run would create, in source order, and the mapping its checkpoint already
	// accounts for. They are empty otherwise.
	WouldCreate    []PlannedItem
	AlreadyCreated map[string]int
}

// PlanStep is one line of a dry run's plan, in source order.
type PlanStep struct {
	// Key identifies the source record the step is about.
	Key string
	// Migrated is the issue number the checkpoint records for Key; zero means
	// the step is a planned create.
	Migrated int
	// Title, Priority, Type, Parent, BlockedBy, InProgress and ThenClose
	// describe the issue a planned create would file, derived from the item's
	// labels and edges. They are zero for a step the checkpoint already
	// accounts for.
	Title      string
	Priority   string
	Type       string
	Parent     string
	BlockedBy  []string
	InProgress bool
	ThenClose  bool
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
