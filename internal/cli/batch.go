package cli

// Shared machinery for the batch writers (migrate, apply): a checkpoint
// state file mapping source keys to created issue numbers, throttled writes,
// and label bootstrapping so batch creates never reference a missing label.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lumberbarons/hew/internal/conventions"
	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/model"
	"github.com/lumberbarons/hew/internal/plan"
	"github.com/lumberbarons/hew/internal/render"
	"github.com/lumberbarons/hew/tracker"
)

// ensureLabels creates any missing convention labels plus the given extras.
func (a *App) ensureLabels(ctx context.Context, extras []gh.Label) error {
	existing, err := a.Client.ListLabels(ctx)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, l := range existing {
		have[strings.ToLower(l.Name)] = true
	}
	var want []gh.Label
	for _, l := range conventions.Labels {
		want = append(want, gh.Label{Name: l.Name, Color: l.Color, Description: l.Description})
	}
	want = append(want, extras...)
	for _, l := range want {
		// GitHub label names are case-insensitive, so Area:CLI is area:cli.
		if have[strings.ToLower(l.Name)] {
			continue
		}
		if err := a.Client.CreateLabel(ctx, l); err != nil {
			return fmt.Errorf("creating label %q: %w", l.Name, err)
		}
		have[strings.ToLower(l.Name)] = true
	}
	return nil
}

// batchStateVersion and batchState are the CLI's spelling of the checkpoint
// schema tracker owns, shared with the migrate engine. ApplyPlan lives in
// tracker and takes a tracker.Checkpoint; migrate's engine still works on the
// struct directly.
const batchStateVersion = tracker.CheckpointVersion

type batchState = tracker.CheckpointState

// fileDigest computes the sha256 digest a checkpoint binds to.
func fileDigest(data []byte) string { return tracker.FileDigest(data) }

// edgeKey identifies a wired edge by kind and resolved endpoints.
func edgeKey(kind plan.EdgeKind, from, to int) string { return tracker.EdgeKey(kind, from, to) }

// loadBatchState reads a checkpoint file, refusing one written for another
// repository, source, schema version, or an older hew that recorded no binding.
func loadBatchState(path string, repo string, digest string, sourceNoun string) (*batchState, error) {
	return tracker.LoadCheckpointFile(path, repo, digest, sourceNoun)
}

func saveBatchState(path string, state *batchState) error {
	return tracker.SaveCheckpointFile(path, state)
}

// stateRepo is the identity a checkpoint binds to: the GitHub repository, or
// the local tracker's stable id. The local backend cannot use Repo.String():
// it is the checkout's directory name, which two checkouts can share and a
// rename changes.
func (a *App) stateRepo() string {
	if a.RepoKey != "" {
		return a.RepoKey
	}
	return a.Repo.String()
}

// verifyBatchState checks every mapping in a checkpoint against GitHub before
// the caller mutates anything: the key must belong to this source file, and
// the issue must carry the provenance marker this batch kind writes, bound to
// this key and this source digest. Both migrate commands run it as one pass up
// front so a rejection costs no writes at all (#81).
//
// Mappings are walked in sorted order so a state file with more than one bad
// entry fails on the same one every time — an unstable error message is a
// bad bug report.
func (a *App) verifyBatchState(ctx context.Context, state *batchState, kind string, valid map[string]bool, keyNoun string) error {
	keys := make([]string, 0, len(state.Mapping))
	for key := range state.Mapping {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		number := state.Mapping[key]
		if !valid[key] {
			return fmt.Errorf("state file maps %s %q to issue #%d, but %q is not in this run's selection — it was removed from the source, or a flag is filtering it out", keyNoun, key, number, key)
		}
		issue, err := a.Client.GetIssue(ctx, number)
		if err != nil {
			return fmt.Errorf("verifying mapped issue #%d for %s: %w", number, key, err)
		}
		if !conventions.HasProvenanceMarker(issue.Body, kind, key, state.Digest) {
			return fmt.Errorf("refusing to modify issue #%d: it does not carry the hew provenance marker for %s %q from this source file, so this run did not create it", number, keyNoun, key)
		}
	}
	return nil
}

func sleep(d time.Duration) {
	if d > 0 {
		time.Sleep(d)
	}
}

// assigneeViewer in a migrationItem's Assignees stands for the authenticated
// user, resolved lazily so a migration with nobody to assign never pays for
// the lookup.
const assigneeViewer = "@me"

// migrationItem is one issue a migration creates, in the source-neutral shape
// the engine works from. Adapters (beads today) map their own records onto it;
// everything GitHub-specific — labels, provenance, ordering of writes — is the
// engine's, so each source gets the same resume and trust guarantees.
type migrationItem struct {
	// Key identifies the item within its source. It is the checkpoint key and
	// the key in the provenance marker.
	Key   string
	Title string
	// Body is the prose the issue shows; the engine appends the provenance
	// marker, so adapters never stamp one themselves.
	Body   string
	Labels []string
	// Assignees are logins; assigneeViewer means the authenticated user.
	Assignees []string
	// Closed items are closed after their edges are wired.
	Closed bool
	// CloseReason is the GitHub close reason; empty means completed.
	CloseReason gh.CloseReason
	// Comments are posted, in order, after the edges are wired and before the
	// issue is closed, if it is. Each one is checkpointed once posted, so a
	// resume carries on from the first that is missing; a Closed item whose
	// issue is already closed is skipped, which keeps a finished run quiet.
	Comments []string
	// Parent and BlockedBy are the Keys of other items.
	Parent    string
	BlockedBy []string
}

// migrationEngine drives the create, wire, relink and finish (comment, close) passes over a source's
// items. The caller owns everything that happens before a write — parsing,
// the dry run, and state verification — because those depend on the source.
type migrationEngine struct {
	// Kind is the provenance kind stamped into created bodies and checked
	// before the close pass touches an issue.
	Kind string
	// ItemNoun and SourceNoun name the source's records and file in messages
	// ("bead", "snapshot").
	ItemNoun   string
	SourceNoun string
	Items      []migrationItem
	StatePath  string
	Throttle   time.Duration
	// Relink, when set, rewrites the references in an item's body and comments
	// once every item has an issue number: it is handed the text and the
	// key → number mapping. Bodies are created as the source wrote them, since
	// the numbers are not known yet, and edited afterwards.
	Relink func(text string, mapping map[string]int) string
}

// migrationResult is what the passes did, for the caller to report. Failed
// counts the steps that failed and would be tried again by a rerun, so a caller
// can tell a migration that is whole from one that needs another run; warnings
// about things a rerun cannot change (an edge dropped for a cycle, a body a
// person edited) are listed in Warnings but are not failures.
type migrationResult struct {
	Created  int
	Wired    int
	Closed   int
	Relinked int
	Failed   int
	Warnings []string
}

// note records a warning that a rerun cannot change.
func (r *migrationResult) note(a *App, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	r.Warnings = append(r.Warnings, msg)
	a.warnf("%s", msg)
}

// fail records a warning for a step a rerun will try again.
func (r *migrationResult) fail(a *App, format string, args ...any) {
	r.note(a, format, args...)
	r.Failed++
}

// runMigration creates every item not already in the checkpoint, wires
// parents and blockers, rewrites references, then posts comments and closes the
// items marked closed. A create failure aborts (the checkpoint makes the rerun
// resume); later failures warn and continue.
func (a *App) runMigration(ctx context.Context, eng migrationEngine, state *batchState) (migrationResult, error) {
	seen := make(map[string]bool, len(eng.Items))
	for _, it := range eng.Items {
		if seen[it.Key] {
			return migrationResult{}, fmt.Errorf("%s %s appears more than once; keys identify items in the checkpoint and must be unique", eng.ItemNoun, it.Key)
		}
		seen[it.Key] = true
	}
	var res migrationResult
	created, err := a.migrationCreate(ctx, eng, state)
	res.Created = created
	if err != nil {
		return res, err
	}
	a.migrationWire(ctx, eng, state, &res)
	a.migrationRelink(ctx, eng, state, &res)
	a.migrationFinish(ctx, eng, state, &res)
	return res, nil
}

func (a *App) migrationCreate(ctx context.Context, eng migrationEngine, state *batchState) (int, error) {
	created := 0
	viewer := ""
	for _, it := range eng.Items {
		if n, ok := state.Mapping[it.Key]; ok {
			a.progressf("already migrated: %s → #%d\n", it.Key, n)
			continue
		}
		body := it.Body + "\n\n" + conventions.ProvenanceMarker(eng.Kind, it.Key, state.Digest)
		issue, err := a.Client.CreateIssue(ctx, it.Title, body, it.Labels)
		if err != nil {
			return created, fmt.Errorf("creating %s (rerun to resume): %w", it.Key, err)
		}
		state.Mapping[it.Key] = issue.Number
		if err := saveBatchState(eng.StatePath, state); err != nil {
			return created, err
		}
		created++
		a.progressf("created #%d from %s: %s\n", issue.Number, it.Key, it.Title)
		for _, login := range it.Assignees {
			if login == assigneeViewer {
				if viewer == "" {
					v, err := a.Client.Viewer(ctx)
					if err != nil {
						return created, err
					}
					viewer = v
				}
				login = viewer
			}
			if err := a.Client.AddAssignee(ctx, issue.Number, login); err != nil {
				a.warnf("assigning #%d: %v", issue.Number, err)
			}
		}
		sleep(eng.Throttle)
	}
	return created, nil
}

// migrationWire connects parents and blockers. Failures warn and continue and
// are retried on resume. Each edge is checkpointed once wired, because GitHub
// answers a duplicate edge with an error, and a resume that warned about edges
// it had already made would never read as complete.
//
// GitHub rejects self-blocks and two-issue cycles but silently accepts longer
// ones, and a cycle removes all its members from ready — so every blocked-by
// edge is checked against the edges accepted so far before it is wired, and an
// edge that would close a cycle is dropped with a warning. The graph is the
// items' own: only they can reference each other, and replaying the items in
// order on a resume rebuilds the same graph. An edge enters the graph once it
// passes the check, even if wiring it then fails, because a resume may find it
// already on GitHub — missing it would hide a cycle.
func (a *App) migrationWire(ctx context.Context, eng migrationEngine, state *batchState, res *migrationResult) {
	mapping := state.Mapping

	graph := make([]model.Issue, 0, len(mapping))
	index := make(map[int]int, len(mapping))
	for _, it := range eng.Items {
		if n, ok := mapping[it.Key]; ok {
			index[n] = len(graph)
			graph = append(graph, model.Issue{Number: n, State: string(gh.StateOpen)})
		}
	}

	edge := func(fromKey, toKey, kind string, connect func(from, to int) error) {
		from, ok := mapping[fromKey]
		if !ok {
			return
		}
		to, ok := mapping[toKey]
		if !ok {
			res.note(a, "%s of %s not migrated, %s edge dropped", toKey, fromKey, kind)
			return
		}
		if kind == "blocked-by" {
			check := model.CheckBlockedBy(graph, from, to)
			if check.Cycle != nil {
				res.note(a, "blocked-by edge #%d→#%d dropped: would create dependency cycle %s", from, to, render.FormatCycle(check.Cycle))
				return
			}
			i := index[from]
			graph[i].BlockedBy = append(graph[i].BlockedBy, model.Ref{Number: to, State: string(gh.StateOpen)})
			graph[i].BlockedByTotal = len(graph[i].BlockedBy)
		}
		key := edgeKey(plan.EdgeKind(kind), from, to)
		if state.Edges[key] {
			return
		}
		if err := connect(from, to); err != nil {
			res.fail(a, "%s edge #%d→#%d: %v", kind, from, to, err)
			return
		}
		state.Edges[key] = true
		if err := saveBatchState(eng.StatePath, state); err != nil {
			res.fail(a, "saving checkpoint: %v", err)
		}
		res.Wired++
		sleep(eng.Throttle)
	}
	for _, it := range eng.Items {
		if it.Parent != "" {
			edge(it.Key, it.Parent, "parent", func(child, parent int) error {
				return a.Client.AddSubIssue(ctx, parent, child, true)
			})
		}
		for _, blocker := range it.BlockedBy {
			edge(it.Key, blocker, "blocked-by", func(issue, blocker int) error {
				return a.Client.AddBlockedBy(ctx, issue, blocker)
			})
		}
	}
}

// migrationRelink rewrites the references in each created issue's title and
// body now that every item has a number. It only edits text still exactly as
// the create wrote it: one a person has changed since is left alone with a
// warning, and one already rewritten is skipped, so a resume is quiet.
func (a *App) migrationRelink(ctx context.Context, eng migrationEngine, state *batchState, res *migrationResult) {
	if eng.Relink == nil {
		return
	}
	for _, it := range eng.Items {
		number, ok := state.Mapping[it.Key]
		if !ok {
			continue
		}
		marker := conventions.ProvenanceMarker(eng.Kind, it.Key, state.Digest)
		original := it.Body + "\n\n" + marker
		rewritten := eng.Relink(it.Body, state.Mapping) + "\n\n" + marker
		title := eng.Relink(it.Title, state.Mapping)
		if rewritten == original && title == it.Title {
			continue
		}
		issue, err := a.Client.GetIssue(ctx, number)
		if err != nil {
			res.fail(a, "relinking #%d: %v", number, err)
			continue
		}
		edited := false
		if rewritten != original {
			switch strings.TrimSpace(issue.Body) {
			case strings.TrimSpace(rewritten):
			case strings.TrimSpace(original):
				if err := a.Client.EditBody(ctx, number, rewritten); err != nil {
					res.fail(a, "relinking #%d: %v", number, err)
					continue
				}
				edited = true
			default:
				res.note(a, "not relinking #%d: its body changed since it was created, so references to other %ss are left as written", number, eng.ItemNoun)
			}
		}
		if title != it.Title {
			switch issue.Title {
			case title:
			case it.Title:
				if err := a.Client.EditTitle(ctx, number, title); err != nil {
					res.fail(a, "relinking the title of #%d: %v", number, err)
					continue
				}
				edited = true
			default:
				res.note(a, "not relinking the title of #%d: it changed since it was created", number)
			}
		}
		if edited {
			res.Relinked++
			sleep(eng.Throttle)
		}
	}
}

// commentKey identifies one posted comment in the checkpoint's edge record.
func commentKey(itemKey string, index int) string {
	return fmt.Sprintf("comment:%s:%d", itemKey, index)
}

// migrationFinish posts the items' comments, in order, then closes the ones
// marked closed. Tolerant: an issue already closed (a resume of a finished
// run) is skipped silently, and a failed call warns and moves on. It returns
// its counts in res.
//
// The provenance check repeats here rather than trusting the up-front pass:
// commenting and closing is the most destructive thing a migration does, and
// the mapping it reads includes entries this run added after that pass.
// Failing closed on a mapping that cannot be vouched for skips one item
// instead of aborting a migration that is already part-written (#81).
func (a *App) migrationFinish(ctx context.Context, eng migrationEngine, state *batchState, res *migrationResult) {
	for _, it := range eng.Items {
		if !it.Closed && len(it.Comments) == 0 {
			continue
		}
		number, ok := state.Mapping[it.Key]
		if !ok {
			continue
		}
		issue, err := a.Client.GetIssue(ctx, number)
		if err != nil {
			res.fail(a, "closing #%d: %v", number, err)
			continue
		}
		if it.Closed && !issue.IsOpen() {
			continue
		}
		if !conventions.HasProvenanceMarker(issue.Body, eng.Kind, it.Key, state.Digest) {
			res.fail(a, "not closing #%d: it does not carry the hew provenance marker for %s %s from this %s", number, eng.ItemNoun, it.Key, eng.SourceNoun)
			continue
		}
		commented := true
		for i, c := range it.Comments {
			key := commentKey(it.Key, i)
			if state.Edges[key] {
				continue
			}
			if eng.Relink != nil {
				c = eng.Relink(c, state.Mapping)
			}
			if err := a.Client.Comment(ctx, number, c); err != nil {
				res.fail(a, "comment on #%d: %v", number, err)
				// Later comments would land out of order, and closing now would
				// stop a rerun from posting the rest; leave both for the rerun.
				commented = false
				break
			}
			state.Edges[key] = true
			if err := saveBatchState(eng.StatePath, state); err != nil {
				res.fail(a, "saving checkpoint: %v", err)
			}
			sleep(eng.Throttle)
		}
		if !it.Closed || !commented {
			continue
		}
		reason := it.CloseReason
		if reason == "" {
			reason = gh.CloseCompleted
		}
		if err := a.Client.CloseIssue(ctx, number, reason); err != nil {
			res.fail(a, "closing #%d: %v", number, err)
			continue
		}
		res.Closed++
		sleep(eng.Throttle)
	}
}
