package cli

import (
	"errors"
	"fmt"
	"strings"
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

func TestValidateArgCount(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		min  int
		max  int
		want int
	}{
		{"no args", nil, 0, 0, 0},
		{"unexpected arg", []string{"extra"}, 0, 0, ExitUsage},
		{"required arg missing", nil, 1, 1, ExitUsage},
		{"required arg present", []string{"12"}, 1, 1, 0},
		{"surplus arg", []string{"12", "13"}, 1, 1, ExitUsage},
		{"optional absent", nil, 0, 1, 0},
		{"optional present", []string{"12"}, 0, 1, 0},
		{"optional surplus", []string{"12", "13"}, 0, 1, ExitUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateArgCount(tc.args, tc.min, tc.max, "hew command <n>")
			if ExitCode(err) != tc.want {
				t.Fatalf("exit = %d, error = %v; want %d", ExitCode(err), err, tc.want)
			}
			if err != nil && err.Error() != "usage: hew command <n>" {
				t.Errorf("error = %v, want command usage", err)
			}
		})
	}
}

func TestValidateIssueNumbers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		numbers []int
		want    int
	}{
		{"positive", []int{1}, 0},
		{"positive list", []int{1, 12, 42}, 0},
		{"zero", []int{0}, ExitUsage},
		{"negative", []int{-5}, ExitUsage},
		{"mixed zero", []int{12, 0}, ExitUsage},
		{"mixed negative", []int{-5, 12}, ExitUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateIssueNumbers("children", tc.numbers...)
			if ExitCode(err) != tc.want {
				t.Fatalf("exit = %d, error = %v; want %d", ExitCode(err), err, tc.want)
			}
			if err != nil && !strings.Contains(err.Error(), "--children") {
				t.Errorf("error = %v, want flag name", err)
			}
		})
	}
}
