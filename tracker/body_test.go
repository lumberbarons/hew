package tracker

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/internal/conventions"
)

func TestComposeBodyRendersIssueSectionsInTemplateOrder(t *testing.T) {
	composed, err := ComposeBody(Body{Sections: Sections{
		Where:    "`tracker/body.go`",
		Problem:  "a caller could not build a body",
		Fix:      "expose the composition the CLI uses",
		DoneWhen: []string{"tests pass", "the CLI delegates"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := "### Where\n\n`tracker/body.go`\n\n### Problem\n\na caller could not build a body\n\n### Fix\n\nexpose the composition the CLI uses\n\n### Done when\n\n- [ ] tests pass\n- [ ] the CLI delegates"
	if composed.Body != want {
		t.Errorf("Body = %q, want %q", composed.Body, want)
	}
	if len(composed.Findings) != 0 {
		t.Errorf("Findings = %+v, want none", composed.Findings)
	}
}

// A section the caller did not fill in is left out rather than shipped as a
// bare header, which is the template rule the CLI's bodies already follow.
func TestComposeBodyOmitsUnsetSections(t *testing.T) {
	composed, err := ComposeBody(Body{Sections: Sections{Goal: "ship it"}})
	if err != nil {
		t.Fatal(err)
	}
	if composed.Body != "### Goal\n\nship it" {
		t.Errorf("Body = %q", composed.Body)
	}
}

// Open questions is the optional discovery section: an idea can carry only a
// problem and its questions, without an approach or a done-when checklist.
func TestComposeBodyWritesOpenQuestionsOnlyWhenSupplied(t *testing.T) {
	composed, err := ComposeBody(Body{Sections: Sections{
		Problem:       "Ideas get lost in the backlog.",
		OpenQuestions: []string{"Which command filters ideas?", "How does hewd browse them?"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := "### Problem\n\nIdeas get lost in the backlog.\n\n### Open questions\n\n- Which command filters ideas?\n- How does hewd browse them?"
	if composed.Body != want {
		t.Errorf("Body = %q, want %q", composed.Body, want)
	}
	if strings.Contains(composed.Body, "### Approach") || strings.Contains(composed.Body, "### Done when") {
		t.Errorf("Body carried a section it was not given:\n%s", composed.Body)
	}
	if got, err := ComposeBody(Body{Sections: Sections{Problem: "no questions"}}); err != nil || strings.Contains(got.Body, "Open questions") {
		t.Errorf("unset open questions = %q, %v; want no heading", got.Body, err)
	}
}

func TestComposeBodyUsesSuppliedTextVerbatim(t *testing.T) {
	const text = "freeform **body**\n\n- a list\n"
	composed, err := ComposeBody(Body{Text: text})
	if err != nil {
		t.Fatal(err)
	}
	if composed.Body != text {
		t.Errorf("Body = %q, want the supplied bytes %q", composed.Body, text)
	}
}

func TestComposeBodyAppendsTheDiscoveryLink(t *testing.T) {
	for name, tc := range map[string]struct{ text, want string }{
		"empty body": {"", conventions.DiscoveredFrom(7)},
		"text":       {"notes", "notes\n\n" + conventions.DiscoveredFrom(7)},
	} {
		t.Run(name, func(t *testing.T) {
			composed, err := ComposeBody(Body{Text: tc.text, DiscoveredFrom: 7})
			if err != nil {
				t.Fatal(err)
			}
			if composed.Body != tc.want {
				t.Errorf("Body = %q, want %q", composed.Body, tc.want)
			}
		})
	}
}

// The body sources are exclusive: a caller composes sections or supplies text,
// never both. The CLI's flag-level refusal is the same rule in flag terms.
func TestComposeBodyRefusesConflictingSources(t *testing.T) {
	for name, tc := range map[string]struct {
		body   Body
		reason string
	}{
		"sections and text": {
			Body{Sections: Sections{Goal: "g"}, Text: "t"},
			"body and section fields (where/problem/goal/fix/approach/open-questions/done-when) are mutually exclusive",
		},
		"issue and pull-request sections": {
			Body{Sections: Sections{Goal: "g"}, PRSections: PRSections{What: "w"}},
			"issue and pull-request sections are mutually exclusive",
		},
		"pull-request sections and text": {
			Body{PRSections: PRSections{What: "w"}, Text: "t"},
			"pull-request sections and body text are mutually exclusive",
		},
		"issue sections and trailers": {
			Body{Sections: Sections{Goal: "g"}, Trailers: PRTrailers{Fixes: []int{1}}},
			"issue sections cannot carry pull-request trailers",
		},
		"pull-request body and discovered-from": {
			Body{PRSections: PRSections{What: "w"}, DiscoveredFrom: 7},
			"discovered-from applies to issue bodies only",
		},
		"pull-request text and discovered-from": {
			Body{Text: "t", Trailers: PRTrailers{Fixes: []int{1}}, DiscoveredFrom: 7},
			"discovered-from applies to issue bodies only",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ComposeBody(tc.body)
			var invalid *InvalidInputError
			if !errors.As(err, &invalid) {
				t.Fatalf("err = %T (%v), want *InvalidInputError", err, err)
			}
			if invalid.Field != "body" {
				t.Errorf("Field = %q, want body", invalid.Field)
			}
			if !strings.Contains(invalid.Reason, tc.reason) {
				t.Errorf("Reason = %q, want %q", invalid.Reason, tc.reason)
			}
		})
	}
}

func TestValidateSectionsRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		sections Sections
		field    string
		reason   string
	}{
		"problem and goal": {
			Sections{Problem: "p", Goal: "g"},
			"problem", "problem and goal are mutually exclusive",
		},
		"fix and approach": {
			Sections{Fix: "f", Approach: "a"},
			"fix", "fix and approach are mutually exclusive",
		},
		"empty open question": {
			Sections{OpenQuestions: []string{"ok", "  "}},
			"open-questions", "open questions cannot be empty",
		},
		"empty done-when": {
			Sections{DoneWhen: []string{"ok", "  "}},
			"done-when", "done-when items cannot be empty",
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := ValidateSections(tc.sections)
			var invalid *InvalidInputError
			if !errors.As(err, &invalid) {
				t.Fatalf("err = %T (%v), want *InvalidInputError", err, err)
			}
			if invalid.Field != tc.field {
				t.Errorf("Field = %q, want %q", invalid.Field, tc.field)
			}
			if invalid.Reason != tc.reason {
				t.Errorf("Reason = %q, want %q", invalid.Reason, tc.reason)
			}
		})
	}
	if err := ValidateSections(Sections{Goal: "g", DoneWhen: []string{"ok"}}); err != nil {
		t.Errorf("ValidateSections = %v, want no error", err)
	}
}

// ComposeBody runs the same section rules, so a caller that skipped
// ValidateSections still cannot publish a body the CLI would refuse.
func TestComposeBodyRefusesInvalidSections(t *testing.T) {
	_, err := ComposeBody(Body{Sections: Sections{Problem: "p", Goal: "g"}})
	var invalid *InvalidInputError
	if !errors.As(err, &invalid) {
		t.Fatalf("err = %T (%v), want *InvalidInputError", err, err)
	}
	if !strings.Contains(invalid.Reason, "problem and goal are mutually exclusive") {
		t.Errorf("Reason = %q", invalid.Reason)
	}
}

func TestPRSectionsComposeRendersTrailers(t *testing.T) {
	sections := PRSections{What: "the thing"}
	got := sections.Compose(PRTrailers{Fixes: []int{46}, Epics: []int{33}})
	want := "### What\n\nthe thing\n\nFixes #46\nPart of #33"
	if got != want {
		t.Errorf("Compose = %q, want %q", got, want)
	}
	if got := (PRTrailers{}).Render(); got != "" {
		t.Errorf("Render = %q, want empty", got)
	}
}

func TestComposeBodyRendersPullRequestSectionsAndTrailers(t *testing.T) {
	composed, err := ComposeBody(Body{
		PRSections: PRSections{What: "the thing", Testing: "`go test ./...`"},
		Trailers:   PRTrailers{Fixes: []int{46}, Epics: []int{33}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "### What\n\nthe thing\n\n### Testing\n\n`go test ./...`\n\nFixes #46\nPart of #33"
	if composed.Body != want {
		t.Errorf("Body = %q, want %q", composed.Body, want)
	}
}

// A supplied PR body must close exactly the declared set: a missing link would
// leave the issue claimed, and an extra one would close an issue on merge that
// nobody declared.
func TestComposeBodyEnforcesThePullRequestClosingSet(t *testing.T) {
	trailers := PRTrailers{Fixes: []int{46, 50}}
	for name, tc := range map[string]struct {
		text, want, reason string
	}{
		"none gets all":   {"narrative", "narrative\n\nFixes #46\nFixes #50", ""},
		"equal":           {"narrative\n\nFixes #46\nFixes #50", "narrative\n\nFixes #46\nFixes #50", ""},
		"equal reordered": {"narrative\n\nCloses #50\nFixes #46", "narrative\n\nCloses #50\nFixes #46", ""},
		"drops one":       {"narrative\n\nFixes #46", "", "closes #46 but this PR is for #46 #50"},
		"adds one":        {"Fixes #46\nFixes #50\nFixes #51", "", "closes #46 #50 #51 but this PR is for #46 #50"},
		"changes one":     {"Fixes #46\nFixes #99", "", "closes #46 #99 but this PR is for #46 #50"},
		"repeats one":     {"Fixes #46\nFixes #46\nFixes #50", "", "closes #46 #46 #50 but this PR is for #46 #50"},
	} {
		t.Run(name, func(t *testing.T) {
			composed, err := ComposeBody(Body{Text: tc.text, Trailers: trailers})
			if tc.reason != "" {
				var invalid *InvalidInputError
				if !errors.As(err, &invalid) {
					t.Fatalf("err = %T (%v), want *InvalidInputError", err, err)
				}
				if invalid.Field != "trailers" || invalid.Reason != tc.reason {
					t.Errorf("InvalidInputError = %+v, want field trailers reason %q", invalid, tc.reason)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if composed.Body != tc.want {
				t.Errorf("Body = %q, want %q", composed.Body, tc.want)
			}
		})
	}
}

func TestComposeBodyExplainsARepeatedClosingReferenceToOneIssue(t *testing.T) {
	_, err := ComposeBody(Body{Text: "Fixes #30\nCloses #30", Trailers: PRTrailers{Fixes: []int{30}}})
	if err == nil || !strings.Contains(err.Error(), "closes #30 more than once; write the closing reference once") {
		t.Errorf("err = %v, want the repeat named with its remedy", err)
	}
}

func TestComposeBodyRefusesSeveralClosingReferencesToOneIssue(t *testing.T) {
	_, err := ComposeBody(Body{Text: "Fixes #30\nCloses #31", Trailers: PRTrailers{Fixes: []int{30}}})
	if err == nil || !strings.Contains(err.Error(), "closes 2 issues; a PR closes exactly one (declare a set with repeated --for)") {
		t.Errorf("err = %v, want the closing-set rule", err)
	}
}

// A trailer the author already wrote is left alone rather than repeated: the
// composed body is what a reviewer reads.
func TestComposeBodyDoesNotRepeatAnEpicTrailer(t *testing.T) {
	const text = "narrative\n\nFixes #46\nPart of #33"
	composed, err := ComposeBody(Body{Text: text, Trailers: PRTrailers{Fixes: []int{46}, Epics: []int{33}}})
	if err != nil {
		t.Fatal(err)
	}
	if composed.Body != text {
		t.Errorf("Body = %q, want %q", composed.Body, text)
	}
}

func TestComposeBodyRefusesAClosingReferenceWithNoDeclaredSet(t *testing.T) {
	_, err := ComposeBody(Body{Text: "Fixes #30", Trailers: PRTrailers{Epics: []int{33}}})
	if err == nil || !strings.Contains(err.Error(), "closes #30 but no closing issues were declared") {
		t.Errorf("err = %v, want the missing declaration named", err)
	}
}

// Markup findings are advice, not validation: the body comes back exactly as
// supplied and the call succeeds.
func TestComposeBodyReportsUnmarkedCodeWithoutRewriting(t *testing.T) {
	const text = "Run `go test` after editing internal/cli/write.go with --body-file; TestComposeBody covers it."
	composed, err := ComposeBody(Body{Text: text})
	if err != nil {
		t.Errorf("unmarked code failed the composition: %v", err)
	}
	if composed.Body != text {
		t.Errorf("Body = %q, want the author's bytes %q", composed.Body, text)
	}
	if len(composed.Findings) != 1 {
		t.Fatalf("Findings = %+v, want one", composed.Findings)
	}
	finding := composed.Findings[0]
	if finding.Kind != FindingUnmarkedCode {
		t.Errorf("Kind = %q, want %q", finding.Kind, FindingUnmarkedCode)
	}
	for _, want := range []string{"internal/cli/write.go", "--body-file", "TestComposeBody"} {
		if !slices.Contains(finding.Tokens, want) {
			t.Errorf("Tokens = %v, want %q", finding.Tokens, want)
		}
	}
}

func TestComposeBodyReportsACodeTokenOnce(t *testing.T) {
	composed, err := ComposeBody(Body{Text: "internal/cli/write.go and internal/cli/write.go"})
	if err != nil {
		t.Fatal(err)
	}
	if got := composed.Findings[0].Tokens; !slices.Equal(got, []string{"internal/cli/write.go"}) {
		t.Errorf("Tokens = %v, want the token once", got)
	}
}

func TestCheckBodyReportsFindings(t *testing.T) {
	clean, err := CheckBody("Run `go test ./...` before pushing")
	if err != nil {
		t.Fatal(err)
	}
	if len(clean.Findings) != 0 {
		t.Errorf("Findings = %+v, want none for marked code", clean.Findings)
	}

	check, err := CheckBody("Run go test ./... and read internal/cli/pr.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(check.Findings) != 1 || check.Findings[0].Kind != FindingUnmarkedCode {
		t.Fatalf("Findings = %+v, want one FindingUnmarkedCode", check.Findings)
	}
	for _, want := range []string{"./...", "internal/cli/pr.go"} {
		if !slices.Contains(check.Findings[0].Tokens, want) {
			t.Errorf("Tokens = %v, want %q", check.Findings[0].Tokens, want)
		}
	}
}

// create with no body flags files an empty body, so composition must allow
// one; the draft command's "nothing to compose" refusal is its own rule.
func TestComposeBodyAllowsAnEmptyBody(t *testing.T) {
	composed, err := ComposeBody(Body{})
	if err != nil {
		t.Fatalf("ComposeBody = %v, want no error", err)
	}
	if composed.Body != "" {
		t.Errorf("Body = %q, want empty", composed.Body)
	}
	if len(composed.Findings) != 0 {
		t.Errorf("Findings = %+v, want none", composed.Findings)
	}
}
