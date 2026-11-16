package migrate

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lumberbarons/hew/internal/conventions"
	"github.com/lumberbarons/hew/tracker"
)

// engineFixture is one engine run over a fresh state file, plus what a rerun
// needs to resume it.
type engineFixture struct {
	target *fakeTarget
	path   string
	req    RunRequest
	eng    *Migrator
}

func newEngine(t *testing.T, items ...Item) *engineFixture {
	t.Helper()
	f := newFakeTarget()
	path := filepath.Join(t.TempDir(), "state.json")
	checkpoint, err := tracker.OpenFileCheckpoint(path, "o/r", "sha256:test", "snapshot")
	if err != nil {
		t.Fatal(err)
	}
	return &engineFixture{
		target: f,
		path:   path,
		req: RunRequest{
			Items: items,
			Options: Options{
				Kind:       conventions.ProvenanceMigrate,
				ItemNoun:   "item",
				SourceNoun: "source",
				KeyNoun:    "item key",
			},
		},
		eng: Wrap(f, checkpoint),
	}
}

// resume reopens the checkpoint the fixture wrote, as a rerun does.
func (e *engineFixture) resume(t *testing.T) {
	t.Helper()
	checkpoint, err := tracker.OpenFileCheckpoint(e.path, "o/r", "sha256:test", "snapshot")
	if err != nil {
		t.Fatal(err)
	}
	e.eng = Wrap(e.target, checkpoint)
}

func (e *engineFixture) run(t *testing.T) Result {
	t.Helper()
	res, err := e.eng.Run(context.Background(), e.req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestEngineClosesWithTheItemsReasonAndComments(t *testing.T) {
	e := newEngine(t,
		Item{Key: "a", Title: "Dropped", Closed: true, CloseReason: CloseNotPlanned, Comments: []string{"first", "second"}},
		Item{Key: "b", Title: "Done", Closed: true},
	)
	res := e.run(t)
	if res.Created != 2 || res.Closed != 2 {
		t.Fatalf("result = %+v, want 2 created and 2 closed", res)
	}
	a, b := res.Mapping["a"], res.Mapping["b"]
	if got := e.target.byNumber(a).StateReason; got != string(CloseNotPlanned) {
		t.Errorf("a closed as %q, want the item's %q", got, CloseNotPlanned)
	}
	if got := e.target.byNumber(b).StateReason; got != string(CloseCompleted) {
		t.Errorf("b closed as %q, want %q when the item names no reason", got, CloseCompleted)
	}
	if got := e.target.comments[a]; !slices.Equal(got, []string{"first", "second"}) {
		t.Errorf("a comments = %q, want both in order", got)
	}
	if got := e.target.comments[b]; len(got) != 0 {
		t.Errorf("b got comments %q, but its item carried none", got)
	}
}

func TestEnginePostsCommentsOnOpenItemsExactlyOnce(t *testing.T) {
	e := newEngine(t, Item{Key: "a", Title: "Open with notes", Comments: []string{"one", "two"}})
	res := e.run(t)
	n := res.Mapping["a"]
	if got := e.target.comments[n]; !slices.Equal(got, []string{"one", "two"}) {
		t.Fatalf("comments = %q, want both in order", got)
	}
	if res.Closed != 0 || e.target.byNumber(n).State != "OPEN" {
		t.Errorf("closed=%d state=%s, want the item left open", res.Closed, e.target.byNumber(n).State)
	}

	// The issue is still open, so only the checkpoint stops a resume from
	// posting the notes again.
	e.resume(t)
	e.run(t)
	if got := e.target.comments[n]; len(got) != 2 {
		t.Errorf("resume re-posted comments: %q", got)
	}
}

func TestEngineResumesCommentingWhereItStopped(t *testing.T) {
	e := newEngine(t, Item{Key: "a", Title: "Open", Comments: []string{"one", "two", "three"}})
	e.target.failAfter["Comment"] = failPoint{calls: 1, err: errors.New("boom")}
	res, err := e.eng.Run(context.Background(), e.req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed == 0 || len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0].Message, "boom") {
		t.Fatalf("failed=%d warnings=%+v, want the comment failure reported", res.Failed, res.Warnings)
	}
	n := res.Mapping["a"]

	delete(e.target.failAfter, "Comment")
	e.resume(t)
	e.run(t)
	if got := e.target.comments[n]; !slices.Equal(got, []string{"one", "two", "three"}) {
		t.Errorf("comments after resume = %q, want each exactly once, in order", got)
	}
}

func TestEngineRelinksBodiesAndCommentsOnceNumbersAreKnown(t *testing.T) {
	e := newEngine(t,
		Item{Key: "a", Title: "A", Body: "blocks @b", Closed: true, Comments: []string{"see @b"}},
		Item{Key: "b", Title: "B", Body: "after @a and @zzz"},
	)
	e.req.Options.Relink = func(text string, m map[string]int) string {
		for key, n := range m {
			text = strings.ReplaceAll(text, "@"+key, fmt.Sprintf("#%d", n))
		}
		return text
	}
	res := e.run(t)
	a, b := res.Mapping["a"], res.Mapping["b"]
	if res.Relinked != 2 {
		t.Errorf("relinked = %d, want both bodies", res.Relinked)
	}
	wantA := fmt.Sprintf("blocks #%d", b)
	if got := e.target.byNumber(a).Body; !strings.HasPrefix(got, wantA+"\n\n<!-- hew:migrate key=a") {
		t.Errorf("a body = %q, want %q then the provenance marker", got, wantA)
	}
	if got := e.target.byNumber(b).Body; !strings.HasPrefix(got, fmt.Sprintf("after #%d and @zzz\n\n", a)) {
		t.Errorf("b body = %q, want a's number and the unknown key untouched", got)
	}
	if got := e.target.comments[a]; !slices.Equal(got, []string{fmt.Sprintf("see #%d", b)}) {
		t.Errorf("a comments = %q, want the reference rewritten", got)
	}
	if !conventions.HasProvenanceMarker(e.target.byNumber(a).Body, conventions.ProvenanceMigrate, "a", "sha256:test") {
		t.Error("relinking dropped the provenance marker")
	}

	before := len(e.target.calls)
	e.run(t)
	for _, c := range e.target.calls[before:] {
		if strings.HasPrefix(c, "EditBody") || strings.HasPrefix(c, "Comment") {
			t.Errorf("a finished migration wrote again: %s", c)
		}
	}
}

func TestEngineLeavesABodyAloneThatChangedSinceItWasCreated(t *testing.T) {
	e := newEngine(t,
		Item{Key: "a", Title: "A", Body: "see @b"},
		Item{Key: "b", Title: "B"},
	)
	// First pass without a relinker: the bodies stay as the source wrote them.
	res := e.run(t)
	a := res.Mapping["a"]
	e.target.byNumber(a).Body += "\n\nedited by a person"

	e.req.Options.Relink = func(text string, m map[string]int) string { return strings.ReplaceAll(text, "@b", "#7") }
	res = e.run(t)
	if got := e.target.byNumber(a).Body; !strings.Contains(got, "edited by a person") || strings.Contains(got, "#7") {
		t.Errorf("body = %q, want the person's edit kept and no rewrite", got)
	}
	// Nothing a rerun could change, so it must not stop a migration from finishing.
	if res.Relinked != 0 || res.Failed != 0 {
		t.Errorf("relinked=%d failed=%d, want no rewrite and no failure", res.Relinked, res.Failed)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0].Message, "not relinking") || res.Warnings[0].Kind != WarnBodyEdited {
		t.Errorf("warnings = %+v, want the skipped relink listed as body-edited", res.Warnings)
	}
}

func TestEngineCountsFailedRelinksAndCloses(t *testing.T) {
	e := newEngine(t, Item{Key: "a", Title: "A", Body: "see @a", Closed: true})
	e.req.Options.Relink = func(text string, m map[string]int) string { return strings.ReplaceAll(text, "@a", "#1") }
	e.target.failOn["EditBody"] = errors.New("no edit")
	e.target.failOn["CloseIssue"] = errors.New("no close")
	res := e.run(t)
	if res.Failed != 2 || res.Relinked != 0 || res.Closed != 0 {
		t.Errorf("result = %+v, want two failures and nothing relinked or closed", res)
	}
}

func TestEngineDropsABlockedByEdgeThatClosesACycle(t *testing.T) {
	// a→b→c→a: GitHub itself accepts a three-issue cycle, so the engine has
	// to be the one to refuse it. The first two edges are fine; the third is
	// the one that closes the loop.
	e := newEngine(t,
		Item{Key: "a", Title: "A", BlockedBy: []string{"b"}},
		Item{Key: "b", Title: "B", BlockedBy: []string{"c"}},
		Item{Key: "c", Title: "C", BlockedBy: []string{"a"}},
	)
	res := e.run(t)
	a, b, c := res.Mapping["a"], res.Mapping["b"], res.Mapping["c"]
	if res.Wired != 2 {
		t.Errorf("wired %d edges, want 2", res.Wired)
	}
	if !slices.Contains(e.target.calls, fmt.Sprintf("AddBlockedBy %d %d", a, b)) || !slices.Contains(e.target.calls, fmt.Sprintf("AddBlockedBy %d %d", b, c)) {
		t.Errorf("the two safe edges were not wired: %v", e.target.calls)
	}
	if slices.Contains(e.target.calls, fmt.Sprintf("AddBlockedBy %d %d", c, a)) {
		t.Error("wired the edge that closes the cycle")
	}
	want := fmt.Sprintf("blocked-by edge #%d→#%d dropped: would create dependency cycle #%d → #%d → #%d → #%d", c, a, c, a, b, c)
	if len(res.Warnings) != 1 || res.Warnings[0].Message != want || res.Warnings[0].Kind != WarnEdgeDropped {
		t.Errorf("warnings = %+v, want [%q] as edge-dropped", res.Warnings, want)
	}
}

func TestEngineDropsASelfBlock(t *testing.T) {
	e := newEngine(t, Item{Key: "a", Title: "A", BlockedBy: []string{"a"}})
	res := e.run(t)
	if res.Wired != 0 || len(res.Warnings) != 1 {
		t.Errorf("result = %+v, want the self-block dropped with one warning", res)
	}
	for _, call := range e.target.calls {
		if strings.HasPrefix(call, "AddBlockedBy") {
			t.Errorf("attempted a self-block: %s", call)
		}
	}
}

func TestEngineWiresAnAcyclicDiamond(t *testing.T) {
	// d is blocked by b and c, both blocked by a: two paths to the same
	// ancestor are not a cycle.
	e := newEngine(t,
		Item{Key: "a", Title: "A"},
		Item{Key: "b", Title: "B", BlockedBy: []string{"a"}},
		Item{Key: "c", Title: "C", BlockedBy: []string{"a"}},
		Item{Key: "d", Title: "D", BlockedBy: []string{"b", "c"}},
	)
	res := e.run(t)
	if res.Wired != 4 || len(res.Warnings) != 0 {
		t.Errorf("result = %+v, want 4 edges and no warnings", res)
	}
}

func TestEngineAssignsNamedLoginsWithoutLookingUpTheViewer(t *testing.T) {
	e := newEngine(t,
		Item{Key: "a", Title: "A", Assignees: []string{"octocat"}},
		Item{Key: "b", Title: "B", Assignees: []string{AssigneeViewer}},
	)
	res := e.run(t)
	if got := e.target.byNumber(res.Mapping["a"]).Assignees; !slices.Equal(got, []string{"octocat"}) {
		t.Errorf("a assignees = %v, want [octocat]", got)
	}
	if got := e.target.byNumber(res.Mapping["b"]).Assignees; !slices.Equal(got, []string{e.target.viewer}) {
		t.Errorf("b assignees = %v, want the viewer %q", got, e.target.viewer)
	}
	if n := e.target.callCounts["Viewer"]; n != 1 {
		t.Errorf("Viewer called %d times, want once for the one item that asked", n)
	}
}

func TestEngineRejectsDuplicateKeysBeforeWriting(t *testing.T) {
	e := newEngine(t,
		Item{Key: "a", Title: "First"},
		Item{Key: "a", Title: "Second"},
	)
	_, err := e.eng.Run(context.Background(), e.req)
	if err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("err = %v, want a duplicate-key rejection", err)
	}
	var duplicate *DuplicateKeyError
	if !errors.As(err, &duplicate) || duplicate.Key != "a" {
		t.Errorf("err = %T %v, want a *DuplicateKeyError naming a", err, err)
	}
	if len(e.target.calls) != 0 {
		t.Errorf("wrote before rejecting: %v", e.target.calls)
	}
}

func TestEngineRejectsAnItemWithoutAKey(t *testing.T) {
	e := newEngine(t, Item{Key: "", Title: "No key"})
	_, err := e.eng.Run(context.Background(), e.req)
	var itemErr *ItemError
	if !errors.As(err, &itemErr) || itemErr.Field != "key" {
		t.Fatalf("err = %T %v, want an *ItemError naming the key", err, err)
	}
	if len(e.target.calls) != 0 {
		t.Errorf("wrote before rejecting: %v", e.target.calls)
	}
}

// A resume may find an edge already on the target even though this run's
// attempt to wire it failed (a duplicate is answered with an error), so a
// failed edge must still count toward the cycle check.
func TestEngineChecksCyclesThroughEdgesThatFailedToWire(t *testing.T) {
	e := newEngine(t,
		Item{Key: "a", Title: "A", BlockedBy: []string{"b"}},
		Item{Key: "b", Title: "B", BlockedBy: []string{"c"}},
		Item{Key: "c", Title: "C", BlockedBy: []string{"a"}},
	)
	e.target.failOn["AddBlockedBy"] = errors.New("already exists")
	res := e.run(t)
	c, a := res.Mapping["c"], res.Mapping["a"]
	var dropped bool
	for _, w := range res.Warnings {
		if strings.Contains(w.Message, fmt.Sprintf("edge #%d→#%d dropped", c, a)) {
			dropped = true
		}
	}
	if !dropped {
		t.Errorf("c→a was not dropped as a cycle through the failed edges: %+v", res.Warnings)
	}
}

func TestEngineDoesNotRewireEdgesItAlreadyWired(t *testing.T) {
	e := newEngine(t,
		Item{Key: "a", Title: "A"},
		Item{Key: "b", Title: "B", Parent: "a", BlockedBy: []string{"a"}},
	)
	res := e.run(t)
	if res.Wired != 2 {
		t.Fatalf("first run wired %d, want both edges", res.Wired)
	}
	// A target rejects a duplicate edge, so a resume that tried again would
	// warn about work it had already done.
	e.target.failOn["AddSubIssue"] = errors.New("already exists")
	e.target.failOn["AddBlockedBy"] = errors.New("already exists")
	before := len(e.target.calls)
	res = e.run(t)
	if res.Wired != 0 || res.Failed != 0 {
		t.Errorf("resume = %+v, want no edge attempted and no failures", res)
	}
	for _, call := range e.target.calls[before:] {
		if strings.HasPrefix(call, "AddSubIssue") || strings.HasPrefix(call, "AddBlockedBy") {
			t.Errorf("resume retried a wired edge: %s", call)
		}
	}
}

func TestEngineRelinksTitlesAndKeepsEditedOnes(t *testing.T) {
	e := newEngine(t,
		Item{Key: "a", Title: "A"},
		Item{Key: "b", Title: "Follow-up to @a"},
		Item{Key: "c", Title: "Also @a"},
	)
	res := e.run(t)
	e.target.byNumber(res.Mapping["c"]).Title = "Renamed by a person"

	e.req.Options.Relink = func(text string, m map[string]int) string {
		return strings.ReplaceAll(text, "@a", fmt.Sprintf("#%d", m["a"]))
	}
	res = e.run(t)
	if got, want := e.target.byNumber(res.Mapping["b"]).Title, fmt.Sprintf("Follow-up to #%d", res.Mapping["a"]); got != want {
		t.Errorf("b title = %q, want %q", got, want)
	}
	if got := e.target.byNumber(res.Mapping["c"]).Title; got != "Renamed by a person" {
		t.Errorf("c title = %q, want the person's edit kept", got)
	}
	if res.Relinked != 1 || res.Failed != 0 || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0].Message, "title") {
		t.Errorf("relinked=%d failed=%d warnings=%+v", res.Relinked, res.Failed, res.Warnings)
	}
}

func TestEngineListsEveryWarningInTheResult(t *testing.T) {
	e := newEngine(t, Item{Key: "a", Title: "A", Closed: true, Comments: []string{"note"}})
	e.target.failOn["Comment"] = errors.New("no comment")
	res := e.run(t)
	if res.Failed != 1 || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0].Message, "no comment") {
		t.Errorf("result = %+v, want the comment failure counted and listed", res)
	}
	if res.Closed != 0 {
		t.Error("closed an issue whose comments did not all post; a rerun could not finish them")
	}
}

func TestEngineDryRunPlansWithoutWriting(t *testing.T) {
	e := newEngine(t,
		Item{Key: "a", Title: "A"},
		Item{Key: "b", Title: "B"},
	)
	e.req.Options.DryRun = true
	res, err := e.eng.Run(context.Background(), e.req)
	if err != nil {
		t.Fatal(err)
	}
	if !res.DryRun || res.Created != 0 || res.Wired != 0 || res.Closed != 0 {
		t.Errorf("result = %+v, want a plan and no writes", res)
	}
	want := []PlannedItem{{Key: "a", Title: "A"}, {Key: "b", Title: "B"}}
	if !slices.Equal(res.WouldCreate, want) {
		t.Errorf("WouldCreate = %+v, want %+v", res.WouldCreate, want)
	}
	if len(res.AlreadyCreated) != 0 {
		t.Errorf("AlreadyCreated = %v, want empty", res.AlreadyCreated)
	}
	for _, call := range e.target.calls {
		if !strings.HasPrefix(call, "GetIssue") {
			t.Errorf("a dry run wrote: %s", call)
		}
	}
}

func TestEngineDryRunReportsWhatTheCheckpointAlreadyHolds(t *testing.T) {
	e := newEngine(t,
		Item{Key: "a", Title: "A"},
		Item{Key: "b", Title: "B"},
	)
	// Record what a previous run left, with the issue and marker it wrote.
	checkpoint, err := tracker.OpenFileCheckpoint(e.path, "o/r", "sha256:test", "snapshot")
	if err != nil {
		t.Fatal(err)
	}
	issue := e.target.addIssue(101, "A", "body\n\n"+conventions.ProvenanceMarker(conventions.ProvenanceMigrate, "a", "sha256:test"))
	if err := checkpoint.RecordCreated("a", issue.Number); err != nil {
		t.Fatal(err)
	}
	e.eng = Wrap(e.target, checkpoint)
	e.req.Options.DryRun = true

	res, err := e.eng.Run(context.Background(), e.req)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.WouldCreate, []PlannedItem{{Key: "b", Title: "B"}}) {
		t.Errorf("WouldCreate = %+v, want only the unmapped item", res.WouldCreate)
	}
	if res.AlreadyCreated["a"] != 101 || res.Mapping["a"] != 101 {
		t.Errorf("AlreadyCreated = %v, Mapping = %v, want a at 101", res.AlreadyCreated, res.Mapping)
	}
}

func TestEngineRefusesUpToDateMappingWithoutProvenance(t *testing.T) {
	e := newEngine(t, Item{Key: "a", Title: "A"})
	checkpoint, err := tracker.OpenFileCheckpoint(e.path, "o/r", "sha256:test", "snapshot")
	if err != nil {
		t.Fatal(err)
	}
	e.target.addIssue(42, "An unrelated issue", "Do not touch me")
	if err := checkpoint.RecordCreated("a", 42); err != nil {
		t.Fatal(err)
	}
	e.eng = Wrap(e.target, checkpoint)

	_, err = e.eng.Run(context.Background(), e.req)
	if err == nil || !strings.Contains(err.Error(), "does not carry the hew provenance marker") {
		t.Fatalf("err = %v, want a missing-provenance refusal", err)
	}
	for _, call := range e.target.calls {
		if !strings.HasPrefix(call, "GetIssue") {
			t.Errorf("a refused run wrote: %s", call)
		}
	}
}

func TestEngineRefusesAKeyOutsideTheRunsSelection(t *testing.T) {
	e := newEngine(t, Item{Key: "a", Title: "A"})
	checkpoint, err := tracker.OpenFileCheckpoint(e.path, "o/r", "sha256:test", "snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if err := checkpoint.RecordCreated("ghost", 99); err != nil {
		t.Fatal(err)
	}
	e.eng = Wrap(e.target, checkpoint)
	e.req.Options.KnownKeys = map[string]bool{"a": true}

	_, err = e.eng.Run(context.Background(), e.req)
	if err == nil || !strings.Contains(err.Error(), "is not in this run's selection") {
		t.Fatalf("err = %v, want an unknown-key refusal", err)
	}
	if len(e.target.calls) != 0 {
		t.Errorf("wrote or read before rejecting: %v", e.target.calls)
	}
}

func TestEngineRefusesADigestThatContradictsTheCheckpoint(t *testing.T) {
	e := newEngine(t, Item{Key: "a", Title: "A"})
	e.req.Options.Digest = "sha256:other"
	_, err := e.eng.Run(context.Background(), e.req)
	var invalid *tracker.InvalidInputError
	if !errors.As(err, &invalid) || invalid.Field != "digest" {
		t.Fatalf("err = %T %v, want an invalid digest", err, err)
	}
	if len(e.target.calls) != 0 {
		t.Errorf("wrote before rejecting: %v", e.target.calls)
	}
}

// BeforeCreate is where the CLI bootstraps labels: it has to run after the
// checkpoint is vouched for and before a create, or a refused state file would
// cost a write.
func TestEngineBeforeCreateRunsAfterVerificationAndBeforeCreate(t *testing.T) {
	t.Run("a refused checkpoint never reaches the hook", func(t *testing.T) {
		e := newEngine(t, Item{Key: "a", Title: "A"})
		checkpoint, err := tracker.OpenFileCheckpoint(e.path, "o/r", "sha256:test", "snapshot")
		if err != nil {
			t.Fatal(err)
		}
		e.target.addIssue(42, "An unrelated issue", "Do not touch me")
		if err := checkpoint.RecordCreated("a", 42); err != nil {
			t.Fatal(err)
		}
		e.eng = Wrap(e.target, checkpoint)

		called := false
		e.req.Options.BeforeCreate = func(context.Context) error { called = true; return nil }
		if _, err := e.eng.Run(context.Background(), e.req); err == nil {
			t.Fatal("want the provenance refusal")
		}
		if called {
			t.Error("the hook ran before the checkpoint was vouched for")
		}
		if len(e.target.calls) != 1 || e.target.calls[0] != "GetIssue 42" {
			t.Errorf("calls = %v, want only the verification read", e.target.calls)
		}
	})

	t.Run("a hook failure aborts before the first create", func(t *testing.T) {
		e := newEngine(t, Item{Key: "a", Title: "A"})
		e.req.Options.BeforeCreate = func(context.Context) error { return errors.New("no labels") }
		_, err := e.eng.Run(context.Background(), e.req)
		if err == nil || !strings.Contains(err.Error(), "no labels") {
			t.Fatalf("err = %v, want the hook's failure", err)
		}
		if got := e.target.callCounts["CreateIssue"]; got != 0 {
			t.Errorf("CreateIssue called %d times after the hook failed", got)
		}
	})

	t.Run("a dry run never calls the hook", func(t *testing.T) {
		e := newEngine(t, Item{Key: "a", Title: "A"})
		called := false
		e.req.Options.DryRun = true
		e.req.Options.BeforeCreate = func(context.Context) error { called = true; return nil }
		e.run(t)
		if called {
			t.Error("a dry run ran the write-preparation hook")
		}
	})
}

func TestEngineCancellationInterruptsTheThrottle(t *testing.T) {
	e := newEngine(t,
		Item{Key: "a", Title: "A"},
		Item{Key: "b", Title: "B"},
	)
	e.req.Options.Throttle = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.target.onCreate = func(int) { cancel() }

	start := time.Now()
	res, err := e.eng.Run(ctx, e.req)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("the run waited %s, want cancellation to interrupt the throttle", elapsed)
	}
	if res.Created != 1 || len(res.Mapping) != 1 {
		t.Errorf("result = %+v, want the one create that landed and no more", res)
	}
	if got := e.target.callCounts["CreateIssue"]; got != 1 {
		t.Errorf("CreateIssue called %d times, want the run to stop at the throttle", got)
	}
}

func TestEngineCancellationInterruptsABackendCall(t *testing.T) {
	e := newEngine(t, Item{Key: "a", Title: "A"})
	e.target.blockCreate = true
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := e.eng.Run(ctx, e.req)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}

func TestEngineProgressReceivesTheWrites(t *testing.T) {
	e := newEngine(t, Item{Key: "a", Title: "A"})
	var lines []string
	e.req.Options.Progress = func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	e.run(t)
	if len(lines) != 1 || !strings.Contains(lines[0], "#101") || !strings.Contains(lines[0], "from a") {
		t.Errorf("progress = %q, want the create narrated", lines)
	}
}

func TestEngineRefusesAMigratorWithoutATargetOrCheckpoint(t *testing.T) {
	res, err := Wrap(nil, nil).Run(context.Background(), RunRequest{Items: []Item{{Key: "a"}}})
	var invalid *tracker.InvalidInputError
	if !errors.As(err, &invalid) || invalid.Field != "target" {
		t.Fatalf("err = %T %v, want an invalid target", err, err)
	}
	if res.Created != 0 {
		t.Errorf("result = %+v, want nothing written", res)
	}
}
