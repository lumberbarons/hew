package tracker_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/tracker"
)

// TestAnExternalModuleDrivesQueriesAndPrimerFromPublicTypes is the read-side
// consumer contract: target metadata, snapshot list filtering, detail, search,
// triage and primer state are all obtainable without an internal import.
func TestAnExternalModuleDrivesQueriesAndPrimerFromPublicTypes(t *testing.T) {
	root, tr := externalTrackerCheckout(t)
	ctx := context.Background()

	applied, err := tr.ApplyPlan(ctx, []tracker.PlanEntry{
		{ID: "ready", Title: "Parser \u0430rea", Type: "bug", Priority: "P1", Areas: []string{"parser"}, Body: "the parser body"},
		{ID: "claimed", Title: "Claimed work", Type: "task", Priority: "P2"},
		{ID: "epic", Title: "Container", Type: "epic", Priority: "P2"},
		{ID: "child", Title: "Child work", Type: "task", Priority: "P2", Parent: &tracker.PlanRef{ID: "epic"}},
	}, tracker.ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ready := applied.Mapping["ready"]
	if _, err := tr.Start(ctx, applied.Mapping["claimed"], tracker.StartOptions{}); err != nil {
		t.Fatal(err)
	}

	target, err := tracker.Detect(root)
	if err != nil {
		t.Fatal(err)
	}
	if target.Backend != tracker.BackendLocal || target.Dir != filepath.Join(root, ".hew") || target.MainCheckout != root {
		t.Errorf("Target = %+v, want the local tracker at %s", target, filepath.Join(root, ".hew"))
	}
	if target.ID != "external-consumer" {
		t.Errorf("Target.ID = %q, want the tracker's stable id", target.ID)
	}
	if target.Repo != "" {
		t.Errorf("Target.Repo = %q, want empty on a local tracker", target.Repo)
	}

	primer, err := tr.Primer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(primer.Conventions, "Workflow: hew ready") || !strings.Contains(primer.Conventions, "hew close <n> --completed") {
		t.Errorf("Primer.Conventions = %q, want the local variant", primer.Conventions)
	}
	if primer.OpenTotal != 4 || primer.ReadyTotal != 2 {
		t.Errorf("primer totals = open %d ready %d, want 4/2", primer.OpenTotal, primer.ReadyTotal)
	}

	snapshot, err := tr.Snapshot(ctx, tracker.SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	listed := snapshot.List(tracker.ListFilter{Label: "PARSER"})
	if len(listed) != 1 || listed[0].Number != ready || listed[0].Title != "Parser \u0430rea" {
		t.Fatalf("List = %+v, want only #%d", listed, ready)
	}

	detail, err := tr.Detail(ctx, ready)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Issue.Body == nil || !strings.Contains(*detail.Issue.Body, "the parser body") {
		t.Errorf("Detail body = %v", detail.Issue.Body)
	}
	var confusable bool
	for _, finding := range detail.TextFindings {
		if finding.Source == tracker.TextFindingTitle && finding.Confusable {
			confusable = true
		}
	}
	if !confusable {
		t.Errorf("Detail.TextFindings = %+v, want the title's confusable finding", detail.TextFindings)
	}

	search, err := tr.Search(ctx, tracker.SearchRequest{Terms: "parser"})
	if err != nil {
		t.Fatal(err)
	}
	if len(search.Issues) != 1 || search.Issues[0].Number != ready || search.Total != 1 || search.Fetched != 1 {
		t.Errorf("Search = %+v, want #%d with complete counts", search, ready)
	}

	triage, err := tr.Triage(ctx, tracker.TriageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(triage.Issues) != 0 || triage.Total != 0 {
		t.Errorf("Triage = %+v, want no untriaged issues", triage)
	}
}

// A GitHub checkout's target report names the repository from the origin
// remote, still without an internal import or a credential.
func TestAnExternalModuleReadsGitHubTargetMetadata(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "GIT_") {
			t.Setenv(k, os.Getenv(k))
			if err := os.Unsetenv(k); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "--initial-branch=main")
	run("remote", "add", "origin", "git@github.com:acme/hew.git")

	target, err := tracker.Detect(root)
	if err != nil {
		t.Fatal(err)
	}
	if target.Backend != tracker.BackendGitHub || target.Repo != "acme/hew" || target.MainCheckout != root {
		t.Errorf("Target = %+v, want GitHub acme/hew in %s", target, root)
	}
	if target.Dir != "" || target.ID != "" {
		t.Errorf("Target = %+v, want no local path or id on GitHub", target)
	}
}
