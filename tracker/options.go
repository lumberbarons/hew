package tracker

import "time"

// State names an issue state a Snapshot can fetch.
type State string

const (
	// StateOpen is open issues.
	StateOpen State = "open"
	// StateClosed is closed issues.
	StateClosed State = "closed"
)

// SnapshotOptions selects which issues to fetch. The zero value fetches open
// issues only. An unknown state is an invalid-input error.
type SnapshotOptions struct {
	// States selects the issue states to fetch. Empty means open only.
	States []State
}

// StartOptions configures a claim. The zero value preserves the existing
// priority and refuses an existing claim or open blockers.
type StartOptions struct {
	// Priority optionally supplies P0..P4; required for an untriaged target.
	Priority string
	// Force replaces an existing claim and permits open blockers.
	Force bool
}

// ReleaseOptions configures claim release. The zero value requires the
// issue to be claimed solely by the acting identity.
type ReleaseOptions struct {
	Force bool
}

// CloseOptions configures issue closure. Reason is required; the zero
// values of Completed and DuplicateOf select a not-planned closure.
type CloseOptions struct {
	Reason      string
	Completed   bool
	DuplicateOf int
}

// BlockOptions configures Block and Unblock. The zero value refuses a
// closed target; AllowClosed permits editing it. Cycle checks still apply.
type BlockOptions struct {
	AllowClosed bool
}

// ApplyOptions configures plan application. The zero value applies the
// plan without a delay between writes and with an in-memory checkpoint, so
// nothing resumes across calls; DryRun validates without writing.
type ApplyOptions struct {
	DryRun   bool
	Throttle time.Duration
	// Checkpoint is where progress is recorded and resumed from. Nil means a
	// fresh in-memory checkpoint.
	Checkpoint Checkpoint
	// Digest identifies the plan source in provenance markers and checkpoint
	// records. Empty means the checkpoint's own digest, or a digest computed
	// from the entries when it has none; a caller that parses a file passes
	// that file's digest so a checkpoint on disk agrees with the markers its
	// issues carry.
	Digest string
}
