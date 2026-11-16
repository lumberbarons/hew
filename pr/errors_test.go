package pr

import (
	"strings"
	"testing"
)

// The typed refusals carry the fields a caller branches on; their messages
// name the same facts so a log read without the type is still actionable.
func TestErrorMessages(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want []string
	}{
		"no pull requests": {
			err: &NoPullRequestsError{}, want: []string{"GitHub backend"},
		},
		"no upstream": {
			err: &NoUpstreamError{Branch: "feat/local"}, want: []string{"feat/local", "git push -u origin feat/local"},
		},
		"branch and claim disagree": {
			err: &IssueInferenceError{
				Failure: InferenceConflict, Branch: "fix/2-x", BranchIssue: 2, Claimed: []int{30},
			},
			want: []string{"fix/2-x", "#2", "#30"},
		},
		"several claims": {
			err: &IssueInferenceError{
				Failure: InferenceAmbiguous, Branch: "feat/x", Claimed: []int{46, 50},
			},
			want: []string{"#46 #50"},
		},
		"no signal": {
			err: &IssueInferenceError{
				Failure: InferenceMissing, Branch: "feat/x", Actor: "octocat",
			},
			want: []string{"feat/x", "octocat"},
		},
		"an existing pull request": {
			err:  &ExistingPullRequestError{Issue: 30, Head: "feat/x", Number: 501},
			want: []string{"#30", "feat/x", "#501"},
		},
		"no open pull request": {
			err:  &NoOpenPullRequestError{Issue: 30, Head: "feat/x"},
			want: []string{"#30", "feat/x"},
		},
		"several matching pull requests": {
			err:  &AmbiguousPullRequestError{Kind: AmbiguousMatches, Head: "feat/x", OpenCount: 2},
			want: []string{"feat/x", "2 open matches"},
		},
		"a foreign head": {
			err: &AmbiguousPullRequestError{
				Kind: AmbiguousHead, Number: 501, HeadRepository: "contributor/r", Repo: "o/r",
			},
			want: []string{"#501", "contributor/r", "o/r"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			for _, want := range tc.want {
				if !strings.Contains(tc.err.Error(), want) {
					t.Errorf("%v does not name %q", tc.err, want)
				}
			}
		})
	}
}

// A caller may construct one of these errors itself; a message must not panic
// on the fields it did not fill.
func TestErrorMessagesTolerateMissingFields(t *testing.T) {
	for _, err := range []error{
		&IssueInferenceError{Failure: InferenceConflict},
		&IssueInferenceError{},
		&AmbiguousPullRequestError{},
	} {
		if err.Error() == "" {
			t.Errorf("%T has an empty message", err)
		}
	}
}

func TestSectionsIsZero(t *testing.T) {
	if !(Sections{}).IsZero() {
		t.Error("zero Sections is not zero")
	}
	for name, sections := range map[string]Sections{
		"what": {What: "w"}, "why": {Why: "y"}, "testing": {Testing: "t"}, "risk": {Risk: "r"},
	} {
		if sections.IsZero() {
			t.Errorf("%s sections reported zero", name)
		}
	}
}
