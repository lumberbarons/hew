package pr

import (
	"context"
	"strings"

	"github.com/lumberbarons/hew/internal/conventions"
	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/tracker"
)

// BodyEdit is an amendment's body change. Every field is optional: a nil
// section pointer preserves the existing value, a pointer to the empty string
// removes that section, and Text replaces the whole body. Text and the section
// pointers are mutually exclusive.
type BodyEdit struct {
	What    *string
	Why     *string
	Testing *string
	Risk    *string
	Text    *string
}

// IsZero reports whether the edit changes no part of the body.
func (e BodyEdit) IsZero() bool {
	return e.What == nil && e.Why == nil && e.Testing == nil && e.Risk == nil && e.Text == nil
}

func (e BodyEdit) hasSections() bool {
	return e.What != nil || e.Why != nil || e.Testing != nil || e.Risk != nil
}

// AmendRequest configures Amend.
type AmendRequest struct {
	// For declares the issues the pull request closes, primary first. Empty
	// infers exactly one from the branch and the acting identity's claim.
	For []int
	// Title replaces the pull-request title when set; nil preserves it.
	Title *string
	// Edit changes the body. The zero value edits nothing, which is how a
	// title-only amendment preserves even a free-form body.
	Edit BodyEdit
}

// AmendResult is the pull request an amendment revised.
type AmendResult = CreateResult

// Amend revises the open pull request on the branch: the title when one is
// given, the named body sections, or the whole body. Omitted sections keep
// their existing values, and the closing references are re-checked against the
// declared issue set so an edit cannot quietly change what the merge closes.
//
// Amendment requires exactly one open pull request whose head repository is
// this repository; the API's head-name match alone also covers fork heads and
// several base branches, and editing one of those would be somebody else's pull
// request.
func (c *Client) Amend(ctx context.Context, req AmendRequest) (AmendResult, error) {
	p, err := c.prepare(ctx, req.For)
	if err != nil {
		return AmendResult{}, err
	}
	primary := p.linked[0]
	existing := p.prCtx.Existing
	if existing == nil {
		return AmendResult{}, &NoOpenPullRequestError{Issue: primary.Number, Head: p.head}
	}
	if p.prCtx.OpenCount != 1 {
		return AmendResult{}, &AmbiguousPullRequestError{
			Kind: AmbiguousMatches, Head: p.head, OpenCount: p.prCtx.OpenCount,
		}
	}
	if !strings.EqualFold(existing.HeadRepository, c.repoName()) {
		return AmendResult{}, &AmbiguousPullRequestError{
			Kind: AmbiguousHead, Head: p.head, Number: existing.Number,
			HeadRepository: existing.HeadRepository, Repo: c.repoName(),
		}
	}

	body, findings, warnings, err := amendBody(req.Edit, *existing, p.linked)
	if err != nil {
		return AmendResult{}, err
	}
	title := existing.Title
	if req.Title != nil {
		title = *req.Title
	}
	if err := c.prs.UpdatePullRequest(ctx, existing.Number, title, body); err != nil {
		return AmendResult{Warnings: warnings, Findings: findings}, asAuthError(err)
	}
	return AmendResult{
		Number: existing.Number, URL: existing.URL, Draft: existing.Draft,
		Fixes: primary.Number, FixedIssues: issueNumbers(p.linked),
		Head: p.head, Base: existing.Base, Title: title,
		Warnings: warnings, Findings: findings,
	}, nil
}

// amendBody resolves the body an amendment writes and the advice that rides on
// it. A title-only amendment still composes the existing body: that is what
// re-checks the closing set and reports unmarked code, and it is also what
// appends a tracker link the body is missing.
func amendBody(edit BodyEdit, existing gh.PullRequest, linked []tracker.Issue) (string, []tracker.Finding, []Warning, error) {
	links := trailers(linked)
	switch {
	case edit.Text != nil && edit.hasSections():
		return "", nil, nil, invalid(FieldBody, "body text and section fields are mutually exclusive")
	case edit.Text != nil:
		composed, err := ComposeBody(Body{Text: *edit.Text, Trailers: links})
		if err != nil {
			return "", nil, nil, err
		}
		return composed.Body, composed.Findings, bodyWarnings(composed.Body, !existing.Draft), nil
	case edit.IsZero():
		composed, err := ComposeBody(Body{Text: existing.Body, Trailers: links})
		if err != nil {
			return "", nil, nil, err
		}
		return composed.Body, composed.Findings, nil, nil
	default:
		composed, err := editSections(edit, existing.Body, links)
		if err != nil {
			return "", nil, nil, err
		}
		return composed.Body, composed.Findings, bodyWarnings(composed.Body, !existing.Draft), nil
	}
}

// editSections rewrites only the named sections of an existing body, keeping
// every other section and the author's wording. Section reads to the next
// heading, so the generated trailer block is detached before reading the final
// section; other closing links remain in the content and are checked along with
// the replacement sections.
func editSections(edit BodyEdit, existing string, links tracker.PRTrailers) (ComposedBody, error) {
	content := existing
	// Our own trailer block is regenerated, so detaching it keeps a trailing
	// section readable. Both shapes are tried because an earlier version wrote
	// the Fixes lines without the epic ones.
	fixesOnly := tracker.PRTrailers{Fixes: links.Fixes}
	for _, suffix := range []string{links.Render(), fixesOnly.Render()} {
		content = strings.TrimSpace(content)
		if content == suffix {
			content = ""
		} else {
			content = strings.TrimSuffix(content, "\n\n"+suffix)
		}
	}
	preserved := Sections{
		What:    conventions.Section(content, "What"),
		Why:     conventions.Section(content, "Why"),
		Testing: conventions.Section(content, "Testing"),
		Risk:    conventions.Section(content, "Risk"),
	}
	// Section is a simple template reader, not a Markdown parser. Require an
	// exact round trip before editing, and refuse fences even when an example's
	// headings happen to look like the canonical template.
	safe := preserved.trackerSections().Compose(tracker.PRTrailers{}) == content
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			safe = false
		}
	}
	if !safe {
		return ComposedBody{}, invalid(FieldBodyEdit,
			"cannot safely parse this pull request body for section edits")
	}
	sections := preserved
	if edit.What != nil {
		sections.What = *edit.What
	}
	if edit.Why != nil {
		sections.Why = *edit.Why
	}
	if edit.Testing != nil {
		sections.Testing = *edit.Testing
	}
	if edit.Risk != nil {
		sections.Risk = *edit.Risk
	}
	return ComposeBody(Body{Sections: sections, Trailers: links})
}
