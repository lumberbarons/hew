package pr

import (
	"errors"
	"slices"
	"testing"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/git"
	"github.com/lumberbarons/hew/internal/model"
	"github.com/lumberbarons/hew/tracker"
)

// branchIssueNumber decides whether a branch links an issue at all, and every
// wrong answer either mislinks a pull request or drops a usable hint, so its
// rules are pinned directly rather than only through the workflows.
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

// withBranch is a Client over the fake halves on a named, pushed branch.
func withBranch(branch string, issues ...model.Issue) (*Client, *fakeStore, *fakePRs) {
	store := &fakeStore{issues: issues, viewer: "me"}
	prs := &fakePRs{defaultBranch: "main"}
	client := Wrap(store, prs, gh.Repo{Owner: "o", Name: "r"},
		git.State{Branch: branch, Pushed: true})
	return client, store, prs
}

// inferenceCase is one create attempt, reduced to the linked set or the
// refusal it produces.
func inferenceCase(t *testing.T, branch string, issues ...model.Issue) ([]int, error) {
	t.Helper()
	client, _, _ := withBranch(branch, issues...)
	result, err := client.Create(t.Context(), CreateRequest{Body: Body{Sections: sections("w", "y", "t")}})
	if err != nil {
		return nil, err
	}
	return result.FixedIssues, nil
}

func TestLinkedIssuesSettlesTheClaimAndBranch(t *testing.T) {
	epic := epicFixture(33, "Epic: the tree")
	for name, tc := range map[string]struct {
		branch string
		issues []model.Issue
		want   []int
	}{
		"the claim is the signal": {
			branch: "feat/pr-command", issues: []model.Issue{claimed(30, "the claim")}, want: []int{30},
		},
		"the branch breaks a tie": {
			branch: "feat/31-two", issues: []model.Issue{claimed(30, "one"), claimed(31, "two")}, want: []int{31},
		},
		"the branch stands in when nothing is claimed": {
			branch: "fix/issue-42", issues: []model.Issue{fixture(42, "unclaimed")}, want: []int{42},
		},
		"a branch number matching the claim is agreement": {
			branch: "fix/30-the-claim", issues: []model.Issue{claimed(30, "the claim")}, want: []int{30},
		},
		"a branch number naming nothing open is no second candidate": {
			branch: "fix/pagination-77", issues: []model.Issue{claimed(30, "the claim")}, want: []int{30},
		},
		"a branch naming an epic does not block the claim": {
			branch: "feat/33-the-tree", issues: []model.Issue{epic, claimed(30, "the claim")}, want: []int{30},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := inferenceCase(t, tc.branch, tc.issues...)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("linked = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLinkedIssuesRefusesAmbiguity(t *testing.T) {
	epic := epicFixture(33, "Epic: the tree")
	for name, tc := range map[string]struct {
		branch string
		issues []model.Issue
		want   InferenceFailure
	}{
		"several claims": {
			branch: "feat/ambiguous", issues: []model.Issue{claimed(30, "one"), claimed(31, "two")},
			want: InferenceAmbiguous,
		},
		"a claim contradicted by the branch": {
			branch: "fix/pagination-2", issues: []model.Issue{claimed(30, "the claim"), fixture(2, "pagination")},
			want: InferenceConflict,
		},
		"nothing claimed": {
			branch: "feat/mystery", issues: []model.Issue{fixture(30, "someone else's")},
			want: InferenceMissing,
		},
		"a branch number naming an epic is not a fallback": {
			branch: "feat/33-the-tree", issues: []model.Issue{epic, fixture(30, "child")},
			want: InferenceMissing,
		},
		"a branch number naming no open issue is not a fallback": {
			branch: "feat/77-long-gone", issues: []model.Issue{fixture(30, "unrelated")},
			want: InferenceMissing,
		},
		"a digit run inside a word is not a link": {
			branch: "fix/http500-retries", issues: []model.Issue{fixture(500, "unrelated")},
			want: InferenceMissing,
		},
		"an epic claim is not a target": {
			branch: "feat/x", issues: []model.Issue{claimedEpic(33, "Epic: the tree")},
			want: InferenceMissing,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := inferenceCase(t, tc.branch, tc.issues...)
			var inference *IssueInferenceError
			if !errors.As(err, &inference) {
				t.Fatalf("err = %T (%v), want *IssueInferenceError", err, err)
			}
			if inference.Failure != tc.want {
				t.Errorf("Failure = %q, want %q", inference.Failure, tc.want)
			}
		})
	}
}

// An inference refusal names the candidates instead of guessing: a wrong Fixes
// line closes the wrong issue on merge.
func TestInferenceErrorsCarryTheCandidates(t *testing.T) {
	_, err := inferenceCase(t, "fix/pagination-2", claimed(30, "the claim"), fixture(2, "pagination"))
	var conflict *IssueInferenceError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %T (%v), want *IssueInferenceError", err, err)
	}
	if conflict.Branch != "fix/pagination-2" || conflict.BranchIssue != 2 || !slices.Equal(conflict.Claimed, []int{30}) {
		t.Errorf("conflict = %+v", conflict)
	}

	_, err = inferenceCase(t, "feat/ambiguous", claimed(30, "one"), claimed(31, "two"))
	var ambiguous *IssueInferenceError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("err = %T (%v), want *IssueInferenceError", err, err)
	}
	if len(ambiguous.Claimed) != 2 || ambiguous.Actor != "me" {
		t.Errorf("ambiguous = %+v", ambiguous)
	}
}

// Explicit declarations are the only way several issues reach one pull
// request: inference resolves at most one, which is what keeps the accidental
// closure protection.
func TestLinkedIssuesCollapsesRepeatedDeclarations(t *testing.T) {
	client, _, _ := wrapped(claimed(30, "one"), claimed(31, "two"))
	result, err := client.Create(t.Context(), CreateRequest{
		For: []int{31, 31, 30}, Body: Body{Sections: sections("w", "y", "t")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.FixedIssues, []int{31, 30}) {
		t.Errorf("FixedIssues = %v, want the declared order without repeats", result.FixedIssues)
	}
	if result.Fixes != 31 {
		t.Errorf("Fixes = %d, want the primary issue", result.Fixes)
	}
}

func TestLinkedIssueRejectsAnUnusableDeclaration(t *testing.T) {
	epic := epicFixture(33, "Epic: the tree")
	for name, tc := range map[string]struct {
		numbers []int
		check   func(error) bool
	}{
		"missing": {[]int{999}, func(err error) bool {
			var notFound *tracker.NotFoundError
			return errors.As(err, &notFound) && notFound.Issue == 999
		}},
		"epic": {[]int{33}, func(err error) bool {
			var epicErr *tracker.EpicError
			return errors.As(err, &epicErr) && epicErr.Issue == 33
		}},
	} {
		t.Run(name, func(t *testing.T) {
			client, _, _ := wrapped(epic, claimed(46, "first"))
			_, err := client.Create(t.Context(), CreateRequest{For: tc.numbers})
			if err == nil || !tc.check(err) {
				t.Fatalf("err = %T (%v), want the typed refusal", err, err)
			}
		})
	}
}

// Relatedness is a union over shared parents, area labels and dependency
// edges, and the grouping drives the unrelated-set warning.
func TestRelatedGroups(t *testing.T) {
	parent := func(n int) *int { return &n }
	issues := []tracker.Issue{
		{Number: 1, Areas: []string{"cli"}, Parent: parent(9)},
		{Number: 2, Areas: []string{"cli", "docs"}},
		{Number: 3, Areas: []string{"docs"}},
		{Number: 4, BlockedBy: []int{1}},
		{Number: 5, Areas: []string{"infra"}},
	}
	got := relatedGroups(issues)
	want := [][]int{{1, 2, 3, 4}, {5}}
	if len(got) != len(want) {
		t.Fatalf("groups = %v, want %v", got, want)
	}
	for i := range want {
		if !slices.Equal(got[i], want[i]) {
			t.Errorf("group %d = %v, want %v", i, got[i], want[i])
		}
	}
	if groups := relatedGroups(nil); groups != nil {
		t.Errorf("groups of nothing = %v, want nil", groups)
	}
}
