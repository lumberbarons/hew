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
