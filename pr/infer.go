package pr

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/lumberbarons/hew/tracker"
)

// linkedIssues resolves the set of issues a pull request closes. Inference
// only ever yields one issue; several come solely from an explicit request,
// because a wrong Fixes line closes the wrong issue on merge and an inferred
// second one would do it silently. A repeated number is collapsed.
func linkedIssues(issues []tracker.Issue, branch, actor string, requested []int) ([]tracker.Issue, error) {
	if len(requested) == 0 {
		issue, err := linkedIssue(issues, branch, actor, 0)
		if err != nil {
			return nil, err
		}
		return []tracker.Issue{issue}, nil
	}
	var linked []tracker.Issue
	for _, n := range requested {
		if slices.ContainsFunc(linked, func(i tracker.Issue) bool { return i.Number == n }) {
			continue
		}
		issue, err := linkedIssue(issues, branch, actor, n)
		if err != nil {
			return nil, err
		}
		linked = append(linked, issue)
	}
	return linked, nil
}

// linkedIssue decides which single issue a pull request closes. The claim is
// the primary signal — it is tracker state, not a naming guess — and a number
// in the branch name breaks ties, covers the unclaimed case, and vetoes a lone
// claim it contradicts. Ambiguity is a typed refusal naming the candidates
// rather than a coin flip: a wrong Fixes line closes the wrong issue on merge.
func linkedIssue(issues []tracker.Issue, branch, actor string, requested int) (tracker.Issue, error) {
	byNum := byNumber(issues)
	if requested > 0 {
		issue, ok := byNum[requested]
		if !ok {
			return tracker.Issue{}, &tracker.NotFoundError{Issue: requested}
		}
		if issue.Epic {
			return tracker.Issue{}, &tracker.EpicError{Issue: requested}
		}
		return issue, nil
	}

	var claimed []tracker.Issue
	for _, i := range issues {
		if !i.Epic && slices.Contains(i.Assignees, actor) {
			claimed = append(claimed, i)
		}
	}
	branchNum, hasBranchNum := branchIssueNumber(branch)
	switch {
	case len(claimed) == 1:
		// The claim is the stronger signal, but when the branch name points
		// at a different open issue the two disagree and one of them is
		// wrong. Preferring the claim silently writes a Fixes line that
		// closes the other issue on merge, so the disagreement is the
		// caller's to resolve.
		if other, ok := byNum[branchNum]; hasBranchNum && ok &&
			other.Number != claimed[0].Number && !other.Epic {
			return tracker.Issue{}, &IssueInferenceError{
				Failure: InferenceConflict, Branch: branch, Actor: actor,
				Claimed: issueNumbers(claimed), BranchIssue: other.Number,
			}
		}
		return claimed[0], nil
	case len(claimed) > 1:
		if hasBranchNum {
			for _, i := range claimed {
				if i.Number == branchNum {
					return i, nil
				}
			}
		}
		return tracker.Issue{}, &IssueInferenceError{
			Failure: InferenceAmbiguous, Branch: branch, Actor: actor,
			Claimed: issueNumbers(claimed),
		}
	default:
		if hasBranchNum {
			if issue, ok := byNum[branchNum]; ok && !issue.Epic {
				return issue, nil
			}
		}
		return tracker.Issue{}, &IssueInferenceError{
			Failure: InferenceMissing, Branch: branch, Actor: actor,
		}
	}
}

// branchIssueNumber pulls an issue number out of a branch name, matching the
// digit run in segments like feat/30-pr-command or fix/issue-42. Only a
// segment that is entirely digits counts, so fix/http500-retries stays a name
// and not a link to #500 — the branch is a hint, and a hint that guesses wrong
// closes the wrong issue.
func branchIssueNumber(branch string) (int, bool) {
	for _, part := range strings.FieldsFunc(branch, func(r rune) bool {
		return r == '/' || r == '-' || r == '_' || r == '.'
	}) {
		if n, err := strconv.Atoi(part); err == nil && n > 0 {
			return n, true
		}
	}
	return 0, false
}

func byNumber(issues []tracker.Issue) map[int]tracker.Issue {
	byNum := make(map[int]tracker.Issue, len(issues))
	for _, issue := range issues {
		byNum[issue.Number] = issue
	}
	return byNum
}

// relatedGroups partitions the declared set into connected groups: a shared
// parent, a shared area label or a dependency edge joins two issues. More than
// one group is what draws the unrelated-set warning.
func relatedGroups(issues []tracker.Issue) [][]int {
	if len(issues) == 0 {
		return nil
	}
	group := make([]int, len(issues))
	for i := range group {
		group[i] = i
	}
	for i := range issues {
		for j := i + 1; j < len(issues); j++ {
			if related(issues[i], issues[j]) {
				group[findRoot(group, j)] = findRoot(group, i)
			}
		}
	}
	var out [][]int
	index := map[int]int{}
	for i, issue := range issues {
		root := findRoot(group, i)
		at, ok := index[root]
		if !ok {
			at = len(out)
			index[root] = at
			out = append(out, nil)
		}
		out[at] = append(out[at], issue.Number)
	}
	return out
}

// findRoot follows the union-find parent links to a group's representative.
func findRoot(group []int, i int) int {
	for group[i] != i {
		group[i] = group[group[i]]
		i = group[i]
	}
	return i
}

// related reports whether two issues are connected closely enough for one pull
// request to be a single change.
func related(a, b tracker.Issue) bool {
	if a.Parent != nil && b.Parent != nil && *a.Parent == *b.Parent {
		return true
	}
	for _, area := range a.Areas {
		if slices.Contains(b.Areas, area) {
			return true
		}
	}
	return slices.Contains(a.BlockedBy, b.Number) || slices.Contains(b.BlockedBy, a.Number)
}

func issueNumbers(issues []tracker.Issue) []int {
	numbers := make([]int, len(issues))
	for i, issue := range issues {
		numbers[i] = issue.Number
	}
	return numbers
}

func formatRefs(numbers []int) string {
	parts := make([]string, len(numbers))
	for i, n := range numbers {
		parts[i] = fmt.Sprintf("#%d", n)
	}
	return strings.Join(parts, " ")
}
