package pr

import (
	"slices"

	"github.com/lumberbarons/hew/internal/conventions"
	"github.com/lumberbarons/hew/tracker"
)

// Sections are the pull-request body's structured fields, mirroring the issue
// template: What replaces the issue's Fix/Approach, Why its Problem/Goal,
// Testing is the evidence an issue never carries, and Risk is the merge claim
// a reviewer weighs.
type Sections struct {
	What    string
	Why     string
	Testing string
	Risk    string
}

// IsZero reports whether no section was provided.
func (s Sections) IsZero() bool {
	return s.What == "" && s.Why == "" && s.Testing == "" && s.Risk == ""
}

func (s Sections) trackerSections() tracker.PRSections {
	return tracker.PRSections{What: s.What, Why: s.Why, Testing: s.Testing, Risk: s.Risk}
}

// Body is a pull-request body to compose: structured sections or text the
// caller already has, at most one source, plus the closing references the pull
// request carries. Supplied text is used verbatim apart from missing trailers;
// composition never rewrites the author's words.
type Body struct {
	// Sections compose the What / Why / Testing / Risk template.
	Sections Sections
	// Text is a body the caller already has — a file it read, or an existing
	// body being revalidated. It is mutually exclusive with Sections.
	Text string
	// Trailers are the closing references the body must carry. A body that
	// already closes a different set is refused rather than repaired.
	Trailers tracker.PRTrailers
}

// ComposedBody is the body to publish and the advisory findings about it.
// Findings never fail the call and never change the text.
type ComposedBody = tracker.ComposedBody

// BodyCheck is the advisory result of checking body text the caller already
// has.
type BodyCheck = tracker.BodyCheck

// ComposeBody renders a pull-request body, enforcing the closing-reference
// rule first: the body closes exactly the declared set, and the missing
// trailers are appended. A conflicting source or a mismatched closing set is a
// *tracker.InvalidInputError; unmarked code is an advisory finding on the
// result.
func ComposeBody(b Body) (ComposedBody, error) {
	return tracker.ComposeBody(tracker.Body{
		PRSections: b.Sections.trackerSections(),
		Text:       b.Text,
		Trailers:   b.Trailers,
	})
}

// CheckBody reports advisory findings for body text the caller already has. It
// never rewrites the text and never fails on a finding.
func CheckBody(text string) (BodyCheck, error) {
	return tracker.CheckBody(text)
}

// trailers are the closing references a pull request for linked carries: one
// Fixes line per issue, sorted so the block reads the same however a declared
// set was ordered, plus each epic those issues hang off.
func trailers(linked []tracker.Issue) tracker.PRTrailers {
	out := tracker.PRTrailers{Fixes: slices.Sorted(slices.Values(issueNumbers(linked)))}
	for _, issue := range linked {
		if issue.Parent != nil && !slices.Contains(out.Epics, *issue.Parent) {
			out.Epics = append(out.Epics, *issue.Parent)
		}
	}
	return out
}

// issueBody is an issue's body, empty when the read carried none.
func issueBody(issue tracker.Issue) string {
	if issue.Body == nil {
		return ""
	}
	return *issue.Body
}

// composeCreate builds the created pull request's body: the declared sections
// with What and Why defaulted from the issue when nothing supplies them. A set
// gets no defaults — one issue's Fix says nothing true about the others — and
// is asked for its own text instead.
func composeCreate(linked []tracker.Issue, body Body) (ComposedBody, []Warning, error) {
	body.Trailers = trailers(linked)
	sections := body.Sections
	var warnings []Warning
	if body.Text == "" {
		if len(linked) == 1 {
			if sections.What == "" {
				sections.What = conventions.FirstSection(issueBody(linked[0]), "Fix", "Approach")
			}
			if sections.Why == "" {
				sections.Why = conventions.FirstSection(issueBody(linked[0]), "Problem", "Goal")
			}
		} else if sections.What == "" || sections.Why == "" {
			warnings = append(warnings, Warning{Kind: WarnNoSectionDefaults, Issues: issueNumbers(linked)})
		}
	}
	composed, err := ComposeBody(Body{Sections: sections, Text: body.Text, Trailers: body.Trailers})
	if err != nil {
		return ComposedBody{}, nil, err
	}
	return composed, warnings, nil
}

// bodyWarnings reports the body contract at composition time. Help is read by
// someone who goes looking, so the sections a reviewer needs are checked on
// every path that writes a body. Risk is required only once the pull request
// is ready for review: a draft may legitimately be incomplete.
func bodyWarnings(body string, requireRisk bool) []Warning {
	var out []Warning
	if conventions.ReviewSection(body, "Testing") == "" {
		out = append(out, Warning{Kind: WarnMissingTesting})
	}
	if requireRisk && conventions.ReviewSection(body, "Risk") == "" {
		out = append(out, Warning{Kind: WarnMissingRisk})
	}
	return out
}

// claimWarnings flags every linked issue the acting identity does not hold,
// and a set whose members share nothing.
func claimWarnings(linked []tracker.Issue, actor string) []Warning {
	var out []Warning
	for _, issue := range linked {
		switch {
		case len(issue.Assignees) == 0:
			out = append(out, Warning{Kind: WarnUnclaimed, Issue: issue.Number, Actor: actor})
		case !slices.Contains(issue.Assignees, actor):
			out = append(out, Warning{
				Kind: WarnUnclaimed, Issue: issue.Number,
				Assignees: slices.Clone(issue.Assignees), Actor: actor,
			})
		}
	}
	if groups := relatedGroups(linked); len(groups) > 1 {
		out = append(out, Warning{Kind: WarnUnrelatedSet, Groups: groups})
	}
	return out
}

// prTitle derives the title convention's default. A squashed merge makes the
// pull-request title the commit subject, so it carries the prefix of the
// issue's type; an untyped issue has nothing to derive one from and is left
// unprefixed rather than filed under an invented heading.
func prTitle(primary tracker.Issue) string {
	issueType := ""
	if primary.Type != nil {
		issueType = *primary.Type
	}
	return conventions.PRTitle(issueType, primary.Title)
}
