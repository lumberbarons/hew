// Package migrate provides hew's migration workflows and the source-neutral,
// checkpointed engine underneath them in-process. Open resolves the target
// backend, repository and transport; Migrator.Beads parses a beads snapshot and
// maps it onto the conventions; Migrator.Local freezes a local tracker, copies
// every issue and seals the tracker to GitHub only after every step finished.
// Callers that opened their own target use Wrap with a Config naming their
// checkpoint and repo identity, and may drive the engine directly with Run.
//
// Every write is checkpointed, so an interrupted run resumes where it stopped.
// Nothing here prints, exits, or names an internal store in a public
// signature; a Result carries the counts, the dry-run plan, the backend a
// workflow changed and typed warnings, and a caller decides what it all means.
// Its API is experimental until hew 1.0.
package migrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/lumberbarons/hew/internal/conventions"
	"github.com/lumberbarons/hew/tracker"
)

// CloseReason is why a migration closes a created issue. The zero value means
// completed.
type CloseReason string

const (
	// CloseCompleted closes the issue as completed.
	CloseCompleted CloseReason = "completed"
	// CloseNotPlanned closes the issue as not planned.
	CloseNotPlanned CloseReason = "not-planned"
	// CloseDuplicate closes the issue as a duplicate.
	CloseDuplicate CloseReason = "duplicate"
)

// AssigneeViewer, in an Item's Assignees, stands for the target's
// authenticated identity. It is resolved lazily, so a migration with nobody to
// assign never pays for the lookup. A Target that cannot name its viewer
// reports the failure when the engine asks for it.
const AssigneeViewer = "@me"

// Item is one issue a migration creates, in the source-neutral shape the
// engine works from. Adapters (beads, a local tracker, an external caller) map
// their own records onto it; everything target-specific — the provenance
// marker, write ordering, checkpoint keys — is the engine's, so every source
// gets the same resume and trust guarantees.
type Item struct {
	// Key identifies the item within its source. It is the checkpoint key and
	// the key in the provenance marker.
	Key   string
	Title string
	// Body is the prose the issue shows; the engine appends the provenance
	// marker, so adapters never stamp one themselves.
	Body   string
	Labels []string
	// Assignees are logins; AssigneeViewer means the target's authenticated
	// identity.
	Assignees []string
	// Closed items are closed after their edges are wired and comments posted.
	Closed bool
	// CloseReason is why a closed item is closed; empty means completed.
	CloseReason CloseReason
	// Comments are posted, in order, after the edges are wired and before the
	// issue is closed, if it is. Each one is checkpointed once posted, so a
	// resume carries on from the first that is missing; a Closed item whose
	// issue is already closed is skipped, which keeps a finished run quiet.
	Comments []string
	// Parent and BlockedBy are the Keys of other items.
	Parent    string
	BlockedBy []string
	// Epic marks a source record that is a container. It changes nothing about
	// the writes — an epic's labels and children carry that — but a dry run
	// names it "epic" rather than leaving an untyped issue in the plan.
	Epic bool
}

// CreateRequest is the issue the engine asks a Target to create. The body
// already carries the provenance marker.
type CreateRequest struct {
	Title  string
	Body   string
	Labels []string
}

// Issue is the Target's view of an issue the engine verifies or rewrites: the
// subset it needs, named in public terms so a consumer can fake it.
type Issue struct {
	Number int
	Title  string
	Body   string
	// State is the backend's state name: "OPEN" or "CLOSED".
	State string
}

// Target is the narrow surface the engine reads and writes through: create,
// read, edit, comment, close and dependency edges, plus the acting identity
// for AssigneeViewer. A consumer implements it over its own backend, and a
// test fakes it without a network or a gh process.
type Target interface {
	// Create files one issue and returns its number.
	Create(ctx context.Context, req CreateRequest) (int, error)
	// Get reads one issue for verification and relinking.
	Get(ctx context.Context, number int) (Issue, error)
	EditTitle(ctx context.Context, number int, title string) error
	EditBody(ctx context.Context, number int, body string) error
	Comment(ctx context.Context, number int, body string) error
	Close(ctx context.Context, number int, reason CloseReason) error
	// AddSubIssue attaches child under parent.
	AddSubIssue(ctx context.Context, parent, child int) error
	// AddBlockedBy marks issue as blocked by blocker.
	AddBlockedBy(ctx context.Context, issue, blocker int) error
	AddAssignee(ctx context.Context, number int, login string) error
	// Viewer returns the authenticated identity AssigneeViewer resolves to.
	Viewer(ctx context.Context) (string, error)
}

// Checkpoint is where a run records progress and resumes from: the issue each
// item became, the dependency edges already wired, and the named steps already
// taken (a posted comment). tracker.FileCheckpoint persists it to the state
// file the CLI uses; tracker.MemoryCheckpoint keeps it in memory.
type Checkpoint interface {
	tracker.Checkpoint
	// Recorded reports whether a named step was already recorded.
	Recorded(key string) bool
	// RecordStep records a named step and persists the checkpoint.
	RecordStep(key string) error
}

// Options configure one run.
type Options struct {
	// Kind is the provenance kind stamped into created bodies and checked
	// before the finish pass touches an issue. Empty means the migration kind
	// hew's own commands write.
	Kind string
	// ItemNoun and SourceNoun name the source's records and file in messages
	// ("bead", "snapshot").
	ItemNoun   string
	SourceNoun string
	// KeyNoun names a checkpoint key in a refusal ("bead ID"). Empty derives
	// one from ItemNoun.
	KeyNoun string
	// Throttle is waited between writes to stay under a backend's rate limits.
	// The wait is interruptible by the run's context.
	Throttle time.Duration
	// DryRun validates the checkpoint and the items and reports what a real
	// run would create, without writing.
	DryRun bool
	// Relink, when set, rewrites the references in an item's body, title and
	// comments once every item has a target number: it is handed the text and
	// the key → number mapping. Bodies are created as the source wrote them,
	// since the numbers are not known yet, and edited afterwards.
	Relink func(text string, mapping map[string]int) string
	// KnownKeys is every key that belongs to the source, including records a
	// run's selection filtered out. A checkpoint mapping outside it is
	// refused: a removed record's issue number cannot be trusted. Nil means
	// the keys of Items.
	KnownKeys map[string]bool
	// Digest identifies the source in provenance markers and checkpoint
	// records. Empty means the checkpoint's own binding, or a digest computed
	// from the items when it has none.
	Digest string
	// BeforeCreate, when set, runs after the checkpoint and provenance checks
	// pass and before the first create. It is where a caller prepares what the
	// creates need — the CLI bootstraps labels there — so a refused checkpoint
	// costs no write at all. It is not called under DryRun.
	BeforeCreate func(ctx context.Context) error
	// Progress receives one line of running narration per write, for a caller
	// that renders it. Nil is silent; the engine never prints.
	Progress func(format string, args ...any)
}

// RunRequest is one engine run: the source's items and the options that name
// it.
type RunRequest struct {
	Items   []Item
	Options Options
}

// Migrator runs one migration workflow over one Target: the engine's passes
// with the checkpoint, identity and source options the caller or Open
// resolved. It owns the checkpoint and a run, so callers run one migration at
// a time per Migrator.
type Migrator struct {
	target     Target
	checkpoint Checkpoint
	cfg        Config
	// backend, repoName, repoKey and worktree are what Open resolved: the
	// target's backend, the GitHub repository a Local migration seals to, the
	// identity a checkpoint binds to, and the checkout the run works from.
	// Wrap leaves them empty, because its caller supplied the target itself.
	backend  tracker.Backend
	repoName string
	repoKey  string
	worktree string
}

// Wrap makes a Migrator from an already-open target and the workflow options
// its caller resolved: the seam hew's own CLI uses, where it has opened the
// store and knows the identity its checkpoint binds to. External callers use
// Open, which resolves the backend, repository and transport too.
//
// A nil target is allowed only for a dry run: the plan then comes from the
// source alone, and a workflow asked to verify an existing checkpoint refuses
// rather than reading a state file it cannot vouch for.
func Wrap(target Target, cfg Config) *Migrator {
	key := cfg.RepoKey
	if key == "" {
		key = cfg.Repo
	}
	return &Migrator{target: target, checkpoint: cfg.Checkpoint, cfg: cfg, repoName: cfg.Repo, repoKey: key}
}

// requireTarget refuses a workflow with nowhere to write. A dry run may plan
// without one, reading the source alone.
func (m *Migrator) requireTarget() error {
	if m.target != nil || m.cfg.DryRun {
		return nil
	}
	return &tracker.InvalidInputError{Field: "target", Reason: "must be set; only a dry run may plan without one"}
}

// checkpointFor settles the resume state one workflow records through: the
// checkpoint a wrapping caller supplied, or a file opened at CheckpointPath
// and bound to the source's digest. A targetless dry run has nothing to verify
// a file against, so an existing one is refused and a fresh in-memory
// checkpoint plans the source whole.
func (m *Migrator) checkpointFor(digest, sourceNoun string) (Checkpoint, error) {
	if m.checkpoint != nil {
		return m.checkpoint, nil
	}
	if m.target == nil {
		if err := m.refuseUnverifiableCheckpoint(); err != nil {
			return nil, err
		}
		return tracker.NewMemoryCheckpoint(), nil
	}
	if m.cfg.CheckpointPath == "" {
		return nil, &tracker.InvalidInputError{Field: "checkpoint", Reason: "must name the resume-state file the run records progress in"}
	}
	return tracker.OpenFileCheckpoint(m.cfg.CheckpointPath, m.repoKey, digest, sourceNoun)
}

// refuseUnverifiableCheckpoint reports the one case a targetless dry run
// cannot answer: a checkpoint on disk can only be checked against the issues it
// maps, and there is no issue store to read.
func (m *Migrator) refuseUnverifiableCheckpoint() error {
	path := m.cfg.CheckpointPath
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err == nil {
		return &CheckpointError{Path: path, Reason: "a checkpoint from an earlier run can only be checked against GitHub, and no target repository is set"}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("cannot tell whether %s holds a checkpoint: %w", path, err)
	}
	return nil
}

// Run creates every item not already in the checkpoint, wires parents and
// blockers, rewrites references, then posts comments and closes the items
// marked closed. A create failure aborts (the checkpoint makes the rerun
// resume); later failures warn and continue.
//
// The checkpoint's mappings are verified against the target before the first
// write: every mapped issue must carry the provenance marker this source kind
// wrote, bound to its key and digest. A refused run therefore leaves nothing
// behind. Context cancellation interrupts throttling and backend calls and
// returns the partial Result with the context error.
func (m *Migrator) Run(ctx context.Context, req RunRequest) (Result, error) {
	return m.run(ctx, req, m.checkpoint)
}

// run is Run with the checkpoint named explicitly: a workflow opens one per
// source, because the digest binds to bytes that are only known then. The copy
// keeps the migrator's own checkpoint untouched, so a wrapped migrator can run
// either seam without one run's state file leaking into another.
func (m *Migrator) run(ctx context.Context, req RunRequest, checkpoint Checkpoint) (res Result, err error) {
	runner := *m
	runner.checkpoint = checkpoint
	return runner.runPasses(ctx, req)
}

// runPasses is the engine proper.
func (m *Migrator) runPasses(ctx context.Context, req RunRequest) (res Result, err error) {
	opts := req.Options.normalized()
	if err := m.validate(req.Items, opts); err != nil {
		return Result{}, err
	}
	digest, err := m.digest(req.Items, opts)
	if err != nil {
		return Result{}, err
	}
	if err := m.verify(ctx, req.Items, opts, digest); err != nil {
		return Result{}, err
	}

	res = Result{DryRun: opts.DryRun}
	defer func() { res.Mapping = m.mapping() }()
	if opts.DryRun {
		res.Plan, res.WouldCreate, res.AlreadyCreated = m.plan(req.Items)
		return res, nil
	}

	if opts.BeforeCreate != nil {
		if err := opts.BeforeCreate(ctx); err != nil {
			return res, err
		}
	}

	created, err := m.create(ctx, req.Items, opts, digest, &res)
	res.Created = created
	if err != nil {
		return res, err
	}
	if err := m.wire(ctx, req.Items, opts, &res); err != nil {
		return res, err
	}
	if err := m.relink(ctx, req.Items, opts, digest, &res); err != nil {
		return res, err
	}
	if err := m.finish(ctx, req.Items, opts, digest, &res); err != nil {
		return res, err
	}
	return res, nil
}

// normalized fills the defaults a caller may leave out.
func (o Options) normalized() Options {
	if o.Kind == "" {
		o.Kind = conventions.ProvenanceMigrate
	}
	if o.ItemNoun == "" {
		o.ItemNoun = "item"
	}
	if o.SourceNoun == "" {
		o.SourceNoun = "source"
	}
	if o.KeyNoun == "" {
		o.KeyNoun = o.ItemNoun + " key"
	}
	return o
}

// validate refuses a Migrator without a target or checkpoint, and items whose
// keys cannot identify them in the checkpoint. No backend is touched.
func (m *Migrator) validate(items []Item, opts Options) error {
	if m.target == nil && !opts.DryRun {
		return &tracker.InvalidInputError{Field: "target", Reason: "must be set"}
	}
	if m.checkpoint == nil {
		return &tracker.InvalidInputError{Field: "checkpoint", Reason: "must be set"}
	}
	seen := make(map[string]bool, len(items))
	for _, it := range items {
		if it.Key == "" {
			return &ItemError{Field: "key", Reason: "must not be empty, because it identifies the item in the checkpoint"}
		}
		if seen[it.Key] {
			return &DuplicateKeyError{Key: it.Key, ItemNoun: opts.ItemNoun}
		}
		seen[it.Key] = true
	}
	return nil
}

// digest settles the source identity the run's provenance markers and
// checkpoint agree on. An explicit digest must agree with the checkpoint's
// binding: using both would write markers the checkpoint can never verify.
func (m *Migrator) digest(items []Item, opts Options) (string, error) {
	bound := m.checkpoint.Digest()
	if opts.Digest != "" {
		if bound != "" && opts.Digest != bound {
			return "", &tracker.InvalidInputError{Field: "digest", Reason: "does not match the checkpoint's source binding"}
		}
		return opts.Digest, nil
	}
	if bound != "" {
		return bound, nil
	}
	data, err := json.Marshal(items)
	if err != nil {
		return "", fmt.Errorf("computing the source digest: %w", err)
	}
	return tracker.FileDigest(data), nil
}

// verify checks every mapping in the checkpoint against the target before the
// caller mutates anything: the key must belong to this source, and the issue
// must carry the provenance marker this batch kind writes, bound to this key
// and this source digest. Mappings are walked in sorted order (the
// checkpoint's own ordering), so a state file with more than one bad entry
// fails on the same one every time.
func (m *Migrator) verify(ctx context.Context, items []Item, opts Options, digest string) error {
	known := opts.KnownKeys
	if known == nil {
		known = itemKeys(items)
	}
	for _, entry := range m.checkpoint.CreatedEntries() {
		key, number := entry.Key, entry.Number
		if !known[key] {
			return fmt.Errorf("state file maps %s %q to issue #%d, but %q is not in this run's selection — it was removed from the source, or a flag is filtering it out",
				opts.KeyNoun, key, number, key)
		}
		issue, err := m.target.Get(ctx, number)
		if err != nil {
			return fmt.Errorf("verifying mapped issue #%d for %s: %w", number, key, err)
		}
		if !conventions.HasProvenanceMarker(issue.Body, opts.Kind, key, digest) {
			return fmt.Errorf("refusing to modify issue #%d: it does not carry the hew provenance marker for %s %q from this source file, so this run did not create it",
				number, opts.KeyNoun, key)
		}
	}
	return nil
}

// itemKeys is the KnownKeys default: every item the run carries.
func itemKeys(items []Item) map[string]bool {
	known := make(map[string]bool, len(items))
	for _, it := range items {
		known[it.Key] = true
	}
	return known
}
