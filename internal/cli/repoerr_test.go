package cli

import (
	"errors"
	"strings"
	"testing"
)

// go-gh builds its git failures as "failed to run git: <stderr>. error: <err>",
// so the real text carries stderr's trailing newline and a stray suffix.
const (
	goGHNotARepo  = "failed to run git: fatal: not a git repository (or any of the parent directories): .git\n. error: exit status 128"
	goGHNoRemotes = "unable to determine current repository, no git remotes configured for this repository"
)

func TestRepoDetectErrorWithoutRemoteOffersLocalMode(t *testing.T) {
	err := RepoDetectError(errors.New(goGHNoRemotes))
	for _, want := range []string{"hew init --local", "--repo owner/name"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to mention %q", err, want)
		}
	}
}

func TestRepoDetectErrorOutsideGitIsOneCleanLine(t *testing.T) {
	err := RepoDetectError(errors.New(goGHNotARepo))
	msg := err.Error()
	if strings.Contains(msg, "\n") || strings.Contains(msg, "exit status") || strings.Contains(msg, ". error:") {
		t.Errorf("err = %q, want one line without go-gh's wrapper noise", msg)
	}
	if !strings.Contains(msg, "not a git repository") {
		t.Errorf("err = %q, want git's own message kept", msg)
	}
	// Local mode needs a checkout too, so pointing at it here would send the
	// user down a path that fails the same way.
	if strings.Contains(msg, "init --local") {
		t.Errorf("err = %q, must not offer local mode outside a git repository", msg)
	}
}

func TestRepoDetectErrorKeepsOtherFailuresReadable(t *testing.T) {
	err := RepoDetectError(errors.New("could not find git executable in PATH. error: exec: \"git\": executable file not found in $PATH"))
	msg := err.Error()
	if !strings.Contains(msg, "could not find git executable in PATH") || !strings.Contains(msg, "--repo owner/name") {
		t.Errorf("err = %q, want the cause and the --repo escape hatch", msg)
	}
	if strings.Contains(msg, "PATH. error:") {
		t.Errorf("err = %q, want the wrapper suffix dropped", msg)
	}
}
