package tracker

import (
	"fmt"
	"slices"
	"strings"

	"github.com/lumberbarons/hew/internal/conventions"
)

// Body is one body to compose: structured sections for an issue, structured
// sections for a pull request, or text the caller already has — at most one
// source. A zero Body composes an empty body, which is what create does when
// no body flags are given.
//
// Composition is pure: the caller reads files and launches editors, and hands
// the bytes over as Text. Nothing here touches a store or the network.
type Body struct {
	// Sections compose the issue template: Where, Problem or Goal, Fix or
	// Approach, and Done when. Wording pairs are exclusive, and Done when
	// items must be non-blank.
	Sections Sections
	// PRSections compose the pull-request template: What, Why, Testing and
	// Risk.
	PRSections PRSections
	// Trailers are the closing references a pull request carries. With a
	// pull-request body they are enforced and any missing ones appended; with
	// issue sections they are refused.
	Trailers PRTrailers
	// Text is a body the caller already has — a file it read, an editor's
	// output, or an existing body being revalidated. It is used verbatim:
	// findings report what to improve and never rewrite it.
	Text string
	// DiscoveredFrom appends the link create's --discovered-from flag adds.
	// It applies to issue bodies only.
	DiscoveredFrom int
}

// PRSections are the pull-request body fields, mirroring the issue template:
// What replaces the issue's Fix/Approach, Why its Problem/Goal, Testing is the
// evidence an issue never carries, and Risk is the merge claim a reviewer
// weighs.
type PRSections struct {
	What    string
	Why     string
	Testing string
	Risk    string
}

// IsZero reports whether no pull-request section was provided.
func (s PRSections) IsZero() bool {
	return s.What == "" && s.Why == "" && s.Testing == "" && s.Risk == ""
}

// Compose renders the provided sections in template order, empty sections
// omitted, followed by the trailers — the same body `hew pr` writes.
func (s PRSections) Compose(t PRTrailers) string {
	return conventions.PRSections{
		What: s.What, Why: s.Why, Testing: s.Testing, Risk: s.Risk,
	}.Compose(conventions.PRTrailers{Fixes: t.Fixes, Epics: t.Epics})
}

// PRTrailers are the tracker links a pull-request body carries: one Fixes line
// per issue the pull request closes, plus each epic those issues hang off.
type PRTrailers struct {
	Fixes []int
	Epics []int
}

// Render returns the trailer block, empty when there is nothing to link.
func (t PRTrailers) Render() string {
	return conventions.PRTrailers{Fixes: t.Fixes, Epics: t.Epics}.Render()
}

func (t PRTrailers) isZero() bool { return len(t.Fixes) == 0 && len(t.Epics) == 0 }

// ComposedBody is the result of composing a body. Body is the text to publish;
// Findings are advisory and never fail the call or change the text.
type ComposedBody struct {
	Body     string
	Findings []Finding
}

// BodyCheck is the result of checking body text a caller already has. A check
// never rewrites the text: the caller keeps its bytes, and the findings say
// what to improve.
type BodyCheck struct {
	Findings []Finding
}

// FindingKind identifies an advisory finding about body text, so a caller can
// filter by cause and render its own sentence.
type FindingKind string

const (
	// FindingUnmarkedCode reports prose that looks like code and should be in
	// a backtick span. It is advice: the text is never rewritten.
	FindingUnmarkedCode FindingKind = "unmarked-code"
)

// Finding is one piece of advice about body text.
type Finding struct {
	Kind FindingKind
	// Tokens are the offending code-shaped tokens, de-duplicated and in order
	// of first appearance, for FindingUnmarkedCode.
	Tokens []string
}

// ValidateSections enforces the issue body's section rules: Problem and Goal
// are one wording pair, Fix and Approach another, and a Done when item cannot
// be blank. A rejection is an *InvalidInputError naming the offending field.
func ValidateSections(s Sections) error {
	if s.Problem != "" && s.Goal != "" {
		return &InvalidInputError{Field: "problem", Reason: "problem and goal are mutually exclusive"}
	}
	if s.Fix != "" && s.Approach != "" {
		return &InvalidInputError{Field: "fix", Reason: "fix and approach are mutually exclusive"}
	}
	for _, item := range s.DoneWhen {
		if strings.TrimSpace(item) == "" {
			return &InvalidInputError{Field: "done-when", Reason: "done-when items cannot be empty"}
		}
	}
	return nil
}

// ComposeBody renders a body from its structured sections or supplied text,
// enforcing the write path's rules first. Conflicting sources, a mixed
// issue/pull-request body, and a supplied pull-request body that closes a
// different set of issues are *InvalidInputError; markup findings ride on the
// result and never refuse it.
func ComposeBody(b Body) (ComposedBody, error) {
	text, err := composeBodyText(b)
	if err != nil {
		return ComposedBody{}, err
	}
	return ComposedBody{Body: text, Findings: findingsFor(text)}, nil
}

// CheckBody reports advisory findings for body text the caller already has. It
// never rewrites the text and never fails on a finding.
func CheckBody(text string) (BodyCheck, error) {
	return BodyCheck{Findings: findingsFor(text)}, nil
}

// composeBodyText validates and composes, without computing findings. PlanBody
// uses it too, so a plan entry's body and create's body cannot drift apart.
func composeBodyText(b Body) (string, error) {
	if err := validateBody(b); err != nil {
		return "", err
	}
	if !b.PRSections.IsZero() || !b.Trailers.isZero() {
		body := b.Text
		if !b.PRSections.IsZero() {
			body = b.PRSections.Compose(PRTrailers{})
		}
		return composePullRequestBody(body, b.Trailers)
	}
	body := b.Text
	if !b.Sections.IsZero() {
		body = b.Sections.Compose()
	}
	if b.DiscoveredFrom > 0 {
		link := conventions.DiscoveredFrom(b.DiscoveredFrom)
		if body == "" {
			body = link
		} else {
			body = strings.TrimRight(body, "\n") + "\n\n" + link
		}
	}
	return body, nil
}

func validateBody(b Body) error {
	if err := ValidateSections(b.Sections); err != nil {
		return err
	}
	switch {
	case !b.Sections.IsZero() && !b.PRSections.IsZero():
		return &InvalidInputError{Field: "body", Reason: "issue and pull-request sections are mutually exclusive"}
	case !b.Sections.IsZero() && b.Text != "":
		return &InvalidInputError{Field: "body", Reason: "body and section fields (where/problem/goal/fix/approach/done-when) are mutually exclusive"}
	case !b.PRSections.IsZero() && b.Text != "":
		return &InvalidInputError{Field: "body", Reason: "pull-request sections and body text are mutually exclusive"}
	case !b.Sections.IsZero() && !b.Trailers.isZero():
		return &InvalidInputError{Field: "body", Reason: "issue sections cannot carry pull-request trailers"}
	case b.DiscoveredFrom > 0 && (!b.PRSections.IsZero() || !b.Trailers.isZero()):
		return &InvalidInputError{Field: "body", Reason: "discovered-from applies to issue bodies only"}
	}
	return nil
}

// composePullRequestBody appends the declared closing references a supplied
// body is missing, and refuses one that closes a different set. The set is the
// tracker link the merge acts on, so it is enforced rather than repaired.
func composePullRequestBody(raw string, trailers PRTrailers) (string, error) {
	body := strings.TrimRight(raw, "\n")
	want := conventions.PRTrailers{Fixes: slices.Clone(trailers.Fixes), Epics: slices.Clone(trailers.Epics)}
	if refs := conventions.FixesReferences(body); len(refs) > 0 {
		if !sameClosingSet(refs, want.Fixes) {
			return "", closingSetError(refs, want.Fixes)
		}
		want.Fixes = nil
	}
	want.Epics = slices.DeleteFunc(want.Epics, func(epic int) bool {
		return strings.Contains(body, conventions.PartOfLine(epic))
	})
	if missing := want.Render(); missing != "" {
		if body != "" {
			body += "\n\n"
		}
		body += missing
	}
	return body, nil
}

// sameClosingSet compares as sets but refuses a repeated reference: the body
// that ships should read as exactly the declared issues.
func sameClosingSet(refs, want []int) bool {
	return len(refs) == len(want) && slices.Equal(slices.Sorted(slices.Values(refs)), slices.Sorted(slices.Values(want)))
}

func closingSetError(refs, want []int) error {
	if len(want) == 0 {
		return &InvalidInputError{Field: "trailers", Reason: fmt.Sprintf("closes %s but no closing issues were declared", formatNumbers(refs, " "))}
	}
	if len(want) == 1 && len(refs) > 1 {
		if distinct := slices.Compact(slices.Sorted(slices.Values(refs))); len(distinct) == 1 {
			return &InvalidInputError{Field: "trailers", Reason: fmt.Sprintf("closes #%d more than once; write the closing reference once", distinct[0])}
		}
		return &InvalidInputError{Field: "trailers", Reason: fmt.Sprintf("closes %d issues; a PR closes exactly one (declare a set with repeated --for)", len(refs))}
	}
	return &InvalidInputError{Field: "trailers", Reason: fmt.Sprintf("closes %s but this PR is for %s", formatNumbers(refs, " "), formatNumbers(want, " "))}
}

// findingsFor reports the advisory findings for a body. Only markup
// findings exist today, and they share the checker the CLI has always used.
func findingsFor(text string) []Finding {
	tokens := conventions.UnmarkedCodeText(text)
	if len(tokens) == 0 {
		return nil
	}
	return []Finding{{Kind: FindingUnmarkedCode, Tokens: tokens}}
}
