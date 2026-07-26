package conventions

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// BranchPrefixes are the branch-name prefixes the workflow prescribes. A
// branch outside this set isn't refused — the work is already committed by
// the time a PR is opened — but it is warned about.
var BranchPrefixes = []string{"feat/", "fix/", "chore/", "docs/"}

// IsConventionalBranch reports whether a branch name carries one of the
// prescribed prefixes.
func IsConventionalBranch(branch string) bool {
	for _, p := range BranchPrefixes {
		if strings.HasPrefix(branch, p) {
			return true
		}
	}
	return false
}

// CommitPrefixes are the conventional-commit prefixes a PR title may carry.
// The release changelog is grouped by them, so they are the reason the tool
// has an opinion about titles at all.
var CommitPrefixes = []string{"feat", "fix", "chore", "docs", "refactor", "test", "perf", "build", "ci", "style", "revert"}

// prefixForType maps a type label to the prefix that says the same thing.
// The write path guarantees exactly one type label, so the mapping is total
// for anything this tool filed.
var prefixForType = map[string]string{
	"bug":         "fix",
	"enhancement": "feat",
	"task":        "chore",
}

// commitPrefix matches a leading conventional-commit prefix, with its
// optional scope and breaking-change marker: "feat: ", "fix(cli)!: ".
var commitPrefix = regexp.MustCompile(`^(` + strings.Join(CommitPrefixes, "|") + `)(\([^)]*\))?!?: `)

// HasCommitPrefix reports whether a title already carries a prefix.
func HasCommitPrefix(title string) bool {
	return commitPrefix.MatchString(strings.ToLower(title))
}

// PRTitle derives a PR title from the issue's type and title. A squash merge
// makes the PR title the commit subject, and the changelog is grouped by
// conventional-commit prefix — so a PR titled with the issue's own words
// lands the work under "Other" (#44). Issue titles deliberately carry no
// prefix (the type label holds that), which makes the type exactly the
// information the prefix encodes. An untyped issue gets no prefix: there is
// nothing to derive one from, and inventing one is worse than none.
func PRTitle(issueType, title string) string {
	prefix, ok := prefixForType[issueType]
	if !ok || HasCommitPrefix(title) {
		return title
	}
	return prefix + ": " + title
}

// PRSections are the pull-request body fields, mirroring the issue body
// template: What replaces the issue's Fix/Approach, Why its Problem/Goal,
// and Testing is the half an issue never carries.
type PRSections struct {
	What    string
	Why     string
	Testing string
}

// IsZero reports whether no section was provided.
func (s PRSections) IsZero() bool {
	return s.What == "" && s.Why == "" && s.Testing == ""
}

// PRTrailers are the tracker links that close the claim lifecycle: exactly
// one Fixes line so a merge closes the issue, plus the epic the issue hangs
// off, when it has one.
type PRTrailers struct {
	Fixes int
	Epic  int
}

// FixesLine is the closing keyword GitHub links a merged PR to its issue by.
// Rendered in one place so the read-back check and the composed body can
// never drift apart.
func FixesLine(n int) string { return fmt.Sprintf("Fixes #%d", n) }

// PartOfLine names the epic a sub-issue's PR belongs to.
func PartOfLine(n int) string { return fmt.Sprintf("Part of #%d", n) }

// Render returns the trailer block, empty when there is nothing to link.
// Both body paths go through it: the composed one renders the whole set,
// the --body-file one renders whatever the author didn't already write, so
// the escape hatch can't quietly lose a link the template would have made.
func (t PRTrailers) Render() string {
	var lines []string
	if t.Fixes > 0 {
		lines = append(lines, FixesLine(t.Fixes))
	}
	if t.Epic > 0 {
		lines = append(lines, PartOfLine(t.Epic))
	}
	return strings.Join(lines, "\n")
}

// Compose renders the PR body: the provided sections in template order,
// then the trailers. Empty sections are omitted rather than left as bare
// headers, matching the issue template's rule.
func (s PRSections) Compose(t PRTrailers) string {
	var b strings.Builder
	section := func(header, content string) {
		if strings.TrimSpace(content) == "" {
			return
		}
		fmt.Fprintf(&b, "### %s\n\n%s\n\n", header, strings.TrimSpace(content))
	}
	section("What", s.What)
	section("Why", s.Why)
	section("Testing", s.Testing)
	b.WriteString(t.Render())
	return strings.TrimSpace(b.String())
}

// FixesReferences returns the issue numbers a body already closes, in order
// of appearance, so the write path can enforce exactly one. It matches the
// closing keywords GitHub itself acts on (case-insensitively), not just the
// one this tool writes — a hand-written "Closes #7" links the PR just as
// hard as "Fixes #7", and silently adding a second link would close two
// issues on merge.
func FixesReferences(body string) []int {
	var out []int
	// The number is the field after the keyword; scanning fields rather than
	// regexing the raw text keeps "prefixes #7" from matching.
	fields := strings.Fields(body)
	for i, word := range fields {
		if !isClosingKeyword(word) || i+1 >= len(fields) {
			continue
		}
		if n, ok := issueRef(fields[i+1]); ok {
			out = append(out, n)
		}
	}
	return out
}

// closingKeywords are GitHub's issue-closing keywords.
var closingKeywords = []string{
	"close", "closes", "closed",
	"fix", "fixes", "fixed",
	"resolve", "resolves", "resolved",
}

func isClosingKeyword(word string) bool {
	return slices.Contains(closingKeywords, strings.ToLower(strings.Trim(word, "*_`")))
}

// issueRef parses a "#123" reference, tolerating trailing punctuation.
func issueRef(word string) (int, bool) {
	w := strings.TrimRight(strings.Trim(word, "*_`"), ".,;:)")
	if !strings.HasPrefix(w, "#") {
		return 0, false
	}
	n := 0
	digits := w[1:]
	if digits == "" {
		return 0, false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}

// Section extracts the content under a "### Name" header from an issue
// body, empty when absent. It lets the PR body be composed from what the
// issue already says instead of asking an agent to retype it.
func Section(body, name string) string {
	header := "### " + name
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if !strings.EqualFold(strings.TrimSpace(line), header) {
			continue
		}
		j := i + 1
		for j < len(lines) && !strings.HasPrefix(lines[j], "### ") {
			j++
		}
		return strings.TrimSpace(strings.Join(lines[i+1:j], "\n"))
	}
	return ""
}

// FirstSection returns the first non-empty of the named sections, so a
// caller can ask for a wording pair (Fix or Approach) in one go.
func FirstSection(body string, names ...string) string {
	for _, name := range names {
		if s := Section(body, name); s != "" {
			return s
		}
	}
	return ""
}
