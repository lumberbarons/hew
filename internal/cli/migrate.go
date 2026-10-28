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
		result := map[string]any{
			"created": 0, "wired": 0, "closed": 0,
			"skippedClosed": skippedClosed, "warnings": []string{},
			"mapping": map[string]int{},
		}
		if opts.DryRun {
			result = map[string]any{
				"dryRun": true, "wouldMigrate": []migrationPlanEntry{},
				"alreadyMigrated": map[string]int{}, "skippedClosed": skippedClosed,
			}
		}
		return a.emitResult(result, func() {
			a.printf("nothing to migrate (%d closed beads skipped; use --include-closed)\n", skippedClosed)
		})
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
		return a.migrationPlan(selected, state.Mapping, skippedClosed)
	}

	if err := a.ensureLabels(ctx, beadAreaLabels(selected)); err != nil {
		return err
	}

	res, err := a.runMigration(ctx, migrationEngine{
		Kind: conventions.ProvenanceMigrate, ItemNoun: "bead", SourceNoun: "snapshot",
		Items: beadItems(selected), StatePath: opts.StatePath, Throttle: opts.Throttle,
	}, state)
	if err != nil {
		return err
	}

	return a.emitResult(map[string]any{
		"created": res.Created, "wired": res.Wired, "closed": res.Closed,
		"skippedClosed": skippedClosed, "warnings": res.Warnings,
		"mapping": state.Mapping,
	}, func() {
		a.printf("migrated %d beads: %d created, %d dependencies wired, %d closed", len(selected), res.Created, res.Wired, res.Closed)
		if skippedClosed > 0 {
			a.printf(" (%d closed beads skipped; use --include-closed)", skippedClosed)
		}
		a.printf("\nmapping saved to %s\n", opts.StatePath)
	})
}

// migrationPlanEntry is one bead a dry run would create, in the shape both
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

// migrationPlan reports what a real run would do.
func (a *App) migrationPlan(selected []beads.Bead, state map[string]int, skippedClosed int) error {
	// One step per selected bead, in snapshot order: either already migrated
	// or a planned create. Text walks the steps; JSON splits them.
	type step struct {
		id       string
		migrated int
		planned  bool
		item     migrationPlanEntry
	}
	steps := make([]step, 0, len(selected))
	wouldMigrate := []migrationPlanEntry{}
	already := map[string]int{}
	for _, b := range selected {
		if n, ok := state[b.ID]; ok {
			already[b.ID] = n
			steps = append(steps, step{id: b.ID, migrated: n})
			continue
		}
		item := migrationPlanEntry{
			ID: b.ID, Priority: model.Priority(clampPriority(b.Priority)).String(),
			Title: b.Title, BlockedBy: append([]string{}, b.BlockedBy()...),
			InProgress: b.Status == "in_progress", ThenClose: b.Closed(),
		}
		if t := beadTypeLabels[b.IssueType]; t != "" {
			item.Type = &t
		} else if b.IssueType == "epic" {
			epic := "epic"
			item.Type = &epic
		}
		if p := b.Parent(); p != "" {
			item.Parent = &p
		}
		wouldMigrate = append(wouldMigrate, item)
		steps = append(steps, step{id: b.ID, planned: true, item: item})
	}
	return a.emitResult(map[string]any{
		"dryRun": true, "wouldMigrate": wouldMigrate, "alreadyMigrated": already,
		"skippedClosed": skippedClosed,
	}, func() {
		for _, st := range steps {
			if !st.planned {
				a.printf("already migrated: %s → #%d\n", st.id, st.migrated)
				continue
			}
			item := st.item
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
			a.printf("%s\n", line)
		}
		a.printf("dry run: %d beads would be migrated", len(selected))
		if skippedClosed > 0 {
			a.printf(" (%d closed skipped; use --include-closed)", skippedClosed)
		}
		a.printf("\n")
	})
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

// beadItems maps beads onto the engine's item shape: the convention labels
// derived from priority, type and status, the epic title prefix, the viewer as
// assignee for in-progress beads, and the close reason as a comment on closed
// ones. The provenance marker is the engine's to add.
func beadItems(selected []beads.Bead) []migrationItem {
	items := make([]migrationItem, 0, len(selected))
	for _, b := range selected {
		labels := []string{model.Priority(clampPriority(b.Priority)).String()}
		if t := beadTypeLabels[b.IssueType]; t != "" {
			labels = append(labels, t)
		}
		var assignees []string
		if b.Status == "in_progress" {
			labels = append(labels, model.InProgressLabel)
			assignees = []string{assigneeViewer}
		}
		labels = append(labels, areaLabels(b.Labels)...)

		title := b.Title
		if b.IssueType == "epic" && !strings.HasPrefix(title, conventions.EpicTitlePrefix) {
			title = conventions.EpicTitlePrefix + title
		}

		item := migrationItem{
			Key: b.ID, Title: title, Body: beadBody(b), Labels: labels,
			Assignees: assignees, Parent: b.Parent(), BlockedBy: b.BlockedBy(),
		}
		if b.Closed() {
			item.Closed = true
			item.CloseReason = gh.CloseCompleted
			if b.CloseReason != "" {
				item.Comments = []string{b.CloseReason}
			}
		}
		items = append(items, item)
	}
	return items
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
