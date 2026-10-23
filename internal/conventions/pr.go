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
// Testing is the half an issue never carries, and Risk is the merge claim a
// reviewer weighs after reading the change.
type PRSections struct {
	What    string
	Why     string
	Testing string
	Risk    string
}

// PRTestingGuidance is the short form carried by the missing-Testing warning.
// Help is read by someone who goes looking; the agent composing a body
// generally does not, so the rule is also stated at the moment it applies.
// It says how to lay the evidence out as well as what to prove, because a
// section written as one run-on paragraph renders as a dense block on GitHub.
const PRTestingGuidance = `give Before: and After: each its own line — the test or output that failed, then the same thing passing — then a short list of the checks run; "tests pass" is a claim, not evidence`

// PRRiskGuidance is the short form carried by the missing-Risk warning.
const PRRiskGuidance = `give a Reversibility: line (one-way or two-way door) and a Blast radius: line (what breaks if this is wrong), with anything else on its own line`

// PRBodyGuidance is the body contract stated on the pr help surface. It sits
// next to Compose so the template and its explanation cannot drift, and it
// embeds the warning constants so the layout advice has one source; the primer
// carries only the short form.
const PRBodyGuidance = `Body: ### What / ### Why / ### Testing, plus ### Risk when a reviewer must
weigh the merge. What and Why default to the issue's Fix/Approach and Problem/Goal.
What, Why, Testing and Risk repeat: several values become a bullet list, one
value stays a paragraph. Empty sections are omitted.

Testing: ` + PRTestingGuidance + `.
Risk: ` + PRRiskGuidance + `.
Repeat the flag once per line.

For a diff sketch, file tree or diagram, compose the body in a file and pass
--body-file; the section flags are for short text.`

// PRList renders the values of a repeatable section flag. One value is
// returned verbatim, so a caller that passes a single string gets the same
// body it always did; several become a markdown bullet list, one item per
// value. Blank values are dropped; a leading "- " or "* " the author already
// wrote is stripped so it does not nest as "- - item"; and a continuation line
// is indented under its bullet so it stays part of that item.
func PRList(values []string) string {
	if len(values) == 1 {
		return values[0]
	}
	var items []string
	for _, v := range values {
		v = strings.TrimSpace(v)
		if rest, ok := cutBullet(v); ok {
			v = rest
		}
		if v != "" {
			items = append(items, v)
		}
	}
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	var b strings.Builder
	for _, item := range items {
		lines := strings.Split(item, "\n")
		fmt.Fprintf(&b, "- %s\n", lines[0])
		for _, line := range lines[1:] {
			// A blank line stays empty rather than becoming trailing whitespace.
			if strings.TrimSpace(line) != "" {
				line = "  " + line
			} else {
				line = ""
			}
			b.WriteString(line + "\n")
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// cutBullet removes one leading markdown bullet marker from an item.
func cutBullet(s string) (string, bool) {
	for _, marker := range []string{"- ", "* "} {
		if rest, ok := strings.CutPrefix(s, marker); ok {
			return strings.TrimSpace(rest), true
		}
	}
	return s, false
}

// IsZero reports whether no section was provided.
func (s PRSections) IsZero() bool {
	return s.What == "" && s.Why == "" && s.Testing == "" && s.Risk == ""
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
	section("Risk", s.Risk)
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

// templateSections are the PR body sections a bold label can stand in for.
var templateSections = []string{"What", "Why", "Testing", "Risk"}

// ReviewSection extracts the content under a section from a body nobody
// templated, such as --body-file. Section trusts a "### Name" line; this reads
// structure instead: a heading of any level or a bold label opens the
// section, any heading or a bold label naming another template section ends
// it, and fenced or indented code is never a heading. Closing/epic trailer
// lines are skipped rather than read as content. Every occurrence counts, so
// an empty first heading does not hide a real later one.
func ReviewSection(body, name string) string {
	var content []string
	var fence string
	inSection := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if fence == "" && (strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t")) {
			// Four spaces make an indented code block, whatever it looks like.
			if inSection {
				content = append(content, line)
			}
			continue
		}
		if fence != "" {
			if strings.HasPrefix(trimmed, fence) && strings.Trim(trimmed, fence[:1]) == "" {
				fence = ""
			}
			if inSection {
				content = append(content, line)
			}
			continue
		}
		if marker := openingFence(trimmed); marker != "" {
			fence = marker
			if inSection {
				content = append(content, line)
			}
			continue
		}
		if title, ok := headingTitle(trimmed); ok {
			inSection = strings.EqualFold(title, name)
			continue
		}
		// A bold line opens a section only when it names one; other bold
		// lines ("**Before:**") are the author's content.
		if label, ok := boldLabel(trimmed); ok && slices.ContainsFunc(templateSections, func(s string) bool { return strings.EqualFold(s, label) }) {
			inSection = strings.EqualFold(label, name)
			continue
		}
		if isTrailerLine(trimmed) {
			continue
		}
		if inSection {
			content = append(content, line)
		}
	}
	return strings.TrimSpace(strings.Join(content, "\n"))
}

// openingFence returns the run of backticks or tildes that opens a fenced code
// block on this line, empty when it does not open one.
func openingFence(line string) string {
	for _, c := range []string{"`", "~"} {
		run := len(line) - len(strings.TrimLeft(line, c))
		if run < 3 {
			continue
		}
		// A backtick fence's info string cannot hold a backtick; that line is
		// an inline code span.
		if c == "`" && strings.Contains(line[run:], "`") {
			return ""
		}
		return strings.Repeat(c, run)
	}
	return ""
}

// headingTitle returns the text of an ATX heading ("#" to "######").
func headingTitle(line string) (string, bool) {
	rest := strings.TrimLeft(line, "#")
	if hashes := len(line) - len(rest); hashes == 0 || hashes > 6 || (rest != "" && rest[0] != ' ') {
		return "", false
	}
	title := strings.TrimSpace(strings.TrimRight(rest, "# "))
	return strings.TrimSpace(strings.TrimSuffix(title, ":")), true
}

// boldLabel returns the text of a line that is nothing but a bold label,
// with or without a trailing colon: "**Testing**" or "**Testing:**".
func boldLabel(line string) (string, bool) {
	for _, mark := range []string{"**", "__"} {
		if len(line) > 2*len(mark) && strings.HasPrefix(line, mark) && strings.HasSuffix(strings.TrimSuffix(line, ":"), mark) {
			inner := strings.TrimSuffix(strings.TrimSuffix(line, ":"), mark)[len(mark):]
			return strings.TrimSpace(strings.TrimSuffix(inner, ":")), true
		}
	}
	return "", false
}

// isTrailerLine reports whether the line is a closing reference or an epic
// link on its own, the lines finishPRBody appends after the author's text.
func isTrailerLine(line string) bool {
	fields := strings.Fields(line)
	if len(fields) != 2 && len(fields) != 3 {
		return false
	}
	if len(fields) == 3 {
		_, ok := issueRef(fields[2])
		return ok && strings.EqualFold(fields[0], "part") && strings.EqualFold(fields[1], "of")
	}
	_, ok := issueRef(fields[1])
	return ok && isClosingKeyword(fields[0])
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
