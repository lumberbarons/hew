package tracker

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/lumberbarons/hew/internal/conventions"
	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/model"
)

// ApplyResult is what ApplyPlan did, or — under DryRun — what it would do.
type ApplyResult struct {
	// DryRun reports which mode ran.
	DryRun bool `json:"dryRun"`
	// Created, Wired and Unwired count the run's writes. Unwired is the
	// number of edges that did not land; they are listed in Warnings and a
	// rerun retries them.
	Created int `json:"created"`
	Wired   int `json:"wired"`
	Unwired int `json:"unwired"`
	// Warnings names the edges that did not land and the checkpoints that
	// could not be saved.
	Warnings []string `json:"warnings"`
	// Mapping is every entry key → issue number after the run.
	Mapping map[string]int `json:"mapping"`
	// Applied is each entry's outcome in plan order, for progress rendering.
	Applied []AppliedEntry `json:"applied"`
	// WouldCreate and AlreadyCreated are the dry run's plan and the entries
	// its checkpoint already accounts for. They are empty otherwise.
	WouldCreate    []PlannedEntry `json:"wouldCreate"`
	AlreadyCreated map[string]int `json:"alreadyCreated"`
}

// AppliedEntry is one entry's outcome in a real run.
type AppliedEntry struct {
	Key   string `json:"key"`
	Issue int    `json:"issue"`
	// Created is false when the checkpoint already mapped the entry, so the
	// run only reported it.
	Created bool `json:"created"`
}

// PlannedEntry is one issue a dry run would create, in the flat shape the
// CLI's plan output uses. Every field is present on every entry; Parent is
// null for an entry without one.
type PlannedEntry struct {
	Key       string   `json:"key"`
	Priority  string   `json:"priority"`
	Type      string   `json:"type"`
	Title     string   `json:"title"`
	Areas     []string `json:"areas"`
	Parent    *string  `json:"parent"`
	BlockedBy []string `json:"blockedBy"`
}

// ApplyPlan batch-creates issues from typed plan entries: create in entry
// order, checkpointing after every write, then wire parents and blockers —
// the same machinery `hew apply` runs over a JSONL plan, for callers that
// build plans in code.
//
// A checkpoint makes an interrupted run resumable: every entry it maps is
// skipped, and an issue a checkpoint points at is verified to carry the
// provenance marker this plan wrote before anything is touched. The zero
// ApplyOptions uses an in-memory checkpoint; FileCheckpoint persists to a
// state file.
//
// ApplyPlan validates the whole plan, verifies every existing-issue reference
// and every checkpoint mapping before the first write, so a refused plan
// leaves nothing behind. A create failure aborts with the checkpoint already
// holding the issues created so far; edge failures warn and are retried by a
// rerun, and are counted in ApplyResult.Unwired.
func (t *Tracker) ApplyPlan(ctx context.Context, entries []PlanEntry, opts ApplyOptions) (ApplyResult, error) {
	entries = normalizePlan(entries)
	if err := ValidatePlan(entries); err != nil {
		return ApplyResult{}, err
	}
	if len(entries) == 0 {
		return ApplyResult{
			DryRun:         opts.DryRun,
			Mapping:        map[string]int{},
			WouldCreate:    []PlannedEntry{},
			AlreadyCreated: map[string]int{},
		}, nil
	}
	checkpoint := opts.Checkpoint
	if checkpoint == nil {
		checkpoint = NewMemoryCheckpoint()
	}
	digest := opts.Digest
	if bound := checkpoint.Digest(); bound != "" {
		if digest != "" && digest != bound {
			return ApplyResult{}, &InvalidInputError{Field: "digest", Reason: "does not match the checkpoint's source binding"}
		}
		digest = bound
	}
	if digest == "" {
		computed, err := planDigest(entries)
		if err != nil {
			return ApplyResult{}, err
		}
		digest = computed
	}
	if err := t.verifyPlanCheckpoint(ctx, checkpoint, entries, digest); err != nil {
		return ApplyResult{}, err
	}
	// The write path's title and area rules live here so a caller that builds
	// entries in code cannot bypass them; pending entries only, after the
	// checkpoint is verified, so a resume never re-gates what it already made.
	entries, err := validatePendingPlanFields(entries, checkpoint)
	if err != nil {
		return ApplyResult{}, err
	}
	if err := t.requirePlanRefs(ctx, entries, checkpoint); err != nil {
		return ApplyResult{}, err
	}
	if opts.DryRun {
		return planDryRun(entries, checkpoint), nil
	}
	if err := t.ensurePlanLabels(ctx, entries); err != nil {
		return ApplyResult{}, err
	}

	var result ApplyResult
	for _, e := range entries {
		if n, ok := checkpoint.Created(e.Key()); ok {
			result.Applied = append(result.Applied, AppliedEntry{Key: e.Key(), Issue: n})
			continue
		}
		issue, err := t.store.CreateIssue(ctx, PlanTitle(e), PlanBodyWithProvenance(e, digest), planLabels(e))
		if err != nil {
			return result, fmt.Errorf("creating %s (rerun to resume): %w", e.Key(), asWriteError(err))
		}
		if err := checkpoint.RecordCreated(e.Key(), issue.Number); err != nil {
			return result, err
		}
		result.Created++
		result.Applied = append(result.Applied, AppliedEntry{Key: e.Key(), Issue: issue.Number, Created: true})
		sleep(opts.Throttle)
	}
	result.Wired, result.Unwired, result.Warnings = t.wirePlan(ctx, entries, checkpoint, opts.Throttle)
	result.Mapping = planMapping(checkpoint)
	return result, nil
}

// normalizePlan copies entries with the defaults ApplyPlan applies: an empty
// priority is P2, and an id-less entry built in code is keyed by its 1-based
// position instead of line 0.
func normalizePlan(entries []PlanEntry) []PlanEntry {
	out := make([]PlanEntry, len(entries))
	for i, e := range entries {
		if e.Line == 0 {
			e.Line = i + 1
		}
		if e.Priority == "" {
			e.Priority = model.DefaultPriority.String()
		}
		out[i] = e
	}
	return out
}

// planDigest identifies a code-built plan for provenance markers: a caller
// that rebuilds the same entries resumes its own checkpoint, and one that
// changes them does not.
func planDigest(entries []PlanEntry) (string, error) {
	data, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	return FileDigest(data), nil
}

// verifyPlanCheckpoint checks every mapping before ApplyPlan mutates anything:
// the key must belong to this plan, and the issue must carry the provenance
// marker this plan writes, bound to that key and this digest. Mappings are
// walked in sorted order so a checkpoint with more than one bad entry fails on
// the same one every time — an unstable error message is a bad bug report.
func (t *Tracker) verifyPlanCheckpoint(ctx context.Context, checkpoint Checkpoint, entries []PlanEntry, digest string) error {
	valid := make(map[string]bool, len(entries))
	for _, e := range entries {
		valid[e.Key()] = true
	}
	for _, record := range checkpoint.CreatedEntries() {
		if !valid[record.Key] {
			return fmt.Errorf("state file maps entry key %q to issue #%d, but %q is not in this run's selection — it was removed from the source, or a flag is filtering it out",
				record.Key, record.Number, record.Key)
		}
		issue, err := t.store.GetIssue(ctx, record.Number)
		if err != nil {
			return fmt.Errorf("verifying mapped issue #%d for %s: %w", record.Number, record.Key, asReadError(err))
		}
		if !conventions.HasProvenanceMarker(issue.Body, conventions.ProvenanceApply, record.Key, digest) {
			return fmt.Errorf("refusing to modify issue #%d: it does not carry the hew provenance marker for entry key %q from this source file, so this run did not create it",
				record.Number, record.Key)
		}
	}
	return nil
}

// requirePlanRefs checks every issue a pending entry references by number —
// parent, blocked-by and discovered-from — exists, reads only, naming the line
// and field that carries a bad one. Each number is looked up once, and the
// first line to use it is the one reported. Entries already created are
// skipped: their references were checked then, and refusing a resume over one
// that has since gone would strand the rest.
func (t *Tracker) requirePlanRefs(ctx context.Context, entries []PlanEntry, checkpoint Checkpoint) error {
	checked := map[int]bool{}
	for _, e := range entries {
		if _, done := checkpoint.Created(e.Key()); done {
			continue
		}
		for _, ref := range e.ExistingRefs() {
			if checked[ref.Number] {
				continue
			}
			checked[ref.Number] = true
			if _, err := t.store.GetIssue(ctx, ref.Number); err != nil {
				return fmt.Errorf("line %d: %s: %w", ref.Line, ref.Field, asReadError(err))
			}
		}
	}
	return nil
}

// ensurePlanLabels creates any missing convention labels plus the area labels
// the plan names, so a create never references a missing label.
func (t *Tracker) ensurePlanLabels(ctx context.Context, entries []PlanEntry) error {
	existing, err := t.store.ListLabels(ctx)
	if err != nil {
		return asReadError(err)
	}
	have := map[string]bool{}
	for _, l := range existing {
		have[strings.ToLower(l.Name)] = true
	}
	var want []gh.Label
	for _, l := range conventions.Labels {
		want = append(want, gh.Label{Name: l.Name, Color: l.Color, Description: l.Description})
	}
	for _, e := range entries {
		for _, area := range e.Areas {
			if have[strings.ToLower(area)] {
				continue
			}
			if slices.ContainsFunc(want, func(l gh.Label) bool { return strings.EqualFold(l.Name, area) }) {
				continue
			}
			want = append(want, gh.Label{Name: area, Color: "ededed", Description: "created by hew apply"})
		}
	}
	for _, l := range want {
		if have[strings.ToLower(l.Name)] {
			continue
		}
		if err := t.store.CreateLabel(ctx, l); err != nil {
			return fmt.Errorf("creating label %q: %w", l.Name, asWriteError(err))
		}
		have[strings.ToLower(l.Name)] = true
	}
	return nil
}

// planLabels composes the labels an entry's issue carries: one priority, the
// epic marker or the type, then its areas.
func planLabels(e PlanEntry) []string {
	labels := []string{e.Priority}
	if e.IsEpic() {
		labels = append(labels, model.EpicLabel)
	} else {
		labels = append(labels, e.Type)
	}
	return append(labels, e.Areas...)
}

// wirePlan connects parents and blockers, checkpointing each edge as it lands
// so a resume skips it. Failures warn and continue, and are retried on a
// rerun; they are counted in Unwired. A warning about the checkpoint file is
// not counted, because that edge did land. A successful edge is never
// re-attempted, because GitHub rejects the duplicate and the warning it
// produces would drown the ones that mean something. Local references need no
// cycle check here: ValidatePlan already rejected cycles, and existing issues
// cannot depend on issues that didn't exist yet.
func (t *Tracker) wirePlan(ctx context.Context, entries []PlanEntry, checkpoint Checkpoint, throttle time.Duration) (wired, unwired int, warnings []string) {
	warn := func(format string, args ...any) {
		warnings = append(warnings, fmt.Sprintf(format, args...))
	}
	// resolve maps a reference to an issue number: local ids through the
	// checkpoint, numeric refs as-is.
	resolve := func(r PlanRef) (int, bool) {
		if r.ID == "" {
			return r.Number, true
		}
		return checkpoint.Created(r.ID)
	}
	for _, e := range entries {
		from, ok := checkpoint.Created(e.Key())
		if !ok {
			continue
		}
		for _, edge := range e.Edges() {
			to, ok := resolve(edge.To)
			if !ok {
				warn("%s %s of %s not created, edge dropped", edge.Kind, edge.To, e.Key())
				unwired++
				continue
			}
			if checkpoint.Wired(edge.Kind, from, to) {
				continue
			}
			var err error
			if edge.Kind == ParentEdge {
				err = t.store.AddSubIssue(ctx, to, from, true)
			} else {
				err = t.store.AddBlockedBy(ctx, from, to)
			}
			if err != nil {
				warn("%s edge #%d→#%d: %v", edge.Kind, from, to, err)
				unwired++
				continue
			}
			if err := checkpoint.RecordWired(edge.Kind, from, to); err != nil {
				warn("saving checkpoint: %v", err)
			}
			wired++
			sleep(throttle)
		}
	}
	return wired, unwired, warnings
}

// planDryRun reports what a real run would do, reading nothing: verification
// has already run, so the answer is the checkpoint plus the pending entries.
func planDryRun(entries []PlanEntry, checkpoint Checkpoint) ApplyResult {
	result := ApplyResult{
		DryRun:         true,
		WouldCreate:    []PlannedEntry{},
		AlreadyCreated: map[string]int{},
	}
	for _, e := range entries {
		if n, ok := checkpoint.Created(e.Key()); ok {
			result.AlreadyCreated[e.Key()] = n
			continue
		}
		result.WouldCreate = append(result.WouldCreate, plannedEntry(e))
	}
	return result
}

func plannedEntry(e PlanEntry) PlannedEntry {
	item := PlannedEntry{
		Key:       e.Key(),
		Priority:  e.Priority,
		Type:      e.Type,
		Title:     PlanTitle(e),
		Areas:     append([]string{}, e.Areas...),
		BlockedBy: []string{},
	}
	if e.Parent != nil {
		parent := e.Parent.String()
		item.Parent = &parent
	}
	for _, b := range e.BlockedBy {
		item.BlockedBy = append(item.BlockedBy, b.String())
	}
	return item
}

func planMapping(checkpoint Checkpoint) map[string]int {
	records := checkpoint.CreatedEntries()
	out := make(map[string]int, len(records))
	for _, record := range records {
		out[record.Key] = record.Number
	}
	return out
}

func sleep(d time.Duration) {
	if d > 0 {
		time.Sleep(d)
	}
}
