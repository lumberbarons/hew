package conventions

import (
	"strings"
	"testing"

	"github.com/lumberbarons/hew/internal/model"
)

func TestLabelsCoverConventionSet(t *testing.T) {
	byName := map[string]Label{}
	for _, l := range Labels {
		byName[l.Name] = l
		if l.Color == "" || l.Description == "" {
			t.Errorf("label %q missing color or description", l.Name)
		}
		if strings.HasPrefix(l.Color, "#") {
			t.Errorf("label %q color has leading #", l.Name)
		}
	}
	for _, want := range []string{"P0", "P1", "P2", "P3", "P4", "bug", "enhancement", "task", model.InProgressLabel} {
		if _, ok := byName[want]; !ok {
			t.Errorf("label set missing %q", want)
		}
	}
	for _, l := range Labels {
		if _, ok := model.ParsePriority(l.Name); ok {
			continue
		}
		if model.IsType(l.Name) || l.Name == model.InProgressLabel {
			continue
		}
		t.Errorf("label %q is not part of the priority/type/in-progress conventions", l.Name)
	}
}

func TestLabelStylesCoverVocabulary(t *testing.T) {
	// Every name model defines must appear in the bootstrap set with
	// cosmetics attached; adding a priority or type in model without a style
	// here must fail rather than silently ship a blank label.
	byName := map[string]Label{}
	for _, l := range Labels {
		byName[l.Name] = l
	}
	for _, name := range model.LabelVocabulary() {
		l, ok := byName[name]
		if !ok {
			t.Errorf("vocabulary name %q has no bootstrap label", name)
			continue
		}
		if l.Color == "" || l.Description == "" {
			t.Errorf("vocabulary name %q has no color/description style", name)
		}
	}
	if len(Labels) != len(model.LabelVocabulary()) {
		t.Errorf("Labels has %d entries, vocabulary has %d — a style exists for a name model doesn't define",
			len(Labels), len(model.LabelVocabulary()))
	}
}

func TestTemplateSections(t *testing.T) {
	bug := TemplateSections("bug")
	if bug[1] != "### Problem" || bug[2] != "### Fix" {
		t.Errorf("bug sections = %v", bug)
	}
	for _, typ := range []string{"enhancement", "task"} {
		s := TemplateSections(typ)
		if s[1] != "### Goal" || s[2] != "### Approach" {
			t.Errorf("%s sections = %v", typ, s)
		}
	}
}

func TestTemplateSkeleton(t *testing.T) {
	// The skeleton is a fixed template, so assert it whole: section order,
	// blank slots, and the checklist seeded under "Done when" are all part
	// of the contract with --edit and StripEmptySections.
	wantBug := "### Where\n\n### Problem\n\n### Fix\n\n### Done when\n\n- [ ] \n"
	if got := TemplateSkeleton("bug"); got != wantBug {
		t.Errorf("bug skeleton = %q, want %q", got, wantBug)
	}
	wantTask := "### Where\n\n### Goal\n\n### Approach\n\n### Done when\n\n- [ ] \n"
	if got := TemplateSkeleton("task"); got != wantTask {
		t.Errorf("task skeleton = %q, want %q", got, wantTask)
	}
}

func TestStripEmptySections(t *testing.T) {
	in := "### Where\n\ninternal/model\n\n### Problem\n\n\n### Fix\n\nDo the thing\n\n### Done when\n\n- [ ] \n"
	got := StripEmptySections(in)
	if strings.Contains(got, "Problem") || strings.Contains(got, "Done when") {
		t.Errorf("empty sections not stripped:\n%s", got)
	}
	for _, keep := range []string{"### Where", "internal/model", "### Fix", "Do the thing"} {
		if !strings.Contains(got, keep) {
			t.Errorf("filled content lost %q:\n%s", keep, got)
		}
	}
}

func TestStripEmptySectionsChecklist(t *testing.T) {
	in := "### Done when\n\n- [ ] tests pass\n"
	got := StripEmptySections(in)
	if !strings.Contains(got, "- [ ] tests pass") {
		t.Errorf("filled checklist stripped:\n%s", got)
	}
}

func TestStripEmptySectionsPreservesNonTemplate(t *testing.T) {
	in := "Free-form intro.\n\n### Where\n\n\nTrailing? No: header then blank."
	got := StripEmptySections("Free-form intro.\n\nDiscovered while working on #9")
	if got != "Free-form intro.\n\nDiscovered while working on #9" {
		t.Errorf("non-template body altered: %q", got)
	}
	_ = in
}

func TestSectionsCompose(t *testing.T) {
	// The composed body is the write-path template guarantee, so assert it
	// whole: header order, blank-line separation, and the checklist shape.
	got := Sections{
		Where:    "internal/cli",
		Goal:     "Ship it",
		Approach: "Carefully",
		DoneWhen: []string{"tests pass", "docs updated"},
	}.Compose()
	want := "### Where\n\ninternal/cli\n\n### Goal\n\nShip it\n\n### Approach\n\nCarefully\n\n### Done when\n\n- [ ] tests pass\n- [ ] docs updated"
	if got != want {
		t.Errorf("composed = %q, want %q", got, want)
	}
}

func TestSectionsComposeBugWording(t *testing.T) {
	got := Sections{Problem: "It breaks", Fix: "Stop it"}.Compose()
	want := "### Problem\n\nIt breaks\n\n### Fix\n\nStop it"
	if got != want {
		t.Errorf("composed = %q, want %q", got, want)
	}
}

func TestSectionsComposeOmitsEmpty(t *testing.T) {
	got := Sections{Where: "  ", DoneWhen: []string{"just this"}}.Compose()
	want := "### Done when\n\n- [ ] just this"
	if got != want {
		t.Errorf("composed = %q, want %q", got, want)
	}
}

func TestSectionsIsZero(t *testing.T) {
	if !(Sections{}).IsZero() {
		t.Error("zero Sections not IsZero")
	}
	for _, s := range []Sections{
		{Where: "w"}, {Problem: "p"}, {Goal: "g"}, {Fix: "f"},
		{Approach: "a"}, {DoneWhen: []string{"d"}},
	} {
		if s.IsZero() {
			t.Errorf("%+v reported IsZero", s)
		}
	}
}

func TestDiscoveredFrom(t *testing.T) {
	if got := DiscoveredFrom(123); got != "Discovered while working on #123" {
		t.Errorf("DiscoveredFrom = %q", got)
	}
}

func TestPrimerStaticMentionsCoreCommands(t *testing.T) {
	for _, cmd := range []string{"hew ready", "start", "triage", "hew search", "--discovered-from", "Fixes #n", "P0", "P4", "exit 3",
		"### Where", "### Done when", "Area labels sparingly", "No title prefixes",
		"--goal|--problem", "--approach|--fix", "--done-when", "--body-file F for long bodies"} {
		if !strings.Contains(PrimerStatic, cmd) {
			t.Errorf("primer missing %q", cmd)
		}
	}
}

func TestClaudeSnippet(t *testing.T) {
	// Every load-bearing claim in the snippet hew init writes into user
	// repos: where work is tracked, the session-start command, and the
	// fallback.
	for _, want := range []string{"GitHub Issues", "`hew` CLI", "hew prime", "hew ready"} {
		if !strings.Contains(ClaudeSnippet, want) {
			t.Errorf("snippet missing %q:\n%s", want, ClaudeSnippet)
		}
	}
}
