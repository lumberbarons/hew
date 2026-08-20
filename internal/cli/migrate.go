package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lumberbarons/hew/internal/beads"
	"github.com/lumberbarons/hew/internal/conventions"
	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/model"
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

// beadTypeLabels maps bead issue types to convention type labels; epics
// map to no type label (epic-ness is having sub-issues).
var beadTypeLabels = map[string]string{
	"bug":     "bug",
	"feature": "enhancement",
	"task":    "task",
	"chore":   "task",
	"epic":    "",
}

// MigrateBeads imports a beads snapshot as GitHub issues: create in
// history order, wire parents and blockers, then close what was closed.
func (a *App) MigrateBeads(ctx context.Context, opts MigrateOpts) error {
	if opts.File == "" {
		return usageErr("usage: hew migrate beads --file <issues.jsonl>")
	}
	if opts.StatePath == "" {
		opts.StatePath = filepath.Join(filepath.Dir(opts.File), "github-migration.json")
	}
	data, err := os.ReadFile(opts.File)
	if err != nil {
		return genericErr("cannot read beads snapshot: %v", err)
	}
	all, err := beads.Parse(bytes.NewReader(data))
	if err != nil {
		return genericErr("parsing %s: %v", opts.File, err)
	}

	var selected []beads.Bead
	skippedClosed := 0
	for _, b := range all {
		switch b.Status {
		case "open", "in_progress":
			selected = append(selected, b)
		case "closed":
			if opts.IncludeClosed {
				selected = append(selected, b)
			} else {
				skippedClosed++
			}
		default:
			a.warnf("%s has unknown status %q; migrating as open", b.ID, b.Status)
			selected = append(selected, b)
		}
	}
	if len(selected) == 0 {
		a.printf("nothing to migrate (%d closed beads skipped; use --include-closed)\n", skippedClosed)
		return nil
	}

	digest := fileDigest(data)
	state, err := loadBatchState(opts.StatePath, a.Repo.String(), digest, "snapshot")
	if err != nil {
		return err
	}
	// Validated against every bead in the snapshot, not just the selected
	// ones: --include-closed changes the selection but not the file, so a
	// mapping written by a run with the flag must not read as an unknown ID
	// on a run without it.
	validIDs := make(map[string]bool, len(all))
	for _, b := range all {
		validIDs[b.ID] = true
	}

	// Reads GitHub, writes nothing — so the dry run makes the same pass and
	// can show the failure the real run would hit (#81).
	if err := a.verifyBatchState(ctx, state, conventions.ProvenanceMigrate, validIDs, "bead ID"); err != nil {
		return err
	}

	if opts.DryRun {
		a.migrationPlan(selected, state.Mapping, skippedClosed)
		return nil
	}

	if err := a.ensureLabels(ctx, beadAreaLabels(selected)); err != nil {
		return err
	}

	viewer := ""
	created, err := a.migrateCreate(ctx, selected, state, opts, &viewer)
	if err != nil {
		return err
	}
	wired, warned := a.migrateWire(ctx, selected, state.Mapping, opts)
	closed := a.migrateClose(ctx, selected, state, opts)

	return a.emitResult(map[string]any{
		"created": created, "wired": wired, "closed": closed,
		"skippedClosed": skippedClosed, "warnings": warned,
		"mapping": state.Mapping,
	}, func() {
		a.printf("migrated %d beads: %d created, %d dependencies wired, %d closed", len(selected), created, wired, closed)
		if skippedClosed > 0 {
			a.printf(" (%d closed beads skipped; use --include-closed)", skippedClosed)
		}
		a.printf("\nmapping saved to %s\n", opts.StatePath)
	})
}

// migrationPlan prints what a real run would do.
func (a *App) migrationPlan(selected []beads.Bead, state map[string]int, skippedClosed int) {
	for _, b := range selected {
		if n, ok := state[b.ID]; ok {
			a.printf("already migrated: %s → #%d\n", b.ID, n)
			continue
		}
		line := fmt.Sprintf("create: %s as %s", b.ID, model.Priority(clampPriority(b.Priority)))
		if t := beadTypeLabels[b.IssueType]; t != "" {
			line += " " + t
		} else if b.IssueType == "epic" {
			line += " epic"
		}
		line += "  " + b.Title
		var marks []string
		if p := b.Parent(); p != "" {
			marks = append(marks, "parent "+p)
		}
		if blockers := b.BlockedBy(); len(blockers) > 0 {
			marks = append(marks, "blocked by "+strings.Join(blockers, " "))
		}
		if b.Status == "in_progress" {
			marks = append(marks, "in progress")
		}
		if b.Closed() {
			marks = append(marks, "then close")
		}
		if len(marks) > 0 {
			line += "  [" + strings.Join(marks, "; ") + "]"
		}
		a.printf("%s\n", line)
	}
	a.printf("dry run: %d beads would be migrated", len(selected))
	if skippedClosed > 0 {
		a.printf(" (%d closed skipped; use --include-closed)", skippedClosed)
	}
	a.printf("\n")
}

// beadAreaLabels collects every distinct area label the beads carry, for
// the ensureLabels bootstrap.
func beadAreaLabels(selected []beads.Bead) []gh.Label {
	var out []gh.Label
	seen := map[string]bool{}
	for _, b := range selected {
		for _, area := range areaLabels(b.Labels) {
			if !seen[area] {
				seen[area] = true
				out = append(out, gh.Label{Name: area, Color: "ededed", Description: "migrated from beads"})
			}
		}
	}
	return out
}

func (a *App) migrateCreate(ctx context.Context, selected []beads.Bead, state *batchState, opts MigrateOpts, viewer *string) (int, error) {
	created := 0
	for _, b := range selected {
		if n, ok := state.Mapping[b.ID]; ok {
			a.progressf("already migrated: %s → #%d\n", b.ID, n)
			continue
		}
		labels := []string{model.Priority(clampPriority(b.Priority)).String()}
		if t := beadTypeLabels[b.IssueType]; t != "" {
			labels = append(labels, t)
		}
		if b.Status == "in_progress" {
			labels = append(labels, model.InProgressLabel)
		}
		labels = append(labels, areaLabels(b.Labels)...)

		title := b.Title
		if b.IssueType == "epic" && !strings.HasPrefix(title, conventions.EpicTitlePrefix) {
			title = conventions.EpicTitlePrefix + title
		}

		issue, err := a.Client.CreateIssue(ctx, title, beadBodyWithProvenance(b, state.Digest), labels)
		if err != nil {
			return created, fmt.Errorf("creating %s (rerun to resume): %w", b.ID, err)
		}
		state.Mapping[b.ID] = issue.Number
		if err := saveBatchState(opts.StatePath, state); err != nil {
			return created, err
		}
		created++
		a.progressf("created #%d from %s: %s\n", issue.Number, b.ID, b.Title)
		if b.Status == "in_progress" {
			if *viewer == "" {
				v, err := a.Client.Viewer(ctx)
				if err != nil {
					return created, err
				}
				*viewer = v
			}
			if err := a.Client.AddAssignee(ctx, issue.Number, *viewer); err != nil {
				a.warnf("assigning #%d: %v", issue.Number, err)
			}
		}
		sleep(opts.Throttle)
	}
	return created, nil
}

// migrateWire connects parents and blockers. Failures warn and continue —
// on resume the edges are retried, and a duplicate edge is harmless.
func (a *App) migrateWire(ctx context.Context, selected []beads.Bead, state map[string]int, opts MigrateOpts) (wired int, warnings []string) {
	warn := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		warnings = append(warnings, msg)
		a.warnf("%s", msg)
	}
	edge := func(fromBead, toBead, kind string, connect func(from, to int) error) {
		from, ok := state[fromBead]
		if !ok {
			return
		}
		to, ok := state[toBead]
		if !ok {
			warn("%s of %s not migrated, %s edge dropped", toBead, fromBead, kind)
			return
		}
		if err := connect(from, to); err != nil {
			warn("%s edge #%d→#%d: %v", kind, from, to, err)
			return
		}
		wired++
		sleep(opts.Throttle)
	}
	for _, b := range selected {
		if p := b.Parent(); p != "" {
			edge(b.ID, p, "parent", func(child, parent int) error {
				return a.Client.AddSubIssue(ctx, parent, child, true)
			})
		}
		for _, blocker := range b.BlockedBy() {
			edge(b.ID, blocker, "blocked-by", func(issue, blocker int) error {
				return a.Client.AddBlockedBy(ctx, issue, blocker)
			})
		}
	}
	return wired, warnings
}

// migrateClose closes migrated beads that were closed, commenting the
// close reason first. Tolerant: re-closing on resume just warns.
//
// The provenance check repeats here rather than trusting the up-front pass:
// commenting and closing is the most destructive thing migrate does, and the
// mapping it reads includes entries this run added after that pass. Failing
// closed on a mapping that cannot be vouched for skips one bead instead of
// aborting a migration that is already part-written (#81).
func (a *App) migrateClose(ctx context.Context, selected []beads.Bead, state *batchState, opts MigrateOpts) int {
	closed := 0
	for _, b := range selected {
		if !b.Closed() {
			continue
		}
		number, ok := state.Mapping[b.ID]
		if !ok {
			continue
		}
		issue, err := a.Client.GetIssue(ctx, number)
		if err != nil {
			a.warnf("closing #%d: %v", number, err)
			continue
		}
		if !issue.IsOpen() {
			continue
		}
		if !conventions.HasProvenanceMarker(issue.Body, conventions.ProvenanceMigrate, b.ID, state.Digest) {
			a.warnf("not closing #%d: it does not carry the hew provenance marker for bead %s from this snapshot", number, b.ID)
			continue
		}
		if b.CloseReason != "" {
			if err := a.Client.Comment(ctx, number, b.CloseReason); err != nil {
				a.warnf("close comment on #%d: %v", number, err)
			}
		}
		if err := a.Client.CloseIssue(ctx, number, gh.CloseCompleted); err != nil {
			a.warnf("closing #%d: %v", number, err)
			continue
		}
		closed++
		sleep(opts.Throttle)
	}
	return closed
}

// beadBodyWithProvenance is what actually gets written: the composed body
// plus the marker binding the issue to this bead and this snapshot file.
// The human-readable footer below is prose and stays prose — a person may
// reword or delete it — so verification reads only the marker (#81).
func beadBodyWithProvenance(b beads.Bead, digest string) string {
	marker := conventions.ProvenanceMarker(conventions.ProvenanceMigrate, b.ID, digest)
	return beadBody(b) + "\n\n" + marker
}

// beadBody assembles the issue body from the bead's prose fields, with a
// provenance footer carrying what GitHub can't store natively.
func beadBody(b beads.Bead) string {
	var parts []string
	if s := strings.TrimSpace(b.Description); s != "" {
		parts = append(parts, s)
	}
	if s := strings.TrimSpace(b.Design); s != "" {
		parts = append(parts, "### Design\n\n"+s)
	}
	if s := strings.TrimSpace(b.AcceptanceCriteria); s != "" {
		parts = append(parts, "### Done when\n\n"+s)
	}
	if s := strings.TrimSpace(b.Notes); s != "" {
		parts = append(parts, "### Notes\n\n"+s)
	}
	footer := fmt.Sprintf("Migrated from beads `%s` (created %s", b.ID, b.CreatedAt.Format("2006-01-02"))
	if b.ClosedAt != nil {
		footer += ", closed " + b.ClosedAt.Format("2006-01-02")
	}
	footer += ")"
	parts = append(parts, "---\n"+footer)
	return strings.Join(parts, "\n\n")
}

// areaLabels filters bead labels that would collide with the convention
// labels (priority, type, in-progress) — those are derived, not copied.
func areaLabels(labels []string) []string {
	var out []string
	for _, l := range labels {
		if _, isPriority := model.ParsePriority(l); isPriority || model.IsType(l) || l == model.InProgressLabel {
			continue
		}
		out = append(out, l)
	}
	return out
}

func clampPriority(p int) int {
	if p < 0 {
		return 0
	}
	if p > 4 {
		return 4
	}
	return p
}
