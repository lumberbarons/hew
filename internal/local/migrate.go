package local

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/lumberbarons/hew/internal/gh"
)

// MigratedFile records where each local issue went. It sits beside the
// archived issues.jsonl, which a finished migration leaves untouched.
const MigratedFile = "migrated.json"

// Comment is one comment as stored, with the author and time the GitHub
// comment API cannot carry over.
type Comment struct {
	Author    string
	CreatedAt time.Time
	Body      string
}

// Issue is one stored issue with its relationships as numbers and its full
// comment history, the shape migration reads. State and StateReason are the
// GraphQL spellings gh uses (OPEN, CLOSED, NOT_PLANNED).
type Issue struct {
	Number      int
	Title       string
	Body        string
	State       string
	StateReason string
	CreatedAt   time.Time
	Labels      []string
	Assignees   []string
	Parent      int
	BlockedBy   []int
	Comments    []Comment
}

// Snapshot is a tracker's whole contents at one moment. Digest is a hash of
// the file's bytes, so a checkpoint made against a snapshot can tell whether
// the tracker changed underneath it.
type Snapshot struct {
	Digest string
	Labels []gh.Label
	Issues []Issue
}

// Migrated is the migrated.json record.
type Migrated struct {
	Version    int       `json:"version"`
	Repo       string    `json:"repo"`
	MigratedAt time.Time `json:"migrated_at"`
	// Mapping is local issue number → GitHub issue number.
	Mapping map[int]int `json:"mapping"`
}

// ReadMigrated reads dir's migration record. A tracker that has not been
// migrated satisfies errors.Is(err, fs.ErrNotExist).
func ReadMigrated(dir string) (Migrated, error) {
	var m Migrated
	raw, err := os.ReadFile(filepath.Join(dir, MigratedFile))
	if err != nil {
		return m, err
	}
	if err := strictUnmarshal(raw, &m); err != nil {
		return m, fmt.Errorf("%s: %w", filepath.Join(dir, MigratedFile), err)
	}
	return m, nil
}

// ReadSnapshot reads the local tracker in dir without locking it. It refuses a
// tracker that is no longer local: that one is an archive already.
func ReadSnapshot(dir string) (*Snapshot, error) {
	if err := requireLocal(dir); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, issuesFile))
	if err != nil {
		return nil, err
	}
	d, err := decodeData(bytes.NewReader(raw), filepath.Join(dir, issuesFile))
	if err != nil {
		return nil, err
	}
	snap := &Snapshot{Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), Labels: slices.Clone(d.labels)}
	for _, rec := range d.issues {
		i := Issue{
			Number: rec.Number, Title: rec.Title, Body: rec.Body,
			State: rec.State, StateReason: rec.StateReason, CreatedAt: rec.CreatedAt,
			Labels: slices.Clone(rec.Labels), Assignees: slices.Clone(rec.Assignees),
			Parent: rec.Parent, BlockedBy: slices.Clone(rec.BlockedBy),
		}
		for _, c := range rec.Comments {
			i.Comments = append(i.Comments, Comment{Author: c.Author, CreatedAt: c.CreatedAt, Body: c.Body})
		}
		snap.Issues = append(snap.Issues, i)
	}
	return snap, nil
}

// requireLocal fails unless dir's config says the tracker is local, with the
// same wording Open uses so every refusal reads alike.
func requireLocal(dir string) error {
	_, err := readLocalConfig(dir)
	return err
}

// readLocalConfig is ReadConfig for a tracker that must still be local.
func readLocalConfig(dir string) (Config, error) {
	cfg, err := ReadConfig(dir)
	if err != nil {
		return cfg, err
	}
	if cfg.Backend != BackendLocal {
		return cfg, fmt.Errorf("%s tracks issues in %q, not locally", dir, cfg.Backend)
	}
	return cfg, nil
}

// Frozen is a tracker held still for the length of a migration. Every writer
// takes the same lock, so none can change the issues between the snapshot and
// the seal; once sealed they find the tracker no longer local and refuse.
type Frozen struct {
	*Snapshot
	dir    string
	unlock func()
}

// migratingFile is the marker a frozen tracker keeps beside its lock, naming
// the migration that holds it. It is advisory: writers read it only to explain
// a wait, never to decide whether they may write.
const migratingFile = "migrating"

type migrationMarker struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

func (m migrationMarker) describe() string {
	if m.PID == 0 {
		return "a `hew migrate local` migration is in progress and holds this tracker until it finishes"
	}
	return fmt.Sprintf("a `hew migrate local` migration is in progress (pid %d, started %s) and holds this tracker until it finishes",
		m.PID, m.StartedAt.Format(time.RFC3339))
}

// readMigrationMarker reports the migration the marker names. A marker that is
// there but garbled still means a migration was under way, so it is described
// without the detail it lost rather than ignored.
func readMigrationMarker(dir string) (migrationMarker, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, migratingFile))
	if err != nil {
		return migrationMarker{}, false
	}
	var m migrationMarker
	_ = json.Unmarshal(raw, &m)
	return m, true
}

// writeMigrationMarker is best effort: the marker only explains a wait, so a
// tracker that cannot record it is still migrated correctly. It is written in
// place rather than by rename: a torn marker reads as garbled and still
// explains the wait, whereas a rename's temp file would outlive a crash with
// nothing sweeping it.
func writeMigrationMarker(dir string) {
	raw, err := marshalLine(migrationMarker{PID: os.Getpid(), StartedAt: time.Now().UTC()})
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, migratingFile), raw, 0o644)
}

func removeMigrationMarker(dir string) { _ = os.Remove(filepath.Join(dir, migratingFile)) }

// Freeze takes the tracker's exclusive lock and snapshots it, leaving a marker
// that tells waiting writers a migration holds it. Release it when the
// migration ends, sealed or not.
func Freeze(ctx context.Context, dir string) (*Frozen, error) {
	if err := requireLocal(dir); err != nil {
		return nil, err
	}
	s := &Store{dir: dir}
	unlock, err := s.lock(ctx)
	if err != nil {
		return nil, err
	}
	// Re-read under the lock: a migration that finished while this one waited
	// leaves a tracker that is no longer local.
	if err := requireLocal(dir); err != nil {
		unlock()
		return nil, err
	}
	snap, err := ReadSnapshot(dir)
	if err != nil {
		unlock()
		return nil, err
	}
	writeMigrationMarker(dir)
	return &Frozen{Snapshot: snap, dir: dir, unlock: unlock}, nil
}

// Seal records where each issue went and flips the tracker to GitHub, leaving
// issues.jsonl as an archive. The record is written first: if the process dies
// between the two, the tracker is still local and the migration can be rerun,
// whereas the other order would strand a migrated tracker with no record.
func (f *Frozen) Seal(repo string, mapping map[int]int) error {
	raw, err := marshalLine(Migrated{Version: formatVersion, Repo: repo, MigratedAt: time.Now().UTC(), Mapping: mapping})
	if err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(f.dir, MigratedFile), raw); err != nil {
		return err
	}
	// Keep the id: the archive still names one tracker, and a config rewritten
	// without it would lose that identity for no gain.
	cfg, err := ReadConfig(f.dir)
	if err != nil {
		return err
	}
	cfg.Backend = BackendGitHub
	return WriteConfig(f.dir, cfg)
}

// Release lets writers back in. Safe to call more than once.
func (f *Frozen) Release() {
	if f.unlock != nil {
		removeMigrationMarker(f.dir)
		f.unlock()
		f.unlock = nil
	}
}
