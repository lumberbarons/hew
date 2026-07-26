package conventions

import (
	"reflect"
	"strings"
	"testing"
)

func TestPRComposeRendersSectionsThenTrailers(t *testing.T) {
	got := PRSections{
		What:    "Adds the pr command.",
		Why:     "The PR step was freeform.",
		Testing: "go test -race ./...",
	}.Compose(PRTrailers{Fixes: 30, Epic: 33})
	want := `### What

Adds the pr command.

### Why

The PR step was freeform.

### Testing

go test -race ./...

Fixes #30
Part of #33`
	if got != want {
		t.Errorf("Compose() =\n%s\n\nwant\n%s", got, want)
	}
}

// An absent section leaves no bare header behind — the same rule the issue
// template follows.
func TestPRComposeOmitsEmptySections(t *testing.T) {
	got := PRSections{Why: "  \n "}.Compose(PRTrailers{Fixes: 7})
	if got != "Fixes #7" {
		t.Errorf("Compose() = %q, want just the trailer", got)
	}
	if strings.Contains(got, "###") {
		t.Errorf("Compose() kept an empty header: %q", got)
	}
}

func TestPRComposeOmitsEpicWhenAbsent(t *testing.T) {
	got := PRSections{What: "x"}.Compose(PRTrailers{Fixes: 7})
	if strings.Contains(got, "Part of") {
		t.Errorf("Compose() invented an epic link: %q", got)
	}
	if !strings.HasSuffix(got, "Fixes #7") {
		t.Errorf("Compose() = %q, want it to end with the Fixes trailer", got)
	}
}

func TestPRSectionsIsZero(t *testing.T) {
	if !(PRSections{}).IsZero() {
		t.Error("empty PRSections reported non-zero")
	}
	for _, s := range []PRSections{{What: "a"}, {Why: "a"}, {Testing: "a"}} {
		if s.IsZero() {
			t.Errorf("%+v reported zero", s)
		}
	}
}

func TestFixesReferences(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []int
	}{
		{"the trailer this tool writes", "### What\n\nx\n\nFixes #30", []int{30}},
		{"lowercase and other keywords", "closes #1 and resolved #2", []int{1, 2}},
		{"trailing punctuation", "Fixes #42.", []int{42}},
		{"markdown emphasis", "**Fixes** **#9**", []int{9}},
		{"no keyword means no link", "See #30 for context", nil},
		{"keyword must be a whole word", "prefixes #30", nil},
		{"keyword must be followed by a ref", "this fixes the bug", nil},
		{"a ref must be numeric", "Fixes #abc", nil},
		{"a bare hash is not a ref", "Fixes #", nil},
		{"keyword at the very end", "Fixes", nil},
		{"empty body", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FixesReferences(tt.body); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("FixesReferences(%q) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}

// FixesLine is what Compose writes, so FixesReferences must read it back —
// otherwise the one-Fixes guard could pass a body it just mis-parsed.
func TestFixesLineRoundTrips(t *testing.T) {
	body := PRSections{What: "x"}.Compose(PRTrailers{Fixes: 30})
	if got := FixesReferences(body); !reflect.DeepEqual(got, []int{30}) {
		t.Errorf("FixesReferences(Compose(...)) = %v, want [30]", got)
	}
}

func TestSectionExtractsContent(t *testing.T) {
	body := `### Where

internal/cli

### Goal

Close the claim
lifecycle.

### Done when

- [ ] it works`
	if got := Section(body, "Goal"); got != "Close the claim\nlifecycle." {
		t.Errorf("Section(Goal) = %q", got)
	}
	if got := Section(body, "Where"); got != "internal/cli" {
		t.Errorf("Section(Where) = %q", got)
	}
	if got := Section(body, "Problem"); got != "" {
		t.Errorf("Section(Problem) = %q, want empty for an absent section", got)
	}
}

// The last section has no following header to stop at.
func TestSectionReadsTheFinalSection(t *testing.T) {
	if got := Section("### Goal\n\nship it", "Goal"); got != "ship it" {
		t.Errorf("Section(Goal) = %q, want ship it", got)
	}
}

func TestSectionEmptyWhenHeaderHasNoBody(t *testing.T) {
	if got := Section("### Goal\n\n### Approach\n\ndo it", "Goal"); got != "" {
		t.Errorf("Section(Goal) = %q, want empty", got)
	}
}

func TestFirstSectionPicksTheWordingThatIsPresent(t *testing.T) {
	body := "### Approach\n\nthin command"
	if got := FirstSection(body, "Fix", "Approach"); got != "thin command" {
		t.Errorf("FirstSection = %q, want thin command", got)
	}
	if got := FirstSection(body, "Problem", "Goal"); got != "" {
		t.Errorf("FirstSection = %q, want empty when neither wording is present", got)
	}
}

// Fix wins over Approach when a body carries both, so the result doesn't
// depend on which order they happen to appear in.
func TestFirstSectionPrefersTheEarlierName(t *testing.T) {
	body := "### Approach\n\nsecond\n\n### Fix\n\nfirst"
	if got := FirstSection(body, "Fix", "Approach"); got != "first" {
		t.Errorf("FirstSection = %q, want first", got)
	}
}

func TestIsConventionalBranch(t *testing.T) {
	for _, branch := range []string{"feat/x", "fix/x", "chore/x", "docs/x"} {
		if !IsConventionalBranch(branch) {
			t.Errorf("IsConventionalBranch(%q) = false", branch)
		}
	}
	for _, branch := range []string{"main", "wip", "feature/x", "feat-x"} {
		if IsConventionalBranch(branch) {
			t.Errorf("IsConventionalBranch(%q) = true", branch)
		}
	}
}

// PRTitle is where the changelog convention is enforced (#44), and every
// wrong answer either mis-files a release note or corrupts the subject a
// squash merge writes, so its rules are pinned directly.
func TestPRTitle(t *testing.T) {
	tests := []struct {
		name      string
		issueType string
		title     string
		want      string
	}{
		{"bug", "bug", "pr links the wrong issue", "fix: pr links the wrong issue"},
		{"enhancement", "enhancement", "reopen command", "feat: reopen command"},
		{"task", "task", "token-efficiency comparison", "chore: token-efficiency comparison"},
		{"untyped", "", "drive-by report", "drive-by report"},
		{"unknown type", "question", "from a label set we do not own", "from a label set we do not own"},
		{"already prefixed", "bug", "fix: filed with a prefix", "fix: filed with a prefix"},
		{"prefixed with a different type", "bug", "chore: filed with a prefix", "chore: filed with a prefix"},
		{"prefixed with a scope", "enhancement", "feat(cli): scoped", "feat(cli): scoped"},
		{"prefixed as breaking", "enhancement", "feat!: breaking", "feat!: breaking"},
		{"prefixed in mixed case", "bug", "Fix: crash on startup", "Fix: crash on startup"},
		{"colon that is not a prefix", "bug", "ready: sorts epics last", "fix: ready: sorts epics last"},
		{"prose containing a colon", "task", "document the rule: it is subtle", "chore: document the rule: it is subtle"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := PRTitle(tc.issueType, tc.title); got != tc.want {
				t.Errorf("PRTitle(%q, %q) = %q, want %q", tc.issueType, tc.title, got, tc.want)
			}
		})
	}
}

// PRTitle only derives three prefixes, but HasCommitPrefix must recognize
// every one it knows: a "docs:" title on an enhancement issue would
// otherwise be double-prefixed to "feat: docs: …", corrupting the very
// squash-merge subject the convention protects. Looping the list keeps a
// prefix from being added without its recognition being covered.
func TestPRTitleLeavesEveryKnownPrefixAlone(t *testing.T) {
	// Spelled out rather than ranged over CommitPrefixes: a loop over the
	// list under test drops the case along with the entry, so removing a
	// prefix would still pass. Pinning it also means adding a prefix fails
	// here until it is covered.
	want := []string{"feat", "fix", "chore", "docs", "refactor", "test", "perf", "build", "ci", "style", "revert"}
	if !reflect.DeepEqual(CommitPrefixes, want) {
		t.Fatalf("CommitPrefixes = %v, want %v", CommitPrefixes, want)
	}
	for _, prefix := range want {
		title := prefix + ": already prefixed"
		if got := PRTitle("enhancement", title); got != title {
			t.Errorf("PRTitle(enhancement, %q) = %q, want it unchanged", title, got)
		}
	}
}
