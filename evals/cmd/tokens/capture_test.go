package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeRunner answers the capture spec's commands without gh, recording how
// often each argv ran so dedupe can be asserted.
type fakeRunner struct {
	calls      []string
	openIssues int
	nextPage   bool
	empty      string // argv substring whose command returns nothing
	broken     bool   // the open-issue response is not JSON
}

func (f *fakeRunner) run(argv []string) ([]byte, error) {
	joined := strings.Join(argv, " ")
	f.calls = append(f.calls, joined)
	switch {
	case f.empty != "" && strings.Contains(joined, f.empty):
		return []byte("  \n"), nil
	case strings.Contains(joined, "--version"):
		return []byte("hew version 1.2.3\n"), nil
	case strings.Contains(joined, "issues-open.graphql"):
		if f.broken {
			return []byte("not json at all"), nil
		}
		return openIssuesResponse(f.openIssues, f.nextPage), nil
	default:
		return []byte("output of " + joined + "\n"), nil
	}
}

func (f *fakeRunner) countOf(substr string) int {
	n := 0
	for _, c := range f.calls {
		if strings.Contains(c, substr) {
			n++
		}
	}
	return n
}

func openIssuesResponse(n int, hasNextPage bool) []byte {
	nodes := make([]string, 0, n)
	for i := range n {
		nodes = append(nodes, fmt.Sprintf(`{"number":%d,"title":"issue %d","state":"OPEN"}`, 100+i, i))
	}
	return fmt.Appendf(nil,
		`{"data":{"repository":{"issues":{"pageInfo":{"hasNextPage":%t},"nodes":[%s]}}}}`,
		hasNextPage, strings.Join(nodes, ","))
}

func captureForTest(t *testing.T, r *fakeRunner) (*Manifest, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "fixture")
	at := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	m, err := capture(testFixture(), dir, r.run, at, io.Discard)
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	return m, dir
}

func TestCaptureRecordsRepoStateAndVersion(t *testing.T) {
	r := &fakeRunner{openIssues: 17}
	m, dir := captureForTest(t, r)

	if m.Repo != "lumberbarons/solar-controller" {
		t.Errorf("repo = %q", m.Repo)
	}
	if m.OpenIssues != 17 {
		t.Errorf("openIssues = %d, want 17", m.OpenIssues)
	}
	if m.Truncated {
		t.Error("truncated = true for a single-page repo")
	}
	if m.HewVersion != "hew version 1.2.3" {
		t.Errorf("hewVersion = %q, want the trimmed --version output", m.HewVersion)
	}
	if m.CapturedAt != "2026-07-29T12:00:00Z" {
		t.Errorf("capturedAt = %q", m.CapturedAt)
	}

	// The manifest on disk has to be the one report reads back.
	var onDisk Manifest
	raw, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if onDisk.OpenIssues != m.OpenIssues || len(onDisk.Entries) != len(m.Entries) {
		t.Fatalf("manifest on disk = %+v, want the returned one", onDisk)
	}
}

// A committed fixture records `hew ready`, not the scratch path the binary was
// measured from — but the scratch path is still what runs.
func TestCaptureRecordsCommandNamesNotBuildPaths(t *testing.T) {
	f := testFixture()
	f.Hew = "/tmp/scratch-build/hew"
	r := &fakeRunner{openIssues: 3}
	m, err := capture(f, filepath.Join(t.TempDir(), "fixture"), r.run, time.Now(), io.Discard)
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	for _, e := range m.Entries {
		if got := e.Hew.Argv[0]; got != "hew" {
			t.Errorf("%q recorded argv[0] = %q, want %q", e.Command, got, "hew")
		}
	}
	if r.countOf("/tmp/scratch-build/hew ready") == 0 {
		t.Errorf("ran %v, want the scratch build to be what executed", r.calls)
	}
}

func TestCaptureWritesEveryReferencedFile(t *testing.T) {
	m, dir := captureForTest(t, &fakeRunner{openIssues: 3})
	for _, c := range allCaptures(m.Entries) {
		info, err := os.Stat(filepath.Join(dir, c.File))
		if err != nil {
			t.Errorf("%s: %v", c.File, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("%s is empty", c.File)
		}
	}
}

// The open-issue query is the input to ready, list, list --json, and prime.
// Running it once per entry would be four identical API calls.
func TestCaptureRunsSharedBaselinesOnce(t *testing.T) {
	r := &fakeRunner{openIssues: 3}
	captureForTest(t, r)
	if got := r.countOf("issues-open.graphql"); got != 1 {
		t.Fatalf("open-issue query ran %d times, want 1", got)
	}
	if got := r.countOf("gh issue list"); got != 1 {
		t.Fatalf("gh issue list ran %d times, want 1", got)
	}
}

func TestCaptureFailsOnEmptyOutput(t *testing.T) {
	r := &fakeRunner{openIssues: 3, empty: "hew ready"}
	dir := filepath.Join(t.TempDir(), "fixture")
	_, err := capture(testFixture(), dir, r.run, time.Now(), io.Discard)
	if err == nil {
		t.Fatal("capture accepted a command that produced no output")
	}
	if !strings.Contains(err.Error(), "no output") {
		t.Fatalf("error = %q, want it to name the empty output", err)
	}
}

func TestCaptureFailsOnUnusableOpenIssueResponse(t *testing.T) {
	cases := []struct {
		name string
		r    *fakeRunner
		want string
	}{
		{"unparseable", &fakeRunner{openIssues: 3, broken: true}, "parse open-issue query response"},
		{"no issues", &fakeRunner{openIssues: 0}, "no issues"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "fixture")
			if _, err := capture(testFixture(), dir, tc.r.run, time.Now(), io.Discard); err == nil {
				t.Fatal("capture accepted an unusable open-issue response")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// A repo with more open issues than one page makes both sides undercount, so
// the fixture has to say so rather than publish per-issue figures as exact.
func TestCaptureFlagsTruncatedRepos(t *testing.T) {
	r := &fakeRunner{openIssues: 100, nextPage: true}
	m, _ := captureForTest(t, r)
	if !m.Truncated {
		t.Fatal("truncated = false despite hasNextPage")
	}
}

func TestCaptureRejectsBadFixtureBeforeRunningAnything(t *testing.T) {
	f := testFixture()
	f.Repo = "no-owner"
	r := &fakeRunner{openIssues: 3}
	if _, err := capture(f, filepath.Join(t.TempDir(), "x"), r.run, time.Now(), io.Discard); err == nil {
		t.Fatal("capture accepted an invalid fixture")
	}
	if len(r.calls) != 0 {
		t.Fatalf("ran %v before validating", r.calls)
	}
}

func TestCaptureReportsWhichCommandFailed(t *testing.T) {
	failing := func(argv []string) ([]byte, error) {
		if strings.Contains(strings.Join(argv, " "), "--version") {
			return []byte("hew version 1.2.3"), nil
		}
		return nil, fmt.Errorf("gh: HTTP 403")
	}
	_, err := capture(testFixture(), filepath.Join(t.TempDir(), "x"), failing, time.Now(), io.Discard)
	if err == nil {
		t.Fatal("capture ignored a failing command")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("error = %q, want the underlying failure", err)
	}
}

func TestShellQuoteQuotesArgumentsWithSpaces(t *testing.T) {
	got := shellQuote([]string{"hew", "create", "--title", "two words", "--body", "it's here"})
	want := `hew create --title 'two words' --body 'it'\''s here'`
	if got != want {
		t.Fatalf("shellQuote = %s, want %s", got, want)
	}
}

func TestExecRunnerReturnsStderrOnFailure(t *testing.T) {
	if _, err := execRunner([]string{"sh", "-c", "echo boom >&2; exit 1"}); err == nil {
		t.Fatal("execRunner ignored a nonzero exit")
	} else if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error = %q, want the command's stderr", err)
	}
	out, err := execRunner([]string{"sh", "-c", "printf hi"})
	if err != nil {
		t.Fatalf("execRunner: %v", err)
	}
	if string(out) != "hi" {
		t.Fatalf("stdout = %q, want %q", out, "hi")
	}
}
