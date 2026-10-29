package cli

import (
	"fmt"
	"strings"
)

// RepoDetectError turns the failure of detecting the GitHub repository from
// the git remote into one readable line that says what to do next. The cause
// arrives from go-gh as plain text, so the cases are told apart by the words
// it and git use for them; anything unrecognised keeps its message and the
// --repo escape hatch.
//
// It is only reached once the local backend has been ruled out, so a repository
// with no remote is exactly where `hew init --local` is the way forward. Outside
// a git repository it is not: local mode needs a checkout too.
func RepoDetectError(err error) error {
	cause := cleanGitMessage(err.Error())
	switch {
	case strings.Contains(cause, "not a git repository"):
		return fmt.Errorf("cannot detect repository: %s (hew works inside a git checkout; --repo owner/name targets GitHub from anywhere)", cause)
	case strings.Contains(cause, "no git remotes configured"):
		return fmt.Errorf("no git remote to find a GitHub repository from: use --repo owner/name, or `hew init --local` to track issues in this checkout without GitHub")
	default:
		return fmt.Errorf("cannot detect repository (use --repo owner/name): %s", cause)
	}
}

// cleanGitMessage drops the wrapping go-gh puts around a failed git command
// ("failed to run git: <stderr>. error: <exit status>"), leaving git's own
// words on one line.
func cleanGitMessage(msg string) string {
	if i := strings.LastIndex(msg, ". error: "); i >= 0 {
		msg = msg[:i]
	}
	msg = strings.TrimPrefix(strings.TrimSpace(msg), "failed to run git: ")
	return strings.Join(strings.Fields(msg), " ")
}
