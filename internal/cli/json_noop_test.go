package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/internal/model"
)

// The primer promises every command takes --json, and the paths an agent hits
// most are the idempotent retries: the request already holds, so nothing is
// written. Each of these asserts exit 0 produces one parseable JSON document.

func decodeJSON(t *testing.T, out *bytes.Buffer) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out.String())
	}
	return got
}

func assertUnchangedIssue(t *testing.T, got map[string]any, number int) {
	t.Helper()
	if n, _ := got["number"].(float64); int(n) != number {
		t.Errorf("number = %v, want %d", got["number"], number)
	}
	if got["unchanged"] != true {
		t.Errorf("unchanged = %v, want true", got["unchanged"])
	}
}

// assertNoWrites fails on any call that is not an issue read; unlike
// assertOnlyReads it also allows the open-issue listing Block starts from.
func assertNoWrites(t *testing.T, f *fakeClient) {
	t.Helper()
	for _, call := range f.calls {
		if !strings.HasPrefix(call, "GetIssue") && !strings.HasPrefix(call, "ListIssues") {
			t.Errorf("no-op made a write: %s", call)
		}
	}
}

func TestReopenOpenIssueIsJSON(t *testing.T) {
	f := newFake(issue(1, "Work", "P2", "bug"))
	app, out, _ := newApp(f)
	app.JSON = true
	if err := app.Reopen(ctx, 1, "r"); err != nil {
		t.Fatal(err)
	}
	got := decodeJSON(t, out)
	assertUnchangedIssue(t, got, 1)
	if got["state"] != "open" {
		t.Errorf("state = %v, want open", got["state"])
	}
	assertOnlyReads(t, f)
}

func TestBlockAlreadyBlockedIsJSON(t *testing.T) {
	a := issue(1, "A", "P2", "bug")
	a.BlockedBy = []model.Ref{{Number: 2, State: "OPEN"}}
	f := newFake(a, issue(2, "B", "P2", "bug"))
	app, out, _ := newApp(f)
	app.JSON = true
	if err := app.Block(ctx, 1, 2, false); err != nil {
		t.Fatal(err)
	}
	got := decodeJSON(t, out)
	assertUnchangedIssue(t, got, 1)
	if blockers, _ := got["openBlockers"].([]any); len(blockers) != 1 {
		t.Errorf("openBlockers = %v, want the existing edge", got["openBlockers"])
	}
	assertNoWrites(t, f)
}

// The no-op and the changed write answer with the same document, so a caller
// parses one shape whichever happened: Block's listing read is leaner than
// GetIssue, and the no-op has to re-fetch rather than echo it.
func TestBlockNoOpJSONHasTheSameKeysAsAChangedBlock(t *testing.T) {
	keys := func(got map[string]any) map[string]bool {
		m := map[string]bool{}
		for k := range got {
			m[k] = true
		}
		return m
	}
	f := newFake(issue(1, "A", "P2", "bug"), issue(2, "B", "P2", "bug"))
	app, out, _ := newApp(f)
	app.JSON = true
	if err := app.Block(ctx, 1, 2, false); err != nil {
		t.Fatal(err)
	}
	changed := decodeJSON(t, out)
	out.Reset()
	if err := app.Block(ctx, 1, 2, false); err != nil {
		t.Fatal(err)
	}
	noop := decodeJSON(t, out)
	if noop["unchanged"] != true || changed["unchanged"] != nil {
		t.Fatalf("unchanged: noop = %v, changed = %v", noop["unchanged"], changed["unchanged"])
	}
	delete(noop, "unchanged")
	got, want := keys(noop), keys(changed)
	for k := range want {
		if !got[k] {
			t.Errorf("no-op document lacks %q", k)
		}
	}
	for k := range got {
		if !want[k] {
			t.Errorf("no-op document has extra %q", k)
		}
	}
}

func TestUnblockNotBlockedIsJSON(t *testing.T) {
	f := newFake(issue(1, "A", "P2", "bug"), issue(2, "B", "P2", "bug"))
	app, out, _ := newApp(f)
	app.JSON = true
	if err := app.Unblock(ctx, 1, 2, false); err != nil {
		t.Fatal(err)
	}
	assertUnchangedIssue(t, decodeJSON(t, out), 1)
	assertOnlyReads(t, f)
}

func TestApplyEmptyPlanIsJSON(t *testing.T) {
	f, app, opts := applySetup(t, "\n")
	app.JSON = true
	if err := app.Apply(ctx, opts); err != nil {
		t.Fatal(err)
	}
	got := decodeJSON(t, app.Out.(*bytes.Buffer))
	if got["created"] != float64(0) || got["wired"] != float64(0) {
		t.Errorf("counts = %v/%v, want 0/0", got["created"], got["wired"])
	}
	if w, ok := got["warnings"].([]any); !ok || len(w) != 0 {
		t.Errorf("warnings = %#v, want an empty array", got["warnings"])
	}
	if len(f.calls) != 0 {
		t.Errorf("calls made: %v", f.calls)
	}
}

func TestApplyDryRunIsJSON(t *testing.T) {
	f, app, opts := applySetup(t, applyFixture)
	app.JSON = true
	opts.DryRun = true
	if err := app.Apply(ctx, opts); err != nil {
		t.Fatal(err)
	}
	got := decodeJSON(t, app.Out.(*bytes.Buffer))
	if got["dryRun"] != true {
		t.Errorf("dryRun = %v, want true", got["dryRun"])
	}
	toCreate, _ := got["wouldCreate"].([]any)
	if len(toCreate) != 3 {
		t.Fatalf("wouldCreate = %v, want 3 entries", got["wouldCreate"])
	}
	if epic, _ := toCreate[0].(map[string]any); epic["parent"] != nil {
		t.Errorf("epic1 parent = %v, want null", epic["parent"])
	} else if _, ok := epic["parent"]; !ok {
		t.Errorf("epic1 lacks a parent key: %v", epic)
	}
	scaffold, _ := toCreate[1].(map[string]any)
	if scaffold["key"] != "scaffold" || scaffold["priority"] != "P2" || scaffold["type"] != "task" ||
		scaffold["title"] != "Scaffold" || scaffold["parent"] != "epic1" {
		t.Errorf("scaffold = %v", scaffold)
	}
	collector, _ := toCreate[2].(map[string]any)
	if blockers, _ := collector["blockedBy"].([]any); len(blockers) != 2 || blockers[0] != "scaffold" || blockers[1] != "#42" {
		t.Errorf("collector blockedBy = %v", collector["blockedBy"])
	}
	assertOnlyReads(t, f)
}

func TestApplyEmptyPlanDryRunIsJSON(t *testing.T) {
	_, app, opts := applySetup(t, "\n")
	app.JSON = true
	opts.DryRun = true
	if err := app.Apply(ctx, opts); err != nil {
		t.Fatal(err)
	}
	got := decodeJSON(t, app.Out.(*bytes.Buffer))
	if got["dryRun"] != true {
		t.Errorf("dryRun = %v, want true", got["dryRun"])
	}
	if c, ok := got["wouldCreate"].([]any); !ok || len(c) != 0 {
		t.Errorf("wouldCreate = %#v, want an empty array", got["wouldCreate"])
	}
	if m, ok := got["alreadyCreated"].(map[string]any); !ok || len(m) != 0 {
		t.Errorf("alreadyCreated = %#v, want an empty object", got["alreadyCreated"])
	}
}

func TestApplyDryRunJSONReportsAlreadyCreated(t *testing.T) {
	_, app, opts := applySetup(t, applyFixture)
	if err := app.Apply(ctx, opts); err != nil {
		t.Fatal(err)
	}
	app.Out.(*bytes.Buffer).Reset()
	app.JSON = true
	opts.DryRun = true
	if err := app.Apply(ctx, opts); err != nil {
		t.Fatal(err)
	}
	got := decodeJSON(t, app.Out.(*bytes.Buffer))
	if c, _ := got["wouldCreate"].([]any); len(c) != 0 {
		t.Errorf("wouldCreate = %v, want empty on a finished plan", c)
	}
	if m, _ := got["alreadyCreated"].(map[string]any); len(m) != 3 || m["epic1"] != float64(101) {
		t.Errorf("alreadyCreated = %v", got["alreadyCreated"])
	}
}

func TestMigrateBeadsNothingToDoIsJSON(t *testing.T) {
	fixture := `{"_type":"issue","id":"sc-done","title":"Old","status":"closed","priority":2,"issue_type":"task","created_at":"2026-05-01T00:00:00Z"}
`
	f, app, opts := migrateSetup(t, fixture)
	app.JSON = true
	if err := app.MigrateBeads(ctx, opts); err != nil {
		t.Fatal(err)
	}
	got := decodeJSON(t, app.Out.(*bytes.Buffer))
	if got["created"] != float64(0) || got["skippedClosed"] != float64(1) {
		t.Errorf("created/skippedClosed = %v/%v, want 0/1", got["created"], got["skippedClosed"])
	}
	if len(f.calls) != 0 {
		t.Errorf("calls made: %v", f.calls)
	}
}

func TestMigrateBeadsNothingToDoDryRunIsJSON(t *testing.T) {
	fixture := `{"_type":"issue","id":"sc-done","title":"Old","status":"closed","priority":2,"issue_type":"task","created_at":"2026-05-01T00:00:00Z"}
`
	_, app, opts := migrateSetup(t, fixture)
	app.JSON = true
	opts.DryRun = true
	if err := app.MigrateBeads(ctx, opts); err != nil {
		t.Fatal(err)
	}
	got := decodeJSON(t, app.Out.(*bytes.Buffer))
	if got["dryRun"] != true || got["skippedClosed"] != float64(1) {
		t.Errorf("dryRun/skippedClosed = %v/%v, want true/1", got["dryRun"], got["skippedClosed"])
	}
	if c, ok := got["wouldMigrate"].([]any); !ok || len(c) != 0 {
		t.Errorf("wouldMigrate = %#v, want an empty array", got["wouldMigrate"])
	}
}

func TestMigrateBeadsDryRunIsJSON(t *testing.T) {
	f, app, opts := migrateSetup(t, migrationFixture)
	app.JSON = true
	opts.DryRun = true
	opts.IncludeClosed = true
	if err := app.MigrateBeads(ctx, opts); err != nil {
		t.Fatal(err)
	}
	got := decodeJSON(t, app.Out.(*bytes.Buffer))
	if got["dryRun"] != true {
		t.Errorf("dryRun = %v, want true", got["dryRun"])
	}
	toCreate, _ := got["wouldMigrate"].([]any)
	if len(toCreate) != 4 {
		t.Fatalf("wouldMigrate = %v, want 4 entries", got["wouldMigrate"])
	}
	byID := map[string]map[string]any{}
	for _, e := range toCreate {
		m, _ := e.(map[string]any)
		byID[m["id"].(string)] = m
	}
	if e := byID["sc-epic"]; e["priority"] != "P2" || e["type"] != "epic" {
		t.Errorf("sc-epic = %v", e)
	}
	sc1 := byID["sc-1"]
	if sc1["parent"] != "sc-epic" || sc1["inProgress"] != true || sc1["type"] != "enhancement" {
		t.Errorf("sc-1 = %v", sc1)
	}
	if blockers, _ := byID["sc-2"]["blockedBy"].([]any); len(blockers) != 1 || blockers[0] != "sc-1" {
		t.Errorf("sc-2 blockedBy = %v", byID["sc-2"]["blockedBy"])
	}
	// Absent facts are null, never a missing key: every entry has one shape.
	for id, e := range byID {
		for _, k := range []string{"type", "parent", "blockedBy"} {
			if _, ok := e[k]; !ok {
				t.Errorf("%s lacks key %q: %v", id, k, e)
			}
		}
	}
	if byID["sc-epic"]["parent"] != nil {
		t.Errorf("sc-epic parent = %v, want null", byID["sc-epic"]["parent"])
	}
	if byID["sc-done"]["thenClose"] != true {
		t.Errorf("sc-done = %v", byID["sc-done"])
	}
	if len(f.calls) != 0 {
		t.Errorf("dry run made API calls: %v", f.calls)
	}
}
