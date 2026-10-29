package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/lumberbarons/hew/internal/conventions"
	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/local"
	"github.com/lumberbarons/hew/internal/model"
)

// MigrateLocalOpts configure the local-to-GitHub migration.
type MigrateLocalOpts struct {
	// Dir is the local tracker directory (.hew at the main checkout).
	Dir string
	// StatePath is where the local number → GitHub number checkpoint is kept
	// between a failed run and its rerun; default <Dir>/migration-state.json.
	// Bound to the repository and to a digest of issues.jsonl.
	StatePath string
	// DryRun prints the plan without touching GitHub or the tracker.
	DryRun bool
	// Throttle is slept between writes to stay under GitHub's secondary
	// rate limits for content creation.
	Throttle time.Duration
}

// MigrateLocal moves every issue in the local tracker — closed ones too — to
// the GitHub repository a.Client talks to, then flips the tracker's backend to
// GitHub and keeps issues.jsonl as an archive.
//
// The tracker is held under its lock for the whole run, so nothing written
// locally can slip in after the snapshot and be lost; writers wait, and once
// the tracker is sealed they find it is no longer local and refuse. The flip
// happens only when every step finished: a half-wired migration stays local so
// the rerun that completes it is still possible.
func (a *App) MigrateLocal(ctx context.Context, opts MigrateLocalOpts) error {
	if opts.Dir == "" {
		return usageErr("usage: hew migrate local")
	}
	if opts.StatePath == "" {
		opts.StatePath = filepath.Join(opts.Dir, "migration-state.json")
	}
	if err := requireLocalTracker(opts.Dir); err != nil {
		return err
	}

	var snap *local.Snapshot
	var frozen *local.Frozen
	if opts.DryRun {
		s, err := local.ReadSnapshot(opts.Dir)
		if err != nil {
			return genericErr("%v", err)
		}
		snap = s
	} else {
		fr, err := local.Freeze(ctx, opts.Dir)
		if err != nil {
			return genericErr("%v", err)
		}
		defer fr.Release()
		frozen, snap = fr, fr.Snapshot
	}

	// A dry run with no target repository never contacts GitHub, which is also
	// why it cannot vouch for a checkpoint: that has to be checked against the
	// issues it maps. Refuse rather than list work a real run would skip.
	targetless := opts.DryRun && a.Client == nil
	if targetless {
		if _, err := os.Stat(opts.StatePath); err == nil {
			return usageErr("%s holds a checkpoint from an earlier run, and a dry run can only check it against GitHub: pass --repo", opts.StatePath)
		}
		return a.localMigrationPlan(localItems(snap), map[string]int{})
	}

	state, err := loadBatchState(opts.StatePath, a.stateRepo(), snap.Digest, "issues.jsonl")
	if err != nil {
		return err
	}
	valid := make(map[string]bool, len(snap.Issues))
	for _, i := range snap.Issues {
		valid[localKey(i.Number)] = true
	}
	// Reads GitHub, writes nothing, so the dry run vouches for the checkpoint
	// exactly as the real run would (#81).
	if err := a.verifyBatchState(ctx, state, conventions.ProvenanceMigrate, valid, "local issue number"); err != nil {
		return err
	}

	for _, i := range snap.Issues {
		a.warnCommaLabels(i.Labels)
	}
	items := localItems(snap)
	if opts.DryRun {
		return a.localMigrationPlan(items, state.Mapping)
	}

	if err := a.ensureLabels(ctx, localLabels(snap)); err != nil {
		return err
	}
	res, err := a.runMigration(ctx, migrationEngine{
		Kind: conventions.ProvenanceMigrate, ItemNoun: "local issue", SourceNoun: "issues.jsonl",
		Items: items, StatePath: opts.StatePath, Throttle: opts.Throttle,
		Relink: func(text string, mapping map[string]int) string {
			return rewriteLocalRefs(text, localNumbers(mapping))
		},
	}, state)
	if err != nil {
		return err
	}
	if res.Failed > 0 {
		return genericErr("%d migration steps did not finish, so the tracker is still local: fix the cause and rerun `hew migrate local` to finish", res.Failed)
	}
	if err := frozen.Seal(a.Repo.String(), localNumbers(state.Mapping)); err != nil {
		return genericErr("everything is on GitHub but the tracker could not be switched over (rerun `hew migrate local` to retry): %v", err)
	}

	return a.emitResult(map[string]any{
		"created": res.Created, "wired": res.Wired, "closed": res.Closed, "relinked": res.Relinked,
		"warnings": res.Warnings, "mapping": state.Mapping, "backend": local.BackendGitHub,
	}, func() {
		a.printf("migrated %d local issues to %s: %d created, %d dependencies wired, %d closed, %d bodies relinked\n",
			len(items), a.Repo, res.Created, res.Wired, res.Closed, res.Relinked)
		a.printf("number map saved to %s; this repository now uses GitHub, and %s is kept as an archive\n",
			filepath.Join(opts.Dir, local.MigratedFile), filepath.Join(opts.Dir, "issues.jsonl"))
	})
}

// requireLocalTracker names what is wrong when dir is not a tracker that can
// be migrated, before anything is locked or read.
func requireLocalTracker(dir string) error {
	cfg, err := local.ReadConfig(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return genericErr("no local tracker at %s (hew init --local creates one)", dir)
	case err != nil:
		return genericErr("%v", err)
	case cfg.Backend == local.BackendLocal:
		return nil
	}
	if m, err := local.ReadMigrated(dir); err == nil {
		return genericErr("%s tracks issues in %q, not locally: it was already migrated to %s (see %s)",
			dir, cfg.Backend, m.Repo, filepath.Join(dir, local.MigratedFile))
	}
	return genericErr("%s tracks issues in %q, not locally", dir, cfg.Backend)
}

func localKey(number int) string { return strconv.Itoa(number) }

// localNumbers converts a key → GitHub number mapping back to local numbers.
// Keys that are not numbers cannot have come from a local tracker and are dropped.
func localNumbers(mapping map[string]int) map[int]int {
	out := make(map[int]int, len(mapping))
	for key, n := range mapping {
		if local, err := strconv.Atoi(key); err == nil {
			out[local] = n
		}
	}
	return out
}

// localItems maps the tracker's issues onto engine items, in number order. Labels
// carry over as written; assignees do not, because a local handle is not a
// GitHub login — an open issue that was claimed is claimed by the person
// migrating, and a closed one keeps no claim at all.
func localItems(snap *local.Snapshot) []migrationItem {
	items := make([]migrationItem, 0, len(snap.Issues))
	for _, i := range snap.Issues {
		closed := i.State == string(gh.StateClosed)
		labels := make([]string, 0, len(i.Labels))
		for _, l := range i.Labels {
			if closed && l == model.InProgressLabel {
				continue
			}
			labels = append(labels, migratedLabel(l))
		}
		item := migrationItem{
			Key: localKey(i.Number), Title: i.Title, Body: localBody(i), Labels: labels,
			Closed: closed,
		}
		if !closed && len(i.Assignees) > 0 {
			item.Assignees = []string{assigneeViewer}
		}
		if closed {
			switch reason := gh.CloseReason(i.StateReason); reason {
			case gh.CloseCompleted, gh.CloseNotPlanned, gh.CloseDuplicate:
				item.CloseReason = reason
			}
		}
		if i.Parent != 0 {
			item.Parent = localKey(i.Parent)
		}
		for _, b := range i.BlockedBy {
			item.BlockedBy = append(item.BlockedBy, localKey(b))
		}
		for _, c := range i.Comments {
			item.Comments = append(item.Comments, localComment(c))
		}
		items = append(items, item)
	}
	return items
}

// localBody is the issue's own text followed by a footer saying where it came
// from. The footer sets the local number in a code span so reference rewriting
// leaves the one place that names it alone.
func localBody(i local.Issue) string {
	footer := fmt.Sprintf("Migrated from local issue `#%d` (created %s)", i.Number, i.CreatedAt.Format("2006-01-02"))
	if body := strings.TrimSpace(i.Body); body != "" {
		return body + "\n\n---\n" + footer
	}
	return "---\n" + footer
}

// localComment credits the original author and date in a lead line GitHub's
// comment API has no field for. The author sits in a code span: local handles
// look like logins, and a bare one could mention a real account.
func localComment(c local.Comment) string {
	author := strings.TrimSpace(strings.ReplaceAll(c.Author, "`", "'"))
	if author == "" {
		author = "unknown"
	}
	return fmt.Sprintf("`%s` commented on %s:\n\n%s", author, c.CreatedAt.Format("2006-01-02"), c.Body)
}

// localLabels is every label the tracker uses that GitHub may not have yet, for
// the ensureLabels bootstrap: the tracker's own definitions where it has them,
// a neutral grey otherwise. Convention labels are the bootstrap's already and
// keep their canonical cosmetics.
func localLabels(snap *local.Snapshot) []gh.Label {
	defined := map[string]gh.Label{}
	for _, l := range snap.Labels {
		defined[strings.ToLower(l.Name)] = l
	}
	conventional := map[string]bool{}
	for _, l := range conventions.Labels {
		conventional[strings.ToLower(l.Name)] = true
	}
	var out []gh.Label
	seen := map[string]bool{}
	add := func(name string) {
		name = migratedLabel(name)
		key := strings.ToLower(name)
		if seen[key] || conventional[key] {
			return
		}
		seen[key] = true
		if l, ok := defined[key]; ok {
			out = append(out, l)
			return
		}
		out = append(out, gh.Label{Name: name, Color: "ededed", Description: "migrated from the local tracker"})
	}
	for _, l := range snap.Labels {
		add(l.Name)
	}
	for _, i := range snap.Issues {
		for _, l := range i.Labels {
			add(l)
		}
	}
	return out
}

// planLineLimit is how many per-issue lines a text dry run prints before it
// summarises instead; --json always lists every issue.
const planLineLimit = 20

// localMigrationPlan reports what a real run would do, in the same shape
// `migrate beads --dry-run` uses.
func (a *App) localMigrationPlan(items []migrationItem, state map[string]int) error {
	wouldMigrate := []migrationPlanEntry{}
	already := map[string]int{}
	type step struct {
		key      string
		migrated int
		entry    migrationPlanEntry
	}
	steps := make([]step, 0, len(items))
	for _, it := range items {
		if n, ok := state[it.Key]; ok {
			already[it.Key] = n
			steps = append(steps, step{key: it.Key, migrated: n})
			continue
		}
		entry := planEntry(it)
		wouldMigrate = append(wouldMigrate, entry)
		steps = append(steps, step{key: it.Key, entry: entry})
	}
	return a.emitResult(map[string]any{
		"dryRun": true, "wouldMigrate": wouldMigrate, "alreadyMigrated": already,
	}, func() {
		if len(steps) > planLineLimit {
			closing := 0
			for _, e := range wouldMigrate {
				if e.ThenClose {
					closing++
				}
			}
			a.printf("%d would be created, %d would be closed, %d already migrated\n",
				len(wouldMigrate), closing, len(already))
			a.printf("dry run: %d local issues would be migrated; per-issue lines are omitted above %d issues, use --json for the full plan\n",
				len(items), planLineLimit)
			return
		}
		for _, st := range steps {
			if st.migrated != 0 {
				a.printf("already migrated: %s → #%d\n", st.key, st.migrated)
				continue
			}
			a.printf("%s\n", planLine(st.entry))
		}
		a.printf("dry run: %d local issues would be migrated\n", len(items))
	})
}

// planEntry describes one item a dry run would create.
func planEntry(it migrationItem) migrationPlanEntry {
	view := model.Issue{Labels: it.Labels}
	p, _ := view.Priority()
	entry := migrationPlanEntry{
		ID: it.Key, Priority: p.String(), Title: it.Title,
		BlockedBy: append([]string{}, it.BlockedBy...), InProgress: view.InProgress(), ThenClose: it.Closed,
	}
	if t, _ := view.Type(); t != "" {
		entry.Type = &t
	}
	if it.Parent != "" {
		parent := it.Parent
		entry.Parent = &parent
	}
	return entry
}

// blankLine ends a paragraph, and with it any code span still open in it.
var blankLine = regexp.MustCompile(`\r?\n[ \t]*\r?\n`)

// rewriteLocalRefs replaces each #n that names a key of mapping with #mapping[n],
// leaving code alone: inline code spans, fenced blocks (including ones nested in
// a list item or blockquote), indented code blocks, and anything that only
// looks like a reference — part of a word, a URL fragment, an HTML entity, a
// number the mapping does not know. A local issue's #n means nothing on GitHub,
// where the numbers are new.
func rewriteLocalRefs(text string, mapping map[int]int) string {
	var out, chunk strings.Builder
	flush := func() {
		out.WriteString(rewriteInline(chunk.String(), mapping))
		chunk.Reset()
	}
	var (
		stack []container
		fence string // the opening fence while inside a fenced block
		para  bool   // the previous line was paragraph text, which an indented line continues
	)
	for rest := text; rest != ""; {
		line := rest
		if i := strings.IndexByte(rest, '\n'); i >= 0 {
			line = rest[:i+1]
		}
		rest = rest[len(line):]

		body, matched := continueContainers(line, stack)
		var sibling byte
		if matched < len(stack) {
			sibling = stack[matched].marker
			if para && fence == "" && continuesParagraph(body, sibling) {
				// Paragraph continuations may omit quote markers or list
				// indentation. Keep their containers for subsequent lines.
				chunk.WriteString(line)
				continue
			}
			// A container the line does not continue has ended, and takes
			// any fence opened inside it along.
			flush()
			stack, fence = stack[:matched], ""
		}
		if fence != "" {
			out.WriteString(line)
			if closesFence(body, fence) {
				fence, para = "", false
			}
			continue
		}
		for !isThematicBreak(body) {
			c, after, ok := containerStart(body, para, sibling)
			if !ok {
				break
			}
			// Inline code cannot continue into a new list item or quote.
			flush()
			stack, body, para = append(stack, c), after, false
			sibling = 0
		}
		opened, blank := opensFence(body), strings.TrimSpace(body) == ""
		switch {
		case blank:
			// A marker-only line (such as ">") is a paragraph break too.
			flush()
			out.WriteString(line)
			para = false
		case opened != "":
			flush()
			fence, para = opened, false
			out.WriteString(line)
		case !para && !blank && indentColumns(body) >= 4:
			flush()
			out.WriteString(line)
		default:
			chunk.WriteString(line)
			para = !blank && !endsParagraph(body)
		}
	}
	flush()
	return out.String()
}

// container is an open blockquote or list item. A list item's indent is how far
// its continuation lines must be indented to belong to it.
type container struct {
	quote  bool
	indent int
	marker byte // list bullet or ordered delimiter, to recognize sibling items
}

// continueContainers strips from line the markers and indentation of the
// containers in stack, outermost first, and returns the rest along with how
// many of them the line continues. A blank line continues a list item but ends
// a blockquote.
func continueContainers(line string, stack []container) (body string, matched int) {
	body = line
	for _, c := range stack {
		switch {
		case c.quote:
			trimmed := strings.TrimLeft(body, " ")
			if len(body)-len(trimmed) > 3 || !strings.HasPrefix(trimmed, ">") {
				return body, matched
			}
			body = strings.TrimPrefix(trimmed[1:], " ")
		case strings.TrimSpace(body) == "":
		case indentColumns(body) >= c.indent:
			body = dropColumns(body, c.indent)
		default:
			return body, matched
		}
		matched++
	}
	return body, matched
}

// containerStart reads a blockquote or list-item marker at the start of s, after
// up to three spaces of indentation, and returns the container it opens and what
// follows the marker. Five or more spaces after a list marker are one space of
// separation and an indented code block. In a paragraph, a new list must have
// nonempty content and an ordered list must start at 1; sibling items in an
// existing list (identified by its marker) are exempt from those restrictions.
func containerStart(s string, paragraph bool, sibling byte) (c container, after string, ok bool) {
	trimmed := strings.TrimLeft(s, " ")
	lead := len(s) - len(trimmed)
	if lead > 3 || trimmed == "" {
		return container{}, "", false
	}
	if trimmed[0] == '>' {
		return container{quote: true}, strings.TrimPrefix(trimmed[1:], " "), true
	}
	marker := 1
	if !strings.ContainsRune("-*+", rune(trimmed[0])) {
		marker = len(trimmed) - len(strings.TrimLeft(trimmed, "0123456789"))
		if marker < 1 || marker > 9 || marker >= len(trimmed) || (trimmed[marker] != '.' && trimmed[marker] != ')') {
			return container{}, "", false
		}
		marker++
	}
	tail := trimmed[marker:]
	if tail != "" && !strings.ContainsRune(" \t\r\n", rune(tail[0])) {
		return container{}, "", false
	}
	if paragraph && trimmed[marker-1] != sibling {
		if strings.TrimSpace(tail) == "" {
			return container{}, "", false
		}
		if marker > 1 {
			n, _ := strconv.Atoi(trimmed[:marker-1]) // at most nine digits
			if n != 1 {
				return container{}, "", false
			}
		}
	}
	gap := markerGap(tail, lead+marker)
	if gap > 4 || strings.TrimSpace(tail) == "" {
		gap = min(gap, 1)
	}
	return container{indent: lead + marker + max(gap, 1), marker: trimmed[marker-1]}, dropColumns(tail, gap), true
}

// continuesParagraph distinguishes lazy continuation text from a new block at
// the first unmatched container. A sibling list item also ends the paragraph.
func continuesParagraph(body string, sibling byte) bool {
	if strings.TrimSpace(body) == "" {
		return false
	}
	if indentColumns(body) >= 4 {
		// Indented code cannot interrupt a paragraph, and these markers
		// are too deeply indented to start another kind of block.
		return true
	}
	_, _, starts := containerStart(body, true, sibling)
	return !endsParagraph(body) && opensFence(body) == "" && !starts
}

// endsParagraph reports whether body is a line that is not paragraph text and
// so leaves an indented line after it as code: an ATX heading, a thematic
// break, or a setext underline.
func endsParagraph(body string) bool {
	trimmed := strings.TrimSpace(body)
	return isHeading(body) || isThematicBreak(body) || (trimmed != "" && strings.Trim(trimmed, "=") == "")
}

// isThematicBreak reports whether body is three or more -, * or _ alone on the
// line, spaces allowed between them. It takes precedence over a list marker.
func isThematicBreak(body string) bool {
	trimmed := strings.TrimLeft(body, " ")
	if len(body)-len(trimmed) > 3 || trimmed == "" || !strings.ContainsRune("-*_", rune(trimmed[0])) {
		return false
	}
	count := 0
	for _, r := range trimmed {
		switch {
		case r == rune(trimmed[0]):
			count++
		case !strings.ContainsRune(" \t\r\n", r):
			return false
		}
	}
	return count >= 3
}

// markerGap is how many columns of whitespace start tail, which begins at
// column col; a tab reaches the next multiple of four.
func markerGap(tail string, col int) int {
	gap := 0
	for _, r := range tail {
		switch r {
		case ' ':
			gap++
		case '\t':
			gap += 4 - (col+gap)%4
		default:
			return gap
		}
	}
	return gap
}

// isHeading reports whether body is an ATX heading, which ends a paragraph.
func isHeading(body string) bool {
	trimmed := strings.TrimLeft(body, " ")
	hashes := len(trimmed) - len(strings.TrimLeft(trimmed, "#"))
	if hashes < 1 || hashes > 6 {
		return false
	}
	return hashes == len(trimmed) || strings.ContainsRune(" \t\r\n", rune(trimmed[hashes]))
}

// indentColumns is the width of s's leading whitespace, a tab counting as four.
func indentColumns(s string) int {
	cols := 0
	for _, r := range s {
		switch r {
		case ' ':
			cols++
		case '\t':
			cols += 4
		default:
			return cols
		}
	}
	return cols
}

// dropColumns removes n columns of leading whitespace from s, a tab counting as
// four; a tab straddling the boundary goes whole.
func dropColumns(s string, n int) string {
	for cols := 0; cols < n && s != "" && (s[0] == ' ' || s[0] == '\t'); s = s[1:] {
		if s[0] == '\t' {
			cols += 3
		}
		cols++
	}
	return s
}

// opensFence returns the fence a line opens (its whole run of ` or ~), or "".
// A backtick fence whose info string holds a backtick is inline code instead.
func opensFence(line string) string {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 || len(trimmed) < 3 || (trimmed[0] != '`' && trimmed[0] != '~') {
		return ""
	}
	run := len(trimmed) - len(strings.TrimLeft(trimmed, trimmed[:1]))
	if run < 3 || (trimmed[0] == '`' && strings.Contains(trimmed[run:], "`")) {
		return ""
	}
	return trimmed[:run]
}

// closesFence reports whether line is a closing fence for fence: the same
// character, at least as long, and nothing else on the line.
func closesFence(line, fence string) bool {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		return false
	}
	run := strings.TrimLeft(trimmed, fence[:1])
	return len(trimmed)-len(run) >= len(fence) && strings.TrimSpace(run) == ""
}

// rewriteInline rewrites references paragraph by paragraph, since a code span
// cannot cross a blank line.
func rewriteInline(text string, mapping map[int]int) string {
	var out strings.Builder
	last := 0
	for _, loc := range blankLine.FindAllStringIndex(text, -1) {
		out.WriteString(rewriteParagraph(text[last:loc[0]], mapping))
		out.WriteString(text[loc[0]:loc[1]])
		last = loc[1]
	}
	out.WriteString(rewriteParagraph(text[last:], mapping))
	return out.String()
}

func rewriteParagraph(p string, mapping map[int]int) string {
	var out strings.Builder
	for i := 0; i < len(p); {
		switch p[i] {
		case '`':
			end := codeSpanEnd(p, i)
			out.WriteString(p[i:end])
			i = end
		case '#':
			end, n, ok := refAt(p, i)
			if to, known := mapping[n]; ok && known {
				out.WriteString("#" + strconv.Itoa(to))
			} else {
				out.WriteString(p[i:end])
			}
			i = end
		default:
			out.WriteByte(p[i])
			i++
		}
	}
	return out.String()
}

// codeSpanEnd returns where the backtick run at start ends: after the matching
// closing run when there is one, otherwise just past the opening run, which is
// then literal backticks.
func codeSpanEnd(p string, start int) int {
	n := runLength(p, start, '`')
	for i := start + n; i < len(p); {
		next := strings.IndexByte(p[i:], '`')
		if next < 0 {
			break
		}
		i += next
		m := runLength(p, i, '`')
		if m == n {
			return i + m
		}
		i += m
	}
	return start + n
}

func runLength(p string, start int, c byte) int {
	n := 0
	for start+n < len(p) && p[start+n] == c {
		n++
	}
	return n
}

// refAt reads a reference at the '#' at start. It returns where to resume, the
// number, and whether this really is a reference rather than text that happens
// to contain '#' and digits.
func refAt(p string, start int) (end, n int, ok bool) {
	end = start + 1
	if start > 0 {
		prev, _ := utf8.DecodeLastRuneInString(p[:start])
		if isWordRune(prev) || strings.ContainsRune("/&#", prev) {
			return end, 0, false
		}
	}
	for end < len(p) && p[end] >= '0' && p[end] <= '9' {
		end++
	}
	if end == start+1 {
		return end, 0, false
	}
	if end < len(p) {
		if next, _ := utf8.DecodeRuneInString(p[end:]); isWordRune(next) {
			return end, 0, false
		}
	}
	n, err := strconv.Atoi(p[start+1 : end])
	return end, n, err == nil
}

func isWordRune(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }
