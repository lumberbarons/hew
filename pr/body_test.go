package pr

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/tracker"
)

func TestComposeBodyAppendsTheDeclaredClosingSet(t *testing.T) {
	composed, err := ComposeBody(Body{
		Sections: Sections{What: "w", Why: "y", Testing: "t"},
		Trailers: tracker.PRTrailers{Fixes: []int{30}, Epics: []int{33}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "### What\n\nw\n\n### Why\n\ny\n\n### Testing\n\nt\n\nFixes #30\nPart of #33"
	if composed.Body != want {
		t.Errorf("body = %q, want %q", composed.Body, want)
	}
}

// The closing set is the tracker link the merge acts on, so a supplied body
// that closes a different one is refused rather than silently repaired.
func TestComposeBodyRefusesAMismatchedClosingSet(t *testing.T) {
	for name, tc := range map[string]struct {
		text    string
		fixes   []int
		reason  string
		repeats bool
	}{
		"another issue":  {text: "Fixes #99", fixes: []int{30}, reason: "closes #99 but this PR is for #30"},
		"several others": {text: "Fixes #30\nCloses #31", fixes: []int{30}, reason: "closes exactly one"},
		"a repeated one": {text: "Fixes #30\nFixes #30", fixes: []int{30}, reason: "closes #30 more than once"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ComposeBody(Body{Text: tc.text, Trailers: tracker.PRTrailers{Fixes: tc.fixes}})
			var invalid *tracker.InvalidInputError
			if !errors.As(err, &invalid) || invalid.Field != "trailers" {
				t.Fatalf("err = %T (%v), want an InvalidInputError for trailers", err, err)
			}
			if !strings.Contains(err.Error(), tc.reason) {
				t.Errorf("err = %q, want it to contain %q", err, tc.reason)
			}
		})
	}
}

// A body that already closes the declared set keeps its own wording and gains
// only what it is missing.
func TestComposeBodyKeepsAnExistingClosingReference(t *testing.T) {
	composed, err := ComposeBody(Body{
		Text:     "narrative\n\nCloses #30\n",
		Trailers: tracker.PRTrailers{Fixes: []int{30}, Epics: []int{33}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if composed.Body != "narrative\n\nCloses #30\n\nPart of #33" {
		t.Errorf("body = %q", composed.Body)
	}
}

func TestComposeBodyReportsUnmarkedCodeWithoutRewriting(t *testing.T) {
	text := "Rewrites internal/cli/pr.go and runs go test ./..."
	composed, err := ComposeBody(Body{Text: text, Trailers: tracker.PRTrailers{Fixes: []int{30}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(composed.Body, text) {
		t.Errorf("body = %q, want the author's text untouched", composed.Body)
	}
	if len(composed.Findings) != 1 || composed.Findings[0].Kind != tracker.FindingUnmarkedCode {
		t.Fatalf("findings = %+v, want one unmarked-code finding", composed.Findings)
	}
	if !slices.Contains(composed.Findings[0].Tokens, "internal/cli/pr.go") {
		t.Errorf("tokens = %v", composed.Findings[0].Tokens)
	}
}

func TestCheckBodyReportsFindingsWithoutRefusing(t *testing.T) {
	check, err := CheckBody("runs go test ./...")
	if err != nil {
		t.Fatal(err)
	}
	if len(check.Findings) != 1 || check.Findings[0].Kind != tracker.FindingUnmarkedCode {
		t.Errorf("findings = %+v", check.Findings)
	}
	if check2, err := CheckBody("runs `go test ./...`"); err != nil || len(check2.Findings) != 0 {
		t.Errorf("marked-up text: findings = %+v, err = %v", check2.Findings, err)
	}
}

func TestComposeBodyRefusesConflictingSources(t *testing.T) {
	_, err := ComposeBody(Body{Sections: Sections{What: "w"}, Text: "text"})
	var invalid *tracker.InvalidInputError
	if !errors.As(err, &invalid) || invalid.Field != "body" {
		t.Fatalf("err = %T (%v), want an InvalidInputError for body", err, err)
	}
}

// The trailer block is sorted and carries each epic once, so it reads the same
// however a declared set was ordered.
func TestTrailers(t *testing.T) {
	child := func(n, epic int) tracker.Issue {
		parent := epic
		return tracker.Issue{Number: n, Parent: &parent}
	}
	got := trailers([]tracker.Issue{child(50, 33), child(46, 33), {Number: 47}})
	if !slices.Equal(got.Fixes, []int{46, 47, 50}) {
		t.Errorf("Fixes = %v, want sorted", got.Fixes)
	}
	if !slices.Equal(got.Epics, []int{33}) {
		t.Errorf("Epics = %v, want one #33", got.Epics)
	}
}

func TestComposeCreateDefaultsFromTheIssue(t *testing.T) {
	issue := tracker.Issue{
		Number: 30, Title: "the work",
		Body: ptr("### Where\n\ninternal/cli\n\n### Goal\n\nClose the claim lifecycle.\n\n### Approach\n\nA thin pr command."),
	}
	composed, warnings, err := composeCreate([]tracker.Issue{issue}, Body{Sections: sections("", "", "go test")})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"### What\n\nA thin pr command.",
		"### Why\n\nClose the claim lifecycle.",
		"### Testing\n\ngo test",
		"Fixes #30",
	} {
		if !strings.Contains(composed.Body, want) {
			t.Errorf("body missing %q:\n%s", want, composed.Body)
		}
	}
	if strings.Contains(composed.Body, "internal/cli") {
		t.Errorf("body carried the issue's Where section:\n%s", composed.Body)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %+v", warnings)
	}
}

func TestComposeCreateDoesNotBorrowOneIssueForASet(t *testing.T) {
	first := tracker.Issue{Number: 46, Title: "first", Body: ptr("### Approach\n\nOnly the first issue's approach\n\n### Goal\n\nOnly the first issue's goal")}
	second := tracker.Issue{Number: 50, Title: "second", Body: ptr("### Approach\n\nsecond approach\n\n### Goal\n\nsecond goal")}
	body := Body{Trailers: tracker.PRTrailers{Fixes: []int{46, 50}}}
	composed, warnings, err := composeCreate([]tracker.Issue{first, second}, body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(composed.Body, "first issue's") || strings.Contains(composed.Body, "second approach") {
		t.Errorf("a set borrowed one issue's text:\n%s", composed.Body)
	}
	if len(warnings) != 1 || warnings[0].Kind != WarnNoSectionDefaults {
		t.Fatalf("warnings = %+v, want the no-defaults warning", warnings)
	}
	if !slices.Equal(warnings[0].Issues, []int{46, 50}) {
		t.Errorf("Issues = %v", warnings[0].Issues)
	}
}

func TestBodyWarningsRequireRiskOnlyWhenReady(t *testing.T) {
	const noRisk = "### Testing\n\nBefore: failed. After: passes."
	got := bodyWarnings(noRisk, true)
	kinds := make([]WarningKind, len(got))
	for i, w := range got {
		kinds[i] = w.Kind
	}
	if !slices.Equal(kinds, []WarningKind{WarnMissingRisk}) {
		t.Errorf("ready warnings = %v, want just the Risk warning", kinds)
	}
	if got := bodyWarnings(noRisk, false); len(got) != 0 {
		t.Errorf("draft warnings = %+v, want none", got)
	}
	both := bodyWarnings("### What\n\nw", true)
	if len(both) != 2 {
		t.Errorf("empty body warnings = %+v, want Testing and Risk", both)
	}
}

// An untyped issue has nothing to derive a prefix from, and inventing one
// would file the work under the wrong changelog heading.
func TestPRTitle(t *testing.T) {
	typ := "enhancement"
	for name, tc := range map[string]struct {
		issue tracker.Issue
		want  string
	}{
		"typed":   {issue: tracker.Issue{Title: "the work", Type: &typ}, want: "feat: the work"},
		"untyped": {issue: tracker.Issue{Title: "the work"}, want: "the work"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := prTitle(tc.issue); got != tc.want {
				t.Errorf("prTitle = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIssueBodyAbsent(t *testing.T) {
	if got := issueBody(tracker.Issue{}); got != "" {
		t.Errorf("issueBody = %q, want empty", got)
	}
}

func TestClaimWarnings(t *testing.T) {
	other := tracker.Issue{Number: 50, Assignees: []string{"someone"}, Areas: []string{"cli"}}
	mine := tracker.Issue{Number: 46, Assignees: []string{"me"}, Areas: []string{"cli"}}
	unclaimed := tracker.Issue{Number: 51, Areas: []string{"cli"}}
	got := claimWarnings([]tracker.Issue{mine, other, unclaimed}, "me")
	if len(got) != 2 {
		t.Fatalf("warnings = %+v, want the foreign claim and the unclaimed issue", got)
	}
	if got[0].Kind != WarnUnclaimed || got[0].Issue != 50 || !slices.Equal(got[0].Assignees, []string{"someone"}) {
		t.Errorf("foreign claim warning = %+v", got[0])
	}
	if got[1].Kind != WarnUnclaimed || got[1].Issue != 51 || len(got[1].Assignees) != 0 {
		t.Errorf("unclaimed warning = %+v", got[1])
	}
	if unrelated := claimWarnings([]tracker.Issue{mine, other}, "me"); len(unrelated) != 1 {
		t.Errorf("related set warnings = %+v, want only the claim warning", unrelated)
	}
}
