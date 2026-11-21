package migrate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"

	"github.com/lumberbarons/hew/internal/conventions"
	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/local"
	"github.com/lumberbarons/hew/internal/model"
	"github.com/lumberbarons/hew/tracker"
)

// localMigratedFile is the record a finished local migration leaves beside the
// archived issues.jsonl.
const localMigratedFile = local.MigratedFile

// LocalRequest is one local-to-GitHub migration. The tracker directory is the
// adapter's input (it resolves the main checkout); everything else — the
// snapshot, the checkpoint and the seal — is the workflow's.
type LocalRequest struct {
	// Dir is the local tracker directory (.hew at the main checkout).
	Dir string
}

// Local moves every issue in the local tracker — closed ones too — to the
// GitHub repository this Migrator writes to, then flips the tracker's backend
// to GitHub and keeps issues.jsonl as an archive.
//
// The tracker is held under its lock for the whole run, so nothing written
// locally can slip in after the snapshot and be lost; writers wait, and once
// the tracker is sealed they find it is no longer local and refuse. The flip
// happens only when every step finished: a half-wired migration stays local so
// the rerun that completes it is still possible.
//
// A dry run reads the tracker without locking it and never writes. A dry run
// with no target (see Open) plans from issues.jsonl alone, and refuses an
// existing checkpoint rather than listing work a real run would skip.
func (m *Migrator) Local(ctx context.Context, req LocalRequest) (Result, error) {
	if req.Dir == "" {
		return Result{}, &tracker.InvalidInputError{Field: "dir", Reason: "must name the local tracker directory"}
	}
	if m.backend == tracker.BackendLocal {
		return Result{}, &tracker.InvalidInputError{Field: "backend", Reason: "the local migration writes to GitHub; pin Backend to github and Repo to owner/name"}
	}
	if err := m.requireTarget(); err != nil {
		return Result{}, err
	}
	if !m.cfg.DryRun && m.repoName == "" {
		return Result{}, &tracker.InvalidInputError{Field: "repo", Reason: "the local migration writes to GitHub, so it must be owner/name"}
	}
	if err := requireLocalTracker(req.Dir); err != nil {
		return Result{}, err
	}

	var snap *local.Snapshot
	var frozen *local.Frozen
	if m.cfg.DryRun {
		s, err := local.ReadSnapshot(req.Dir)
		if err != nil {
			return Result{}, err
		}
		snap = s
	} else {
		fr, err := local.Freeze(ctx, req.Dir)
		if err != nil {
			return Result{}, err
		}
		defer fr.Release()
		frozen, snap = fr, fr.Snapshot
	}

	res := Result{DryRun: m.cfg.DryRun, Selected: len(snap.Issues)}
	res.Warnings = labelRenameWarnings(localIssueLabels(snap))
	items := localItems(snap)

	checkpoint, err := m.checkpointFor(snap.Digest, "issues.jsonl")
	if err != nil {
		return res, err
	}
	opts := Options{
		Kind:       conventions.ProvenanceMigrate,
		ItemNoun:   "local issue",
		SourceNoun: "issues.jsonl",
		KeyNoun:    "local issue number",
		Throttle:   m.cfg.Throttle,
		KnownKeys:  localKeys(snap),
		Progress:   m.cfg.Progress,
		Digest:     snap.Digest,
		DryRun:     m.cfg.DryRun,
		Relink: func(text string, mapping map[string]int) string {
			return rewriteLocalRefs(text, localNumbers(mapping))
		},
	}
	if labels, ok := m.labeler(); ok {
		extras := localLabels(snap)
		opts.BeforeCreate = func(ctx context.Context) error { return ensureLabels(ctx, labels, extras) }
	}
	runRes, err := m.run(ctx, RunRequest{Items: items, Options: opts}, checkpoint)
	runRes.Warnings = append(res.Warnings, runRes.Warnings...)
	runRes.Selected = res.Selected
	runRes.DryRun = m.cfg.DryRun
	if err != nil || m.cfg.DryRun {
		return runRes, err
	}
	if runRes.Failed > 0 {
		return runRes, &IncompleteError{Failed: runRes.Failed}
	}
	if err := frozen.Seal(m.repoName, localNumbers(runRes.Mapping)); err != nil {
		return runRes, &SealError{Err: err}
	}
	runRes.Backend = tracker.BackendGitHub
	return runRes, nil
}

// requireLocalTracker names what is wrong when dir is not a tracker that can be
// migrated, before anything is locked or read.
func requireLocalTracker(dir string) error {
	cfg, err := local.ReadConfig(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return &NotLocalError{Dir: dir}
	case err != nil:
		return err
	case cfg.Backend == local.BackendLocal:
		return nil
	}
	refusal := &NotLocalError{Dir: dir, Backend: cfg.Backend}
	if m, err := local.ReadMigrated(dir); err == nil {
		refusal.Repo = m.Repo
	}
	return refusal
}

// localKeys is every key the snapshot carries, so a checkpoint naming one the
// tracker no longer holds is refused rather than trusted.
func localKeys(snap *local.Snapshot) map[string]bool {
	valid := make(map[string]bool, len(snap.Issues))
	for _, i := range snap.Issues {
		valid[localKey(i.Number)] = true
	}
	return valid
}

// localIssueLabels is every label the tracker's issues carry, in number order.
func localIssueLabels(snap *local.Snapshot) []string {
	var out []string
	for _, i := range snap.Issues {
		out = append(out, i.Labels...)
	}
	return out
}

func localKey(number int) string { return strconv.Itoa(number) }

// localNumbers converts a key → GitHub number mapping back to local numbers.
// Keys that are not numbers cannot have come from a local tracker and are
// dropped.
func localNumbers(mapping map[string]int) map[int]int {
	out := make(map[int]int, len(mapping))
	for key, n := range mapping {
		if number, err := strconv.Atoi(key); err == nil {
			out[number] = n
		}
	}
	return out
}

// localItems maps the tracker's issues onto engine items, in number order.
// Labels carry over as written, so a local issue typed `idea` arrives on GitHub
// with the type label the bootstrap defines; assignees do not, because a local
// handle is not a GitHub login — an open issue that was claimed is claimed by
// the person migrating, and a closed one keeps no claim at all.
func localItems(snap *local.Snapshot) []Item {
	items := make([]Item, 0, len(snap.Issues))
	for _, i := range snap.Issues {
		closed := i.State == string(gh.StateClosed)
		labels := make([]string, 0, len(i.Labels))
		for _, l := range i.Labels {
			if closed && model.IsInProgressLabel(l) {
				continue
			}
			labels = append(labels, migratedLabel(l))
		}
		item := Item{
			Key: localKey(i.Number), Title: i.Title, Body: localBody(i), Labels: labels,
			Closed: closed,
		}
		if !closed && len(i.Assignees) > 0 {
			item.Assignees = []string{AssigneeViewer}
		}
		if closed {
			switch gh.CloseReason(i.StateReason) {
			case gh.CloseCompleted:
				item.CloseReason = CloseCompleted
			case gh.CloseNotPlanned:
				item.CloseReason = CloseNotPlanned
			case gh.CloseDuplicate:
				item.CloseReason = CloseDuplicate
			}
		}
		if i.Parent != 0 {
			item.Parent = localKey(i.Parent)
		}
		for _, b := range i.BlockedBy {
			item.BlockedBy = append(item.BlockedBy, localKey(b))
		}
		for _, c := range i.Comments {
			item.Comments = append(item.Comments, localComment(c))
		}
		items = append(items, item)
	}
	return items
}

// localBody is the issue's own text followed by a footer saying where it came
// from. The footer sets the local number in a code span so reference rewriting
// leaves the one place that names it alone.
func localBody(i local.Issue) string {
	footer := fmt.Sprintf("Migrated from local issue `#%d` (created %s)", i.Number, i.CreatedAt.UTC().Format("2006-01-02"))
	if body := strings.TrimSpace(i.Body); body != "" {
		return body + "\n\n---\n" + footer
	}
	return "---\n" + footer
}

// localComment credits the original author and date in a lead line GitHub's
// comment API has no field for. The author sits in a code span: local handles
// look like logins, and a bare one could mention a real account.
func localComment(c local.Comment) string {
	author := strings.TrimSpace(strings.ReplaceAll(c.Author, "`", "'"))
	if author == "" {
		author = "unknown"
	}
	return fmt.Sprintf("`%s` commented on %s:\n\n%s", author, c.CreatedAt.UTC().Format("2006-01-02"), c.Body)
}

// localLabels is every label the tracker uses that GitHub may not have yet, for
// the label bootstrap: the tracker's own definitions where it has them, a
// neutral grey otherwise. Convention labels are the bootstrap's already and
// keep their canonical cosmetics.
func localLabels(snap *local.Snapshot) []Label {
	defined := map[string]gh.Label{}
	for _, l := range snap.Labels {
		defined[strings.ToLower(l.Name)] = l
	}
	conventional := map[string]bool{}
	for _, l := range conventions.Labels {
		conventional[strings.ToLower(l.Name)] = true
	}
	var out []Label
	seen := map[string]bool{}
	add := func(name string) {
		name = migratedLabel(name)
		key := strings.ToLower(name)
		if seen[key] || conventional[key] {
			return
		}
		seen[key] = true
		if l, ok := defined[key]; ok {
			out = append(out, Label{Name: l.Name, Color: l.Color, Description: l.Description})
			return
		}
		out = append(out, Label{Name: name, Color: "ededed", Description: "migrated from the local tracker"})
	}
	for _, l := range snap.Labels {
		add(l.Name)
	}
	for _, i := range snap.Issues {
		for _, l := range i.Labels {
			add(l)
		}
	}
	return out
}
