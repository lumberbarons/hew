package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lumberbarons/hew/internal/conventions"
	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/plan"
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
		return usageErr("usage: hew apply <plan.jsonl>")
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
				"dryRun": true, "wouldCreate": []applyPlanEntry{},
				"alreadyCreated": map[string]int{},
			}
		}
		return a.emitResult(result, func() {
			a.printf("nothing to apply: %s has no entries\n", opts.File)
		})
	}

	digest := fileDigest(data)
	state, err := loadBatchState(opts.StatePath, a.stateRepo(), digest, "plan")
	if err != nil {
		return err
	}
	validKeys := make(map[string]bool, len(entries))
	for _, e := range entries {
		validKeys[e.Key()] = true
	}

	// Reported in the pass that runs before anything is written, and on the
	// dry run that exists to be read first — so the plan can be fixed while
	// fixing it is still free.
	a.warnPlanCodeText(entries)

	// Verification reads GitHub but writes nothing, so the dry run makes the
	// same pass: the mode whose whole point is being read before the real run
	// has to be able to show the failure the real run would hit (#81).
	if err := a.verifyBatchState(ctx, state, conventions.ProvenanceApply, validKeys, "entry key"); err != nil {
		return err
	}
	// Validate pending creates before any writes. Already-created entries
	// may contain fields older releases accepted; re-gating them would
	// strand a checkpoint whose source digest cannot be changed to repair it.
	for _, e := range entries {
		if _, created := state.Mapping[e.Key()]; created {
			continue
		}
		if err := validateTitle(applyTitle(e)); err != nil {
			return usageErr("line %d: %v", e.Line, err)
		}
		if err := validateAreas("areas", e.Areas); err != nil {
			return usageErr("line %d: %v", e.Line, err)
		}
	}

	// Same reasoning: a reference to an issue that does not exist is a typo,
	// and finding it after half the plan is filed leaves those issues behind to
	// clean up (#233).
	if err := a.requireExistingRefs(ctx, entries, state.Mapping); err != nil {
		return err
	}

	if opts.DryRun {
		return a.applyPlan(entries, state)
	}

	if err := a.ensureLabels(ctx, planAreaLabels(entries)); err != nil {
		return err
	}

	created, err := a.applyCreate(ctx, entries, state, opts)
	if err != nil {
		return err
	}
	wired, warned := a.applyWire(ctx, entries, state, opts)

	if err := a.emitResult(map[string]any{
		"created": created, "wired": wired, "warnings": warned,
		"mapping": state.Mapping,
	}, func() {
		a.printf("applied %s: %d created, %d dependencies wired\n", opts.File, created, wired)
		a.printf("mapping saved to %s\n", opts.StatePath)
	}); err != nil {
		return err
	}
	// The issues exist and the result above says so, but an edge that did not
	// land is a plan that was not applied: a script branches on the exit code,
	// not on stderr (#233). The edge is not checkpointed, so a rerun retries it.
	if len(warned) > 0 {
		return genericErr("%d dependency edges were not wired (see warnings): fix the cause and rerun to retry them", len(warned))
	}
	return nil
}

// applyPlanEntry is one issue a dry run would create, in the shape both the
// text line and the --json result are built from.
type applyPlanEntry struct {
	Key       string   `json:"key"`
	Priority  string   `json:"priority"`
	Type      string   `json:"type"`
	Title     string   `json:"title"`
	Areas     []string `json:"areas"`
	Parent    *string  `json:"parent"`
	BlockedBy []string `json:"blockedBy"`
}

// applyPlan reports what a real run would do.
func (a *App) applyPlan(entries []plan.Entry, state *batchState) error {
	// One step per plan entry, in file order: either already created or a
	// planned create. Text walks the steps; JSON splits them.
	type step struct {
		key     string
		created int
		planned bool
		item    applyPlanEntry
	}
	steps := make([]step, 0, len(entries))
	wouldCreate := []applyPlanEntry{}
	already := map[string]int{}
	for _, e := range entries {
		if n, ok := state.Mapping[e.Key()]; ok {
			already[e.Key()] = n
			steps = append(steps, step{key: e.Key(), created: n})
			continue
		}
		item := applyPlanEntry{
			Key: e.Key(), Priority: e.Priority.String(), Type: e.Type, Title: applyTitle(e),
			Areas: append([]string{}, e.Areas...), BlockedBy: []string{},
		}
		if e.Parent != nil {
			parent := e.Parent.String()
			item.Parent = &parent
		}
		for _, b := range e.BlockedBy {
			item.BlockedBy = append(item.BlockedBy, b.String())
		}
		wouldCreate = append(wouldCreate, item)
		steps = append(steps, step{key: e.Key(), planned: true, item: item})
	}
	return a.emitResult(map[string]any{
		"dryRun": true, "wouldCreate": wouldCreate, "alreadyCreated": already,
	}, func() {
		for _, st := range steps {
			if !st.planned {
				a.printf("already created: %s → #%d\n", st.key, st.created)
				continue
			}
			item := st.item
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
		a.printf("dry run: %d issues would be created\n", len(wouldCreate))
	})
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
		issue, err := a.Client.CreateIssue(ctx, applyTitle(e), applyBodyWithProvenance(e, state.Digest), labels)
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
// the next run — Apply turns them into a non-zero exit — but a *successful* edge is never re-attempted, because
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

// requireExistingRefs checks every issue the plan references by number —
// parent, blocked-by and discovered-from — exists, reads only, naming the line
// and field that carries a bad one. Each number is looked up once, and the
// first line to use it is the one reported. Entries an earlier run already
// created are skipped: their references were checked then, and refusing a
// resume over one that has since gone would strand the rest. A reference of
// theirs that was never wired is retried and, if it still fails, exits
// non-zero from the wire pass.
func (a *App) requireExistingRefs(ctx context.Context, entries []plan.Entry, created map[string]int) error {
	checked := map[int]bool{}
	for _, e := range entries {
		if _, done := created[e.Key()]; done {
			continue
		}
		for _, ref := range e.ExistingRefs() {
			if checked[ref.Number] {
				continue
			}
			checked[ref.Number] = true
			if _, err := a.requireIssue(ctx, ref.Number, ref.Field); err != nil {
				return fmt.Errorf("line %d: %w", ref.Line, err)
			}
		}
	}
	return nil
}

// applyTitle prefixes epic entries the same way epic create does.
func applyTitle(e plan.Entry) string {
	if e.IsEpic() && !strings.HasPrefix(e.Title, conventions.EpicTitlePrefix) {
		return conventions.EpicTitlePrefix + e.Title
	}
	return e.Title
}

// warnPlanCodeText reports code-shaped text left as prose, per entry, so a
// plan naming twenty files says which entry to fix rather than which token.
func (a *App) warnPlanCodeText(entries []plan.Entry) {
	for _, e := range entries {
		tokens := conventions.UnmarkedCodeText(applyBody(e))
		if len(tokens) == 0 {
			continue
		}
		a.warnf("%s: not in code spans: %s — wrap them (and any command around them) in backticks",
			e.Key(), conventions.FormatUnmarkedCodeText(tokens))
	}
}

// applyBody composes structured section fields the same way the create
// section flags do and appends the discovered-from link. This is the body a
// human reads; the provenance marker is added separately so the code-text
// warning scans prose rather than the tool's own bookkeeping.
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

// applyBodyWithProvenance is what actually gets written: the composed body
// plus the marker binding the issue to this entry and this plan file, which
// is what lets a later run tell an issue it created from one a state file
// merely points at (#81).
func applyBodyWithProvenance(e plan.Entry, digest string) string {
	marker := conventions.ProvenanceMarker(conventions.ProvenanceApply, e.Key(), digest)
	body := applyBody(e)
	if body == "" {
		return marker
	}
	return strings.TrimRight(body, "\n") + "\n\n" + marker
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
				out = append(out, gh.Label{Name: area, Color: "ededed", Description: "created by hew apply"})
			}
		}
	}
	return out
}
