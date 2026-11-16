package pr

import (
	"errors"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/model"
	"github.com/lumberbarons/hew/tracker"
)

// amendSetup is a claimed sub-issue with an open draft pull request whose body
// carries every section and both trailers.
func amendSetup() (*Client, *fakeStore, *fakePRs) {
	issue := subIssueOf(claimed(30, "issue title", "P2", "enhancement"), 33)
	issue.Body = "### Approach\n\nIssue approach\n\n### Goal\n\nIssue goal"
	client, store, prs := wrapped(issue)
	prs.existing = &gh.PullRequest{
		Number: 501, URL: "https://github.com/o/r/pull/501", Draft: true,
		Title: "feat: original title", Base: "release", HeadRepository: "o/r",
		Body: "### What\n\nOriginal what\n\n### Why\n\nOriginal why\n\n### Testing\n\nOriginal testing\n\n### Risk\n\nOriginal risk\n\nFixes #30\nPart of #33",
	}
	return client, store, prs
}

func TestAmendRecomposesOnlyTheNamedSections(t *testing.T) {
	const want = "### What\n\nRevised what\n\n### Why\n\nOriginal why\n\n### Testing\n\nOriginal testing\n\n### Risk\n\nOriginal risk\n\nFixes #30\nPart of #33"
	client, _, prs := amendSetup()
	if err := amendClient(t, client, AmendRequest{Edit: BodyEdit{What: ptr("Revised what")}}); err != nil {
		t.Fatal(err)
	}
	if got := prs.updated[0]; got.body != want || got.number != 501 {
		t.Errorf("update = %+v, want %q", got, want)
	}
	if got := prs.updated[0].title; got != "feat: original title" {
		t.Errorf("title = %q, want the existing one", got)
	}
}

func amendClient(t *testing.T, client *Client, req AmendRequest) error {
	t.Helper()
	_, err := client.Amend(t.Context(), req)
	return err
}

func TestAmendPreservesAbsentSectionsAndClearsExplicitEmpty(t *testing.T) {
	client, _, prs := amendSetup()
	prs.existing.Body = "### Testing\n\nOld tests\n\nFixes #30"
	if err := amendClient(t, client, AmendRequest{Edit: BodyEdit{Testing: ptr("")}}); err != nil {
		t.Fatal(err)
	}
	if got, want := prs.updated[0].body, "Fixes #30\nPart of #33"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestAmendPreservesAndClearsRisk(t *testing.T) {
	client, _, prs := amendSetup()
	if err := amendClient(t, client, AmendRequest{Edit: BodyEdit{Testing: ptr("Revised testing")}}); err != nil {
		t.Fatal(err)
	}
	if body := prs.updated[0].body; !strings.Contains(body, "### Risk\n\nOriginal risk") {
		t.Errorf("amend lost the preserved Risk section:\n%s", body)
	}
	if err := amendClient(t, client, AmendRequest{Edit: BodyEdit{Risk: ptr("")}}); err != nil {
		t.Fatal(err)
	}
	body := prs.updated[1].body
	if strings.Contains(body, "### Risk") {
		t.Errorf("an explicitly empty section did not remove it:\n%s", body)
	}
	if !strings.Contains(body, "### Testing\n\nRevised testing") {
		t.Errorf("clearing Risk changed Testing:\n%s", body)
	}
}

// A title-only amendment preserves even a free-form body, and still reports
// the pull request's existing metadata.
func TestAmendTitleOnlyPreservesAFreeformBody(t *testing.T) {
	client, _, prs := amendSetup()
	prs.existing.Body = "Freeform body\n\nCloses #30\nPart of #33"
	result, err := client.Amend(t.Context(), AmendRequest{Title: ptr("feat: revised title")})
	if err != nil {
		t.Fatal(err)
	}
	got := prs.updated[0]
	if got.title != "feat: revised title" || got.body != prs.existing.Body {
		t.Errorf("update = %+v, want the title changed and the body untouched", got)
	}
	if result.Number != 501 || result.Fixes != 30 || result.Head != "feat/x" || result.Base != "release" || !result.Draft {
		t.Errorf("result = %+v", result)
	}
	if len(result.Warnings) != 0 {
		t.Errorf("a title-only amendment drew body advice: %+v", result.Warnings)
	}
}

// A body the workflow wrote without a tracker link still gains the missing one
// on the title-only path, which is the same composition the create path
// enforces.
func TestAmendTitleOnlyAppendsAMissingTrailer(t *testing.T) {
	client, _, prs := amendSetup()
	prs.existing.Body = "Freeform body"
	if err := amendClient(t, client, AmendRequest{Title: ptr("feat: new")}); err != nil {
		t.Fatal(err)
	}
	if got, want := prs.updated[0].body, "Freeform body\n\nFixes #30\nPart of #33"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestAmendBodyFileReplacesTheWholeBody(t *testing.T) {
	client, _, prs := amendSetup()
	custom := "Introduction.\n\n### What\n\n```markdown\n### Example\nKeep this.\n```\n\n### Notes\n\nCustom notes.\n\nFixes #30\nPart of #33"
	if err := amendClient(t, client, AmendRequest{Edit: BodyEdit{Text: ptr(custom)}}); err != nil {
		t.Fatal(err)
	}
	if got := prs.updated[0].body; got != custom {
		t.Errorf("body = %q, want %q", got, custom)
	}
}

func TestAmendRefusesUnsafeSectionEdits(t *testing.T) {
	bodies := map[string]string{
		"fenced heading":       "### What\n\nExample:\n\n```markdown\n### Example\nKeep this example.\n```\n\n### Why\n\nKeep why.\n\n### Testing\n\nOld tests\n\nFixes #30\nPart of #33",
		"fenced known heading": "### What\n\n```markdown\n\n### Why\n\nInside the fence.\n```\n\n### Testing\n\nOld tests\n\nFixes #30\nPart of #33",
		"tilde fence":          "### What\n\n~~~markdown\n### Example\nKeep this example.\n~~~\n\n### Testing\n\nOld tests\n\nFixes #30\nPart of #33",
		"preamble":             "Keep this introduction.\n\n### What\n\nKeep what.\n\n### Testing\n\nOld tests\n\nFixes #30\nPart of #33",
		"extra section":        "### What\n\nKeep what.\n\n### Notes\n\nKeep these notes.\n\n### Testing\n\nOld tests\n\nFixes #30\nPart of #33",
		"freeform":             "Keep this entire freeform description.\n\nFixes #30\nPart of #33",
		"duplicate section":    "### What\n\nFirst what.\n\n### What\n\nSecond what.\n\nFixes #30\nPart of #33",
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			client, _, prs := amendSetup()
			original := *prs.existing
			prs.existing.Body = body
			err := amendClient(t, client, AmendRequest{
				Title: ptr("also must not change"), Edit: BodyEdit{Testing: ptr("New tests")},
			})
			assertInvalid(t, err, FieldBodyEdit)
			if len(prs.updated) != 0 {
				t.Error("a refused edit updated the pull request")
			}
			if prs.existing.Title != original.Title {
				t.Error("a refused edit changed the pull request")
			}
		})
	}
}

func TestAmendRefusesTextAndSectionsTogether(t *testing.T) {
	client, _, prs := amendSetup()
	err := amendClient(t, client, AmendRequest{Edit: BodyEdit{Text: ptr("body"), What: ptr("w")}})
	assertInvalid(t, err, FieldBody)
	if len(prs.updated) != 0 {
		t.Error("a conflicting edit updated the pull request")
	}
}

func TestAmendRefusesAMissingOpenPullRequest(t *testing.T) {
	client, _, prs := wrapped(claimed(30, "work"))
	_, err := client.Amend(t.Context(), AmendRequest{Title: ptr("new")})
	var noOpen *NoOpenPullRequestError
	if !errors.As(err, &noOpen) {
		t.Fatalf("err = %T (%v), want *NoOpenPullRequestError", err, err)
	}
	if noOpen.Head != "feat/x" || noOpen.Issue != 30 {
		t.Errorf("error = %+v", noOpen)
	}
	if len(prs.updated) != 0 {
		t.Error("an absent pull request was updated")
	}
}

func TestAmendRefusesAmbiguousOrForeignHead(t *testing.T) {
	for _, tc := range []struct {
		name       string
		count      int
		repository string
		kind       AmbiguityKind
	}{
		{"two base branches", 2, "o/r", AmbiguousMatches},
		{"truncated matches", 100, "o/r", AmbiguousMatches},
		{"fork", 1, "contributor/r", AmbiguousHead},
		{"unknown repository", 1, "", AmbiguousHead},
		{"unknown count", 0, "o/r", AmbiguousMatches},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _, prs := amendSetup()
			prs.existing.HeadRepository = tc.repository
			prs.openCount, prs.countSet = tc.count, true
			_, err := client.Amend(t.Context(), AmendRequest{Title: ptr("must not update")})
			var ambiguous *AmbiguousPullRequestError
			if !errors.As(err, &ambiguous) {
				t.Fatalf("err = %T (%v), want *AmbiguousPullRequestError", err, err)
			}
			if ambiguous.Kind != tc.kind {
				t.Errorf("Kind = %q, want %q", ambiguous.Kind, tc.kind)
			}
			if len(prs.updated) != 0 {
				t.Error("an ambiguous match was updated")
			}
		})
	}
}

func TestAmendMatchesHeadRepositoryCaseInsensitively(t *testing.T) {
	client, _, prs := amendSetup()
	prs.existing.HeadRepository = "O/R"
	if err := amendClient(t, client, AmendRequest{Title: ptr("new title")}); err != nil {
		t.Fatal(err)
	}
	if got := prs.updated[0].title; got != "new title" {
		t.Errorf("title = %q", got)
	}
}

// The closing set is re-checked on every path that writes a body: a link
// already in the preserved text, one the edit introduces, and a supplied body
// file that closes a different set.
func TestAmendRefusesAClosingSetMismatch(t *testing.T) {
	t.Run("preserved", func(t *testing.T) {
		client, _, prs := amendSetup()
		prs.existing.Body = strings.Replace(prs.existing.Body, "Original why", "Closes #99", 1)
		err := amendClient(t, client, AmendRequest{Edit: BodyEdit{What: ptr("new what")}})
		assertInvalid(t, err, "trailers")
		if len(prs.updated) != 0 {
			t.Error("a mismatched closing set was written")
		}
	})
	t.Run("introduced by the edit", func(t *testing.T) {
		client, _, prs := amendSetup()
		err := amendClient(t, client, AmendRequest{Edit: BodyEdit{What: ptr("Fixes #99")}})
		assertInvalid(t, err, "trailers")
		if len(prs.updated) != 0 {
			t.Error("a mismatched closing set was written")
		}
	})
	t.Run("supplied body file", func(t *testing.T) {
		client, _, prs := amendSetup()
		err := amendClient(t, client, AmendRequest{Edit: BodyEdit{Text: ptr("Fixes #99")}})
		assertInvalid(t, err, "trailers")
		if len(prs.updated) != 0 {
			t.Error("a mismatched closing set was written")
		}
	})
}

// Risk is asked for once a pull request is out of draft; a draft keeps it
// optional.
func TestAmendWarnsAboutMissingRiskOnlyWhenReady(t *testing.T) {
	const noRisk = "### What\n\nOriginal what\n\n### Why\n\nOriginal why\n\n### Testing\n\nOriginal testing\n\nFixes #30\nPart of #33"
	for _, tc := range []struct {
		name  string
		draft bool
		req   AmendRequest
		want  bool
	}{
		{"ready section edit", false, AmendRequest{Edit: BodyEdit{What: ptr("Revised")}}, true},
		{"draft section edit", true, AmendRequest{Edit: BodyEdit{What: ptr("Revised")}}, false},
		{"ready title-only amend", false, AmendRequest{Title: ptr("feat: new title")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _, prs := amendSetup()
			prs.existing.Draft, prs.existing.Body = tc.draft, noRisk
			result, err := client.Amend(t.Context(), tc.req)
			if err != nil {
				t.Fatal(err)
			}
			got := false
			for _, w := range result.Warnings {
				if w.Kind == WarnMissingRisk {
					got = true
				}
			}
			if got != tc.want {
				t.Errorf("warned about Risk = %v, want %v (%+v)", got, tc.want, result.Warnings)
			}
		})
	}
}

func TestAmendKeepsItsAdviceWhenTheUpdateFails(t *testing.T) {
	client, _, prs := amendSetup()
	cause := errors.New("update refused")
	prs.updateErr = cause
	result, err := client.Amend(t.Context(), AmendRequest{
		Edit: BodyEdit{What: ptr("Changes internal/cli/pr.go")},
	})
	if !errors.Is(err, cause) {
		t.Fatalf("err = %v, want the backend cause", err)
	}
	if len(result.Findings) == 0 {
		t.Error("the composition's findings were lost")
	}
	if len(prs.updated) != 0 {
		t.Error("a failed update was recorded")
	}
}

// An amendment with no body edit still validates the preserved body and
// reports its unmarked code, because the merge depends on that closing set.
func TestAmendValidatesAndAdvisesOnThePreservedBody(t *testing.T) {
	client, _, prs := amendSetup()
	prs.existing.Body = "### Testing\n\nRan go test ./...\n\nFixes #30"
	result, err := client.Amend(t.Context(), AmendRequest{Title: ptr("feat: new")})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 1 || result.Findings[0].Kind != "unmarked-code" {
		t.Errorf("findings = %+v, want the unmarked-code advice", result.Findings)
	}
}

// A tracked issue that is not open cannot be a pull-request target, and an
// epic never is.
func TestAmendRefusesAnUnusableDeclaration(t *testing.T) {
	client, store, _ := amendSetup()
	store.issues = append(store.issues, model.Issue{Number: 99, Title: "closed", State: "CLOSED"})
	_, err := client.Amend(t.Context(), AmendRequest{For: []int{99}})
	if err == nil {
		t.Fatal("a closed issue was accepted")
	}
}

// The update path maps auth the same way the create path does: one error
// vocabulary for a consumer, whichever half of the client failed.
func TestAmendMapsAWriteAuthFailure(t *testing.T) {
	client, _, prs := amendSetup()
	cause := errors.New("token expired")
	prs.updateErr = &gh.AuthError{Err: cause}
	_, err := client.Amend(t.Context(), AmendRequest{Title: ptr("new")})
	var auth *tracker.AuthError
	if !errors.As(err, &auth) || !errors.Is(err, cause) {
		t.Fatalf("err = %T (%v), want a *tracker.AuthError retaining the cause", err, err)
	}
}
