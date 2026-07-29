package main

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	readyOut  = "#42 P2 task  Wire voltgo into config\n#43 P3 bug  Epever decoding\n"
	primeOut  = "# hew primer — lumberbarons/solar-controller\nWorkflow: hew ready → hew start <n>\n"
	gqlOut    = `{"data":{"repository":{"issues":{"nodes":[{"number":42},{"number":43}]}}}}`
	depsOut   = `{"data":{"repository":{"issue":{"blockedBy":{"totalCount":0,"nodes":[]}}}}}`
	ghListOut = `[{"number":42,"labels":[{"id":"LA_kwD","name":"P2","description":"Default","color":"0e8a16"}]}]`
)

// writeFixture lays down a fixture whose token counts the test can recompute,
// so the assertions are about the report's arithmetic, not the tokenizer's.
func writeFixture(t *testing.T, m Manifest, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, manifestName), raw, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return dir
}

func sampleManifest() Manifest {
	equivalent := Baseline{
		Name: "gh api graphql (open issues)",
		Captures: []Capture{
			{File: "gql.json", Argv: []string{"gh", "api", "graphql"}},
			{File: "deps.json", Argv: []string{"gh", "api", "graphql"}},
		},
	}
	return Manifest{
		Repo:       "lumberbarons/solar-controller",
		CapturedAt: "2026-07-29T12:00:00Z",
		HewVersion: "hew version 1.2.3",
		OpenIssues: 2,
		Entries: []Entry{
			{
				Command:  "ready",
				PerIssue: true,
				Hew:      Capture{File: "ready.txt"},
				Baselines: []Baseline{
					equivalent,
					{
						Name:     "gh issue list --json",
						Partial:  true,
						Note:     "gh cannot return blockedBy",
						Captures: []Capture{{File: "ghlist.json"}},
					},
				},
			},
			{
				Command:   "prime",
				Hew:       Capture{File: "prime.txt"},
				Baselines: []Baseline{equivalent},
			},
		},
	}
}

func sampleFiles() map[string]string {
	return map[string]string{
		"ready.txt":   readyOut,
		"prime.txt":   primeOut,
		"gql.json":    gqlOut,
		"deps.json":   depsOut,
		"ghlist.json": ghListOut,
	}
}

func buildSampleReport(t *testing.T) (*Report, *counter) {
	t.Helper()
	c := newTestCounter(t)
	rep, err := buildReport(writeFixture(t, sampleManifest(), sampleFiles()), c)
	if err != nil {
		t.Fatalf("buildReport: %v", err)
	}
	return rep, c
}

func count(t *testing.T, c *counter, s string) int {
	t.Helper()
	n, err := c.count(s)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func TestBuildReportSumsEveryCaptureInABaseline(t *testing.T) {
	rep, c := buildSampleReport(t)
	if len(rep.Rows) != 3 {
		t.Fatalf("rows = %d, want one per (command, baseline) pair", len(rep.Rows))
	}
	want := count(t, c, gqlOut) + count(t, c, depsOut)
	if rep.Rows[0].BaselineTokens != want {
		t.Errorf("baselineTokens = %d, want %d (both captures summed)", rep.Rows[0].BaselineTokens, want)
	}
	if got, wantBytes := rep.Rows[0].BaselineBytes, len(gqlOut)+len(depsOut); got != wantBytes {
		t.Errorf("baselineBytes = %d, want %d", got, wantBytes)
	}
	if got := rep.Rows[0].HewTokens; got != count(t, c, readyOut) {
		t.Errorf("hewTokens = %d, want %d", got, count(t, c, readyOut))
	}
}

func TestBuildReportCarriesFixtureProvenance(t *testing.T) {
	rep, _ := buildSampleReport(t)
	if rep.Repo != "lumberbarons/solar-controller" || rep.OpenIssues != 2 {
		t.Errorf("report = %+v, want the manifest's repo and open count", rep)
	}
	if rep.Encoding != encodingName {
		t.Errorf("encoding = %q, want %q", rep.Encoding, encodingName)
	}
	if !strings.Contains(rep.header(), "hew version 1.2.3") {
		t.Errorf("header %q omits the measured binary", rep.header())
	}
}

func TestRatioIsBaselinePerHewToken(t *testing.T) {
	row := Row{HewTokens: 100, BaselineTokens: 250}
	if got := row.Ratio(); math.Abs(got-2.5) > 1e-9 {
		t.Errorf("Ratio() = %v, want 2.5", got)
	}
	if got := (Row{HewTokens: 0, BaselineTokens: 250}).Ratio(); got != 0 {
		t.Errorf("Ratio() with no hew tokens = %v, want 0", got)
	}
}

// Partial baselines are the cheap-but-wrong option; folding them into the
// headline range would credit hew with beating an answer that isn't one.
func TestEquivalentRatiosExcludePartialBaselines(t *testing.T) {
	rep, _ := buildSampleReport(t)
	if got := len(rep.equivalentRatios()); got != 2 {
		t.Fatalf("equivalent ratios = %d, want 2 of 3 rows", got)
	}
}

func TestPrimeTokensFoundOnlyWhenCaptured(t *testing.T) {
	rep, c := buildSampleReport(t)
	if got := rep.primeTokens(); got != count(t, c, primeOut) {
		t.Errorf("primeTokens = %d, want %d", got, count(t, c, primeOut))
	}

	m := sampleManifest()
	m.Entries = m.Entries[:1] // drop prime
	rep2, err := buildReport(writeFixture(t, m, sampleFiles()), c)
	if err != nil {
		t.Fatalf("buildReport: %v", err)
	}
	if got := rep2.primeTokens(); got != 0 {
		t.Errorf("primeTokens = %d for a fixture without prime, want 0", got)
	}
}

func TestTextReportMarksAndExplainsPartialBaselines(t *testing.T) {
	rep, _ := buildSampleReport(t)
	var buf bytes.Buffer
	rep.WriteText(&buf)
	out := buf.String()

	for _, want := range []string{
		"gh issue list --json †",
		"† gh issue list --json: gh cannot return blockedBy",
		"same-information ratios:",
		"median",
		"prime: ",
		"DESIGN.md's ~600-token target",
		"tiktoken o200k_base",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text report missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "† gh api graphql") {
		t.Errorf("equivalent baseline marked partial:\n%s", out)
	}
}

func TestTextReportShowsPerIssueOnlyForWholeTrackerReads(t *testing.T) {
	rep, c := buildSampleReport(t)
	var buf bytes.Buffer
	rep.WriteText(&buf)
	lines := strings.Split(buf.String(), "\n")

	readyLine, primeLine := findLine(t, lines, "ready"), findLine(t, lines, "prime  ")
	// Two open issues in the fixture, so per-issue is half the total.
	wantPer := perIssue(count(t, c, readyOut), 2)
	if !strings.Contains(readyLine, wantPer) {
		t.Errorf("ready line %q missing per-issue figure %q", readyLine, wantPer)
	}
	if strings.Count(primeLine, "-") == 0 {
		t.Errorf("prime line %q should have no per-issue figure", primeLine)
	}
}

func findLine(t *testing.T, lines []string, prefix string) string {
	t.Helper()
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return l
		}
	}
	t.Fatalf("no line starting %q in %v", prefix, lines)
	return ""
}

func TestMarkdownReportEmitsATable(t *testing.T) {
	rep, _ := buildSampleReport(t)
	var buf bytes.Buffer
	rep.WriteMarkdown(&buf)
	out := buf.String()
	for _, want := range []string{"| command | hew | raw gh | ratio | baseline |", "|---|", "| `hew ready` |"} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown missing %q:\n%s", want, out)
		}
	}
}

func TestJSONReportRoundTripsWithRatios(t *testing.T) {
	rep, _ := buildSampleReport(t)
	var buf bytes.Buffer
	if err := rep.WriteJSON(&buf); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	var got struct {
		Repo string `json:"repo"`
		Rows []struct {
			Command        string  `json:"command"`
			Ratio          float64 `json:"ratio"`
			BaselineTokens int     `json:"baselineTokens"`
			HewTokens      int     `json:"hewTokens"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("parse json report: %v\n%s", err, buf.String())
	}
	if got.Repo != rep.Repo {
		t.Errorf("repo = %q, want %q", got.Repo, rep.Repo)
	}
	if len(got.Rows) != len(rep.Rows) {
		t.Fatalf("rows = %d, want %d", len(got.Rows), len(rep.Rows))
	}
	first := got.Rows[0]
	if first.Ratio != round1(float64(first.BaselineTokens)/float64(first.HewTokens)) {
		t.Errorf("ratio %v does not match %d/%d", first.Ratio, first.BaselineTokens, first.HewTokens)
	}
}

func TestWriteRejectsUnknownFormat(t *testing.T) {
	rep, _ := buildSampleReport(t)
	err := rep.Write(&bytes.Buffer{}, "yaml")
	if err == nil {
		t.Fatal("Write accepted an unknown format")
	}
	for _, want := range formats {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not offer %q", err, want)
		}
	}
}

func TestBuildReportRejectsUnusableFixtures(t *testing.T) {
	c := newTestCounter(t)

	t.Run("no manifest", func(t *testing.T) {
		if _, err := buildReport(t.TempDir(), c); err == nil {
			t.Fatal("buildReport accepted a directory with no manifest")
		}
	})

	t.Run("unparseable manifest", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, manifestName), []byte("{nope"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := buildReport(dir, c); err == nil {
			t.Fatal("buildReport accepted an unparseable manifest")
		}
	})

	t.Run("no entries", func(t *testing.T) {
		dir := writeFixture(t, Manifest{Repo: "a/b"}, nil)
		if _, err := buildReport(dir, c); err == nil {
			t.Fatal("buildReport accepted a manifest with no entries")
		} else if !strings.Contains(err.Error(), "no entries") {
			t.Fatalf("error = %q", err)
		}
	})

	t.Run("missing captured file", func(t *testing.T) {
		files := sampleFiles()
		delete(files, "deps.json")
		dir := writeFixture(t, sampleManifest(), files)
		if _, err := buildReport(dir, c); err == nil {
			t.Fatal("buildReport accepted a manifest referencing a missing file")
		} else if !strings.Contains(err.Error(), "read captured output") {
			t.Fatalf("error = %q", err)
		}
	})
}

func TestMedianHandlesBothParities(t *testing.T) {
	cases := []struct {
		in   []float64
		want float64
	}{
		{nil, 0},
		{[]float64{2}, 2},
		{[]float64{1, 2, 6}, 2},
		{[]float64{1, 2, 4, 6}, 3},
	}
	for _, tc := range cases {
		if got := median(tc.in); math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("median(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestPerIssueGuardsAgainstAnEmptyFixture(t *testing.T) {
	if got := perIssue(100, 0); got != "-" {
		t.Errorf("perIssue(100, 0) = %q, want %q", got, "-")
	}
	if got := perIssue(100, 8); got != "12.5" {
		t.Errorf("perIssue(100, 8) = %q, want %q", got, "12.5")
	}
}

func TestHeaderFlagsTruncatedFixtures(t *testing.T) {
	m := sampleManifest()
	m.Truncated = true
	rep, err := buildReport(writeFixture(t, m, sampleFiles()), newTestCounter(t))
	if err != nil {
		t.Fatalf("buildReport: %v", err)
	}
	if !strings.Contains(rep.header(), "floors") {
		t.Errorf("header %q does not warn that per-issue figures are floors", rep.header())
	}
}
