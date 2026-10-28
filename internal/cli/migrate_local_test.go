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
	"time"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/local"
)

// localTracker makes a local tracker in a temp dir and opens it as "dev".
func localTracker(t *testing.T) (*local.Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), local.DirName)
	if err := local.Init(dir); err != nil {
		t.Fatal(err)
	}
	s, err := local.Open(dir, "dev")
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func mustLocal(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func mustCreateLocal(t *testing.T, s *local.Store, title, body string, labels ...string) int {
	t.Helper()
	i, err := s.CreateIssue(ctx, title, body, labels)
	mustLocal(t, err)
	return i.Number
}

// migrateLocalSetup returns a fake GitHub, its app, and options for a tracker
// at dir. Local numbers start at 1 and the fake's first created issue is #101.
func migrateLocalSetup(dir string) (*fakeClient, *App, MigrateLocalOpts) {
	f := newFake()
	app, _, _ := newApp(f)
	return f, app, MigrateLocalOpts{Dir: dir}
}

func TestMigrateLocalKeepsCloseReasonsAndComments(t *testing.T) {
	s, dir := localTracker(t)
	done := mustCreateLocal(t, s, "Shipped", "body one", "P2", "task")
	dropped := mustCreateLocal(t, s, "Dropped", "", "P3", "bug")
	dup := mustCreateLocal(t, s, "Same as shipped", "", "P3", "bug")
	open := mustCreateLocal(t, s, "Still going", "", "P1", "enhancement")
	mustLocal(t, s.Comment(ctx, done, "landed in abc123"))
	mustLocal(t, s.CloseIssue(ctx, done, gh.CloseCompleted))
	mustLocal(t, s.Comment(ctx, dropped, "not worth it"))
	mustLocal(t, s.CloseIssue(ctx, dropped, gh.CloseNotPlanned))
	mustLocal(t, s.Comment(ctx, dup, fmt.Sprintf("Duplicate of #%d", done)))
	mustLocal(t, s.CloseIssue(ctx, dup, gh.CloseDuplicate))
	mustLocal(t, s.Comment(ctx, open, "first thought"))
	mustLocal(t, s.Comment(ctx, open, "second thought"))

	f, app, opts := migrateLocalSetup(dir)
	if err := app.MigrateLocal(ctx, opts); err != nil {
		t.Fatal(err)
	}

	gDone, gDropped, gDup, gOpen := 101, 102, 103, 104
	for n, want := range map[int]struct{ state, reason string }{
		gDone:    {"CLOSED", "COMPLETED"},
		gDropped: {"CLOSED", "NOT_PLANNED"},
		gDup:     {"CLOSED", "DUPLICATE"},
		gOpen:    {"OPEN", ""},
	} {
		i := f.byNumber(n)
		if i.State != want.state || i.StateReason != want.reason {
			t.Errorf("#%d is %s/%s, want %s/%s", n, i.State, i.StateReason, want.state, want.reason)
		}
	}
	if got := f.comments[gDone]; len(got) != 1 || !strings.Contains(got[0], "landed in abc123") {
		t.Errorf("shipped comments = %q", got)
	}
	if got := f.comments[gDropped]; len(got) != 1 || !strings.Contains(got[0], "not worth it") {
		t.Errorf("dropped comments = %q", got)
	}
	if got := f.comments[gDup]; len(got) != 1 || !strings.Contains(got[0], fmt.Sprintf("Duplicate of #%d", gDone)) {
		t.Errorf("duplicate comments = %q, want the reference pointing at the new number", got)
	}
	got := f.comments[gOpen]
	if len(got) != 2 || !strings.Contains(got[0], "first thought") || !strings.Contains(got[1], "second thought") {
		t.Errorf("open issue comments = %q, want both in order", got)
	}
	// The author and date are not GitHub's to carry, so the comment says them,
	// in a code span so a local login never mentions a real account.
	if !strings.Contains(got[0], "`dev`") || !strings.Contains(got[0], "commented on") {
		t.Errorf("comment %q does not credit its local author", got[0])
	}
	if b := f.byNumber(gDone).Body; !strings.Contains(b, "body one") || !strings.Contains(b, "Migrated from local issue `#1`") {
		t.Errorf("body = %q, want the original text and a provenance footer", b)
	}
}

func TestMigrateLocalCarriesLabelsEdgesAndClaims(t *testing.T) {
	s, dir := localTracker(t)
	mustLocal(t, s.CreateLabel(ctx, gh.Label{Name: "area-x", Color: "123456", Description: "an area"}))
	epic := mustCreateLocal(t, s, "Epic: Big", "", "P2", "area-x")
	first := mustCreateLocal(t, s, "First", "", "P2", "task")
	second := mustCreateLocal(t, s, "Second", "", "P1", "task", "hand-made")
	mustLocal(t, s.AddSubIssue(ctx, epic, first, false))
	mustLocal(t, s.AddSubIssue(ctx, epic, second, false))
	mustLocal(t, s.AddBlockedBy(ctx, second, first))
	mustLocal(t, s.ClaimIssue(ctx, second, "dev-worktree-abc123", false))

	f, app, opts := migrateLocalSetup(dir)
	f.viewer = "ghuser"
	if err := app.MigrateLocal(ctx, opts); err != nil {
		t.Fatal(err)
	}

	gEpic, gFirst, gSecond := 101, 102, 103
	calls := strings.Join(f.calls, "\n")
	for _, want := range []string{
		fmt.Sprintf("AddSubIssue %d %d", gEpic, gFirst),
		fmt.Sprintf("AddSubIssue %d %d", gEpic, gSecond),
		fmt.Sprintf("AddBlockedBy %d %d", gSecond, gFirst),
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("missing %q in calls:\n%s", want, calls)
		}
	}
	sec := f.byNumber(gSecond)
	for _, want := range []string{"P1", "task", "hand-made", "in-progress"} {
		if !slices.Contains(sec.Labels, want) {
			t.Errorf("second labels = %v, missing %q", sec.Labels, want)
		}
	}
	// A local claimant is not a GitHub login; the migrating user takes the claim.
	if !slices.Equal(sec.Assignees, []string{"ghuser"}) {
		t.Errorf("assignees = %v, want the viewer, not the local handle", sec.Assignees)
	}
	var labelNames []string
	for _, l := range f.labels {
		labelNames = append(labelNames, l.Name)
		if l.Name == "area-x" && (l.Color != "123456" || l.Description != "an area") {
			t.Errorf("area-x label = %+v, want the local cosmetics", l)
		}
	}
	for _, want := range []string{"area-x", "hand-made", "P1", "bug"} {
		if !slices.Contains(labelNames, want) {
			t.Errorf("labels created = %v, missing %q", labelNames, want)
		}
	}
}

func TestMigrateLocalRewritesReferencesOutsideCodeSpans(t *testing.T) {
	s, dir := localTracker(t)
	a := mustCreateLocal(t, s, "A", "Blocked on #2 and `#2` and #99.\n\n```\n#2 in a fence\n```\n", "P2", "task")
	b := mustCreateLocal(t, s, "Follow-up to #1", "Follow-up to #1", "P2", "task")
	mustLocal(t, s.Comment(ctx, a, "see #2, not `#2`"))
	mustLocal(t, s.CloseIssue(ctx, a, gh.CloseCompleted))
	mustLocal(t, s.Comment(ctx, b, "back to #1"))

	f, app, opts := migrateLocalSetup(dir)
	if err := app.MigrateLocal(ctx, opts); err != nil {
		t.Fatal(err)
	}

	wantA := "Blocked on #102 and `#2` and #99.\n\n```\n#2 in a fence\n```"
	if got := f.byNumber(101).Body; !strings.HasPrefix(got, wantA) {
		t.Errorf("a body = %q, want prefix %q", got, wantA)
	}
	if got := f.byNumber(102).Title; got != "Follow-up to #101" {
		t.Errorf("b title = %q, want the reference in it rewritten too", got)
	}
	if got := f.byNumber(102).Body; !strings.HasPrefix(got, "Follow-up to #101") {
		t.Errorf("b body = %q, want its reference to a rewritten", got)
	}
	if got := f.comments[101]; len(got) != 1 || !strings.Contains(got[0], "see #102, not `#2`") {
		t.Errorf("a comments = %q", got)
	}
	if got := f.comments[102]; len(got) != 1 || !strings.Contains(got[0], "back to #101") {
		t.Errorf("b comments = %q", got)
	}
	// The footer names the local number in a code span, so it survives.
	if got := f.byNumber(101).Body; !strings.Contains(got, "Migrated from local issue `#1`") {
		t.Errorf("a body lost its footer: %q", got)
	}
}

func TestMigrateLocalSealsTheTrackerAsAnArchive(t *testing.T) {
	s, dir := localTracker(t)
	a := mustCreateLocal(t, s, "A", "", "P2", "task")
	b := mustCreateLocal(t, s, "B", "", "P2", "task")
	archived, err := os.ReadFile(filepath.Join(dir, "issues.jsonl"))
	mustLocal(t, err)

	_, app, opts := migrateLocalSetup(dir)
	if err := app.MigrateLocal(ctx, opts); err != nil {
		t.Fatal(err)
	}

	cfg, err := local.ReadConfig(dir)
	mustLocal(t, err)
	if cfg.Backend != local.BackendGitHub {
		t.Errorf("backend = %q, want github", cfg.Backend)
	}
	m, err := local.ReadMigrated(dir)
	mustLocal(t, err)
	if m.Repo != "o/r" || m.Mapping[a] != 101 || m.Mapping[b] != 102 {
		t.Errorf("migrated = %+v, want o/r with 1→101 and 2→102", m)
	}
	now, err := os.ReadFile(filepath.Join(dir, "issues.jsonl"))
	mustLocal(t, err)
	if string(now) != string(archived) {
		t.Error("migration rewrote the archive")
	}
	// The store opened before the migration still cannot write to the archive.
	if err := s.Comment(ctx, a, "late"); err == nil {
		t.Error("a write to issues.jsonl after migration was accepted")
	}
	if _, err := local.Open(dir, "dev"); err == nil {
		t.Error("the migrated tracker opened as local")
	}
	// A second run has nothing to do and must not touch GitHub again.
	f2, app2, opts2 := migrateLocalSetup(dir)
	err = app2.MigrateLocal(ctx, opts2)
	if err == nil || !strings.Contains(err.Error(), "not locally") {
		t.Errorf("rerun err = %v, want a refusal that the tracker is no longer local", err)
	}
	if len(f2.calls) != 0 {
		t.Errorf("rerun called GitHub: %v", f2.calls)
	}
}

func TestMigrateLocalDryRunWritesNothing(t *testing.T) {
	s, dir := localTracker(t)
	epic := mustCreateLocal(t, s, "Epic: Big", "", "P2")
	child := mustCreateLocal(t, s, "Child", "", "P1", "bug")
	mustLocal(t, s.AddSubIssue(ctx, epic, child, false))
	mustLocal(t, s.CloseIssue(ctx, child, gh.CloseCompleted))

	f, app, opts := migrateLocalSetup(dir)
	opts.DryRun = true
	if err := app.MigrateLocal(ctx, opts); err != nil {
		t.Fatal(err)
	}
	out := app.Out.(interface{ String() string }).String()
	for _, want := range []string{"create: 1 as P2", "create: 2 as P1 bug", "parent 1", "then close", "2 local issues would be migrated"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan missing %q:\n%s", want, out)
		}
	}
	assertOnlyReads(t, f)
	if _, err := os.Stat(filepath.Join(dir, "migration-state.json")); !os.IsNotExist(err) {
		t.Error("dry run wrote a checkpoint")
	}
	if cfg, _ := local.ReadConfig(dir); cfg.Backend != local.BackendLocal {
		t.Errorf("dry run flipped the backend to %q", cfg.Backend)
	}
	if _, err := os.Stat(filepath.Join(dir, local.MigratedFile)); !os.IsNotExist(err) {
		t.Error("dry run wrote migrated.json")
	}
	// A dry run must not leave the tracker locked or frozen.
	mustLocal(t, s.Comment(ctx, epic, "still writable"))
}

func TestMigrateLocalDryRunJSON(t *testing.T) {
	s, dir := localTracker(t)
	mustCreateLocal(t, s, "Only", "", "P3", "task")
	_, app, opts := migrateLocalSetup(dir)
	app.JSON = true
	opts.DryRun = true
	if err := app.MigrateLocal(ctx, opts); err != nil {
		t.Fatal(err)
	}
	var got struct {
		DryRun          bool             `json:"dryRun"`
		WouldMigrate    []map[string]any `json:"wouldMigrate"`
		AlreadyMigrated map[string]int   `json:"alreadyMigrated"`
	}
	if err := json.Unmarshal([]byte(app.Out.(interface{ String() string }).String()), &got); err != nil {
		t.Fatal(err)
	}
	if !got.DryRun || len(got.WouldMigrate) != 1 || got.WouldMigrate[0]["id"] != "1" || got.WouldMigrate[0]["priority"] != "P3" {
		t.Errorf("dry run json = %+v", got)
	}
	if got.AlreadyMigrated == nil {
		t.Error("alreadyMigrated absent; an empty map keeps the shape")
	}
}

func TestMigrateLocalRefusesAResumeAfterTheTrackerChanged(t *testing.T) {
	s, dir := localTracker(t)
	for i := 1; i <= 3; i++ {
		mustCreateLocal(t, s, fmt.Sprintf("Issue %d", i), "", "P2", "task")
	}
	f, app, opts := migrateLocalSetup(dir)
	f.failAfter["CreateIssue"] = failPoint{calls: 1, err: errors.New("api down")}
	err := app.MigrateLocal(ctx, opts)
	if err == nil || !strings.Contains(err.Error(), "api down") {
		t.Fatalf("err = %v, want the injected failure", err)
	}
	// The failed run let go of the tracker, so work can go on locally...
	mustLocal(t, s.Comment(ctx, 2, "written between runs"))
	// ...but a checkpoint is bound to the tracker's contents, and these changed.
	delete(f.failAfter, "CreateIssue")
	before := len(f.calls)
	err = app.MigrateLocal(ctx, opts)
	if err == nil || !strings.Contains(err.Error(), "different issues.jsonl") {
		t.Fatalf("resume err = %v, want the checkpoint refused", err)
	}
	if len(f.calls) != before {
		t.Errorf("a refused resume called GitHub: %v", f.calls[before:])
	}
}

func TestMigrateLocalFinishesAnInterruptedRunWithoutDuplicates(t *testing.T) {
	s, dir := localTracker(t)
	for i := 1; i <= 4; i++ {
		mustCreateLocal(t, s, fmt.Sprintf("Issue %d", i), fmt.Sprintf("refers to #%d", i%4+1), "P2", "task")
	}
	mustLocal(t, s.Comment(ctx, 3, "a note"))
	mustLocal(t, s.CloseIssue(ctx, 1, gh.CloseCompleted))

	f, app, opts := migrateLocalSetup(dir)
	f.failAfter["CreateIssue"] = failPoint{calls: 2, err: errors.New("api down")}
	if err := app.MigrateLocal(ctx, opts); err == nil {
		t.Fatal("injected failure not reported")
	}
	if len(f.issues) != 2 {
		t.Fatalf("first run created %d issues, want 2 before the failure", len(f.issues))
	}
	if cfg, _ := local.ReadConfig(dir); cfg.Backend != local.BackendLocal {
		t.Fatalf("a failed run flipped the backend to %q", cfg.Backend)
	}

	delete(f.failAfter, "CreateIssue")
	if err := app.MigrateLocal(ctx, opts); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if f.callCounts["CreateIssue"] != 4+1 { // four successes plus the one that failed
		t.Errorf("CreateIssue called %d times, want each issue created once (+1 failed attempt)", f.callCounts["CreateIssue"])
	}
	if len(f.issues) != 4 {
		t.Fatalf("issues on GitHub = %d, want 4", len(f.issues))
	}
	if got := f.byNumber(101); got.State != "CLOSED" || !strings.Contains(got.Body, "refers to #102") {
		t.Errorf("first issue = %s %q, want closed with its reference rewritten", got.State, got.Body)
	}
	if got := f.comments[103]; len(got) != 1 {
		t.Errorf("note comments = %q, want exactly one", got)
	}
	if cfg, _ := local.ReadConfig(dir); cfg.Backend != local.BackendGitHub {
		t.Errorf("backend = %q after a clean resume, want github", cfg.Backend)
	}
}

func TestMigrateLocalDoesNotSealWhileStepsAreIncomplete(t *testing.T) {
	s, dir := localTracker(t)
	a := mustCreateLocal(t, s, "A", "", "P2", "task")
	b := mustCreateLocal(t, s, "B", "", "P2", "task")
	mustLocal(t, s.AddBlockedBy(ctx, b, a))

	f, app, opts := migrateLocalSetup(dir)
	f.failOn["AddBlockedBy"] = errors.New("rate limited")
	err := app.MigrateLocal(ctx, opts)
	if err == nil || !strings.Contains(err.Error(), "rerun") {
		t.Fatalf("err = %v, want an error telling the caller to rerun", err)
	}
	if cfg, _ := local.ReadConfig(dir); cfg.Backend != local.BackendLocal {
		t.Errorf("backend = %q, want local while an edge is missing", cfg.Backend)
	}
	if _, err := os.Stat(filepath.Join(dir, local.MigratedFile)); !os.IsNotExist(err) {
		t.Error("migrated.json written for an incomplete migration")
	}

	delete(f.failOn, "AddBlockedBy")
	if err := app.MigrateLocal(ctx, opts); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if cfg, _ := local.ReadConfig(dir); cfg.Backend != local.BackendGitHub {
		t.Errorf("backend = %q after the rerun, want github", cfg.Backend)
	}
}

func TestMigrateLocalOfAnEmptyTrackerStillHandsOver(t *testing.T) {
	_, dir := localTracker(t)
	_, app, opts := migrateLocalSetup(dir)
	if err := app.MigrateLocal(ctx, opts); err != nil {
		t.Fatal(err)
	}
	m, err := local.ReadMigrated(dir)
	mustLocal(t, err)
	if len(m.Mapping) != 0 {
		t.Errorf("mapping = %v, want empty", m.Mapping)
	}
	if cfg, _ := local.ReadConfig(dir); cfg.Backend != local.BackendGitHub {
		t.Errorf("backend = %q, want github", cfg.Backend)
	}
}

func TestMigrateLocalRefusesAStateFromAnotherRepo(t *testing.T) {
	s, dir := localTracker(t)
	mustCreateLocal(t, s, "A", "", "P2", "task")
	state := batchState{Version: batchStateVersion, Repo: "someone/else", Digest: "sha256:x", Mapping: map[string]int{"1": 7}}
	raw, err := json.Marshal(state)
	mustLocal(t, err)
	mustLocal(t, os.WriteFile(filepath.Join(dir, "migration-state.json"), raw, 0o644))

	f, app, opts := migrateLocalSetup(dir)
	if err := app.MigrateLocal(ctx, opts); err == nil || !strings.Contains(err.Error(), "belongs to repository") {
		t.Errorf("err = %v, want the foreign checkpoint refused", err)
	}
	assertOnlyReads(t, f)
	if cfg, _ := local.ReadConfig(dir); cfg.Backend != local.BackendLocal {
		t.Errorf("backend = %q, want local", cfg.Backend)
	}
}

func TestMigrateLocalLetsGoOfTheTrackerWhenItFails(t *testing.T) {
	s, dir := localTracker(t)
	n := mustCreateLocal(t, s, "A", "", "P2", "task")
	f, app, opts := migrateLocalSetup(dir)
	f.failOn["CreateIssue"] = errors.New("down")
	if err := app.MigrateLocal(ctx, opts); err == nil {
		t.Fatal("injected failure not reported")
	}
	mustLocal(t, s.Comment(ctx, n, "writable again"))
}

func TestRewriteLocalRefs(t *testing.T) {
	m := map[int]int{1: 101, 12: 112, 3: 103}
	for name, tc := range map[string]struct{ in, want string }{
		"plain":              {"fixes #1", "fixes #101"},
		"start and end":      {"#1", "#101"},
		"punctuation":        {"(#1), #12; #3.", "(#101), #112; #103."},
		"adjacent":           {"#1 #12 #3", "#101 #112 #103"},
		"not in mapping":     {"see #2 and #99", "see #2 and #99"},
		"longer number":      {"#123 is not #12", "#123 is not #112"},
		"inline code":        {"`#1` but #1", "`#1` but #101"},
		"double backticks":   {"``a ` #1`` then #1", "``a ` #1`` then #101"},
		"unterminated tick":  {"a ` then #1", "a ` then #101"},
		"fenced block":       {"#1\n```\n#1\n```\n#1", "#101\n```\n#1\n```\n#101"},
		"tilde fence":        {"~~~\n#1\n~~~\n#1", "~~~\n#1\n~~~\n#101"},
		"longer closing":     {"```\n#1\n`````\n#1", "```\n#1\n`````\n#101"},
		"unclosed fence":     {"#1\n```\n#1\n#12", "#101\n```\n#1\n#12"},
		"url fragment":       {"https://x.test/page#1 and a/#3", "https://x.test/page#1 and a/#3"},
		"word glued":         {"foo#1 and abc_#1", "foo#1 and abc_#1"},
		"html entity":        {"&#12; is a char", "&#12; is a char"},
		"followed by letter": {"#1a and #1_ and #1x", "#1a and #1_ and #1x"},
		"heading":            {"# 1 title\n## #1", "# 1 title\n## #101"},
		"empty":              {"", ""},
		"crlf":               {"a #1\r\nb #12\r\n", "a #101\r\nb #112\r\n"},
		"unicode":            {"héllo #1 wörld", "héllo #101 wörld"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := rewriteLocalRefs(tc.in, m); got != tc.want {
				t.Errorf("rewriteLocalRefs(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestMigrateLocalNamesWhyATrackerCannotBeMigrated(t *testing.T) {
	_, app, opts := migrateLocalSetup(filepath.Join(t.TempDir(), local.DirName))
	if err := app.MigrateLocal(ctx, opts); err == nil || !strings.Contains(err.Error(), "hew init --local") {
		t.Errorf("no tracker: err = %v, want the remedy", err)
	}

	if err := app.MigrateLocal(ctx, MigrateLocalOpts{}); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Errorf("no dir: err = %v, want a usage error", err)
	}

	_, dir := localTracker(t)
	mustLocal(t, local.WriteConfig(dir, local.Config{Backend: local.BackendGitHub}))
	_, app2, opts2 := migrateLocalSetup(dir)
	if err := app2.MigrateLocal(ctx, opts2); err == nil || !strings.Contains(err.Error(), `tracks issues in "github"`) {
		t.Errorf("other backend: err = %v, want the backend named", err)
	}

	s3, dir3 := localTracker(t)
	mustCreateLocal(t, s3, "A", "", "P2", "task")
	_, app3, opts3 := migrateLocalSetup(dir3)
	mustLocal(t, app3.MigrateLocal(ctx, opts3))
	err := app3.MigrateLocal(ctx, opts3)
	if err == nil || !strings.Contains(err.Error(), "already migrated to o/r") {
		t.Errorf("migrated tracker: err = %v, want where it went", err)
	}
}

func TestMigrateLocalDryRunShowsWhatAResumeWouldSkip(t *testing.T) {
	s, dir := localTracker(t)
	for i := 1; i <= 2; i++ {
		mustCreateLocal(t, s, fmt.Sprintf("Issue %d", i), "", "P2", "task")
	}
	f, app, opts := migrateLocalSetup(dir)
	f.failAfter["CreateIssue"] = failPoint{calls: 1, err: errors.New("down")}
	if err := app.MigrateLocal(ctx, opts); err == nil {
		t.Fatal("injected failure not reported")
	}

	delete(f.failAfter, "CreateIssue")
	app.Out.(interface{ Reset() }).Reset()
	opts.DryRun = true
	if err := app.MigrateLocal(ctx, opts); err != nil {
		t.Fatal(err)
	}
	out := app.Out.(interface{ String() string }).String()
	if !strings.Contains(out, "already migrated: 1 → #101") || !strings.Contains(out, "create: 2 as P2 task") {
		t.Errorf("plan = %q, want the finished issue skipped and the other planned", out)
	}
	if cfg, _ := local.ReadConfig(dir); cfg.Backend != local.BackendLocal {
		t.Errorf("dry run flipped the backend to %q", cfg.Backend)
	}
}

func TestLocalCommentCreditsAnAuthorSafely(t *testing.T) {
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	for name, tc := range map[string]struct{ author, want string }{
		"login":      {"dev", "`dev` commented on 2026-03-04:\n\nhello"},
		"backticks":  {"a`b", "`a'b` commented on 2026-03-04:\n\nhello"},
		"no author":  {"", "`unknown` commented on 2026-03-04:\n\nhello"},
		"whitespace": {"  dev ", "`dev` commented on 2026-03-04:\n\nhello"},
	} {
		if got := localComment(local.Comment{Author: tc.author, CreatedAt: at, Body: "hello"}); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}

func TestLocalLabelsSkipsConventionLabelsAndRepeats(t *testing.T) {
	snap := &local.Snapshot{
		Labels: []gh.Label{{Name: "Area-X", Color: "111111"}, {Name: "P1", Color: "222222"}},
		Issues: []local.Issue{
			{Labels: []string{"area-x", "extra", "EXTRA", "P1", "bug"}},
			{Labels: []string{"extra"}},
		},
	}
	got := localLabels(snap)
	var names []string
	for _, l := range got {
		names = append(names, l.Name)
	}
	if !slices.Equal(names, []string{"Area-X", "extra"}) {
		t.Fatalf("labels = %v, want each custom label once, convention labels left to the bootstrap", names)
	}
	if got[0].Color != "111111" || got[1].Color != "ededed" {
		t.Errorf("colors = %q, %q, want the local definition and a neutral default", got[0].Color, got[1].Color)
	}
}

func TestLocalItemsDropTheClaimFromClosedIssues(t *testing.T) {
	snap := &local.Snapshot{Issues: []local.Issue{
		{Number: 1, State: "CLOSED", StateReason: "REOPENED", Labels: []string{"in-progress", "P2"}, Assignees: []string{"dev-wt"}},
		{Number: 2, State: "OPEN", Labels: []string{"in-progress"}, Assignees: []string{"dev-wt", "other"}},
	}}
	items := localItems(snap)
	if slices.Contains(items[0].Labels, "in-progress") || len(items[0].Assignees) != 0 {
		t.Errorf("closed item = %+v, want no claim", items[0])
	}
	if items[0].CloseReason != "" {
		t.Errorf("close reason = %q, want the engine's default for an unrecognised reason", items[0].CloseReason)
	}
	if !slices.Equal(items[1].Assignees, []string{assigneeViewer}) {
		t.Errorf("open item assignees = %v, want one claim for the migrating user", items[1].Assignees)
	}
}
