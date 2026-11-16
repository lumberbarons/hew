// Package migrate_test exercises the public migration engine the way an
// external module does: only public imports, a Target the consumer writes,
// and tracker's in-memory checkpoint. Nothing here runs the hew binary or
// reaches the network.
package migrate_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/migrate"
	"github.com/lumberbarons/hew/tracker"
)

// memoryTarget is a consumer's own migrate.Target: an in-memory issue store
// that records the writes and can fail a chosen call.
type memoryTarget struct {
	issues     map[int]*memoryIssue
	comments   map[int][]string
	calls      []string
	callCounts map[string]int
	failNext   map[string]error
	failAt     map[string]int
	failAtErr  map[string]error
	next       int
	identity   string
	created    int
	closed     int
	commented  int
}

type memoryIssue struct {
	number int
	title  string
	body   string
	state  string
	reason string
}

func newMemoryTarget() *memoryTarget {
	return &memoryTarget{
		issues:     map[int]*memoryIssue{},
		comments:   map[int][]string{},
		callCounts: map[string]int{},
		failNext:   map[string]error{},
		failAt:     map[string]int{},
		failAtErr:  map[string]error{},
		next:       500,
		identity:   "octocat",
	}
}

// failOnce makes the next call to method fail, then behave normally again.
func (t *memoryTarget) failOnce(method string, err error) { t.failNext[method] = err }

// failCall makes the nth (1-based) call to method fail.
func (t *memoryTarget) failCall(method string, n int, err error) {
	t.failAt[method] = n
	t.failAtErr[method] = err
}

func (t *memoryTarget) take(method string) error {
	t.calls = append(t.calls, method)
	t.callCounts[method]++
	if t.failAt[method] == t.callCounts[method] {
		return t.failAtErr[method]
	}
	if err, ok := t.failNext[method]; ok {
		delete(t.failNext, method)
		return err
	}
	return nil
}

func (t *memoryTarget) Create(_ context.Context, req migrate.CreateRequest) (int, error) {
	if err := t.take("Create"); err != nil {
		return 0, err
	}
	t.next++
	t.issues[t.next] = &memoryIssue{number: t.next, title: req.Title, body: req.Body, state: "OPEN"}
	t.created++
	return t.next, nil
}

func (t *memoryTarget) Get(_ context.Context, number int) (migrate.Issue, error) {
	if err := t.take("Get"); err != nil {
		return migrate.Issue{}, err
	}
	issue, ok := t.issues[number]
	if !ok {
		return migrate.Issue{}, fmt.Errorf("issue #%d not found", number)
	}
	return migrate.Issue{Number: issue.number, Title: issue.title, Body: issue.body, State: issue.state}, nil
}

func (t *memoryTarget) EditTitle(_ context.Context, number int, title string) error {
	if err := t.take("EditTitle"); err != nil {
		return err
	}
	t.issues[number].title = title
	return nil
}

func (t *memoryTarget) EditBody(_ context.Context, number int, body string) error {
	if err := t.take("EditBody"); err != nil {
		return err
	}
	t.issues[number].body = body
	return nil
}

func (t *memoryTarget) Comment(_ context.Context, number int, body string) error {
	if err := t.take("Comment"); err != nil {
		return err
	}
	t.comments[number] = append(t.comments[number], body)
	t.commented++
	return nil
}

func (t *memoryTarget) Close(_ context.Context, number int, reason migrate.CloseReason) error {
	if err := t.take("Close"); err != nil {
		return err
	}
	t.issues[number].state = "CLOSED"
	t.issues[number].reason = string(reason)
	t.closed++
	return nil
}

func (t *memoryTarget) AddSubIssue(_ context.Context, parent, child int) error {
	return t.take("AddSubIssue")
}

func (t *memoryTarget) AddBlockedBy(_ context.Context, issue, blocker int) error {
	return t.take("AddBlockedBy")
}

func (t *memoryTarget) AddAssignee(_ context.Context, number int, login string) error {
	return t.take("AddAssignee")
}

func (t *memoryTarget) Viewer(context.Context) (string, error) { return t.identity, nil }

// publicItems is a small source: a parent, a child blocked by it and claimed
// by the viewer, and a closed item with comments and a close reason.
func publicItems() []migrate.Item {
	return []migrate.Item{
		{Key: "epic", Title: "Epic", Body: "the parent"},
		{Key: "child", Title: "Child", Body: "see epic", Parent: "epic", BlockedBy: []string{"epic"}, Assignees: []string{migrate.AssigneeViewer}},
		{Key: "done", Title: "Done", Closed: true, CloseReason: migrate.CloseNotPlanned, Comments: []string{"first", "second"}},
	}
}

// An external consumer can build items and ask for a plan without a write.
func TestExternalConsumerDryRun(t *testing.T) {
	target := newMemoryTarget()
	engine := migrate.Wrap(target, migrate.Config{Checkpoint: tracker.NewMemoryCheckpoint()})
	res, err := engine.Run(context.Background(), migrate.RunRequest{
		Items:   publicItems(),
		Options: migrate.Options{DryRun: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.DryRun || len(res.WouldCreate) != 3 {
		t.Fatalf("result = %+v, want a three-item plan", res)
	}
	if len(target.calls) != 0 {
		t.Errorf("the dry run touched the target: %v", target.calls)
	}
}

// An external consumer can run the whole workflow with only public types and
// a checkpoint it owns.
func TestExternalConsumerRunsTheWorkflow(t *testing.T) {
	target := newMemoryTarget()
	engine := migrate.Wrap(target, migrate.Config{Checkpoint: tracker.NewMemoryCheckpoint()})
	res, err := engine.Run(context.Background(), migrate.RunRequest{Items: publicItems()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 3 || res.Wired != 2 || res.Closed != 1 || res.Failed != 0 {
		t.Fatalf("result = %+v, want 3 created, 2 wired, 1 closed", res)
	}
	epic, child, done := res.Mapping["epic"], res.Mapping["child"], res.Mapping["done"]
	if target.issues[epic].state != "OPEN" || target.issues[child].state != "OPEN" {
		t.Errorf("epic/child state = %q/%q, want both open", target.issues[epic].state, target.issues[child].state)
	}
	if got := target.issues[done].reason; got != string(migrate.CloseNotPlanned) {
		t.Errorf("done reason = %q, want %q", got, migrate.CloseNotPlanned)
	}
	if got := target.comments[done]; !slices.Equal(got, []string{"first", "second"}) {
		t.Errorf("done comments = %q, want both in order", got)
	}
	if !strings.Contains(target.issues[child].body, "<!-- hew:migrate key=child") {
		t.Errorf("child body = %q, want the provenance marker", target.issues[child].body)
	}
	if target.issues[child].number == 0 {
		t.Error("the child was never created")
	}
}

// A failure injected after a creation is resumed by the same checkpoint
// without repeating the creates that already landed.
func TestExternalConsumerResumesAfterAFailedCreate(t *testing.T) {
	target := newMemoryTarget()
	checkpoint := tracker.NewMemoryCheckpoint()
	engine := migrate.Wrap(target, migrate.Config{Checkpoint: checkpoint})

	// Fail the second create: the first item is created and checkpointed.
	target.failCall("Create", 2, errors.New("injected create failure"))
	_, err := engine.Run(context.Background(), migrate.RunRequest{Items: publicItems()})
	if err == nil || !strings.Contains(err.Error(), "rerun to resume") {
		t.Fatalf("err = %v, want a create failure that names the rerun", err)
	}
	if target.created != 1 {
		t.Fatalf("created %d issues before the failure, want 1", target.created)
	}
	first := checkpointEntries(checkpoint)["epic"]
	if first == 0 {
		t.Fatal("the successful create was not checkpointed")
	}

	res, err := engine.Run(context.Background(), migrate.RunRequest{Items: publicItems()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 2 || target.created != 3 {
		t.Errorf("resume created %d (total %d), want only the two missing", res.Created, target.created)
	}
	if got := checkpointEntries(checkpoint)["epic"]; got != first {
		t.Errorf("resume re-created the checkpointed item: %v → %v", first, got)
	}
}

// stageWant is what one injected failure leaves behind, and what the resume
// therefore still has to do.
type stageWant struct {
	wired      int
	closed     int
	commented  int
	resumeWire int
}

// commentedOfResume is how many of the closed item's two comments a resume
// posts: both when the first failed, none when they already landed.
func (w stageWant) commentedOfResume() int { return 2 - w.commented }

// A failure injected after wiring, commenting or closing is resumed from the
// same checkpoint without repeating any of those writes.
func TestExternalConsumerResumesAfterEachLaterStage(t *testing.T) {
	cases := map[string]stageWant{
		// The failed edge is retried; the edge after it landed and is skipped.
		"AddSubIssue":  {wired: 1, closed: 1, commented: 2, resumeWire: 1},
		"AddBlockedBy": {wired: 1, closed: 1, commented: 2, resumeWire: 1},
		// The first comment failed, so neither later comment nor the close ran.
		"Comment": {wired: 2, closed: 0, commented: 0, resumeWire: 0},
		// Both comments landed; only the close is outstanding.
		"Close": {wired: 2, closed: 0, commented: 2, resumeWire: 0},
	}
	for stage, expected := range cases {
		t.Run(stage, func(t *testing.T) {
			target := newMemoryTarget()
			engine := migrate.Wrap(target, migrate.Config{Checkpoint: tracker.NewMemoryCheckpoint()})
			target.failOnce(stage, errors.New("injected "+stage+" failure"))

			res, err := engine.Run(context.Background(), migrate.RunRequest{Items: publicItems()})
			if err != nil {
				t.Fatal(err)
			}
			if res.Failed == 0 {
				t.Fatalf("result = %+v, want the %s failure counted", res, stage)
			}
			if res.Wired != expected.wired || target.closed != expected.closed || target.commented != expected.commented {
				t.Fatalf("first run = %+v with %d closed and %d comments, want wired=%d closed=%d comments=%d",
					res, target.closed, target.commented, expected.wired, expected.closed, expected.commented)
			}
			created, comments := target.created, target.commented

			res, err = engine.Run(context.Background(), migrate.RunRequest{Items: publicItems()})
			if err != nil {
				t.Fatal(err)
			}
			if target.created != created {
				t.Errorf("resume repeated a create: %d → %d", created, target.created)
			}
			if res.Wired != expected.resumeWire {
				t.Errorf("resume wired %d edges, want %d", res.Wired, expected.resumeWire)
			}
			if res.Closed != 1-expected.closed {
				t.Errorf("resume closed %d, want only the outstanding close", res.Closed)
			}
			// A failed comment blocks the rest of that item, so a resume posts
			// both; otherwise it posts none.
			if got := target.commented - comments; got != expected.commentedOfResume() {
				t.Errorf("resume posted %d comments, want %d", got, expected.commentedOfResume())
			}
			if res.Failed != 0 {
				t.Errorf("resume failed again: %+v", res.Warnings)
			}
		})
	}
}

// A checkpoint that maps a key to an issue the migration did not create is
// refused before any write: provenance is what makes a resume safe.
func TestExternalConsumerRefusesAForeignMapping(t *testing.T) {
	target := newMemoryTarget()
	target.issues[42] = &memoryIssue{number: 42, title: "Unrelated", body: "do not touch", state: "OPEN"}
	checkpoint := tracker.NewMemoryCheckpoint()
	if err := checkpoint.RecordCreated("child", 42); err != nil {
		t.Fatal(err)
	}
	engine := migrate.Wrap(target, migrate.Config{Checkpoint: checkpoint})
	_, err := engine.Run(context.Background(), migrate.RunRequest{Items: publicItems()})
	if err == nil || !strings.Contains(err.Error(), "does not carry the hew provenance marker") {
		t.Fatalf("err = %v, want a provenance refusal", err)
	}
	for _, call := range target.calls {
		if call != "Get" {
			t.Errorf("a refused run wrote: %s", call)
		}
	}
	if len(target.comments[42]) != 0 || target.issues[42].state != "OPEN" {
		t.Error("the unrelated issue was mutated")
	}
}

// The consumer names migrate.Checkpoint explicitly: a tracker memory
// checkpoint satisfies it without any internal type.
func TestExternalConsumerCheckpointIsPublic(t *testing.T) {
	var checkpoint migrate.Checkpoint = tracker.NewMemoryCheckpoint()
	engine := migrate.Wrap(newMemoryTarget(), migrate.Config{Checkpoint: checkpoint})
	if _, err := engine.Run(context.Background(), migrate.RunRequest{
		Items:   []migrate.Item{{Key: "one", Title: "One"}},
		Options: migrate.Options{DryRun: true},
	}); err != nil {
		t.Fatal(err)
	}
}

// checkpointEntries reads the public checkpoint's mapping, the way a consumer
// inspects a resume record.
func checkpointEntries(checkpoint tracker.Checkpoint) map[string]int {
	out := map[string]int{}
	for _, entry := range checkpoint.CreatedEntries() {
		out[entry.Key] = entry.Number
	}
	return out
}
