package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lumberbarons/issues/internal/conventions"
	"github.com/lumberbarons/issues/internal/git"
	"github.com/lumberbarons/issues/internal/model"
)

// onBranch wires an App's Git hook to a fixed, pushed branch — the normal
// precondition, so tests that aren't about git state say it in one word.
func onBranch(app *App, branch string) {
	app.Git = func() (git.State, error) {
		return git.State{Branch: branch, Pushed: true}, nil
	}
}

// claimed builds an issue already assigned to the fake's viewer.
func claimed(n int, title string, labels ...string) *model.Issue {
	i := issue(n, title, append(labels, model.InProgressLabel)...)
	i.Assignees = []string{"me"}
	return i
}

// subIssueOf hangs an issue off an epic, the state that earns a PR its
// "Part of #n" trailer.
func subIssueOf(i *model.Issue, epic int) *model.Issue {
	i.Parent = &model.Ref{Number: epic, State: "OPEN"}
	return i
}

// createdPR returns the recorded CreatePullRequest call, failing when the
// command never got that far.
func createdPR(t *testing.T, f *fakeClient) string {
	t.Helper()
	for _, c := range f.calls {
		if strings.HasPrefix(c, "CreatePullRequest ") {
			return c
		}
	}
	t.Fatalf("no pull request was created; calls: %v", f.calls)
	return ""
}

func TestPRCreatesDraftForTheClaimedIssue(t *testing.T) {
	f := newFake(claimed(30, "PR creation command", "P2", "enhancement"))
	app, out, _ := newApp(f)
	onBranch(app, "feat/pr-command")

	if err := app.PR(context.Background(), PROpts{Sections: sections("", "", "go test")}); err != nil {
		t.Fatal(err)
	}
	call := createdPR(t, f)
	if !strings.Contains(call, "feat/pr-command→main") {
		t.Errorf("PR head/base wrong: %s", call)
	}
	if !strings.Contains(call, "draft=true") {
		t.Errorf("PR was not opened as a draft: %s", call)
	}
	if !strings.Contains(call, `"PR creation command"`) {
		t.Errorf("PR title did not default to the issue title: %s", call)
	}
	if !strings.Contains(call, "Fixes #30") {
		t.Errorf("PR body has no Fixes trailer: %s", call)
	}
	// Both lines, exactly: the summary an agent branches on and the URL a
	// human clicks. Asserted whole so dropping either one fails here.
	want := "created draft PR #501 for #30: PR creation command\n" +
		"https://github.com/o/r/pull/501\n"
	if got := out.String(); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// The composed body is the point of the command: What and Why come from
// what the issue already says, so the PR isn't a retyping exercise.
func TestPRComposesBodyFromTheIssue(t *testing.T) {
	i := claimed(30, "PR creation command", "P2", "enhancement")
	i.Body = "### Where\n\ninternal/cli\n\n### Goal\n\nClose the claim lifecycle.\n\n### Approach\n\nA thin pr command."
	f := newFake(i)
	app, _, _ := newApp(f)
	onBranch(app, "feat/pr-command")

	if err := app.PR(context.Background(), PROpts{Sections: sections("", "", "go test")}); err != nil {
		t.Fatal(err)
	}
	body := prBody(t, createdPR(t, f))
	for _, want := range []string{
		"### What\n\nA thin pr command.",
		"### Why\n\nClose the claim lifecycle.",
		"### Testing\n\ngo test",
		"Fixes #30",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
	// Where is the issue's own scaffolding, not something a reviewer needs.
	if strings.Contains(body, "internal/cli") {
		t.Errorf("body carried the issue's Where section:\n%s", body)
	}
}

func TestPRFlagsOverrideTheIssueSections(t *testing.T) {
	i := claimed(30, "PR creation command")
	i.Body = "### Goal\n\nfrom the issue\n\n### Approach\n\nalso from the issue"
	f := newFake(i)
	app, _, _ := newApp(f)
	onBranch(app, "feat/pr-command")

	if err := app.PR(context.Background(), PROpts{
		Title: "feat: issues pr", Sections: sections("hand-written what", "hand-written why", "go test"),
	}); err != nil {
		t.Fatal(err)
	}
	call := createdPR(t, f)
	if !strings.Contains(call, `"feat: issues pr"`) {
		t.Errorf("--title ignored: %s", call)
	}
	body := prBody(t, call)
	if strings.Contains(body, "from the issue") {
		t.Errorf("flags did not override the issue sections:\n%s", body)
	}
	if !strings.Contains(body, "hand-written what") || !strings.Contains(body, "hand-written why") {
		t.Errorf("flag sections missing:\n%s", body)
	}
}

// A sub-issue's PR names the epic it belongs to; a standalone issue's does
// not invent one.
func TestPRLinksTheEpic(t *testing.T) {
	child := subIssueOf(claimed(30, "child"), 33)
	epic := issue(33, "Epic: the tree")
	epic.SubIssues = []model.Ref{{Number: 30, State: "OPEN"}}
	f := newFake(child, epic)
	app, _, _ := newApp(f)
	onBranch(app, "feat/x")

	if err := app.PR(context.Background(), PROpts{Sections: sections("", "", "t")}); err != nil {
		t.Fatal(err)
	}
	if body := prBody(t, createdPR(t, f)); !strings.Contains(body, "Part of #33") {
		t.Errorf("body has no epic link:\n%s", body)
	}
}

func TestPRForOverridesInference(t *testing.T) {
	f := newFake(claimed(30, "claimed one"), issue(31, "not claimed"))
	app, _, _ := newApp(f)
	onBranch(app, "feat/x")

	if err := app.PR(context.Background(), PROpts{For: 31, Sections: sections("", "", "t")}); err != nil {
		t.Fatal(err)
	}
	if body := prBody(t, createdPR(t, f)); !strings.Contains(body, "Fixes #31") {
		t.Errorf("--for was ignored:\n%s", body)
	}
}

func TestPRForRejectsUnknownIssue(t *testing.T) {
	f := newFake(claimed(30, "claimed one"))
	app, _, _ := newApp(f)
	onBranch(app, "feat/x")

	err := app.PR(context.Background(), PROpts{For: 999})
	requireExit(t, err, ExitGeneric, "not an open issue")
	assertNoPR(t, f)
}

func TestPRForRejectsAnEpic(t *testing.T) {
	epic := issue(33, "Epic: the tree")
	epic.SubIssues = []model.Ref{{Number: 30, State: "OPEN"}}
	f := newFake(epic, claimed(30, "child"))
	app, _, _ := newApp(f)
	onBranch(app, "feat/x")

	err := app.PR(context.Background(), PROpts{For: 33})
	requireExit(t, err, ExitGeneric, "epic")
	assertNoPR(t, f)
}

// Two claims are genuinely ambiguous, and a wrong Fixes line closes the
// wrong issue on merge — so the command names the candidates instead of
// guessing.
func TestPRRefusesWhenSeveralIssuesAreClaimed(t *testing.T) {
	f := newFake(claimed(30, "one"), claimed(31, "two"))
	app, _, _ := newApp(f)
	onBranch(app, "feat/ambiguous")

	err := app.PR(context.Background(), PROpts{})
	requireExit(t, err, ExitUsage, "--for")
	if !strings.Contains(err.Error(), "#30") || !strings.Contains(err.Error(), "#31") {
		t.Errorf("error does not name the candidates: %v", err)
	}
	assertNoPR(t, f)
}

// A number in the branch name is the tie-breaker when several are claimed.
func TestPRBranchNumberBreaksTheTie(t *testing.T) {
	f := newFake(claimed(30, "one"), claimed(31, "two"))
	app, _, _ := newApp(f)
	onBranch(app, "feat/31-two")

	if err := app.PR(context.Background(), PROpts{Sections: sections("", "", "t")}); err != nil {
		t.Fatal(err)
	}
	if body := prBody(t, createdPR(t, f)); !strings.Contains(body, "Fixes #31") {
		t.Errorf("branch number did not break the tie:\n%s", body)
	}
}

// #45: with one claim, a branch naming a different open issue is a
// disagreement between two signals, not a tie the claim wins. Resolving it
// silently composed a PR carrying the claimed issue's sections and its
// Fixes line, closing the wrong issue on merge.
func TestPRRefusesWhenTheBranchNamesADifferentOpenIssue(t *testing.T) {
	f := newFake(claimed(30, "the claim"), issue(2, "pagination"))
	app, _, _ := newApp(f)
	onBranch(app, "fix/pagination-2")

	err := app.PR(context.Background(), PROpts{})
	requireExit(t, err, ExitUsage, "--for")
	if !strings.Contains(err.Error(), "#2") || !strings.Contains(err.Error(), "#30") {
		t.Errorf("error does not name both candidates: %v", err)
	}
	assertNoPR(t, f)
}

// The refusal is a last resort: --for is what resolves it, and it must
// still be able to pick either candidate.
func TestPRForResolvesABranchClaimMismatch(t *testing.T) {
	f := newFake(claimed(30, "the claim"), issue(2, "pagination"))
	app, _, _ := newApp(f)
	onBranch(app, "fix/pagination-2")

	if err := app.PR(context.Background(), PROpts{For: 2, Sections: sections("", "", "t")}); err != nil {
		t.Fatal(err)
	}
	if body := prBody(t, createdPR(t, f)); !strings.Contains(body, "Fixes #2") {
		t.Errorf("--for did not link #2:\n%s", body)
	}
}

// A branch number matching the claim is agreement, not a mismatch, and a
// number naming nothing open is not a second candidate to disambiguate
// against — neither may block the ordinary path.
func TestPRAcceptsABranchNumberThatDoesNotContradictTheClaim(t *testing.T) {
	cases := map[string]string{
		"names the claimed issue": "fix/30-the-claim",
		"names no open issue":     "fix/pagination-77",
	}
	for name, branch := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(claimed(30, "the claim"))
			app, _, _ := newApp(f)
			onBranch(app, branch)

			if err := app.PR(context.Background(), PROpts{Sections: sections("", "", "t")}); err != nil {
				t.Fatal(err)
			}
			if body := prBody(t, createdPR(t, f)); !strings.Contains(body, "Fixes #30") {
				t.Errorf("branch %s did not link the claim:\n%s", branch, body)
			}
		})
	}
}

// An epic can never be a PR target, so a branch naming one is not a
// candidate worth refusing over — the claim stands.
func TestPRBranchNamingAnEpicDoesNotBlockTheClaim(t *testing.T) {
	epic := issue(33, "Epic: the tree")
	epic.SubIssues = []model.Ref{{Number: 30, State: "OPEN"}}
	f := newFake(epic, claimed(30, "the claim"))
	app, _, _ := newApp(f)
	onBranch(app, "feat/33-the-tree")

	if err := app.PR(context.Background(), PROpts{Sections: sections("", "", "t")}); err != nil {
		t.Fatal(err)
	}
	if body := prBody(t, createdPR(t, f)); !strings.Contains(body, "Fixes #30") {
		t.Errorf("epic-named branch should not have blocked the claim:\n%s", body)
	}
}

// A branch number that matches nothing claimed is not a tie-break, so the
// ambiguity stands rather than resolving to an unclaimed issue.
func TestPRBranchNumberDoesNotOverrideAmbiguityWhenUnclaimed(t *testing.T) {
	f := newFake(claimed(30, "one"), claimed(31, "two"), issue(99, "someone else's"))
	app, _, _ := newApp(f)
	onBranch(app, "feat/99-other")

	err := app.PR(context.Background(), PROpts{})
	requireExit(t, err, ExitUsage, "--for")
	assertNoPR(t, f)
}

// With nothing claimed the branch number is the only signal left.
func TestPRFallsBackToTheBranchNumberWhenNothingIsClaimed(t *testing.T) {
	f := newFake(issue(42, "unclaimed but referenced"))
	app, _, _ := newApp(f)
	onBranch(app, "fix/issue-42")

	if err := app.PR(context.Background(), PROpts{Sections: sections("", "", "t")}); err != nil {
		t.Fatal(err)
	}
	if body := prBody(t, createdPR(t, f)); !strings.Contains(body, "Fixes #42") {
		t.Errorf("branch fallback did not link #42:\n%s", body)
	}
}

// The branch-number fallback is subject to the same epic rule as every
// other path: resolving feat/33-x to epic #33 would write Fixes #33 and
// close the epic on merge.
func TestPRBranchNumberNamingAnEpicIsNotAFallback(t *testing.T) {
	epic := issue(33, "Epic: the tree")
	epic.SubIssues = []model.Ref{{Number: 30, State: "OPEN"}}
	f := newFake(epic, issue(30, "child, unclaimed"))
	app, _, _ := newApp(f)
	onBranch(app, "feat/33-the-tree")

	err := app.PR(context.Background(), PROpts{})
	requireExit(t, err, ExitUsage, "cannot tell which issue")
	assertNoPR(t, f)
}

// A branch number naming no open issue is not a target either: resolving
// it would open a PR titled "" that closes #0 on merge.
func TestPRBranchNumberNamingNoIssueIsNotAFallback(t *testing.T) {
	f := newFake(issue(30, "unrelated, unclaimed"))
	app, _, _ := newApp(f)
	onBranch(app, "feat/77-long-gone")

	err := app.PR(context.Background(), PROpts{})
	requireExit(t, err, ExitUsage, "cannot tell which issue")
	assertNoPR(t, f)
}

// branchIssueNumber decides whether a branch links an issue at all, and
// every wrong answer either mislinks a PR or drops a usable hint, so its
// rules are pinned directly rather than only through the paths above.
func TestBranchIssueNumber(t *testing.T) {
	tests := []struct {
		branch string
		want   int
	}{
		{"feat/30-pr-command", 30},
		{"fix/issue-42", 42},
		{"chore/bump_7", 7},
		{"docs/readme.12", 12},
		{"feat/pr-command", 0},
		{"main", 0},
		// A digit run inside a word is part of the name, not a reference.
		{"fix/http500-retries", 0},
		{"fix/v2-rewrite", 0},
		// #0 is not an issue, so a zero segment is no hint at all.
		{"feat/0-x", 0},
	}
	for _, tt := range tests {
		t.Run(tt.branch, func(t *testing.T) {
			got, ok := branchIssueNumber(tt.branch)
			if tt.want == 0 {
				if ok {
					t.Errorf("branchIssueNumber(%q) = %d, want no number", tt.branch, got)
				}
				return
			}
			if !ok || got != tt.want {
				t.Errorf("branchIssueNumber(%q) = %d, %v, want %d, true", tt.branch, got, ok, tt.want)
			}
		})
	}
}

// A digit run inside a word is part of the branch name, not a link: linking
// fix/500-error to #500 would close an unrelated issue on merge.
func TestPRIgnoresDigitsInsideBranchWords(t *testing.T) {
	f := newFake(issue(500, "unrelated issue"))
	app, _, _ := newApp(f)
	onBranch(app, "fix/http500-retries")

	err := app.PR(context.Background(), PROpts{})
	requireExit(t, err, ExitUsage, "cannot tell which issue")
	assertNoPR(t, f)
}

func TestPRRefusesWhenNothingIsClaimed(t *testing.T) {
	f := newFake(issue(30, "someone else's work"))
	app, _, _ := newApp(f)
	onBranch(app, "feat/mystery")

	err := app.PR(context.Background(), PROpts{})
	requireExit(t, err, ExitUsage, "cannot tell which issue")
	assertNoPR(t, f)
}

// An epic is never worked directly, so a claim on one is not a PR target.
func TestPRIgnoresAClaimedEpic(t *testing.T) {
	epic := claimed(33, "Epic: the tree")
	epic.SubIssues = []model.Ref{{Number: 30, State: "OPEN"}}
	f := newFake(epic, issue(30, "child"))
	app, _, _ := newApp(f)
	onBranch(app, "feat/x")

	err := app.PR(context.Background(), PROpts{})
	requireExit(t, err, ExitUsage, "cannot tell which issue")
	assertNoPR(t, f)
}

func TestPRRefusesAnUnpushedBranch(t *testing.T) {
	f := newFake(claimed(30, "one"))
	app, _, _ := newApp(f)
	app.Git = func() (git.State, error) {
		return git.State{Branch: "feat/local", Pushed: false}, nil
	}

	err := app.PR(context.Background(), PROpts{})
	requireExit(t, err, ExitGeneric, "git push -u origin feat/local")
	if len(f.calls) != 0 {
		t.Errorf("the API was called before the branch check: %v", f.calls)
	}
}

func TestPRRefusesOnTheDefaultBranch(t *testing.T) {
	f := newFake(claimed(30, "one"))
	app, _, _ := newApp(f)
	onBranch(app, "main")

	err := app.PR(context.Background(), PROpts{})
	requireExit(t, err, ExitGeneric, "a PR needs a branch")
	assertNoPR(t, f)
}

// A second PR from the same branch is the API's 422; naming the existing
// one is more useful than relaying that.
func TestPRRefusesWhenTheBranchAlreadyHasAPR(t *testing.T) {
	f := newFake(claimed(30, "one"))
	app, _, _ := newApp(f)
	onBranch(app, "feat/x")
	if err := app.PR(context.Background(), PROpts{Sections: sections("", "", "t")}); err != nil {
		t.Fatal(err)
	}

	err := app.PR(context.Background(), PROpts{Sections: sections("", "", "t")})
	requireExit(t, err, ExitGeneric, "already has an open PR")
	if !strings.Contains(err.Error(), "#501") {
		t.Errorf("error does not name the existing PR: %v", err)
	}
	if n := f.callCounts["CreatePullRequest"]; n != 1 {
		t.Errorf("CreatePullRequest ran %d times, want 1", n)
	}
}

func TestPRWarnsOnANonConventionalBranch(t *testing.T) {
	f := newFake(claimed(30, "one"))
	app, _, errOut := newApp(f)
	onBranch(app, "wip")

	if err := app.PR(context.Background(), PROpts{Sections: sections("", "", "t")}); err != nil {
		t.Fatal(err)
	}
	if got := errOut.String(); !strings.Contains(got, "wip") || !strings.Contains(got, "feat/") {
		t.Errorf("no branch-prefix warning: %q", got)
	}
	// The warning is advice, not a refusal — the work is already committed.
	createdPR(t, f)
}

func TestPRWarnsWhenTheIssueIsSomeoneElsesClaim(t *testing.T) {
	other := issue(30, "their work")
	other.Assignees = []string{"someone"}
	f := newFake(other)
	app, _, errOut := newApp(f)
	onBranch(app, "feat/30-their-work")

	if err := app.PR(context.Background(), PROpts{Sections: sections("", "", "t")}); err != nil {
		t.Fatal(err)
	}
	if got := errOut.String(); !strings.Contains(got, "claimed by @someone") {
		t.Errorf("no foreign-claim warning: %q", got)
	}
}

func TestPRWarnsWhenTestingIsOmitted(t *testing.T) {
	f := newFake(claimed(30, "one"))
	app, _, errOut := newApp(f)
	onBranch(app, "feat/x")

	if err := app.PR(context.Background(), PROpts{}); err != nil {
		t.Fatal(err)
	}
	if got := errOut.String(); !strings.Contains(got, "--testing") {
		t.Errorf("no missing-testing warning: %q", got)
	}
}

func TestPRReadyOpensForReview(t *testing.T) {
	f := newFake(claimed(30, "one"))
	app, out, _ := newApp(f)
	onBranch(app, "feat/x")

	if err := app.PR(context.Background(), PROpts{Ready: true, Sections: sections("", "", "t")}); err != nil {
		t.Fatal(err)
	}
	if call := createdPR(t, f); !strings.Contains(call, "draft=false") {
		t.Errorf("--ready still opened a draft: %s", call)
	}
	if got := out.String(); strings.Contains(got, "draft") {
		t.Errorf("output called a ready PR a draft: %q", got)
	}
}

func TestPRBaseOverride(t *testing.T) {
	f := newFake(claimed(30, "one"))
	app, _, _ := newApp(f)
	onBranch(app, "feat/x")

	if err := app.PR(context.Background(), PROpts{Base: "release/1.x", Sections: sections("", "", "t")}); err != nil {
		t.Fatal(err)
	}
	if call := createdPR(t, f); !strings.Contains(call, "feat/x→release/1.x") {
		t.Errorf("--base ignored: %s", call)
	}
}

func TestPRReportsAMissingDefaultBranch(t *testing.T) {
	f := newFake(claimed(30, "one"))
	f.defaultBranch = ""
	app, _, _ := newApp(f)
	onBranch(app, "feat/x")

	err := app.PR(context.Background(), PROpts{})
	requireExit(t, err, ExitGeneric, "--base")
	assertNoPR(t, f)
}

func TestPRBodyFileAppendsTheFixesTrailer(t *testing.T) {
	path := writeTemp(t, "long-form body\n")
	f := newFake(claimed(30, "one"))
	app, _, _ := newApp(f)
	onBranch(app, "feat/x")

	if err := app.PR(context.Background(), PROpts{BodyFile: path}); err != nil {
		t.Fatal(err)
	}
	body := prBody(t, createdPR(t, f))
	if body != "long-form body\n\nFixes #30" {
		t.Errorf("body = %q", body)
	}
}

// A body that already closes the right issue is left alone: appending a
// second trailer would be a duplicate link.
func TestPRBodyFileKeepsAnExistingFixes(t *testing.T) {
	path := writeTemp(t, "narrative\n\nCloses #30\n")
	f := newFake(claimed(30, "one"))
	app, _, _ := newApp(f)
	onBranch(app, "feat/x")

	if err := app.PR(context.Background(), PROpts{BodyFile: path}); err != nil {
		t.Fatal(err)
	}
	body := prBody(t, createdPR(t, f))
	if strings.Contains(body, "Fixes #30") {
		t.Errorf("a second closing link was appended: %q", body)
	}
	if !strings.Contains(body, "Closes #30") {
		t.Errorf("the existing link was dropped: %q", body)
	}
}

// The escape hatch appends every link the template would have written, not
// just the closing one: a sub-issue's hand-written body still names its
// epic.
func TestPRBodyFileAppendsTheEpicTrailer(t *testing.T) {
	path := writeTemp(t, "long-form body\n")
	f := newFake(subIssueOf(claimed(30, "one"), 33))
	app, _, _ := newApp(f)
	onBranch(app, "feat/x")

	if err := app.PR(context.Background(), PROpts{BodyFile: path}); err != nil {
		t.Fatal(err)
	}
	if body := prBody(t, createdPR(t, f)); body != "long-form body\n\nFixes #30\nPart of #33" {
		t.Errorf("body = %q", body)
	}
}

// Links the author already wrote are never duplicated, on either trailer.
func TestPRBodyFileKeepsExistingTrailers(t *testing.T) {
	path := writeTemp(t, "narrative\n\nCloses #30\nPart of #33\n")
	f := newFake(subIssueOf(claimed(30, "one"), 33))
	app, _, _ := newApp(f)
	onBranch(app, "feat/x")

	if err := app.PR(context.Background(), PROpts{BodyFile: path}); err != nil {
		t.Fatal(err)
	}
	if body := prBody(t, createdPR(t, f)); body != "narrative\n\nCloses #30\nPart of #33" {
		t.Errorf("body = %q, want it untouched", body)
	}
}

// The two trailers are independent: a sub-issue whose body already closes
// its issue still gains the epic link it didn't write.
func TestPRBodyFileAppendsTheEpicTrailerAlone(t *testing.T) {
	path := writeTemp(t, "narrative\n\nCloses #30\n")
	f := newFake(subIssueOf(claimed(30, "one"), 33))
	app, _, _ := newApp(f)
	onBranch(app, "feat/x")

	if err := app.PR(context.Background(), PROpts{BodyFile: path}); err != nil {
		t.Fatal(err)
	}
	if body := prBody(t, createdPR(t, f)); body != "narrative\n\nCloses #30\n\nPart of #33" {
		t.Errorf("body = %q", body)
	}
}

func TestPRBodyFileRejectsAMismatchedFixes(t *testing.T) {
	path := writeTemp(t, "Fixes #99\n")
	f := newFake(claimed(30, "one"))
	app, _, _ := newApp(f)
	onBranch(app, "feat/x")

	err := app.PR(context.Background(), PROpts{BodyFile: path})
	requireExit(t, err, ExitUsage, "closes #99 but this PR is for #30")
	assertNoPR(t, f)
}

func TestPRBodyFileRejectsSeveralFixes(t *testing.T) {
	path := writeTemp(t, "Fixes #30\nCloses #31\n")
	f := newFake(claimed(30, "one"))
	app, _, _ := newApp(f)
	onBranch(app, "feat/x")

	err := app.PR(context.Background(), PROpts{BodyFile: path})
	requireExit(t, err, ExitUsage, "closes exactly one")
	assertNoPR(t, f)
}

func TestPRBodyFileMissing(t *testing.T) {
	f := newFake(claimed(30, "one"))
	app, _, _ := newApp(f)
	onBranch(app, "feat/x")

	err := app.PR(context.Background(), PROpts{BodyFile: filepath.Join(t.TempDir(), "nope.md")})
	requireExit(t, err, ExitGeneric, "cannot read --body-file")
	assertNoPR(t, f)
}

func TestPRBodyFileAndSectionsAreExclusive(t *testing.T) {
	f := newFake(claimed(30, "one"))
	app, _, _ := newApp(f)
	onBranch(app, "feat/x")

	err := app.PR(context.Background(), PROpts{BodyFile: "x.md", Sections: sections("a", "", "")})
	requireExit(t, err, ExitUsage, "mutually exclusive")
	if len(f.calls) != 0 {
		t.Errorf("the API was called despite a usage error: %v", f.calls)
	}
}

func TestPRWithoutAGitHook(t *testing.T) {
	f := newFake(claimed(30, "one"))
	app, _, _ := newApp(f)

	err := app.PR(context.Background(), PROpts{})
	requireExit(t, err, ExitGeneric, "git checkout")
}

func TestPRReportsAGitFailure(t *testing.T) {
	f := newFake(claimed(30, "one"))
	app, _, _ := newApp(f)
	app.Git = func() (git.State, error) { return git.State{}, errors.New("not a git repository") }

	err := app.PR(context.Background(), PROpts{})
	requireExit(t, err, ExitGeneric, "not a git repository")
}

func TestPRPropagatesAPIFailures(t *testing.T) {
	for _, method := range []string{"ListIssues", "Viewer", "PullRequestContext", "CreatePullRequest"} {
		t.Run(method, func(t *testing.T) {
			f := newFake(claimed(30, "one"))
			f.failOn[method] = errors.New("boom")
			app, _, _ := newApp(f)
			onBranch(app, "feat/x")

			err := app.PR(context.Background(), PROpts{Sections: sections("", "", "t")})
			if err == nil {
				t.Fatalf("%s failure was swallowed", method)
			}
			// The cause must survive, not just the failure: an agent branches
			// on what the message says, so wrapping it in a generic "could
			// not open PR" would be a regression this must catch.
			if !strings.Contains(err.Error(), "boom") {
				t.Errorf("err = %q, want it to carry the %s failure", err, method)
			}
		})
	}
}

func TestPRJSON(t *testing.T) {
	f := newFake(claimed(30, "PR creation command"))
	app, out, _ := newApp(f)
	app.JSON = true
	onBranch(app, "feat/pr-command")

	if err := app.PR(context.Background(), PROpts{Sections: sections("", "", "t")}); err != nil {
		t.Fatal(err)
	}
	var got prJSON
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output is not JSON: %q: %v", out.String(), err)
	}
	want := prJSON{
		Number: 501, URL: "https://github.com/o/r/pull/501", Draft: true,
		Fixes: 30, Head: "feat/pr-command", Base: "main", Title: "PR creation command",
	}
	if got != want {
		t.Errorf("json = %+v, want %+v", got, want)
	}
}

// --- helpers ---

func sections(what, why, testing string) conventions.PRSections {
	return conventions.PRSections{What: what, Why: why, Testing: testing}
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// prBody pulls the body out of a recorded CreatePullRequest call, undoing
// the %q the fake records it with so assertions read as real markdown.
func prBody(t *testing.T, call string) string {
	t.Helper()
	_, quoted, ok := strings.Cut(call, " body=")
	if !ok {
		t.Fatalf("call has no body: %s", call)
	}
	body, err := strconv.Unquote(quoted)
	if err != nil {
		t.Fatalf("body is not a quoted string: %s: %v", quoted, err)
	}
	return body
}

func assertNoPR(t *testing.T, f *fakeClient) {
	t.Helper()
	if n := f.callCounts["CreatePullRequest"]; n != 0 {
		t.Errorf("a PR was created despite the refusal (%d calls)", n)
	}
}

func requireExit(t *testing.T, err error, code int, substr string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error containing %q", substr)
	}
	var exit *ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("err = %v (%T), want *ExitError", err, err)
	}
	if exit.Code != code {
		t.Errorf("exit code = %d, want %d (%v)", exit.Code, code, err)
	}
	if !strings.Contains(err.Error(), substr) {
		t.Errorf("err = %q, want it to contain %q", err, substr)
	}
}
