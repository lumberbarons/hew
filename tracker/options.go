package tracker

import "time"

// SnapshotOptions selects which issues to fetch. The zero value fetches
// open issues only.
type SnapshotOptions struct {
	IncludeClosed bool
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
// plan without a delay between writes; DryRun validates without writing.
type ApplyOptions struct {
	DryRun   bool
	Throttle time.Duration
}
