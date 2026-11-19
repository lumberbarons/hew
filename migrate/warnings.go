package migrate

// WarningKind identifies one advisory notice about a migration, so a caller can
// filter by cause and render its own sentence. A warning never aborts the run:
// a failed edge, comment or close is retried by a rerun; a cycle-dropped edge
// or a body a person edited first never is.
type WarningKind string

const (
	// WarnEdgeDropped reports a parent or blocked-by edge that was not wired: a
	// referenced item was never migrated, or the edge would have closed a
	// dependency cycle.
	WarnEdgeDropped WarningKind = "edge-dropped"
	// WarnWriteFailed reports a write that failed. The step is counted in
	// Result.Failed: a rerun retries it.
	WarnWriteFailed WarningKind = "write-failed"
	// WarnBodyEdited reports a body or title that changed after the migration
	// created it, so its references were left as written rather than
	// overwriting a person's edit.
	WarnBodyEdited WarningKind = "body-edited"
	// WarnAlreadyClosed reports work an item asked for on an issue that is
	// already closed.
	WarnAlreadyClosed WarningKind = "already-closed"
	// WarnLabelRenamed reports a source label whose name had to change to be
	// usable: a comma would read as two areas, so it becomes a hyphen.
	WarnLabelRenamed WarningKind = "label-renamed"
	// WarnUnknownStatus reports a source record whose status the mapping does
	// not know, which the run migrates as open.
	WarnUnknownStatus WarningKind = "unknown-status"
)

// Warning is one advisory notice. The fields a kind uses are documented on that
// kind; the rest stay zero.
type Warning struct {
	Kind WarningKind `json:"kind"`
	// Key is the source item the warning is about.
	Key string `json:"key"`
	// Issue is the target issue number, when one was created or read.
	Issue int `json:"issue"`
	// Message is the complete sentence a caller renders. It is what the CLI
	// prints and what --json reports.
	Message string `json:"message"`
}

// String returns the warning's sentence, so a caller can treat the value as
// the message it renders.
func (w Warning) String() string { return w.Message }
