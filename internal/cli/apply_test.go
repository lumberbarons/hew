package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/internal/conventions"
)

// applyFixture covers the plan shapes: an epic with a body, a child by
// local-id parent, and an id-less entry referencing both a local id and an
// existing issue number, with a discovered-from link.
const applyFixture = `{"id":"epic1","title":"Voltgo support","type":"epic","priority":"P1","body":"### Goal\n\nstuff"}
{"id":"scaffold","title":"Scaffold","type":"task","parent":"epic1","areas":["ble"]}
{"title":"Collector","type":"enhancement","priority":"P3","parent":"epic1","blocked-by":["scaffold",42],"discovered-from":7}
`

func applySetup(t *testing.T, fixture string) (*fakeClient, *App, ApplyOpts) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "plan.jsonl")
	if err := os.WriteFile(file, []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	f := newFake(issue(42, "Existing dep", "P2", "task"))
	app, _, _ := newApp(f)
	return f, app, ApplyOpts{File: file, StatePath: filepath.Join(dir, "state.json")}
}

// readState reads a checkpoint file directly as JSON, so a test can
// inspect what was persisted.
func readState(t *testing.T, path string) *batchState {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var state batchState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("unmarshaling %s: %v", path, err)
	}
	return &state
}

func TestApplyCreatesAndWires(t *testing.T) {
	f, app, opts := applySetup(t, applyFixture)
	if err := app.Apply(ctx, opts); err != nil {
		t.Fatal(err)
	}
	// Creation in file order: epic (101), scaffold (102), collector (103).
	epic := f.byNumber(101)
	if epic == nil || epic.Title != "Epic: Voltgo support" {
		t.Fatalf("epic = %+v", epic)
	}
	if got, _ := epic.Type(); got != "" {
		t.Errorf("epic has type label: %v", epic.Labels)
	}
	if !slices.Contains(epic.Labels, "P1") || !strings.Contains(epic.Body, "### Goal") {
		t.Errorf("epic labels = %v body = %q", epic.Labels, epic.Body)
	}
	scaffold := f.byNumber(102)
	if !slices.Contains(scaffold.Labels, "P2") || !slices.Contains(scaffold.Labels, "task") || !slices.Contains(scaffold.Labels, "ble") {
		t.Errorf("scaffold labels = %v", scaffold.Labels)
	}
	if scaffold.Parent == nil || scaffold.Parent.Number != 101 {
		t.Errorf("scaffold parent = %v", scaffold.Parent)
	}
	collector := f.byNumber(103)
	if collector.Parent == nil || collector.Parent.Number != 101 {
		t.Errorf("collector parent = %v", collector.Parent)
	}
	blockers := []int{}
	for _, b := range collector.BlockedBy {
		blockers = append(blockers, b.Number)
	}
	if !slices.Contains(blockers, 102) || !slices.Contains(blockers, 42) {
		t.Errorf("collector blockedBy = %v", blockers)
	}
	if !strings.Contains(collector.Body, "Discovered while working on #7") {
		t.Errorf("collector body = %q", collector.Body)
	}
	// The id-less entry is checkpointed under its line key.
	state := readState(t, opts.StatePath).Mapping
	if state["epic1"] != 101 || state["scaffold"] != 102 || state["line:3"] != 103 {
		t.Errorf("state = %v", state)
	}
}

func TestApplyComposesSectionBodies(t *testing.T) {
	fixture := `{"title":"T","type":"task","goal":"Ship it","done-when":["tests pass"]}` + "\n"
	f, app, opts := applySetup(t, fixture)
	if err := app.Apply(ctx, opts); err != nil {
		t.Fatal(err)
	}
	want := "### Goal\n\nShip it\n\n### Done when\n\n- [ ] tests pass\n\n" +
		fmt.Sprintf("<!-- hew:apply key=line:1 digest=%s -->", fileDigest([]byte(fixture)))
	if got := f.byNumber(101).Body; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestApplyDryRun(t *testing.T) {
	f, app, opts := applySetup(t, applyFixture)
	opts.DryRun = true
	if err := app.Apply(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 0 {
		t.Errorf("dry run made API calls: %v", f.calls)
	}
	if _, err := os.Stat(opts.StatePath); !os.IsNotExist(err) {
		t.Error("dry run wrote state")
	}
	out := app.Out.(interface{ String() string }).String()
	for _, want := range []string{
		"create: epic1 as P1 epic  Epic: Voltgo support",
		"create: scaffold as P2 task  Scaffold  [areas ble; parent epic1]",
		"create: line:3 as P3 enhancement  Collector  [parent epic1; blocked by scaffold #42]",
		"dry run: 3 issues would be created",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plan missing %q:\n%s", want, out)
		}
	}
}

// A plan can name twenty files across a dozen entries, so the warning is
// per entry and leads with the key: the author needs to know which entry to
// fix, not just which token offended. It reports on the dry run — the pass
// that exists to be read before anything is written.
func TestApplyWarnsAboutUnmarkedCodeTextPerEntry(t *testing.T) {
	fixture := `{"id":"bare","title":"Collector","type":"task","goal":"Rewrite internal/cli/pr.go, run go test ./..., then check"}
{"id":"marked","title":"Wired","type":"task","goal":"Rewrite ` + "`internal/cli/pr.go`" + ` and run ` + "`go test ./...`" + `"}
`
	f, app, opts := applySetup(t, fixture)
	opts.DryRun = true
	if err := app.Apply(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 0 {
		t.Errorf("dry run made API calls: %v", f.calls)
	}
	warning := app.ErrOut.(interface{ String() string }).String()
	for _, want := range []string{"bare: not in code spans", "internal/cli/pr.go", "./...", "backticks"} {
		if !strings.Contains(warning, want) {
			t.Errorf("warning %q does not name %q", warning, want)
		}
	}
	// The entry that already follows the convention must not be named, or
	// the warning fires forever on a correct plan and stops being read.
	if strings.Contains(warning, "marked:") {
		t.Errorf("warned about a correctly marked-up entry: %q", warning)
	}
}

func TestApplyResume(t *testing.T) {
	f, app, opts := applySetup(t, applyFixture)
	// Pretend the epic was already created as #55 with provenance.
	epicIssue := issue(55, "Epic: Voltgo support", "P1")
	epicIssue.ID = "ID55"
	epicIssue.Body = "### Goal\n\nstuff\n\n" + conventions.ProvenanceMarker(conventions.ProvenanceApply, "epic1", fileDigest([]byte(applyFixture)))
	f.issues = append(f.issues, epicIssue)
	digest := fileDigest([]byte(applyFixture))
	stateJSON, _ := json.Marshal(batchState{
		Version: batchStateVersion,
		Repo:    "o/r",
		Digest:  digest,
		Mapping: map[string]int{"epic1": 55},
	})
	if err := os.WriteFile(opts.StatePath, stateJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := app.Apply(ctx, opts); err != nil {
		t.Fatal(err)
	}
	for _, i := range f.issues {
		if i.Title == "Epic: Voltgo support" && i.Number != 55 {
			t.Errorf("epic recreated as #%d", i.Number)
		}
	}
	// The children's parent edges must point at the pre-existing #55.
	scaffold := f.byNumber(101)
	if scaffold.Parent == nil || scaffold.Parent.Number != 55 {
		t.Errorf("scaffold parent = %v", scaffold.Parent)
	}
}

func TestApplyResumesAfterCreateFailure(t *testing.T) {
	f, app, opts := applySetup(t, applyFixture)
	// The API dies on the second create: the first entry's mapping must
	// already be on disk, and a rerun must pick up where the crash left off.
	f.failAfter["CreateIssue"] = failPoint{calls: 1, err: errors.New("boom")}
	err := app.Apply(ctx, opts)
	if err == nil || !strings.Contains(err.Error(), "rerun to resume") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(opts.StatePath); err != nil {
		t.Fatalf("no state persisted before the crash: %v", err)
	}
	state := readState(t, opts.StatePath).Mapping
	if len(state) != 1 || state["epic1"] != 101 {
		t.Fatalf("state after crash = %v", state)
	}

	delete(f.failAfter, "CreateIssue")
	if err := app.Apply(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if len(f.issues) != 4 { // #42 plus the three plan entries
		t.Errorf("resume duplicated issues: %d total", len(f.issues))
	}
	state = readState(t, opts.StatePath).Mapping
	if len(state) != 3 || state["epic1"] != 101 {
		t.Errorf("state after resume = %v", state)
	}
}

func TestApplyRefusesCorruptState(t *testing.T) {
	f, app, opts := applySetup(t, applyFixture)
	if err := os.WriteFile(opts.StatePath, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A corrupt state file must abort, not be treated as "nothing created
	// yet" — that would duplicate every already-created issue.
	err := app.Apply(ctx, opts)
	if err == nil || !strings.Contains(err.Error(), "not a valid resume-state file") {
		t.Fatalf("err = %v", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("API calls made despite corrupt state: %v", f.calls)
	}
}

func TestApplyInvalidPlan(t *testing.T) {
	f, app, opts := applySetup(t, `{"title":"x","type":"task","parent":"nope"}`+"\n")
	err := app.Apply(ctx, opts)
	exitCode(t, err, ExitGeneric)
	if err == nil || !strings.Contains(err.Error(), "parsing") {
		t.Fatalf("err = %v", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("API calls made despite invalid plan: %v", f.calls)
	}
}

func TestApplyMissingFile(t *testing.T) {
	_, app, opts := applySetup(t, applyFixture)
	opts.File = "/nonexistent.jsonl"
	exitCode(t, app.Apply(ctx, opts), ExitGeneric)
}

func TestApplyNoFileArg(t *testing.T) {
	_, app, opts := applySetup(t, applyFixture)
	opts.File = ""
	exitCode(t, app.Apply(ctx, opts), ExitUsage)
}

func TestApplyEmptyPlan(t *testing.T) {
	f, app, opts := applySetup(t, "\n")
	if err := app.Apply(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 0 {
		t.Errorf("calls made: %v", f.calls)
	}
	out := app.Out.(interface{ String() string }).String()
	if !strings.Contains(out, "nothing to apply") {
		t.Errorf("output = %q", out)
	}
}

func TestApplyDefaultStatePath(t *testing.T) {
	_, app, opts := applySetup(t, applyFixture)
	opts.StatePath = ""
	if err := app.Apply(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(opts.File + ".state.json"); err != nil {
		t.Errorf("default state file not written: %v", err)
	}
}

func TestApplyCreatesLabels(t *testing.T) {
	f, app, opts := applySetup(t, applyFixture)
	if err := app.Apply(ctx, opts); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, l := range f.labels {
		names[l.Name] = true
	}
	for _, want := range []string{"P0", "P4", "bug", "enhancement", "task", "in-progress", "ble"} {
		if !names[want] {
			t.Errorf("label %q not ensured", want)
		}
	}
}

func TestApplyFailedEdgeWarns(t *testing.T) {
	// A numeric blocked-by pointing at an issue that doesn't exist fails at
	// the API; the create must survive and the edge failure must warn.
	f, app, opts := applySetup(t, `{"title":"x","type":"task","blocked-by":[999]}`+"\n")
	if err := app.Apply(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if f.byNumber(101) == nil {
		t.Fatal("issue not created")
	}
	errOut := app.ErrOut.(interface{ String() string }).String()
	if !strings.Contains(errOut, "blocked-by edge") {
		t.Errorf("no edge warning: %q", errOut)
	}
}

// #46: re-running a finished plan created nothing but re-attempted every
// edge, and GitHub answers a duplicate edge with an error — so a clean
// resume printed a warning per edge under a "0 created, 0 wired" summary,
// burying any warning that meant something.
func TestApplyResumeSkipsWiredEdges(t *testing.T) {
	f, app, opts := applySetup(t, applyFixture)
	if err := app.Apply(ctx, opts); err != nil {
		t.Fatal(err)
	}
	// Every edge the plan declares is on disk, keyed by resolved endpoints.
	edges := readState(t, opts.StatePath).Edges
	for _, want := range []string{"parent:102->101", "parent:103->101", "blocked-by:103->102", "blocked-by:103->42"} {
		if !edges[want] {
			t.Errorf("edge %q not checkpointed: %v", want, edges)
		}
	}

	f.calls = nil
	app2, out, errOut := newApp(f)
	if err := app2.Apply(ctx, opts); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "AddSubIssue") || strings.HasPrefix(c, "AddBlockedBy") {
			t.Errorf("resume re-attempted an edge: %v", f.calls)
			break
		}
	}
	if errOut.String() != "" {
		t.Errorf("resume warned: %q", errOut.String())
	}
	if !strings.Contains(out.String(), "0 created, 0 dependencies wired") {
		t.Errorf("resume summary = %q", out.String())
	}
}

// An edge that never landed is not checkpointed, so the next run retries it
// and warns again — the whole point of recording the successful ones is
// that the remaining warnings are real.
func TestApplyResumeRetriesAFailedEdge(t *testing.T) {
	f, app, opts := applySetup(t, `{"title":"x","type":"task","blocked-by":[999]}`+"\n")
	if err := app.Apply(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if edges := readState(t, opts.StatePath).Edges; len(edges) != 0 {
		t.Errorf("failed edge was checkpointed: %v", edges)
	}

	app2, _, errOut := newApp(f)
	if err := app2.Apply(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "blocked-by edge") {
		t.Errorf("resume did not retry the failed edge: %q", errOut.String())
	}
}

// A state file without repository or digest binding must abort, not be
// trusted — that would allow mutating unrelated issues from untrusted state (#81).
func TestApplyRefusesUnboundState(t *testing.T) {
	f, app, opts := applySetup(t, applyFixture)
	legacy := `{"epic1":101,"scaffold":102,"line:3":103}` + "\n"
	if err := os.WriteFile(opts.StatePath, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	err := app.Apply(ctx, opts)
	if err == nil || !strings.Contains(err.Error(), "written by an older hew") {
		t.Fatalf("err = %v", err)
	}
	// The message has to name a way forward, and specifically has to say that
	// the obvious one re-creates work, or an agent will silently duplicate a
	// half-finished plan.
	if !strings.Contains(err.Error(), "--state") || !strings.Contains(err.Error(), "created again") {
		t.Errorf("error names no recovery: %v", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("API calls made despite unbound state: %v", f.calls)
	}
}

func TestApplyRefusesMismatchedDigestOrRepo(t *testing.T) {
	cases := map[string]struct {
		state batchState
		want  string
	}{
		"mismatched repo": {
			state: batchState{Version: batchStateVersion, Repo: "other/repo", Digest: fileDigest([]byte(applyFixture))},
			want:  "belongs to repository",
		},
		"mismatched digest": {
			state: batchState{Version: batchStateVersion, Repo: "o/r", Digest: "sha256:0000000000000000000000000000000000000000000000000000000000000000"},
			want:  "was written for a different plan",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f, app, opts := applySetup(t, applyFixture)
			data, _ := json.Marshal(tc.state)
			if err := os.WriteFile(opts.StatePath, data, 0o644); err != nil {
				t.Fatal(err)
			}
			err := app.Apply(ctx, opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if len(f.calls) != 0 {
				t.Errorf("API calls made despite mismatched state: %v", f.calls)
			}
		})
	}
}

// Regression test for #81: a poisoned state file mapping a plan entry to an
// existing unrelated issue must be rejected before wiring any parent or dependency edges.
func TestApplyPoisonedStateCannotWireEdgesOnUnrelatedIssues(t *testing.T) {
	f, app, opts := applySetup(t, applyFixture)
	// Issue #42 is an existing issue in the repo ("Existing dep").
	// Poison the state file to claim "scaffold" was already created as #42,
	// but issue #42 carries no provenance marker for "scaffold".
	digest := fileDigest([]byte(applyFixture))
	poisoned, _ := json.Marshal(batchState{
		Version: batchStateVersion,
		Repo:    "o/r",
		Digest:  digest,
		Mapping: map[string]int{"scaffold": 42},
	})
	if err := os.WriteFile(opts.StatePath, poisoned, 0o644); err != nil {
		t.Fatal(err)
	}

	err := app.Apply(ctx, opts)
	if err == nil || !strings.Contains(err.Error(), "does not carry the hew provenance marker") {
		t.Fatalf("err = %v, want missing provenance error", err)
	}

	// Issue #42 must NOT have any parent or blocker edges wired to it.
	target := f.byNumber(42)
	if target.Parent != nil {
		t.Errorf("unrelated issue #42 parent was modified: %+v", target.Parent)
	}
	if len(target.BlockedBy) != 0 {
		t.Errorf("unrelated issue #42 blockedBy was modified: %+v", target.BlockedBy)
	}
	// Asserted as an allowlist of one rather than a denylist of the edge
	// calls: label bootstrapping writes to the repository too, so a denylist
	// would stay green if verification were ever reordered after it.
	assertOnlyReads(t, f)
}

// assertOnlyReads fails if the run made any call other than the issue reads
// verification itself performs — the precise claim "before any GitHub
// mutation", which a per-call denylist cannot make.
func assertOnlyReads(t *testing.T, f *fakeClient) {
	t.Helper()
	for _, call := range f.calls {
		if !strings.HasPrefix(call, "GetIssue") {
			t.Errorf("call made before verification rejected the state: %s", call)
		}
	}
}

// A state file from a future schema must say so rather than be reported as
// corrupt or as "written by an older hew" — the version field exists so the
// next change to this schema has an accurate message to give.
func TestApplyRefusesANewerStateFileVersion(t *testing.T) {
	f, app, opts := applySetup(t, applyFixture)
	data, _ := json.Marshal(batchState{
		Version: batchStateVersion + 1,
		Repo:    "o/r",
		Digest:  fileDigest([]byte(applyFixture)),
		Mapping: map[string]int{"epic1": 101},
	})
	if err := os.WriteFile(opts.StatePath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	err := app.Apply(ctx, opts)
	if err == nil || !strings.Contains(err.Error(), "written by a newer hew") {
		t.Fatalf("err = %v, want a newer-version rejection", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("API calls made despite an unreadable state file: %v", f.calls)
	}
}

// --dry-run is documented as the pass to read before the real one, so it has
// to reach the same verdict. It reads GitHub but writes nothing, so running
// verification in dry run costs nothing and closes the gap where a dry run
// reported "already created" for a mapping the real run would reject.
func TestApplyDryRunReportsAPoisonedStateFile(t *testing.T) {
	f, app, opts := applySetup(t, applyFixture)
	opts.DryRun = true
	poisoned, _ := json.Marshal(batchState{
		Version: batchStateVersion,
		Repo:    "o/r",
		Digest:  fileDigest([]byte(applyFixture)),
		Mapping: map[string]int{"scaffold": 42},
	})
	if err := os.WriteFile(opts.StatePath, poisoned, 0o644); err != nil {
		t.Fatal(err)
	}
	err := app.Apply(ctx, opts)
	if err == nil || !strings.Contains(err.Error(), "does not carry the hew provenance marker") {
		t.Fatalf("dry run err = %v, want the same rejection the real run gives", err)
	}
	if out := app.Out.(interface{ String() string }).String(); strings.Contains(out, "already created: scaffold") {
		t.Errorf("dry run vouched for the poisoned mapping:\n%s", out)
	}
	assertOnlyReads(t, f)
}

// The digest has to be taken over the file's actual contents. Asserting
// against a hand-written wrong digest cannot show that: a fileDigest that
// ignored its input entirely — hashing nothing, or the path — would satisfy
// it and every positive test too, while letting state from one plan drive a
// run of another. So this drives two genuinely different files through a
// real run.
func TestApplyRejectsStateFromADifferentPlanFile(t *testing.T) {
	f, app, opts := applySetup(t, applyFixture)
	if err := app.Apply(ctx, opts); err != nil {
		t.Fatal(err)
	}
	before := len(f.issues)

	// Same run, same repo, same state file — only the plan's contents move.
	edited := applyFixture + `{"id":"late","title":"Added later","type":"task"}` + "\n"
	if err := os.WriteFile(opts.File, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	err := app.Apply(ctx, opts)
	if err == nil || !strings.Contains(err.Error(), "was written for a different plan") {
		t.Fatalf("err = %v, want a rejection naming the changed plan", err)
	}
	if len(f.issues) != before {
		t.Errorf("issues created against a rejected state file: %d → %d", before, len(f.issues))
	}
	if len(f.calls) != 0 {
		t.Errorf("API calls made despite a rejected state file: %v", f.calls)
	}
}

// The marker binds the entry key, not just "hew made this". Without that,
// a state file could point one entry at the issue another entry created in
// the same run — same repo, same digest, real provenance — and the edges
// would land on the wrong issue.
func TestApplyRejectsStateMappingAnEntryToAnotherEntrysIssue(t *testing.T) {
	f, app, opts := applySetup(t, applyFixture)
	if err := app.Apply(ctx, opts); err != nil {
		t.Fatal(err)
	}
	// #101 is the issue this plan's "epic1" entry created, marker and all.
	digest := fileDigest([]byte(applyFixture))
	swapped, _ := json.Marshal(batchState{
		Version: batchStateVersion,
		Repo:    "o/r",
		Digest:  digest,
		Mapping: map[string]int{"scaffold": 101},
	})
	if err := os.WriteFile(opts.StatePath, swapped, 0o644); err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	err := app.Apply(ctx, opts)
	if err == nil || !strings.Contains(err.Error(), "does not carry the hew provenance marker") {
		t.Fatalf("err = %v, want a rejection for the mismatched key", err)
	}
	if !strings.Contains(err.Error(), "scaffold") {
		t.Errorf("error does not name the offending key: %v", err)
	}
	assertOnlyReads(t, f)
}

func TestApplyRefusesUnknownKeyOrMissingMappedIssue(t *testing.T) {
	t.Run("unknown key in state", func(t *testing.T) {
		f, app, opts := applySetup(t, applyFixture)
		digest := fileDigest([]byte(applyFixture))
		data, _ := json.Marshal(batchState{
			Version: batchStateVersion,
			Repo:    "o/r",
			Digest:  digest,
			Mapping: map[string]int{"ghost": 99},
		})
		if err := os.WriteFile(opts.StatePath, data, 0o644); err != nil {
			t.Fatal(err)
		}
		err := app.Apply(ctx, opts)
		if err == nil || !strings.Contains(err.Error(), "is not in this run's selection") {
			t.Fatalf("err = %v, want unknown-key error", err)
		}
		if len(f.calls) != 0 {
			t.Errorf("API calls made: %v", f.calls)
		}
	})

	t.Run("mapped issue not on GitHub", func(t *testing.T) {
		_, app, opts := applySetup(t, applyFixture)
		digest := fileDigest([]byte(applyFixture))
		data, _ := json.Marshal(batchState{
			Version: batchStateVersion,
			Repo:    "o/r",
			Digest:  digest,
			Mapping: map[string]int{"epic1": 999},
		})
		if err := os.WriteFile(opts.StatePath, data, 0o644); err != nil {
			t.Fatal(err)
		}
		err := app.Apply(ctx, opts)
		if err == nil || !strings.Contains(err.Error(), "verifying mapped issue #999") {
			t.Fatalf("err = %v, want verifying mapped issue error", err)
		}
	})
}

// A state file need not carry both halves, and JSON has two ways to say so:
// omit the key, or write null — and null is the one that reaches through
// into the decoded struct and leaves a nil map behind, which the next create
// would panic assigning into. Both shapes must load as an empty map.
func TestApplyReadsAPartialStateFile(t *testing.T) {
	// The recorded edge is between issues this plan never names, so nothing
	// is skipped for the wrong reason.
	digest := fileDigest([]byte(applyFixture))
	cases := map[string]string{
		"mapping key omitted": fmt.Sprintf(`{"version":1,"repo":"o/r","digest":%q,"edges":{"parent:998->999":true}}`, digest),
		"mapping is null":     fmt.Sprintf(`{"version":1,"repo":"o/r","digest":%q,"mapping":null,"edges":{"parent:998->999":true}}`, digest),
		"edges is null":       fmt.Sprintf(`{"version":1,"repo":"o/r","digest":%q,"mapping":{},"edges":null}`, digest),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			f, app, opts := applySetup(t, applyFixture)
			if err := os.WriteFile(opts.StatePath, []byte(content+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := app.Apply(ctx, opts); err != nil {
				t.Fatal(err)
			}
			if f.byNumber(101) == nil {
				t.Fatal("nothing created from a partial state file")
			}
			state := readState(t, opts.StatePath)
			if len(state.Mapping) != 3 {
				t.Errorf("mapping after apply = %v, want the three plan entries", state.Mapping)
			}
			if len(state.Edges) < 4 {
				t.Errorf("edges after apply = %v, want the plan's four", state.Edges)
			}
		})
	}
}

func TestApplyJSON(t *testing.T) {
	_, app, opts := applySetup(t, applyFixture)
	app.JSON = true
	if err := app.Apply(ctx, opts); err != nil {
		t.Fatal(err)
	}
	out := app.Out.(interface{ Bytes() []byte }).Bytes()
	var got struct {
		Created int            `json:"created"`
		Wired   int            `json:"wired"`
		Mapping map[string]int `json:"mapping"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	// Wired: two parent edges plus two blockers.
	if got.Created != 3 || got.Wired != 4 || len(got.Mapping) != 3 {
		t.Errorf("summary = %+v", got)
	}
}
