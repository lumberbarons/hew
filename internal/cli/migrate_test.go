package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/internal/conventions"
)

// migrationFixture covers the shapes seen in real beads databases: an
// epic with a child, a blocks chain, in-progress, closed with reason,
// chore→task mapping, and area labels alongside a colliding one.
const migrationFixture = `{"_type":"issue","id":"sc-epic","title":"Voltgo support","status":"open","priority":2,"issue_type":"epic","created_at":"2026-05-01T00:00:00Z"}
{"_type":"issue","id":"sc-1","title":"Scaffold","description":"the scaffold","status":"in_progress","priority":1,"issue_type":"feature","labels":["ble","bug"],"created_at":"2026-05-02T00:00:00Z","dependencies":[{"issue_id":"sc-1","depends_on_id":"sc-epic","type":"parent-child"}]}
{"_type":"issue","id":"sc-2","title":"Collector","status":"open","priority":2,"issue_type":"chore","created_at":"2026-05-03T00:00:00Z","dependencies":[{"issue_id":"sc-2","depends_on_id":"sc-1","type":"blocks"}]}
{"_type":"issue","id":"sc-done","title":"Old fix","status":"closed","priority":3,"issue_type":"bug","created_at":"2026-04-01T00:00:00Z","closed_at":"2026-04-02T00:00:00Z","close_reason":"Fixed in PR #9","design":"the design","acceptance_criteria":"- [ ] works","notes":"a note"}
`

func migrateSetup(t *testing.T, fixture string) (*fakeClient, *App, MigrateOpts) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "issues.jsonl")
	if err := os.WriteFile(file, []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	f := newFake()
	app, _, _ := newApp(f)
	return f, app, MigrateOpts{File: file, StatePath: filepath.Join(dir, "state.json")}
}

func TestMigrateBeadsOpenOnly(t *testing.T) {
	f, app, opts := migrateSetup(t, migrationFixture)
	if err := app.MigrateBeads(ctx, opts); err != nil {
		t.Fatal(err)
	}
	// Creation in created_at order: epic (101), sc-1 (102), sc-2 (103).
	epic := f.byNumber(101)
	if epic == nil || epic.Title != "Epic: Voltgo support" {
		t.Fatalf("epic = %+v", epic)
	}
	if got, _ := epic.Type(); got != "" {
		t.Errorf("epic has type label: %v", epic.Labels)
	}
	one := f.byNumber(102)
	if !slices.Contains(one.Labels, "P1") || !slices.Contains(one.Labels, "enhancement") ||
		!slices.Contains(one.Labels, "in-progress") || !slices.Contains(one.Labels, "ble") {
		t.Errorf("sc-1 labels = %v", one.Labels)
	}
	if slices.Contains(one.Labels, "bug") {
		t.Errorf("colliding bead label copied: %v", one.Labels)
	}
	if !slices.Contains(one.Assignees, "me") {
		t.Errorf("in-progress bead not assigned: %v", one.Assignees)
	}
	if one.Parent == nil || one.Parent.Number != 101 {
		t.Errorf("parent = %v", one.Parent)
	}
	two := f.byNumber(103)
	if got, _ := two.Type(); got != "task" {
		t.Errorf("chore mapped to %q", got)
	}
	if len(two.BlockedBy) != 1 || two.BlockedBy[0].Number != 102 {
		t.Errorf("blockedBy = %v", two.BlockedBy)
	}
	if f.byNumber(104) != nil {
		t.Error("closed bead migrated without --include-closed")
	}
	if !strings.Contains(one.Body, "the scaffold") || !strings.Contains(one.Body, "Migrated from beads `sc-1` (created 2026-05-02)") {
		t.Errorf("body = %q", one.Body)
	}
}

func TestMigrateBeadsIncludeClosed(t *testing.T) {
	f, app, opts := migrateSetup(t, migrationFixture)
	opts.IncludeClosed = true
	if err := app.MigrateBeads(ctx, opts); err != nil {
		t.Fatal(err)
	}
	// sc-done is oldest, so it's created first (101).
	done := f.byNumber(101)
	if done == nil || done.State != "CLOSED" || done.StateReason != "COMPLETED" {
		t.Fatalf("closed bead = %+v", done)
	}
	if !slices.ContainsFunc(f.comments[101], func(c string) bool { return c == "Fixed in PR #9" }) {
		t.Errorf("close reason comment missing: %v", f.comments[101])
	}
	for _, want := range []string{"### Design", "the design", "### Done when", "- [ ] works", "### Notes", "a note", "closed 2026-04-02"} {
		if !strings.Contains(done.Body, want) {
			t.Errorf("body missing %q:\n%s", want, done.Body)
		}
	}
}

func TestMigrateBeadsResume(t *testing.T) {
	f, app, opts := migrateSetup(t, migrationFixture)
	// Pretend sc-epic was already migrated as #55 with provenance.
	epicIssue := issue(55, "Epic: Voltgo support", "P2")
	epicIssue.ID = "ID55"
	epicIssue.Body = "Migrated from beads `sc-epic` (created 2026-05-01)\n\n" + conventions.ProvenanceMarker(conventions.ProvenanceMigrate, "sc-epic", fileDigest([]byte(migrationFixture)))
	f.issues = append(f.issues, epicIssue)
	digest := fileDigest([]byte(migrationFixture))
	stateJSON, _ := json.Marshal(batchState{
		Version: batchStateVersion,
		Repo:    "o/r",
		Digest:  digest,
		Mapping: map[string]int{"sc-epic": 55},
	})
	if err := os.WriteFile(opts.StatePath, stateJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := app.MigrateBeads(ctx, opts); err != nil {
		t.Fatal(err)
	}
	for _, i := range f.issues {
		if i.Title == "Epic: Voltgo support" && i.Number != 55 {
			t.Errorf("epic recreated as #%d", i.Number)
		}
	}
	// sc-1's parent edge must point at the pre-existing #55.
	var one = f.byNumber(102)
	if one == nil {
		one = f.byNumber(101)
	}
	found := false
	for _, i := range f.issues {
		if i.Parent != nil && i.Parent.Number == 55 {
			found = true
		}
	}
	if !found {
		t.Errorf("no issue wired to resumed parent #55: %+v", one)
	}
	// State file now holds all migrated beads.
	state := readState(t, opts.StatePath).Mapping
	if state["sc-epic"] != 55 || state["sc-1"] == 0 || state["sc-2"] == 0 {
		t.Errorf("state = %v", state)
	}
}

func TestMigrateBeadsResumesAfterCreateFailure(t *testing.T) {
	f, app, opts := migrateSetup(t, migrationFixture)
	// The API dies on the second create: the first bead's mapping must
	// already be on disk (state is persisted after every create, not once at
	// the end), and a rerun must pick up where the crash left off.
	f.failAfter["CreateIssue"] = failPoint{calls: 1, err: errors.New("boom")}
	err := app.MigrateBeads(ctx, opts)
	if err == nil || !strings.Contains(err.Error(), "rerun to resume") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(opts.StatePath); err != nil {
		t.Fatalf("no state persisted before the crash: %v", err)
	}
	state := readState(t, opts.StatePath).Mapping
	if len(state) != 1 || state["sc-epic"] != 101 {
		t.Fatalf("state after crash = %v", state)
	}

	delete(f.failAfter, "CreateIssue")
	if err := app.MigrateBeads(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if len(f.issues) != 3 {
		t.Errorf("resume duplicated issues: %d created", len(f.issues))
	}
	epics := 0
	for _, i := range f.issues {
		if i.Title == "Epic: Voltgo support" {
			epics++
		}
	}
	if epics != 1 {
		t.Errorf("epic recreated on resume: %d copies", epics)
	}
	state = readState(t, opts.StatePath).Mapping
	if len(state) != 3 || state["sc-epic"] != 101 {
		t.Errorf("state after resume = %v", state)
	}
}

func TestMigrateBeadsRefusesCorruptState(t *testing.T) {
	f, app, opts := migrateSetup(t, migrationFixture)
	if err := os.WriteFile(opts.StatePath, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A corrupt state file must abort, not be treated as "nothing migrated
	// yet" — that would duplicate every already-migrated issue.
	err := app.MigrateBeads(ctx, opts)
	if err == nil || !strings.Contains(err.Error(), "not a valid resume-state file") {
		t.Fatalf("err = %v", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("API calls made despite corrupt state: %v", f.calls)
	}
}

func TestMigrateBeadsDryRun(t *testing.T) {
	f, app, opts := migrateSetup(t, migrationFixture)
	opts.DryRun = true
	opts.IncludeClosed = true
	if err := app.MigrateBeads(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 0 {
		t.Errorf("dry run made API calls: %v", f.calls)
	}
	if _, err := os.Stat(opts.StatePath); !os.IsNotExist(err) {
		t.Error("dry run wrote state")
	}
	out := app.Out.(interface{ String() string }).String()
	for _, want := range []string{"create: sc-epic as P2 epic", "parent sc-epic", "blocked by sc-1", "then close", "4 beads would be migrated"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan missing %q:\n%s", want, out)
		}
	}
}

func TestMigrateBeadsDroppedEdgeWarns(t *testing.T) {
	// sc-2 is blocked by a closed bead that isn't migrated.
	fixture := `{"_type":"issue","id":"sc-gone","title":"Closed dep","status":"closed","priority":2,"issue_type":"task","created_at":"2026-05-01T00:00:00Z"}
{"_type":"issue","id":"sc-2","title":"Blocked","status":"open","priority":2,"issue_type":"task","created_at":"2026-05-02T00:00:00Z","dependencies":[{"issue_id":"sc-2","depends_on_id":"sc-gone","type":"blocks"}]}
`
	f, app, opts := migrateSetup(t, fixture)
	if err := app.MigrateBeads(ctx, opts); err != nil {
		t.Fatal(err)
	}
	errOut := app.ErrOut.(interface{ String() string }).String()
	if !strings.Contains(errOut, "sc-gone") || !strings.Contains(errOut, "dropped") {
		t.Errorf("no dropped-edge warning: %q", errOut)
	}
	if len(f.byNumber(101).BlockedBy) != 0 {
		t.Error("edge to unmigrated bead created")
	}
}

func TestMigrateBeadsUnknownStatus(t *testing.T) {
	fixture := `{"_type":"issue","id":"sc-x","title":"Weird","status":"blocked","priority":2,"issue_type":"task","created_at":"2026-05-01T00:00:00Z"}
`
	f, app, opts := migrateSetup(t, fixture)
	if err := app.MigrateBeads(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if f.byNumber(101) == nil {
		t.Error("unknown-status bead not migrated")
	}
	errOut := app.ErrOut.(interface{ String() string }).String()
	if !strings.Contains(errOut, "unknown status") {
		t.Errorf("no warning: %q", errOut)
	}
}

func TestMigrateBeadsNothingToDo(t *testing.T) {
	fixture := `{"_type":"issue","id":"sc-done","title":"Old","status":"closed","priority":2,"issue_type":"task","created_at":"2026-05-01T00:00:00Z"}
`
	f, app, opts := migrateSetup(t, fixture)
	if err := app.MigrateBeads(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 0 {
		t.Errorf("calls made: %v", f.calls)
	}
	out := app.Out.(interface{ String() string }).String()
	if !strings.Contains(out, "nothing to migrate") {
		t.Errorf("output = %q", out)
	}
}

func TestMigrateBeadsCreatesLabels(t *testing.T) {
	f, app, opts := migrateSetup(t, migrationFixture)
	if err := app.MigrateBeads(ctx, opts); err != nil {
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

func TestMigrateBeadsMissingFile(t *testing.T) {
	_, app, opts := migrateSetup(t, migrationFixture)
	opts.File = "/nonexistent.jsonl"
	err := app.MigrateBeads(ctx, opts)
	exitCode(t, err, ExitGeneric)
}

func TestMigrateBeadsJSON(t *testing.T) {
	_, app, opts := migrateSetup(t, migrationFixture)
	app.JSON = true
	if err := app.MigrateBeads(ctx, opts); err != nil {
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
	if got.Created != 3 || got.Wired != 2 || len(got.Mapping) != 3 {
		t.Errorf("summary = %+v", got)
	}
}

// A state file without repository or digest binding must abort, not be
// trusted — that would allow mutating unrelated issues from untrusted state (#81).
func TestMigrateBeadsRefusesUnboundState(t *testing.T) {
	f, app, opts := migrateSetup(t, migrationFixture)
	legacy := `{"sc-epic":101,"sc-1":102,"sc-2":103}` + "\n"
	if err := os.WriteFile(opts.StatePath, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	err := app.MigrateBeads(ctx, opts)
	if err == nil || !strings.Contains(err.Error(), "written by an older hew") {
		t.Fatalf("err = %v", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("API calls made despite unbound state: %v", f.calls)
	}
}

func TestMigrateBeadsRefusesMismatchedDigestOrRepo(t *testing.T) {
	cases := map[string]struct {
		state batchState
		want  string
	}{
		"mismatched repo": {
			state: batchState{Version: batchStateVersion, Repo: "other/repo", Digest: fileDigest([]byte(migrationFixture))},
			want:  "belongs to repository",
		},
		"mismatched digest": {
			state: batchState{Version: batchStateVersion, Repo: "o/r", Digest: "sha256:0000000000000000000000000000000000000000000000000000000000000000"},
			want:  "was written for a different snapshot",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f, app, opts := migrateSetup(t, migrationFixture)
			data, _ := json.Marshal(tc.state)
			if err := os.WriteFile(opts.StatePath, data, 0o644); err != nil {
				t.Fatal(err)
			}
			err := app.MigrateBeads(ctx, opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if len(f.calls) != 0 {
				t.Errorf("API calls made despite mismatched state: %v", f.calls)
			}
		})
	}
}

// Regression test for #81: a poisoned migration state mapping a closed bead
// to an unrelated open issue must NOT comment on or close that issue.
func TestMigrateBeadsPoisonedStateCannotCommentOrClose(t *testing.T) {
	f, app, opts := migrateSetup(t, migrationFixture)
	opts.IncludeClosed = true
	// Issue #42 is an existing open issue in the repository.
	target := issue(42, "Unrelated open issue", "P1", "bug")
	target.State = "OPEN"
	target.Body = "Do not close me"
	f.issues = append(f.issues, target)

	// Poison the state file to map closed bead "sc-done" to issue #42.
	digest := fileDigest([]byte(migrationFixture))
	poisoned, _ := json.Marshal(batchState{
		Version: batchStateVersion,
		Repo:    "o/r",
		Digest:  digest,
		Mapping: map[string]int{"sc-done": 42},
	})
	if err := os.WriteFile(opts.StatePath, poisoned, 0o644); err != nil {
		t.Fatal(err)
	}

	err := app.MigrateBeads(ctx, opts)
	if err == nil || !strings.Contains(err.Error(), "does not carry the hew provenance marker") {
		t.Fatalf("err = %v, want missing provenance error", err)
	}

	// Issue #42 must remain OPEN, uncommented, and unmutated.
	checkTarget := f.byNumber(42)
	if checkTarget.State != "OPEN" {
		t.Errorf("unrelated issue #42 was closed: state=%s", checkTarget.State)
	}
	if len(f.comments[42]) != 0 {
		t.Errorf("unrelated issue #42 received comments: %v", f.comments[42])
	}
	for _, call := range f.calls {
		if strings.HasPrefix(call, "CloseIssue") || strings.HasPrefix(call, "Comment") || strings.HasPrefix(call, "CreateIssue") {
			t.Errorf("unexpected mutating call made: %s", call)
		}
	}
}

// Regression test for #81: a poisoned migration state mapping a bead
// to an unrelated issue must NOT wire dependency or parent edges on it.
func TestMigrateBeadsPoisonedStateCannotWireEdges(t *testing.T) {
	f, app, opts := migrateSetup(t, migrationFixture)
	// Issue #42 is an existing issue in the repository.
	target := issue(42, "Unrelated issue", "P2", "task")
	target.State = "OPEN"
	target.Body = "Unrelated issue body"
	f.issues = append(f.issues, target)

	// Poison the state file to map bead "sc-1" to issue #42.
	digest := fileDigest([]byte(migrationFixture))
	poisoned, _ := json.Marshal(batchState{
		Version: batchStateVersion,
		Repo:    "o/r",
		Digest:  digest,
		Mapping: map[string]int{"sc-1": 42},
	})
	if err := os.WriteFile(opts.StatePath, poisoned, 0o644); err != nil {
		t.Fatal(err)
	}

	err := app.MigrateBeads(ctx, opts)
	if err == nil || !strings.Contains(err.Error(), "does not carry the hew provenance marker") {
		t.Fatalf("err = %v, want missing provenance error", err)
	}

	checkTarget := f.byNumber(42)
	if checkTarget.Parent != nil {
		t.Errorf("unrelated issue #42 parent was modified: %+v", checkTarget.Parent)
	}
	if len(checkTarget.BlockedBy) != 0 {
		t.Errorf("unrelated issue #42 blockedBy was modified: %+v", checkTarget.BlockedBy)
	}
}

func TestMigrateBeadsRefusesUnknownIDOrMissingMappedIssue(t *testing.T) {
	t.Run("unknown bead ID in state", func(t *testing.T) {
		f, app, opts := migrateSetup(t, migrationFixture)
		digest := fileDigest([]byte(migrationFixture))
		data, _ := json.Marshal(batchState{
			Version: batchStateVersion,
			Repo:    "o/r",
			Digest:  digest,
			Mapping: map[string]int{"ghost": 99},
		})
		if err := os.WriteFile(opts.StatePath, data, 0o644); err != nil {
			t.Fatal(err)
		}
		err := app.MigrateBeads(ctx, opts)
		if err == nil || !strings.Contains(err.Error(), "is not in this run's selection") {
			t.Fatalf("err = %v, want unknown bead ID error", err)
		}
		if len(f.calls) != 0 {
			t.Errorf("API calls made: %v", f.calls)
		}
	})

	t.Run("mapped issue not on GitHub", func(t *testing.T) {
		f, app, opts := migrateSetup(t, migrationFixture)
		digest := fileDigest([]byte(migrationFixture))
		data, _ := json.Marshal(batchState{
			Version: batchStateVersion,
			Repo:    "o/r",
			Digest:  digest,
			Mapping: map[string]int{"sc-epic": 999},
		})
		if err := os.WriteFile(opts.StatePath, data, 0o644); err != nil {
			t.Fatal(err)
		}
		err := app.MigrateBeads(ctx, opts)
		if err == nil || !strings.Contains(err.Error(), "verifying mapped issue #999") {
			t.Fatalf("err = %v, want verifying mapped issue error", err)
		}
		assertOnlyReads(t, f)
	})
}

// Regression for a checkpoint the tool wrote and then refused: --include-closed
// changes which beads are selected but not the snapshot's contents, so a
// mapping written with the flag must stay readable by a run without it.
func TestMigrateBeadsResumesWithoutIncludeClosedAfterAFullRun(t *testing.T) {
	f, app, opts := migrateSetup(t, migrationFixture)
	opts.IncludeClosed = true
	if err := app.MigrateBeads(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if _, ok := readState(t, opts.StatePath).Mapping["sc-done"]; !ok {
		t.Fatal("the closed bead was never mapped, so the resume proves nothing")
	}
	before := len(f.issues)

	opts.IncludeClosed = false
	if err := app.MigrateBeads(ctx, opts); err != nil {
		t.Fatalf("re-run without --include-closed: %v", err)
	}
	if len(f.issues) != before {
		t.Errorf("re-run duplicated issues: %d → %d", before, len(f.issues))
	}
}

// The digest must come from the snapshot's contents: a fileDigest that
// ignored its input would satisfy every hand-written mismatch assertion while
// letting one snapshot's state drive another's run.
func TestMigrateBeadsRejectsStateFromADifferentSnapshot(t *testing.T) {
	f, app, opts := migrateSetup(t, migrationFixture)
	if err := app.MigrateBeads(ctx, opts); err != nil {
		t.Fatal(err)
	}
	before := len(f.issues)

	edited := migrationFixture + `{"_type":"issue","id":"sc-late","title":"Added later","status":"open","priority":2,"issue_type":"task","created_at":"2026-06-01T00:00:00Z"}` + "\n"
	if err := os.WriteFile(opts.File, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	err := app.MigrateBeads(ctx, opts)
	if err == nil || !strings.Contains(err.Error(), "was written for a different snapshot") {
		t.Fatalf("err = %v, want a rejection naming the changed snapshot", err)
	}
	if len(f.issues) != before {
		t.Errorf("issues created against a rejected state file: %d → %d", before, len(f.issues))
	}
	if len(f.calls) != 0 {
		t.Errorf("API calls made despite a rejected state file: %v", f.calls)
	}
}

// The marker binds the bead ID, so a state file cannot point one bead at the
// issue another bead produced in the same migration — repo, digest, and "hew
// created this" all check out and only the identity is wrong.
func TestMigrateBeadsRejectsStateMappingABeadToAnotherBeadsIssue(t *testing.T) {
	f, app, opts := migrateSetup(t, migrationFixture)
	if err := app.MigrateBeads(ctx, opts); err != nil {
		t.Fatal(err)
	}
	// #101 is sc-epic's issue, carrying sc-epic's marker.
	swapped, _ := json.Marshal(batchState{
		Version: batchStateVersion,
		Repo:    "o/r",
		Digest:  fileDigest([]byte(migrationFixture)),
		Mapping: map[string]int{"sc-1": 101},
	})
	if err := os.WriteFile(opts.StatePath, swapped, 0o644); err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	err := app.MigrateBeads(ctx, opts)
	if err == nil || !strings.Contains(err.Error(), "does not carry the hew provenance marker") {
		t.Fatalf("err = %v, want a rejection for the mismatched bead ID", err)
	}
	assertOnlyReads(t, f)
}

// migrateClose is the last gate before the two destructive calls. Reaching it
// needs an issue that verifies at load and loses its marker before the close
// pass — what a concurrent body edit looks like.
func TestMigrateBeadsDoesNotCloseAnIssueThatLostItsProvenance(t *testing.T) {
	f, app, opts := migrateSetup(t, migrationFixture)
	opts.IncludeClosed = true
	digest := fileDigest([]byte(migrationFixture))

	target := issue(55, "Old fix", "P3", "bug")
	target.ID = "ID55"
	target.State = "OPEN" // it must be closable, or the guard is never reached
	target.Body = "Migrated from beads `sc-done`\n\n" +
		conventions.ProvenanceMarker(conventions.ProvenanceMigrate, "sc-done", digest)
	f.issues = append(f.issues, target)
	stateJSON, _ := json.Marshal(batchState{
		Version: batchStateVersion,
		Repo:    "o/r",
		Digest:  digest,
		Mapping: map[string]int{"sc-done": 55},
	})
	if err := os.WriteFile(opts.StatePath, stateJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	// Someone rewrites the body between verification and the close pass.
	f.onGetIssue = func(number int) {
		if number == 55 {
			f.byNumber(55).Body = "rewritten by hand"
		}
	}

	if err := app.MigrateBeads(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if got := f.byNumber(55); !got.IsOpen() {
		t.Error("closed an issue whose provenance no longer verified")
	}
	for _, call := range f.calls {
		if strings.HasPrefix(call, "Comment 55") || strings.HasPrefix(call, "CloseIssue 55") {
			t.Errorf("mutating call against the unverified issue: %s", call)
		}
	}
	if warning := app.ErrOut.(interface{ String() string }).String(); !strings.Contains(warning, "not closing #55") {
		t.Errorf("skipped the close without saying so: %q", warning)
	}
}

func TestMigrateBeadsNoFileArg(t *testing.T) {
	_, app, _ := migrateSetup(t, migrationFixture)
	err := app.MigrateBeads(ctx, MigrateOpts{})
	if err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("err = %v, want a usage error", err)
	}
	if code := ExitCode(err); code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
}

func TestMigrateBeadsDefaultStatePath(t *testing.T) {
	_, app, opts := migrateSetup(t, migrationFixture)
	opts.StatePath = ""
	if err := app.MigrateBeads(ctx, opts); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(filepath.Dir(opts.File), "github-migration.json")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("no state file at the default path %s: %v", want, err)
	}
}
