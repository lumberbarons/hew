package main

import (
	"slices"
	"strings"
	"testing"
)

func testFixture() Fixture {
	return Fixture{
		Repo:      "lumberbarons/solar-controller",
		ShowIssue: 123,
		EpicIssue: 137,
		Hew:       "hew",
		QueryDir:  "cmd/tokens/queries",
	}
}

func TestEntriesCoverEveryReadCommand(t *testing.T) {
	var got []string
	for _, e := range testFixture().entries() {
		got = append(got, e.Command)
	}
	want := []string{"ready", "list", "list --json", "prime", "show #123", "epic status 137"}
	if !slices.Equal(got, want) {
		t.Fatalf("commands = %v, want %v", got, want)
	}
}

// The whole-tracker reads all start from one query, and capture keys its
// work by filename — so they have to name the same file or the fixture pays
// for the same API call three times.
func TestTrackerEntriesShareOneBaselineFile(t *testing.T) {
	files := map[string][]string{}
	for _, e := range testFixture().entries() {
		for _, b := range e.Baselines {
			for _, c := range b.Captures {
				files[e.Command] = append(files[e.Command], c.File)
			}
		}
	}
	for _, cmd := range []string{"list", "list --json", "prime"} {
		if !slices.Contains(files[cmd], "gh-graphql-open-issues.json") {
			t.Errorf("%q baselines = %v, want the shared open-issues capture", cmd, files[cmd])
		}
	}
}

func TestEntriesOmitCommandsTheFixtureCannotAnswer(t *testing.T) {
	f := testFixture()
	f.EpicIssue = 0
	f.ShowIssue = 0
	for _, e := range f.entries() {
		if strings.HasPrefix(e.Command, "epic status") || strings.HasPrefix(e.Command, "show") {
			t.Fatalf("entry %q captured for a fixture with no epic or show issue", e.Command)
		}
	}
}

func TestPerIssueOnlyOnWholeTrackerReads(t *testing.T) {
	for _, e := range testFixture().entries() {
		wholeTracker := e.Command == "ready" || e.Command == "list" || e.Command == "list --json"
		if e.PerIssue != wholeTracker {
			t.Errorf("%q PerIssue = %v, want %v", e.Command, e.PerIssue, wholeTracker)
		}
	}
}

// Each entry needs at least one baseline that answers the same question;
// a comparison against only partial baselines would not be a comparison.
func TestEveryEntryHasAnEquivalentBaseline(t *testing.T) {
	for _, e := range testFixture().entries() {
		if !slices.ContainsFunc(e.Baselines, func(b Baseline) bool { return !b.Partial }) {
			t.Errorf("%q has no baseline that answers the same question", e.Command)
		}
	}
	for _, e := range testFixture().entries() {
		for _, b := range e.Baselines {
			if b.Partial && b.Note == "" {
				t.Errorf("%q baseline %q is partial with no note saying what is missing", e.Command, b.Name)
			}
		}
	}
}

func TestGraphqlArgvSplitsRepoAndResolvesQueryPath(t *testing.T) {
	argv := testFixture().graphql("epic.graphql", "-F", "number=137")
	got := strings.Join(argv, " ")
	want := "gh api graphql -F owner=lumberbarons -F name=solar-controller " +
		"-F query=@cmd/tokens/queries/epic.graphql -F number=137"
	if got != want {
		t.Fatalf("argv =\n%s\nwant\n%s", got, want)
	}
}

func TestHewArgvTargetsTheFixtureRepo(t *testing.T) {
	argv := testFixture().hew("epic", "status", "137")
	want := "hew epic status 137 --repo lumberbarons/solar-controller"
	if got := strings.Join(argv, " "); got != want {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

// hew is variadic over a shared slice; a second call must not see the first
// call's --repo flags.
func TestHewArgvDoesNotLeakBetweenCalls(t *testing.T) {
	f := testFixture()
	first := strings.Join(f.hew("ready"), " ")
	second := strings.Join(f.hew("ready"), " ")
	if first != second {
		t.Fatalf("two identical calls differed:\n%s\n%s", first, second)
	}
}

func TestValidateRejectsUnusableFixtures(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Fixture)
		want string
	}{
		{"no owner", func(f *Fixture) { f.Repo = "solar-controller" }, "owner/name"},
		{"empty repo", func(f *Fixture) { f.Repo = "" }, "owner/name"},
		{"negative issue", func(f *Fixture) { f.ShowIssue = -1 }, "positive"},
		{"no query dir", func(f *Fixture) { f.QueryDir = "" }, "--queries"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := testFixture()
			tc.mut(&f)
			err := f.validate()
			if err == nil {
				t.Fatal("validate accepted an unusable fixture")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
	if err := testFixture().validate(); err != nil {
		t.Fatalf("validate rejected a good fixture: %v", err)
	}
}
