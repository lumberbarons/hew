package migrate

import (
	"fmt"
	"path/filepath"
)

// DuplicateKeyError reports two items sharing a checkpoint key. Keys identify
// an item in the checkpoint and in its provenance marker, so the run refuses
// before writing anything rather than letting the second item overwrite the
// first's mapping.
type DuplicateKeyError struct {
	Key string
	// ItemNoun names the source's records ("bead"); empty means "item".
	ItemNoun string
}

func (e *DuplicateKeyError) Error() string {
	noun := e.ItemNoun
	if noun == "" {
		noun = "item"
	}
	return fmt.Sprintf("%s %s appears more than once; keys identify items in the checkpoint and must be unique", noun, e.Key)
}

// ItemError reports a source item the engine cannot migrate because one of its
// own fields is invalid.
type ItemError struct {
	Key    string
	Field  string
	Reason string
}

func (e *ItemError) Error() string {
	if e.Key == "" {
		return fmt.Sprintf("migration item has an invalid %s: %s", e.Field, e.Reason)
	}
	return fmt.Sprintf("migration item %q has an invalid %s: %s", e.Key, e.Field, e.Reason)
}

// NotLocalError reports a tracker that cannot be migrated because it is not the
// local tracker in dir: it is missing, belongs to another backend, or was
// already moved.
type NotLocalError struct {
	Dir     string
	Backend string
	// Repo is where an earlier migration put the issues, when its record is
	// readable. Empty otherwise.
	Repo string
}

func (e *NotLocalError) Error() string {
	if e.Backend == "" {
		return fmt.Sprintf("no local tracker at %s (hew init --local creates one)", e.Dir)
	}
	if e.Repo != "" {
		return fmt.Sprintf("%s tracks issues in %q, not locally: it was already migrated to %s (see %s)",
			e.Dir, e.Backend, e.Repo, filepath.Join(e.Dir, localMigratedFile))
	}
	return fmt.Sprintf("%s tracks issues in %q, not locally", e.Dir, e.Backend)
}

// CheckpointError reports a resume-state file the workflow could not use
// without a target to verify it against.
type CheckpointError struct {
	Path   string
	Reason string
}

func (e *CheckpointError) Error() string {
	return fmt.Sprintf("%s: %s", e.Path, e.Reason)
}

// SealError reports a local migration that finished its writes but could not
// switch the tracker over. Everything is on GitHub; the tracker is still local
// so the rerun that completes the move is possible.
type SealError struct{ Err error }

func (e *SealError) Error() string {
	return fmt.Sprintf("everything is on GitHub but the tracker could not be switched over (rerun `hew migrate local` to retry): %v", e.Err)
}

func (e *SealError) Unwrap() error { return e.Err }

// IncompleteError reports a local migration whose steps did not all finish, so
// the tracker is still local. Err, when set, is the step's cause.
type IncompleteError struct {
	Failed int
	Err    error
}

func (e *IncompleteError) Error() string {
	return fmt.Sprintf("%d migration steps did not finish, so the tracker is still local: fix the cause and rerun `hew migrate local` to finish", e.Failed)
}

func (e *IncompleteError) Unwrap() error { return e.Err }
