package conventions

import (
	"fmt"
	"strings"
)

// TemplateSections returns the body-template section headers for a type, in
// order. Bugs describe a problem and a fix; enhancements and tasks a goal
// and an approach.
func TemplateSections(issueType string) []string {
	if issueType == "bug" {
		return []string{"### Where", "### Problem", "### Fix", "### Done when"}
	}
	return []string{"### Where", "### Goal", "### Approach", "### Done when"}
}

// TemplateSkeleton renders the empty template for --edit: headers with
// blank slots, "Done when" seeded with one checklist item.
func TemplateSkeleton(issueType string) string {
	var b strings.Builder
	for _, h := range TemplateSections(issueType) {
		b.WriteString(h)
		b.WriteString("\n\n")
		if h == "### Done when" {
			b.WriteString("- [ ] \n\n")
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// StripEmptySections removes template sections whose body is blank, so a
// half-filled skeleton posts clean. Non-template content is preserved
// verbatim.
func StripEmptySections(body string) string {
	lines := strings.Split(body, "\n")
	var out []string
	i := 0
	for i < len(lines) {
		line := lines[i]
		if strings.HasPrefix(line, "### ") {
			// Collect the section: header plus everything up to the next header.
			j := i + 1
			for j < len(lines) && !strings.HasPrefix(lines[j], "### ") {
				j++
			}
			if sectionHasContent(lines[i+1 : j]) {
				out = append(out, lines[i:j]...)
			}
			i = j
			continue
		}
		out = append(out, line)
		i++
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func sectionHasContent(lines []string) bool {
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" || t == "- [ ]" {
			continue
		}
		return true
	}
	return false
}

// Sections are the structured body fields carried by the create section
// flags and plan entries. Problem/Goal and Fix/Approach are wording pairs —
// at most one of each is set (the write path enforces it), and whichever is
// set picks the header, so word choice is never policed against the issue
// type.
type Sections struct {
	Where    string
	Problem  string
	Goal     string
	Fix      string
	Approach string
	DoneWhen []string
}

// IsZero reports whether no section was provided.
func (s Sections) IsZero() bool {
	return s.Where == "" && s.Problem == "" && s.Goal == "" &&
		s.Fix == "" && s.Approach == "" && len(s.DoneWhen) == 0
}

// Compose renders the sections as a template-conformant body: headers in
// template order, provided sections only, Done when as a checklist.
func (s Sections) Compose() string {
	var b strings.Builder
	section := func(header, content string) {
		if strings.TrimSpace(content) == "" {
			return
		}
		fmt.Fprintf(&b, "### %s\n\n%s\n\n", header, strings.TrimSpace(content))
	}
	section("Where", s.Where)
	section("Problem", s.Problem)
	section("Goal", s.Goal)
	section("Fix", s.Fix)
	section("Approach", s.Approach)
	if len(s.DoneWhen) > 0 {
		b.WriteString("### Done when\n\n")
		for _, item := range s.DoneWhen {
			fmt.Fprintf(&b, "- [ ] %s\n", strings.TrimSpace(item))
		}
	}
	return strings.TrimSpace(b.String())
}

// DiscoveredFrom is the body line linking discovered work to its origin.
func DiscoveredFrom(n int) string {
	return fmt.Sprintf("Discovered while working on #%d", n)
}

// EpicTitlePrefix is the cosmetic title prefix the tool adds to parent
// issues. Epic-ness itself is defined by having sub-issues.
const EpicTitlePrefix = "Epic: "
