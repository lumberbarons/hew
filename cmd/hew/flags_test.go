package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	ucli "github.com/urfave/cli/v3"

	appcli "github.com/lumberbarons/hew/internal/cli"
)

// visibleFlags is every flag a caller is shown in --help, by command path.
// Hidden flags are the deprecated spellings kept so old command lines keep
// working; they are not part of the vocabulary the tool teaches.
func visibleFlags(root *ucli.Command) map[string][]ucli.Flag {
	out := map[string][]ucli.Flag{}
	var walk func(c *ucli.Command, path string)
	walk = func(c *ucli.Command, path string) {
		for _, f := range c.Flags {
			if vf, ok := f.(ucli.VisibleFlag); ok && !vf.IsVisible() {
				continue
			}
			out[path] = append(out[path], f)
		}
		for _, sub := range c.Commands {
			walk(sub, path+" "+sub.Name)
		}
	}
	walk(root, root.Name)
	return out
}

// TestFlagNamesKeepOneMeaning pins #156: a flag name that two commands both
// teach must mean the same thing on both, or an agent that learned it on one
// carries a wrong model to the other. Shape is what a test can see; the
// pinned names are the ones that used to diverge.
func TestFlagNamesKeepOneMeaning(t *testing.T) {
	byName := map[string]map[string][]string{} // name → shape → commands
	for path, flags := range visibleFlags(root()) {
		for _, f := range flags {
			for _, name := range f.Names() {
				if byName[name] == nil {
					byName[name] = map[string][]string{}
				}
				shape := flagShape(f)
				byName[name][shape] = append(byName[name][shape], path)
			}
		}
	}
	for name, shapes := range byName {
		if len(shapes) > 1 {
			t.Errorf("--%s has several shapes across commands: %v", name, shapes)
		}
	}

	carriers := func(name string) []string {
		var paths []string
		for _, commands := range byName[name] {
			paths = append(paths, commands...)
		}
		slices.Sort(paths)
		return paths
	}
	// --state is an issue state filter, and only that.
	if got, want := carriers("state"), []string{"hew list"}; !slices.Equal(got, want) {
		t.Errorf("--state is taught by %v, want only %v: a resume file is --checkpoint", got, want)
	}
	// --closed is the closed-target override, and only that.
	if got, want := carriers("closed"), []string{"hew block", "hew set", "hew unblock"}; !slices.Equal(got, want) {
		t.Errorf("--closed is taught by %v, want only %v: closed issues on list are --state closed", got, want)
	}
	// One name for the batch writers' resume file.
	if got, want := carriers("checkpoint"), []string{"hew apply", "hew migrate beads", "hew migrate local"}; !slices.Equal(got, want) {
		t.Errorf("--checkpoint is taught by %v, want %v", got, want)
	}
	// Dependencies are named the same way wherever several can be given.
	for _, name := range []string{"on", "from", "blocked-by"} {
		for shape := range byName[name] {
			if !strings.HasSuffix(shape, "repeatable") {
				t.Errorf("--%s has shape %q, want repeatable", name, shape)
			}
		}
	}
}

// Every hidden flag is a deprecated spelling, and says what replaced it, so
// --help never hides a flag without a trail to its replacement.
func TestHiddenFlagsNameTheirReplacement(t *testing.T) {
	var walk func(c *ucli.Command, path string)
	hidden := 0
	walk = func(c *ucli.Command, path string) {
		for _, f := range c.Flags {
			vf, ok := f.(ucli.VisibleFlag)
			if !ok || vf.IsVisible() {
				continue
			}
			hidden++
			if usage := f.(ucli.DocGenerationFlag).GetUsage(); !strings.Contains(usage, "deprecated: use ") {
				t.Errorf("%s --%s is hidden but its usage %q names no replacement", path, f.Names()[0], usage)
			}
		}
		for _, sub := range c.Commands {
			walk(sub, path+" "+sub.Name)
		}
	}
	walk(root(), "hew")
	if hidden == 0 {
		t.Error("no deprecated flags found; the old spellings are meant to stay as hidden flags")
	}
}

// runLeaf runs argv against a fresh tree whose leaf action is replaced by
// probe, and returns what the framework wrote to stderr. The flag wiring's
// Before hooks still run, which is where notes and conflicts belong.
func runLeaf(t *testing.T, path []string, args []string, probe func(*ucli.Command)) (string, error) {
	t.Helper()
	app := root()
	var stderr bytes.Buffer
	app.ErrWriter = &stderr
	findCommand(t, app, path...).Action = func(_ context.Context, cmd *ucli.Command) error {
		if probe != nil {
			probe(cmd)
		}
		return nil
	}
	argv := append(append([]string{"hew", "--repo", "o/r"}, path...), args...)
	err := app.Run(context.Background(), argv)
	return stderr.String(), err
}

func TestListClosedStillWorksAndSaysWhatReplacedIt(t *testing.T) {
	var closed bool
	note, err := runLeaf(t, []string{"list"}, []string{"--closed"}, func(cmd *ucli.Command) { closed = cmd.Bool("closed") })
	if err != nil || !closed {
		t.Fatalf("err = %v, closed = %v; the old spelling must still reach list", err, closed)
	}
	if want := "⚠ --closed is deprecated; use --state closed\n"; note != want {
		t.Errorf("note = %q, want %q", note, want)
	}
	if note, err := runLeaf(t, []string{"list"}, []string{"--state", "closed"}, nil); err != nil || note != "" {
		t.Errorf("--state closed: err = %v, note = %q; want neither", err, note)
	}
	// The override flag on set keeps its name and carries no note.
	if note, err := runLeaf(t, []string{"set"}, []string{"1", "--closed"}, nil); err != nil || note != "" {
		t.Errorf("set --closed: err = %v, note = %q; want neither", err, note)
	}
}

func TestBatchCheckpointFlag(t *testing.T) {
	for _, path := range [][]string{{"apply"}, {"migrate", "beads"}, {"migrate", "local"}} {
		name := strings.Join(path, " ")
		args := func(more ...string) []string {
			if name == "apply" {
				more = append(more, "plan.jsonl")
			}
			return more
		}
		t.Run(name, func(t *testing.T) {
			var got string
			probe := func(cmd *ucli.Command) { got = checkpointPath(cmd) }

			note, err := runLeaf(t, path, args("--checkpoint", "cp.json"), probe)
			if err != nil || got != "cp.json" || note != "" {
				t.Errorf("--checkpoint: err = %v, path = %q, note = %q", err, got, note)
			}

			got = ""
			note, err = runLeaf(t, path, args("--state", "old.json"), probe)
			if err != nil || got != "old.json" {
				t.Errorf("--state: err = %v, path = %q; the old spelling must still name the checkpoint", err, got)
			}
			if want := "⚠ --state is deprecated; use --checkpoint\n"; note != want {
				t.Errorf("--state note = %q, want %q", note, want)
			}

			_, err = runLeaf(t, path, args("--state", "a.json", "--checkpoint", "b.json"), probe)
			if appcli.ExitCode(err) != appcli.ExitUsage || !strings.Contains(fmt.Sprint(err), "--checkpoint") {
				t.Errorf("both spellings: exit = %d, err = %v; want a usage error naming --checkpoint", appcli.ExitCode(err), err)
			}

			got = "unset"
			if _, err = runLeaf(t, path, args(), probe); err != nil || got != "" {
				t.Errorf("neither: err = %v, path = %q; want the empty default", err, got)
			}
		})
	}
}

// apply and migrate beads take their input the same way: positionally or with
// --file, never both.
func TestApplyAndMigrateBeadsAcceptInputTheSameWay(t *testing.T) {
	for _, path := range [][]string{{"apply"}, {"migrate", "beads"}} {
		name := strings.Join(path, " ")
		t.Run(name, func(t *testing.T) {
			var got string
			probe := func(cmd *ucli.Command) { got = inputFile(cmd) }

			for _, args := range [][]string{{"in.jsonl"}, {"--file", "in.jsonl"}} {
				got = ""
				if _, err := runLeaf(t, path, args, probe); err != nil || got != "in.jsonl" {
					t.Errorf("%v: err = %v, input = %q, want in.jsonl", args, err, got)
				}
			}

			_, err := runLeaf(t, path, []string{"a.jsonl", "--file", "b.jsonl"}, probe)
			if appcli.ExitCode(err) != appcli.ExitUsage || !strings.Contains(fmt.Sprint(err), "--file") {
				t.Errorf("both: exit = %d, err = %v; want a usage error naming --file", appcli.ExitCode(err), err)
			}
			_, err = runLeaf(t, path, []string{"--file", ""}, probe)
			if appcli.ExitCode(err) != appcli.ExitUsage {
				t.Errorf("empty --file: exit = %d, err = %v; want usage", appcli.ExitCode(err), err)
			}
			_, err = runLeaf(t, path, []string{"a.jsonl", "b.jsonl"}, probe)
			if appcli.ExitCode(err) != appcli.ExitUsage {
				t.Errorf("two positionals: exit = %d, err = %v; want usage", appcli.ExitCode(err), err)
			}
		})
	}
}

func TestBlockAndUnblockCollectRepeatedIssues(t *testing.T) {
	for _, tc := range []struct{ cmd, flag string }{{"block", "on"}, {"unblock", "from"}} {
		t.Run(tc.cmd, func(t *testing.T) {
			var got []int
			_, err := runLeaf(t, []string{tc.cmd}, []string{"1", "--" + tc.flag, "2", "--" + tc.flag, "3,4"},
				func(cmd *ucli.Command) { got = cmd.IntSlice(tc.flag) })
			if err != nil || !slices.Equal(got, []int{2, 3, 4}) {
				t.Errorf("err = %v, --%s = %v, want [2 3 4]", err, tc.flag, got)
			}
			// Required stays required: a bare block is still a usage error.
			if _, err := runLeaf(t, []string{tc.cmd}, []string{"1"}, nil); appcli.ExitCode(err) != appcli.ExitUsage {
				t.Errorf("without --%s: exit = %d, err = %v; want usage", tc.flag, appcli.ExitCode(err), err)
			}
		})
	}
}

// The tests above replace each command's action, so they prove the flags parse
// but not that the app receives what was typed. These run the real actions: a
// corrupt file at the checkpoint path is rejected by name before any network
// call, so the error is only reachable if the path (and, for the plan or
// snapshot, the input file) actually arrived.
func TestBatchWritersReceiveTheCheckpointAndInputFiles(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	dir := t.TempDir()
	plan := filepath.Join(dir, "plan.jsonl")
	if err := os.WriteFile(plan, []byte(`{"title":"T","type":"task"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(dir, "issues.jsonl")
	bead := `{"_type":"issue","id":"sc-1","title":"Open","status":"open","priority":2,"issue_type":"task","created_at":"2026-05-01T00:00:00Z"}`
	if err := os.WriteFile(snapshot, []byte(bead+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		args []string
		// local runs the command inside a linked worktree of a local tracker.
		local bool
	}{
		{"apply argument", []string{"apply", plan}, false},
		{"apply --file", []string{"apply", "--file", plan}, false},
		{"migrate beads argument", []string{"migrate", "beads", snapshot}, false},
		{"migrate beads --file", []string{"migrate", "beads", "--file", snapshot}, false},
		{"migrate local", []string{"migrate", "local"}, true},
	}
	for _, tc := range cases {
		for _, spelling := range []string{"--checkpoint", "--state"} {
			t.Run(tc.name+" "+spelling, func(t *testing.T) {
				if tc.local {
					_, wt := localRepo(t)
					t.Chdir(wt)
				}
				checkpoint := filepath.Join(t.TempDir(), "cp.json")
				if err := os.WriteFile(checkpoint, []byte("{not json"), 0o644); err != nil {
					t.Fatal(err)
				}
				args := append(slices.Clone(tc.args), "--dry-run", spelling, checkpoint, "--repo", "octo/hello")
				_, err := capture(t, args...)
				if err == nil || !strings.Contains(err.Error(), "not a valid resume-state file") || !strings.Contains(err.Error(), checkpoint) {
					t.Fatalf("err = %v; want the corrupt checkpoint %s rejected by name", err, checkpoint)
				}
			})
		}
	}
}

// With no argument and no --file, migrate beads reads the project's own
// .beads/issues.jsonl, found from any subdirectory. A corrupt checkpoint is
// rejected only after that snapshot was read and parsed, so the error proves
// the default path was resolved to a real file.
func TestMigrateBeadsDefaultsToTheProjectSnapshot(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{".git", ".beads", filepath.Join("src", "pkg")} {
		if err := os.MkdirAll(filepath.Join(project, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bead := `{"_type":"issue","id":"sc-1","title":"Open","status":"open","priority":2,"issue_type":"task","created_at":"2026-05-01T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(project, ".beads", "issues.jsonl"), []byte(bead+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	checkpoint := filepath.Join(t.TempDir(), "cp.json")
	if err := os.WriteFile(checkpoint, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Join(project, "src", "pkg"))

	_, err = capture(t, "migrate", "beads", "--dry-run", "--checkpoint", checkpoint, "--repo", "octo/hello")
	if err == nil || !strings.Contains(err.Error(), "not a valid resume-state file") || !strings.Contains(err.Error(), checkpoint) {
		t.Fatalf("err = %v; want the corrupt checkpoint rejected, which needs the default snapshot to have been read", err)
	}
}
