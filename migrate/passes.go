package migrate

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/lumberbarons/hew/internal/conventions"
	"github.com/lumberbarons/hew/internal/model"
	"github.com/lumberbarons/hew/tracker"
)

// create files every item not already in the checkpoint. A create failure
// aborts: the checkpoint makes the rerun resume, and continuing would file
// items whose relationships cannot be established.
func (m *Migrator) create(ctx context.Context, items []Item, opts Options, digest string, res *Result) (int, error) {
	created := 0
	viewer := ""
	for _, it := range items {
		if err := ctx.Err(); err != nil {
			return created, err
		}
		if n, ok := m.checkpoint.Created(it.Key); ok {
			progress(opts, "already migrated: %s → #%d\n", it.Key, n)
			continue
		}
		body := it.Body + "\n\n" + conventions.ProvenanceMarker(opts.Kind, it.Key, digest)
		number, err := m.target.Create(ctx, CreateRequest{Title: it.Title, Body: body, Labels: it.Labels})
		if err != nil {
			return created, fmt.Errorf("creating %s (rerun to resume): %w", it.Key, err)
		}
		if err := m.checkpoint.RecordCreated(it.Key, number); err != nil {
			return created, err
		}
		created++
		progress(opts, "created #%d from %s: %s\n", number, it.Key, it.Title)
		for _, login := range it.Assignees {
			if err := ctx.Err(); err != nil {
				return created, err
			}
			if login == AssigneeViewer {
				if viewer == "" {
					v, err := m.target.Viewer(ctx)
					if err != nil {
						return created, err
					}
					viewer = v
				}
				login = viewer
			}
			if err := m.target.AddAssignee(ctx, number, login); err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return created, ctxErr
				}
				res.note(WarnWriteFailed, it.Key, number, "assigning #%d: %v", number, err)
			}
		}
		if err := wait(ctx, opts.Throttle); err != nil {
			return created, err
		}
	}
	return created, nil
}

// wire connects parents and blockers. Failures warn and continue and are
// retried on resume. Each edge is checkpointed once wired, because a backend
// answers a duplicate edge with an error, and a resume that warned about edges
// it had already made would never read as complete.
//
// A dependency cycle removes all its members from ready, and GitHub accepts
// cycles longer than two issues, so every blocked-by edge is checked against
// the edges accepted so far before it is wired; an edge that would close a
// cycle is dropped with a warning. The graph is the items' own: only they can
// reference each other, and replaying the items in order on a resume rebuilds
// the same graph. An edge enters the graph once it passes the check, even if
// wiring it then fails, because a resume may find it already on the target —
// missing it would hide a cycle.
func (m *Migrator) wire(ctx context.Context, items []Item, opts Options, res *Result) error {
	graph := make([]model.Issue, 0, len(items))
	index := make(map[int]int, len(items))
	for _, it := range items {
		if n, ok := m.checkpoint.Created(it.Key); ok {
			index[n] = len(graph)
			graph = append(graph, model.Issue{Number: n, State: "OPEN"})
		}
	}

	edge := func(fromKey, toKey string, kind tracker.EdgeKind, connect func(from, to int) error) error {
		from, ok := m.checkpoint.Created(fromKey)
		if !ok {
			return nil
		}
		to, ok := m.checkpoint.Created(toKey)
		if !ok {
			res.note(WarnEdgeDropped, fromKey, from, "%s of %s not migrated, %s edge dropped", toKey, fromKey, kind)
			return nil
		}
		if kind == tracker.BlockedByEdge {
			check := model.CheckBlockedBy(graph, from, to)
			if check.Cycle != nil {
				res.note(WarnEdgeDropped, fromKey, from, "blocked-by edge #%d→#%d dropped: would create dependency cycle %s", from, to, formatCycle(check.Cycle))
				return nil
			}
			i := index[from]
			graph[i].BlockedBy = append(graph[i].BlockedBy, model.Ref{Number: to, State: "OPEN"})
			graph[i].BlockedByTotal = len(graph[i].BlockedBy)
		}
		if m.checkpoint.Wired(kind, from, to) {
			return nil
		}
		if err := connect(from, to); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			res.fail(WarnWriteFailed, fromKey, from, "%s edge #%d→#%d: %v", kind, from, to, err)
			return nil
		}
		if err := m.checkpoint.RecordWired(kind, from, to); err != nil {
			res.fail(WarnWriteFailed, fromKey, from, "saving checkpoint: %v", err)
		}
		res.Wired++
		return wait(ctx, opts.Throttle)
	}

	for _, it := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		if it.Parent != "" {
			if err := edge(it.Key, it.Parent, tracker.ParentEdge, func(child, parent int) error {
				return m.target.AddSubIssue(ctx, parent, child)
			}); err != nil {
				return err
			}
		}
		for _, blocker := range it.BlockedBy {
			if err := edge(it.Key, blocker, tracker.BlockedByEdge, func(issue, blocker int) error {
				return m.target.AddBlockedBy(ctx, issue, blocker)
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// relink rewrites the references in each created issue's title, body and
// comments now that every item has a number. It only edits text still exactly
// as the create wrote it: one a person has changed since is left alone with a
// warning, and one already rewritten is skipped, so a resume is quiet.
func (m *Migrator) relink(ctx context.Context, items []Item, opts Options, digest string, res *Result) error {
	if opts.Relink == nil {
		return nil
	}
	mapping := m.mapping()
	for _, it := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		number, ok := m.checkpoint.Created(it.Key)
		if !ok {
			continue
		}
		marker := conventions.ProvenanceMarker(opts.Kind, it.Key, digest)
		original := it.Body + "\n\n" + marker
		rewritten := opts.Relink(it.Body, mapping) + "\n\n" + marker
		title := opts.Relink(it.Title, mapping)
		if rewritten == original && title == it.Title {
			continue
		}
		issue, err := m.target.Get(ctx, number)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			res.fail(WarnWriteFailed, it.Key, number, "relinking #%d: %v", number, err)
			continue
		}
		edited := false
		if rewritten != original {
			switch strings.TrimSpace(issue.Body) {
			case strings.TrimSpace(rewritten):
			case strings.TrimSpace(original):
				if err := m.target.EditBody(ctx, number, rewritten); err != nil {
					if ctxErr := ctx.Err(); ctxErr != nil {
						return ctxErr
					}
					res.fail(WarnWriteFailed, it.Key, number, "relinking #%d: %v", number, err)
					continue
				}
				edited = true
			default:
				res.note(WarnBodyEdited, it.Key, number, "not relinking #%d: its body changed since it was created, so references to other %ss are left as written", number, opts.ItemNoun)
			}
		}
		if title != it.Title {
			switch issue.Title {
			case title:
			case it.Title:
				if err := m.target.EditTitle(ctx, number, title); err != nil {
					if ctxErr := ctx.Err(); ctxErr != nil {
						return ctxErr
					}
					res.fail(WarnWriteFailed, it.Key, number, "relinking the title of #%d: %v", number, err)
					continue
				}
				edited = true
			default:
				res.note(WarnBodyEdited, it.Key, number, "not relinking the title of #%d: it changed since it was created", number)
			}
		}
		if edited {
			res.Relinked++
			if err := wait(ctx, opts.Throttle); err != nil {
				return err
			}
		}
	}
	return nil
}

// commentKey identifies one posted comment in the checkpoint's record of named
// steps.
func commentKey(itemKey string, index int) string {
	return fmt.Sprintf("comment:%s:%d", itemKey, index)
}

// finish posts the items' comments, in order, then closes the ones marked
// closed. Tolerant: an issue already closed (a resume of a finished run) is
// skipped silently, and a failed call warns and moves on.
//
// The provenance check repeats here rather than trusting the up-front pass:
// commenting and closing is the most destructive thing a migration does, and
// the mapping includes entries this run added after that pass. Failing closed
// on a mapping that cannot be vouched for skips one item instead of aborting a
// migration that is already part-written (#81).
func (m *Migrator) finish(ctx context.Context, items []Item, opts Options, digest string, res *Result) error {
	mapping := m.mapping()
	for _, it := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !it.Closed && len(it.Comments) == 0 {
			continue
		}
		number, ok := m.checkpoint.Created(it.Key)
		if !ok {
			continue
		}
		issue, err := m.target.Get(ctx, number)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			res.fail(WarnWriteFailed, it.Key, number, "closing #%d: %v", number, err)
			continue
		}
		if it.Closed && !strings.EqualFold(issue.State, "OPEN") {
			continue
		}
		if !conventions.HasProvenanceMarker(issue.Body, opts.Kind, it.Key, digest) {
			res.fail(WarnWriteFailed, it.Key, number, "not closing #%d: it does not carry the hew provenance marker for %s %s from this %s", number, opts.ItemNoun, it.Key, opts.SourceNoun)
			continue
		}
		commented := true
		for i, comment := range it.Comments {
			if err := ctx.Err(); err != nil {
				return err
			}
			key := commentKey(it.Key, i)
			if m.checkpoint.Recorded(key) {
				continue
			}
			if opts.Relink != nil {
				comment = opts.Relink(comment, mapping)
			}
			if err := m.target.Comment(ctx, number, comment); err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return ctxErr
				}
				res.fail(WarnWriteFailed, it.Key, number, "comment on #%d: %v", number, err)
				// Later comments would land out of order, and closing now would
				// stop a rerun from posting the rest; leave both for the rerun.
				commented = false
				break
			}
			if err := m.checkpoint.RecordStep(key); err != nil {
				res.fail(WarnWriteFailed, it.Key, number, "saving checkpoint: %v", err)
			}
			if err := wait(ctx, opts.Throttle); err != nil {
				return err
			}
		}
		if !it.Closed || !commented {
			continue
		}
		reason := it.CloseReason
		if reason == "" {
			reason = CloseCompleted
		}
		if err := m.target.Close(ctx, number, reason); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			res.fail(WarnWriteFailed, it.Key, number, "closing #%d: %v", number, err)
			continue
		}
		res.Closed++
		if err := wait(ctx, opts.Throttle); err != nil {
			return err
		}
	}
	return nil
}

// progress hands one narration line to the caller's renderer, if it has one.
func progress(opts Options, format string, args ...any) {
	if opts.Progress != nil {
		opts.Progress(format, args...)
	}
}

// wait sleeps between writes without hiding cancellation: a canceled run stops
// at the throttle instead of finishing the delay first.
func wait(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// formatCycle renders a cycle path the way the CLI's other diagnostics do.
func formatCycle(path []int) string {
	parts := make([]string, len(path))
	for i, n := range path {
		parts[i] = "#" + strconv.Itoa(n)
	}
	return strings.Join(parts, " → ")
}

// mapping is the checkpoint's key → number record.
func (m *Migrator) mapping() map[string]int {
	entries := m.checkpoint.CreatedEntries()
	out := make(map[string]int, len(entries))
	for _, entry := range entries {
		out[entry.Key] = entry.Number
	}
	return out
}

// plan is the dry run's plan: every item in source order, marked with the
// checkpoint's issue when it already has one, plus the planned creates and the
// mapping on their own.
func (m *Migrator) plan(items []Item) (steps []PlanStep, would []PlannedItem, already map[string]int) {
	already = m.mapping()
	steps = make([]PlanStep, 0, len(items))
	would = make([]PlannedItem, 0, len(items))
	for _, it := range items {
		if n, ok := already[it.Key]; ok {
			steps = append(steps, PlanStep{Key: it.Key, Migrated: n})
			continue
		}
		step := plannedStep(it)
		steps = append(steps, step)
		would = append(would, PlannedItem{Key: it.Key, Title: it.Title})
	}
	return steps, would, already
}

// plannedStep describes the issue one item would create, in the terms a plan
// renders: the effective priority and type its labels carry, its edges by key,
// and whether it is claimed or closed.
func plannedStep(it Item) PlanStep {
	view := model.Issue{Labels: it.Labels}
	step := PlanStep{
		Key: it.Key, Title: it.Title, Parent: it.Parent,
		BlockedBy:  append([]string(nil), it.BlockedBy...),
		InProgress: view.InProgress(), ThenClose: it.Closed,
	}
	// An untriaged item renders P?, the same placeholder the list commands use;
	// the plan should not claim it has no priority at all.
	priority, _ := view.Priority()
	step.Priority = priority.String()
	if typ, _ := view.Type(); typ != "" {
		step.Type = typ
	}
	if it.Epic {
		// Epic-ness is a marker rather than a type label: a plan names it so
		// the reader can tell an epic from an untyped issue.
		step.Type = "epic"
	}
	return step
}
