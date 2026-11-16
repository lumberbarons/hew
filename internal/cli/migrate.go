package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lumberbarons/hew/migrate"
)

// MigrateOpts configure the beads migration.
type MigrateOpts struct {
	// File is the beads issues.jsonl snapshot.
	File string
	// StatePath is where the beadID→issue-number mapping is persisted
	// after every create, making a failed run resumable. Bound to the repository
	// and a digest of the snapshot (#81).
	StatePath string
	// DryRun prints the plan without touching GitHub.
	DryRun bool
	// IncludeClosed migrates closed beads too (created, commented with the
	// close reason, then closed). Real databases are >95% closed, so this
	// is opt-in.
	IncludeClosed bool
	// Throttle is slept between writes to stay under GitHub's secondary
	// rate limits for content creation.
	Throttle time.Duration
}

// MigrateBeads imports a beads snapshot: create in history order, wire
// parents and blockers, then close what was closed. The mapping, its
// checkpoint and the write ordering belong to the public migrate package; the
// snapshot path and its default checkpoint are this command's inputs.
func (a *App) MigrateBeads(ctx context.Context, opts MigrateOpts) error {
	if opts.File == "" {
		return usageErr("usage: hew migrate beads <issues.jsonl> (or --file FILE)")
	}
	if opts.StatePath == "" {
		opts.StatePath = filepath.Join(filepath.Dir(opts.File), "github-migration.json")
	}
	data, err := os.ReadFile(opts.File)
	if err != nil {
		return genericErr("cannot read beads snapshot: %v", err)
	}

	m := migrate.Wrap(a.migrationTarget(), migrate.Config{
		Repo:           a.Repo.String(),
		RepoKey:        a.stateRepo(),
		CheckpointPath: opts.StatePath,
		Throttle:       opts.Throttle,
		DryRun:         opts.DryRun,
		Progress:       a.progressf,
	})
	res, err := m.Beads(ctx, migrate.BeadsRequest{
		Snapshot: data, Source: opts.File, IncludeClosed: opts.IncludeClosed,
	})
	a.printWarnings(res.Warnings)
	if err != nil {
		return err
	}
	if opts.DryRun {
		return a.migrationPlan(res)
	}
	if res.Selected == 0 {
		return a.emitResult(map[string]any{
			"created": 0, "wired": 0, "closed": 0,
			"skippedClosed": res.Skipped, "warnings": warningTexts(res.Warnings),
			"mapping": map[string]int{},
		}, func() {
			a.printf("nothing to migrate (%d closed beads skipped; use --include-closed)\n", res.Skipped)
		})
	}
	return a.emitResult(map[string]any{
		"created": res.Created, "wired": res.Wired, "closed": res.Closed,
		"skippedClosed": res.Skipped, "warnings": warningTexts(res.Warnings),
		"mapping": res.Mapping,
	}, func() {
		a.printf("migrated %d beads: %d created, %d dependencies wired, %d closed", res.Selected, res.Created, res.Wired, res.Closed)
		if res.Skipped > 0 {
			a.printf(" (%d closed beads skipped; use --include-closed)", res.Skipped)
		}
		a.printf("\nmapping saved to %s\n", opts.StatePath)
	})
}

// migrationPlanEntry is one issue a dry run would create, in the shape both
// the text line and the --json result are built from.
type migrationPlanEntry struct {
	ID         string   `json:"id"`
	Priority   string   `json:"priority"`
	Type       *string  `json:"type"`
	Title      string   `json:"title"`
	Parent     *string  `json:"parent"`
	BlockedBy  []string `json:"blockedBy"`
	InProgress bool     `json:"inProgress"`
	ThenClose  bool     `json:"thenClose"`
}

// planEntry describes one planned create in the CLI's rendering shape.
func planEntry(step migrate.PlanStep) migrationPlanEntry {
	entry := migrationPlanEntry{
		ID: step.Key, Priority: step.Priority, Title: step.Title,
		BlockedBy:  append([]string{}, step.BlockedBy...),
		InProgress: step.InProgress, ThenClose: step.ThenClose,
	}
	if step.Type != "" {
		t := step.Type
		entry.Type = &t
	}
	if step.Parent != "" {
		p := step.Parent
		entry.Parent = &p
	}
	return entry
}

// planStep is one compact line of a rendered plan: either a step the
// checkpoint already accounts for or a planned create.
type planStep struct {
	key      string
	migrated int
	entry    migrationPlanEntry
}

// planSteps splits a dry run's plan into its ordered steps and the mapping the
// checkpoint already accounts for.
func planSteps(plan []migrate.PlanStep) (wouldMigrate []migrationPlanEntry, already map[string]int, steps []planStep) {
	wouldMigrate = []migrationPlanEntry{}
	already = map[string]int{}
	for _, step := range plan {
		if step.Migrated != 0 {
			already[step.Key] = step.Migrated
			steps = append(steps, planStep{key: step.Key, migrated: step.Migrated})
			continue
		}
		entry := planEntry(step)
		wouldMigrate = append(wouldMigrate, entry)
		steps = append(steps, planStep{key: step.Key, entry: entry})
	}
	return wouldMigrate, already, steps
}

// migrationPlan reports what a real beads run would do.
func (a *App) migrationPlan(res migrate.Result) error {
	wouldMigrate, already, steps := planSteps(res.Plan)
	return a.emitResult(map[string]any{
		"dryRun": true, "wouldMigrate": wouldMigrate, "alreadyMigrated": already,
		"skippedClosed": res.Skipped,
	}, func() {
		if len(steps) == 0 {
			a.printf("nothing to migrate (%d closed beads skipped; use --include-closed)\n", res.Skipped)
			return
		}
		for _, st := range steps {
			if st.migrated != 0 {
				a.printf("already migrated: %s → #%d\n", st.key, st.migrated)
				continue
			}
			a.printf("%s\n", planLine(st.entry))
		}
		a.printf("dry run: %d beads would be migrated", len(res.Plan))
		if res.Skipped > 0 {
			a.printf(" (%d closed skipped; use --include-closed)", res.Skipped)
		}
		a.printf("\n")
	})
}

// planLine is the text a dry run prints for one issue it would create.
func planLine(item migrationPlanEntry) string {
	line := fmt.Sprintf("create: %s as %s", item.ID, item.Priority)
	if item.Type != nil {
		line += " " + *item.Type
	}
	line += "  " + item.Title
	var marks []string
	if item.Parent != nil {
		marks = append(marks, "parent "+*item.Parent)
	}
	if len(item.BlockedBy) > 0 {
		marks = append(marks, "blocked by "+strings.Join(item.BlockedBy, " "))
	}
	if item.InProgress {
		marks = append(marks, "in progress")
	}
	if item.ThenClose {
		marks = append(marks, "then close")
	}
	if len(marks) > 0 {
		line += "  [" + strings.Join(marks, "; ") + "]"
	}
	return line
}
