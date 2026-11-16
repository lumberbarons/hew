package pr

import "fmt"

// Request fields named by an *tracker.InvalidInputError this package returns.
// They are library terms, not flag spellings: the CLI adapter maps them onto
// its flags and sentences.
const (
	// FieldBase is a base branch that could not be resolved or is the head
	// branch itself.
	FieldBase = "base"
	// FieldHead is a head branch that cannot carry a pull request.
	FieldHead = "head"
	// FieldBody is conflicting body sources.
	FieldBody = "body"
	// FieldBodyEdit is an existing body the section reader cannot reproduce
	// losslessly.
	FieldBodyEdit = "body-edit"
)

// NoPullRequestsError reports a workflow that needs pull requests on a backend
// that has none: the local tracker implements issue storage only.
type NoPullRequestsError struct{}

func (e *NoPullRequestsError) Error() string {
	return "pull requests need the GitHub backend; this repository tracks issues locally"
}

// NoUpstreamError reports a branch GitHub has never seen. A pull request can
// only be opened for a ref the remote already has, so the branch has to be
// pushed first; Branch is the local name, which is what the push advice needs.
type NoUpstreamError struct {
	Branch string
}

func (e *NoUpstreamError) Error() string {
	return fmt.Sprintf("branch %s has no upstream; push it first: git push -u origin %s", e.Branch, e.Branch)
}

// InferenceFailure names why issue inference could not settle on one issue.
type InferenceFailure string

const (
	// InferenceConflict is one claim contradicted by a branch naming a
	// different open issue: two signals disagree, and one of them is wrong.
	InferenceConflict InferenceFailure = "branch-claim-conflict"
	// InferenceAmbiguous is several issues claimed by the acting identity
	// with no branch number to break the tie.
	InferenceAmbiguous InferenceFailure = "several-claims"
	// InferenceMissing is no claim and no branch number naming an open
	// issue — the branch carries no usable signal at all.
	InferenceMissing InferenceFailure = "no-signal"
)

// IssueInferenceError reports that the issue a pull request closes could not
// be resolved. Guessing is not offered: a wrong closing reference closes the
// wrong issue on merge. Claimed carries the acting identity's open non-epic
// claims, and BranchIssue the number the branch names (0 when it names none).
type IssueInferenceError struct {
	Failure     InferenceFailure
	Branch      string
	Actor       string
	Claimed     []int
	BranchIssue int
}

func (e *IssueInferenceError) Error() string {
	switch e.Failure {
	case InferenceConflict:
		claimed := 0
		if len(e.Claimed) > 0 {
			claimed = e.Claimed[0]
		}
		return fmt.Sprintf("branch %s names issue #%d but the acting identity has #%d claimed", e.Branch, e.BranchIssue, claimed)
	case InferenceAmbiguous:
		return fmt.Sprintf("the acting identity has several issues claimed: %s", formatRefs(e.Claimed))
	default:
		return fmt.Sprintf("cannot tell which issue branch %s is for: nothing is claimed by %s", e.Branch, e.Actor)
	}
}

// ExistingPullRequestError reports that the branch already has an open pull
// request, which the API would refuse as a second one. Issue is the linked
// issue and Number the existing pull request.
type ExistingPullRequestError struct {
	Issue  int
	Head   string
	Number int
}

func (e *ExistingPullRequestError) Error() string {
	return fmt.Sprintf("issue #%d already has an open pull request from %s: #%d", e.Issue, e.Head, e.Number)
}

// NoOpenPullRequestError reports an amendment on a branch with no open pull
// request to revise.
type NoOpenPullRequestError struct {
	Issue int
	Head  string
}

func (e *NoOpenPullRequestError) Error() string {
	return fmt.Sprintf("issue #%d has no open pull request from %s", e.Issue, e.Head)
}

// AmbiguityKind names why an amendment could not select one pull request.
type AmbiguityKind string

const (
	// AmbiguousMatches is a head name that matches several open pull
	// requests — more than one base branch, or a fork's head.
	AmbiguousMatches AmbiguityKind = "several-matches"
	// AmbiguousHead is one match whose head repository is not the repository
	// being amended.
	AmbiguousHead AmbiguityKind = "foreign-head"
)

// AmbiguousPullRequestError reports an amendment that cannot safely select the
// pull request to revise. Mutation needs proof that the match is the only one
// and that its head belongs to the repository, or an edit could land on
// somebody else's pull request.
type AmbiguousPullRequestError struct {
	Kind           AmbiguityKind
	Head           string
	OpenCount      int
	Number         int
	HeadRepository string
	Repo           string
}

func (e *AmbiguousPullRequestError) Error() string {
	if e.Kind == AmbiguousHead {
		return fmt.Sprintf("cannot amend pull request #%d: head repository %q does not identify %s", e.Number, e.HeadRepository, e.Repo)
	}
	return fmt.Sprintf("cannot select an unambiguous pull request from %s: found %d open matches", e.Head, e.OpenCount)
}
