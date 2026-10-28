package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	ucli "github.com/urfave/cli/v3"

	appcli "github.com/lumberbarons/hew/internal/cli"
)

func TestFixedArgumentsRejectSurplus(t *testing.T) {
	for _, args := range [][]string{
		{"prime", "extra"}, {"ready", "extra"}, {"list", "extra"},
		{"show", "12", "13"}, {"create", "extra"}, {"draft", "extra"},
		{"start", "12", "13"}, {"triage", "extra"}, {"set", "12", "13"},
		{"pr", "extra"}, {"close", "12", "13"}, {"reopen", "12", "13"},
		{"block", "12", "13", "--on", "14"},
		{"unblock", "12", "13", "--from", "14"},
		{"epic", "create", "extra"}, {"epic", "status", "12", "13"},
		{"epic", "list", "12", "13"},
		{"apply", "one.jsonl", "two.jsonl"}, {"init", "extra"},
		{"hooks", "install", "claude", "extra"},
		{"hooks", "remove", "claude", "extra"},
		{"migrate", "beads", "extra"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			// A malformed repo is a tripwire: reaching buildApp is already
			// too late, and this prevents an unfixed command calling GitHub.
			argv := append([]string{"hew", "--repo", "invalid"}, args...)
			err := root().Run(context.Background(), argv)
			if appcli.ExitCode(err) != appcli.ExitUsage || !strings.Contains(fmt.Sprint(err), "usage: hew "+args[0]) {
				t.Fatalf("exit = %d, error = %v; want argument usage error before app construction", appcli.ExitCode(err), err)
			}
		})
	}
}

func TestFixedArgumentsRequirePositionals(t *testing.T) {
	for _, path := range [][]string{
		{"show"}, {"start"}, {"set"}, {"close"}, {"reopen"},
		{"block", "--on", "12"}, {"unblock", "--from", "12"}, {"apply"},
		{"hooks", "install"}, {"hooks", "remove"},
	} {
		t.Run(strings.Join(path, " "), func(t *testing.T) {
			err := root().Run(context.Background(), append([]string{"hew", "--repo", "invalid"}, path...))
			if appcli.ExitCode(err) != appcli.ExitUsage || !strings.Contains(fmt.Sprint(err), "usage: hew "+path[0]) {
				t.Fatalf("exit = %d, error = %v; want missing-argument usage error", appcli.ExitCode(err), err)
			}
		})
	}
}

func TestFixedArgumentsAcceptValidInput(t *testing.T) {
	for _, tc := range []struct {
		path []string
		args []string
	}{
		{[]string{"prime"}, nil}, {[]string{"ready"}, []string{"--limit", "0"}},
		{[]string{"list"}, nil}, {[]string{"show"}, []string{"#12"}},
		{[]string{"search"}, []string{"several", "search", "terms"}},
		{[]string{"create"}, nil}, {[]string{"draft"}, nil},
		{[]string{"start"}, []string{"12"}}, {[]string{"triage"}, nil},
		{[]string{"set"}, []string{"12"}}, {[]string{"pr"}, nil},
		{[]string{"close"}, []string{"12"}}, {[]string{"reopen"}, []string{"12"}},
		{[]string{"block"}, []string{"12", "--on", "13"}},
		{[]string{"unblock"}, []string{"12", "--from", "13"}},
		{[]string{"epic", "create"}, nil},
		{[]string{"epic", "status"}, nil}, {[]string{"epic", "status"}, []string{"12"}},
		{[]string{"apply"}, []string{"plan.jsonl"}}, {[]string{"init"}, nil},
		{[]string{"hooks", "install"}, []string{"claude"}},
		{[]string{"hooks", "remove"}, []string{"codex"}},
		{[]string{"migrate", "beads"}, nil},
	} {
		t.Run(strings.Join(append(append([]string{}, tc.path...), tc.args...), " "), func(t *testing.T) {
			cmd := root()
			invoked := false
			findCommand(t, cmd, tc.path...).Action = func(context.Context, *ucli.Command) error {
				invoked = true
				return nil
			}
			argv := append(append([]string{"hew"}, tc.path...), tc.args...)
			if err := cmd.Run(context.Background(), argv); err != nil || !invoked {
				t.Fatalf("error = %v, invoked = %v; want action reached", err, invoked)
			}
		})
	}
}

func TestIssueFlagsRejectNonPositive(t *testing.T) {
	cases := []struct {
		args []string
		flag string
	}{
		{[]string{"create"}, "parent"}, {[]string{"set", "12"}, "parent"},
		{[]string{"list"}, "epic"}, {[]string{"pr"}, "for"},
		{[]string{"pr", "--amend"}, "for"},
		{[]string{"create"}, "blocked-by"}, {[]string{"epic", "create"}, "children"},
		{[]string{"close", "12"}, "duplicate-of"},
		{[]string{"create"}, "discovered-from"},
		{[]string{"block", "12"}, "on"}, {[]string{"unblock", "12"}, "from"},
	}
	for _, tc := range cases {
		values := []string{"0", "-5"}
		if tc.flag == "blocked-by" || tc.flag == "children" || tc.flag == "for" {
			values = append(values, "12,0", "12,-5", "0,12", "-5,12")
		}
		for _, value := range values {
			t.Run(strings.Join(tc.args, " ")+"/"+tc.flag+"="+value, func(t *testing.T) {
				argv := append([]string{"hew", "--repo", "invalid"}, tc.args...)
				argv = append(argv, "--"+tc.flag+"="+value)
				err := root().Run(context.Background(), argv)
				message := fmt.Sprint(err)
				if appcli.ExitCode(err) != appcli.ExitUsage || !strings.Contains(message, "--"+tc.flag) || !strings.Contains(message, "positive") {
					t.Fatalf("exit = %d, error = %v; want positive --%s usage error", appcli.ExitCode(err), err, tc.flag)
				}
			})
		}
	}
}

func TestIssueFlagsAcceptPositive(t *testing.T) {
	for _, tc := range []struct {
		path []string
		args []string
	}{
		{[]string{"create"}, []string{"--parent", "1", "--discovered-from", "2", "--blocked-by", "3,4", "--blocked-by", "5"}},
		{[]string{"set"}, []string{"12", "--parent", "1"}},
		{[]string{"list"}, []string{"--epic", "1"}},
		{[]string{"pr"}, []string{"--for", "1", "--for", "2"}},
		{[]string{"close"}, []string{"12", "--duplicate-of", "1"}},
		{[]string{"epic", "create"}, []string{"--children", "1,2", "--children", "3"}},
	} {
		t.Run(strings.Join(tc.path, " "), func(t *testing.T) {
			cmd := root()
			invoked := false
			findCommand(t, cmd, tc.path...).Action = func(context.Context, *ucli.Command) error {
				invoked = true
				return nil
			}
			argv := append(append([]string{"hew"}, tc.path...), tc.args...)
			if err := cmd.Run(context.Background(), argv); err != nil || !invoked {
				t.Fatalf("error = %v, invoked = %v; want positive flags accepted", err, invoked)
			}
		})
	}
}
