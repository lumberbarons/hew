package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lumberbarons/hew/internal/conventions"
	"github.com/lumberbarons/hew/internal/plan"
	"github.com/lumberbarons/hew/tracker"
)

// ApplyOpts configure a batch apply.
type ApplyOpts struct {
	// File is the JSONL plan.
	File string
	// StatePath is where the entry-key→issue-number mapping and the edges
	// already wired are persisted after every write, making a failed run
	// resumable and an unchanged finished one a quiet no-op. Bound to the
	// repository and a digest of the plan, so an edited plan or a state file
	// from elsewhere is refused rather than trusted (#81). Empty means
	// File + ".state.json".
	StatePath string
	// DryRun prints the plan without creating anything. It still reads: the
	// state file's issues and every existing issue the plan references are
	// looked up, so it shows the refusal the real run would hit.
	DryRun bool
	// Throttle is slept between writes to stay under GitHub's secondary
	// rate limits for content creation.
	Throttle time.Duration
}

// Apply batch-creates issues from a plan file: create in file order,
// checkpointing after every write, then wire parents and blockers — the
// migrate machinery generalized to arbitrary plans, with local ids playing
// the role of bead IDs. The execution itself lives in tracker.ApplyPlan; this
// command parses the file, validates the flags' extra rules and renders the
// result.
func (a *App) Apply(ctx context.Context, opts ApplyOpts) error {
	if opts.File == "" {
		return usageErr("usage: hew apply <plan.jsonl> (or --file FILE)")
	}
	if opts.StatePath == "" {
		opts.StatePath = opts.File + ".state.json"
	}
	data, err := os.ReadFile(opts.File)
	if err != nil {
		return genericErr("cannot read plan file: %v", err)
	}
	entries, err := plan.Parse(bytes.NewReader(data))
	if err != nil {
		return genericErr("parsing %s: %v", opts.File, err)
	}
	if len(entries) == 0 {
		// An empty plan still answers in the shape the mode promises: a dry
		// run keyed on dryRun/wouldCreate must parse this as it parses any other.
		result := map[string]any{
			"created": 0, "wired": 0, "warnings": []string{},
			"mapping": map[string]int{},
		}
		if opts.DryRun {
			result = map[string]any{
				"dryRun": true, "wouldCreate": []tracker.PlannedEntry{},
				"alreadyCreated": map[string]int{},
			}
		}
		return a.emitResult(result, func() {
			a.printf("nothing to apply: %s has no entries\n", opts.File)
		})
	}

	digest := fileDigest(data)
	checkpoint, err := tracker.OpenFileCheckpoint(opts.StatePath, a.stateRepo(), digest, "plan")
	if err != nil {
		return err
	}
	// Reported first, so the plan can be fixed while fixing it is still free.
	a.warnPlanCodeText(entries)

	result, err := tracker.WrapAs(a.Client, a.applyBackend(), "").ApplyPlan(ctx, entries, tracker.ApplyOptions{
		DryRun:     opts.DryRun,
		Throttle:   opts.Throttle,
		Checkpoint: checkpoint,
		Digest:     digest,
	})
	a.printApplyProgress(entries, result.Applied)
	for _, warning := range result.Warnings {
		a.warnf("%s", warning)
	}
	if err != nil {
		// ApplyPlan owns the title and area rules now, including the check
		// that an already-created entry is not re-gated and the idea area
		// reservation on pending entries. A rejected plan is bad input, so it
		// keeps the apply command's usage exit code.
		var invalid *tracker.InvalidInputError
		if errors.As(err, &invalid) && invalid.Field == "plan" {
			return usageErr("%s", invalid.Reason)
		}
		return err
	}

	if result.DryRun {
		return a.emitApplyDryRun(entries, result)
	}
	if err := a.emitResult(map[string]any{
		"created": result.Created, "wired": result.Wired, "warnings": result.Warnings,
		"mapping": result.Mapping,
	}, func() {
		a.printf("applied %s: %d created, %d dependencies wired\n", opts.File, result.Created, result.Wired)
		a.printf("mapping saved to %s\n", opts.StatePath)
	}); err != nil {
		return err
	}
	// The issues exist and the result above says so, but an edge that did not
	// land is a plan that was not applied: a script branches on the exit code,
	// not on stderr (#233). The edge is not checkpointed, so a rerun retries it.
	switch {
	case result.Unwired == 1:
		return genericErr("1 dependency edge was not wired (see warnings): fix the cause and rerun to retry it")
	case result.Unwired > 1:
		return genericErr("%d dependency edges were not wired (see warnings): fix the cause and rerun to retry them", result.Unwired)
	}
	return nil
}

// applyBackend reports the backend the open client came from, for the tracker
// seam.
func (a *App) applyBackend() tracker.Backend {
	if a.Local {
		return tracker.BackendLocal
	}
	return tracker.BackendGitHub
}

// printApplyProgress narrates the create pass in file order: either the issue
// a pending entry became, or the one its checkpoint already maps.
func (a *App) printApplyProgress(entries []plan.Entry, applied []tracker.AppliedEntry) {
	titles := make(map[string]string, len(entries))
	for _, e := range entries {
		titles[e.Key()] = applyTitle(e)
	}
	for _, entry := range applied {
		if entry.Created {
			a.progressf("created #%d from %s: %s\n", entry.Issue, entry.Key, titles[entry.Key])
			continue
		}
		a.progressf("already created: %s → #%d\n", entry.Key, entry.Issue)
	}
}

// emitApplyDryRun reports what a real run would do, in file order: a line per
// entry, either already created or the planned issue with its edges.
func (a *App) emitApplyDryRun(entries []plan.Entry, result tracker.ApplyResult) error {
	planned := make(map[string]tracker.PlannedEntry, len(result.WouldCreate))
	for _, item := range result.WouldCreate {
		planned[item.Key] = item
	}
	return a.emitResult(map[string]any{
		"dryRun": true, "wouldCreate": result.WouldCreate, "alreadyCreated": result.AlreadyCreated,
	}, func() {
		for _, e := range entries {
			if n, ok := result.AlreadyCreated[e.Key()]; ok {
				a.printf("already created: %s → #%d\n", e.Key(), n)
				continue
			}
			item, ok := planned[e.Key()]
			if !ok {
				continue
			}
			line := fmt.Sprintf("create: %s as %s %s  %s", item.Key, item.Priority, item.Type, item.Title)
			var marks []string
			if len(item.Areas) > 0 {
				marks = append(marks, "areas "+strings.Join(item.Areas, " "))
			}
			if item.Parent != nil {
				marks = append(marks, "parent "+*item.Parent)
			}
			if len(item.BlockedBy) > 0 {
				marks = append(marks, "blocked by "+strings.Join(item.BlockedBy, " "))
			}
			if len(marks) > 0 {
				line += "  [" + strings.Join(marks, "; ") + "]"
			}
			a.printf("%s\n", line)
		}
		a.printf("dry run: %d issues would be created\n", len(result.WouldCreate))
	})
}

// applyTitle prefixes epic entries the same way epic create does.
func applyTitle(e plan.Entry) string { return tracker.PlanTitle(e) }

// warnPlanCodeText reports code-shaped text left as prose, per entry, so a
// plan naming twenty files says which entry to fix rather than which token.
// The check is the library's; the entry prefix is the plan file's.
func (a *App) warnPlanCodeText(entries []plan.Entry) {
	for _, e := range entries {
		check, err := tracker.CheckBody(tracker.PlanBody(e))
		if err != nil {
			continue
		}
		tokens := unmarkedCode(check.Findings)
		if len(tokens) == 0 {
			continue
		}
		a.warnf("%s: not in code spans: %s — wrap them (and any command around them) in backticks",
			e.Key(), conventions.FormatUnmarkedCodeText(tokens))
	}
}

// applyBodyWithProvenance is the body ApplyPlan writes: the composed body plus
// the marker binding the issue to this entry and this plan file. Kept here for
// tests that seed a checkpoint's issue exactly as the applier would.
func applyBodyWithProvenance(e plan.Entry, digest string) string {
	return tracker.PlanBodyWithProvenance(e, digest)
}
