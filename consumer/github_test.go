// This file drives the GitHub-backed tracker workflows from outside the hew
// module: a consumer opens a tracker over an isolated HTTP transport, files
// and edits work, claims and releases it, writes dependencies, applies a plan
// and reads the query surface. Nothing here executes the hew binary, reads a
// gh credential or reaches the network.
package consumer_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/tracker"
)

// openGitHubTracker opens a tracker over the fake transport. The worktree is
// a throwaway directory: an explicit repository and backend need no detection,
// which is the same path the CLI's --repo takes.
func openGitHubTracker(t *testing.T, fake *githubFake) *tracker.Tracker {
	t.Helper()
	transport := openFake(t, fake)
	tr, err := tracker.Open(context.Background(), tracker.Config{
		Worktree:  t.TempDir(),
		Backend:   tracker.BackendGitHub,
		Repo:      "o/r",
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return tr
}

// seed adds an issue the API already has, without going through a workflow.
func seed(fake *githubFake, number int, title, body string, labels ...string) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.issues[number] = &fakeIssue{number: number, title: title, body: body, state: "OPEN", labels: labels}
}

// TestExternalConsumerInitializesGitHubConventions is done-when's
// initialization and config-metadata scenario on the GitHub backend: the
// public Init bootstraps the convention labels, and Open reports the resolved
// backend and identity a caller records.
func TestExternalConsumerInitializesGitHubConventions(t *testing.T) {
	fake := newGitHubFake()
	transport := openFake(t, fake)
	ctx := context.Background()

	init, err := tracker.Init(ctx, tracker.InitConfig{
		Worktree: t.TempDir(), Backend: tracker.BackendGitHub, Repo: "o/r", Transport: transport,
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if init.Backend != tracker.BackendGitHub || init.Path != "" {
		t.Errorf("Init = %+v, want the GitHub backend and no local path", init)
	}
	if !slices.Contains(init.CreatedLabels, "P0") || !slices.Contains(init.CreatedLabels, "in-progress") {
		t.Errorf("CreatedLabels = %v, want the convention labels", init.CreatedLabels)
	}

	// A rerun creates nothing: the bootstrap is idempotent from outside too.
	again, err := tracker.Init(ctx, tracker.InitConfig{
		Worktree: t.TempDir(), Backend: tracker.BackendGitHub, Repo: "o/r", Transport: transport,
	})
	if err != nil {
		t.Fatalf("second Init: %v", err)
	}
	if len(again.CreatedLabels) != 0 {
		t.Errorf("a rerun created %v", again.CreatedLabels)
	}

	tr, err := tracker.Open(ctx, tracker.Config{Worktree: t.TempDir(), Backend: tracker.BackendGitHub, Repo: "o/r", Transport: transport})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if tr.Backend() != tracker.BackendGitHub || tr.Identity() != "octocat" {
		t.Errorf("tracker = {%q, %q}, want GitHub as the authenticated login", tr.Backend(), tr.Identity())
	}
}

// TestExternalConsumerDrivesGitHubWorkflows is the scenario list on the GitHub
// backend: creation and epics, the query surface, claims, edits, closure,
// dependencies and a checkpointed plan, all through public requests and
// results.
func TestExternalConsumerDrivesGitHubWorkflows(t *testing.T) {
	fake := newGitHubFake()
	tr := openGitHubTracker(t, fake)
	ctx := context.Background()

	blocker, err := tr.Create(ctx, tracker.CreateRequest{Title: "Blocker", Type: "bug", Priority: "P1"})
	if err != nil {
		t.Fatalf("Create blocker: %v", err)
	}
	epic, err := tr.CreateEpic(ctx, tracker.CreateEpicRequest{Title: "Big feature"})
	if err != nil {
		t.Fatalf("CreateEpic: %v", err)
	}
	if epic.Issue.Title != "Epic: Big feature" || !epic.Issue.HasLabel("epic") {
		t.Errorf("epic = %+v, want the prefix and the epic marker", epic.Issue)
	}

	work, err := tr.Create(ctx, tracker.CreateRequest{
		Title:    "Parser work",
		Type:     "task",
		Priority: "P2",
		Areas:    []string{"parser"},
		Body: tracker.Body{Sections: tracker.Sections{
			Goal:     "parse the body",
			DoneWhen: []string{"the parser runs"},
		}},
		Parent:    epic.Issue.Number,
		BlockedBy: []int{blocker.Issue.Number},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	wantEdges := []tracker.CreatedEdge{
		{Kind: tracker.BlockedByEdge, To: blocker.Issue.Number},
		{Kind: tracker.ParentEdge, To: epic.Issue.Number},
	}
	if !slices.Equal(work.Edges, wantEdges) {
		t.Errorf("Edges = %v, want %v", work.Edges, wantEdges)
	}

	read, err := tr.Get(ctx, work.Issue.Number)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if read.Priority == nil || *read.Priority != "P2" || read.Type == nil || *read.Type != "task" {
		t.Errorf("labels = %v/%v, want P2/task", read.Priority, read.Type)
	}
	if !slices.Equal(read.Areas, []string{"parser"}) {
		t.Errorf("Areas = %v", read.Areas)
	}
	if read.Body == nil || !strings.Contains(*read.Body, "### Done when") {
		t.Errorf("Body = %v, want the composed template", read.Body)
	}
	if !slices.Equal(read.BlockedBy, []int{blocker.Issue.Number}) {
		t.Errorf("BlockedBy = %v", read.BlockedBy)
	}
	if read.Parent == nil || *read.Parent != epic.Issue.Number {
		t.Errorf("Parent = %v, want #%d", read.Parent, epic.Issue.Number)
	}

	// The read side: one snapshot carries ready, list, epics and the epic's
	// children, and Detail names the next workable child.
	snapshot, err := tr.Snapshot(ctx, tracker.SnapshotOptions{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	ready := snapshot.Ready()
	if len(ready) != 1 || ready[0].Number != blocker.Issue.Number {
		t.Errorf("Ready = %+v, want only the unblocked blocker", ready)
	}
	if listed := snapshot.List(tracker.ListFilter{Label: "PARSER"}); len(listed) != 1 || listed[0].Number != work.Issue.Number {
		t.Errorf("List(label parser) = %+v", listed)
	}
	if children := snapshot.List(tracker.ListFilter{Epic: epic.Issue.Number}); len(children) != 1 {
		t.Errorf("List(epic) = %+v", children)
	}
	if epics := snapshot.Epics(); len(epics) != 1 || epics[0].Number != epic.Issue.Number {
		t.Errorf("Epics = %+v", epics)
	}
	detail, err := tr.Detail(ctx, epic.Issue.Number)
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if len(detail.Children) != 1 || detail.Children[0].Number != work.Issue.Number {
		t.Errorf("Detail children = %+v", detail.Children)
	}
	if detail.Next != nil {
		t.Errorf("Detail next = %v, want none while the only child is blocked", detail.Next)
	}

	search, err := tr.Search(ctx, tracker.SearchRequest{Terms: "parser"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(search.Issues) != 1 || search.Issues[0].Number != work.Issue.Number || search.Total != 1 {
		t.Errorf("Search = %+v", search)
	}

	primer, err := tr.Primer(ctx)
	if err != nil {
		t.Fatalf("Primer: %v", err)
	}
	if primer.OpenTotal != 3 || primer.ReadyTotal != 1 || primer.EpicsTotal != 1 {
		t.Errorf("primer = open %d ready %d epics %d, want 3/1/1", primer.OpenTotal, primer.ReadyTotal, primer.EpicsTotal)
	}
	if !strings.Contains(primer.Conventions, "Workflow: hew ready") || !strings.Contains(primer.Conventions, "hew pr") {
		t.Errorf("Primer.Conventions = %q, want the GitHub variant", primer.Conventions)
	}

	// Edits swap labels rather than stacking them, and move the parent.
	priority, typ := "P0", "enhancement"
	edited, err := tr.Edit(ctx, tracker.EditRequest{
		Issue:       work.Issue.Number,
		Priority:    &priority,
		Type:        &typ,
		AddAreas:    []string{"api"},
		RemoveAreas: []string{"parser"},
		Parent:      nil,
	})
	if err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if !slices.Contains(edited.Applied, "priority") || !slices.Contains(edited.Applied, "type") {
		t.Errorf("Applied = %v, want the label swaps", edited.Applied)
	}
	editedRead, err := tr.Get(ctx, work.Issue.Number)
	if err != nil {
		t.Fatal(err)
	}
	if editedRead.Priority == nil || *editedRead.Priority != "P0" {
		t.Errorf("priority = %v, want P0", editedRead.Priority)
	}
	if !slices.Equal(editedRead.Areas, []string{"api"}) {
		t.Errorf("areas = %v, want [api]", editedRead.Areas)
	}

	// Dependencies: a cycle is refused with the public type, and Unblock
	// removes the edge that was written.
	if _, err := tr.Block(ctx, blocker.Issue.Number, []int{work.Issue.Number}, tracker.BlockOptions{}); err == nil {
		t.Fatal("Block accepted a cycle")
	} else {
		var cycle *tracker.CycleError
		if !errors.As(err, &cycle) {
			t.Fatalf("Block err = %T (%v), want *tracker.CycleError", err, err)
		}
	}
	unblocked, err := tr.Unblock(ctx, work.Issue.Number, []int{blocker.Issue.Number}, tracker.BlockOptions{})
	if err != nil {
		t.Fatalf("Unblock: %v", err)
	}
	if !slices.Equal(unblocked.Removed, []int{blocker.Issue.Number}) {
		t.Errorf("Unblock = %+v, want the removed edge", unblocked)
	}

	// The claim lifecycle: one claimant wins, a second Start is refused with
	// the public type, and Release gives it back.
	started, err := tr.Start(ctx, work.Issue.Number, tracker.StartOptions{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if started.Claimant != "octocat" || !slices.Contains(started.Issue.Assignees, "octocat") || !started.Issue.HasLabel("in-progress") {
		t.Errorf("Start = %+v, want the claim as octocat", started)
	}
	_, err = tr.Start(ctx, work.Issue.Number, tracker.StartOptions{})
	var claimed *tracker.ClaimedError
	if !errors.As(err, &claimed) || claimed.Issue != work.Issue.Number {
		t.Fatalf("second Start err = %T (%v), want *tracker.ClaimedError", err, err)
	}
	if err := tr.Release(ctx, work.Issue.Number, tracker.ReleaseOptions{}); err != nil {
		t.Fatalf("Release: %v", err)
	}
	released, err := tr.Get(ctx, work.Issue.Number)
	if err != nil {
		t.Fatal(err)
	}
	if len(released.Assignees) != 0 || released.HasLabel("in-progress") {
		t.Errorf("after Release = %+v, want the claim cleared", released)
	}
	if _, err := tr.Start(ctx, work.Issue.Number, tracker.StartOptions{}); err != nil {
		t.Fatalf("Start after Release: %v", err)
	}

	workable, err := tr.Detail(ctx, epic.Issue.Number)
	if err != nil {
		t.Fatalf("Detail after unblock: %v", err)
	}
	if workable.Next == nil || *workable.Next != work.Issue.Number {
		t.Errorf("Detail next after unblock = %v, want #%d", workable.Next, work.Issue.Number)
	}

	// Close and reopen carry the reason as a comment and the state.
	closed, err := tr.Close(ctx, blocker.Issue.Number, tracker.CloseOptions{Reason: "shipped", Completed: true})
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if closed.StateReason != "COMPLETED" {
		t.Errorf("StateReason = %q, want COMPLETED", closed.StateReason)
	}
	reopened, err := tr.Reopen(ctx, blocker.Issue.Number, tracker.ReopenOptions{Reason: "regressed"})
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if !reopened.Reopened {
		t.Error("Reopen reported the issue was already open")
	}
	afterReopen, err := tr.Get(ctx, blocker.Issue.Number)
	if err != nil {
		t.Fatal(err)
	}
	if afterReopen.State != "open" || !slices.ContainsFunc(afterReopen.Comments, func(c tracker.Comment) bool {
		return strings.Contains(c.Body, "regressed")
	}) {
		t.Errorf("after Reopen = %+v, want open with the reason comment", afterReopen)
	}

	// A checkpointed plan: a dry run writes nothing, the real run creates and
	// wires, and a rerun over the same file checkpoint is a quiet no-op.
	entries := []tracker.PlanEntry{
		{ID: "epic", Title: "Planned epic", Type: tracker.PlanTypeEpic},
		{ID: "child", Title: "Planned child", Type: "task", Priority: "P3", Parent: &tracker.PlanRef{ID: "epic"}},
	}
	dry, err := tr.ApplyPlan(ctx, entries, tracker.ApplyOptions{DryRun: true})
	if err != nil {
		t.Fatalf("ApplyPlan dry run: %v", err)
	}
	if !dry.DryRun || len(dry.WouldCreate) != 2 || dry.Created != 0 {
		t.Errorf("dry run = %+v, want two planned entries and no writes", dry)
	}

	digest := tracker.FileDigest([]byte("consumer plan"))
	path := filepath.Join(t.TempDir(), "apply-state.json")
	checkpoint, err := tracker.OpenFileCheckpoint(path, "o/r", digest, "plan")
	if err != nil {
		t.Fatalf("OpenFileCheckpoint: %v", err)
	}
	applied, err := tr.ApplyPlan(ctx, entries, tracker.ApplyOptions{Checkpoint: checkpoint, Digest: digest})
	if err != nil {
		t.Fatalf("ApplyPlan: %v", err)
	}
	if applied.Created != 2 || applied.Wired != 1 {
		t.Errorf("ApplyPlan = %+v, want two creates and one edge", applied)
	}
	rerun, err := tracker.OpenFileCheckpoint(path, "o/r", digest, "plan")
	if err != nil {
		t.Fatalf("reopen checkpoint: %v", err)
	}
	againApplied, err := tr.ApplyPlan(ctx, entries, tracker.ApplyOptions{Checkpoint: rerun, Digest: digest})
	if err != nil {
		t.Fatalf("ApplyPlan rerun: %v", err)
	}
	if againApplied.Created != 0 || len(againApplied.Applied) != 2 {
		t.Fatalf("rerun = %+v, want every entry reported and nothing created", againApplied)
	}
	for _, entry := range againApplied.Applied {
		if entry.Created {
			t.Errorf("rerun created %+v", entry)
		}
	}
}

// TestExternalConsumerSeesAPartialCreate is the partial-operation error a
// multi-write creation reports when the issue landed and an edge did not: the
// consumer gets the created issue and the failed stage, not an opaque failure.
func TestExternalConsumerSeesAPartialCreate(t *testing.T) {
	fake := newGitHubFake()
	tr := openGitHubTracker(t, fake)
	ctx := context.Background()

	blocker, err := tr.Create(ctx, tracker.CreateRequest{Title: "Blocker", Type: "bug"})
	if err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.failEdges = true
	fake.mu.Unlock()

	_, err = tr.Create(ctx, tracker.CreateRequest{Title: "Child", Type: "task", BlockedBy: []int{blocker.Issue.Number}})
	var partial *tracker.PartialCreateError
	if !errors.As(err, &partial) {
		t.Fatalf("err = %T (%v), want *tracker.PartialCreateError", err, err)
	}
	if partial.Issue.Number == 0 {
		t.Error("PartialCreateError lost the created issue")
	}
	if partial.Stage != tracker.CreateStageBlockedBy {
		t.Errorf("Stage = %q, want %q", partial.Stage, tracker.CreateStageBlockedBy)
	}
}

// TestExternalConsumerRetriesALimitAndRecoversAuth exercises the two client
// behaviors a long-running consumer depends on without live credentials: a
// secondary rate limit is retried, and a rejected token is refreshed once.
func TestExternalConsumerRetriesALimitAndRecoversAuth(t *testing.T) {
	ctx := context.Background()

	recovering := newGitHubFake()
	recovering.authFailures = 1
	if tr := openGitHubTracker(t, recovering); tr.Identity() != "octocat" {
		t.Errorf("Identity = %q after the re-auth, want octocat", tr.Identity())
	}

	fake := newGitHubFake()
	tr := openGitHubTracker(t, fake)
	fake.mu.Lock()
	fake.rateLimits = 1
	fake.mu.Unlock()
	if _, err := tr.Snapshot(ctx, tracker.SnapshotOptions{}); err != nil {
		t.Fatalf("Snapshot behind a rate limit: %v", err)
	}
	budget, known := tr.RateLimit()
	if !known || budget.Remaining != 4999 {
		t.Errorf("RateLimit = %+v, known %v; want the budget the retried list reported", budget, known)
	}

	// A local tracker has no budget to report, and says so rather than
	// returning a zero value that looks like an exhausted one.
	_, local := externalTrackerCheckout(t)
	if budget, known := local.RateLimit(); known || budget.Remaining != 0 {
		t.Errorf("local RateLimit = %+v, known %v; want no budget", budget, known)
	}
}

// TestExternalConsumerTriagesUntriagedWork is the deliberate untriaged path:
// the query surface omits untriaged titles, and Triage is the one API that
// returns them, in both states.
func TestExternalConsumerTriagesUntriagedWork(t *testing.T) {
	fake := newGitHubFake()
	seed(fake, 90, "raw report", "no labels yet")
	seed(fake, 91, "old raw report", "closed and untriaged")
	fake.mu.Lock()
	fake.issues[91].state = "CLOSED"
	fake.mu.Unlock()

	tr := openGitHubTracker(t, fake)
	ctx := context.Background()

	snapshot, err := tr.Snapshot(ctx, tracker.SnapshotOptions{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if listed := snapshot.List(tracker.ListFilter{}); len(listed) != 0 {
		t.Errorf("List = %+v, want untriaged issues omitted", listed)
	}
	if ready := snapshot.Ready(); len(ready) != 0 {
		t.Errorf("Ready = %+v, want untriaged issues omitted", ready)
	}

	triage, err := tr.Triage(ctx, tracker.TriageRequest{})
	if err != nil {
		t.Fatalf("Triage: %v", err)
	}
	if len(triage.Issues) != 1 || triage.Issues[0].Number != 90 {
		t.Errorf("Triage = %+v, want only the open untriaged issue", triage)
	}

	both, err := tr.Triage(ctx, tracker.TriageRequest{Search: "raw"})
	if err != nil {
		t.Fatalf("Triage search: %v", err)
	}
	if len(both.Issues) != 2 {
		t.Errorf("Triage search = %+v, want both states", both)
	}
}
