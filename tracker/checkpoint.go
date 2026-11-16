package tracker

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// CheckpointVersion is the current resume-state schema. It exists so a state
// file this build cannot safely interpret is named as such instead of being
// reported as corrupt: version 0 is any file written before checkpoints
// carried a repository and source binding (#81).
const CheckpointVersion = 1

// CheckpointState is the on-disk shape of a batch checkpoint shared by
// `hew apply` and `hew migrate`: which source key became which issue, which
// dependency edges were already wired, and which named steps (a posted
// migration comment) were already taken. FileCheckpoint reads and writes it;
// the CLI's migrate path uses the same struct for the same files.
type CheckpointState struct {
	Version int             `json:"version"`
	Repo    string          `json:"repo"`
	Digest  string          `json:"digest"`
	Mapping map[string]int  `json:"mapping"`
	Edges   map[string]bool `json:"edges,omitempty"`
}

// FileDigest computes the sha256 digest a checkpoint binds to: a plan's or a
// snapshot's exact bytes.
func FileDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", sum)
}

// EdgeKey identifies a wired edge by kind and resolved endpoints, so the
// record survives a plan whose local ids or line numbers moved.
func EdgeKey(kind EdgeKind, from, to int) string {
	return fmt.Sprintf("%s:%d->%d", kind, from, to)
}

// LoadCheckpointFile reads a checkpoint file. A missing file is a fresh start;
// a corrupt, unbound, or mismatched one aborts — treating it as empty would
// duplicate every already-created issue, and using an unbound or mismatched
// one could mutate unrelated issues (#81).
func LoadCheckpointFile(path string, repo string, digest string, sourceNoun string) (*CheckpointState, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &CheckpointState{
			Version: CheckpointVersion,
			Repo:    repo,
			Digest:  digest,
			Mapping: map[string]int{},
			Edges:   map[string]bool{},
		}, nil
	}
	if err != nil {
		return nil, err
	}
	var state CheckpointState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("%s is not a valid resume-state file: %w; %s", path, err, restartHint(path))
	}
	switch {
	case state.Version == 0 || state.Repo == "" || state.Digest == "":
		return nil, fmt.Errorf("%s was written by an older hew and cannot be resumed safely: it records no repository or %s binding, so its issue numbers cannot be trusted; %s",
			path, sourceNoun, restartHint(path))
	case state.Version > CheckpointVersion:
		return nil, fmt.Errorf("%s was written by a newer hew (state version %d, this build understands %d); upgrade hew to resume it",
			path, state.Version, CheckpointVersion)
	}
	if !strings.EqualFold(state.Repo, repo) {
		return nil, fmt.Errorf("state file %s belongs to repository %q, not %q; %s", path, state.Repo, repo, restartHint(path))
	}
	if state.Digest != digest {
		return nil, fmt.Errorf("state file %s was written for a different %s — the file's contents changed since the run that created it, and checkpoints are bound to those contents; restore the original %s to resume, or %s",
			path, sourceNoun, sourceNoun, restartHint(path))
	}
	if state.Mapping == nil {
		state.Mapping = map[string]int{}
	}
	if state.Edges == nil {
		state.Edges = map[string]bool{}
	}
	return &state, nil
}

// SaveCheckpointFile persists a checkpoint after every write. It is written to
// a temp name and renamed over the checkpoint: it is rewritten after every
// create, edge and comment, and a torn file would be refused as corrupt with
// only a duplicating restart on offer.
func SaveCheckpointFile(path string, state *CheckpointState) error {
	data, err := json.MarshalIndent(state, "", "  ") // map keys marshal sorted
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// restartHint is appended to every rejection. Naming the remedy matters more
// than usual here: the output is read by agents, and the obvious move —
// deleting the checkpoint file — silently re-creates everything the
// interrupted run already created, which is the one outcome the checkpoint
// exists to prevent.
func restartHint(path string) string {
	return fmt.Sprintf("pass --checkpoint with a new path to start a fresh run (every issue the interrupted run already created will be created again), or delete %s", path)
}

// Checkpoint is where ApplyPlan records progress and resumes from: the issue
// each entry became, and the dependency edges already wired. ApplyPlan reads
// it before writing anything and records every write through it.
//
// The zero ApplyOptions uses a fresh MemoryCheckpoint, so nothing resumes
// unless the caller passes the same one back. FileCheckpoint persists to the
// state file `hew apply` uses and survives the process.
type Checkpoint interface {
	// Created reports the issue number already recorded for an entry key.
	Created(key string) (int, bool)
	// CreatedEntries returns every recorded mapping, sorted by key, so a
	// validation pass fails on the same entry every time.
	CreatedEntries() []CheckpointEntry
	// RecordCreated records that key was created as number and persists the
	// checkpoint. A failure means the issue exists but the resume record does
	// not, which ApplyPlan reports without undoing the create.
	RecordCreated(key string, number int) error

	// Wired reports whether an edge was already recorded as wired.
	Wired(kind EdgeKind, from, to int) bool
	// RecordWired records a wired edge and persists the checkpoint. A failure
	// means the edge landed but a rerun would attempt it again.
	RecordWired(kind EdgeKind, from, to int) error

	// Digest is the source identity the checkpoint is bound to, or empty for
	// a checkpoint with no durable source. ApplyPlan uses it when the caller
	// passes no digest, so a file checkpoint's markers agree with its binding.
	Digest() string
}

// CheckpointEntry is one recorded entry-key → issue-number mapping.
type CheckpointEntry struct {
	Key    string
	Number int
}

// MemoryCheckpoint is a Checkpoint that keeps its records in memory. The zero
// value is ready to use; nothing survives the process.
type MemoryCheckpoint struct {
	mapping map[string]int
	edges   map[string]bool
}

// NewMemoryCheckpoint returns an empty in-memory checkpoint.
func NewMemoryCheckpoint() *MemoryCheckpoint {
	return &MemoryCheckpoint{mapping: map[string]int{}, edges: map[string]bool{}}
}

func (c *MemoryCheckpoint) Created(key string) (int, bool) {
	n, ok := c.mapping[key]
	return n, ok
}

func (c *MemoryCheckpoint) CreatedEntries() []CheckpointEntry {
	return checkpointEntries(c.mapping)
}

func (c *MemoryCheckpoint) RecordCreated(key string, number int) error {
	if c.mapping == nil {
		c.mapping = map[string]int{}
	}
	c.mapping[key] = number
	return nil
}

func (c *MemoryCheckpoint) Wired(kind EdgeKind, from, to int) bool {
	return c.edges[EdgeKey(kind, from, to)]
}

func (c *MemoryCheckpoint) RecordWired(kind EdgeKind, from, to int) error {
	if c.edges == nil {
		c.edges = map[string]bool{}
	}
	c.edges[EdgeKey(kind, from, to)] = true
	return nil
}

// Recorded reports whether a named step was already recorded. Named steps are
// the migration engine's non-edge writes, such as a posted comment.
func (c *MemoryCheckpoint) Recorded(key string) bool {
	return c.edges[key]
}

// RecordStep records a named step. The key shares the record space with
// wired edges but is not one: callers pick a namespace that cannot collide
// with EdgeKey's "kind:from->to" form.
func (c *MemoryCheckpoint) RecordStep(key string) error {
	if c.edges == nil {
		c.edges = map[string]bool{}
	}
	c.edges[key] = true
	return nil
}

// Digest is always empty: an in-memory checkpoint has no durable source, so
// ApplyPlan derives one from the entries.
func (c *MemoryCheckpoint) Digest() string { return "" }

// FileCheckpoint is a Checkpoint persisted in the state file the CLI uses.
// OpenFileCheckpoint verifies the file belongs to repo and digest before any
// write, so a checkpoint from another repository or source is refused.
type FileCheckpoint struct {
	path  string
	state *CheckpointState
}

// OpenFileCheckpoint opens or creates the checkpoint at path, bound to repo
// and digest. A missing file is a fresh run; an existing one must agree with
// both bindings, or it is refused rather than treated as empty.
func OpenFileCheckpoint(path string, repo string, digest string, sourceNoun string) (*FileCheckpoint, error) {
	state, err := LoadCheckpointFile(path, repo, digest, sourceNoun)
	if err != nil {
		return nil, err
	}
	return &FileCheckpoint{path: path, state: state}, nil
}

func (c *FileCheckpoint) Created(key string) (int, bool) {
	n, ok := c.state.Mapping[key]
	return n, ok
}

func (c *FileCheckpoint) CreatedEntries() []CheckpointEntry {
	return checkpointEntries(c.state.Mapping)
}

func (c *FileCheckpoint) RecordCreated(key string, number int) error {
	c.state.Mapping[key] = number
	return SaveCheckpointFile(c.path, c.state)
}

func (c *FileCheckpoint) Wired(kind EdgeKind, from, to int) bool {
	return c.state.Edges[EdgeKey(kind, from, to)]
}

func (c *FileCheckpoint) RecordWired(kind EdgeKind, from, to int) error {
	c.state.Edges[EdgeKey(kind, from, to)] = true
	return SaveCheckpointFile(c.path, c.state)
}

// Recorded reports whether a named step was already recorded. Named steps are
// the migration engine's non-edge writes, such as a posted comment.
func (c *FileCheckpoint) Recorded(key string) bool {
	return c.state.Edges[key]
}

// RecordStep records a named step and persists the checkpoint. The key shares
// the record space with wired edges but is not one: callers pick a namespace
// that cannot collide with EdgeKey's "kind:from->to" form.
func (c *FileCheckpoint) RecordStep(key string) error {
	c.state.Edges[key] = true
	return SaveCheckpointFile(c.path, c.state)
}

// Digest is the digest the file was opened with and every marker it verifies
// is bound to.
func (c *FileCheckpoint) Digest() string { return c.state.Digest }

func checkpointEntries(mapping map[string]int) []CheckpointEntry {
	keys := make([]string, 0, len(mapping))
	for key := range mapping {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]CheckpointEntry, 0, len(keys))
	for _, key := range keys {
		out = append(out, CheckpointEntry{Key: key, Number: mapping[key]})
	}
	return out
}
