package cli

import (
	"errors"
	"fmt"
	"testing"

	"github.com/lumberbarons/hew/internal/gh"
)

func TestExitCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, 0},
		{"plain", errors.New("boom"), ExitGeneric},
		{"usage", usageErr("bad"), ExitUsage},
		{"claimed", &ExitError{Code: ExitClaimed, Message: "claimed"}, ExitClaimed},
		{"claimed by you", &ExitError{Code: ExitClaimedByYou, Message: "yours"}, ExitClaimedByYou},
		{"auth", &gh.AuthError{Err: errors.New("401")}, ExitAuth},
		{"wrapped auth", fmt.Errorf("context: %w", &gh.AuthError{Err: errors.New("401")}), ExitAuth},
		{"wrapped usage", fmt.Errorf("context: %w", usageErr("bad")), ExitUsage},
	}
	for _, tt := range tests {
		if got := ExitCode(tt.err); got != tt.want {
			t.Errorf("%s: ExitCode() = %d, want %d", tt.name, got, tt.want)
		}
	}
}

func TestParseRepoSpec(t *testing.T) {
	got, err := ParseRepoSpec("octocat/hello")
	if err != nil || got.Owner != "octocat" || got.Name != "hello" {
		t.Fatalf("ParseRepoSpec = %+v, %v", got, err)
	}
	for _, bad := range []string{"", "noslash", "/name", "owner/", "owner"} {
		if _, err := ParseRepoSpec(bad); ExitCode(err) != ExitUsage {
			t.Errorf("ParseRepoSpec(%q) exit = %d, want ExitUsage", bad, ExitCode(err))
		}
	}
}

func TestParseIssueNumber(t *testing.T) {
	for _, in := range []string{"42", "#42"} {
		if n, err := ParseIssueNumber(in, "show <n>"); err != nil || n != 42 {
			t.Errorf("ParseIssueNumber(%q) = %d, %v", in, n, err)
		}
	}
	for _, bad := range []string{"", "abc", "0", "-3", "#0"} {
		if _, err := ParseIssueNumber(bad, "show <n>"); ExitCode(err) != ExitUsage {
			t.Errorf("ParseIssueNumber(%q) exit = %d, want ExitUsage", bad, ExitCode(err))
		}
	}
}
