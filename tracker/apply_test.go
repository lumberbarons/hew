package tracker

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/internal/conventions"
	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/model"
)

var testCtx = context.Background()

// applyStore is a write-capable store for ApplyPlan: fakeClient supplies the
// reads and the issue set, and the methods below really create and wire so a
// resume sees exactly what the interrupted run left behind.
type applyStore struct {
	*fakeClient
	next          int
	createCalls   int
	failCreateAt  int
	createErr     error
	edgeErr       error
	created       []string
	edges         []string
	labels        []gh.Label
	getIssueCalls []int
}

func newApplyStore(issues ...model.Issue) *applyStore {
	f := &fakeClient{}
	f.issues = append(f.issues, issues...)
	return &applyStore{fakeClient: f, next: 100}
}

func (s *applyStore) CreateIssue(_ context.Context, title, body string, labels []string) (model.Issue, error) {
	s.createCalls++
	if s.createErr != nil {
		return model.Issue{}, s.createErr
	}
	if s.failCreateAt > 0 && s.createCalls >= s.failCreateAt {
		return model.Issue{}, errors.New("injected create failure")
	}
	s.next++
	issue := model.Issue{Number: s.next, Title: title, Body: body, State: "OPEN", Labels: labels}
	s.issues = append(s.issues, issue)
	s.created = append(s.created, title)
	return issue, nil
}

func (s *applyStore) AddSubIssue(_ context.Context, parent, child int, _ bool) error {
	if s.edgeErr != nil {
		return s.edgeErr
	}
	s.edges = append(s.edges, fmt.Sprintf("parent:%d->%d", child, parent))
	for i := range s.issues {
		if s.issues[i].Number == child {
			s.issues[i].Parent = &model.Ref{Number: parent, State: "OPEN"}
		}
	}
	return nil
}

func (s *applyStore) AddBlockedBy(_ context.Context, issue, blocker int) error {
	if s.edgeErr != nil {
		return s.edgeErr
	}
	s.edges = append(s.edges, fmt.Sprintf("blocked-by:%d->%d", issue, blocker))
	for i := range s.issues {
		i := &s.issues[i]
		if i.Number != issue {
			continue
		}
		i.BlockedBy = append(i.BlockedBy, model.Ref{Number: blocker, State: "OPEN"})
		i.BlockedByTotal = len(i.BlockedBy)
	}
	return nil
}

func (s *applyStore) GetIssue(testCtx context.Context, number int) (model.Issue, error) {
	s.getIssueCalls = append(s.getIssueCalls, number)
	return s.fakeClient.GetIssue(testCtx, number)
}

func (s *applyStore) ListLabels(context.Context) ([]gh.Label, error) {
	return slices.Clone(s.labels), nil
}

func (s *applyStore) CreateLabel(_ context.Context, label gh.Label) error {
	s.labels = append(s.labels, label)
	return nil
}

func (s *applyStore) byTitle(title string) *model.Issue {
	for i := range s.issues {
		if s.issues[i].Title == title {
			return &s.issues[i]
		}
	}
	return nil
}

func applyPlanFixture() []PlanEntry {
	return []PlanEntry{
		{ID: "epic", Title: "Voltgo support", Type: PlanTypeEpic, Priority: "P1", Body: "### Goal\n\nstuff"},
		{ID: "scaffold", Title: "Scaffold", Type: "task", Areas: []string{"ble"}, Parent: &PlanRef{ID: "epic"}},
		{ID: "collector", Title: "Collector", Type: "enhancement", Priority: "P3",
			Sections:       Sections{Goal: "ship it"},
			Parent:         &PlanRef{ID: "epic"},
			BlockedBy:      []PlanRef{{ID: "scaffold"}, {Number: 42}},
			DiscoveredFrom: 7},
	}
}

func TestApplyPlanCreatesAndWiresTypedEntries(t *testing.T) {
	store := newApplyStore(fixture(42, "Existing dep", "P2", "task"), fixture(7, "Source", "P2", "task"))
	tr := WrapAs(store, BackendGitHub, "")
	entries := applyPlanFixture()

	result, err := tr.ApplyPlan(testCtx, entries, ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 3 || result.Wired != 4 || result.Unwired != 0 {
		t.Fatalf("result = %+v, want 3 created and 4 wired", result)
	}
	epic := store.byTitle("Epic: Voltgo support")
	if epic == nil {
		t.Fatal("epic was not created with its cosmetic prefix")
	}
	if !slices.Contains(epic.Labels, "P1") || !slices.Contains(epic.Labels, model.EpicLabel) {
		t.Errorf("epic labels = %v, want P1 and the epic marker", epic.Labels)
	}
	if got := countStrings(epic.Labels, model.EpicLabel); got != 1 {
		t.Errorf("epic labels = %v, want the epic marker exactly once", epic.Labels)
	}
	scaffold := store.byTitle("Scaffold")
	if scaffold == nil || scaffold.Parent == nil || scaffold.Parent.Number != epic.Number {
		t.Fatalf("scaffold = %+v, want the epic as parent", scaffold)
	}
	if !slices.Contains(scaffold.Labels, "ble") {
		t.Errorf("scaffold labels = %v, want the area", scaffold.Labels)
	}
	collector := store.byTitle("Collector")
	if collector == nil || collector.Parent == nil || collector.Parent.Number != epic.Number {
		t.Fatalf("collector = %+v, want the epic as parent", collector)
	}
	var blockers []int
	for _, b := range collector.BlockedBy {
		blockers = append(blockers, b.Number)
	}
	if !slices.Contains(blockers, scaffold.Number) || !slices.Contains(blockers, 42) {
		t.Errorf("collector blockers = %v", blockers)
	}
	if !strings.Contains(collector.Body, "### Goal\n\nship it") {
		t.Errorf("collector body = %q, want the composed sections", collector.Body)
	}
	if !strings.Contains(collector.Body, conventions.DiscoveredFrom(7)) {
		t.Errorf("collector body = %q, want the discovered-from link", collector.Body)
	}
	for _, key := range []string{"epic", "scaffold", "collector"} {
		if _, ok := result.Mapping[key]; !ok {
			t.Errorf("mapping %v missing %q", result.Mapping, key)
		}
	}
	if len(store.labels) == 0 {
		t.Error("no convention labels were bootstrapped")
	}
}

// The done-when: an in-memory checkpoint lets a second call resume after an
// injected failure without duplicating the issues the first call created.
func TestApplyPlanResumesFromMemoryCheckpointAfterFailure(t *testing.T) {
	store := newApplyStore()
	tr := WrapAs(store, BackendGitHub, "")
	entries := []PlanEntry{
		{ID: "a", Title: "First", Type: "task"},
		{ID: "b", Title: "Second", Type: "task", Parent: &PlanRef{ID: "a"}},
		{ID: "c", Title: "Third", Type: "task", BlockedBy: []PlanRef{{ID: "b"}}},
	}
	checkpoint := NewMemoryCheckpoint()
	opts := ApplyOptions{Checkpoint: checkpoint}

	store.failCreateAt = 2
	if _, err := tr.ApplyPlan(testCtx, entries, opts); err == nil || !strings.Contains(err.Error(), "rerun to resume") {
		t.Fatalf("err = %v, want a resumable create failure", err)
	}
	if n, ok := checkpoint.Created("a"); !ok || n == 0 {
		t.Fatalf("the interrupted run recorded nothing for a: %v", checkpoint.CreatedEntries())
	}
	if len(store.created) != 1 {
		t.Fatalf("created %v before the failure, want only the first entry", store.created)
	}

	store.failCreateAt = 0
	result, err := tr.ApplyPlan(testCtx, entries, opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 2 {
		t.Errorf("resume created %d, want the two that were left", result.Created)
	}
	for _, title := range []string{"First", "Second", "Third"} {
		if got := countStrings(store.created, title); got != 1 {
			t.Errorf("%q was created %d times, want once: %v", title, got, store.created)
		}
	}
	if len(store.issues) != 3 {
		t.Errorf("store holds %d issues, want 3", len(store.issues))
	}
	// The resume wires the edges the interrupted run never reached.
	if !slices.Contains(store.edges, fmt.Sprintf("parent:%d->%d", result.Mapping["b"], result.Mapping["a"])) {
		t.Errorf("edges = %v, want b's parent", store.edges)
	}
	if !slices.Contains(store.edges, fmt.Sprintf("blocked-by:%d->%d", result.Mapping["c"], result.Mapping["b"])) {
		t.Errorf("edges = %v, want c's blocker", store.edges)
	}
}

// A file checkpoint's binding is the plan's identity: a caller that opens one
// and passes no digest still gets markers the file's next run can verify.
func TestApplyPlanUsesAFileCheckpointsDigest(t *testing.T) {
	store := newApplyStore()
	path := filepath.Join(t.TempDir(), "state.json")
	checkpoint, err := OpenFileCheckpoint(path, "o/r", "sha256:file", "plan")
	if err != nil {
		t.Fatal(err)
	}
	entries := []PlanEntry{{ID: "a", Title: "A", Type: "task"}}
	if _, err := WrapAs(store, BackendGitHub, "").ApplyPlan(testCtx, entries, ApplyOptions{Checkpoint: checkpoint}); err != nil {
		t.Fatal(err)
	}
	issue := store.byTitle("A")
	if issue == nil {
		t.Fatal("issue not created")
	}
	if !conventions.HasProvenanceMarker(issue.Body, conventions.ProvenanceApply, "a", "sha256:file") {
		t.Errorf("body = %q, want the file's digest in the marker", issue.Body)
	}
	reloaded, err := OpenFileCheckpoint(path, "o/r", "sha256:file", "plan")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WrapAs(store, BackendGitHub, "").ApplyPlan(testCtx, entries, ApplyOptions{Checkpoint: reloaded}); err != nil {
		t.Fatalf("resume with the checkpoint's own digest: %v", err)
	}
}

func TestApplyPlanRefusesADigestThatDisagreesWithTheCheckpoint(t *testing.T) {
	store := newApplyStore()
	checkpoint, err := OpenFileCheckpoint(filepath.Join(t.TempDir(), "state.json"), "o/r", "sha256:file", "plan")
	if err != nil {
		t.Fatal(err)
	}
	_, err = WrapAs(store, BackendGitHub, "").ApplyPlan(testCtx,
		[]PlanEntry{{ID: "a", Title: "A", Type: "task"}},
		ApplyOptions{Checkpoint: checkpoint, Digest: "sha256:other"})
	var invalid *InvalidInputError
	if !errors.As(err, &invalid) || invalid.Field != "digest" {
		t.Fatalf("err = %v, want an invalid-digest refusal", err)
	}
	if len(store.created) != 0 {
		t.Errorf("created %v despite a mismatched digest", store.created)
	}
}

func TestApplyPlanEmptyPlanDoesNothing(t *testing.T) {
	store := newApplyStore()
	result, err := WrapAs(store, BackendGitHub, "").ApplyPlan(testCtx, nil, ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 0 || len(result.Mapping) != 0 || len(store.labels) != 0 {
		t.Errorf("result = %+v labels = %v, want no work", result, store.labels)
	}
	if result.Mapping == nil || result.WouldCreate == nil || result.AlreadyCreated == nil {
		t.Errorf("empty result is missing its non-nil collections: %+v", result)
	}
}

func TestApplyPlanDryRunWritesNothing(t *testing.T) {
	store := newApplyStore(fixture(42, "Existing dep", "P2", "task"), fixture(7, "Source", "P2", "task"))
	tr := WrapAs(store, BackendGitHub, "")
	digest := "sha256:test"
	first, err := tr.ApplyPlan(testCtx, []PlanEntry{{ID: "epic", Title: "Voltgo support", Type: PlanTypeEpic}}, ApplyOptions{Digest: digest})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := NewMemoryCheckpoint()
	if err := checkpoint.RecordCreated("epic", first.Mapping["epic"]); err != nil {
		t.Fatal(err)
	}

	before := len(store.issues)
	store.created, store.edges, store.labels = nil, nil, nil
	issuesBefore := slices.Clone(store.issues)
	result, err := tr.ApplyPlan(testCtx, applyPlanFixture(), ApplyOptions{DryRun: true, Checkpoint: checkpoint, Digest: digest})
	if err != nil {
		t.Fatal(err)
	}
	if !result.DryRun {
		t.Error("dry run result does not say so")
	}
	if result.AlreadyCreated["epic"] != first.Mapping["epic"] {
		t.Errorf("alreadyCreated = %v, want the checkpointed epic", result.AlreadyCreated)
	}
	if len(result.WouldCreate) != 2 {
		t.Fatalf("wouldCreate = %+v, want the two pending entries", result.WouldCreate)
	}
	if result.WouldCreate[0].Key != "scaffold" || result.WouldCreate[0].Parent == nil || *result.WouldCreate[0].Parent != "epic" {
		t.Errorf("planned scaffold = %+v", result.WouldCreate[0])
	}
	if result.WouldCreate[1].Priority != "P3" || len(result.WouldCreate[1].BlockedBy) != 2 {
		t.Errorf("planned collector = %+v", result.WouldCreate[1])
	}
	if len(store.created) != 0 || len(store.edges) != 0 {
		t.Errorf("dry run wrote: created %v edges %v", store.created, store.edges)
	}
	if len(store.labels) != 0 {
		t.Errorf("dry run bootstrapped labels: %v", store.labels)
	}
	if len(store.issues) != before || !reflect.DeepEqual(issuesBefore, store.issues) {
		t.Error("dry run changed the store")
	}
}

// A dry run is the pass a caller reads before the real one, so a reference the
// real run would refuse must refuse it too.
func TestApplyPlanDryRunChecksExistingReferences(t *testing.T) {
	store := newApplyStore()
	tr := WrapAs(store, BackendGitHub, "")
	entries := []PlanEntry{{ID: "orphan", Title: "Orphan", Type: "task", Parent: &PlanRef{Number: 9999}}}
	_, err := tr.ApplyPlan(testCtx, entries, ApplyOptions{DryRun: true})
	if err == nil || !strings.Contains(err.Error(), "line 1") || !strings.Contains(err.Error(), "#9999") {
		t.Fatalf("err = %v, want a refusal naming line 1 and #9999", err)
	}
}

func TestApplyPlanChecksEachExistingReferenceOnce(t *testing.T) {
	store := newApplyStore(fixture(42, "Existing dep", "P2", "task"))
	tr := WrapAs(store, BackendGitHub, "")
	entries := []PlanEntry{
		{ID: "a", Title: "A", Type: "task", BlockedBy: []PlanRef{{Number: 42}}},
		{ID: "b", Title: "B", Type: "task", BlockedBy: []PlanRef{{Number: 42}}, DiscoveredFrom: 42},
	}
	if _, err := tr.ApplyPlan(testCtx, entries, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := store.getCalls(42); got != 1 {
		t.Errorf("GetIssue 42 called %d times, want 1", got)
	}
}

func TestApplyPlanRefusesACheckpointItCannotVouchFor(t *testing.T) {
	entries := []PlanEntry{{ID: "a", Title: "A", Type: "task"}}
	tests := map[string]struct {
		seed func(*MemoryCheckpoint)
		want string
	}{
		"unknown key": {
			seed: func(c *MemoryCheckpoint) { c.mapping["ghost"] = 1 },
			want: "is not in this run's selection",
		},
		"missing mapped issue": {
			seed: func(c *MemoryCheckpoint) { c.mapping["a"] = 999 },
			want: "verifying mapped issue #999",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			store := newApplyStore()
			checkpoint := NewMemoryCheckpoint()
			tc.seed(checkpoint)
			_, err := WrapAs(store, BackendGitHub, "").ApplyPlan(testCtx, entries, ApplyOptions{Checkpoint: checkpoint, Digest: "sha256:test"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if len(store.created) != 0 {
				t.Errorf("created %v despite a rejected checkpoint", store.created)
			}
		})
	}
}

func TestApplyPlanRejectsAMappingWithoutItsProvenance(t *testing.T) {
	store := newApplyStore(fixture(42, "Unrelated", "P2", "task"))
	checkpoint := NewMemoryCheckpoint()
	checkpoint.mapping["a"] = 42
	_, err := WrapAs(store, BackendGitHub, "").ApplyPlan(testCtx,
		[]PlanEntry{{ID: "a", Title: "A", Type: "task"}},
		ApplyOptions{Checkpoint: checkpoint, Digest: "sha256:test"})
	if err == nil || !strings.Contains(err.Error(), "does not carry the hew provenance marker") {
		t.Fatalf("err = %v, want a provenance rejection", err)
	}
	if len(store.created) != 0 || len(store.edges) != 0 {
		t.Errorf("the rejected run wrote: created %v edges %v", store.created, store.edges)
	}
}

func TestApplyPlanReportsAnUnwiredEdgeAndRetriesIt(t *testing.T) {
	store := newApplyStore(fixture(42, "Existing dep", "P2", "task"))
	tr := WrapAs(store, BackendGitHub, "")
	entries := []PlanEntry{{ID: "a", Title: "A", Type: "task", BlockedBy: []PlanRef{{Number: 42}}}}
	checkpoint := NewMemoryCheckpoint()
	opts := ApplyOptions{Checkpoint: checkpoint}
	store.edgeErr = errors.New("boom")

	result, err := tr.ApplyPlan(testCtx, entries, opts)
	if err != nil {
		t.Fatalf("an unwired edge should be reported, not returned as an error: %v", err)
	}
	if result.Unwired != 1 || len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "blocked-by edge") {
		t.Fatalf("result = %+v, want one unwired edge warning", result)
	}

	store.edgeErr = nil
	result, err = tr.ApplyPlan(testCtx, entries, opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 0 || result.Wired != 1 || result.Unwired != 0 || len(result.Warnings) != 0 {
		t.Errorf("retry result = %+v, want the edge wired and nothing else", result)
	}
	if got := countStrings(store.created, "A"); got != 1 {
		t.Errorf("retry created A %d times, want once", got)
	}
}

func TestApplyPlanKeysEntriesWithoutIDsByPosition(t *testing.T) {
	store := newApplyStore()
	tr := WrapAs(store, BackendGitHub, "")
	entries := []PlanEntry{
		{Title: "One", Type: "task"},
		{Title: "Two", Type: "task"},
	}
	result, err := tr.ApplyPlan(testCtx, entries, ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.Mapping["line:1"]; !ok {
		t.Errorf("mapping = %v, want the first entry keyed by its position", result.Mapping)
	}
	if _, ok := result.Mapping["line:2"]; !ok {
		t.Errorf("mapping = %v, want the second entry keyed by its position", result.Mapping)
	}
}

func TestApplyPlanRefusesInvalidPendingFields(t *testing.T) {
	tests := map[string]struct {
		entry PlanEntry
		want  string
	}{
		"whitespace title": {PlanEntry{Title: "   ", Type: "task"}, "must not be empty"},
		"multi-line title": {PlanEntry{Title: "first\nsecond", Type: "task"}, "single line"},
		"over-long title":  {PlanEntry{Title: strings.Repeat("a", 257), Type: "task"}, "256"},
		"epic prefix pushes the title over": {
			PlanEntry{Title: strings.Repeat("a", 251), Type: PlanTypeEpic}, "256",
		},
		"comma in an area": {PlanEntry{Title: "x", Type: "task", Areas: []string{"x,y"}}, "comma"},
		"whitespace area":  {PlanEntry{Title: "x", Type: "task", Areas: []string{"   "}}, "must not be blank"},
		"over-long area":   {PlanEntry{Title: "x", Type: "task", Areas: []string{strings.Repeat("a", 51)}}, "50"},
		// Padding slips past ValidatePlan's untrimmed check, so these exercise
		// the convention-label rule on the trimmed name.
		"padded priority area": {PlanEntry{Title: "x", Type: "task", Areas: []string{" P1 "}}, "priority label"},
		"padded type area":     {PlanEntry{Title: "x", Type: "task", Areas: []string{" bug "}}, "type label"},
		"padded claim area":    {PlanEntry{Title: "x", Type: "task", Areas: []string{" in-progress "}}, "reserved convention label"},
		"padded epic area":     {PlanEntry{Title: "x", Type: "task", Areas: []string{" epic "}}, "reserved convention label"},
		// The idea reservation is enforced here rather than in ValidatePlan,
		// so a fresh plan is refused before its first write like any other.
		"idea area on a fresh plan": {PlanEntry{Title: "x", Type: "task", Areas: []string{"idea"}}, "type label"},
		"padded idea area":          {PlanEntry{Title: "x", Type: "task", Areas: []string{" idea "}}, "type label"},
		"mixed-case idea area":      {PlanEntry{Title: "x", Type: "task", Areas: []string{"IDEA"}}, "type label"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			store := newApplyStore()
			_, err := WrapAs(store, BackendGitHub, "").ApplyPlan(testCtx, []PlanEntry{tc.entry}, ApplyOptions{})
			var invalid *InvalidInputError
			if !errors.As(err, &invalid) || invalid.Field != "plan" {
				t.Fatalf("err = %v, want an *InvalidInputError naming the plan", err)
			}
			if !strings.HasPrefix(invalid.Reason, "line 1: ") || !strings.Contains(invalid.Reason, tc.want) {
				t.Errorf("reason = %q, want the entry position and %q", invalid.Reason, tc.want)
			}
			if store.createCalls != 0 || len(store.created) != 0 {
				t.Errorf("refused plan created %d issues", store.createCalls)
			}
			if len(store.labels) != 0 {
				t.Errorf("refused plan bootstrapped labels: %v", store.labels)
			}
		})
	}
}

// The direct API's areas go through the same trim and de-duplication the CLI's
// flags do, so a plan built in code does not create differently spelled labels.
func TestApplyPlanNormalizesPendingEntryAreas(t *testing.T) {
	store := newApplyStore()
	entries := []PlanEntry{{ID: "a", Title: "A", Type: "task", Areas: []string{" api ", "api", " ui "}}}
	if _, err := WrapAs(store, BackendGitHub, "").ApplyPlan(testCtx, entries, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	issue := store.byTitle("A")
	if issue == nil {
		t.Fatal("issue was not created")
	}
	for _, want := range []string{"api", "ui"} {
		if got := countStrings(issue.Labels, want); got != 1 {
			t.Errorf("labels = %v, want %q exactly once", issue.Labels, want)
		}
	}
	if slices.Contains(issue.Labels, " api ") {
		t.Errorf("labels = %v, want the untrimmed spelling gone", issue.Labels)
	}
}

// An entry the checkpoint already maps was created under a previous release's
// rules; re-gating it would strand a resume whose source digest cannot change
// to repair it. Only the pending entry gets the write path's field rules.
func TestApplyPlanSkipsFieldValidationForCheckpointedEntries(t *testing.T) {
	store := newApplyStore()
	entries := normalizePlan([]PlanEntry{
		{ID: "legacy", Title: "First\nsecond", Type: "task", Areas: []string{strings.Repeat("a", 80)}},
		{ID: "next", Title: "Next", Type: "task"},
	})
	digest, err := planDigest(entries)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateIssue(testCtx, entries[0].Title, PlanBodyWithProvenance(entries[0], digest), []string{"P2", "task"})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := NewMemoryCheckpoint()
	if err := checkpoint.RecordCreated("legacy", created.Number); err != nil {
		t.Fatal(err)
	}

	result, err := WrapAs(store, BackendGitHub, "").ApplyPlan(testCtx, entries, ApplyOptions{Checkpoint: checkpoint})
	if err != nil {
		t.Fatalf("checkpointed entry was re-gated: %v", err)
	}
	if result.Created != 1 {
		t.Errorf("created = %d, want only the pending entry", result.Created)
	}
	if result.Mapping["legacy"] != created.Number {
		t.Errorf("mapping = %v, want the checkpointed issue kept", result.Mapping)
	}
	if store.byTitle("Next") == nil {
		t.Error("pending entry was not created after the checkpointed one was skipped")
	}
}

// A plan written before `idea` was a type could name it as an area on a task.
// When the checkpoint maps that entry to the issue the earlier build created,
// the reservation must not be re-gated: the task keeps its number and labels,
// only pending entries are created, and pending edges are still wired.
func TestApplyPlanResumesAPreUpgradeIdeaAreaCheckpoint(t *testing.T) {
	store := newApplyStore(fixture(55, "Legacy idea-area task", "P2", "task", "idea"))
	entries := normalizePlan([]PlanEntry{
		{ID: "legacy", Title: "Legacy idea-area task", Type: "task", Areas: []string{"idea"}},
		{ID: "pending", Title: "Pending work", Type: "task", BlockedBy: []PlanRef{{ID: "legacy"}}},
	})
	digest, err := planDigest(entries)
	if err != nil {
		t.Fatal(err)
	}
	legacy := store.byTitle("Legacy idea-area task")
	legacy.Body = PlanBodyWithProvenance(entries[0], digest)
	checkpoint := NewMemoryCheckpoint()
	if err := checkpoint.RecordCreated("legacy", legacy.Number); err != nil {
		t.Fatal(err)
	}

	result, err := WrapAs(store, BackendGitHub, "").ApplyPlan(testCtx, entries, ApplyOptions{Checkpoint: checkpoint})
	if err != nil {
		t.Fatalf("pre-upgrade checkpoint did not resume: %v", err)
	}
	if result.Created != 1 || len(store.created) != 1 || store.created[0] != "Pending work" {
		t.Errorf("created %d %v, want only the pending entry", result.Created, store.created)
	}
	if result.Mapping["legacy"] != 55 {
		t.Errorf("mapping = %v, want the legacy issue kept", result.Mapping)
	}
	if got := store.byTitle("Legacy idea-area task"); got.Number != 55 || !slices.Equal(got.Labels, []string{"P2", "task", "idea"}) {
		t.Errorf("legacy issue = #%d %v, want #55 with its stored labels", got.Number, got.Labels)
	}
	pending := store.byTitle("Pending work")
	if pending == nil {
		t.Fatal("pending entry was not created")
	}
	if len(pending.BlockedBy) != 1 || pending.BlockedBy[0].Number != 55 {
		t.Errorf("pending blockers = %v, want the legacy issue", pending.BlockedBy)
	}
}

// A checkpoint that does not carry this plan's provenance for the entry is
// refused by the existing binding check, idea-area entry or not.
func TestApplyPlanRejectsAMismatchedIdeaAreaCheckpoint(t *testing.T) {
	store := newApplyStore(fixture(55, "Unrelated", "P2", "task"))
	entries := normalizePlan([]PlanEntry{
		{ID: "legacy", Title: "Legacy idea-area task", Type: "task", Areas: []string{"idea"}},
	})
	checkpoint := NewMemoryCheckpoint()
	if err := checkpoint.RecordCreated("legacy", 55); err != nil {
		t.Fatal(err)
	}
	_, err := WrapAs(store, BackendGitHub, "").ApplyPlan(testCtx, entries, ApplyOptions{Checkpoint: checkpoint})
	if err == nil || !strings.Contains(err.Error(), "does not carry the hew provenance marker") {
		t.Fatalf("err = %v, want the existing provenance rejection", err)
	}
	if store.createCalls != 0 {
		t.Errorf("refused plan created %d issues", store.createCalls)
	}
}

// preQuestionsPlanJSON is json.Marshal's output for the plan below with the
// PlanEntry and Sections shapes that predate the optional questions field.
// ApplyPlan derives a code-built plan's digest by marshaling the normalized
// entries, so that optional field must be omitted when unset: a checkpoint
// whose provenance markers were written before the field existed has to keep
// resuming after the upgrade.
const preQuestionsPlanJSON = `[{"ID":"one","Title":"One","Type":"task","Priority":"P2","Areas":null,"Body":"","Sections":{"Where":"","Problem":"","Goal":"","Fix":"","Approach":"","DoneWhen":null},"Parent":null,"BlockedBy":null,"DiscoveredFrom":0,"Line":1},{"ID":"two","Title":"Two","Type":"task","Priority":"P3","Areas":null,"Body":"","Sections":{"Where":"","Problem":"","Goal":"","Fix":"","Approach":"","DoneWhen":null},"Parent":null,"BlockedBy":null,"DiscoveredFrom":0,"Line":2}]`

func TestApplyPlanDerivedDigestMatchesAPreQuestionsPlan(t *testing.T) {
	entries := []PlanEntry{
		{ID: "one", Title: "One", Type: "task", Priority: "P2"},
		{ID: "two", Title: "Two", Type: "task", Priority: "P3"},
	}
	// Nothing here supplies a digest: the plan's derived one is under test.
	want := FileDigest([]byte(preQuestionsPlanJSON))
	derived, err := planDigest(normalizePlan(entries))
	if err != nil {
		t.Fatal(err)
	}
	if derived != want {
		t.Fatalf("derived digest = %s, want the pre-questions plan's %s; an optional unset field changed the plan's identity", derived, want)
	}

	store := newApplyStore(fixture(55, "One", "P2", "task"))
	one := store.byTitle("One")
	one.Body = PlanBodyWithProvenance(normalizePlan(entries)[0], want)
	checkpoint := NewMemoryCheckpoint()
	if err := checkpoint.RecordCreated("one", one.Number); err != nil {
		t.Fatal(err)
	}
	result, err := WrapAs(store, BackendGitHub, "").ApplyPlan(testCtx, entries, ApplyOptions{Checkpoint: checkpoint})
	if err != nil {
		t.Fatalf("pre-upgrade checkpoint did not resume: %v", err)
	}
	if result.Created != 1 || len(store.created) != 1 || store.created[0] != "Two" {
		t.Errorf("created %d %v, want only the pending entry", result.Created, store.created)
	}
	if result.Mapping["one"] != one.Number {
		t.Errorf("mapping = %v, want the pre-upgrade issue kept", result.Mapping)
	}
}

func TestValidatePlan(t *testing.T) {
	valid := PlanEntry{Title: "x", Type: "task"}
	tests := map[string]struct {
		entries []PlanEntry
		want    string
	}{
		"title":                     {[]PlanEntry{{Type: "task"}}, "title is required"},
		"type":                      {[]PlanEntry{{Title: "x"}}, "type must be one of bug|enhancement|task|idea|epic"},
		"idea is a type":            {[]PlanEntry{{Title: "x", Type: model.IdeaType}}, ""},
		"idea area is deferred":     {[]PlanEntry{{Title: "x", Type: "task", Areas: []string{"idea"}}}, ""},
		"bad type":                  {[]PlanEntry{{Title: "x", Type: "feature"}}, "type must be"},
		"priority":                  {[]PlanEntry{{Title: "x", Type: "task", Priority: "P9"}}, "priority must be P0..P4"},
		"convention area":           {[]PlanEntry{{Title: "x", Type: "task", Areas: []string{"P1"}}}, `area "P1" collides`},
		"negative discovered":       {[]PlanEntry{{Title: "x", Type: "task", DiscoveredFrom: -1}}, "discovered-from"},
		"problem and goal":          {[]PlanEntry{{Title: "x", Type: "task", Sections: Sections{Problem: "p", Goal: "g"}}}, "problem and goal are mutually exclusive"},
		"fix and approach":          {[]PlanEntry{{Title: "x", Type: "task", Sections: Sections{Fix: "f", Approach: "a"}}}, "fix and approach are mutually exclusive"},
		"empty done-when":           {[]PlanEntry{{Title: "x", Type: "task", Sections: Sections{DoneWhen: []string{" "}}}}, "done-when items cannot be empty"},
		"body and sections":         {[]PlanEntry{{Title: "x", Type: "task", Body: "b", Sections: Sections{Goal: "g"}}}, "body and section fields"},
		"duplicate id":              {[]PlanEntry{{ID: "a", Title: "x", Type: "task"}, {ID: "a", Title: "y", Type: "task"}}, `duplicate id "a"`},
		"reserved id":               {[]PlanEntry{{ID: "line:9", Title: "x", Type: "task"}}, "reserved"},
		"unknown parent":            {[]PlanEntry{{Title: "x", Type: "task", Parent: &PlanRef{ID: "nope"}}}, `parent "nope" does not match any entry id`},
		"unknown blocker":           {[]PlanEntry{{Title: "x", Type: "task", BlockedBy: []PlanRef{{ID: "nope"}}}}, `blocked-by "nope" does not match any entry id`},
		"self parent":               {[]PlanEntry{{ID: "a", Title: "x", Type: "task", Parent: &PlanRef{ID: "a"}}}, "cannot be its own parent"},
		"self blocker":              {[]PlanEntry{{ID: "a", Title: "x", Type: "task", BlockedBy: []PlanRef{{ID: "a"}}}}, "cannot be its own blocked-by"},
		"duplicate blocker":         {[]PlanEntry{{ID: "a", Title: "x", Type: "task"}, {ID: "b", Title: "y", Type: "task", BlockedBy: []PlanRef{{ID: "a"}, {ID: "a"}}}}, "duplicate blocked-by a"},
		"dependency cycle":          {[]PlanEntry{{ID: "a", Title: "x", Type: "task", BlockedBy: []PlanRef{{ID: "b"}}}, {ID: "b", Title: "y", Type: "task", BlockedBy: []PlanRef{{ID: "a"}}}}, "dependency cycle in plan"},
		"parent cycle":              {[]PlanEntry{{ID: "a", Title: "x", Type: "task", Parent: &PlanRef{ID: "b"}}, {ID: "b", Title: "y", Type: "task", Parent: &PlanRef{ID: "a"}}}, "parent cycle in plan"},
		"valid entry":               {[]PlanEntry{valid}, ""},
		"empty priority is P2":      {[]PlanEntry{{Title: "x", Type: "task", Priority: ""}}, ""},
		"epic is a valid type":      {[]PlanEntry{{Title: "x", Type: PlanTypeEpic}}, ""},
		"duplicate numeric blocker": {[]PlanEntry{{Title: "x", Type: "task", BlockedBy: []PlanRef{{Number: 1}, {Number: 1}}}}, "duplicate blocked-by #1"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := ValidatePlan(tc.entries)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("ValidatePlan = %v, want no error", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestPlanTitleAndBody(t *testing.T) {
	epic := PlanEntry{Title: "Voltgo", Type: PlanTypeEpic}
	if got := PlanTitle(epic); got != "Epic: Voltgo" {
		t.Errorf("PlanTitle = %q", got)
	}
	prefixed := PlanEntry{Title: "Epic: Voltgo", Type: PlanTypeEpic}
	if got := PlanTitle(prefixed); got != "Epic: Voltgo" {
		t.Errorf("PlanTitle prefixed twice: %q", got)
	}
	entry := PlanEntry{
		Title: "x", Type: "task",
		Sections:       Sections{Goal: "ship", DoneWhen: []string{"tests pass"}},
		DiscoveredFrom: 7,
	}
	want := "### Goal\n\nship\n\n### Done when\n\n- [ ] tests pass\n\n" + conventions.DiscoveredFrom(7)
	if got := PlanBody(entry); got != want {
		t.Errorf("PlanBody = %q, want %q", got, want)
	}
	body := PlanBodyWithProvenance(entry, "sha256:test")
	if !strings.HasSuffix(body, conventions.ProvenanceMarker(conventions.ProvenanceApply, entry.Key(), "sha256:test")) {
		t.Errorf("PlanBodyWithProvenance = %q, want the marker", body)
	}
}

// getCalls counts the reads of one issue the fake recorded. fakeClient here
// keeps no call log, so the store counts through the embedded GetIssue by
// inspecting the issue set is not enough; the test wraps GetIssue instead.
func (s *applyStore) getCalls(number int) int {
	count := 0
	for _, n := range s.getIssueCalls {
		if n == number {
			count++
		}
	}
	return count
}

func countStrings(values []string, want string) int {
	count := 0
	for _, value := range values {
		if value == want {
			count++
		}
	}
	return count
}

func TestApplyPlanTypesItsFailuresAtTheBoundary(t *testing.T) {
	t.Run("a missing parent is a NotFoundError", func(t *testing.T) {
		tr := WrapAs(newApplyStore(), BackendGitHub, "")
		entries := []PlanEntry{{ID: "orphan", Title: "Orphan", Type: "task", Parent: &PlanRef{Number: 9999}}}
		_, err := tr.ApplyPlan(testCtx, entries, ApplyOptions{})
		var notFound *NotFoundError
		if !errors.As(err, &notFound) || notFound.Issue != 9999 {
			t.Fatalf("err = %v, want a *NotFoundError for #9999", err)
		}
	})
	t.Run("an auth failure on create is an AuthError", func(t *testing.T) {
		store := newApplyStore()
		store.createErr = &gh.AuthError{Err: errors.New("bad credentials")}
		_, err := WrapAs(store, BackendGitHub, "").ApplyPlan(testCtx, []PlanEntry{{ID: "a", Title: "A", Type: "task"}}, ApplyOptions{})
		var auth *AuthError
		if !errors.As(err, &auth) {
			t.Fatalf("err = %v, want a *AuthError", err)
		}
	})
	t.Run("a rejected plan is an InvalidInputError", func(t *testing.T) {
		tr := WrapAs(newApplyStore(), BackendGitHub, "")
		_, err := tr.ApplyPlan(testCtx, []PlanEntry{{Type: "task"}}, ApplyOptions{})
		var invalid *InvalidInputError
		if !errors.As(err, &invalid) || invalid.Field != "plan" || !strings.Contains(invalid.Reason, "title is required") {
			t.Fatalf("err = %v, want an *InvalidInputError naming the missing title", err)
		}
	})
}

// A fresh plan never introduces an idea into the epic hierarchy — as a child
// or as a parent, through a local id or an existing issue number — and the
// refusal names the plan line before anything is created.
func TestApplyPlanRefusesFreshIdeaHierarchyPlans(t *testing.T) {
	tests := map[string]struct {
		entries []PlanEntry
		want    string // the rule the refusal names
	}{
		"idea child": {[]PlanEntry{
			{ID: "epic", Title: "Voltgo support", Type: PlanTypeEpic},
			{ID: "idea", Title: "Consider a cache", Type: model.IdeaType, Parent: &PlanRef{ID: "epic"}},
		}, "an idea cannot be an epic child"},
		"idea parent": {[]PlanEntry{
			{ID: "idea", Title: "Consider a cache", Type: model.IdeaType},
			{ID: "work", Title: "Add a cache", Type: "task", Parent: &PlanRef{ID: "idea"}},
		}, "an idea cannot be an epic parent"},
		"existing idea parent": {[]PlanEntry{
			{ID: "work", Title: "Add a cache", Type: "task", Parent: &PlanRef{Number: 55}},
		}, "#55 is an idea; an idea cannot be an epic parent"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			store := newApplyStore(fixture(55, "Consider a cache", "P2", model.IdeaType))
			_, err := WrapAs(store, BackendGitHub, "").ApplyPlan(testCtx, tc.entries, ApplyOptions{})
			var invalid *InvalidInputError
			if !errors.As(err, &invalid) || invalid.Field != "plan" {
				t.Fatalf("err = %v, want an *InvalidInputError naming the plan", err)
			}
			if !strings.Contains(invalid.Reason, tc.want) || !strings.Contains(invalid.Reason, "line ") {
				t.Errorf("reason = %q, want the plan line and %q", invalid.Reason, tc.want)
			}
			if strings.Contains(invalid.Reason, "#0") {
				t.Errorf("reason = %q, names an uncreated parent as #0", invalid.Reason)
			}
			if store.createCalls != 0 || len(store.created) != 0 {
				t.Errorf("refused plan created %d issue(s): %v", store.createCalls, store.created)
			}
		})
	}
}

// A relationship the checkpoint already recorded as wired stays put: an idea
// attached before the rule existed must not block the rest of its resume.
func TestApplyPlanResumesAWiredIdeaChild(t *testing.T) {
	entries := normalizePlan([]PlanEntry{
		{ID: "epic", Title: "Voltgo support", Type: PlanTypeEpic},
		{ID: "idea", Title: "Consider a cache", Type: model.IdeaType, Parent: &PlanRef{ID: "epic"}},
	})
	digest, err := planDigest(entries)
	if err != nil {
		t.Fatal(err)
	}
	epic := fixture(40, "Epic: Voltgo support", "P2", model.EpicLabel)
	epic.Body = PlanBodyWithProvenance(entries[0], digest)
	epic.SubIssues = []model.Ref{{Number: 55, State: "OPEN"}}
	epic.SubIssuesTotal = 1
	idea := fixture(55, "Consider a cache", "P2", model.IdeaType)
	idea.Body = PlanBodyWithProvenance(entries[1], digest)
	idea.Parent = &model.Ref{Number: 40, State: "OPEN"}

	store := newApplyStore(epic, idea)
	checkpoint := NewMemoryCheckpoint()
	for key, number := range map[string]int{"epic": 40, "idea": 55} {
		if err := checkpoint.RecordCreated(key, number); err != nil {
			t.Fatal(err)
		}
	}
	if err := checkpoint.RecordWired(ParentEdge, 55, 40); err != nil {
		t.Fatal(err)
	}

	result, err := WrapAs(store, BackendGitHub, "").ApplyPlan(testCtx, entries, ApplyOptions{Checkpoint: checkpoint})
	if err != nil {
		t.Fatalf("resume over a wired idea edge: %v", err)
	}
	if result.Created != 0 || result.Wired != 0 {
		t.Errorf("result = %+v, want no writes on a fully recorded resume", result)
	}
	if got := store.byTitle("Consider a cache"); got == nil || got.Parent == nil || got.Parent.Number != 40 {
		t.Errorf("idea = %+v, want its recorded parent kept", got)
	}
}

// A recorded edge costs no read: a resume must not fail over an existing
// parent that can no longer be read, the way requirePlanRefs skips entries it
// already created.
func TestApplyPlanResumeSkipsReadingAWiredParent(t *testing.T) {
	entries := normalizePlan([]PlanEntry{
		{ID: "work", Title: "Add a cache", Type: "task", Parent: &PlanRef{Number: 12}},
	})
	digest, err := planDigest(entries)
	if err != nil {
		t.Fatal(err)
	}
	work := fixture(55, "Add a cache", "P2", "task")
	work.Body = PlanBodyWithProvenance(entries[0], digest)
	store := newApplyStore(work) // #12 is gone

	checkpoint := NewMemoryCheckpoint()
	if err := checkpoint.RecordCreated("work", 55); err != nil {
		t.Fatal(err)
	}
	if err := checkpoint.RecordWired(ParentEdge, 55, 12); err != nil {
		t.Fatal(err)
	}
	if _, err := WrapAs(store, BackendGitHub, "").ApplyPlan(testCtx, entries, ApplyOptions{Checkpoint: checkpoint}); err != nil {
		t.Fatalf("resume over a wired edge to an unreadable parent: %v", err)
	}
}

// On resume only pending parent relationships are validated, using each
// endpoint's stored normalized type. An attachment still pending that touches
// an idea fails before the run creates anything.
func TestApplyPlanRefusesAPendingIdeaEdgeOnResume(t *testing.T) {
	t.Run("a checkpointed idea child awaiting its epic", func(t *testing.T) {
		entries := normalizePlan([]PlanEntry{
			{ID: "epic", Title: "Voltgo support", Type: PlanTypeEpic},
			{ID: "idea", Title: "Consider a cache", Type: model.IdeaType, Parent: &PlanRef{ID: "epic"}},
		})
		digest, err := planDigest(entries)
		if err != nil {
			t.Fatal(err)
		}
		idea := fixture(55, "Consider a cache", "P2", model.IdeaType)
		idea.Body = PlanBodyWithProvenance(entries[1], digest)

		store := newApplyStore(idea)
		checkpoint := NewMemoryCheckpoint()
		if err := checkpoint.RecordCreated("idea", 55); err != nil {
			t.Fatal(err)
		}
		_, err = WrapAs(store, BackendGitHub, "").ApplyPlan(testCtx, entries, ApplyOptions{Checkpoint: checkpoint})
		var invalid *InvalidInputError
		if !errors.As(err, &invalid) || invalid.Field != "plan" || !strings.Contains(invalid.Reason, "idea") {
			t.Fatalf("err = %v, want an InvalidInputError naming the pending idea edge", err)
		}
		if store.createCalls != 0 {
			t.Errorf("refused resume created %d issue(s)", store.createCalls)
		}
	})

	t.Run("a pending child of a checkpointed idea", func(t *testing.T) {
		entries := normalizePlan([]PlanEntry{
			{ID: "idea", Title: "Consider a cache", Type: model.IdeaType},
			{ID: "work", Title: "Add a cache", Type: "task", Parent: &PlanRef{ID: "idea"}},
		})
		digest, err := planDigest(entries)
		if err != nil {
			t.Fatal(err)
		}
		idea := fixture(55, "Consider a cache", "P2", model.IdeaType)
		idea.Body = PlanBodyWithProvenance(entries[0], digest)

		store := newApplyStore(idea)
		checkpoint := NewMemoryCheckpoint()
		if err := checkpoint.RecordCreated("idea", 55); err != nil {
			t.Fatal(err)
		}
		_, err = WrapAs(store, BackendGitHub, "").ApplyPlan(testCtx, entries, ApplyOptions{Checkpoint: checkpoint})
		var invalid *InvalidInputError
		if !errors.As(err, &invalid) || invalid.Field != "plan" || !strings.Contains(invalid.Reason, "cannot be an epic parent") {
			t.Fatalf("err = %v, want an InvalidInputError naming the idea parent", err)
		}
		if store.createCalls != 0 {
			t.Errorf("refused resume created %d issue(s)", store.createCalls)
		}
	})
}
