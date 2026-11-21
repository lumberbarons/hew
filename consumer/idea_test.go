// This file is the external-consumer scenario for the idea type: the public
// create, edit, apply and local-migration workflows treat `idea` like the
// implementation types, on both backends, with no internal imports.
package consumer_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/migrate"
	"github.com/lumberbarons/hew/tracker"
)

// typeLabels returns the labels a consumer recognizes as issue types. The
// vocabulary is part of the public contract, so the test spells it out rather
// than importing the model's list.
func typeLabels(labels []string) []string {
	var out []string
	for _, l := range labels {
		switch strings.ToLower(l) {
		case "bug", "enhancement", "task", "idea":
			out = append(out, strings.ToLower(l))
		}
	}
	return out
}

func priorityLabels(labels []string) []string {
	var out []string
	for _, l := range labels {
		if len(l) == 2 && (l[0] == 'P' || l[0] == 'p') && l[1] >= '0' && l[1] <= '4' {
			out = append(out, l)
		}
	}
	return out
}

// assertOneTypeAndPriority is done-when's "exactly one type and one
// priority": the write path leaves no stray second label of either kind.
func assertOneTypeAndPriority(t *testing.T, labels []string, wantType string) {
	t.Helper()
	types := typeLabels(labels)
	if !slices.Equal(types, []string{wantType}) {
		t.Errorf("type labels = %v, want exactly %q (all labels: %v)", types, wantType, labels)
	}
	if priorities := priorityLabels(labels); len(priorities) != 1 {
		t.Errorf("priority labels = %v, want exactly one (all labels: %v)", priorities, labels)
	}
}

// TestExternalConsumerRecognizesIdeaAsAType drives create and edit through the
// local backend, which is a real tracker on disk, and reads both back.
func TestExternalConsumerRecognizesIdeaAsAType(t *testing.T) {
	tr := externalTracker(t)
	ctx := context.Background()

	created, err := tr.Create(ctx, tracker.CreateRequest{Title: "Consider a cache", Type: "idea"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	read, err := tr.Get(ctx, created.Issue.Number)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if read.Type == nil || *read.Type != "idea" || read.Priority == nil || *read.Priority != "P2" {
		t.Errorf("read = type %v priority %v, want idea/P2", read.Type, read.Priority)
	}
	assertOneTypeAndPriority(t, read.Labels(), "idea")

	task := "task"
	if _, err := tr.Edit(ctx, tracker.EditRequest{Issue: read.Number, Type: &task}); err != nil {
		t.Fatalf("Edit to task: %v", err)
	}
	read, err = tr.Get(ctx, created.Issue.Number)
	if err != nil {
		t.Fatalf("Get after retype: %v", err)
	}
	if read.Type == nil || *read.Type != "task" {
		t.Errorf("type = %v, want task after the swap", read.Type)
	}
	assertOneTypeAndPriority(t, read.Labels(), "task")

	idea := "idea"
	if _, err := tr.Edit(ctx, tracker.EditRequest{Issue: read.Number, Type: &idea}); err != nil {
		t.Fatalf("Edit back to idea: %v", err)
	}
	read, err = tr.Get(ctx, created.Issue.Number)
	if err != nil {
		t.Fatalf("Get after the second swap: %v", err)
	}
	if read.Type == nil || *read.Type != "idea" {
		t.Errorf("type = %v, want idea", read.Type)
	}
	assertOneTypeAndPriority(t, read.Labels(), "idea")
}

// TestExternalConsumerRecognizesIdeaAsATypeOnGitHub does the same over an
// isolated transport, and pins the compatibility rule for an issue a
// repository already labelled both task and idea: it reads as a task and
// carries the multiple-type warning, with both labels left stored.
func TestExternalConsumerRecognizesIdeaAsATypeOnGitHub(t *testing.T) {
	fake := newGitHubFake()
	tr := openGitHubTracker(t, fake)
	ctx := context.Background()

	created, err := tr.Create(ctx, tracker.CreateRequest{Title: "Consider a cache", Type: "idea", Priority: "P1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	read, err := tr.Get(ctx, created.Issue.Number)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if read.Type == nil || *read.Type != "idea" || read.Priority == nil || *read.Priority != "P1" {
		t.Errorf("read = type %v priority %v, want idea/P1", read.Type, read.Priority)
	}
	assertOneTypeAndPriority(t, read.Labels(), "idea")

	seed(fake, 700, "Legacy mixed labels", "", "P2", "task", "idea")
	legacy, err := tr.Get(ctx, 700)
	if err != nil {
		t.Fatalf("Get legacy: %v", err)
	}
	if legacy.Type == nil || *legacy.Type != "task" {
		t.Errorf("legacy type = %v, want the existing task to win", legacy.Type)
	}
	if !slices.Equal(legacy.Labels(), []string{"P2", "task", "idea"}) {
		t.Errorf("legacy labels = %v, want both stored labels retained", legacy.Labels())
	}
	primer, err := tr.Primer(ctx)
	if err != nil {
		t.Fatalf("Primer: %v", err)
	}
	warned := false
	for _, w := range primer.Warnings {
		if w.Kind == tracker.WarnMultiType && w.Issue == 700 {
			warned = true
		}
	}
	if !warned {
		t.Errorf("warnings = %v, want the multiple-type warning for #700", primer.Warnings)
	}
}

// TestExternalConsumerClaimsAnIdeaOnlyForDiscovery is the claim half from
// outside the module: the refusal is a public type with the issue number on it,
// a refused idea keeps its labels and claim state, discovery intent claims it,
// and creating an idea with an assignee fails before writing anything.
func TestExternalConsumerClaimsAnIdeaOnlyForDiscovery(t *testing.T) {
	tr := externalTracker(t)
	ctx := context.Background()

	created, err := tr.Create(ctx, tracker.CreateRequest{Title: "Consider a cache", Type: "idea"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	number := created.Issue.Number

	var refusal *tracker.DiscoveryIntentError
	if _, err := tr.Start(ctx, number, tracker.StartOptions{}); !errors.As(err, &refusal) {
		t.Fatalf("start idea = %T (%v), want *tracker.DiscoveryIntentError", err, err)
	}
	if refusal.Issue != number {
		t.Errorf("DiscoveryIntentError.Issue = %d, want %d", refusal.Issue, number)
	}
	read, err := tr.Get(ctx, number)
	if err != nil {
		t.Fatalf("Get after refusal: %v", err)
	}
	if len(read.Assignees) != 0 || !slices.Equal(read.Labels(), []string{"P2", "idea"}) {
		t.Errorf("refused start left labels %v assignees %v, want the idea untouched", read.Labels(), read.Assignees)
	}

	claimed, err := tr.Start(ctx, number, tracker.StartOptions{ForDiscovery: true})
	if err != nil {
		t.Fatalf("discovery start: %v", err)
	}
	if claimed.Claimant == "" || !slices.Contains(claimed.Issue.Assignees, claimed.Claimant) {
		t.Errorf("claim = %+v, want the claimant assigned", claimed)
	}

	var invalid *tracker.InvalidInputError
	if _, err := tr.Create(ctx, tracker.CreateRequest{
		Title: "Already claimed", Type: "idea", Assignees: []string{"octocat"},
	}); !errors.As(err, &invalid) || invalid.Field != "assignees" {
		t.Fatalf("idea create with an assignee = %v, want an assignees InvalidInputError", err)
	}
	snapshot, err := tr.Snapshot(ctx, tracker.SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(snapshot.Issues()); got != 1 {
		t.Errorf("refused create left %d issues, want only the original idea", got)
	}
}

// TestExternalConsumerAppliesAndMigratesAnIdea covers the plan and migration
// halves: a dry run and a real apply both accept `idea`, and a local idea
// keeps its type and its single-priority invariant when the tracker moves to
// GitHub.
func TestExternalConsumerAppliesAndMigratesAnIdea(t *testing.T) {
	root, tr := localWork(t)
	ctx := context.Background()
	entries := []tracker.PlanEntry{
		{ID: "idea", Title: "Consider a cache", Type: "idea", Priority: "P2"},
		{ID: "work", Title: "Add a cache", Type: "task", Priority: "P3"},
	}

	dry, err := tr.ApplyPlan(ctx, entries, tracker.ApplyOptions{DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if len(dry.WouldCreate) != 2 || dry.WouldCreate[0].Type != "idea" {
		t.Fatalf("dry run plan = %+v, want the idea entry first", dry.WouldCreate)
	}
	snapshot, err := tr.Snapshot(ctx, tracker.SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(snapshot.Issues()); got != 0 {
		t.Fatalf("dry run wrote %d issue(s)", got)
	}

	applied, err := tr.ApplyPlan(ctx, entries, tracker.ApplyOptions{})
	if err != nil {
		t.Fatalf("ApplyPlan: %v", err)
	}
	ideaNumber := applied.Mapping["idea"]
	if ideaNumber == 0 {
		t.Fatalf("mapping = %v, want the idea entry created", applied.Mapping)
	}
	read, err := tr.Get(ctx, ideaNumber)
	if err != nil {
		t.Fatal(err)
	}
	if read.Type == nil || *read.Type != "idea" {
		t.Errorf("applied type = %v, want idea", read.Type)
	}
	assertOneTypeAndPriority(t, read.Labels(), "idea")

	fake := newGitHubFake()
	transport := openFake(t, fake)
	m, err := migrate.Open(ctx, migrateConfig(t, root, transport, false))
	if err != nil {
		t.Fatalf("migrate.Open: %v", err)
	}
	res, err := m.Local(ctx, migrate.LocalRequest{Dir: filepath.Join(root, ".hew")})
	if err != nil {
		t.Fatalf("Local: %v", err)
	}
	if res.Created != 2 {
		t.Fatalf("created %d, want the idea and the task", res.Created)
	}
	fake.mu.Lock()
	migrated := fake.issues[101]
	var labels []string
	if migrated != nil {
		labels = slices.Clone(migrated.labels)
	}
	fake.mu.Unlock()
	if migrated == nil {
		t.Fatal("the idea was not migrated")
	}
	assertOneTypeAndPriority(t, labels, "idea")
}

// TestExternalConsumerKeepsIdeasOutOfEpicHierarchy is the hierarchy contract
// from outside the module: create and epic creation refuse new idea
// relationships before writing, and an ordinary issue reference stays a
// reference rather than becoming a child link.
func TestExternalConsumerKeepsIdeasOutOfEpicHierarchy(t *testing.T) {
	tr := externalTracker(t)
	ctx := context.Background()

	epic, err := tr.CreateEpic(ctx, tracker.CreateEpicRequest{Title: "Voltgo support"})
	if err != nil {
		t.Fatalf("CreateEpic: %v", err)
	}
	idea, err := tr.Create(ctx, tracker.CreateRequest{Title: "Consider a cache", Type: "idea"})
	if err != nil {
		t.Fatalf("Create idea: %v", err)
	}

	var invalid *tracker.InvalidInputError
	if _, err := tr.Create(ctx, tracker.CreateRequest{
		Title: "Child idea", Type: "idea", Parent: epic.Issue.Number,
	}); !errors.As(err, &invalid) || invalid.Field != "parent" {
		t.Fatalf("idea child = %v, want a parent InvalidInputError", err)
	}
	invalid = nil
	if _, err := tr.Create(ctx, tracker.CreateRequest{
		Title: "Work", Type: "task", Parent: idea.Issue.Number,
	}); !errors.As(err, &invalid) || invalid.Field != "parent" {
		t.Fatalf("task under an idea = %v, want a parent InvalidInputError", err)
	}
	invalid = nil
	if _, err := tr.CreateEpic(ctx, tracker.CreateEpicRequest{
		Title: "Another epic", Children: []int{idea.Issue.Number},
	}); !errors.As(err, &invalid) || invalid.Field != "children" {
		t.Fatalf("epic with an idea child = %v, want a children InvalidInputError", err)
	}

	snapshot, err := tr.Snapshot(ctx, tracker.SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(snapshot.Issues()); got != 2 {
		t.Fatalf("refused writes left %d issues, want only the epic and the idea", got)
	}

	referenced, err := tr.Create(ctx, tracker.CreateRequest{
		Title: "Further thought", Type: "idea",
		Body: tracker.Body{Text: fmt.Sprintf("Follow-up to #%d", epic.Issue.Number)},
	})
	if err != nil {
		t.Fatalf("Create referencing idea: %v", err)
	}
	read, err := tr.Get(ctx, referenced.Issue.Number)
	if err != nil {
		t.Fatal(err)
	}
	if read.Parent != nil {
		t.Errorf("parent = %+v, want a body reference to create no child link", read.Parent)
	}
	detail, err := tr.Detail(ctx, epic.Issue.Number)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Children) != 0 {
		t.Errorf("epic children = %v, want the referenced idea outside the hierarchy", detail.Children)
	}
}

// TestExternalConsumerReadsAnExternallyAttachedIdeaAsAnEpic pins the read
// precedence on GitHub: an idea a repository attached to a child reads as an
// epic, keeps its stored relationships visible, cannot be claimed even with
// discovery intent, and can still be detached through the ordinary edit.
func TestExternalConsumerReadsAnExternallyAttachedIdeaAsAnEpic(t *testing.T) {
	fake := newGitHubFake()
	tr := openGitHubTracker(t, fake)
	ctx := context.Background()

	seed(fake, 800, "Epic: Voltgo support", "", "P2", "epic")
	seed(fake, 801, "Consider a cache", "", "P1", "idea")
	seed(fake, 802, "Implementation", "", "P2", "task")
	fake.mu.Lock()
	fake.issues[801].parent = 800
	fake.issues[802].parent = 801
	fake.mu.Unlock()

	read, err := tr.Get(ctx, 801)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !read.Epic {
		t.Error("Epic = false, want the idea with children read as an epic")
	}
	if read.Type == nil || *read.Type != "idea" {
		t.Errorf("type = %v, want the stored idea type kept", read.Type)
	}
	if got, want := read.SubIssues, []int{802}; !slices.Equal(got, want) {
		t.Errorf("subIssues = %v, want the stored relationship %v visible", got, want)
	}

	var epicErr *tracker.EpicError
	if _, err := tr.Start(ctx, 801, tracker.StartOptions{ForDiscovery: true}); !errors.As(err, &epicErr) {
		t.Fatalf("discovery start = %v, want an *tracker.EpicError", err)
	}

	detail, err := tr.Detail(ctx, 801)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := issueNumbers(detail.Children), []int{802}; !slices.Equal(got, want) {
		t.Errorf("detail children = %v, want %v", got, want)
	}

	if _, err := tr.Edit(ctx, tracker.EditRequest{Issue: 801, NoParent: true}); err != nil {
		t.Fatalf("detach externally attached idea: %v", err)
	}
	read, err = tr.Get(ctx, 801)
	if err != nil {
		t.Fatal(err)
	}
	if read.Parent != nil {
		t.Errorf("parent = %+v, want the external attachment removed", read.Parent)
	}
	if !read.Epic {
		t.Error("Epic = false, want the idea's own child kept")
	}
}

func issueNumbers(issues []tracker.Issue) []int {
	out := make([]int, len(issues))
	for i, issue := range issues {
		out[i] = issue.Number
	}
	return out
}
