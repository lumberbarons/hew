package main

import (
	"strings"
	"testing"
)

// mustRun is capture for a command that must succeed.
func mustRun(t *testing.T, args ...string) string {
	t.Helper()
	out, err := capture(t, args...)
	if err != nil {
		t.Fatalf("hew %s: %v", strings.Join(args, " "), err)
	}
	return out
}

func lineCount(out string) int {
	if strings.TrimSpace(out) == "" {
		return 0
	}
	return len(strings.Split(strings.TrimSpace(out), "\n"))
}

// The flag wiring is the one thing the adapter tests cannot see: they call
// the App methods directly, so a flag read into the wrong option, or left out
// of the options entirely, would pass all of them.
func TestReadFlagsReachTheirCommandsOnALocalTracker(t *testing.T) {
	main, _ := localRepo(t) // seeds #1 "Shared across worktrees"
	t.Chdir(main)
	mustRun(t, "create", "--type", "task", "--title", "Widget open", "--goal", "g", "--done-when", "d")
	mustRun(t, "create", "--type", "bug", "--title", "Widget fixed", "--goal", "g", "--done-when", "d")
	mustRun(t, "close", "3", "--reason", "done", "--completed")
	mustRun(t, "epic", "create", "--title", "Open epic", "--goal", "g", "--done-when", "d")
	mustRun(t, "epic", "create", "--title", "Finished epic", "--goal", "g", "--done-when", "d")
	mustRun(t, "close", "5", "--reason", "done", "--completed")

	// list: every open issue by default, --limit caps it.
	if got := lineCount(mustRun(t, "list")); got != 3 {
		t.Errorf("list printed %d lines, want all 3 open issues", got)
	}
	if got := lineCount(mustRun(t, "list", "--limit", "1")); got != 1 {
		t.Errorf("list --limit 1 printed %d lines, want 1", got)
	}

	// search: both states by default, --state narrows, --limit caps.
	if out := mustRun(t, "search", "widget"); !strings.Contains(out, "Widget open") || !strings.Contains(out, "Widget fixed") {
		t.Errorf("search spans both states by default:\n%s", out)
	}
	out := mustRun(t, "search", "widget", "--state", "closed")
	if strings.Contains(out, "Widget open") || !strings.Contains(out, "Widget fixed") {
		t.Errorf("search --state closed:\n%s", out)
	}
	if got := lineCount(mustRun(t, "search", "widget", "--limit", "1")); got != 1 {
		t.Errorf("search --limit 1 printed %d lines, want 1", got)
	}

	// epic status: open epics by default, --state selects.
	if out := mustRun(t, "epic", "status"); !strings.Contains(out, "Open epic") || strings.Contains(out, "Finished epic") {
		t.Errorf("epic status default is open epics:\n%s", out)
	}
	out = mustRun(t, "epic", "status", "--state", "closed")
	if strings.Contains(out, "Open epic") || !strings.Contains(out, "Finished epic") || !strings.Contains(out, "[closed]") {
		t.Errorf("epic status --state closed:\n%s", out)
	}
	if out := mustRun(t, "epic", "status", "--state", "all"); !strings.Contains(out, "Open epic") || !strings.Contains(out, "Finished epic") {
		t.Errorf("epic status --state all:\n%s", out)
	}
}
