package migrate

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/lumberbarons/hew/internal/beads"
	"github.com/lumberbarons/hew/internal/conventions"
	"github.com/lumberbarons/hew/internal/model"
	"github.com/lumberbarons/hew/tracker"
)

// BeadsRequest is one beads (bd) snapshot to import. The source data or the
// reader that streams it is the caller's input; which file it came from, and
// where a checkpoint lives, are the adapter's choices.
type BeadsRequest struct {
	// Snapshot is the issues.jsonl contents.
	Snapshot []byte
	// Reader streams the snapshot. When non-nil it is read instead of
	// Snapshot, so a caller that has a file or an HTTP body need not buffer it.
	Reader io.Reader
	// Source names the snapshot in messages ("issues.jsonl"). Empty means
	// "beads snapshot".
	Source string
	// IncludeClosed migrates closed beads too: the issue is created, carries
	// the close reason as a comment and is closed. Real databases are mostly
	// closed, so this is opt-in.
	IncludeClosed bool
}

// beadTypeLabels maps bead issue types to convention type labels; epics map to
// no type label because epic-ness is the epic marker and the sub-issue tree.
var beadTypeLabels = map[string]string{
	"bug":     "bug",
	"feature": "enhancement",
	"task":    "task",
	"chore":   "task",
	"epic":    "",
}

// Beads imports a beads snapshot: create in history order, wire parents and
// blockers, then close what was closed.
//
// The snapshot's exact bytes are the checkpoint's digest, so a state file
// written for one snapshot is refused by another. Warnings about the source
// (an unknown status migrated as open, a label renamed) ride on the result
// beside the engine's own.
func (m *Migrator) Beads(ctx context.Context, req BeadsRequest) (Result, error) {
	if err := m.requireTarget(); err != nil {
		return Result{}, err
	}
	data, err := req.bytes()
	if err != nil {
		return Result{}, err
	}
	all, err := beads.Parse(bytes.NewReader(data))
	if err != nil {
		return Result{}, fmt.Errorf("parsing %s: %w", req.sourceName(), err)
	}

	res := Result{DryRun: m.cfg.DryRun}
	var selected []beads.Bead
	for _, b := range all {
		switch b.Status {
		case "open", "in_progress":
			selected = append(selected, b)
		case "closed":
			if req.IncludeClosed {
				selected = append(selected, b)
			} else {
				res.Skipped++
			}
		default:
			res.note(WarnUnknownStatus, b.ID, 0, "%s has unknown status %q; migrating as open", b.ID, b.Status)
			selected = append(selected, b)
		}
	}
	res.Selected = len(selected)
	res.Warnings = append(res.Warnings, labelRenameWarnings(selectedLabels(selected))...)
	if len(selected) == 0 {
		res.Mapping = map[string]int{}
		res.Plan = []PlanStep{}
		res.WouldCreate = []PlannedItem{}
		res.AlreadyCreated = map[string]int{}
		return res, nil
	}

	validIDs := make(map[string]bool, len(all))
	for _, b := range all {
		validIDs[b.ID] = true
	}
	digest := tracker.FileDigest(data)
	checkpoint, err := m.checkpointFor(digest, "snapshot")
	if err != nil {
		return res, err
	}

	opts := Options{
		Kind:       conventions.ProvenanceMigrate,
		ItemNoun:   "bead",
		SourceNoun: "snapshot",
		KeyNoun:    "bead ID",
		Throttle:   m.cfg.Throttle,
		KnownKeys:  validIDs,
		Progress:   m.cfg.Progress,
		Digest:     digest,
		DryRun:     m.cfg.DryRun,
	}
	if labels, ok := m.labeler(); ok {
		extras := beadAreaLabels(selected)
		opts.BeforeCreate = func(ctx context.Context) error { return ensureLabels(ctx, labels, extras) }
	}
	runRes, err := m.run(ctx, RunRequest{Items: beadItems(selected), Options: opts}, checkpoint)
	runRes.Warnings = append(res.Warnings, runRes.Warnings...)
	runRes.Selected = res.Selected
	runRes.Skipped = res.Skipped
	runRes.DryRun = m.cfg.DryRun
	return runRes, err
}

// bytes returns the snapshot's contents, from the reader when one was given.
func (r BeadsRequest) bytes() ([]byte, error) {
	switch {
	case r.Reader != nil:
		data, err := io.ReadAll(r.Reader)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", r.sourceName(), err)
		}
		return data, nil
	case r.Snapshot != nil:
		return r.Snapshot, nil
	default:
		return nil, &tracker.InvalidInputError{Field: "snapshot", Reason: "must carry the beads issues.jsonl contents, as bytes or a reader"}
	}
}

// sourceName names the snapshot in messages.
func (r BeadsRequest) sourceName() string {
	if r.Source != "" {
		return r.Source
	}
	return "beads snapshot"
}

// selectedLabels is every label carried by the selected beads, in source order.
func selectedLabels(selected []beads.Bead) []string {
	var out []string
	for _, b := range selected {
		out = append(out, b.Labels...)
	}
	return out
}

// labelRenameWarnings names each source label that migratedLabel renames, once,
// in source order.
func labelRenameWarnings(labels []string) []Warning {
	var out []Warning
	seen := map[string]bool{}
	for _, l := range labels {
		renamed := migratedLabel(l)
		if renamed == l || seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, Warning{
			Kind:    WarnLabelRenamed,
			Message: fmt.Sprintf("label %q contains a comma; migrating as %q", l, renamed),
		})
	}
	return out
}

// beadAreaLabels collects every distinct area label the beads carry, for the
// label bootstrap: the area the imported issue will reference, in source order.
func beadAreaLabels(selected []beads.Bead) []Label {
	var out []Label
	seen := map[string]bool{}
	for _, b := range selected {
		for _, area := range areaLabels(b.Labels) {
			if !seen[area] {
				seen[area] = true
				out = append(out, Label{Name: area, Color: "ededed", Description: "migrated from beads"})
			}
		}
	}
	return out
}

// beadItems maps beads onto the engine's item shape: the convention labels
// derived from priority, type and status, the epic title prefix and marker, the
// viewer as assignee for in-progress beads, and the close reason as a comment
// on closed ones. The provenance marker is the engine's to add.
func beadItems(selected []beads.Bead) []Item {
	items := make([]Item, 0, len(selected))
	for _, b := range selected {
		labels := []string{model.Priority(clampPriority(b.Priority)).String()}
		if t := beadTypeLabels[b.IssueType]; t != "" {
			labels = append(labels, t)
		}
		if b.IssueType == "epic" {
			labels = append(labels, model.EpicLabel)
		}
		var assignees []string
		if b.Status == "in_progress" {
			labels = append(labels, model.InProgressLabel)
			assignees = []string{AssigneeViewer}
		}
		labels = append(labels, areaLabels(b.Labels)...)

		title := b.Title
		if b.IssueType == "epic" && !strings.HasPrefix(title, conventions.EpicTitlePrefix) {
			title = conventions.EpicTitlePrefix + title
		}

		item := Item{
			Key: b.ID, Title: title, Body: beadBody(b), Labels: labels,
			Assignees: assignees, Parent: b.Parent(), BlockedBy: b.BlockedBy(),
			Epic: b.IssueType == "epic",
		}
		if b.Closed() {
			item.Closed = true
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
	footer := fmt.Sprintf("Migrated from beads `%s` (created %s", b.ID, b.CreatedAt.UTC().Format("2006-01-02"))
	if b.ClosedAt != nil {
		footer += ", closed " + b.ClosedAt.UTC().Format("2006-01-02")
	}
	footer += ")"
	parts = append(parts, "---\n"+footer)
	return strings.Join(parts, "\n\n")
}

// areaLabels filters bead labels that would collide with the convention labels
// (priority, type, in-progress, epic) — those are derived, not copied. A comma
// in a name becomes a hyphen (see migratedLabel).
func areaLabels(labels []string) []string {
	var out []string
	for _, l := range labels {
		if model.IsConventionLabel(l) {
			continue
		}
		out = append(out, migratedLabel(l))
	}
	return out
}

// migratedLabel is the name a source label takes on GitHub. The write path
// refuses a comma in an area because areas are split and rendered on commas,
// so an imported "x,y" would read as the two areas "x" and "y"; it becomes
// "x-y" instead of being dropped.
func migratedLabel(name string) string { return strings.ReplaceAll(name, ",", "-") }

// clampPriority keeps a source priority inside the convention's P0..P4 range.
func clampPriority(p int) int {
	if p < 0 {
		return 0
	}
	if p > 4 {
		return 4
	}
	return p
}
