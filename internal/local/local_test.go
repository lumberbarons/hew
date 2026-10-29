package local

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

func TestOpenGivesATrackerWithoutAnIDOne(t *testing.T) {
	dir := filepath.Join(t.TempDir(), DirName)
	must(t, Init(dir))
	cfg, err := ReadConfig(dir)
	must(t, err)
	cfg.ID = ""
	must(t, WriteConfig(dir, cfg))

	first, err := Open(dir, "me")
	must(t, err)
	if first.ID() == "" {
		t.Fatal("a tracker written before ids stayed without one")
	}
	second, err := Open(dir, "me")
	must(t, err)
	if second.ID() != first.ID() {
		t.Errorf("a second open saw id %q, want the persisted %q", second.ID(), first.ID())
	}
	stored, err := ReadConfig(dir)
	must(t, err)
	if stored.ID != first.ID() {
		t.Errorf("config id = %q, want %q", stored.ID, first.ID())
	}
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
	if cfg.ID == "" {
		t.Error("config has no id: a checkpoint cannot tell this tracker from another checkout's")
	}

	s, err := Open(dir, "me")
	must(t, err)
	mustCreate(t, s, "keep me", "")
	must(t, Init(dir))
	again, err := ReadConfig(dir)
	must(t, err)
	if again.ID != cfg.ID {
		t.Errorf("a second Init changed the id from %q to %q", cfg.ID, again.ID)
	}
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
		"not json":     "nope",
		"newer format": `{"backend":"local","version":99}`,
	} {
		must(t, os.WriteFile(path, []byte(content), 0o644))
		if _, err := ReadConfig(dir); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestConfigToleratesFieldsFromANewerHew(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, configFile)
	must(t, os.WriteFile(path, []byte(`{"backend":"local","version":1,"id":"abc","surprise":{"n":[1,2]}}`), 0o644))

	cfg, err := ReadConfig(dir)
	if err != nil {
		t.Fatalf("a config with an extra field at the current version: %v", err)
	}
	if cfg.Backend != BackendLocal || cfg.ID != "abc" {
		t.Errorf("config = %+v, want the known fields read as written", cfg)
	}

	cfg.Backend = BackendGitHub
	must(t, WriteConfig(dir, cfg))
	var got map[string]json.RawMessage
	must(t, json.Unmarshal([]byte(readFile(t, path)), &got))
	if string(got["surprise"]) != `{"n":[1,2]}` {
		t.Errorf("unknown field after WriteConfig = %s, want it carried through verbatim", got["surprise"])
	}
	if string(got["backend"]) != `"github"` {
		t.Errorf("backend = %s, want the edit applied beside the extra field", got["backend"])
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
	at := func(root string) func() (string, error) {
		return func() (string, error) { return root, nil }
	}
	for _, tc := range []struct {
		name     string
		env      map[string]string
		worktree string
		want     string
	}{
		{"main checkout", map[string]string{"USER": "jim"}, "", "jim"},
		{"linked worktree", map[string]string{"USER": "jim"}, "/w/agent-two", "jim-agent-two-" + shortDigest("/w/agent-two")},
		{"no login", nil, "", "local"},
		{"no login in a worktree", nil, "/w/agent-two", "local-agent-two-" + shortDigest("/w/agent-two")},
		{"explicit identity wins", map[string]string{"HEW_USER": "agent", "USER": "jim"}, "/w/agent-two", "agent"},
		{"separators cannot split a handle", map[string]string{"USER": "jim"}, "/w/feature/one two", "jim-one-two-" + shortDigest("/w/feature/one two")},
		{"an unusable name still tells worktrees apart", map[string]string{"USER": "jim"}, "/w////", "jim-w-" + shortDigest("/w////")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := viewerFor(env(tc.env), at(tc.worktree))
			if err != nil || got != tc.want {
				t.Errorf("= %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

// Distinct worktrees must be distinct claimants even when their directory
// names match or only differ in characters the handle drops.
func TestViewerForSeparatesWorktreesThatShareAName(t *testing.T) {
	login := func(k string) string {
		if k == "USER" {
			return "jim"
		}
		return ""
	}
	seen := map[string]string{}
	for _, root := range []string{"/a/work", "/b/work", "/w/feature/one", "/w/feature-one", "/w/x"} {
		got, err := viewerFor(login, func() (string, error) { return root, nil })
		must(t, err)
		if prev, dup := seen[got]; dup {
			t.Errorf("%s and %s share the identity %q", prev, root, got)
		}
		seen[got] = root
	}
	// A main-checkout user whose login looks like a qualified one stays apart.
	other, err := viewerFor(func(k string) string {
		if k == "USER" {
			return "jim-x"
		}
		return ""
	}, func() (string, error) { return "", nil })
	must(t, err)
	if prev, dup := seen[other]; dup {
		t.Errorf("login jim-x collides with the worktree %s as %q", prev, other)
	}
}

// A git failure must not quietly demote the identity to the bare login: the
// same agent would be a different claimant on its next invocation.
func TestViewerForRefusesToGuessWhenTheCheckoutIsUnknown(t *testing.T) {
	getenv := func(k string) string {
		if k == "USER" {
			return "jim"
		}
		return ""
	}
	_, err := viewerFor(getenv, func() (string, error) { return "", errors.New("git: not found") })
	if err == nil || !strings.Contains(err.Error(), "HEW_USER") || !strings.Contains(err.Error(), "git: not found") {
		t.Errorf("err = %v, want it to name HEW_USER and wrap the git failure", err)
	}
	// An explicit identity needs no checkout at all.
	got, err := viewerFor(func(k string) string {
		if k == "HEW_USER" {
			return "agent"
		}
		return ""
	}, func() (string, error) { return "", errors.New("must not be asked") })
	if err != nil || got != "agent" {
		t.Errorf("= %q, %v; want HEW_USER without consulting git", got, err)
	}
}

// shortDigest is the path tag the identity carries, spelled out independently
// of worktreeTag so a change to the scheme is a deliberate test change.
func shortDigest(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:3])
}

func TestDefaultViewerLetsAnAgentOverrideItsIdentity(t *testing.T) {
	t.Setenv("HEW_USER", "agent")
	if got, err := DefaultViewer(); err != nil || got != "agent" {
		t.Errorf("= %q, %v; want HEW_USER first", got, err)
	}
}

// Two agents under one OS account must not share a viewer: exit 5 means "this
// claim is yours", which is only true inside one worktree. The main checkout
// keeps the bare login; a linked worktree qualifies it with its name.
func TestDefaultViewerTellsWorktreesApart(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	// A developer's global config (signing, hooks, identity) must not reach
	// the throwaway repo.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	main, err := filepath.EvalSymlinks(t.TempDir())
	must(t, err)
	gitIn(t, main, "init", "--initial-branch=main")
	gitIn(t, main, "-c", "user.email=t@example.com", "-c", "user.name=t", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "seed")
	worktree := filepath.Join(t.TempDir(), "agent-two")
	gitIn(t, main, "worktree", "add", "-b", "feat/two", worktree)

	t.Setenv("HEW_USER", "")
	t.Setenv("USER", "jim")
	t.Chdir(main)
	if got, err := DefaultViewer(); err != nil || got != "jim" {
		t.Errorf("main checkout viewer = %q, %v; want the bare login", got, err)
	}
	t.Chdir(worktree)
	// git reports the resolved path, which is what the tag digests.
	real, err := filepath.EvalSymlinks(worktree)
	must(t, err)
	want := "jim-agent-two-" + shortDigest(real)
	if got, err := DefaultViewer(); err != nil || got != want {
		t.Errorf("worktree viewer = %q, %v; want %q", got, err, want)
	}
	// Outside any checkout there is nothing to qualify with: an error, not a guess.
	t.Chdir(t.TempDir())
	if got, err := DefaultViewer(); err == nil {
		t.Errorf("outside a checkout viewer = %q, want an error", got)
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

// start's pre-read decides "claimed" with the model's exact-case test, so the
// store's atomic claim must agree: a label differing only in case is not a
// claim, and claiming reuses its spelling rather than adding a second label.
func TestClaimIssueJudgesTheInProgressLabelLikeTheModel(t *testing.T) {
	s := newStore(t)
	n := mustCreate(t, s, "odd label", "", "P2", "bug")
	must(t, s.AddLabels(ctx, n, []string{"In-Progress"}))
	before, err := s.GetIssue(ctx, n)
	must(t, err)
	if before.Claimed() {
		t.Fatalf("labels %v read as claimed; the premise of this test is that the model says no", before.Labels)
	}
	must(t, s.ClaimIssue(ctx, n, "agent", false))
	got, err := s.GetIssue(ctx, n)
	must(t, err)
	inProgress := 0
	for _, l := range got.Labels {
		if strings.EqualFold(l, "in-progress") {
			inProgress++
		}
	}
	if inProgress != 1 || !slices.Equal(got.Assignees, []string{"agent"}) {
		t.Errorf("labels %v assignees %v, want one in-progress label and the claimant", got.Labels, got.Assignees)
	}
}

func TestARefusedChangeLeavesTheFileUntouched(t *testing.T) {
	s := newStore(t)
	a := mustCreate(t, s, "a", "")
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
		"repeated number":       meta + issue(1) + issue(1),
		"non-positive":          meta + issue(0),
		"truncated json":        meta + `{"number":1,"ti`,
		"bad label record":      meta + `{"_type":"label","name":5}` + "\n",
		"bad meta record":       `{"_type":"meta","version":"one"}` + "\n",
		"head is not an object": meta + "[1]\n",
	} {
		path := filepath.Join(t.TempDir(), issuesFile)
		must(t, os.WriteFile(path, []byte(content), 0o644))
		if _, err := readData(path); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestReadingRejectsAnUnknownStateNamingIt(t *testing.T) {
	meta := `{"_type":"meta","version":1}` + "\n"
	for _, state := range []string{"open", "closed", "DONE", ""} {
		path := filepath.Join(t.TempDir(), issuesFile)
		rec := fmt.Sprintf(`{"number":1,"title":"t","state":%q,"created_at":"2026-07-01T00:00:00Z"}`+"\n", state)
		must(t, os.WriteFile(path, []byte(meta+rec), 0o644))
		_, err := readData(path)
		if err == nil || !strings.Contains(err.Error(), "unknown state "+fmt.Sprintf("%q", state)) {
			t.Errorf("state %q: err = %v, want an unknown-state error naming it", state, err)
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

func TestLocateFindsOnlyAConfiguredLocalTracker(t *testing.T) {
	main := func(dir string) (string, error) { return dir, nil }
	notGit := func(string) (string, error) {
		return "", fmt.Errorf("cannot find the main checkout: %w",
			&exec.ExitError{Stderr: []byte("fatal: not a git repository (or any of the parent directories): .git")})
	}
	gitBroken := func(string) (string, error) {
		return "", fmt.Errorf("cannot find the main checkout: %w",
			&exec.ExitError{Stderr: []byte("fatal: detected dubious ownership in repository")})
	}

	t.Run("no .hew directory is not local", func(t *testing.T) {
		got, ok, err := locate(main, t.TempDir())
		if err != nil || ok || got != "" {
			t.Errorf("locate = %q, %v, %v; want not local", got, ok, err)
		}
	})
	t.Run("a .hew directory without a config is not local", func(t *testing.T) {
		root := t.TempDir()
		must(t, os.Mkdir(filepath.Join(root, DirName), 0o755))
		if _, ok, err := locate(main, root); err != nil || ok {
			t.Errorf("locate ok = %v, err = %v; want not local", ok, err)
		}
	})
	t.Run("a local config selects the tracker", func(t *testing.T) {
		root := t.TempDir()
		must(t, Init(filepath.Join(root, DirName)))
		got, ok, err := locate(main, root)
		if err != nil || !ok || got != filepath.Join(root, DirName) {
			t.Errorf("locate = %q, %v, %v; want the tracker", got, ok, err)
		}
	})
	t.Run("a migrated config is not local", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, DirName)
		must(t, Init(dir))
		must(t, WriteConfig(dir, Config{Backend: BackendGitHub}))
		if got, ok, err := locate(main, root); err != nil || ok || got != "" {
			t.Errorf("locate = %q, %v, %v; want not local", got, ok, err)
		}
	})
	t.Run("an unreadable config is an error, not a silent GitHub fallback", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, DirName)
		must(t, Init(dir))
		must(t, os.WriteFile(filepath.Join(dir, configFile), []byte("{not json"), 0o644))
		if _, ok, err := locate(main, root); err == nil || ok {
			t.Errorf("locate ok = %v, err = %v; want an error", ok, err)
		}
	})
	t.Run("outside a checkout is not local", func(t *testing.T) {
		if _, ok, err := locate(notGit, t.TempDir()); err != nil || ok {
			t.Errorf("locate ok = %v, err = %v; want not local", ok, err)
		}
	})
	t.Run("git failing for another reason is an error, not a GitHub fallback", func(t *testing.T) {
		_, ok, err := locate(gitBroken, t.TempDir())
		var exit *exec.ExitError
		if ok || !errors.As(err, &exit) || !strings.Contains(string(exit.Stderr), "dubious ownership") {
			t.Errorf("locate ok = %v, err = %v; want the git failure surfaced", ok, err)
		}
	})
	t.Run("a .hew that is a file belongs to something else", func(t *testing.T) {
		root := t.TempDir()
		must(t, os.WriteFile(filepath.Join(root, DirName), []byte("not a tracker"), 0o644))
		if got, ok, err := locate(main, root); err != nil || ok || got != "" {
			t.Errorf("locate = %q, %v, %v; want not local", got, ok, err)
		}
	})
}

// Backend selection runs before every command, so a checkout that never opted
// in must be answered from the filesystem. These are the layouts the shortcut
// is allowed to read; anything else has to fall through to git rather than be
// guessed, because a wrong answer hides a real tracker.
func TestMainCheckoutWithoutGit(t *testing.T) {
	mainCheckout := func(t *testing.T) string {
		t.Helper()
		root, err := filepath.EvalSymlinks(t.TempDir())
		must(t, err)
		gitDir := filepath.Join(root, ".git")
		must(t, os.Mkdir(gitDir, 0o755))
		must(t, os.WriteFile(filepath.Join(gitDir, "config"), []byte("[core]\n\tbare = false\n"), 0o644))
		return root
	}

	t.Run("the main checkout, from a subdirectory", func(t *testing.T) {
		root := mainCheckout(t)
		sub := filepath.Join(root, "cmd", "hew")
		must(t, os.MkdirAll(sub, 0o755))
		got, ok := mainCheckoutWithoutGit(sub)
		if !ok || got != root {
			t.Errorf("main = %q, %v; want %q", got, ok, root)
		}
	})

	t.Run("a linked worktree", func(t *testing.T) {
		main := mainCheckout(t)
		gitdir := filepath.Join(main, ".git", "worktrees", "wt")
		must(t, os.MkdirAll(gitdir, 0o755))
		wt := filepath.Join(t.TempDir(), "wt")
		must(t, os.MkdirAll(wt, 0o755))
		must(t, os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o644))
		got, ok := mainCheckoutWithoutGit(wt)
		if !ok || got != main {
			t.Errorf("main = %q, %v; want the main checkout %q", got, ok, main)
		}
	})

	t.Run("a relative worktree gitdir", func(t *testing.T) {
		main := mainCheckout(t)
		gitdir := filepath.Join(main, ".git", "worktrees", "wt")
		must(t, os.MkdirAll(gitdir, 0o755))
		wt := filepath.Join(main, "wt")
		must(t, os.MkdirAll(wt, 0o755))
		must(t, os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: ../.git/worktrees/wt\n"), 0o644))
		got, ok := mainCheckoutWithoutGit(wt)
		if !ok || got != main {
			t.Errorf("main = %q, %v; want %q", got, ok, main)
		}
	})

	t.Run("something that is not a checkout", func(t *testing.T) {
		if _, ok := mainCheckoutWithoutGit(t.TempDir()); ok {
			t.Error("a directory with no .git was read as a checkout")
		}
	})

	t.Run("a submodule gitdir is left to git", func(t *testing.T) {
		super := mainCheckout(t)
		gitdir := filepath.Join(super, ".git", "modules", "sub")
		must(t, os.MkdirAll(gitdir, 0o755))
		sub := filepath.Join(super, "sub")
		must(t, os.MkdirAll(sub, 0o755))
		must(t, os.WriteFile(filepath.Join(sub, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o644))
		if got, ok := mainCheckoutWithoutGit(sub); ok {
			t.Errorf("main = %q; want git to decide the submodule", got)
		}
	})

	t.Run("a symlinked directory is read by its physical path", func(t *testing.T) {
		// The lexical parent of the link holds its own checkout; git, and so
		// this, must look at the real one.
		decoy := mainCheckout(t)
		real := mainCheckout(t)
		sub := filepath.Join(real, "sub")
		must(t, os.MkdirAll(sub, 0o755))
		link := filepath.Join(decoy, "link")
		must(t, os.Symlink(sub, link))
		got, ok := mainCheckoutWithoutGit(link)
		if !ok || got != real {
			t.Errorf("main = %q, %v; want %q, not the link's parent %q", got, ok, real, decoy)
		}
	})

	t.Run("a bare repository named .git is left to git", func(t *testing.T) {
		bare := mainCheckout(t)
		must(t, os.WriteFile(filepath.Join(bare, ".git", "config"), []byte("[core]\n\tbare = true\n"), 0o644))
		gitdir := filepath.Join(bare, ".git", "worktrees", "wt")
		must(t, os.MkdirAll(gitdir, 0o755))
		wt := filepath.Join(t.TempDir(), "wt")
		must(t, os.MkdirAll(wt, 0o755))
		must(t, os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o644))
		if got, ok := mainCheckoutWithoutGit(wt); ok {
			t.Errorf("main = %q; want git to find the bare repository's directory", got)
		}
	})

	t.Run("a core.worktree override is left to git", func(t *testing.T) {
		root := mainCheckout(t)
		must(t, os.WriteFile(filepath.Join(root, ".git", "config"), []byte("[core]\n\tbare = false\n\tworktree = /elsewhere\n"), 0o644))
		if got, ok := mainCheckoutWithoutGit(root); ok {
			t.Errorf("main = %q; want git to honour core.worktree", got)
		}
	})

	t.Run("a gitdir override is left to git", func(t *testing.T) {
		root := mainCheckout(t)
		t.Setenv("GIT_DIR", filepath.Join(root, ".git"))
		if got, ok := mainCheckoutWithoutGit(root); ok {
			t.Errorf("main = %q; want git to honour GIT_DIR", got)
		}
	})
}

func TestMainDirSurfacesGitFailure(t *testing.T) {
	_, err := mainDir(func(string) (string, error) { return "", errors.New("boom") }, "")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v, want the git failure", err)
	}
}

const (
	testMeta  = `{"_type":"meta","version":1}` + "\n"
	testIssue = `{"number":1,"title":"t","state":"OPEN","created_at":"2026-07-01T00:00:00Z"}` + "\n"
)

// A newer hew at the same format version may add fields; an older build in
// another worktree must read them and, because every mutation rewrites the
// whole file, hand them back untouched.
func TestUnknownFieldsLoadAndSurviveARewrite(t *testing.T) {
	s := newStore(t)
	must(t, os.WriteFile(s.path(), []byte(
		`{"_type":"meta","version":1,"writer":"hew-9"}`+"\n"+
			`{"_type":"label","name":"P2","color":"fbca04","icon":"flame"}`+"\n"+
			`{"number":1,"title":"t","state":"OPEN","created_at":"2026-07-01T00:00:00Z","estimate":{"points":3},"watchers":["a","b"]}`+"\n"), 0o644))

	must(t, s.EditTitle(ctx, 1, "renamed"))

	got := readFile(t, s.path())
	for _, want := range []string{`"writer":"hew-9"`, `"icon":"flame"`, `"estimate":{"points":3}`, `"watchers":["a","b"]`, `"title":"renamed"`} {
		if !strings.Contains(got, want) {
			t.Errorf("rewritten file lost %s:\n%s", want, got)
		}
	}
	// The rewrite is a fixed point: nothing is duplicated on the next one.
	must(t, s.EditTitle(ctx, 1, "renamed"))
	if again := readFile(t, s.path()); again != got {
		t.Errorf("second rewrite changed the file:\n%s\nvs\n%s", got, again)
	}
}

func TestUnknownFieldsDoNotShadowKnownOnes(t *testing.T) {
	s := newStore(t)
	must(t, os.WriteFile(s.path(), []byte(testMeta+
		`{"number":1,"title":"t","state":"OPEN","created_at":"2026-07-01T00:00:00Z","parent":0,"future":1}`+"\n"), 0o644))
	must(t, s.EditTitle(ctx, 1, "x"))
	got := readFile(t, s.path())
	if strings.Count(got, `"title"`) != 1 || strings.Count(got, `"future":1`) != 1 {
		t.Errorf("fields duplicated or lost:\n%s", got)
	}
}

func TestDecodeErrorsNameTheLineAndHowToRecover(t *testing.T) {
	for name, tc := range map[string]struct {
		content string
		line    string
	}{
		"truncated last line": {testMeta + testIssue + `{"number":2,"ti`, "line 3"},
		"blank lines count":   {testMeta + "\n\n" + `{"number":1,"title":`, "line 4"},
		"repeated number":     {testMeta + testIssue + testIssue, "line 3"},
		"not an object":       {testMeta + "[1]\n", "line 2"},
	} {
		path := filepath.Join(t.TempDir(), issuesFile)
		must(t, os.WriteFile(path, []byte(tc.content), 0o644))
		_, err := readData(path)
		if err == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		for _, want := range []string{path, tc.line, "restore"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: err = %v, want it to mention %q", name, err, want)
			}
		}
	}
}

// A file from a newer hew is not damaged, so telling the user to repair it
// would send them to corrupt a good file.
func TestNewerFormatIsNotOfferedARepair(t *testing.T) {
	path := filepath.Join(t.TempDir(), issuesFile)
	must(t, os.WriteFile(path, []byte(`{"_type":"meta","version":99}`+"\n"), 0o644))
	_, err := readData(path)
	if err == nil || !strings.Contains(err.Error(), "newer hew") || !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("err = %v, want a newer-hew error naming line 1", err)
	}
	if strings.Contains(err.Error(), "restore") {
		t.Errorf("err = %v, offers a repair for a file that is not damaged", err)
	}
}

func TestLongLinesAreNotTruncated(t *testing.T) {
	s := newStore(t)
	body := strings.Repeat("x", 1<<20)
	n := mustCreate(t, s, "big", body)
	got, err := s.GetIssue(ctx, n)
	must(t, err)
	if got.Body != body {
		t.Errorf("body length = %d, want %d", len(got.Body), len(body))
	}
}

func TestOpenRemovesStaleTempFiles(t *testing.T) {
	s := newStore(t)
	stale := []string{
		filepath.Join(s.dir, issuesFile+".tmp-123456"),
		filepath.Join(s.dir, issuesFile+".tmp-abc"),
	}
	keep := filepath.Join(s.dir, "notes.tmp-1")
	for _, p := range append(stale[:len(stale):len(stale)], keep) {
		must(t, os.WriteFile(p, []byte("partial"), 0o644))
		old := time.Now().Add(-time.Hour)
		must(t, os.Chtimes(p, old, old))
	}

	if _, err := Open(s.dir, "me"); err != nil {
		t.Fatal(err)
	}

	for _, p := range stale {
		if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s survived Open (stat err = %v)", p, err)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("an unrelated file was removed: %v", err)
	}
	if _, err := os.Stat(s.path()); err != nil {
		t.Errorf("issues file gone: %v", err)
	}
}

// While a writer holds the lock its temp file is live, not stale.
func TestOpenLeavesTempFilesWhileAWriterHoldsTheLock(t *testing.T) {
	s := newStore(t)
	tmp := filepath.Join(s.dir, issuesFile+".tmp-live")
	must(t, os.WriteFile(tmp, []byte("in flight"), 0o644))
	old := time.Now().Add(-time.Hour)
	must(t, os.Chtimes(tmp, old, old))
	unlock, err := s.lock(ctx)
	must(t, err)
	defer unlock()

	if _, err := Open(s.dir, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tmp); err != nil {
		t.Errorf("a writer's temp file was removed: %v", err)
	}
}

func TestLockGivesUpWhenTheContextIsCancelled(t *testing.T) {
	s := newStore(t)
	unlock, err := s.lock(ctx)
	must(t, err)
	defer unlock()

	cctx, cancel := context.WithCancel(ctx)
	time.AfterFunc(20*time.Millisecond, cancel)
	err = s.EditTitle(cctx, 1, "x")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if !strings.Contains(err.Error(), filepath.Join(s.dir, lockFile)) {
		t.Errorf("err = %v, want it to name the lock file", err)
	}
}

func TestLockTimesOutNamingTheLockFile(t *testing.T) {
	s := newStore(t)
	unlock, err := s.lock(ctx)
	must(t, err)

	s.lockTimeout = 30 * time.Millisecond
	start := time.Now()
	_, err = s.CreateIssue(ctx, "x", "", nil)
	if err == nil || !strings.Contains(err.Error(), filepath.Join(s.dir, lockFile)) || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a timeout naming the lock file", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("gave up after %v", took)
	}

	// Nothing was written, and the store works again once the holder lets go.
	unlock()
	s.lockTimeout = 0
	if _, err := s.CreateIssue(ctx, "x", "", nil); err != nil {
		t.Errorf("after release: %v", err)
	}
}

func TestLockRefusesACancelledContextEvenWhenFree(t *testing.T) {
	s := newStore(t)
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.EditTitle(cctx, 1, "x"); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// Init writes issues.jsonl without the lock, so a young temp file may belong to
// a writer the lock cannot see.
func TestOpenLeavesYoungTempFilesEvenWhenTheLockIsFree(t *testing.T) {
	s := newStore(t)
	tmp := filepath.Join(s.dir, issuesFile+".tmp-young")
	must(t, os.WriteFile(tmp, []byte("being written"), 0o644))
	if _, err := Open(s.dir, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tmp); err != nil {
		t.Errorf("a just-created temp file was removed: %v", err)
	}
}

func TestUnknownFieldsInCommentsSurviveARewrite(t *testing.T) {
	s := newStore(t)
	must(t, os.WriteFile(s.path(), []byte(testMeta+
		`{"number":1,"title":"t","state":"OPEN","created_at":"2026-07-01T00:00:00Z","comments":[{"author":"a","created_at":"2026-07-01T00:00:00Z","body":"hi <!-- x -->","reaction":"+1"}]}`+"\n"), 0o644))
	must(t, s.EditTitle(ctx, 1, "renamed"))
	got := readFile(t, s.path())
	for _, want := range []string{`"reaction":"+1"`, `"body":"hi <!-- x -->"`} {
		if !strings.Contains(got, want) {
			t.Errorf("rewritten file lost %s:\n%s", want, got)
		}
	}
}

// A record type this build has never heard of most likely comes from a newer
// hew; telling the user to remove its line would destroy good data.
func TestUnknownRecordTypeIsNotOfferedARepair(t *testing.T) {
	path := filepath.Join(t.TempDir(), issuesFile)
	must(t, os.WriteFile(path, []byte(testMeta+`{"_type":"milestone","name":"v2"}`+"\n"), 0o644))
	_, err := readData(path)
	if err == nil || !strings.Contains(err.Error(), `"milestone"`) || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("err = %v, want an unknown-type error naming the type and line 2", err)
	}
	if strings.Contains(err.Error(), "restore") || strings.Contains(err.Error(), "remove") {
		t.Errorf("err = %v, offers to remove a record that is probably fine", err)
	}
}

func TestDuplicateMetaAndLabelRecordsKeepEveryUnknownField(t *testing.T) {
	s := newStore(t)
	must(t, os.WriteFile(s.path(), []byte(
		`{"_type":"meta","version":1,"first":1}`+"\n"+
			`{"_type":"meta","version":1,"second":2}`+"\n"+
			`{"_type":"label","name":"x","a":1}`+"\n"+
			`{"_type":"label","name":"x","b":2}`+"\n"), 0o644))
	d, err := s.load()
	must(t, err)
	out, err := d.encode()
	must(t, err)
	for _, want := range []string{`"first":1`, `"second":2`, `"a":1`, `"b":2`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("encode lost %s:\n%s", want, out)
		}
	}
}

func TestMarshalWithExtrasHandlesAnEmptyBase(t *testing.T) {
	type empty struct {
		A int `json:"a,omitempty"`
	}
	got, err := marshalWithExtras(empty{}, extras{"k": json.RawMessage(`1`)})
	must(t, err)
	if string(got) != `{"k":1}` {
		t.Errorf("got %s, want {\"k\":1}", got)
	}
}

// Freeze keeps the lock for a whole migration, so a writer that times out
// behind it must not be told the holder is hung.
func TestLockTimeoutDoesNotAccuseTheHolderOfHanging(t *testing.T) {
	s := newStore(t)
	fr, err := Freeze(ctx, s.dir)
	must(t, err)
	defer fr.Release()

	s.lockTimeout = 20 * time.Millisecond
	err = s.EditTitle(ctx, 1, "x")
	if err == nil || !strings.Contains(err.Error(), "migrate") {
		t.Fatalf("err = %v, want a timeout that names a running migration as a possible holder", err)
	}
	if strings.Contains(err.Error(), "hung") || strings.Contains(err.Error(), "stop it") {
		t.Errorf("err = %v, tells the user to kill a holder that may be healthy", err)
	}
}
