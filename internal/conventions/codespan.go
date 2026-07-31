package conventions

import (
	"regexp"
	"strings"
)

// CodeSpanGuidance is the authoring rule the body templates prescribe. It is
// stated on the create/pr/apply help surfaces, but help text is read when
// someone goes looking for it and the composing agent generally does not —
// which is why the rule is also checked. UnmarkedCodeText is what makes it
// land; this text is what the warning points at.
const CodeSpanGuidance = "Write commands, flags, branch names, paths and error strings as code spans\n" +
	"(`--title`, `feat/pr-head`, `internal/cli/pr.go`, `go test -race ./...`): issue\n" +
	"and PR bodies are read in a browser and outlive the branch, and those are the\n" +
	"characters that blur into prose. Wrap the whole command, not just the flag\n" +
	"inside it. Composed bodies are checked and unmarked code text is warned about."

// codeShaped are the token shapes that are worth flagging when they appear
// outside a code span. Each one is high-precision on purpose: a warning the
// author learns to skip past is worse than no warning, so a shape earns its
// place by being something prose does not produce.
//
// The set is deliberately not exhaustive. "go vet" and "gofmt -l" are code
// text this will miss, because a command is an unbounded shape and guessing
// at one costs more credibility than it buys. Missing a case leaves the
// authoring convention to cover it; crying wolf trains the author to ignore
// the cases it does catch.
var codeShaped = []struct {
	name string
	re   *regexp.Regexp
}{
	// A long flag: two dashes then a letter. Prose writes "--" as an em-dash
	// and in "well--known", but never immediately before a letter.
	{"flag", regexp.MustCompile(`^--[A-Za-z][A-Za-z0-9-]*(=\S+)?$`)},
	// A path with a file extension. The extension is what separates
	// internal/cli/pr.go from "and/or", which is otherwise the same shape.
	{"path", regexp.MustCompile(`^[\w.\-/]+/[\w.\-]+\.(go|mod|sum|md|ya?ml|json|jsonl|sh|toml)$`)},
	// A bare filename carrying a code extension.
	{"file", regexp.MustCompile(`^[\w.\-]+\.(go|mod|sum|ya?ml|jsonl|sh|toml)$`)},
	// A Go test or exported identifier, which is how test names get named in
	// a Testing section.
	{"identifier", regexp.MustCompile(`^(Test|Benchmark|Fuzz)[A-Z][A-Za-z0-9]+$`)},
	// The package wildcard, unambiguous wherever it appears.
	{"wildcard", regexp.MustCompile(`^\./\.\.\.$`)},
}

// fenceMarker matches the opening or closing line of a fenced code block.
var fenceMarker = regexp.MustCompile("^\\s*(```|~~~)")

// UnmarkedCodeText returns the code-shaped tokens in a body that are not
// inside a code span or a fenced block, de-duplicated and in order of first
// appearance. It reports; it never rewrites.
//
// Checking rather than transforming is the whole design. Marking text up
// automatically requires knowing what the text means — whether --body-file
// is a flag being named or part of "gh pr edit --body-file" — and that
// information is not in the token stream, so a transform splits compound
// commands and cannot be iterated out of it. The author knows. So the tool's
// job is to notice and say so, at the moment the body is composed, rather
// than to guess or to rely on help text having been read.
//
// Reporting also inverts the cost of a false positive: a wrong rewrite ships
// permanently in a published body, while a wrong warning costs one line of
// stderr. That is what lets the checked set cover paths and identifiers,
// which no safe transform could ever touch.
func UnmarkedCodeText(body string) []string {
	var found []string
	seen := map[string]bool{}
	for _, segment := range outsideCode(body) {
		for _, token := range strings.Fields(segment) {
			// The raw token is judged first: trimming punctuation is what
			// lets "--title." be recognised, but it would also dismantle
			// "./...", which is punctuation all the way down.
			core := token
			if !isCodeShaped(core) {
				core = trimPunctuation(token)
			}
			if core == "" || seen[core] || !isCodeShaped(core) {
				continue
			}
			seen[core] = true
			found = append(found, core)
		}
	}
	return found
}

func isCodeShaped(token string) bool {
	for _, shape := range codeShaped {
		if shape.re.MatchString(token) {
			return true
		}
	}
	return false
}

// trimPunctuation strips the characters prose wraps a token in, so a token
// ending a sentence is judged on the token.
func trimPunctuation(token string) string {
	return strings.Trim(token, "([\"'.,;:)]")
}

// outsideCode returns the parts of a body that are not inside a fenced block
// or a code span. Splitting a line on backticks alternates the parts: even
// indexes are outside, odd are inside. An unpaired backtick leaves its
// trailing text at an odd index, so a half-written span is treated as code
// and not flagged — the reading that stays quiet when it cannot be sure.
func outsideCode(body string) []string {
	var out []string
	inFence := false
	for _, line := range strings.Split(body, "\n") {
		if fenceMarker.MatchString(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		parts := strings.Split(line, "`")
		for i := 0; i < len(parts); i += 2 {
			out = append(out, parts[i])
		}
	}
	return out
}

// FormatUnmarkedCodeText renders the warning body for a set of findings,
// capping the list so a long body cannot bury the rest of the output.
func FormatUnmarkedCodeText(tokens []string) string {
	const max = 5
	shown := tokens
	suffix := ""
	if len(tokens) > max {
		shown = tokens[:max]
		suffix = ", …"
	}
	return strings.Join(shown, ", ") + suffix
}
