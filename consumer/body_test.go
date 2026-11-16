package consumer_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/lumberbarons/hew/tracker"
)

// Body composition is public API: an external module composes and checks
// bodies with tracker's types alone, no internal import and no network.
func TestBodyCompositionIsNameableFromAnExternalPackage(t *testing.T) {
	composed, err := tracker.ComposeBody(tracker.Body{
		Sections: tracker.Sections{Goal: "ship it", DoneWhen: []string{"tests pass"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if composed.Body != "### Goal\n\nship it\n\n### Done when\n\n- [ ] tests pass" {
		t.Errorf("Body = %q", composed.Body)
	}

	check, err := tracker.CheckBody("see internal/cli/write.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(check.Findings) != 1 || check.Findings[0].Kind != tracker.FindingUnmarkedCode {
		t.Fatalf("Findings = %+v, want one tracker.FindingUnmarkedCode", check.Findings)
	}
	if !slices.Contains(check.Findings[0].Tokens, "internal/cli/write.go") {
		t.Errorf("Tokens = %v", check.Findings[0].Tokens)
	}

	pr, err := tracker.ComposeBody(tracker.Body{
		PRSections: tracker.PRSections{What: "the change", Testing: "`go test ./...`"},
		Trailers:   tracker.PRTrailers{Fixes: []int{46}, Epics: []int{33}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "### What\n\nthe change\n\n### Testing\n\n`go test ./...`\n\nFixes #46\nPart of #33"; pr.Body != want {
		t.Errorf("Body = %q, want %q", pr.Body, want)
	}

	err = tracker.ValidateSections(tracker.Sections{Problem: "p", Goal: "g"})
	var invalid *tracker.InvalidInputError
	if !errors.As(err, &invalid) {
		t.Fatalf("err = %T (%v), want *tracker.InvalidInputError", err, err)
	}
}
