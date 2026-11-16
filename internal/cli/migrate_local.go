package cli

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/lumberbarons/hew/internal/local"
	"github.com/lumberbarons/hew/migrate"
)

// MigrateLocalOpts configure the local-to-GitHub migration.
type MigrateLocalOpts struct {
	// Dir is the local tracker directory (.hew at the main checkout).
	Dir string
	// StatePath is where the local number → GitHub number checkpoint is kept
	// between a failed run and its rerun; default <Dir>/migration-state.json.
	// Bound to the repository and to a digest of issues.jsonl.
	StatePath string
	// DryRun prints the plan without touching GitHub or the tracker.
	DryRun bool
	// Throttle is slept between writes to stay under GitHub's secondary
	// rate limits for content creation.
	Throttle time.Duration
}

// MigrateLocal moves every issue in the local tracker — closed ones too — to
// the GitHub repository a.Client talks to, then flips the tracker's backend to
// GitHub and keeps issues.jsonl as an archive. The freeze, the checkpoint, the
// reference rewriting and the seal belong to the public migrate package; the
// tracker directory and its default checkpoint are this command's inputs.
func (a *App) MigrateLocal(ctx context.Context, opts MigrateLocalOpts) error {
	if opts.Dir == "" {
		return usageErr("usage: hew migrate local")
	}
	if opts.StatePath == "" {
		opts.StatePath = filepath.Join(opts.Dir, "migration-state.json")
	}

	m := migrate.Wrap(a.migrationTarget(), migrate.Config{
		Repo:           a.Repo.String(),
		RepoKey:        a.stateRepo(),
		CheckpointPath: opts.StatePath,
		Throttle:       opts.Throttle,
		DryRun:         opts.DryRun,
		Progress:       a.progressf,
	})
	res, err := m.Local(ctx, migrate.LocalRequest{Dir: opts.Dir})
	a.printWarnings(res.Warnings)
	if err != nil {
		// A targetless dry run cannot vouch for a checkpoint, and the remedy is
		// the flag that names a repository: the library names the fact, the
		// adapter names the flag.
		var checkpoint *migrate.CheckpointError
		if errors.As(err, &checkpoint) {
			return usageErr("%s holds a checkpoint from an earlier run, and a dry run can only check it against GitHub: pass --repo", checkpoint.Path)
		}
		return err
	}
	if opts.DryRun {
		return a.localMigrationPlan(res)
	}
	return a.emitResult(map[string]any{
		"created": res.Created, "wired": res.Wired, "closed": res.Closed, "relinked": res.Relinked,
		"warnings": warningTexts(res.Warnings), "mapping": res.Mapping, "backend": string(res.Backend),
	}, func() {
		a.printf("migrated %d local issues to %s: %d created, %d dependencies wired, %d closed, %d bodies relinked\n",
			res.Selected, a.Repo, res.Created, res.Wired, res.Closed, res.Relinked)
		a.printf("number map saved to %s; this repository now uses GitHub, and %s is kept as an archive\n",
			filepath.Join(opts.Dir, local.MigratedFile), filepath.Join(opts.Dir, "issues.jsonl"))
	})
}

// planLineLimit is how many per-issue lines a text dry run prints before it
// summarises instead; --json always lists every issue.
const planLineLimit = 20

// localMigrationPlan reports what a real local run would do, in the same shape
// `migrate beads --dry-run` uses.
func (a *App) localMigrationPlan(res migrate.Result) error {
	wouldMigrate, already, steps := planSteps(res.Plan)
	return a.emitResult(map[string]any{
		"dryRun": true, "wouldMigrate": wouldMigrate, "alreadyMigrated": already,
	}, func() {
		if len(steps) > planLineLimit {
			closing := 0
			for _, e := range wouldMigrate {
				if e.ThenClose {
					closing++
				}
			}
			a.printf("dry run: %d local issues would be migrated (%d would be closed after creation); %d already migrated\n",
				len(wouldMigrate), closing, len(already))
			a.printf("per-issue lines are omitted for plans over %d issues; use --json for the full plan\n", planLineLimit)
			return
		}
		for _, st := range steps {
			if st.migrated != 0 {
				a.printf("already migrated: %s → #%d\n", st.key, st.migrated)
				continue
			}
			a.printf("%s\n", planLine(st.entry))
		}
		a.printf("dry run: %d local issues would be migrated\n", len(res.Plan))
	})
}

// migrationTarget is the App's store as a migrate.Target, or nil when the
// command is a targetless dry run. A nil interface is what the library reads
// as "plan from the source alone", so a wrapped nil store must not hide it.
func (a *App) migrationTarget() migrate.Target {
	if a.Client == nil {
		return nil
	}
	return migrationTarget{a.Client}
}
