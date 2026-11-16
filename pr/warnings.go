package pr

// WarningKind identifies one advisory notice about a pull-request operation,
// so a caller can filter by cause and render its own sentence. A warning never
// fails the call: the work is already committed by the time one fires.
type WarningKind string

const (
	// WarnBranchPrefix reports a head branch outside the prescribed prefixes.
	WarnBranchPrefix WarningKind = "branch-prefix"
	// WarnUnclaimed reports a linked issue the acting identity does not hold.
	// Assignees is empty for an unclaimed issue and names the other claimant
	// otherwise.
	WarnUnclaimed WarningKind = "unclaimed"
	// WarnUnrelatedSet reports declared issues with no shared parent, area
	// label or dependency. Whether a combined pull request stays reviewable
	// is a judgement the library cannot make, so it advises instead.
	WarnUnrelatedSet WarningKind = "unrelated-set"
	// WarnNoSectionDefaults reports a declared set whose What and Why have no
	// default: one issue's Fix does not describe the others.
	WarnNoSectionDefaults WarningKind = "no-section-defaults"
	// WarnPrimaryTitle reports a derived title that comes from the primary
	// issue alone while the pull request closes several.
	WarnPrimaryTitle WarningKind = "primary-title"
	// WarnMissingTesting reports a body with no Testing section.
	WarnMissingTesting WarningKind = "missing-testing"
	// WarnMissingRisk reports a review-ready body with no Risk section.
	WarnMissingRisk WarningKind = "missing-risk"
)

// Warning is one advisory notice. The fields a kind uses are documented on
// that kind; the rest stay zero.
type Warning struct {
	Kind WarningKind
	// Issue is the subject of WarnUnclaimed and WarnPrimaryTitle.
	Issue int
	// Issues is the declared set, primary first, for WarnNoSectionDefaults.
	Issues []int
	// Branch is the head a WarnBranchPrefix is about.
	Branch string
	// Assignees and Actor describe the claim WarnUnclaimed found.
	Assignees []string
	Actor     string
	// Groups partitions the declared set for WarnUnrelatedSet.
	Groups [][]int
}
