package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
)

// primeBudget is the target DESIGN.md sets for the primer. The harness
// measures against it rather than asserting it: a primer that grows is a
// judgement call, not a build failure.
const primeBudget = 600

// primeCommand is the entry whose token count is checked against primeBudget.
const primeCommand = "prime"

// Row is one hew-versus-baseline comparison.
type Row struct {
	Command  string `json:"command"`
	PerIssue bool   `json:"perIssue"`
	Baseline string `json:"baseline"`
	Partial  bool   `json:"partial"`
	Note     string `json:"note,omitempty"`

	HewTokens      int `json:"hewTokens"`
	HewBytes       int `json:"hewBytes"`
	BaselineTokens int `json:"baselineTokens"`
	BaselineBytes  int `json:"baselineBytes"`
}

// Ratio is how many tokens the baseline spends per token hew spends.
func (r Row) Ratio() float64 {
	if r.HewTokens == 0 {
		return 0
	}
	return float64(r.BaselineTokens) / float64(r.HewTokens)
}

// Report is a fixture's measured comparison, ready to render.
type Report struct {
	Repo       string `json:"repo"`
	CapturedAt string `json:"capturedAt"`
	HewVersion string `json:"hewVersion"`
	OpenIssues int    `json:"openIssues"`
	Truncated  bool   `json:"truncated"`
	Encoding   string `json:"encoding"`
	Rows       []Row  `json:"rows"`
}

// buildReport tokenizes a captured fixture. Every file is counted once even
// though baselines are shared between entries.
func buildReport(dir string, c *counter) (*Report, error) {
	raw, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return nil, fmt.Errorf("read fixture manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Join(dir, manifestName), err)
	}
	if len(m.Entries) == 0 {
		return nil, fmt.Errorf("%s: fixture records no entries", filepath.Join(dir, manifestName))
	}

	tokens := map[string]int{}
	bytesOf := map[string]int{}
	measure := func(c2 Capture) error {
		if _, done := tokens[c2.File]; done {
			return nil
		}
		body, err := os.ReadFile(filepath.Join(dir, c2.File))
		if err != nil {
			return fmt.Errorf("read captured output: %w", err)
		}
		n, err := c.count(string(body))
		if err != nil {
			return err
		}
		tokens[c2.File], bytesOf[c2.File] = n, len(body)
		return nil
	}

	rep := &Report{
		Repo:       m.Repo,
		CapturedAt: m.CapturedAt,
		HewVersion: m.HewVersion,
		OpenIssues: m.OpenIssues,
		Truncated:  m.Truncated,
		Encoding:   encodingName,
	}
	for _, e := range m.Entries {
		if err := measure(e.Hew); err != nil {
			return nil, err
		}
		for _, b := range e.Baselines {
			row := Row{
				Command:   e.Command,
				PerIssue:  e.PerIssue,
				Baseline:  b.Name,
				Partial:   b.Partial,
				Note:      b.Note,
				HewTokens: tokens[e.Hew.File],
				HewBytes:  bytesOf[e.Hew.File],
			}
			for _, capt := range b.Captures {
				if err := measure(capt); err != nil {
					return nil, err
				}
				row.BaselineTokens += tokens[capt.File]
				row.BaselineBytes += bytesOf[capt.File]
			}
			rep.Rows = append(rep.Rows, row)
		}
	}
	return rep, nil
}

// equivalentRatios returns the ratios of the baselines that actually answer
// the same question, sorted. Partial baselines are excluded: comparing against
// an answer that isn't one would flatter the tool.
func (r *Report) equivalentRatios() []float64 {
	var out []float64
	for _, row := range r.Rows {
		if !row.Partial {
			out = append(out, row.Ratio())
		}
	}
	sort.Float64s(out)
	return out
}

// primeTokens returns the primer's measured size, or 0 when the fixture has no
// prime entry.
func (r *Report) primeTokens() int {
	for _, row := range r.Rows {
		if row.Command == primeCommand {
			return row.HewTokens
		}
	}
	return 0
}

func ratio(f float64) string { return strconv.FormatFloat(f, 'f', 1, 64) + "x" }

func perIssue(tokens, issues int) string {
	if issues == 0 {
		return "-"
	}
	return strconv.FormatFloat(float64(tokens)/float64(issues), 'f', 1, 64)
}

func (r *Report) header() string {
	h := fmt.Sprintf("%s — %d open issues, captured %s, %s", r.Repo, r.OpenIssues, r.CapturedAt, r.HewVersion)
	if r.Truncated {
		h += " (open issues exceed one page: per-issue figures are floors)"
	}
	return h
}

// WriteText renders the report as an aligned table for a terminal.
func (r *Report) WriteText(w io.Writer) {
	fmt.Fprintln(w, r.header())
	fmt.Fprintf(w, "tokens measured with tiktoken %s\n\n", r.Encoding)

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "command\thew\tbaseline\tratio\tper issue (hew/raw)\tbaseline source")
	for _, row := range r.Rows {
		per := "-"
		if row.PerIssue {
			per = perIssue(row.HewTokens, r.OpenIssues) + " / " + perIssue(row.BaselineTokens, r.OpenIssues)
		}
		source := row.Baseline
		if row.Partial {
			source += " †"
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%s\t%s\t%s\n",
			row.Command, row.HewTokens, row.BaselineTokens, ratio(row.Ratio()), per, source)
	}
	tw.Flush()

	r.writeNotes(w)
	r.writeSummary(w)
}

// WriteMarkdown renders the report as a GFM table, for pasting into DESIGN.md.
func (r *Report) WriteMarkdown(w io.Writer) {
	fmt.Fprintf(w, "%s. Tokens measured with tiktoken `%s`.\n\n", r.header(), r.Encoding)
	fmt.Fprintln(w, "| command | hew | raw gh | ratio | baseline |")
	fmt.Fprintln(w, "|---|---|---|---|---|")
	for _, row := range r.Rows {
		source := row.Baseline
		if row.Partial {
			source += " †"
		}
		fmt.Fprintf(w, "| `hew %s` | %d | %d | %s | %s |\n",
			row.Command, row.HewTokens, row.BaselineTokens, ratio(row.Ratio()), source)
	}
	r.writeNotes(w)
	r.writeSummary(w)
}

func (r *Report) writeNotes(w io.Writer) {
	seen := map[string]bool{}
	var notes []string
	for _, row := range r.Rows {
		if row.Note == "" || seen[row.Note] {
			continue
		}
		seen[row.Note] = true
		marker := ""
		if row.Partial {
			marker = "† "
		}
		notes = append(notes, fmt.Sprintf("%s%s: %s", marker, row.Baseline, row.Note))
	}
	if len(notes) == 0 {
		return
	}
	fmt.Fprintln(w)
	for _, n := range notes {
		fmt.Fprintln(w, n)
	}
}

// writeSummary states the range rather than a total: `ready`, `list`, and
// `prime` all read the same tracker state, so adding their costs up would
// count that state three times.
func (r *Report) writeSummary(w io.Writer) {
	fmt.Fprintln(w)
	if ratios := r.equivalentRatios(); len(ratios) > 0 {
		fmt.Fprintf(w, "same-information ratios: %s–%s (median %s over %d comparisons)\n",
			ratio(ratios[0]), ratio(ratios[len(ratios)-1]), ratio(median(ratios)), len(ratios))
	}
	if tokens := r.primeTokens(); tokens > 0 {
		fmt.Fprintf(w, "prime: %d tokens against DESIGN.md's ~%d-token target\n", tokens, primeBudget)
	}
}

func median(sorted []float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// WriteJSON emits the machine-readable form, ratios included so consumers
// don't recompute them.
func (r *Report) WriteJSON(w io.Writer) error {
	type jsonRow struct {
		Row
		Ratio float64 `json:"ratio"`
	}
	out := struct {
		*Report
		Rows []jsonRow `json:"rows"`
	}{Report: r}
	for _, row := range r.Rows {
		out.Rows = append(out.Rows, jsonRow{Row: row, Ratio: round1(row.Ratio())})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func round1(f float64) float64 {
	v, err := strconv.ParseFloat(strconv.FormatFloat(f, 'f', 1, 64), 64)
	if err != nil {
		return f
	}
	return v
}

// formats are the supported --format values.
var formats = []string{"text", "markdown", "json"}

func (r *Report) Write(w io.Writer, format string) error {
	switch format {
	case "text":
		r.WriteText(w)
	case "markdown":
		r.WriteMarkdown(w)
	case "json":
		return r.WriteJSON(w)
	default:
		return fmt.Errorf("unknown --format %q (want %s)", format, strings.Join(formats, ", "))
	}
	return nil
}
