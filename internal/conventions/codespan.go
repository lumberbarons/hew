package conventions

import (
	"regexp"
	"strings"
)

// CodeSpanGuidance is the authoring rule the body templates prescribe. It
// lives on the create/pr/apply help surfaces rather than in the primer,
// which is already over its ~600-token budget (#63). Transformation covers
// only the one token shape that cannot be prose, so the rest is authoring:
// the text is correct where it is written, and both body escape hatches go
// on carrying it verbatim.
const CodeSpanGuidance = "Write commands, flags, branch names, paths and error strings as code spans\n" +
	"(`--title`, `feat/pr-head`, `internal/cli/pr.go`, `HTTP 422`): issue and PR\n" +
	"bodies are read in a browser and outlive the branch, and those are the\n" +
	"characters that blur into prose. A bare --flag token is marked up for you;\n" +
	"nothing else is guessed at, so mark the rest up as you write it."

// bareLongFlag matches a token that can only be a command-line flag: two
// dashes, then a letter. The rule is deliberately the narrowest one worth
// having. Prose reaches for a double dash too — as an em-dash ("the fix --
// and this is key -- was late"), inside a compound ("well--known"), as a
// rule line ("---"), and as the end-of-flags separator ("git log -- path")
// — but none of those put a letter immediately after the dashes, so none of
// them match. Anything broader (branch names, paths, error strings) shares a
// shape with ordinary words: a rule that catches origin/feat/x also catches
// "and/or", and a false positive ships in a body nobody re-reads.
var bareLongFlag = regexp.MustCompile(`^--[A-Za-z][A-Za-z0-9-]*(=\S+)?$`)

// fenceMarker matches the opening or closing line of a fenced code block.
var fenceMarker = regexp.MustCompile("^\\s*(```|~~~)")

// codeSpanFlags wraps bare long-flag tokens in code spans, leaving text
// that is already marked up untouched. It is idempotent by construction —
// anything inside a code span or a fenced block is skipped — which is what
// lets `pr` re-compose a section it already read back out of an issue body
// without accumulating backticks.
//
// It runs on composed sections only. A --body-file body is the escape hatch
// and passes through verbatim: text is correct where it is written, and the
// author who reached for the escape hatch has already said so.
func codeSpanFlags(s string) string {
	lines := strings.Split(s, "\n")
	inFence := false
	for i, line := range lines {
		if fenceMarker.MatchString(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		lines[i] = markUpOutsideSpans(line)
	}
	return strings.Join(lines, "\n")
}

// markUpOutsideSpans applies the flag rule to the parts of a line that are
// not already inside a code span. Splitting on backticks alternates the
// parts: even indexes are outside, odd are inside. An unpaired backtick
// leaves its trailing text at an odd index, so a half-written span is
// treated as code and left alone — the conservative reading.
func markUpOutsideSpans(line string) string {
	parts := strings.Split(line, "`")
	for i := 0; i < len(parts); i += 2 {
		parts[i] = markUpTokens(parts[i])
	}
	return strings.Join(parts, "`")
}

// markUpTokens rewrites whitespace-delimited tokens that match the flag
// rule, preserving the original spacing so a body's line breaks and
// indentation survive.
func markUpTokens(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		j := i
		for j < len(s) && !isSpace(s[j]) {
			j++
		}
		if j > i {
			b.WriteString(markUpToken(s[i:j]))
			i = j
		}
		for i < len(s) && isSpace(s[i]) {
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t'
}

// markUpToken wraps one token if its core — the token minus the punctuation
// prose wraps it in — is a bare long flag. The punctuation stays outside the
// span, so "pass --title." keeps its full stop as prose.
func markUpToken(token string) string {
	const (
		leading  = "([\"'"
		trailing = ".,;:)]\"'"
	)
	core := strings.TrimLeft(token, leading)
	prefix := token[:len(token)-len(core)]
	core = strings.TrimRight(core, trailing)
	suffix := token[len(prefix)+len(core):]
	if !bareLongFlag.MatchString(core) {
		return token
	}
	return prefix + "`" + core + "`" + suffix
}
