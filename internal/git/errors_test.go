package git

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestMainCheckoutErrorCarriesGitsStderr(t *testing.T) {
	failing := func(string, ...string) (string, error) {
		return "", &exec.ExitError{Stderr: []byte("fatal: not a git repository (or any of the parent directories): .git\n")}
	}
	_, err := mainCheckout(failing, "")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "fatal: not a git repository") {
		t.Errorf("err = %q, want git's stderr", err)
	}
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("err = %q, want a single line", err)
	}
	if !IsNotRepo(err) {
		t.Errorf("IsNotRepo(%q) = false; the wrapper must keep the exit error reachable", err)
	}
}

func TestMainCheckoutErrorWithoutStderrKeepsTheCause(t *testing.T) {
	failing := func(string, ...string) (string, error) { return "", errors.New("boom") }
	if _, err := mainCheckout(failing, ""); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v, want the underlying cause", err)
	}
}

// git states the cause first and follows it with remedy lines; keeping the
// last line would report the remedy as if it were the failure.
func TestDescribeFailureKeepsTheCauseNotTheRemedy(t *testing.T) {
	stderr := "fatal: detected dubious ownership in repository at '/x'\n" +
		"To add an exception for this directory, call:\n\n" +
		"\tgit config --global --add safe.directory /x\n"
	err := describeFailure(&exec.ExitError{Stderr: []byte(stderr)})
	if got, want := err.Error(), "fatal: detected dubious ownership in repository at '/x'"; got != want {
		t.Errorf("message = %q, want %q", got, want)
	}
	plain := describeFailure(&exec.ExitError{Stderr: []byte("\nsomething odd\nmore\n")})
	if plain.Error() != "something odd" {
		t.Errorf("message = %q, want the first line when there is no fatal: line", plain)
	}
	bare := &exec.ExitError{}
	if describeFailure(bare) != error(bare) {
		t.Error("an exit error with no stderr must come back unchanged")
	}
}

func TestLinkedWorktreeErrorCarriesGitsStderr(t *testing.T) {
	failing := func(string, ...string) (string, error) {
		return "", &exec.ExitError{Stderr: []byte("fatal: not a git repository (or any of the parent directories): .git\n")}
	}
	_, err := linkedWorktree(failing, "")
	if err == nil || !strings.Contains(err.Error(), "fatal: not a git repository") || strings.Contains(err.Error(), "exit status") {
		t.Errorf("err = %v, want git's message and no bare exit status", err)
	}
	if !IsNotRepo(err) {
		t.Errorf("IsNotRepo(%v) = false", err)
	}
}
