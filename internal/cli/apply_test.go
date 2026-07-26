package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
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

// readState reads a checkpoint file the way a resume does, so a test that
// asserts on it fails when the on-disk shape drifts from what load expects.
func readState(t *testing.T, path string) *batchState {
	t.Helper()
	state, err := loadBatchState(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return state
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
	want := "### Goal\n\nShip it\n\n### Done when\n\n- [ ] tests pass"
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

func TestApplyResume(t *testing.T) {
	f, app, opts := applySetup(t, applyFixture)
	// Pretend the epic was already created as #55.
	f.issues = append(f.issues, issue(55, "Epic: Voltgo support", "P1"))
	f.issues[len(f.issues)-1].ID = "ID55"
	if err := os.WriteFile(opts.StatePath, []byte(`{"epic1": 55}`+"\n"), 0o644); err != nil {
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

// State files written before edges were checkpointed are a bare key→number
// map. A batch mid-flight across an upgrade must resume from one, not read
// it as "nothing created yet" and duplicate everything.
func TestApplyReadsALegacyStateFile(t *testing.T) {
	f, app, opts := applySetup(t, applyFixture)
	legacy := `{"epic1":101,"scaffold":102,"line:3":103}` + "\n"
	if err := os.WriteFile(opts.StatePath, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := app.Apply(ctx, opts); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "CreateIssue") {
			t.Errorf("legacy state ignored, issues recreated: %v", f.calls)
			break
		}
	}
	if got := readState(t, opts.StatePath).Mapping["epic1"]; got != 101 {
		t.Errorf("legacy mapping lost: %v", got)
	}
}

// A state file need not carry both halves, and JSON has two ways to say so:
// omit the key, or write null — and null is the one that reaches through
// into the decoded struct and leaves a nil map behind, which the next create
// would panic assigning into. Both shapes must load as an empty map.
func TestApplyReadsAPartialStateFile(t *testing.T) {
	// The recorded edge is between issues this plan never names, so nothing
	// is skipped for the wrong reason.
	cases := map[string]string{
		"mapping key omitted": `{"edges":{"parent:998->999":true}}`,
		"mapping is null":     `{"mapping":null,"edges":{"parent:998->999":true}}`,
		"edges is null":       `{"mapping":{},"edges":null}`,
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
