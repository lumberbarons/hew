package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/lumberbarons/hew/internal/conventions"
	"github.com/lumberbarons/hew/internal/render"
	"github.com/lumberbarons/hew/pr"
	"github.com/lumberbarons/hew/tracker"
)

// PROpts are the pr command's inputs. Every section is optional: the point
// of the command is that tracker state fills the gaps.
type PROpts struct {
	// Amend revises the open PR on this branch instead of creating one.
	Amend bool
	// SectionFlags records explicitly supplied flags, including empty values.
	SectionFlags []string
	// For overrides issue inference; empty means infer. Several values are a
	// deliberate multi-issue PR: only declared issues are ever combined,
	// never inferred ones.
	For      []int
	Title    string
	Sections conventions.PRSections
	BodyFile string
	// Ready opens the PR for review instead of as a draft.
	Ready bool
	// Base overrides the target branch; empty means the repo default.
	Base string
}

// PR opens or revises the pull request that closes the claim lifecycle. The
// workflow — issue inference, body composition, guards and the API writes —
// lives in the public pr package; this adapter reads the inputs the user chose,
// renders the result, and maps the library's typed refusals onto exit codes.
// Reviews, merges and checks stay with `gh`.
func (a *App) PR(ctx context.Context, opts PROpts) error {
	if err := validatePROpts(opts); err != nil {
		return err
	}
	if a.PRs == nil {
		return genericErr("pr needs the GitHub backend; this repo tracks issues locally")
	}
	if a.Git == nil {
		return genericErr("pr needs a git checkout; it is not available here")
	}
	state, err := a.Git()
	if err != nil {
		return genericErr("%v", err)
	}

	client := pr.Wrap(a.Client, a.PRs, a.Repo, state)
	if opts.Amend {
		return a.amendPR(ctx, client, opts)
	}
	return a.createPR(ctx, client, opts)
}

// validatePROpts enforces the command's flag rules before any read: the body
// sources are exclusive, and --amend edits the title and body only.
func validatePROpts(opts PROpts) error {
	if opts.BodyFile != "" && (!opts.Sections.IsZero() || len(opts.SectionFlags) > 0) {
		return usageErr("section flags (--what/--why/--testing) and --body-file are mutually exclusive")
	}
	if opts.Amend && (opts.Ready || opts.Base != "") {
		return usageErr("--amend cannot be combined with --ready or --base; it edits the title and body only")
	}
	return nil
}

func (a *App) createPR(ctx context.Context, client *pr.Client, opts PROpts) error {
	body, err := a.prBody(opts)
	if err != nil {
		return err
	}
	result, err := client.Create(ctx, pr.CreateRequest{
		For: opts.For, Title: opts.Title, Body: body, Ready: opts.Ready, Base: opts.Base,
	})
	a.warnPRWarnings(result.Warnings)
	a.warnFindings(result.Findings, "hew pr --amend")
	if err != nil {
		return a.prErr(err, source(opts))
	}
	kind := "draft PR"
	if !result.Draft {
		kind = "PR"
	}
	return a.emitResult(prJSON{
		Number: result.Number, URL: result.URL, Draft: result.Draft,
		Fixes: result.Fixes, FixedIssues: result.FixedIssues,
		Head: result.Head, Base: result.Base, Title: result.Title,
	}, func() {
		// The summary an agent branches on, then the URL a human clicks. The
		// title may carry the issue's own (GitHub-derived) text, so the printed
		// line needs the same neutralizing the JSON/API copy does not.
		a.printf("created %s #%d for %s: %s\n", kind, result.Number,
			formatRefs(result.FixedIssues), render.SanitizeInline(result.Title))
		a.printf("%s\n", result.URL)
	})
}

func (a *App) amendPR(ctx context.Context, client *pr.Client, opts PROpts) error {
	edit := pr.BodyEdit{}
	if opts.BodyFile != "" {
		text, err := a.readBodyFile(opts.BodyFile)
		if err != nil {
			return err
		}
		edit.Text = &text
	} else {
		// Presence, not value: an explicitly empty flag removes the section,
		// while an omitted one keeps its existing value. A non-empty value
		// applies even without its flag, which is how a library caller states
		// an edit and how the command has always read its sections.
		if slices.Contains(opts.SectionFlags, "what") || opts.Sections.What != "" {
			edit.What = &opts.Sections.What
		}
		if slices.Contains(opts.SectionFlags, "why") || opts.Sections.Why != "" {
			edit.Why = &opts.Sections.Why
		}
		if slices.Contains(opts.SectionFlags, "testing") || opts.Sections.Testing != "" {
			edit.Testing = &opts.Sections.Testing
		}
		if slices.Contains(opts.SectionFlags, "risk") || opts.Sections.Risk != "" {
			edit.Risk = &opts.Sections.Risk
		}
	}
	var title *string
	if opts.Title != "" {
		title = &opts.Title
	}

	result, err := client.Amend(ctx, pr.AmendRequest{For: opts.For, Title: title, Edit: edit})
	a.warnPRWarnings(result.Warnings)
	a.warnFindings(result.Findings, "hew pr --amend")
	if err != nil {
		return a.prErr(err, source(opts))
	}
	return a.emitResult(prJSON{
		Number: result.Number, URL: result.URL, Draft: result.Draft,
		Fixes: result.Fixes, FixedIssues: result.FixedIssues,
		Head: result.Head, Base: result.Base, Title: result.Title,
	}, func() {
		a.printf("updated PR #%d for %s: %s\n", result.Number,
			formatRefs(result.FixedIssues), render.SanitizeInline(result.Title))
		a.printf("%s\n", result.URL)
	})
}

// prBody is the create request's body source: the section flags, or the file
// the user chose. Reading the file is the adapter's; composing and enforcing
// the closing set is the library's.
func (a *App) prBody(opts PROpts) (pr.Body, error) {
	body := pr.Body{Sections: pr.Sections{
		What: opts.Sections.What, Why: opts.Sections.Why,
		Testing: opts.Sections.Testing, Risk: opts.Sections.Risk,
	}}
	if opts.BodyFile != "" {
		text, err := a.readBodyFile(opts.BodyFile)
		if err != nil {
			return pr.Body{}, err
		}
		body.Text = text
	}
	return body, nil
}

func (a *App) readBodyFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", genericErr("cannot read --body-file: %v", err)
	}
	return string(raw), nil
}

// source names the input that supplied the body, so a composition refusal
// points at the flag the user typed.
func source(opts PROpts) string {
	if opts.BodyFile != "" {
		return "--body-file"
	}
	return "PR body"
}

// prErr renders a typed workflow refusal in the command's words and maps it
// onto the exit code agent loops branch on. The library decides; only the flag
// spellings and sentences live here.
func (a *App) prErr(err error, bodySource string) error {
	var (
		notFound   *tracker.NotFoundError
		epic       *tracker.EpicError
		inference  *pr.IssueInferenceError
		existing   *pr.ExistingPullRequestError
		noOpen     *pr.NoOpenPullRequestError
		ambiguous  *pr.AmbiguousPullRequestError
		noUpstream *pr.NoUpstreamError
		noPRs      *pr.NoPullRequestsError
		invalid    *tracker.InvalidInputError
	)
	switch {
	case errors.As(err, &noPRs):
		return genericErr("pr needs the GitHub backend; this repo tracks issues locally")
	case errors.As(err, &noUpstream):
		return genericErr("branch %s has no upstream; push it first: git push -u origin %s",
			noUpstream.Branch, noUpstream.Branch)
	case errors.As(err, &notFound):
		return genericErr("#%d is not an open issue in %s", notFound.Issue, a.Repo)
	case errors.As(err, &epic):
		return genericErr("#%d is an epic; PRs close sub-issues, never the epic", epic.Issue)
	case errors.As(err, &inference):
		return usageErr("%s", a.inferenceReason(inference))
	case errors.As(err, &existing):
		return genericErr("#%d already has an open PR from %s: #%d — push to the branch or use --amend to revise its title/body",
			existing.Issue, existing.Head, existing.Number)
	case errors.As(err, &noOpen):
		return genericErr("#%d has no open PR from %s; omit --amend to create one", noOpen.Issue, noOpen.Head)
	case errors.As(err, &ambiguous):
		if ambiguous.Kind == pr.AmbiguousHead {
			return genericErr("cannot amend PR #%d: head repository %q does not identify %s; use gh pr edit with an explicit PR number",
				ambiguous.Number, ambiguous.HeadRepository, ambiguous.Repo)
		}
		return genericErr("cannot select an unambiguous PR from %s: found %d open matches; use gh pr edit with an explicit PR number",
			ambiguous.Head, ambiguous.OpenCount)
	case errors.As(err, &invalid):
		switch invalid.Field {
		case "trailers":
			// The closing set is the tracker link the merge acts on, so the
			// refusal names the input that supplied the body.
			return usageErr("%s %s", bodySource, invalid.Reason)
		case pr.FieldBodyEdit:
			return usageErr("cannot safely parse this PR body for section edits; use --amend --body-file to replace the whole body")
		case pr.FieldBase:
			return genericErr("%s (use --base)", invalid.Reason)
		case pr.FieldHead:
			return genericErr("%s (git checkout -b feat/…)", invalid.Reason)
		}
		return usageErr("%s", invalid.Reason)
	}
	return err
}

// inferenceReason spells the library's inference refusal with the flag that
// resolves it. Guessing is not offered: a wrong Fixes line closes the wrong
// issue on merge.
func (a *App) inferenceReason(e *pr.IssueInferenceError) string {
	switch e.Failure {
	case pr.InferenceConflict:
		claimed := 0
		if len(e.Claimed) > 0 {
			claimed = e.Claimed[0]
		}
		return fmt.Sprintf("branch %s names #%d but you have #%d claimed; say which with --for <n>",
			e.Branch, e.BranchIssue, claimed)
	case pr.InferenceAmbiguous:
		return fmt.Sprintf("you have %d issues claimed (%s); say which with --for <n>",
			len(e.Claimed), formatRefs(e.Claimed))
	default:
		return fmt.Sprintf("cannot tell which issue %s is for: nothing claimed by @%s; pass --for <n>",
			e.Branch, e.Actor)
	}
}

// warnPRWarnings renders the workflow's advisory notices. The library keeps
// the fact (a kind and its fields); the sentence and its flag spellings are
// the CLI's.
func (a *App) warnPRWarnings(warnings []pr.Warning) {
	for _, w := range warnings {
		switch w.Kind {
		case pr.WarnBranchPrefix:
			a.warnf("branch %s has no %s prefix", w.Branch, strings.Join(conventions.BranchPrefixes, "|"))
		case pr.WarnUnclaimed:
			if len(w.Assignees) == 0 {
				a.warnf("#%d is not claimed by you (hew start %d)", w.Issue, w.Issue)
				continue
			}
			a.warnf("#%d is claimed by @%s, not you", w.Issue, render.SanitizeInline(strings.Join(w.Assignees, " @")))
		case pr.WarnUnrelatedSet:
			parts := make([]string, len(w.Groups))
			for i, group := range w.Groups {
				parts[i] = formatRefs(group)
			}
			a.warnf("issues %s look unrelated (no shared parent, area label or dependency); keep a combined PR reviewable as one change",
				strings.Join(parts, " | "))
		case pr.WarnNoSectionDefaults:
			a.warnf("%s share one PR, so What/Why have no default: pass --what and --why", formatRefs(w.Issues))
		case pr.WarnPrimaryTitle:
			a.warnf("title comes from #%d alone; pass --title to name the whole change", w.Issue)
		case pr.WarnMissingTesting:
			a.warnf("no Testing section: %s", conventions.PRTestingGuidance)
		case pr.WarnMissingRisk:
			a.warnf("no Risk section: %s", conventions.PRRiskGuidance)
		}
	}
}

// prJSON is the pr command's --json result: flat, and enough for an agent
// to follow up without a second call.
type prJSON struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	Draft  bool   `json:"draft"`
	// Fixes is the primary issue and stays a scalar so existing consumers
	// keep working; FixedIssues is the whole set the PR closes, primary first.
	Fixes       int    `json:"fixes"`
	FixedIssues []int  `json:"fixedIssues"`
	Head        string `json:"head"`
	Base        string `json:"base"`
	Title       string `json:"title"`
}

// formatRefs renders issue numbers as "#1 #2", the form every line here reads
// in.
func formatRefs(numbers []int) string {
	parts := make([]string, len(numbers))
	for i, n := range numbers {
		parts[i] = fmt.Sprintf("#%d", n)
	}
	return strings.Join(parts, " ")
}
