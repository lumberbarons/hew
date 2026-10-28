package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/internal/conventions"
	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/git"
	"github.com/lumberbarons/hew/internal/model"
)

// onBranch wires an App's Git hook to a fixed, pushed branch — the normal
// precondition, so tests that aren't about git state say it in one word.
func onBranch(app *App, branch string) {
	app.Git = func() (git.State, error) {
		return git.State{Branch: branch, Pushed: true}, nil
	}
}

// inWorktree wires the state an agent harness leaves behind: the checkout is
// on a generated local branch name, and the branch it tracks on the remote
// keeps the real one.
func inWorktree(app *App, local, upstream string) {
	app.Git = func() (git.State, error) {
		return git.State{Branch: local, Upstream: upstream, Pushed: true}, nil
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
	if !strings.Contains(call, `"feat: PR creation command"`) {
		t.Errorf("PR title did not default to the prefixed issue title: %s", call)
	}
	if !strings.Contains(call, "Fixes #30") {
		t.Errorf("PR body has no Fixes trailer: %s", call)
	}
	// Both lines, exactly: the summary an agent branches on and the URL a
	// human clicks. Asserted whole so dropping either one fails here.
	want := "created draft PR #501 for #30: feat: PR creation command\n" +
		"https://github.com/o/r/pull/501\n"
	if got := out.String(); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// #44: a squash merge makes the PR title the commit subject, and the
// changelog is grouped by conventional-commit prefix, so the default title
// derives its prefix from the type label the issue already carries.
func TestPRTitleDerivesThePrefixFromTheIssueType(t *testing.T) {
	cases := map[string]string{
		"enhancement": "feat: the work",
		"bug":         "fix: the work",
		"task":        "chore: the work",
	}
	for typ, want := range cases {
		t.Run(typ, func(t *testing.T) {
			f := newFake(claimed(30, "the work", "P2", typ))
			app, _, _ := newApp(f)
			onBranch(app, "feat/x")

			if err := app.PR(context.Background(), PROpts{Sections: sections("", "", "t")}); err != nil {
				t.Fatal(err)
			}
			if call := createdPR(t, f); !strings.Contains(call, `"`+want+`"`) {
				t.Errorf("title for a %s: %s, want %q", typ, call, want)
			}
		})
	}
}

// An untyped issue — anything filed outside the tool — has nothing to derive
// a prefix from, and inventing one would file the work under the wrong
// changelog heading. The title goes through as-is.
func TestPRTitleOfAnUntypedIssueIsUnprefixed(t *testing.T) {
	f := newFake(claimed(30, "drive-by report", "P2"))
	app, _, _ := newApp(f)
	onBranch(app, "fix/x")

	if err := app.PR(context.Background(), PROpts{Sections: sections("", "", "t")}); err != nil {
		t.Fatal(err)
	}
	if call := createdPR(t, f); !strings.Contains(call, `"drive-by report"`) {
		t.Errorf("untyped issue got an invented prefix: %s", call)
	}
}

// --title is the author's word, prefixed or not: doubling a prefix onto it
// would corrupt the subject just as surely as omitting one.
func TestPRTitleFlagIsPassedThroughUnchanged(t *testing.T) {
	for _, want := range []string{"feat: hand-written", "hand-written, unprefixed"} {
		t.Run(want, func(t *testing.T) {
			f := newFake(claimed(30, "the work", "P2", "enhancement"))
			app, _, _ := newApp(f)
			onBranch(app, "feat/x")

			if err := app.PR(context.Background(), PROpts{Title: want, Sections: sections("", "", "t")}); err != nil {
				t.Fatal(err)
			}
			if call := createdPR(t, f); !strings.Contains(call, `"`+want+`"`) {
				t.Errorf("--title %q was rewritten: %s", want, call)
			}
		})
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

// Risk is the merge claim a reviewer weighs after reading the change; it sits
// after Testing and is optional on a draft.
func TestPRComposesTheRiskSection(t *testing.T) {
	f := newFake(claimed(30, "PR creation command", "P2", "enhancement"))
	app, _, errOut := newApp(f)
	onBranch(app, "feat/pr-command")

	err := app.PR(context.Background(), PROpts{
		Ready: true,
		Sections: conventions.PRSections{
			Testing: "Before: `TestX` failed. After: it passes.",
			Risk:    "Two-way door; blast radius: the pr command.",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := prBody(t, createdPR(t, f))
	if !strings.Contains(body, "### Risk\n\nTwo-way door; blast radius: the pr command.") {
		t.Errorf("body has no Risk section:\n%s", body)
	}
	if strings.Index(body, "### Testing") > strings.Index(body, "### Risk") {
		t.Errorf("Risk did not follow Testing:\n%s", body)
	}
	if strings.Contains(errOut.String(), "no Risk section") {
		t.Errorf("warned about a supplied Risk section: %q", errOut.String())
	}
}

// A draft may be incomplete; opening it for review is the moment to ask for
// the merge claim.
func TestPRReadyWarnsAboutMissingRisk(t *testing.T) {
	f := newFake(claimed(30, "work", "P2", "enhancement"))
	app, _, errOut := newApp(f)
	onBranch(app, "feat/30-work")

	err := app.PR(context.Background(), PROpts{
		Ready:    true,
		Sections: sections("what", "why", "Before: failed. After: passes."),
	})
	if err != nil {
		t.Fatal(err)
	}
	warning := errOut.String()
	for _, want := range []string{"no Risk section", "Reversibility:", "one-way or two-way", "Blast radius:"} {
		if !strings.Contains(warning, want) {
			t.Errorf("warning %q does not name %q", warning, want)
		}
	}
}

func TestPRDraftDoesNotWarnAboutMissingRisk(t *testing.T) {
	f := newFake(claimed(30, "work", "P2", "enhancement"))
	app, _, errOut := newApp(f)
	onBranch(app, "feat/30-work")

	if err := app.PR(context.Background(), PROpts{
		Sections: sections("what", "why", "Before: failed. After: passes."),
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(errOut.String(), "no Risk section") {
		t.Errorf("draft warned about missing risk: %q", errOut.String())
	}
}

// --body-file is the escape hatch, not an exemption from the body contract.
func TestPRBodyFileWarnsAboutMissingTesting(t *testing.T) {
	f := newFake(claimed(30, "work", "P2", "enhancement"))
	app, _, errOut := newApp(f)
	onBranch(app, "feat/30-work")

	if err := app.PR(context.Background(), PROpts{BodyFile: writeTemp(t, "narrative")}); err != nil {
		t.Fatal(err)
	}
	warning := errOut.String()
	for _, want := range []string{"no Testing section", "Before:", "After:", "own line", "claim, not evidence"} {
		if !strings.Contains(warning, want) {
			t.Errorf("warning %q does not name %q", warning, want)
		}
	}
}

func TestPRBodyFileWithTestingDoesNotWarn(t *testing.T) {
	f := newFake(claimed(30, "work", "P2", "enhancement"))
	app, _, errOut := newApp(f)
	onBranch(app, "feat/30-work")

	body := "### Testing\n\nBefore: failed. After: passes.\n\nFixes #30"
	if err := app.PR(context.Background(), PROpts{BodyFile: writeTemp(t, body)}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(errOut.String(), "no Testing section") {
		t.Errorf("warned about supplied Testing: %q", errOut.String())
	}
}

func TestPRFlagsOverrideTheIssueSections(t *testing.T) {
	i := claimed(30, "PR creation command")
	i.Body = "### Goal\n\nfrom the issue\n\n### Approach\n\nalso from the issue"
	f := newFake(i)
	app, _, _ := newApp(f)
	onBranch(app, "feat/pr-command")

	if err := app.PR(context.Background(), PROpts{
		Title: "feat: hew pr", Sections: sections("hand-written what", "hand-written why", "go test"),
	}); err != nil {
		t.Fatal(err)
	}
	call := createdPR(t, f)
	if !strings.Contains(call, `"feat: hew pr"`) {
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

	if err := app.PR(context.Background(), PROpts{For: []int{31}, Sections: sections("", "", "t")}); err != nil {
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

	err := app.PR(context.Background(), PROpts{For: []int{999}})
	requireExit(t, err, ExitGeneric, "not an open issue")
	assertNoPR(t, f)
}

func TestPRForRejectsAnEpic(t *testing.T) {
	epic := issue(33, "Epic: the tree")
	epic.SubIssues = []model.Ref{{Number: 30, State: "OPEN"}}
	f := newFake(epic, claimed(30, "child"))
	app, _, _ := newApp(f)
	onBranch(app, "feat/x")

	err := app.PR(context.Background(), PROpts{For: []int{33}})
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

	if err := app.PR(context.Background(), PROpts{For: []int{2}, Sections: sections("", "", "t")}); err != nil {
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

// GitHub knows the branch by its upstream name only, so a PR opened from the
// worktree's local name is rejected with "PullRequest.head is invalid" (#65).
// Both the create and the existing-PR lookup go out under the resolved name.
func TestPRFromAWorktreeOpensFromTheUpstreamBranch(t *testing.T) {
	f := newFake(claimed(30, "one"))
	app, _, errOut := newApp(f)
	inWorktree(app, "worktree-fix+pr-head", "fix/pr-head")

	if err := app.PR(context.Background(), PROpts{Sections: sections("", "", "t")}); err != nil {
		t.Fatal(err)
	}
	if call := createdPR(t, f); !strings.Contains(call, "fix/pr-head→main") {
		t.Errorf("PR head is not the upstream branch: %s", call)
	}
	if !slices.Contains(f.calls, "PullRequestContext fix/pr-head") {
		t.Errorf("existing-PR lookup did not use the upstream branch: %v", f.calls)
	}
	for _, call := range f.calls {
		if strings.Contains(call, "worktree-fix+pr-head") {
			t.Errorf("the local branch name reached the API: %s", call)
		}
	}
	// fix/pr-head carries a conventional prefix even though the local name
	// does not, so there is nothing to warn about.
	if got := errOut.String(); strings.Contains(got, "prefix") {
		t.Errorf("warned about the local name's missing prefix: %q", got)
	}
}

// The prefix warning is about the name reviewers and the changelog see,
// which is the upstream one — a local name that happens to look conventional
// does not excuse an unprefixed branch on the remote.
func TestPRWarnsAgainstTheUpstreamBranchName(t *testing.T) {
	f := newFake(claimed(30, "one"))
	app, _, errOut := newApp(f)
	inWorktree(app, "feat/looks-fine", "wip")

	if err := app.PR(context.Background(), PROpts{Sections: sections("", "", "t")}); err != nil {
		t.Fatal(err)
	}
	if got := errOut.String(); !strings.Contains(got, "branch wip has no") {
		t.Errorf("prefix warning did not name the upstream branch: %q", got)
	}
}

// The push advice is the one place the local name is the right answer: it is
// what the user has checked out and what they would type.
func TestPRUnpushedAdviceNamesTheLocalBranch(t *testing.T) {
	f := newFake(claimed(30, "one"))
	app, _, _ := newApp(f)
	app.Git = func() (git.State, error) {
		return git.State{Branch: "worktree-feat+x", Pushed: false}, nil
	}

	err := app.PR(context.Background(), PROpts{})
	requireExit(t, err, ExitGeneric, "git push -u origin worktree-feat+x")
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

// #107: the claim warning prints GitHub-derived logins outside the renderer.
func TestPRClaimWarningNeutralizesHostileAssignee(t *testing.T) {
	other := issue(30, "their work")
	other.Assignees = []string{"someone" + hostile}
	f := newFake(other)
	app, _, errOut := newApp(f)
	onBranch(app, "feat/30-their-work")

	if err := app.PR(context.Background(), PROpts{Sections: sections("", "", "t")}); err != nil {
		t.Fatal(err)
	}
	assertNeutralized(t, "foreign-claim warning", errOut.String())
	if !strings.Contains(errOut.String(), "claimed by @someone") {
		t.Errorf("sanitizing dropped the login text: %q", errOut.String())
	}
}

// #107: the derived title carries the issue's own (GitHub-derived) text, and
// the success line prints it outside the renderer. The API call and --json
// still get the raw title — only the printed line is neutralized.
func TestPRCreationLineNeutralizesHostileTitle(t *testing.T) {
	f := newFake(claimed(30, "Work "+hostile, "P2", "enhancement"))
	app, out, _ := newApp(f)
	onBranch(app, "feat/x")

	if err := app.PR(context.Background(), PROpts{Sections: sections("", "", "t")}); err != nil {
		t.Fatal(err)
	}
	assertNeutralized(t, "PR creation line", out.String())
	if !strings.Contains(out.String(), "created draft PR #501 for #30: feat: Work ") {
		t.Errorf("sanitizing dropped the title text: %q", out.String())
	}
	want := fmt.Sprintf("%q", "feat: Work "+hostile)
	if call := createdPR(t, f); !strings.Contains(call, want) {
		t.Errorf("sanitizing the printed line corrupted the API call's title: %s", call)
	}
}

func TestPRWarnsWhenTestingIsOmitted(t *testing.T) {
	f := newFake(claimed(30, "one"))
	app, _, errOut := newApp(f)
	onBranch(app, "feat/x")

	if err := app.PR(context.Background(), PROpts{}); err != nil {
		t.Fatal(err)
	}
	got := errOut.String()
	for _, want := range []string{"no Testing section", "Before:", "After:"} {
		if !strings.Contains(got, want) {
			t.Errorf("warning %q does not name %q", got, want)
		}
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
		Fixes: 30, FixedIssues: []int{30}, Head: "feat/pr-command", Base: "main", Title: "PR creation command",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("json = %+v, want %+v", got, want)
	}
}

// --- several declared issues (#134) ---

// multiSetup is two claimed issues on a branch that names neither, the shape
// of a deliberate combined PR.
func multiSetup(labels ...string) (*fakeClient, *App, *bytes.Buffer) {
	f := newFake(claimed(46, "first", labels...), claimed(50, "second", labels...))
	app, _, errOut := newApp(f)
	onBranch(app, "feat/combined")
	return f, app, errOut
}

func TestPRRepeatedForClosesTheDeclaredSet(t *testing.T) {
	f, app, _ := multiSetup()

	if err := app.PR(context.Background(), PROpts{For: []int{46, 50}, Sections: sections("w", "y", "t")}); err != nil {
		t.Fatal(err)
	}
	body := prBody(t, createdPR(t, f))
	if !strings.HasSuffix(body, "Fixes #46\nFixes #50") {
		t.Errorf("body does not close the declared set in order:\n%s", body)
	}
}

func TestPRRepeatedForCollapsesDuplicates(t *testing.T) {
	f, app, _ := multiSetup()

	if err := app.PR(context.Background(), PROpts{For: []int{46, 46}, Sections: sections("w", "y", "t")}); err != nil {
		t.Fatal(err)
	}
	if body := prBody(t, createdPR(t, f)); strings.Count(body, "Fixes #46") != 1 || strings.Contains(body, "#50") {
		t.Errorf("duplicate --for was not collapsed:\n%s", body)
	}
}

// Multiple issues are declared, never inferred: two claims with a branch
// number still resolve to exactly the one the branch names.
func TestPRInferenceStillResolvesExactlyOne(t *testing.T) {
	f, app, _ := multiSetup()
	onBranch(app, "feat/46-first")

	if err := app.PR(context.Background(), PROpts{Sections: sections("w", "y", "t")}); err != nil {
		t.Fatal(err)
	}
	if body := prBody(t, createdPR(t, f)); !strings.HasSuffix(body, "Fixes #46") || strings.Contains(body, "#50") {
		t.Errorf("inference widened beyond one issue:\n%s", body)
	}
}

func TestPRRepeatedForRejectsAnyUnusableIssue(t *testing.T) {
	epic := issue(33, "Epic: the tree")
	epic.SubIssues = []model.Ref{{Number: 46, State: "OPEN"}}
	for name, tc := range map[string]struct {
		forNums []int
		want    string
	}{
		"missing": {[]int{46, 999}, "#999 is not an open issue"},
		"epic":    {[]int{46, 33}, "epic"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(epic, claimed(46, "first"), claimed(50, "second"))
			app, _, _ := newApp(f)
			onBranch(app, "feat/combined")

			err := app.PR(context.Background(), PROpts{For: tc.forNums})
			requireExit(t, err, ExitGeneric, tc.want)
			assertNoPR(t, f)
		})
	}
}

func TestPRWarnsWhenADeclaredIssueIsNotYoursOrClaimed(t *testing.T) {
	other := claimed(50, "second")
	other.Assignees = []string{"someone"}
	f := newFake(claimed(46, "first"), other, issue(51, "third"))
	app, _, errOut := newApp(f)
	onBranch(app, "feat/combined")

	if err := app.PR(context.Background(), PROpts{For: []int{46, 50, 51}, Sections: sections("w", "y", "t")}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"#50 is claimed by @someone, not you", "#51 is not claimed by you"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("stderr missing %q: %q", want, errOut.String())
		}
	}
	if strings.Contains(errOut.String(), "#46 is") {
		t.Errorf("warned about the issue the viewer holds: %q", errOut.String())
	}
}

func TestPRWarnsWhenDeclaredIssuesLookUnrelated(t *testing.T) {
	_, app, errOut := multiSetup()
	if err := app.PR(context.Background(), PROpts{For: []int{46, 50}, Sections: sections("w", "y", "t")}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "issues #46 | #50 look unrelated") {
		t.Errorf("no unrelated warning: %q", errOut.String())
	}
}

func TestPRStaysQuietWhenDeclaredIssuesAreRelated(t *testing.T) {
	blocked := claimed(51, "third", "cli")
	blocked.BlockedBy = []model.Ref{{Number: 50, State: "OPEN"}}
	for name, issues := range map[string][]*model.Issue{
		"shared area":   {claimed(46, "first", "cli"), claimed(50, "second", "cli")},
		"shared parent": {subIssueOf(claimed(46, "first"), 33), subIssueOf(claimed(50, "second"), 33)},
		"dependency":    {claimed(50, "second"), withBlocker(claimed(46, "first"), 50)},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(issues...)
			app, _, errOut := newApp(f)
			onBranch(app, "feat/combined")
			if err := app.PR(context.Background(), PROpts{For: []int{46, 50}, Sections: sections("w", "y", "t")}); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(errOut.String(), "unrelated") {
				t.Errorf("warned about related issues: %q", errOut.String())
			}
		})
	}
	// The relation only has to connect the set: 51 hangs off 50, which shares
	// an area with 46.
	f := newFake(claimed(46, "first", "cli"), claimed(50, "second", "cli"), blocked)
	app, _, errOut := newApp(f)
	onBranch(app, "feat/combined")
	if err := app.PR(context.Background(), PROpts{For: []int{46, 50, 51}, Sections: sections("w", "y", "t")}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(errOut.String(), "unrelated") {
		t.Errorf("warned about a connected set: %q", errOut.String())
	}
}

func withBlocker(i *model.Issue, blocker int) *model.Issue {
	i.BlockedBy = []model.Ref{{Number: blocker, State: "OPEN"}}
	return i
}

func TestPRSingleIssueNeverWarnsUnrelated(t *testing.T) {
	_, app, errOut := multiSetup()
	if err := app.PR(context.Background(), PROpts{For: []int{46}, Sections: sections("w", "y", "t")}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(errOut.String(), "unrelated") {
		t.Errorf("single issue drew a relatedness warning: %q", errOut.String())
	}
}

// One issue's Fix says nothing true about a PR that also fixes another, so a
// set does not borrow the first issue's text.
func TestPRRepeatedForDoesNotDefaultSectionsFromOneIssue(t *testing.T) {
	first := claimed(46, "first")
	first.Body = "### Approach\n\nOnly the first issue's approach\n\n### Goal\n\nOnly the first issue's goal"
	f := newFake(first, claimed(50, "second"))
	app, _, errOut := newApp(f)
	onBranch(app, "feat/combined")

	if err := app.PR(context.Background(), PROpts{For: []int{46, 50}, Sections: sections("", "", "t")}); err != nil {
		t.Fatal(err)
	}
	if body := prBody(t, createdPR(t, f)); strings.Contains(body, "first issue's") {
		t.Errorf("a set borrowed one issue's text:\n%s", body)
	}
	if !strings.Contains(errOut.String(), "--what") || !strings.Contains(errOut.String(), "--why") {
		t.Errorf("no prompt to supply What/Why: %q", errOut.String())
	}
}

func TestPRRepeatedForSharesAnEpicTrailer(t *testing.T) {
	f := newFake(subIssueOf(claimed(46, "first"), 33), subIssueOf(claimed(50, "second"), 33))
	app, _, _ := newApp(f)
	onBranch(app, "feat/combined")

	if err := app.PR(context.Background(), PROpts{For: []int{46, 50}, Sections: sections("w", "y", "t")}); err != nil {
		t.Fatal(err)
	}
	if body := prBody(t, createdPR(t, f)); !strings.HasSuffix(body, "Fixes #46\nFixes #50\nPart of #33") {
		t.Errorf("trailers = %q", body)
	}
}

func TestPRBodyFileMustMatchTheDeclaredSet(t *testing.T) {
	for name, tc := range map[string]struct {
		body    string
		want    string // error substring; empty means accepted
		trailer string // what the composed body must end with
	}{
		"equal":           {"narrative\n\nFixes #46\nFixes #50", "", "Fixes #46\nFixes #50"},
		"equal reordered": {"narrative\n\nCloses #50\nFixes #46", "", "Closes #50\nFixes #46"},
		"none gets all":   {"narrative", "", "narrative\n\nFixes #46\nFixes #50"},
		"drops one":       {"narrative\n\nFixes #46", "closes #46 but this PR is for #46 #50", ""},
		"adds one":        {"Fixes #46\nFixes #50\nFixes #51", "closes #46 #50 #51 but this PR is for #46 #50", ""},
		"changes one":     {"Fixes #46\nFixes #99", "closes #46 #99 but this PR is for #46 #50", ""},
		"repeats one":     {"Fixes #46\nFixes #46\nFixes #50", "closes #46 #46 #50 but this PR is for #46 #50", ""},
	} {
		t.Run(name, func(t *testing.T) {
			f, app, _ := multiSetup()
			err := app.PR(context.Background(), PROpts{For: []int{46, 50}, BodyFile: writeTemp(t, tc.body)})
			if tc.want != "" {
				requireExit(t, err, ExitUsage, tc.want)
				assertNoPR(t, f)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if body := prBody(t, createdPR(t, f)); !strings.HasSuffix(body, tc.trailer) || strings.Count(strings.ToLower(body), "#46") != 1 {
				t.Errorf("body = %q", body)
			}
		})
	}
}

func TestPRJSONListsEveryDeclaredIssue(t *testing.T) {
	f, app, _ := multiSetup()
	app.JSON = true
	out := app.Out.(*bytes.Buffer)
	if err := app.PR(context.Background(), PROpts{For: []int{50, 46}, Sections: sections("w", "y", "t")}); err != nil {
		t.Fatal(err)
	}
	createdPR(t, f)
	var got prJSON
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output is not JSON: %q: %v", out.String(), err)
	}
	if got.Fixes != 50 || !slices.Equal(got.FixedIssues, []int{50, 46}) {
		t.Errorf("fixes = %d, fixedIssues = %v", got.Fixes, got.FixedIssues)
	}
}

func TestPRAmendHonoursTheDeclaredSet(t *testing.T) {
	setup := func(body string) (*fakeClient, *App) {
		f := newFake(claimed(46, "first"), claimed(50, "second"))
		f.openPRs["feat/combined"] = gh.PullRequest{
			Number: 501, URL: "https://github.com/o/r/pull/501", Draft: true,
			Title: "feat: combined", Base: "main", HeadRepository: "o/r", Body: body,
		}
		app, _, _ := newApp(f)
		onBranch(app, "feat/combined")
		return f, app
	}

	t.Run("revises a section and keeps the set", func(t *testing.T) {
		f, app := setup("### What\n\nOriginal what\n\n### Testing\n\nOld tests\n\nFixes #46\nFixes #50")
		opts := PROpts{Amend: true, For: []int{46, 50}, Sections: conventions.PRSections{Testing: "New tests"}}
		if err := app.PR(context.Background(), opts); err != nil {
			t.Fatal(err)
		}
		want := "### What\n\nOriginal what\n\n### Testing\n\nNew tests\n\nFixes #46\nFixes #50"
		if got := f.openPRs["feat/combined"].Body; got != want {
			t.Errorf("body = %q, want %q", got, want)
		}
	})
	t.Run("refuses a body that closes a different set", func(t *testing.T) {
		f, app := setup("### Testing\n\nOld tests\n\nFixes #46")
		err := app.PR(context.Background(), PROpts{Amend: true, For: []int{46, 50}, Title: "feat: new"})
		requireExit(t, err, ExitUsage, "closes #46 but this PR is for #46 #50")
		assertPRReadsOnly(t, f)
	})
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

// The convention lives in help text, which the agent composing a body has
// generally not read. So the tool says it at the moment the body is
// written: the check names the tokens and the remedy rather than the rule.
func TestPRWarnsAboutUnmarkedCodeText(t *testing.T) {
	f := newFake(claimed(30, "PR creation command", "P2", "enhancement"))
	app, _, errOut := newApp(f)
	onBranch(app, "feat/pr-command")

	err := app.PR(context.Background(), PROpts{
		Sections: sections("Rewrites internal/cli/pr.go.", "", "go test -race ./... is green"),
	})
	if err != nil {
		t.Fatal(err)
	}
	warning := errOut.String()
	for _, want := range []string{"internal/cli/pr.go", "./...", "hew pr --amend"} {
		if !strings.Contains(warning, want) {
			t.Errorf("warning %q does not name %q", warning, want)
		}
	}
	// The body itself is untouched — the author fixes it, not the tool.
	if body := createdPR(t, f); !strings.Contains(body, "Rewrites internal/cli/pr.go.") {
		t.Errorf("pr rewrote the author's text: %s", body)
	}
}

// --body-file is the escape hatch for a body the sections cannot express,
// not an escape from the convention: it composes a body the same way and is
// checked the same way. Its own warn call is separate from the sections
// one, so it needs its own case.
func TestPRWarnsAboutUnmarkedCodeTextInABodyFile(t *testing.T) {
	path := writeTemp(t, "Touch internal/cli/pr.go and run go test ./..., then check\n")
	f := newFake(claimed(30, "one"))
	app, _, errOut := newApp(f)
	onBranch(app, "feat/x")

	if err := app.PR(context.Background(), PROpts{BodyFile: path}); err != nil {
		t.Fatal(err)
	}
	warning := errOut.String()
	for _, want := range []string{"internal/cli/pr.go", "./...", "hew pr --amend"} {
		if !strings.Contains(warning, want) {
			t.Errorf("warning %q does not name %q", warning, want)
		}
	}
	// The hand-written body is kept verbatim; only the trailer is appended.
	if body := prBody(t, createdPR(t, f)); !strings.HasPrefix(body, "Touch internal/cli/pr.go and run go test ./..., then check") {
		t.Errorf("pr rewrote the author's text: %q", body)
	}
}

// A body the author already marked up must draw no warning, or the check
// fires forever on correct input and stops being read.
func TestPRStaysQuietWhenCodeTextIsMarkedUp(t *testing.T) {
	f := newFake(claimed(30, "PR creation command", "P2", "enhancement"))
	app, _, errOut := newApp(f)
	onBranch(app, "feat/pr-command")

	err := app.PR(context.Background(), PROpts{
		Sections: sections("Rewrites `internal/cli/pr.go`.", "", "`go test -race ./...` is green"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(errOut.String(), "code spans") {
		t.Errorf("warned about a correctly marked-up body: %q", errOut.String())
	}
}

func amendSetup() (*fakeClient, *App) {
	i := subIssueOf(claimed(30, "issue title", "P2", "enhancement"), 33)
	i.Body = "### Approach\n\nIssue approach\n\n### Goal\n\nIssue goal"
	f := newFake(i)
	f.openPRs["feat/30-work"] = gh.PullRequest{
		Number: 501, URL: "https://github.com/o/r/pull/501", Draft: true,
		Title: "feat: original title", Base: "release", HeadRepository: "o/r",
		Body: "### What\n\nOriginal what\n\n### Why\n\nOriginal why\n\n### Testing\n\nOriginal testing\n\n### Risk\n\nOriginal risk\n\nFixes #30\nPart of #33",
	}
	app, _, _ := newApp(f)
	inWorktree(app, "local-worktree", "feat/30-work")
	return f, app
}

func assertPRReadsOnly(t *testing.T, f *fakeClient) {
	t.Helper()
	for _, call := range f.calls {
		method, _, _ := strings.Cut(call, " ")
		if !slices.Contains([]string{"ListIssues", "Viewer", "PullRequestContext"}, method) {
			t.Errorf("unexpected mutation: %s", call)
		}
	}
}

func TestPRAmendRecomposesOnlyNamedSections(t *testing.T) {
	for _, name := range []string{"what", "why", "testing", "risk"} {
		t.Run(name, func(t *testing.T) {
			f, app := amendSetup()
			opts := PROpts{Amend: true}
			switch name {
			case "what":
				opts.Sections.What = "Revised what"
			case "why":
				opts.Sections.Why = "Revised why"
			case "testing":
				opts.Sections.Testing = "Revised testing"
			case "risk":
				opts.Sections.Risk = "Revised risk"
			}
			if err := app.PR(context.Background(), opts); err != nil {
				t.Fatal(err)
			}
			got := f.openPRs["feat/30-work"]
			want := conventions.PRSections{
				What: "Original what", Why: "Original why",
				Testing: "Original testing", Risk: "Original risk",
			}
			switch name {
			case "what":
				want.What = "Revised what"
			case "why":
				want.Why = "Revised why"
			case "testing":
				want.Testing = "Revised testing"
			case "risk":
				want.Risk = "Revised risk"
			}
			body := want.Compose(conventions.PRTrailers{Fixes: []int{30}, Epics: []int{33}})
			if got.Body != body {
				t.Errorf("body = %q, want %q", got.Body, body)
			}
			if got.Title != "feat: original title" || !got.Draft || got.Base != "release" {
				t.Errorf("amend changed PR metadata: %+v", got)
			}
			assertNoPR(t, f)
		})
	}
}

func TestPRAmendPreservesAbsentSectionsAndClearsExplicitEmpty(t *testing.T) {
	f, app := amendSetup()
	f.openPRs["feat/30-work"] = gh.PullRequest{Number: 501, HeadRepository: "o/r", Body: "### Testing\n\nOld tests\n\nFixes #30"}
	if err := app.PR(context.Background(), PROpts{Amend: true, SectionFlags: []string{"testing"}}); err != nil {
		t.Fatal(err)
	}
	want := "Fixes #30\nPart of #33"
	if got := f.openPRs["feat/30-work"].Body; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

// Risk behaves like the other sections: an omitted --risk keeps the existing
// value, and an explicitly empty one removes the section.
func TestPRAmendPreservesAndClearsRisk(t *testing.T) {
	f, app := amendSetup()
	if err := app.PR(context.Background(), PROpts{
		Amend:    true,
		Sections: conventions.PRSections{Testing: "Revised testing"},
	}); err != nil {
		t.Fatal(err)
	}
	if body := f.openPRs["feat/30-work"].Body; !strings.Contains(body, "### Risk\n\nOriginal risk") {
		t.Errorf("amend lost the preserved Risk section:\n%s", body)
	}

	if err := app.PR(context.Background(), PROpts{Amend: true, SectionFlags: []string{"risk"}}); err != nil {
		t.Fatal(err)
	}
	body := f.openPRs["feat/30-work"].Body
	if strings.Contains(body, "### Risk") {
		t.Errorf("an explicitly empty --risk did not remove the section:\n%s", body)
	}
	if !strings.Contains(body, "### Testing\n\nRevised testing") {
		t.Errorf("clearing Risk changed Testing:\n%s", body)
	}
}

// A Testing section composed from repeated --testing values (bullets, with an
// indented continuation line) must survive the amend path's lossless-read check,
// or the tool would refuse to edit a PR it wrote itself.
func TestPRAmendPreservesBulletListSections(t *testing.T) {
	f, app := amendSetup()
	testing := conventions.PRList([]string{"build passes\nsecond line", "lint clean"})
	pr := f.openPRs["feat/30-work"]
	pr.Body = conventions.PRSections{What: "w", Testing: testing, Risk: "r"}.Compose(conventions.PRTrailers{Fixes: []int{30}, Epics: []int{33}})
	f.openPRs["feat/30-work"] = pr

	if err := app.PR(context.Background(), PROpts{
		Amend:        true,
		SectionFlags: []string{"what"},
		Sections:     conventions.PRSections{What: conventions.PRList([]string{"a", "b"})},
	}); err != nil {
		t.Fatal(err)
	}
	body := f.openPRs["feat/30-work"].Body
	for _, want := range []string{"### What\n\n- a\n- b", "### Testing\n\n" + testing, "### Risk\n\nr"} {
		if !strings.Contains(body, want) {
			t.Errorf("amended body lost %q:\n%s", want, body)
		}
	}
}

func TestPRAmendTitlePreservesFreeformBodyAndReportsExistingPR(t *testing.T) {
	f, app := amendSetup()
	app.JSON = true
	original := f.openPRs["feat/30-work"]
	original.Body = "Freeform body\n\nCloses #30\nPart of #33"
	f.openPRs["feat/30-work"] = original
	if err := app.PR(context.Background(), PROpts{Amend: true, Title: "feat: revised title"}); err != nil {
		t.Fatal(err)
	}
	got := f.openPRs["feat/30-work"]
	if got.Title != "feat: revised title" || got.Body != original.Body {
		t.Errorf("amended PR = %+v", got)
	}
	var result prJSON
	if err := json.Unmarshal(app.Out.(*bytes.Buffer).Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Number != 501 || result.Title != got.Title || result.Head != "feat/30-work" || result.Base != "release" || !result.Draft || result.Fixes != 30 {
		t.Errorf("result = %+v", result)
	}
}

func TestPRAmendRejectsInvalidClosingLinksBeforeMutation(t *testing.T) {
	for _, bodyFile := range []bool{false, true} {
		for _, body := range []string{"Fixes #99", "Fixes #30\nCloses #99", "Fixes #30\nFixes #30"} {
			t.Run(fmt.Sprintf("file=%v/%s", bodyFile, body), func(t *testing.T) {
				f, app := amendSetup()
				opts := PROpts{Amend: true, Sections: sections(body, "", "")}
				if bodyFile {
					opts.Sections = sections("", "", "")
					opts.BodyFile = writeTemp(t, body)
				}
				err := app.PR(context.Background(), opts)
				requireExit(t, err, ExitUsage, "closes")
				assertPRReadsOnly(t, f)
			})
		}
	}
}

func TestPRAmendRejectsInvalidPreservedClosingLink(t *testing.T) {
	f, app := amendSetup()
	pr := f.openPRs["feat/30-work"]
	pr.Body = strings.Replace(pr.Body, "Original why", "Closes #99", 1)
	f.openPRs["feat/30-work"] = pr
	err := app.PR(context.Background(), PROpts{Amend: true, Sections: sections("new what", "", "")})
	requireExit(t, err, ExitUsage, "closes")
	assertPRReadsOnly(t, f)
}

func TestPRAmendBodyFileAppendsOnlyMissingTrailers(t *testing.T) {
	for _, body := range []string{"Freeform", "Freeform\n\nFixes #30", "Freeform\n\nCloses #30\nPart of #33"} {
		t.Run(body, func(t *testing.T) {
			f, app := amendSetup()
			if err := app.PR(context.Background(), PROpts{Amend: true, BodyFile: writeTemp(t, body)}); err != nil {
				t.Fatal(err)
			}
			got := f.openPRs["feat/30-work"].Body
			if !strings.HasPrefix(got, body) || len(conventions.FixesReferences(got)) != 1 || strings.Count(got, "Part of #33") != 1 {
				t.Errorf("body = %q", got)
			}
		})
	}
}

// The amend body-file path is not exempt from the section contract either.
func TestPRAmendBodyFileWarnsAboutMissingTesting(t *testing.T) {
	_, app := amendSetup()
	if err := app.PR(context.Background(), PROpts{Amend: true, BodyFile: writeTemp(t, "narrative")}); err != nil {
		t.Fatal(err)
	}
	if warning := app.ErrOut.(*bytes.Buffer).String(); !strings.Contains(warning, "no Testing section") {
		t.Errorf("amend --body-file did not warn: %q", warning)
	}
}

// Risk is asked for once a PR is out of draft; amendSetup's draft keeps it
// optional.
func TestPRAmendWarnsAboutMissingRiskOnlyWhenReady(t *testing.T) {
	const noRisk = "### What\n\nOriginal what\n\n### Why\n\nOriginal why\n\n### Testing\n\nOriginal testing\n\nFixes #30\nPart of #33"
	for _, tc := range []struct {
		name  string
		draft bool
		opts  PROpts
		want  bool
	}{
		{"ready section edit", false, PROpts{Amend: true, Sections: conventions.PRSections{What: "Revised"}}, true},
		{"draft section edit", true, PROpts{Amend: true, Sections: conventions.PRSections{What: "Revised"}}, false},
		{"ready title-only amend", false, PROpts{Amend: true, Title: "feat: new title"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, app := amendSetup()
			pr := f.openPRs["feat/30-work"]
			pr.Draft, pr.Body = tc.draft, noRisk
			f.openPRs["feat/30-work"] = pr
			if err := app.PR(context.Background(), tc.opts); err != nil {
				t.Fatal(err)
			}
			got := strings.Contains(app.ErrOut.(*bytes.Buffer).String(), "no Risk section")
			if got != tc.want {
				t.Errorf("warned about Risk = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPRAmendRefusesMissingOpenPR(t *testing.T) {
	f := newFake(claimed(30, "work"))
	app, _, _ := newApp(f)
	onBranch(app, "feat/30-work")
	err := app.PR(context.Background(), PROpts{Amend: true, Title: "new title"})
	requireExit(t, err, ExitGeneric, "no open PR")
	assertPRReadsOnly(t, f)
}

func TestPRAmendRefusesCreationOnlyFlags(t *testing.T) {
	for _, opts := range []PROpts{{Amend: true, Ready: true}, {Amend: true, Base: "main"}} {
		f, app := amendSetup()
		err := app.PR(context.Background(), opts)
		requireExit(t, err, ExitUsage, "--amend")
		assertPRReadsOnly(t, f)
	}
}

func TestPRAmendPropagatesUpdateFailure(t *testing.T) {
	f, app := amendSetup()
	original := f.openPRs["feat/30-work"]
	f.failOn["UpdatePullRequest"] = errors.New("update refused")
	err := app.PR(context.Background(), PROpts{Amend: true, Title: "revised"})
	if err == nil || !strings.Contains(err.Error(), "update refused") {
		t.Fatalf("error = %v", err)
	}
	if got := f.openPRs["feat/30-work"]; got != original {
		t.Errorf("failed update mutated PR: %+v", got)
	}
	if got := app.Out.(*bytes.Buffer).String(); got != "" {
		t.Errorf("failed update reported success: %q", got)
	}
}

func TestPRAmendTextOutputAndCodeWarnings(t *testing.T) {
	f, app := amendSetup()
	if err := app.PR(context.Background(), PROpts{Amend: true, Title: "feat: revised\x1b[31m", Sections: sections("Changes internal/cli/pr.go", "", "")}); err != nil {
		t.Fatal(err)
	}
	want := "updated PR #501 for #30: feat: revised?[31m\nhttps://github.com/o/r/pull/501\n"
	if got := app.Out.(*bytes.Buffer).String(); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
	warning := app.ErrOut.(*bytes.Buffer).String()
	if !strings.Contains(warning, "internal/cli/pr.go") || !strings.Contains(warning, "hew pr --amend") {
		t.Errorf("warning = %q", warning)
	}
	if strings.Contains(warning, "no Testing section") {
		t.Errorf("warned about preserved testing: %q", warning)
	}
	assertNoPR(t, f)
}

func TestPRAmendBodyFileRejectsEvenEmptySectionFlags(t *testing.T) {
	f, app := amendSetup()
	err := app.PR(context.Background(), PROpts{Amend: true, BodyFile: writeTemp(t, "body"), SectionFlags: []string{"what"}})
	requireExit(t, err, ExitUsage, "mutually exclusive")
	assertPRReadsOnly(t, f)
}

func TestPRCreateSectionFlagsEnforceClosingLinks(t *testing.T) {
	for _, body := range []string{"Fixes #99", "Closes #30\nFixes #99", "Fixes #30\nFixes #30"} {
		t.Run(body, func(t *testing.T) {
			f := newFake(claimed(30, "work"))
			app, _, _ := newApp(f)
			onBranch(app, "feat/30-work")
			err := app.PR(context.Background(), PROpts{Sections: sections(body, "", "")})
			requireExit(t, err, ExitUsage, "closes")
			assertPRReadsOnly(t, f)
		})
	}
}

// Bodies the simple section reader cannot safely reproduce must never be
// partially rewritten. The entire body remains available through --body-file.
func TestPRAmendRefusesLossySectionEdits(t *testing.T) {
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
			f, app := amendSetup()
			original := f.openPRs["feat/30-work"]
			original.Body = body
			f.openPRs["feat/30-work"] = original
			err := app.PR(context.Background(), PROpts{Amend: true, Title: "also must not change", Sections: sections("", "", "New tests")})
			requireExit(t, err, ExitUsage, "--body-file")
			assertPRReadsOnly(t, f)
			if got := f.openPRs["feat/30-work"]; got != original {
				t.Errorf("refused edit mutated PR: %+v", got)
			}
		})
	}
}

func TestPRAmendBodyFileCanReplaceCustomMarkdown(t *testing.T) {
	f, app := amendSetup()
	body := "Introduction.\n\n### What\n\n```markdown\n### Example\nKeep this.\n```\n\n### Notes\n\nCustom notes.\n\nFixes #30\nPart of #33"
	if err := app.PR(context.Background(), PROpts{Amend: true, BodyFile: writeTemp(t, body)}); err != nil {
		t.Fatal(err)
	}
	if got := f.openPRs["feat/30-work"].Body; got != body {
		t.Errorf("body = %q, want %q", got, body)
	}
}

func TestPRAmendRefusesAmbiguousOrForeignHead(t *testing.T) {
	for _, tc := range []struct {
		name       string
		count      int
		repository string
		want       string
	}{
		{"two base branches", 2, "o/r", "unambiguous"},
		{"truncated matches", 100, "o/r", "unambiguous"},
		{"fork", 1, "contributor/r", "head repository"},
		{"unknown repository", 1, "", "head repository"},
		{"unknown count", 0, "o/r", "unambiguous"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, app := amendSetup()
			pr := f.openPRs["feat/30-work"]
			pr.HeadRepository = tc.repository
			f.prContextOverride = &gh.PRContext{DefaultBranch: "main", Existing: &pr, OpenCount: tc.count}
			err := app.PR(context.Background(), PROpts{Amend: true, Title: "must not update"})
			requireExit(t, err, ExitGeneric, tc.want)
			assertPRReadsOnly(t, f)
		})
	}
}

func TestPRAmendMatchesHeadRepositoryCaseInsensitively(t *testing.T) {
	f, app := amendSetup()
	pr := f.openPRs["feat/30-work"]
	pr.HeadRepository = "O/R"
	f.openPRs["feat/30-work"] = pr
	if err := app.PR(context.Background(), PROpts{Amend: true, Title: "new title"}); err != nil {
		t.Fatal(err)
	}
	if got := f.openPRs["feat/30-work"].Title; got != "new title" {
		t.Errorf("title = %q", got)
	}
}

// --body-file content has no template guarantee, so the section warnings judge
// structure: what a reader would call a Testing or Risk section, not a literal
// "### Testing" line.
func TestPRBodyFileSectionWarnings(t *testing.T) {
	for _, tc := range []struct {
		name        string
		body        string
		wantTesting bool
		wantRisk    bool
	}{
		{"fenced heading is not a section", "### What\n\n```md\n### Testing\n\nran it\n```", true, true},
		{"empty trailing Testing before the trailer", "### Risk\n\nTwo-way.\n\n### Testing", true, false},
		{"empty trailing Risk before the trailer", "### Testing\n\nran it\n\n### Risk", false, true},
		{"heading-only first occurrence, real later one", "### Testing\n\n### Risk\n\nTwo-way.\n\n### Testing\n\nran it", false, false},
		{"other heading level counts", "## Testing\n\nran it\n\n## Risk\n\nTwo-way.", false, false},
		{"bold label counts", "**Testing**\n\nran it\n\n**Risk**\n\nTwo-way.", false, false},
		{"empty bold Testing before a bold Risk", "**Testing**\n\n**Risk**\n\nTwo-way.", true, false},
		{"headings with a trailing colon", "## Testing:\n\nran it\n\n## Risk:\n\nTwo-way.", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(claimed(30, "work", "P2", "enhancement"))
			app, _, errOut := newApp(f)
			onBranch(app, "feat/30-work")

			if err := app.PR(context.Background(), PROpts{Ready: true, BodyFile: writeTemp(t, tc.body)}); err != nil {
				t.Fatal(err)
			}
			warning := errOut.String()
			if got := strings.Contains(warning, "no Testing section"); got != tc.wantTesting {
				t.Errorf("create: warned about Testing = %v, want %v\n%s", got, tc.wantTesting, warning)
			}
			if got := strings.Contains(warning, "no Risk section"); got != tc.wantRisk {
				t.Errorf("create: warned about Risk = %v, want %v\n%s", got, tc.wantRisk, warning)
			}

			f, app = amendSetup()
			pr := f.openPRs["feat/30-work"]
			pr.Draft = false
			f.openPRs["feat/30-work"] = pr
			if err := app.PR(context.Background(), PROpts{Amend: true, BodyFile: writeTemp(t, tc.body)}); err != nil {
				t.Fatal(err)
			}
			warning = app.ErrOut.(*bytes.Buffer).String()
			if got := strings.Contains(warning, "no Testing section"); got != tc.wantTesting {
				t.Errorf("amend: warned about Testing = %v, want %v\n%s", got, tc.wantTesting, warning)
			}
			if got := strings.Contains(warning, "no Risk section"); got != tc.wantRisk {
				t.Errorf("amend: warned about Risk = %v, want %v\n%s", got, tc.wantRisk, warning)
			}
		})
	}
}
