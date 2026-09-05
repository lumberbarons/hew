// Package render turns domain types into the two output forms: compact
// fixed-column text and flat JSON.
package render

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lumberbarons/hew/internal/model"
)

// GitHub-controlled text reaches a terminal through every text-mode command,
// and control characters are the vector: ANSI SGR sequences repaint the
// display, OSC 52 writes the user's clipboard, and a bare CR overwrites the
// line just printed. The read path normalizes rather than repairs, so the
// renderer neutralizes rather than drops — the offending value stays visible,
// and a single-byte replacement keeps the column alignment honest.
const controlReplacement = '?'

// SanitizeInline neutralizes every control character in a value rendered on
// one line. Newline and tab go too: neither is formatting there, and a
// newline would let a hostile title forge a line of its own. Exported so
// command-level messages that bypass the renderer (start's title echo,
// guard and warning logins) route through the same sanitizer.
func SanitizeInline(s string) string { return sanitize(s, false) }

// sanitizeBlock is the multi-line form, for issue and comment bodies: the
// newlines and tabs carrying the markdown's shape survive, everything else is
// neutralized.
func sanitizeBlock(s string) string { return sanitize(s, true) }

func sanitize(s string, keepLayout bool) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			// Not valid UTF-8. A raw C1 byte such as 0x9b arrives this way,
			// and no terminal should see it intact.
			b.WriteByte(controlReplacement)
		case keepLayout && (r == '\n' || r == '\t'):
			b.WriteRune(r)
		case unicode.IsControl(r):
			// C0, DEL and C1 — every escape-sequence introducer lives here.
			b.WriteByte(controlReplacement)
		default:
			b.WriteRune(r)
		}
		i += size
	}
	return b.String()
}

// meta is the "P2 enhancement (tests)" middle column of an issue line.
func meta(i model.Issue) string {
	p, _ := i.Priority()
	parts := []string{p.String()}
	if t, _ := i.Type(); t != "" {
		parts = append(parts, t)
	}
	if areas := i.Areas(); len(areas) > 0 {
		parts = append(parts, "("+SanitizeInline(strings.Join(areas, ","))+")")
	}
	return strings.Join(parts, " ")
}

// Line renders one issue as `#n meta  title`, unaligned. Under a color
// style the number is amber and the priority green.
func Line(i model.Issue, s Style) string {
	return fmt.Sprintf("%s %s  %s", s.numPadded(i.Number, len(strconv.Itoa(i.Number))), s.metaPadded(meta(i), len(meta(i))), SanitizeInline(i.Title))
}

// lineOpts tweak List output per caller.
type lineOpts struct {
	assignees bool // append @login (in-progress views)
	state     bool // append [closed] (mixed-state views)
	progress  bool // append sub-issue rollup n/m (epic views)
	annotate  bool // append [blocked by #n; epic n/m; ...] (list views)
}

// annotations explains, inline, why an issue isn't plain ready work.
func annotations(i model.Issue) string {
	var parts []string
	if i.IsEpic() {
		parts = append(parts, fmt.Sprintf("epic %d/%d", i.SubIssuesCompleted, i.SubIssuesTotal))
	}
	if blockers := i.OpenBlockers(); len(blockers) > 0 {
		refs := make([]string, len(blockers))
		for idx, n := range blockers {
			refs[idx] = fmt.Sprintf("#%d", n)
		}
		parts = append(parts, "blocked by "+strings.Join(refs, " "))
	}
	if i.Claimed() {
		claim := "in progress"
		if len(i.Assignees) > 0 {
			claim += " @" + SanitizeInline(strings.Join(i.Assignees, " @"))
		}
		parts = append(parts, claim)
	}
	if !i.IsOpen() {
		parts = append(parts, "closed")
	}
	if len(parts) == 0 {
		return ""
	}
	return "  [" + strings.Join(parts, "; ") + "]"
}

func lines(w io.Writer, issues []model.Issue, opts lineOpts, s Style) {
	numWidth, metaWidth := 0, 0
	metas := make([]string, len(issues))
	for idx, i := range issues {
		metas[idx] = meta(i)
		if n := len(strconv.Itoa(i.Number)); n > numWidth {
			numWidth = n
		}
		if len(metas[idx]) > metaWidth {
			metaWidth = len(metas[idx])
		}
	}
	for idx, i := range issues {
		fmt.Fprintf(w, "%s %s  %s", s.numPadded(i.Number, numWidth), s.metaPadded(metas[idx], metaWidth), SanitizeInline(i.Title))
		if opts.progress && i.IsEpic() {
			fmt.Fprintf(w, "  %s", s.dim(fmt.Sprintf("%d/%d", i.SubIssuesCompleted, i.SubIssuesTotal)))
		}
		if opts.assignees && len(i.Assignees) > 0 {
			fmt.Fprintf(w, "  %s", s.dim("@"+SanitizeInline(strings.Join(i.Assignees, " @"))))
		}
		if opts.state && !i.IsOpen() {
			fmt.Fprint(w, "  ", s.dim("[closed]"))
		}
		if opts.annotate {
			fmt.Fprint(w, s.dim(annotations(i)))
		}
		fmt.Fprintln(w)
	}
}

// List renders one aligned line per issue, annotated with whatever keeps
// it from being plain ready work.
func List(w io.Writer, issues []model.Issue, s Style) {
	lines(w, issues, lineOpts{annotate: true}, s)
}

// ListWithAssignees renders lines with @assignee suffixes (in-progress view).
func ListWithAssignees(w io.Writer, issues []model.Issue, s Style) {
	lines(w, issues, lineOpts{assignees: true}, s)
}

// EpicList renders epics with their progress rollups.
func EpicList(w io.Writer, issues []model.Issue, s Style) {
	lines(w, issues, lineOpts{progress: true}, s)
}

// Show renders the full detail view for one issue.
func Show(w io.Writer, i model.Issue, s Style) {
	fmt.Fprintln(w, Line(i, s))
	state := strings.ToLower(i.State)
	if i.StateReason != "" {
		state += " (" + strings.ToLower(i.StateReason) + ")"
	}
	line := fmt.Sprintf("state: %s  created: %s", SanitizeInline(state), i.CreatedAt.Format("2006-01-02"))
	if len(i.Assignees) > 0 {
		line += fmt.Sprintf("  assignee: @%s", SanitizeInline(strings.Join(i.Assignees, " @")))
	}
	fmt.Fprintln(w, s.dim(line))
	if i.Parent != nil {
		fmt.Fprintf(w, "parent: %s %s\n", s.num(i.Parent.Number), SanitizeInline(i.ParentTitle))
	}
	if len(i.BlockedBy) > 0 {
		fmt.Fprintf(w, "blocked by: %s\n", refList(i.BlockedBy, s))
	}
	if i.IsEpic() {
		fmt.Fprintf(w, "sub-issues (%d/%d done): %s\n", i.SubIssuesCompleted, i.SubIssuesTotal, refList(i.SubIssues, s))
	}
	if body := strings.TrimSpace(i.Body); body != "" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, sanitizeBlock(body))
	}
	if len(i.Comments) > 0 {
		fmt.Fprintln(w)
		header := "comments:"
		if i.CommentsTotal > len(i.Comments) {
			header = fmt.Sprintf("comments (showing last %d of %d):", len(i.Comments), i.CommentsTotal)
		}
		fmt.Fprintln(w, s.dim(header))
		for _, c := range i.Comments {
			fmt.Fprintf(w, "  @%s (%s): %s\n", SanitizeInline(c.Author),
				c.CreatedAt.Format("2006-01-02"), sanitizeBlock(strings.TrimSpace(c.Body)))
		}
	}
}

func refList(refs []model.Ref, s Style) string {
	parts := make([]string, len(refs))
	for idx, r := range refs {
		parts[idx] = s.refNum(r.Number, !r.IsOpen())
	}
	return strings.Join(parts, ", ")
}

// EpicStatus renders one epic and its children. Children are the epic's
// full parent-backlinked set (complete even when the sub-issue connection
// was capped); the rollup line keeps the server-side completed/total.
func EpicStatus(w io.Writer, epic model.Issue, children []model.Issue, s Style) {
	fmt.Fprintf(w, "%s  %s\n", Line(epic, s), s.dim(fmt.Sprintf("%d/%d", epic.SubIssuesCompleted, epic.SubIssuesTotal)))
	for _, child := range children {
		mark := "○"
		if !child.IsOpen() {
			mark = "✓"
		}
		fmt.Fprintf(w, "  %s %s %s  %s\n", mark, s.numPadded(child.Number, len(strconv.Itoa(child.Number))), s.metaPadded(meta(child), len(meta(child))), SanitizeInline(child.Title))
	}
}

// FormatCycle renders a cycle's member path as "#a → #b → … → #a".
func FormatCycle(path []int) string {
	parts := make([]string, len(path))
	for i, n := range path {
		parts[i] = "#" + strconv.Itoa(n)
	}
	return strings.Join(parts, " → ")
}

// FormatWarning renders a structured warning as the human sentence shown in
// prime and ready output. It is the single place warning prose lives.
func FormatWarning(w model.Warning) string {
	switch w.Kind {
	case model.WarnMultiPriority:
		return fmt.Sprintf("#%d has multiple priority labels; highest wins", w.Issue)
	case model.WarnMultiType:
		return fmt.Sprintf("#%d has multiple type labels; first of %s wins", w.Issue, strings.Join(model.Types, "|"))
	case model.WarnInProgressEpic:
		return fmt.Sprintf("#%d is an in-progress epic; epics are never worked directly", w.Issue)
	case model.WarnDependencyCycle:
		return "dependency cycle " + FormatCycle(w.Cycle) + ": none will be ready"
	case model.WarnSubIssuesCapped:
		return fmt.Sprintf("#%d has %d sub-issues, only %d fetched; counts may be incomplete", w.Issue, w.Total, w.Fetched)
	case model.WarnBlockersCapped:
		return fmt.Sprintf("#%d has %d blockers, only %d fetched; ready may be wrong", w.Issue, w.Total, w.Fetched)
	}
	return ""
}

// PrimeData is everything the primer needs, precomputed by the command.
type PrimeData struct {
	Repo       string
	Ready      []model.Issue // already capped to top N
	ReadyTotal int
	OpenTotal  int
	InProgress []model.Issue
	Epics      []model.Issue
	Warnings   []model.Warning
	Untriaged  int
}

// Prime renders the session-start primer: static conventions, live state,
// contradictions. Sections are omitted when empty.
func Prime(w io.Writer, static string, d PrimeData, s Style) {
	fmt.Fprintf(w, "# hew primer — %s\n", SanitizeInline(d.Repo))
	fmt.Fprintln(w, static)
	fmt.Fprintf(w, "\n## Ready (%d of %d open)\n", d.ReadyTotal, d.OpenTotal)
	if len(d.Ready) == 0 {
		fmt.Fprintln(w, "no ready work")
	} else {
		lines(w, d.Ready, lineOpts{}, s)
		if d.ReadyTotal > len(d.Ready) {
			fmt.Fprintln(w, s.dim(fmt.Sprintf("… %d more: hew ready", d.ReadyTotal-len(d.Ready))))
		}
	}
	if len(d.InProgress) > 0 {
		fmt.Fprintf(w, "\n## In progress (%d)\n", len(d.InProgress))
		lines(w, d.InProgress, lineOpts{assignees: true}, s)
	}
	if len(d.Epics) > 0 {
		fmt.Fprintln(w, "\n## Epics")
		lines(w, d.Epics, lineOpts{progress: true}, s)
	}
	if d.Untriaged > 0 {
		fmt.Fprintln(w, s.dim(fmt.Sprintf("\n%d untriaged → hew triage", d.Untriaged)))
	}
	if len(d.Warnings) > 0 {
		fmt.Fprintln(w, "\n## Warnings")
		for _, warn := range d.Warnings {
			fmt.Fprintf(w, "⚠ %s\n", FormatWarning(warn))
		}
	}
}
