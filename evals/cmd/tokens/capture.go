package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// runner executes a captured command and returns its stdout. Injected so the
// capture logic is testable without gh, a network, or a repo.
type runner func(argv []string) ([]byte, error)

func execRunner(argv []string) ([]byte, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s: %s", shellQuote(argv), msg)
	}
	return stdout.Bytes(), nil
}

// capture runs every command in the fixture spec, writes each stdout into dir,
// and writes the manifest that report reads back. Commands shared between
// entries run once: the open-issue query is the input to ready, list, and
// prime alike.
func capture(f Fixture, dir string, run runner, now time.Time, log io.Writer) (*Manifest, error) {
	if err := f.validate(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	version, err := run([]string{f.Hew, "--version"})
	if err != nil {
		return nil, fmt.Errorf("read hew version: %w", err)
	}

	m := &Manifest{
		Repo:       f.Repo,
		CapturedAt: now.UTC().Format(time.RFC3339),
		HewVersion: strings.TrimSpace(string(version)),
		Entries:    f.entries(),
	}

	written := map[string][]byte{}
	for _, c := range allCaptures(m.Entries) {
		if _, done := written[c.File]; done {
			continue
		}
		out, err := run(c.Argv)
		if err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(out)) == 0 {
			return nil, fmt.Errorf("%s: produced no output", shellQuote(c.Argv))
		}
		if err := os.WriteFile(filepath.Join(dir, c.File), out, 0o644); err != nil {
			return nil, err
		}
		written[c.File] = out
		fmt.Fprintf(log, "captured %s (%d bytes)\n", c.File, len(out))
	}

	openIssues, truncated, err := openIssueCount(written["gh-graphql-open-issues.json"])
	if err != nil {
		return nil, err
	}
	m.OpenIssues, m.Truncated = openIssues, truncated
	if truncated {
		fmt.Fprintf(log, "warning: %s has more open issues than one page; per-issue figures are floors\n", f.Repo)
	}

	publishArgv(m.Entries)
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return m, os.WriteFile(filepath.Join(dir, manifestName), append(body, '\n'), 0o644)
}

// publishArgv reduces each command's leading path to its name, after the
// commands have run. --hew usually points at a scratch build, and a fixture
// committed to the repo should record `hew ready`, not the absolute path it
// happened to be measured from. HewVersion is what identifies the binary.
func publishArgv(entries []Entry) {
	normalize := func(c *Capture) {
		if len(c.Argv) > 0 {
			c.Argv[0] = filepath.Base(c.Argv[0])
		}
	}
	for i := range entries {
		normalize(&entries[i].Hew)
		for j := range entries[i].Baselines {
			for k := range entries[i].Baselines[j].Captures {
				normalize(&entries[i].Baselines[j].Captures[k])
			}
		}
	}
}

// allCaptures flattens every command the spec asks for, hew side and baseline
// side, in capture order.
func allCaptures(entries []Entry) []Capture {
	var out []Capture
	for _, e := range entries {
		out = append(out, e.Hew)
		for _, b := range e.Baselines {
			out = append(out, b.Captures...)
		}
	}
	return out
}

// openIssueCount reads the denominator for per-issue figures out of the
// open-issue query response, and reports whether the page was full — a
// truncated fixture undercounts both sides.
func openIssueCount(raw []byte) (count int, truncated bool, err error) {
	if len(raw) == 0 {
		return 0, false, fmt.Errorf("no open-issue query response captured")
	}
	var resp struct {
		Data struct {
			Repository struct {
				Issues struct {
					PageInfo struct {
						HasNextPage bool `json:"hasNextPage"`
					} `json:"pageInfo"`
					Nodes []struct {
						Number int `json:"number"`
					} `json:"nodes"`
				} `json:"issues"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return 0, false, fmt.Errorf("parse open-issue query response: %w", err)
	}
	issues := resp.Data.Repository.Issues
	if len(issues.Nodes) == 0 {
		return 0, false, fmt.Errorf("open-issue query returned no issues; nothing to measure")
	}
	return len(issues.Nodes), issues.PageInfo.HasNextPage, nil
}

// shellQuote renders an argv the way a reader would paste it back into a
// shell.
func shellQuote(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		if strings.ContainsAny(a, " \t\"'") {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		parts[i] = a
	}
	return strings.Join(parts, " ")
}
