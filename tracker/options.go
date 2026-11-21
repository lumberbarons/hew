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
	// States selects the issue states to fetch. Empty means open only —
	// unlike SearchRequest.States, where empty means open and closed.
	States []State
	// Bodies carries each issue's body and comments on the snapshot's
	// list-shaped reads — Issues, Ready, List, Blocked, Claimed, Epics and
	// Children. The zero value omits them, matching `hew list` without
	// --bodies; Snapshot.Get is a single-issue read and always carries them.
	Bodies bool
}

// StartOptions configures a claim. The zero value preserves the existing
// priority and refuses an existing claim or open blockers.
type StartOptions struct {
	// Priority optionally supplies P0..P4; required for an untriaged target.
	Priority string
	// Force replaces an existing claim and permits open blockers.
	Force bool
	// ForDiscovery declares that the caller is claiming an idea to investigate
	// it. An idea is refused without it, forced or not: capture is not
	// implementation work.
	ForDiscovery bool
}

// ReleaseOptions configures claim release. The zero value requires the
// issue to be claimed solely by the acting identity.
type ReleaseOptions struct {
	Force bool
}

// CloseOptions configures issue closure. Reason is required, except with
// DuplicateOf, which supplies one; the zero values of Completed and
// DuplicateOf select a not-planned closure.
type CloseOptions struct {
	Reason      string
	Completed   bool
	DuplicateOf int
	// Force closes an epic that still has open sub-issues.
	Force bool
}

// BlockOptions configures Block and Unblock. The zero value refuses a
// closed target; AllowClosed permits editing it. Cycle checks still apply.
type BlockOptions struct {
	AllowClosed bool
}

// EditRequest names the changes to make to one issue. Every field is optional:
// a nil pointer, an empty slice or a false flag leaves that part of the issue
// alone. The pointer fields carry presence rather than value, so an explicitly
// empty title, body, outcome, priority or type is refused instead of being read as an
// omitted field.
type EditRequest struct {
	// Issue is the issue to edit.
	Issue int
	// Priority is P0..P4; the old priority label is swapped, never stacked.
	Priority *string
	// Type is bug|enhancement|task|idea; the old type label is swapped, never
	// stacked.
	Type *string
	// Title replaces the issue's title.
	Title *string
	// Body replaces the whole issue body. An empty body is refused: an edit
	// does not blank an issue.
	Body *string
	// Outcome appends a Markdown note to an idea's Outcome section. It
	// cannot be combined with Body.
	Outcome *string
	// AddAreas and RemoveAreas change the issue's area labels. Added names
	// are trimmed and de-duplicated; neither list may name a convention
	// label.
	AddAreas    []string
	RemoveAreas []string
	// Parent moves the issue under that parent, replacing an existing one.
	// NoParent removes the current parent. The two are mutually exclusive.
	Parent   *int
	NoParent bool
	// AllowClosed permits editing an issue that is already closed.
	AllowClosed bool
}

// ReopenOptions configures Reopen. The reason is required: it is the comment
// that records why closed work is coming back.
type ReopenOptions struct {
	Reason string
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
