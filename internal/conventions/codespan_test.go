package conventions

import "testing"

func TestCodeSpanFlagsMarksUpBareFlags(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bare flag", "pass --title to override", "pass `--title` to override"},
		{"flag with value", "run --priority=P1 next", "run `--priority=P1` next"},
		{"trailing full stop", "name --title.", "name `--title`."},
		{"parenthesised", "(--for n) settles it", "(`--for` n) settles it"},
		{"start of line", "--body-file is the escape hatch", "`--body-file` is the escape hatch"},
		{"list item", "- --testing says how", "- `--testing` says how"},
		{"several on a line", "--what and --why are repeatable", "`--what` and `--why` are repeatable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := codeSpanFlags(c.in); got != c.want {
				t.Errorf("codeSpanFlags(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestCodeSpanFlagsLeavesProseAlone names the false positives the rule must
// not produce. A wrong backtick lands in a published body that nobody
// re-reads, so these are the cases that decide whether the rule is worth
// having at all.
func TestCodeSpanFlagsLeavesProseAlone(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"em-dash", "the fix -- and this is key -- was late"},
		{"end-of-flags separator", "git log -- path/to/file"},
		{"compound word", "a well--known problem"},
		{"horizontal rule", "---"},
		{"long rule", "-------"},
		{"single dash flag", "the -v flag"},
		{"negative number", "offset by --3 places"},
		{"url with dashes", "see https://example.com/a--b"},
		{"and/or", "the branch and/or the claim"},
		{"path", "internal/cli/pr.go composes it"},
		{"bare dashes", "-- "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := codeSpanFlags(c.in); got != c.in {
				t.Errorf("codeSpanFlags(%q) = %q, want it unchanged", c.in, got)
			}
		})
	}
}

// TestCodeSpanFlagsIsIdempotent is the invariant that lets pr re-compose a
// section it read back out of an issue body: composing twice must equal
// composing once, or every PR would thicken the backticks its issue already
// carried.
func TestCodeSpanFlagsIsIdempotent(t *testing.T) {
	inputs := []string{
		"pass --title to override",
		"already `--title` marked up",
		"mixed `--what` and --why",
		"run `git rev-parse --abbrev-ref @{u}` first",
		"```\n--not-a-flag-here\n```",
		"a `--flag` and prose",
	}
	for _, in := range inputs {
		once := codeSpanFlags(in)
		twice := codeSpanFlags(once)
		if once != twice {
			t.Errorf("codeSpanFlags not idempotent for %q: once = %q, twice = %q", in, once, twice)
		}
	}
}

func TestCodeSpanFlagsSkipsExistingSpans(t *testing.T) {
	in := "run `git rev-parse --abbrev-ref @{u}` before --push"
	want := "run `git rev-parse --abbrev-ref @{u}` before `--push`"
	if got := codeSpanFlags(in); got != want {
		t.Errorf("codeSpanFlags(%q) = %q, want %q", in, got, want)
	}
}

func TestCodeSpanFlagsSkipsFencedBlocks(t *testing.T) {
	in := "before --one\n```sh\nhew pr --testing x\n```\nafter --two"
	want := "before `--one`\n```sh\nhew pr --testing x\n```\nafter `--two`"
	if got := codeSpanFlags(in); got != want {
		t.Errorf("codeSpanFlags(%q) = %q, want %q", in, got, want)
	}
}

func TestCodeSpanFlagsTreatsUnpairedBacktickAsCode(t *testing.T) {
	// A half-written span is ambiguous; leaving the tail alone is the
	// reading that cannot produce a nested backtick.
	in := "an unclosed `span with --flag"
	if got := codeSpanFlags(in); got != in {
		t.Errorf("codeSpanFlags(%q) = %q, want it unchanged", in, got)
	}
}

func TestCodeSpanFlagsPreservesSpacing(t *testing.T) {
	in := "  indented --flag\ttabbed --other"
	want := "  indented `--flag`\ttabbed `--other`"
	if got := codeSpanFlags(in); got != want {
		t.Errorf("codeSpanFlags(%q) = %q, want %q", in, got, want)
	}
}
