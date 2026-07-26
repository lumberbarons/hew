package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lumberbarons/issues/internal/conventions"
	"github.com/lumberbarons/issues/internal/gh"
	"github.com/lumberbarons/issues/internal/plan"
)

// ApplyOpts configure a batch apply.
type ApplyOpts struct {
	// File is the JSONL plan.
	File string
	// StatePath is where the entry-key→issue-number mapping and the edges
	// already wired are persisted after every write, making a failed run
	// resumable and a finished one a quiet no-op. Empty means
	// File + ".state.json".
	StatePath string
	// DryRun prints the plan without touching GitHub.
	DryRun bool
	// Throttle is slept between writes to stay under GitHub's secondary
	// rate limits for content creation.
	Throttle time.Duration
}

// Apply batch-creates issues from a plan file: create in file order,
// checkpointing after every write, then wire parents and blockers — the
// migrate machinery generalized to arbitrary plans, with local ids playing
// the role of bead IDs.
func (a *App) Apply(ctx context.Context, opts ApplyOpts) error {
	if opts.File == "" {
		return usageErr("usage: issues apply <plan.jsonl>")
	}
	if opts.StatePath == "" {
		opts.StatePath = opts.File + ".state.json"
	}
	f, err := os.Open(opts.File)
	if err != nil {
		return genericErr("cannot read plan file: %v", err)
	}
	defer func() { _ = f.Close() }()
	entries, err := plan.Parse(f)
	if err != nil {
		return genericErr("parsing %s: %v", opts.File, err)
	}
	if len(entries) == 0 {
		a.printf("nothing to apply: %s has no entries\n", opts.File)
		return nil
	}

	state, err := loadBatchState(opts.StatePath)
	if err != nil {
		return err
	}

	if opts.DryRun {
		a.applyPlan(entries, state)
		return nil
	}

	if err := a.ensureLabels(ctx, planAreaLabels(entries)); err != nil {
		return err
	}

	created, err := a.applyCreate(ctx, entries, state, opts)
	if err != nil {
		return err
	}
	wired, warned := a.applyWire(ctx, entries, state, opts)

	return a.emitResult(map[string]any{
		"created": created, "wired": wired, "warnings": warned,
		"mapping": state.Mapping,
	}, func() {
		a.printf("applied %s: %d created, %d dependencies wired\n", opts.File, created, wired)
		a.printf("mapping saved to %s\n", opts.StatePath)
	})
}

// applyPlan prints what a real run would do.
func (a *App) applyPlan(entries []plan.Entry, state *batchState) {
	toCreate := 0
	for _, e := range entries {
		if n, ok := state.Mapping[e.Key()]; ok {
			a.printf("already created: %s → #%d\n", e.Key(), n)
			continue
		}
		toCreate++
		line := fmt.Sprintf("create: %s as %s %s  %s", e.Key(), e.Priority, e.Type, applyTitle(e))
		var marks []string
		if len(e.Areas) > 0 {
			marks = append(marks, "areas "+strings.Join(e.Areas, " "))
		}
		if e.Parent != nil {
			marks = append(marks, "parent "+e.Parent.String())
		}
		if len(e.BlockedBy) > 0 {
			refs := make([]string, len(e.BlockedBy))
			for i, b := range e.BlockedBy {
				refs[i] = b.String()
			}
			marks = append(marks, "blocked by "+strings.Join(refs, " "))
		}
		if len(marks) > 0 {
			line += "  [" + strings.Join(marks, "; ") + "]"
		}
		a.printf("%s\n", line)
	}
	a.printf("dry run: %d issues would be created\n", toCreate)
}

func (a *App) applyCreate(ctx context.Context, entries []plan.Entry, state *batchState, opts ApplyOpts) (int, error) {
	created := 0
	for _, e := range entries {
		if n, ok := state.Mapping[e.Key()]; ok {
			a.progressf("already created: %s → #%d\n", e.Key(), n)
			continue
		}
		labels := []string{e.Priority.String()}
		if !e.IsEpic() {
			labels = append(labels, e.Type)
		}
		labels = append(labels, e.Areas...)
		issue, err := a.Client.CreateIssue(ctx, applyTitle(e), applyBody(e), labels)
		if err != nil {
			return created, fmt.Errorf("creating %s (rerun to resume): %w", e.Key(), err)
		}
		state.Mapping[e.Key()] = issue.Number
		if err := saveBatchState(opts.StatePath, state); err != nil {
			return created, err
		}
		created++
		a.progressf("created #%d from %s: %s\n", issue.Number, e.Key(), e.Title)
		sleep(opts.Throttle)
	}
	return created, nil
}

// applyWire connects parents and blockers, checkpointing each edge as it
// lands so a resume skips it. Failures warn and continue, and are retried on
// the next run — but a *successful* edge is never re-attempted, because
// GitHub rejects the duplicate and the warning it produces would drown the
// ones that mean something. Local references need no cycle check here:
// plan.Parse already rejected cycles, and existing issues cannot depend on
// issues that didn't exist yet.
func (a *App) applyWire(ctx context.Context, entries []plan.Entry, state *batchState, opts ApplyOpts) (wired int, warnings []string) {
	warn := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		warnings = append(warnings, msg)
		a.warnf("%s", msg)
	}
	// resolve maps a reference to an issue number: local ids through the
	// checkpoint state, numeric refs as-is.
	resolve := func(r plan.Ref) (int, bool) {
		if r.ID == "" {
			return r.Number, true
		}
		n, ok := state.Mapping[r.ID]
		return n, ok
	}
	for _, e := range entries {
		from, ok := state.Mapping[e.Key()]
		if !ok {
			continue
		}
		for _, edge := range e.Edges() {
			to, ok := resolve(edge.To)
			if !ok {
				warn("%s %s of %s not created, edge dropped", edge.Kind, edge.To, e.Key())
				continue
			}
			key := edgeKey(edge.Kind, from, to)
			if state.Edges[key] {
				continue // wired by an earlier run
			}
			var err error
			if edge.Kind == plan.ParentEdge {
				err = a.Client.AddSubIssue(ctx, to, from, true)
			} else {
				err = a.Client.AddBlockedBy(ctx, from, to)
			}
			if err != nil {
				warn("%s edge #%d→#%d: %v", edge.Kind, from, to, err)
				continue
			}
			state.Edges[key] = true
			if err := saveBatchState(opts.StatePath, state); err != nil {
				warn("%v", err)
			}
			wired++
			sleep(opts.Throttle)
		}
	}
	return wired, warnings
}

// applyTitle prefixes epic entries the same way epic create does.
func applyTitle(e plan.Entry) string {
	if e.IsEpic() && !strings.HasPrefix(e.Title, conventions.EpicTitlePrefix) {
		return conventions.EpicTitlePrefix + e.Title
	}
	return e.Title
}

// applyBody composes structured section fields the same way the create
// section flags do, and appends the discovered-from link.
func applyBody(e plan.Entry) string {
	body := e.Body
	if !e.Sections.IsZero() {
		body = e.Sections.Compose()
	}
	if e.DiscoveredFrom > 0 {
		link := conventions.DiscoveredFrom(e.DiscoveredFrom)
		if body == "" {
			body = link
		} else {
			body = strings.TrimRight(body, "\n") + "\n\n" + link
		}
	}
	return body
}

// planAreaLabels collects every distinct area label the plan uses, for the
// ensureLabels bootstrap.
func planAreaLabels(entries []plan.Entry) []gh.Label {
	var out []gh.Label
	seen := map[string]bool{}
	for _, e := range entries {
		for _, area := range e.Areas {
			if !seen[area] {
				seen[area] = true
				out = append(out, gh.Label{Name: area, Color: "ededed", Description: "created by issues apply"})
			}
		}
	}
	return out
}
