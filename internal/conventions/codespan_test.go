package conventions

import (
	"reflect"
	"testing"
)

func TestUnmarkedCodeTextFindsCodeShapedTokens(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"bare flag", "pass --title to override", []string{"--title"}},
		{"flag with value", "run --priority=P1 next", []string{"--priority=P1"}},
		{"trailing full stop", "name --title.", []string{"--title"}},
		{"path with extension", "it lives in internal/cli/pr.go today", []string{"internal/cli/pr.go"}},
		{"bare filename", "codespan.go is new", []string{"codespan.go"}},
		{"go test name", "TestPRCompose covers it", []string{"TestPRCompose"}},
		{"package wildcard", "go test -race ./... is green", []string{"./..."}},
		{"several", "--what and internal/gh/client.go", []string{"--what", "internal/gh/client.go"}},
		{"deduplicated", "--title then --title again", []string{"--title"}},
		// The wildcard is punctuation all the way down, so any clause mark
		// after it used to take the token's own dots with it and the check
		// went quiet on the shape it most wants to catch.
		{"wildcard then comma", "run go test ./..., then check", []string{"./..."}},
		{"wildcard ending a sentence", "run go test ./....", []string{"./..."}},
		{"wildcard in brackets", "the suite (./...) is green", []string{"./..."}},
		{"path ending a sentence", "it lives in internal/cli/pr.go.", []string{"internal/cli/pr.go"}},
		{"quoted flag", "the \"--title\" flag", []string{"--title"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := UnmarkedCodeText(c.in); !reflect.DeepEqual(got, c.want) {
				t.Errorf("UnmarkedCodeText(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// TestUnmarkedCodeTextStaysQuietOnProse names the false positives the check
// must not produce. A warning the author learns to skip past is worse than
// no warning at all, so each of these is load-bearing for the whole design.
func TestUnmarkedCodeTextStaysQuietOnProse(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"em-dash", "the fix -- and this is key -- was late"},
		{"end-of-flags separator", "git log -- path"},
		{"compound word", "a well--known problem"},
		{"horizontal rule", "---"},
		{"single dash flag", "the -v flag"},
		{"leading digit after dashes", "offset by --3 places"},
		{"and/or", "the branch and/or the claim"},
		{"slash prose without an extension", "read/write access to internal/cli"},
		{"sentence with a full stop", "It works. Mostly."},
		{"ordinary capitalised word", "Testing is the section name"},
		{"url", "see https://example.com/a/b"},
		{"empty", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := UnmarkedCodeText(c.in); len(got) != 0 {
				t.Errorf("UnmarkedCodeText(%q) = %v, want no findings", c.in, got)
			}
		})
	}
}

// Text the author already marked up is exactly what the check must not
// nag about, or it would fire forever on a body that is already correct.
func TestUnmarkedCodeTextSkipsMarkedUpText(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"code span", "pass `--title` to override"},
		{"whole command in a span", "run `gh pr edit --body-file x.md` instead"},
		{"path in a span", "it lives in `internal/cli/pr.go`"},
		{"fenced block", "```sh\nhew pr --testing x\ngo test ./...\n```"},
		{"unpaired backtick leaves the tail alone", "an unclosed `span with --flag"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := UnmarkedCodeText(c.in); len(got) != 0 {
				t.Errorf("UnmarkedCodeText(%q) = %v, want no findings", c.in, got)
			}
		})
	}
}

// The check reports across a whole composed body, which is how it catches a
// Testing section written after the prose sections were marked up properly.
func TestUnmarkedCodeTextSpansSections(t *testing.T) {
	body := PRSections{
		What:    "Marks up `--title` correctly.",
		Testing: "go test -race ./... is green; codespan.go is covered.",
	}.Compose(PRTrailers{Fixes: 71})
	got := UnmarkedCodeText(body)
	want := []string{"./...", "codespan.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("UnmarkedCodeText(body) = %v, want %v", got, want)
	}
}

// Compose must not rewrite anything: the check reports, the author fixes.
func TestComposeLeavesBodiesVerbatim(t *testing.T) {
	in := "pass --title to override, see internal/cli/pr.go"
	body := PRSections{What: in}.Compose(PRTrailers{Fixes: 71})
	if want := "### What\n\n" + in + "\n\nFixes #71"; body != want {
		t.Errorf("Compose() = %q, want %q — Compose must not transform", body, want)
	}
}

func TestFormatUnmarkedCodeTextCapsTheList(t *testing.T) {
	many := []string{"a.go", "b.go", "c.go", "d.go", "e.go", "f.go", "g.go"}
	got := FormatUnmarkedCodeText(many)
	if want := "a.go, b.go, c.go, d.go, e.go, …"; got != want {
		t.Errorf("FormatUnmarkedCodeText(%v) = %q, want %q", many, got, want)
	}
	if got := FormatUnmarkedCodeText([]string{"a.go", "b.go"}); got != "a.go, b.go" {
		t.Errorf("FormatUnmarkedCodeText short list = %q", got)
	}
}
