package cli

import (
	"errors"
	"strconv"
	"strings"

	"github.com/lumberbarons/hew/internal/gh"
)

// ExitCode maps an error to the process exit code agent loops branch on: an
// ExitError's own code, ExitAuth for an auth failure (so "run gh auth login"
// is distinguishable), else ExitGeneric. nil maps to 0. The mapping lives
// here, not in main, so it is covered by tests.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *ExitError
	var authErr *gh.AuthError
	switch {
	case errors.As(err, &exitErr):
		return exitErr.Code
	case errors.As(err, &authErr):
		return ExitAuth
	}
	return ExitGeneric
}

// ParseRepoSpec parses an "owner/name" repository spec, returning an
// ExitUsage error when it is malformed.
func ParseRepoSpec(spec string) (gh.Repo, error) {
	owner, name, ok := strings.Cut(spec, "/")
	if !ok || owner == "" || name == "" {
		return gh.Repo{}, usageErr("--repo must be owner/name")
	}
	return gh.Repo{Owner: owner, Name: name}, nil
}

// ParseIssueNumber parses a required positional issue number, tolerating a
// leading '#'. It returns an ExitUsage error (with the given usage string)
// when the argument is missing or not a positive integer.
func ParseIssueNumber(arg, usage string) (int, error) {
	if arg == "" {
		return 0, usageErr("usage: %s", usage)
	}
	n, err := strconv.Atoi(strings.TrimPrefix(arg, "#"))
	if err != nil || n <= 0 {
		return 0, usageErr("invalid issue number %q", arg)
	}
	return n, nil
}
