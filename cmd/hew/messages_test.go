package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	ucli "github.com/urfave/cli/v3"
)

// isolatedDir is an empty directory git cannot walk out of, so a checkout that
// happens to enclose the temp directory cannot answer for it.
func isolatedDir(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GH_REPO", "")
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	t.Chdir(dir)
	return dir
}

func TestNoRemoteAndNoTrackerPointsAtInitLocal(t *testing.T) {
	dir := isolatedDir(t)
	runGit(t, dir, "init", "--initial-branch=main")
	for _, cmd := range []string{"ready", "prime"} {
		_, err := capture(t, cmd)
		if err == nil || !strings.Contains(err.Error(), "hew init --local") {
			t.Errorf("%s: err = %v, want it to mention `hew init --local`", cmd, err)
		}
	}
}

func TestOutsideGitEveryCommandFailsWithOneReadableError(t *testing.T) {
	isolatedDir(t)
	for _, args := range [][]string{{"ready"}, {"prime"}, {"list"}, {"show", "1"}, {"init"}, {"init", "--local"}} {
		_, err := capture(t, args...)
		if err == nil {
			t.Errorf("%v: expected an error outside a git repository", args)
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, "not a git repository") {
			t.Errorf("%v: err = %q, want git's own message", args, msg)
		}
		if strings.Contains(msg, "exit status") || strings.Contains(msg, ". error:") || strings.Contains(msg, "\n") {
			t.Errorf("%v: err = %q, want one line without the stray exit-status suffix", args, msg)
		}
	}
}

func TestTopLevelHelpIsNotGitHubOnly(t *testing.T) {
	usage := root().Usage
	if strings.Contains(usage, "for GitHub Issues") {
		t.Errorf("usage = %q, describes hew as GitHub-only", usage)
	}
	if !strings.Contains(usage, "local") || !strings.Contains(usage, "GitHub") {
		t.Errorf("usage = %q, want both backends named", usage)
	}
}

func TestInitHelpStatesTheGitRequirementForLocalMode(t *testing.T) {
	init := findCommand(t, root(), "init")
	text := init.Description + "\n" + init.Usage
	for _, f := range init.Flags {
		if f.Names()[0] == "local" {
			text += "\n" + f.(*ucli.BoolFlag).Usage
		}
	}
	if !strings.Contains(text, "git") {
		t.Errorf("init help %q does not state that local mode needs git", text)
	}
}
