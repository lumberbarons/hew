package main

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRunRequiresAKnownSubcommand(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no arguments", nil, "no subcommand"},
		{"unknown", []string{"measure"}, `unknown subcommand "measure"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			err := run(tc.args, io.Discard, &stderr)
			if err == nil {
				t.Fatal("run accepted an unusable invocation")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err, tc.want)
			}
			if !strings.Contains(stderr.String(), "usage:") {
				t.Errorf("stderr = %q, want the usage text", stderr.String())
			}
		})
	}
}

func TestRunHelpGoesToStdout(t *testing.T) {
	var stdout bytes.Buffer
	if err := run([]string{"--help"}, &stdout, io.Discard); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(stdout.String(), "tokens capture") {
		t.Errorf("help = %q, want the capture usage line", stdout.String())
	}
}

func TestCaptureRequiresAnOutputDirectory(t *testing.T) {
	err := run([]string{"capture", "--repo", "a/b"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--out") {
		t.Fatalf("error = %v, want it to require --out", err)
	}
}

func TestReportRequiresAFixtureDirectory(t *testing.T) {
	err := run([]string{"report"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "fixture directory") {
		t.Fatalf("error = %v, want it to require a fixture", err)
	}
}

// The committed fixtures are the published numbers; a report over them has to
// keep working as the harness changes.
func TestReportOverCommittedFixtures(t *testing.T) {
	var stdout bytes.Buffer
	if err := run([]string{"report", "--format", "markdown", solarFixture}, &stdout, io.Discard); err != nil {
		t.Fatalf("report: %v", err)
	}
	out := stdout.String()
	for _, want := range []string{"| `hew ready` |", "lumberbarons/solar-controller", "same-information ratios:"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
}

const (
	solarFixture = "../../fixtures/solar-controller"
	hewFixture   = "../../fixtures/hew"
)

func TestReportOverSeveralFixturesEmitsOneDocumentEach(t *testing.T) {
	var stdout bytes.Buffer
	if err := run([]string{"report", "--format", "json", solarFixture, hewFixture}, &stdout, io.Discard); err != nil {
		t.Fatalf("report: %v", err)
	}
	dec := json.NewDecoder(&stdout)
	var repos []string
	for dec.More() {
		var got struct {
			Repo string `json:"repo"`
			Rows []struct {
				Command string `json:"command"`
			} `json:"rows"`
		}
		if err := dec.Decode(&got); err != nil {
			t.Fatalf("decode report: %v", err)
		}
		if len(got.Rows) == 0 {
			t.Errorf("%s reported no rows", got.Repo)
		}
		repos = append(repos, got.Repo)
	}
	want := []string{"lumberbarons/solar-controller", "lumberbarons/hew"}
	if !slices.Equal(repos, want) {
		t.Fatalf("repos = %v, want %v", repos, want)
	}
}

func TestReportRejectsAMissingFixture(t *testing.T) {
	err := run([]string{"report", filepath.Join(t.TempDir(), "absent")}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "read fixture manifest") {
		t.Fatalf("error = %v, want it to name the missing manifest", err)
	}
}

func TestUnknownFlagsAreRejected(t *testing.T) {
	for _, args := range [][]string{
		{"capture", "--nope"},
		{"report", "--nope"},
	} {
		if err := run(args, io.Discard, io.Discard); err == nil {
			t.Errorf("run(%v) accepted an unknown flag", args)
		}
	}
}
