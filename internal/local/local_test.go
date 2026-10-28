package local

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lumberbarons/hew/internal/conformance"
	"github.com/lumberbarons/hew/internal/gh"
)

var ctx = context.Background()

// newStore initialises a tracker in a fresh temp directory with a fixed clock.
func newStore(t *testing.T) *Store {
	t.Helper()
	dir := filepath.Join(t.TempDir(), DirName)
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir, "me")
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC) }
	return s
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func mustCreate(t *testing.T, s *Store, title, body string, labels ...string) int {
	t.Helper()
	i, err := s.CreateIssue(ctx, title, body, labels)
	must(t, err)
	return i.Number
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	must(t, err)
	return string(raw)
}

// Run here as well as from the conformance package so this package's own
// coverage reflects the behavior the shared suite pins down.
func TestStoreConforms(t *testing.T) {
	conformance.Run(t, func(t *testing.T) gh.IssueStore { return newStore(t) })
}

func TestInitCreatesASelfIgnoringTrackerAndIsIdempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), DirName)
	must(t, Init(dir))

	if got := readFile(t, filepath.Join(dir, ".gitignore")); got != "*\n" {
		t.Errorf(".gitignore = %q, want the directory to ignore itself", got)
	}
	cfg, err := ReadConfig(dir)
	must(t, err)
	if cfg.Backend != BackendLocal || cfg.Version != formatVersion {
		t.Errorf("config = %+v", cfg)
	}

	s, err := Open(dir, "me")
	must(t, err)
	mustCreate(t, s, "keep me", "")
	must(t, Init(dir))
	issues, err := s.ListIssues(ctx, []gh.IssueState{gh.StateOpen})
	must(t, err)
	if len(issues) != 1 {
		t.Errorf("a second Init left %d issues, want the 1 it must not touch", len(issues))
	}
}

func TestOpenRefusesWhatIsNotALocalTracker(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "nothing"), "me"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing tracker: err = %v, want fs.ErrNotExist", err)
	}

	dir := filepath.Join(t.TempDir(), DirName)
	must(t, Init(dir))
	must(t, WriteConfig(dir, Config{Backend: BackendGitHub}))
	if _, err := Open(dir, "me"); err == nil || !strings.Contains(err.Error(), "not locally") {
		t.Errorf("migrated tracker: err = %v, want a refusal to write to the archive", err)
	}

	dir = filepath.Join(t.TempDir(), DirName)
	must(t, Init(dir))
	must(t, os.Remove(filepath.Join(dir, issuesFile)))
	if _, err := Open(dir, "me"); err == nil {
		t.Error("a tracker with no issues file opened")
	}
}

func TestReadConfigRejectsBadFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, configFile)
	for name, content := range map[string]string{
		"not json":      "nope",
		"unknown field": `{"backend":"local","surprise":1}`,
		"newer format":  `{"backend":"local","version":99}`,
	} {
		must(t, os.WriteFile(path, []byte(content), 0o644))
		if _, err := ReadConfig(dir); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestOpenDefaultsTheViewer(t *testing.T) {
	t.Setenv("HEW_USER", "agent")
	dir := filepath.Join(t.TempDir(), DirName)
	must(t, Init(dir))
	s, err := Open(dir, "")
	must(t, err)
	if got, _ := s.Viewer(ctx); got != "agent" {
		t.Errorf("viewer = %q, want the explicit agent identity", got)
	}
}

func TestViewerForFallsBackToALoginAndQualifiesAWorktree(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}
	for _, tc := range []struct {
		name     string
		env      map[string]string
		worktree string
		want     string
	}{
		{"main checkout", map[string]string{"USER": "jim"}, "", "jim"},
		{"linked worktree", map[string]string{"USER": "jim"}, "agent-two", "jim-agent-two"},
		{"no login", nil, "", "local"},
		{"no login in a worktree", nil, "agent-two", "local-agent-two"},
		{"explicit identity wins", map[string]string{"HEW_USER": "agent", "USER": "jim"}, "agent-two", "agent"},
		{"separators cannot split a handle", map[string]string{"USER": "jim"}, "feature/one two", "jim-feature-one-two"},
		{"an unusable name adds nothing", map[string]string{"USER": "jim"}, "///", "jim"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := viewerFor(env(tc.env), tc.worktree); got != tc.want {
				t.Errorf("= %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDefaultViewerLetsAnAgentOverrideItsIdentity(t *testing.T) {
	t.Setenv("HEW_USER", "agent")
	if got := DefaultViewer(); got != "agent" {
		t.Errorf("= %q, want HEW_USER first", got)
	}
}

// Two agents under one OS account must not share a viewer: exit 5 means "this
// claim is yours", which is only true inside one worktree. The main checkout
// keeps the bare login; a linked worktree qualifies it with its name.
func TestDefaultViewerTellsWorktreesApart(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	main, err := filepath.EvalSymlinks(t.TempDir())
	must(t, err)
	gitIn(t, main, "init", "--initial-branch=main")
	gitIn(t, main, "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "--allow-empty", "-m", "seed")
	worktree := filepath.Join(t.TempDir(), "agent-two")
	gitIn(t, main, "worktree", "add", "-b", "feat/two", worktree)

	t.Setenv("HEW_USER", "")
	t.Setenv("USER", "jim")
	t.Chdir(main)
	if got := DefaultViewer(); got != "jim" {
		t.Errorf("main checkout viewer = %q, want the bare login", got)
	}
	t.Chdir(worktree)
	if got := DefaultViewer(); got != "jim-agent-two" {
		t.Errorf("worktree viewer = %q, want the login qualified by the worktree", got)
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func TestIssuesPersistAcrossOpens(t *testing.T) {
	s := newStore(t)
	n := mustCreate(t, s, "Persisted", "body", "P1")
	must(t, s.Comment(ctx, n, "a note"))

	again, err := Open(s.dir, "someone-else")
	must(t, err)
	got, err := again.GetIssue(ctx, n)
	must(t, err)
	if got.Title != "Persisted" || len(got.Comments) != 1 || got.Comments[0].Author != "me" {
		t.Errorf("reopened issue = %+v", got)
	}
}

// The file is the product: people will read it, grep it and diff it, so what
// it looks like matters as much as what it round-trips to.
func TestFileIsReadableJSONLines(t *testing.T) {
	s := newStore(t)
	mustCreate(t, s, "Escapes <b> & more", "<!-- hew:apply key=a -->", "P2")
	must(t, s.CreateLabel(ctx, gh.Label{Name: "P2", Color: "00ff00"}))

	lines := strings.Split(strings.TrimSpace(readFile(t, s.path())), "\n")
	if len(lines) != 3 {
		t.Fatalf("file has %d lines, want meta, label and one issue:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	if !strings.HasPrefix(lines[0], `{"_type":"meta"`) || !strings.HasPrefix(lines[1], `{"_type":"label"`) {
		t.Errorf("records out of order: %q / %q", lines[0], lines[1])
	}
	if !strings.Contains(lines[2], `"<!-- hew:apply key=a -->"`) || !strings.Contains(lines[2], `Escapes <b> & more`) {
		t.Errorf("issue line escaped its HTML: %s", lines[2])
	}
}

func TestNumbersAreNeverReused(t *testing.T) {
	s := newStore(t)
	a := mustCreate(t, s, "a", "")
	b := mustCreate(t, s, "b", "")
	must(t, s.CloseIssue(ctx, b, gh.CloseCompleted))
	if c := mustCreate(t, s, "c", ""); c != b+1 || a != 1 {
		t.Errorf("numbers = %d, %d, %d; want 1, 2, 3 even with #2 closed", a, b, c)
	}
}

func TestParallelWritersLoseNothing(t *testing.T) {
	s := newStore(t)
	// Separate Store values over one directory stand in for separate agents
	// in separate worktrees: flock is per open file, so they contend for real.
	const writers, each = 8, 6
	var wg sync.WaitGroup
	errs := make(chan error, writers*each)
	for w := range writers {
		other, err := Open(s.dir, fmt.Sprintf("agent-%d", w))
		must(t, err)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				if _, err := other.CreateIssue(ctx, fmt.Sprintf("w%d-%d", w, i), "", nil); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	issues, err := s.ListIssues(ctx, []gh.IssueState{gh.StateOpen})
	must(t, err)
	if len(issues) != writers*each {
		t.Fatalf("%d issues survived, want %d", len(issues), writers*each)
	}
	seen := map[int]bool{}
	for _, i := range issues {
		if seen[i.Number] {
			t.Errorf("number %d was handed out twice", i.Number)
		}
		seen[i.Number] = true
	}
}

// The point of the atomic claim: separate Store values over one directory
// stand in for separate agents in separate worktrees, and a simultaneous start
// must produce exactly one winner rather than two agents both believing they
// claimed the issue.
func TestConcurrentClaimsHaveExactlyOneWinner(t *testing.T) {
	s := newStore(t)
	n := mustCreate(t, s, "contested", "", "P2", "bug")

	const agents = 8
	type result struct {
		viewer string
		err    error
	}
	start := make(chan struct{})
	results := make(chan result, agents)
	for i := range agents {
		viewer := fmt.Sprintf("agent-%d", i)
		agent, err := Open(s.dir, viewer)
		must(t, err)
		go func() {
			<-start
			results <- result{viewer, agent.ClaimIssue(ctx, n, viewer, false)}
		}()
	}
	close(start)

	var winners []string
	for range agents {
		r := <-results
		switch {
		case r.err == nil:
			winners = append(winners, r.viewer)
		case !errors.Is(r.err, gh.ErrClaimConflict):
			t.Errorf("%s: err = %v, want nil or ErrClaimConflict", r.viewer, r.err)
		}
	}
	if len(winners) != 1 {
		t.Fatalf("%d winners, want exactly one: %v", len(winners), winners)
	}
	got, err := s.GetIssue(ctx, n)
	must(t, err)
	if !slices.Equal(got.Assignees, winners) || !got.InProgress() {
		t.Errorf("claim = assignees %v labels %v, want %v and in-progress", got.Assignees, got.Labels, winners)
	}
}

func TestARefusedChangeLeavesTheFileUntouched(t *testing.T) {
	s := newStore(t)
	a, b := mustCreate(t, s, "a", ""), mustCreate(t, s, "b", "")
	before := readFile(t, s.path())

	if err := s.AddBlockedBy(ctx, a, a); err == nil {
		t.Fatal("self-block was accepted")
	}
	if err := s.AddBlockedBy(ctx, a, 99); err == nil {
		t.Fatal("block by a missing issue was accepted")
	}
	if err := s.AddSubIssue(ctx, a, a, false); err == nil {
		t.Fatal("an issue became its own sub-issue")
	}
	_ = b
	if after := readFile(t, s.path()); after != before {
		t.Errorf("file changed across refused mutations:\n%s\nvs\n%s", before, after)
	}
	entries, err := os.ReadDir(s.dir)
	must(t, err)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file %s was left behind", e.Name())
		}
	}
}

func TestSearchRequiresEveryTermAndRanksTitleMatchesFirst(t *testing.T) {
	s := newStore(t)
	inBody := mustCreate(t, s, "Unrelated heading", "the cache is stale", "P1")
	inTitle := mustCreate(t, s, "Stale cache entries", "")
	mustCreate(t, s, "Stale only", "no other word")
	byLabel := mustCreate(t, s, "Labelled", "", "cache-stale")

	got, total, err := s.SearchIssues(ctx, "CACHE  stale")
	must(t, err)
	var numbers []int
	for _, i := range got {
		numbers = append(numbers, i.Number)
	}
	// #4 carries both terms in a label, so it matches too, behind the others.
	if want := []int{inTitle, inBody, byLabel}; !slices.Equal(numbers, want) {
		t.Errorf("results = %v, want %v (title match first, every term required)", numbers, want)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3", total)
	}

	got, _, err = s.SearchIssues(ctx, "cache-stale")
	must(t, err)
	if len(got) != 1 || got[0].Number != byLabel {
		t.Errorf("label search = %+v, want #%d", got, byLabel)
	}
	if none, total, _ := s.SearchIssues(ctx, "   "); len(none) != 0 || total != 0 {
		t.Errorf("blank search returned %d results, total %d", len(none), total)
	}
}

func TestSearchCapsResultsButReportsTheTrueTotal(t *testing.T) {
	s := newStore(t)
	for i := range searchCap + 5 {
		mustCreate(t, s, fmt.Sprintf("widget %d", i), "")
	}
	got, total, err := s.SearchIssues(ctx, "widget")
	must(t, err)
	if len(got) != searchCap || total != searchCap+5 {
		t.Errorf("got %d results, total %d; want %d of %d", len(got), total, searchCap, searchCap+5)
	}
}

func TestGetIssueShowsOnlyTheMostRecentComments(t *testing.T) {
	s := newStore(t)
	n := mustCreate(t, s, "chatty", "")
	for i := range commentsShown + 3 {
		must(t, s.Comment(ctx, n, fmt.Sprintf("c%d", i)))
	}
	got, err := s.GetIssue(ctx, n)
	must(t, err)
	if got.CommentsTotal != commentsShown+3 || len(got.Comments) != commentsShown {
		t.Fatalf("comments = %d shown of %d", len(got.Comments), got.CommentsTotal)
	}
	if got.Comments[0].Body != "c3" || got.Comments[commentsShown-1].Body != "c7" {
		t.Errorf("shown the wrong window: first %q last %q", got.Comments[0].Body, got.Comments[commentsShown-1].Body)
	}
	list, err := s.ListIssues(ctx, []gh.IssueState{gh.StateOpen})
	must(t, err)
	if len(list[0].Comments) != 0 {
		t.Error("list returned comments; only show should pay for them")
	}
}

func TestParentCarriesItsTitleAndSubIssueOrderFollowsNumber(t *testing.T) {
	s := newStore(t)
	epic := mustCreate(t, s, "The epic", "")
	first, second := mustCreate(t, s, "first", ""), mustCreate(t, s, "second", "")
	must(t, s.AddSubIssue(ctx, epic, second, false))
	must(t, s.AddSubIssue(ctx, epic, first, false))

	got, err := s.GetIssue(ctx, first)
	must(t, err)
	if got.ParentTitle != "The epic" {
		t.Errorf("ParentTitle = %q", got.ParentTitle)
	}
	parent, err := s.GetIssue(ctx, epic)
	must(t, err)
	if len(parent.SubIssues) != 2 || parent.SubIssues[0].Number != first {
		t.Errorf("sub-issues = %+v, want ordered by number", parent.SubIssues)
	}
}

func TestEpicsAreTreesNotGraphs(t *testing.T) {
	s := newStore(t)
	top, mid, leaf := mustCreate(t, s, "top", ""), mustCreate(t, s, "mid", ""), mustCreate(t, s, "leaf", "")
	must(t, s.AddSubIssue(ctx, top, mid, false))
	must(t, s.AddSubIssue(ctx, mid, leaf, false))

	if err := s.AddSubIssue(ctx, leaf, top, false); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Errorf("a grandchild became the parent of its ancestor: %v", err)
	}
	// Re-adding the existing parent is harmless, with or without replace.
	must(t, s.AddSubIssue(ctx, top, mid, false))
}

func TestRemoveSubIssueNeedsTheRealParent(t *testing.T) {
	s := newStore(t)
	epic, other, child := mustCreate(t, s, "epic", ""), mustCreate(t, s, "other", ""), mustCreate(t, s, "child", "")
	must(t, s.AddSubIssue(ctx, epic, child, false))
	if err := s.RemoveSubIssue(ctx, other, child); err == nil {
		t.Error("removed a child from a parent it does not have")
	}
	if err := s.RemoveSubIssue(ctx, 99, child); err == nil {
		t.Error("removed a child from a missing parent")
	}
}

func TestBlockedByIgnoresRepeatsAndMissingBlockersOnRemoval(t *testing.T) {
	s := newStore(t)
	a, b := mustCreate(t, s, "a", ""), mustCreate(t, s, "b", "")
	must(t, s.AddBlockedBy(ctx, a, b))
	must(t, s.AddBlockedBy(ctx, a, b))
	got, err := s.GetIssue(ctx, a)
	must(t, err)
	if len(got.BlockedBy) != 1 {
		t.Errorf("blockers = %+v, want the repeat ignored", got.BlockedBy)
	}
	if err := s.RemoveBlockedBy(ctx, a, 99); err == nil {
		t.Error("removing a missing blocker succeeded")
	}
}

func TestLabelsInUseAreListedAfterTheCreatedOnes(t *testing.T) {
	s := newStore(t)
	mustCreate(t, s, "x", "", "hand-made", "P2")
	must(t, s.CreateLabel(ctx, gh.Label{Name: "P2", Color: "00ff00"}))
	must(t, s.CreateLabel(ctx, gh.Label{Name: "P2", Color: "ignored"}))

	got, err := s.ListLabels(ctx)
	must(t, err)
	want := []gh.Label{{Name: "P2", Color: "00ff00"}, {Name: "hand-made"}}
	if !slices.Equal(got, want) {
		t.Errorf("labels = %+v, want %+v", got, want)
	}
}

func TestReadingRejectsFilesThatAreNotATracker(t *testing.T) {
	meta := `{"_type":"meta","version":1}` + "\n"
	issue := func(n int) string {
		return fmt.Sprintf(`{"number":%d,"title":"t","state":"OPEN","created_at":"2026-07-01T00:00:00Z"}`+"\n", n)
	}
	for name, content := range map[string]string{
		"no meta record":        issue(1),
		"newer format":          `{"_type":"meta","version":99}` + "\n",
		"unknown type":          meta + `{"_type":"mystery"}` + "\n",
		"unknown field":         meta + `{"number":1,"title":"t","state":"OPEN","extra":true}` + "\n",
		"repeated number":       meta + issue(1) + issue(1),
		"non-positive":          meta + issue(0),
		"truncated json":        meta + `{"number":1,"ti`,
		"bad label record":      meta + `{"_type":"label","name":"x","extra":1}` + "\n",
		"bad meta record":       `{"_type":"meta","version":1,"extra":1}` + "\n",
		"head is not an object": meta + "[1]\n",
	} {
		path := filepath.Join(t.TempDir(), issuesFile)
		must(t, os.WriteFile(path, []byte(content), 0o644))
		if _, err := readData(path); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestReadingAcceptsAnExplicitIssueType(t *testing.T) {
	path := filepath.Join(t.TempDir(), issuesFile)
	content := `{"_type":"meta","version":1}` + "\n" +
		`{"_type":"issue","number":4,"title":"t","state":"OPEN","created_at":"2026-07-01T00:00:00Z"}` + "\n"
	must(t, os.WriteFile(path, []byte(content), 0o644))
	d, err := readData(path)
	must(t, err)
	if len(d.issues) != 1 || d.issues[0].Number != 4 {
		t.Errorf("issues = %+v", d.issues)
	}
}

func TestRecordsCanBeInAnyOrderOnDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), issuesFile)
	row := func(n int) string {
		return fmt.Sprintf(`{"number":%d,"title":"t%d","state":"OPEN","created_at":"2026-07-01T00:00:00Z"}`+"\n", n, n)
	}
	must(t, os.WriteFile(path, []byte(`{"_type":"meta","version":1}`+"\n"+row(3)+row(1)+row(2)), 0o644))
	d, err := readData(path)
	must(t, err)
	var got []int
	for _, rec := range d.issues {
		got = append(got, rec.Number)
	}
	if !slices.Equal(got, []int{1, 2, 3}) {
		t.Errorf("order = %v, want sorted by number", got)
	}
}

func TestOperationsReportAMissingIssuesFile(t *testing.T) {
	s := newStore(t)
	must(t, os.Remove(s.path()))
	if _, err := s.ListIssues(ctx, []gh.IssueState{gh.StateOpen}); err == nil {
		t.Error("list succeeded with no file")
	}
	if _, err := s.GetIssue(ctx, 1); err == nil {
		t.Error("get succeeded with no file")
	}
	if _, _, err := s.SearchIssues(ctx, "x"); err == nil {
		t.Error("search succeeded with no file")
	}
	if _, err := s.ListLabels(ctx); err == nil {
		t.Error("list labels succeeded with no file")
	}
	if _, err := s.CreateIssue(ctx, "x", "", nil); err == nil {
		t.Error("create succeeded with no file")
	}
}

func TestLockFailsCleanlyWhenTheDirectoryIsGone(t *testing.T) {
	s := newStore(t)
	must(t, os.RemoveAll(s.dir))
	if err := s.EditTitle(ctx, 1, "x"); err == nil {
		t.Error("a mutation succeeded with the tracker deleted")
	}
}

func TestWritesReportAnUnwritableDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "plain-file")
	must(t, os.WriteFile(file, nil, 0o644))

	if err := Init(filepath.Join(file, DirName)); err == nil {
		t.Error("Init created a tracker underneath a regular file")
	}
	if err := WriteConfig(filepath.Join(t.TempDir(), "missing"), Config{Backend: BackendGitHub}); err == nil {
		t.Error("WriteConfig wrote into a directory that does not exist")
	}
}
