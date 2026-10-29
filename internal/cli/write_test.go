package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/lumberbarons/hew/internal/conventions"
	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/local"
	"github.com/lumberbarons/hew/internal/model"
)

func exitCode(t *testing.T, err error, want int) {
	t.Helper()
	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected ExitError, got %v", err)
	}
	if exitErr.Code != want {
		t.Errorf("exit code = %d (%s), want %d", exitErr.Code, exitErr.Message, want)
	}
}

// hostile is what a GitHub user can put in any free-text field: an SGR
// colour change, an OSC 52 clipboard write, a bare CR that overwrites the
// line already printed, a C1 CSI both as valid UTF-8 and as a raw byte, a
// newline that forges a line of its own, and a DEL. Command-level progress
// and warning messages bypass the renderer, so they need the same assertion
// against the same surface (#107).
const hostile = "a\x1b[31mb\x1b]52;c;aGVsbG8=\x07c\rd\x9be\u009bf\ng\x7fh"

// assertNeutralized fails when output still carries anything a terminal
// would act on. Newline is the CLI's own formatting.
func assertNeutralized(t *testing.T, label, s string) {
	t.Helper()
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			t.Errorf("%s: invalid UTF-8 byte %#x at offset %d in %q", label, s[i], i, s)
		case r == '\n':
		case unicode.IsControl(r):
			t.Errorf("%s: control character %#x at offset %d in %q", label, r, i, s)
		}
		i += size
	}
}

func TestCreateValidation(t *testing.T) {
	f := newFake()
	app, _, _ := newApp(f)
	exitCode(t, app.Create(ctx, CreateOpts{Type: "bug"}), ExitUsage)
	exitCode(t, app.Create(ctx, CreateOpts{Title: "T", Type: "story"}), ExitUsage)
	exitCode(t, app.Create(ctx, CreateOpts{Title: "T", Type: "bug", Priority: "P9"}), ExitUsage)
	exitCode(t, app.Create(ctx, CreateOpts{Title: "T", Type: "bug", BodyFile: "f", Edit: true}), ExitUsage)
	// Usage errors mean "nothing happened": no issue may leak out before
	// validation completes.
	if len(f.calls) != 0 {
		t.Errorf("refused creates still called the API: %v", f.calls)
	}
}

func TestCreateEpicPointsToEpicCreate(t *testing.T) {
	for _, title := range []string{"", "Parent"} {
		t.Run(title, func(t *testing.T) {
			f := newFake()
			app, _, _ := newApp(f)
			err := app.Create(ctx, CreateOpts{Title: title, Type: "epic"})
			exitCode(t, err, ExitUsage)
			if !strings.Contains(err.Error(), "hew epic create") {
				t.Errorf("error = %v, want hew epic create hint", err)
			}
			if len(f.calls) != 0 {
				t.Errorf("refused create called the API: %v", f.calls)
			}
		})
	}
}

func TestAreaFlagsRejectConventionLabels(t *testing.T) {
	f := newFake(issue(1, "Work", "P2", "bug"))
	app, _, _ := newApp(f)
	// Smuggling a priority or type through an area flag would stack a second
	// convention label (or strip the only one) — refuse before mutating.
	exitCode(t, app.Create(ctx, CreateOpts{Title: "T", Type: "bug", Areas: []string{"P0"}}), ExitUsage)
	exitCode(t, app.Create(ctx, CreateOpts{Title: "T", Type: "bug", Areas: []string{"task"}}), ExitUsage)
	exitCode(t, app.Set(ctx, 1, SetOpts{AddAreas: []string{"task"}}), ExitUsage)
	exitCode(t, app.Set(ctx, 1, SetOpts{RemoveAreas: []string{"P2"}}), ExitUsage)
	if len(f.calls) != 0 {
		t.Errorf("refused area flags still called the API: %v", f.calls)
	}
	if got := f.byNumber(1).Labels; !reflect.DeepEqual(got, []string{"P2", "bug"}) {
		t.Errorf("labels mutated despite refusal: %v", got)
	}
}

// Label names are case-insensitive on GitHub, so "Bug" would attach the existing
// bug label and leave a second type (or priority) on the issue.
func TestAreaFlagsRejectConventionLabelsInAnyCase(t *testing.T) {
	for _, area := range []string{"Bug", "ENHANCEMENT", "p0", "p4", "IN-Progress"} {
		t.Run(area, func(t *testing.T) {
			eachBackend(t, func(t *testing.T, store gh.IssueStore) {
				work, err := store.CreateIssue(ctx, "Existing", "body", []string{"P2", "task"})
				if err != nil {
					t.Fatal(err)
				}
				app, out, _ := newLocalApp(store)
				exitCode(t, app.Create(ctx, CreateOpts{Title: "T", Type: "task", Areas: []string{area}}), ExitUsage)
				exitCode(t, app.Set(ctx, work.Number, SetOpts{AddAreas: []string{area}}), ExitUsage)
				exitCode(t, app.Set(ctx, work.Number, SetOpts{RemoveAreas: []string{area}}), ExitUsage)

				plan := filepath.Join(t.TempDir(), "plan.jsonl")
				entry, err := json.Marshal(map[string]any{"title": "T", "type": "task", "areas": []string{area}})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(plan, append(entry, '\n'), 0o600); err != nil {
					t.Fatal(err)
				}
				err = app.Apply(ctx, ApplyOpts{File: plan})
				if err == nil || !strings.Contains(err.Error(), "collides with a convention label") {
					t.Errorf("apply error = %v, want a convention-label collision", err)
				}

				if out.Len() != 0 {
					t.Errorf("refused writes emitted output: %q", out.String())
				}
				got, err := store.GetIssue(ctx, work.Number)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got.Labels, []string{"P2", "task"}) {
					t.Errorf("labels mutated despite refusal: %v", got.Labels)
				}
				open, err := store.ListIssues(ctx, []gh.IssueState{gh.StateOpen})
				if err != nil {
					t.Fatal(err)
				}
				if len(open) != 1 {
					t.Errorf("%d open issues, want only the existing one", len(open))
				}
			})
		})
	}
}

// Re-asserting a convention label that exists in another case must neither lose
// it (remove, then skip the add) nor stack a second one.
func TestSetTypeAndPriorityTreatStrayCaseAsTheSameLabel(t *testing.T) {
	eachBackend(t, func(t *testing.T, store gh.IssueStore) {
		work, err := store.CreateIssue(ctx, "Existing", "body", []string{"p2", "Bug", "task"})
		if err != nil {
			t.Fatal(err)
		}
		app, _, _ := newLocalApp(store)
		if err := app.Set(ctx, work.Number, SetOpts{Type: "bug", Priority: "P2"}); err != nil {
			t.Fatal(err)
		}
		got, err := store.GetIssue(ctx, work.Number)
		if err != nil {
			t.Fatal(err)
		}
		if p, multi := got.Priority(); p != model.P2 || multi {
			t.Errorf("priority = %v, %v from labels %v", p, multi, got.Labels)
		}
		if typ, multi := got.Type(); typ != "bug" || multi {
			t.Errorf("type = %q, %v from labels %v", typ, multi, got.Labels)
		}
	})
}

func TestCreateDefaults(t *testing.T) {
	f := newFake()
	app, out, _ := newApp(f)
	if err := app.Create(ctx, CreateOpts{Title: "New thing", Type: "enhancement"}); err != nil {
		t.Fatal(err)
	}
	created := f.byNumber(101)
	if created == nil {
		t.Fatal("issue not created")
	}
	if !reflect.DeepEqual(created.Labels, []string{"P2", "enhancement"}) {
		t.Errorf("labels = %v", created.Labels)
	}
	if !strings.Contains(out.String(), "created #101: New thing") {
		t.Errorf("output = %q", out.String())
	}
}

func TestCreateFull(t *testing.T) {
	f := newFake(issue(1, "Blocker", "P2", "bug"), issue(10, "Epic: parent", "P2"))
	app, _, _ := newApp(f)
	err := app.Create(ctx, CreateOpts{
		Title: "Child work", Type: "task", Priority: "P1",
		Areas: []string{"tests"}, BlockedBy: []int{1}, Parent: 10, DiscoveredFrom: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	created := f.byNumber(101)
	if !reflect.DeepEqual(created.Labels, []string{"P1", "task", "tests"}) {
		t.Errorf("labels = %v", created.Labels)
	}
	if len(created.BlockedBy) != 1 || created.BlockedBy[0].Number != 1 {
		t.Errorf("blockedBy = %v", created.BlockedBy)
	}
	if created.Parent == nil || created.Parent.Number != 10 {
		t.Errorf("parent = %v", created.Parent)
	}
	if created.Body != "Discovered while working on #1" {
		t.Errorf("body = %q", created.Body)
	}
}

func TestCreateBodyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(path, []byte("### Where\n\nhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := newFake(issue(9, "Source", "P2", "task"))
	app, _, _ := newApp(f)
	if err := app.Create(ctx, CreateOpts{Title: "T", Type: "bug", BodyFile: path, DiscoveredFrom: 9}); err != nil {
		t.Fatal(err)
	}
	body := f.byNumber(101).Body
	if !strings.Contains(body, "here") || !strings.HasSuffix(body, "Discovered while working on #9") {
		t.Errorf("body = %q", body)
	}
}

func TestCreateSections(t *testing.T) {
	f := newFake()
	app, _, _ := newApp(f)
	err := app.Create(ctx, CreateOpts{
		Title: "T", Type: "task",
		Sections: conventions.Sections{
			Goal:     "Ship the thing",
			Approach: "Carefully",
			DoneWhen: []string{"tests pass", "docs updated"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "### Goal\n\nShip the thing\n\n### Approach\n\nCarefully\n\n### Done when\n\n- [ ] tests pass\n- [ ] docs updated"
	if got := f.byNumber(101).Body; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestCreateSectionsWithDiscoveredFrom(t *testing.T) {
	f := newFake(issue(7, "Source", "P2", "task"))
	app, _, _ := newApp(f)
	err := app.Create(ctx, CreateOpts{
		Title: "T", Type: "bug", DiscoveredFrom: 7,
		Sections: conventions.Sections{Problem: "It breaks"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "### Problem\n\nIt breaks\n\nDiscovered while working on #7"
	if got := f.byNumber(101).Body; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

// usageError asserts a usage refusal carrying the given message fragment,
// so a rule firing the wrong branch's message can't pass as the right one.
func usageError(t *testing.T, err error, wantMsg string) {
	t.Helper()
	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected ExitError, got %v", err)
	}
	if exitErr.Code != ExitUsage {
		t.Errorf("exit code = %d (%s), want %d", exitErr.Code, exitErr.Message, ExitUsage)
	}
	if !strings.Contains(exitErr.Message, wantMsg) {
		t.Errorf("message = %q, want substring %q", exitErr.Message, wantMsg)
	}
}

func TestCreateSectionValidation(t *testing.T) {
	f := newFake()
	app, _, _ := newApp(f)
	sections := conventions.Sections{Goal: "G"}
	usageError(t, app.Create(ctx, CreateOpts{Title: "T", Type: "task",
		Sections: conventions.Sections{Problem: "P", Goal: "G"}}), "--problem and --goal are mutually exclusive")
	usageError(t, app.Create(ctx, CreateOpts{Title: "T", Type: "task",
		Sections: conventions.Sections{Fix: "F", Approach: "A"}}), "--fix and --approach are mutually exclusive")
	usageError(t, app.Create(ctx, CreateOpts{Title: "T", Type: "task",
		Sections: sections, BodyFile: "f"}), "section flags")
	usageError(t, app.Create(ctx, CreateOpts{Title: "T", Type: "task",
		Sections: sections, Edit: true}), "section flags")
	usageError(t, app.Create(ctx, CreateOpts{Title: "T", Type: "task",
		Sections: conventions.Sections{DoneWhen: []string{" "}}}), "--done-when items cannot be empty")
	if len(f.calls) != 0 {
		t.Errorf("refused creates still called the API: %v", f.calls)
	}
}

func TestCreateBodyFileMissing(t *testing.T) {
	app, _, _ := newApp(newFake())
	err := app.Create(ctx, CreateOpts{Title: "T", Type: "bug", BodyFile: "/nonexistent"})
	exitCode(t, err, ExitGeneric)
}

func TestCreateEdit(t *testing.T) {
	f := newFake()
	app, _, _ := newApp(f)
	var seeded string
	app.Edit = func(initial string) (string, error) {
		seeded = initial
		return "### Where\n\nfilled in\n\n### Problem\n\n\n### Fix\n\n\n### Done when\n\n- [ ] \n", nil
	}
	if err := app.Create(ctx, CreateOpts{Title: "T", Type: "bug", Edit: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seeded, "### Problem") {
		t.Errorf("editor not seeded with bug template: %q", seeded)
	}
	body := f.byNumber(101).Body
	if !strings.Contains(body, "filled in") || strings.Contains(body, "Problem") {
		t.Errorf("empty sections not stripped: %q", body)
	}
}

func TestCreateEditUnavailable(t *testing.T) {
	app, _, _ := newApp(newFake())
	exitCode(t, app.Create(ctx, CreateOpts{Title: "T", Type: "bug", Edit: true}), ExitGeneric)
}

func TestCreateJSON(t *testing.T) {
	f := newFake()
	app, out, _ := newApp(f)
	app.JSON = true
	if err := app.Create(ctx, CreateOpts{Title: "T", Type: "bug"}); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["number"].(float64) != 101 || got["type"] != "bug" {
		t.Errorf("JSON = %s", out.String())
	}
}

func TestStartClaims(t *testing.T) {
	f := newFake(issue(1, "Work", "P2", "bug"))
	app, out, errOut := newApp(f)
	if err := app.Start(ctx, 1, StartOpts{}); err != nil {
		t.Fatal(err)
	}
	i := f.byNumber(1)
	if !slices.Contains(i.Labels, "in-progress") || !slices.Contains(i.Assignees, "me") {
		t.Errorf("claim not applied: labels=%v assignees=%v", i.Labels, i.Assignees)
	}
	if !strings.Contains(out.String(), "started #1") {
		t.Errorf("output = %q", out.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("unexpected warnings: %q", errOut.String())
	}
}

// start --show is the claim-and-read path: the claim confirmation followed by
// the same detail `show` renders, taken from the post-claim re-read rather
// than a further fetch.
func TestStartShowRendersClaimedDetail(t *testing.T) {
	i := issue(1, "Work", "P2", "bug")
	i.Body = "### Goal\n\nDo the thing."
	i.Comments = []model.Comment{{Author: "alice", Body: "context here"}}
	i.CommentsTotal = 1
	i.BlockedBy = []model.Ref{{Number: 9, State: "CLOSED"}}
	f := newFake(i)
	app, out, errOut := newApp(f)
	if err := app.Start(ctx, 1, StartOpts{Show: true}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"started #1", "### Goal", "Do the thing.", "blocked by: #9", "@alice", "context here", "assignee: @me"} {
		if !strings.Contains(got, want) {
			t.Errorf("start --show output missing %q:\n%s", want, got)
		}
	}
	if !slices.Contains(f.byNumber(1).Assignees, "me") {
		t.Errorf("claim not applied: %v", f.byNumber(1).Assignees)
	}
	if errOut.Len() != 0 {
		t.Errorf("unexpected warnings: %q", errOut.String())
	}
	// The detail comes from the post-claim read already made: one before the
	// guard, one after the claim, and nothing for the detail view.
	if n := f.callCounts["GetIssue"]; n != 2 {
		t.Errorf("GetIssue calls = %d, want 2 (calls: %v)", n, f.calls)
	}
}

// The detail view carries the same text-findings annotations as show.
func TestStartShowAnnotatesTextFindings(t *testing.T) {
	i := issue(1, "Work", "P2", "bug")
	i.Body = "Pay p\u0430ypal\u200b now"
	app, out, _ := newApp(newFake(i))
	if err := app.Start(ctx, 1, StartOpts{Show: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "text findings:") {
		t.Errorf("start --show should annotate text findings like show:\n%s", out.String())
	}
}

// Without --show, the default text summary is unchanged: no body, no detail.
func TestStartDefaultOmitsDetail(t *testing.T) {
	i := issue(1, "Work", "P2", "bug")
	i.Body = "### Goal\n\nDo the thing."
	app, out, _ := newApp(newFake(i))
	if err := app.Start(ctx, 1, StartOpts{}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "started #1: Work\n" {
		t.Errorf("default output = %q", out.String())
	}
}

// --json already is the full issue; --show must not change its schema or
// append text to the stream.
func TestStartShowWithJSONKeepsSchema(t *testing.T) {
	i := issue(1, "Work", "P2", "bug")
	i.Body = "body text"
	plain, plainOut, _ := newApp(newFake(issue(1, "Work", "P2", "bug")))
	plain.JSON = true
	plain.Client.(*fakeClient).byNumber(1).Body = "body text"
	if err := plain.Start(ctx, 1, StartOpts{}); err != nil {
		t.Fatal(err)
	}
	app, out, _ := newApp(newFake(i))
	app.JSON = true
	if err := app.Start(ctx, 1, StartOpts{Show: true}); err != nil {
		t.Fatal(err)
	}
	if out.String() != plainOut.String() {
		t.Errorf("--json --show differs from --json:\n%s\nvs\n%s", out.String(), plainOut.String())
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("not a single JSON object: %v\n%s", err, out.String())
	}
	if got["body"] != "body text" {
		t.Errorf("body = %v", got["body"])
	}
}

// A refused claim never looks successful under --show: no detail on stdout,
// the same exit codes, and no mutation.
func TestStartShowRefusalsStayRefusals(t *testing.T) {
	theirs := issue(1, "Taken", "P2", "bug")
	theirs.Assignees = []string{"other"}
	theirs.Body = "secret body"
	mine := issue(2, "Mine", "P2", "bug")
	mine.Assignees = []string{"me"}
	mine.Body = "my body"
	f := newFake(theirs, mine)
	app, out, _ := newApp(f)
	exitCode(t, app.Start(ctx, 1, StartOpts{Show: true}), ExitClaimed)
	exitCode(t, app.Start(ctx, 2, StartOpts{Show: true}), ExitClaimedByYou)
	if out.Len() != 0 {
		t.Errorf("refused start --show wrote to stdout: %q", out.String())
	}
	if got := f.byNumber(1).Assignees; !reflect.DeepEqual(got, []string{"other"}) {
		t.Errorf("refused start mutated #1: %v", got)
	}
}

func TestStartRefusesClaimed(t *testing.T) {
	assigned := issue(1, "Taken", "P2", "bug")
	assigned.Assignees = []string{"other"}
	f := newFake(assigned, issue(2, "Labeled", "P2", "bug", "in-progress"))
	app, _, _ := newApp(f)
	err := app.Start(ctx, 1, StartOpts{})
	exitCode(t, err, ExitClaimed)
	if !strings.Contains(err.Error(), "@other") {
		t.Errorf("message should name the claimant: %v", err)
	}
	exitCode(t, app.Start(ctx, 2, StartOpts{}), ExitClaimed)
	// The refusal must come before any mutation: the claimant keeps the
	// issue exactly as it was.
	one := f.byNumber(1)
	if !reflect.DeepEqual(one.Assignees, []string{"other"}) || slices.Contains(one.Labels, "in-progress") {
		t.Errorf("refused start mutated #1: labels=%v assignees=%v", one.Labels, one.Assignees)
	}
	if got := f.byNumber(2).Assignees; len(got) != 0 {
		t.Errorf("refused start assigned #2: %v", got)
	}
}

// TestStartRefusesOwnClaim covers the split in the claim refusal: my own
// claim is a distinct exit code from someone else's, because the correct
// response differs (resume vs pick the next ready item).
func TestStartRefusesOwnClaim(t *testing.T) {
	mine := issue(1, "Mine", "P2", "bug", "in-progress")
	mine.Assignees = []string{"me"}
	shared := issue(2, "Shared", "P2", "bug")
	shared.Assignees = []string{"other", "me"}
	f := newFake(mine, shared)
	app, _, errOut := newApp(f)

	err := app.Start(ctx, 1, StartOpts{})
	exitCode(t, err, ExitClaimedByYou)
	if ExitClaimedByYou == ExitClaimed {
		t.Fatal("the two claim refusals must use different exit codes")
	}
	if !strings.Contains(err.Error(), "claimed by you") || !strings.Contains(err.Error(), "@me") {
		t.Errorf("message should say the claim is yours and name the assignee: %v", err)
	}
	// A co-assignment still counts as mine: I am one of the claimants, so
	// resuming — not skipping to the next item — is the right move.
	exitCode(t, app.Start(ctx, 2, StartOpts{}), ExitClaimedByYou)
	// Refusing reports; it must not mutate either issue.
	if got := f.byNumber(2).Assignees; !reflect.DeepEqual(got, []string{"other", "me"}) {
		t.Errorf("refused start mutated #2: %v", got)
	}
	if slices.Contains(f.byNumber(2).Labels, "in-progress") {
		t.Errorf("refused start labeled #2: %v", f.byNumber(2).Labels)
	}
	if errOut.Len() != 0 {
		t.Errorf("unexpected warnings: %q", errOut.String())
	}
}

// TestStartClaimRefusalWithoutViewer covers the degraded path: the refusal
// itself is not in doubt when the viewer lookup fails, so start keeps the
// conservative exit 3 and says why it could not tell.
func TestStartClaimRefusalWithoutViewer(t *testing.T) {
	mine := issue(1, "Mine", "P2", "bug")
	mine.Assignees = []string{"me"}
	f := newFake(mine)
	f.failOn["Viewer"] = errors.New("network down")
	app, _, errOut := newApp(f)

	err := app.Start(ctx, 1, StartOpts{})
	exitCode(t, err, ExitClaimed)
	if !strings.Contains(errOut.String(), "network down") {
		t.Errorf("degraded refusal should warn why: %q", errOut.String())
	}
	if got := f.byNumber(1).Assignees; !reflect.DeepEqual(got, []string{"me"}) {
		t.Errorf("refused start mutated #1: %v", got)
	}
}

func TestStartForceSteals(t *testing.T) {
	assigned := issue(1, "Taken", "P2", "bug", "in-progress")
	assigned.Assignees = []string{"other"}
	f := newFake(assigned)
	app, _, _ := newApp(f)
	if err := app.Start(ctx, 1, StartOpts{Force: true}); err != nil {
		t.Fatal(err)
	}
	i := f.byNumber(1)
	if !reflect.DeepEqual(i.Assignees, []string{"me"}) {
		t.Errorf("assignees = %v", i.Assignees)
	}
}

func TestStartGuards(t *testing.T) {
	closed := issue(1, "Closed", "P2", "bug")
	closed.State = "CLOSED"
	epicIssue := issue(2, "Epic: e", "P2")
	epicIssue.SubIssues = []model.Ref{{Number: 1, State: "CLOSED"}}
	f := newFake(closed, epicIssue, issue(3, "Untriaged"))
	app, _, _ := newApp(f)
	if err := app.Start(ctx, 1, StartOpts{}); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("closed err = %v", err)
	}
	if err := app.Start(ctx, 2, StartOpts{}); err == nil || !strings.Contains(err.Error(), "epic") {
		t.Errorf("epic err = %v", err)
	}
	exitCode(t, app.Start(ctx, 3, StartOpts{}), ExitUsage)
	exitCode(t, app.Start(ctx, 3, StartOpts{Priority: "P7"}), ExitUsage)
}

func TestStartUntriagedWithPriority(t *testing.T) {
	f := newFake(issue(3, "Untriaged"))
	app, _, _ := newApp(f)
	if err := app.Start(ctx, 3, StartOpts{Priority: "P1"}); err != nil {
		t.Fatal(err)
	}
	i := f.byNumber(3)
	if !slices.Contains(i.Labels, "P1") {
		t.Errorf("priority not applied: %v", i.Labels)
	}
}

func TestStartSwapsPriority(t *testing.T) {
	f := newFake(issue(1, "Work", "P3", "bug"))
	app, _, _ := newApp(f)
	if err := app.Start(ctx, 1, StartOpts{Priority: "P0"}); err != nil {
		t.Fatal(err)
	}
	i := f.byNumber(1)
	if slices.Contains(i.Labels, "P3") || !slices.Contains(i.Labels, "P0") {
		t.Errorf("labels = %v", i.Labels)
	}
}

func TestStartRaceWarning(t *testing.T) {
	f := newFake(issue(1, "Work", "P2", "bug"))
	f.rivalOnAssign = "rival"
	app, _, errOut := newApp(f)
	if err := app.Start(ctx, 1, StartOpts{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "claim may have raced") {
		t.Errorf("stderr = %q", errOut.String())
	}
}

// #107: the messages below bypass the renderer, so their GitHub-derived
// titles and logins need sanitizing at the call site.
func TestStartNeutralizesHostileTitle(t *testing.T) {
	f := newFake(issue(1, "Work "+hostile, "P2", "bug"))
	app, out, errOut := newApp(f)
	if err := app.Start(ctx, 1, StartOpts{}); err != nil {
		t.Fatal(err)
	}
	assertNeutralized(t, "start success line", out.String())
	assertNeutralized(t, "start warnings", errOut.String())
	// Neutralized, not dropped: the offending title stays visible.
	if !strings.Contains(out.String(), "started #1: Work ") {
		t.Errorf("sanitizing dropped the title text: %q", out.String())
	}
}

func TestClaimRefusalNeutralizesHostileAssignee(t *testing.T) {
	assigned := issue(1, "Taken", "P2", "bug")
	assigned.Assignees = []string{"other" + hostile}
	f := newFake(assigned)
	app, _, errOut := newApp(f)
	err := app.Start(ctx, 1, StartOpts{})
	exitCode(t, err, ExitClaimed)
	assertNeutralized(t, "claim refusal message", err.Error())
	assertNeutralized(t, "claim refusal warnings", errOut.String())
}

func TestOwnClaimRefusalNeutralizesHostileAssignee(t *testing.T) {
	mine := issue(1, "Mine", "P2", "bug")
	mine.Assignees = []string{"me", "other" + hostile}
	f := newFake(mine)
	app, _, errOut := newApp(f)
	err := app.Start(ctx, 1, StartOpts{})
	exitCode(t, err, ExitClaimedByYou)
	assertNeutralized(t, "own-claim refusal message", err.Error())
	assertNeutralized(t, "own-claim refusal warnings", errOut.String())
}

func TestStartRaceWarningNeutralizesHostileAssignee(t *testing.T) {
	f := newFake(issue(1, "Work", "P2", "bug"))
	f.rivalOnAssign = "rival" + hostile
	app, _, errOut := newApp(f)
	if err := app.Start(ctx, 1, StartOpts{}); err != nil {
		t.Fatal(err)
	}
	assertNeutralized(t, "race warning", errOut.String())
	if !strings.Contains(errOut.String(), "claim may have raced") {
		t.Errorf("stderr = %q", errOut.String())
	}
}

func TestReopenNeutralizesHostileReleasedClaimant(t *testing.T) {
	closed := issue(1, "Work", "P2", "bug", model.InProgressLabel)
	closed.State = "CLOSED"
	closed.Assignees = []string{"alice" + hostile}
	f := newFake(closed)
	app, out, _ := newApp(f)
	if err := app.Reopen(ctx, 1, "r"); err != nil {
		t.Fatal(err)
	}
	assertNeutralized(t, "reopen message", out.String())
	if !strings.Contains(out.String(), "released @alice") {
		t.Errorf("sanitizing dropped the login text: %q", out.String())
	}
}

func TestSetValidation(t *testing.T) {
	f := newFake(issue(1, "Work", "P2", "bug"))
	app, _, _ := newApp(f)
	exitCode(t, app.Set(ctx, 1, SetOpts{}), ExitUsage)
	exitCode(t, app.Set(ctx, 1, SetOpts{Priority: "P9"}), ExitUsage)
	exitCode(t, app.Set(ctx, 1, SetOpts{Type: "story"}), ExitUsage)
	exitCode(t, app.Set(ctx, 1, SetOpts{Parent: 2, NoParent: true}), ExitUsage)
	// --closed modifies how a change is applied; it is not itself a change,
	// so it must not satisfy the "nothing to change" check.
	exitCode(t, app.Set(ctx, 1, SetOpts{AllowClosed: true}), ExitUsage)
}

// TestSetRejectsAnExplicitlyEmptyTitle: --title "" was indistinguishable from
// no --title, so it reported "nothing to change" instead of naming the flag.
func TestSetRejectsAnExplicitlyEmptyTitle(t *testing.T) {
	for _, title := range []string{"", "   "} {
		f := newFake(issue(1, "Work", "P2", "bug"))
		app, _, _ := newApp(f)
		// Paired with a valid change, the bad title must still refuse the lot.
		err := app.Set(ctx, 1, SetOpts{Priority: "P0", Title: title, TitleSet: true})
		exitCode(t, err, ExitUsage)
		if !strings.Contains(err.Error(), "--title") || strings.Contains(err.Error(), "nothing to change") {
			t.Errorf("title %q: err = %v, want a --title usage error", title, err)
		}
		if got := f.byNumber(1); got.Title != "Work" || !slices.Contains(got.Labels, "P2") {
			t.Errorf("title %q: refused set still mutated the issue: %+v", title, got)
		}
	}
}

// TestBlankTitlesAreRefusedWhoeverCalls: the check lives in validateTitle, so
// a caller that never sets TitleSet, and create, are covered too.
func TestBlankTitlesAreRefusedWhoeverCalls(t *testing.T) {
	f := newFake(issue(1, "Work", "P2", "bug"))
	app, _, _ := newApp(f)
	exitCode(t, app.Set(ctx, 1, SetOpts{Title: " ", TitleSet: true}), ExitUsage)
	exitCode(t, app.Create(ctx, CreateOpts{Type: "task", Title: "   ", Sections: conventions.Sections{Goal: "g", DoneWhen: []string{"d"}}}), ExitUsage)
	exitCode(t, app.EpicCreate(ctx, EpicCreateOpts{Title: "  "}), ExitUsage)
	if got := f.byNumber(1).Title; got != "Work" {
		t.Errorf("title = %q after refused edits", got)
	}
}

func TestSetSwapsLabels(t *testing.T) {
	f := newFake(issue(1, "Work", "P2", "bug", "old-area"))
	app, out, _ := newApp(f)
	err := app.Set(ctx, 1, SetOpts{
		Priority: "P0", Type: "task",
		AddAreas: []string{"new-area"}, RemoveAreas: []string{"old-area"},
		Title: "Renamed",
	})
	if err != nil {
		t.Fatal(err)
	}
	i := f.byNumber(1)
	want := []string{"P0", "task", "new-area"}
	slices.Sort(i.Labels)
	slices.Sort(want)
	if !reflect.DeepEqual(i.Labels, want) {
		t.Errorf("labels = %v", i.Labels)
	}
	if i.Title != "Renamed" {
		t.Errorf("title = %q", i.Title)
	}
	if !strings.Contains(out.String(), "updated #1") {
		t.Errorf("output = %q", out.String())
	}
}

func TestSetIdempotentLabels(t *testing.T) {
	f := newFake(issue(1, "Work", "P2", "bug"))
	app, _, _ := newApp(f)
	if err := app.Set(ctx, 1, SetOpts{Priority: "P2", Type: "bug"}); err != nil {
		t.Fatal(err)
	}
	for _, call := range f.calls {
		if strings.HasPrefix(call, "AddLabels") || strings.HasPrefix(call, "RemoveLabel") {
			t.Errorf("unneeded label mutation: %s", call)
		}
	}
}

func TestSetParent(t *testing.T) {
	epicA := issue(10, "Epic: a", "P2")
	epicA.SubIssues = []model.Ref{{Number: 1, State: "OPEN"}}
	child := issue(1, "Work", "P2", "bug")
	child.Parent = &model.Ref{Number: 10, State: "OPEN"}
	epicB := issue(20, "Epic: b", "P2")
	f := newFake(epicA, child, epicB)
	app, _, _ := newApp(f)
	if err := app.Set(ctx, 1, SetOpts{Parent: 20}); err != nil {
		t.Fatal(err)
	}
	if f.byNumber(1).Parent.Number != 20 {
		t.Errorf("parent = %v", f.byNumber(1).Parent)
	}
	if len(f.byNumber(10).SubIssues) != 0 {
		t.Errorf("old parent still has child: %v", f.byNumber(10).SubIssues)
	}

	if err := app.Set(ctx, 1, SetOpts{NoParent: true}); err != nil {
		t.Fatal(err)
	}
	if f.byNumber(1).Parent != nil {
		t.Errorf("parent not cleared: %v", f.byNumber(1).Parent)
	}
}

func TestSetNoParentNoop(t *testing.T) {
	f := newFake(issue(1, "Work", "P2", "bug"))
	app, _, errOut := newApp(f)
	if err := app.Set(ctx, 1, SetOpts{NoParent: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "no parent") {
		t.Errorf("stderr = %q", errOut.String())
	}
}

func TestSetBodyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(path, []byte("### Goal\n\nrewritten\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := newFake(issue(1, "Work", "P3", "bug"))
	f.byNumber(1).Body = "### Goal\n\nstale\n"
	app, out, _ := newApp(f)
	// Body edits combine with the label/title edits in one call.
	if err := app.Set(ctx, 1, SetOpts{Priority: "P1", Title: "Renamed", BodyFile: path}); err != nil {
		t.Fatal(err)
	}
	i := f.byNumber(1)
	if i.Body != "### Goal\n\nrewritten\n" {
		t.Errorf("body = %q", i.Body)
	}
	if i.Title != "Renamed" || !slices.Contains(i.Labels, "P1") {
		t.Errorf("title = %q labels = %v", i.Title, i.Labels)
	}
	if !strings.Contains(out.String(), "updated #1") {
		t.Errorf("output = %q", out.String())
	}
}

func TestSetBodyFileRefusesBlanking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.md")
	if err := os.WriteFile(path, []byte("  \n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := newFake(issue(1, "Work", "P2", "bug"))
	f.byNumber(1).Body = "### Goal\n\nkeep me\n"
	app, _, _ := newApp(f)
	exitCode(t, app.Set(ctx, 1, SetOpts{Priority: "P0", BodyFile: path}), ExitUsage)
	if f.byNumber(1).Body != "### Goal\n\nkeep me\n" {
		t.Errorf("body was blanked: %q", f.byNumber(1).Body)
	}
	if len(f.calls) != 0 {
		t.Errorf("refused set still called the API: %v", f.calls)
	}
}

func TestSetBodyFileMissingIsCheckedBeforeMutating(t *testing.T) {
	f := newFake(issue(1, "Work", "P2", "bug"))
	app, _, _ := newApp(f)
	// The unreadable file must be caught before --priority lands, so the
	// failure isn't a half-applied edit.
	exitCode(t, app.Set(ctx, 1, SetOpts{Priority: "P0", BodyFile: "/nonexistent"}), ExitGeneric)
	if len(f.calls) != 0 {
		t.Errorf("refused set still called the API: %v", f.calls)
	}
}

func TestSetBodyFailurePropagates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(path, []byte("### Goal\n\nnew\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := newFake(issue(1, "Work", "P2", "bug"))
	f.failOn["EditBody"] = errors.New("boom")
	app, _, _ := newApp(f)
	err := app.Set(ctx, 1, SetOpts{BodyFile: path})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v", err)
	}
}

// TestSetRefusesClosedIssue covers #39: an issue closed by a merge mid-session
// used to absorb a later edit without any signal, and the staleness only
// surfaced by accident. The refusal has to name the close state (so the caller
// can tell "closed as completed" from "closed as not planned") and land before
// any mutation, since Set applies several in sequence.
func TestSetRefusesClosedIssue(t *testing.T) {
	closed := issue(1, "Work", "P2", "bug")
	closed.State = "CLOSED"
	closed.StateReason = "COMPLETED"
	f := newFake(closed)
	app, _, _ := newApp(f)

	err := app.Set(ctx, 1, SetOpts{Priority: "P0", Title: "Renamed"})
	if err == nil || !strings.Contains(err.Error(), "#1 is closed (completed)") {
		t.Errorf("err = %v", err)
	}
	// The remedy belongs in the message: an agent that reads the refusal
	// should not have to go looking through --help for the override.
	if err != nil && !strings.Contains(err.Error(), "--closed") {
		t.Errorf("refusal omits the override flag: %v", err)
	}
	i := f.byNumber(1)
	if i.Title != "Work" {
		t.Errorf("title mutated on a closed issue: %q", i.Title)
	}
	if !reflect.DeepEqual(i.Labels, []string{"P2", "bug"}) {
		t.Errorf("labels mutated on a closed issue: %v", i.Labels)
	}
	for _, call := range f.calls {
		if strings.HasPrefix(call, "AddLabels") || strings.HasPrefix(call, "RemoveLabel") ||
			strings.HasPrefix(call, "EditTitle") || strings.HasPrefix(call, "EditBody") {
			t.Errorf("refused set still mutated: %s", call)
		}
	}
}

func TestCloseNotPlanned(t *testing.T) {
	f := newFake(issue(1, "Work", "P2", "bug"))
	app, out, _ := newApp(f)
	if err := app.Close(ctx, 1, "wontfix: superseded", false, 0); err != nil {
		t.Fatal(err)
	}
	i := f.byNumber(1)
	if i.State != "CLOSED" || i.StateReason != "NOT_PLANNED" {
		t.Errorf("state = %s %s", i.State, i.StateReason)
	}
	if !reflect.DeepEqual(f.comments[1], []string{"wontfix: superseded"}) {
		t.Errorf("comments = %v", f.comments[1])
	}
	if !strings.Contains(out.String(), "closed #1 (not planned)") {
		t.Errorf("output = %q", out.String())
	}
}

func TestCloseCompleted(t *testing.T) {
	f := newFake(issue(1, "Work", "P2", "bug"))
	app, _, _ := newApp(f)
	if err := app.Close(ctx, 1, "done out of band", true, 0); err != nil {
		t.Fatal(err)
	}
	if f.byNumber(1).StateReason != "COMPLETED" {
		t.Errorf("reason = %s", f.byNumber(1).StateReason)
	}
}

func TestCloseDuplicate(t *testing.T) {
	f := newFake(issue(1, "Work", "P2", "bug"), issue(5, "Original", "P2", "bug"))
	app, _, _ := newApp(f)
	if err := app.Close(ctx, 1, "", false, 5); err != nil {
		t.Fatal(err)
	}
	if f.byNumber(1).StateReason != "DUPLICATE" {
		t.Errorf("reason = %s", f.byNumber(1).StateReason)
	}
	if !reflect.DeepEqual(f.comments[1], []string{"Duplicate of #5"}) {
		t.Errorf("comments = %v", f.comments[1])
	}
}

func TestCloseValidation(t *testing.T) {
	f := newFake(issue(1, "Work", "P2", "bug"))
	app, _, _ := newApp(f)
	exitCode(t, app.Close(ctx, 1, "r", true, 5), ExitUsage)
	exitCode(t, app.Close(ctx, 1, "", false, 0), ExitUsage)
	if len(f.calls) != 0 || len(f.comments[1]) != 0 {
		t.Errorf("refused close still touched the API: calls=%v comments=%v", f.calls, f.comments[1])
	}
	closed := issue(2, "Closed", "P2", "bug")
	closed.State = "CLOSED"
	f2 := newFake(closed)
	app2, _, _ := newApp(f2)
	if err := app2.Close(ctx, 2, "r", false, 0); err == nil || !strings.Contains(err.Error(), "already closed") {
		t.Errorf("err = %v", err)
	}
	// The already-closed guard runs before the reason comment posts:
	// re-closing must not spam a comment onto the closed issue.
	if len(f2.comments[2]) != 0 {
		t.Errorf("refused close still commented: %v", f2.comments[2])
	}
}

// TestCloseReportsExistingCloseState covers #39: close already refused an
// already-closed issue, but said only "already closed" — which leaves the
// caller unable to tell a completed issue from one closed as not planned, the
// distinction that decides whether re-closing was a mistake at all.
func TestCloseReportsExistingCloseState(t *testing.T) {
	completed := issue(1, "Done", "P2", "bug")
	completed.State = "CLOSED"
	completed.StateReason = "COMPLETED"
	// GitHub records no state reason on issues closed before the field
	// existed, and reports none for them.
	reasonless := issue(2, "Ancient", "P2", "bug")
	reasonless.State = "CLOSED"
	f := newFake(completed, reasonless)
	app, _, _ := newApp(f)

	err := app.Close(ctx, 1, "r", false, 0)
	if err == nil || !strings.Contains(err.Error(), "#1 is already closed (completed)") {
		t.Errorf("err = %v", err)
	}
	err = app.Close(ctx, 2, "r", false, 0)
	if err == nil || !strings.Contains(err.Error(), "#2 is already closed") {
		t.Errorf("err = %v", err)
	}
	if err != nil && strings.Contains(err.Error(), "()") {
		t.Errorf("absent state reason rendered as empty parentheses: %v", err)
	}
	if len(f.comments[1]) != 0 || len(f.comments[2]) != 0 {
		t.Errorf("refused close still commented: %v %v", f.comments[1], f.comments[2])
	}
}

// TestReopen covers #40: reopen is close's inverse, so the reason comment and
// the state change land in one call the same way close's do.
func TestReopen(t *testing.T) {
	closed := issue(1, "Work", "P2", "bug")
	closed.State = "CLOSED"
	closed.StateReason = "NOT_PLANNED"
	f := newFake(closed)
	app, out, _ := newApp(f)
	if err := app.Reopen(ctx, 1, "#24 stalled; picking this back up"); err != nil {
		t.Fatal(err)
	}
	i := f.byNumber(1)
	if i.State != "OPEN" || i.StateReason != "REOPENED" {
		t.Errorf("state = %s %s", i.State, i.StateReason)
	}
	if !reflect.DeepEqual(f.comments[1], []string{"#24 stalled; picking this back up"}) {
		t.Errorf("comments = %v", f.comments[1])
	}
	if !strings.Contains(out.String(), "reopened #1") {
		t.Errorf("output = %q", out.String())
	}
	if strings.Contains(out.String(), "released") {
		t.Errorf("unclaimed issue reported a released claim: %q", out.String())
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "RemoveLabel") || strings.HasPrefix(c, "RemoveAssignees") {
			t.Errorf("unclaimed issue saw a claim-release call: %s", c)
		}
	}
}

// A reopened issue is not retriaged, so it rejoins the normal read path:
// list shows it again with whatever labels it already had.
func TestReopenRestoresIssueToList(t *testing.T) {
	closed := issue(1, "Work", "P2", "bug")
	closed.State = "CLOSED"
	closed.StateReason = "NOT_PLANNED"
	f := newFake(closed)
	app, out, _ := newApp(f)
	if err := app.List(ctx, ListOpts{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "#1") {
		t.Fatalf("closed issue listed before reopen: %q", out.String())
	}
	if err := app.Reopen(ctx, 1, "back on the plate"); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := app.List(ctx, ListOpts{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "#1") || !strings.Contains(out.String(), "Work") {
		t.Errorf("reopened issue missing from list: %q", out.String())
	}
}

// Reopening an open issue is the state the caller asked for, so it reports
// that and stops — erroring would make an idempotent retry look like a
// failure, and commenting would leave a stray note on an untouched issue.
func TestReopenOpenIssueIsANoOp(t *testing.T) {
	f := newFake(issue(1, "Work", "P2", "bug"))
	app, out, _ := newApp(f)
	if err := app.Reopen(ctx, 1, "r"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "#1 is already open") {
		t.Errorf("output = %q", out.String())
	}
	if len(f.comments[1]) != 0 {
		t.Errorf("no-op reopen still commented: %v", f.comments[1])
	}
	assertOnlyReads(t, f)
}

func TestReopenValidation(t *testing.T) {
	closed := issue(1, "Work", "P2", "bug")
	closed.State = "CLOSED"
	f := newFake(closed)
	app, _, _ := newApp(f)
	exitCode(t, app.Reopen(ctx, 1, ""), ExitUsage)
	if len(f.calls) != 0 || len(f.comments[1]) != 0 {
		t.Errorf("refused reopen still touched the API: calls=%v comments=%v", f.calls, f.comments[1])
	}
	// A failed pre-read is reported as-is: nothing has been written yet, so
	// there is no partial state to describe.
	f.failOn["GetIssue"] = errors.New("read boom")
	if err := app.Reopen(ctx, 1, "r"); err == nil || !strings.Contains(err.Error(), "read boom") {
		t.Errorf("err = %v", err)
	}
	// A failed comment likewise: the state change never ran.
	delete(f.failOn, "GetIssue")
	f.failOn["Comment"] = errors.New("comment boom")
	if err := app.Reopen(ctx, 1, "r"); err == nil || !strings.Contains(err.Error(), "comment boom") {
		t.Errorf("err = %v", err)
	}
	if f.byNumber(1).State != "CLOSED" {
		t.Errorf("state = %s, want CLOSED", f.byNumber(1).State)
	}
}

// The reason comment posts before the state change, so a failure between the
// two has to say the comment already landed — a bare retry would post it
// twice, exactly as close's does.
func TestReopenReportsPostedCommentWhenReopenFails(t *testing.T) {
	closed := issue(1, "Work", "P2", "bug")
	closed.State = "CLOSED"
	f := newFake(closed)
	f.failOn["ReopenIssue"] = errors.New("boom")
	app, _, _ := newApp(f)
	err := app.Reopen(ctx, 1, "back on")
	if err == nil || !strings.Contains(err.Error(), "posted the reason comment on #1") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "a retry will comment again") {
		t.Errorf("err = %v", err)
	}
	if f.byNumber(1).State != "CLOSED" {
		t.Errorf("state = %s, want CLOSED", f.byNumber(1).State)
	}
}

// A claim says someone is actively working the issue, and the close ended
// that work. Neither close nor a PR merge clears the in-progress label or the
// assignee, so a reopened issue would otherwise carry a claim that makes the
// exit-code contract lie: exit 5 tells the old owner to resume merged work,
// exit 3 tells everyone else it is taken. Reopen releases the claim and says
// so; whoever wants it back runs start.
func TestReopenReleasesStaleClaim(t *testing.T) {
	closed := issue(1, "Work", "P2", "bug", model.InProgressLabel)
	closed.State = "CLOSED"
	closed.StateReason = "COMPLETED"
	closed.Assignees = []string{"alice"}
	f := newFake(closed)
	app, out, _ := newApp(f)
	if err := app.Reopen(ctx, 1, "regressed in v1.2"); err != nil {
		t.Fatal(err)
	}
	i := f.byNumber(1)
	if i.State != "OPEN" {
		t.Errorf("state = %s, want OPEN", i.State)
	}
	if i.Claimed() {
		t.Errorf("reopened issue still claimed: labels=%v assignees=%v", i.Labels, i.Assignees)
	}
	// Releasing the claim is not a retriage: priority and type stay put.
	if !slices.Contains(i.Labels, "P2") || !slices.Contains(i.Labels, "bug") {
		t.Errorf("triage labels disturbed: %v", i.Labels)
	}
	if !strings.Contains(out.String(), "reopened #1 (released @alice's stale claim)") {
		t.Errorf("output = %q", out.String())
	}
	// Back in the pool: list no longer marks it in progress.
	out.Reset()
	if err := app.List(ctx, ListOpts{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "#1") || strings.Contains(out.String(), "in progress") {
		t.Errorf("list after reopen = %q", out.String())
	}
}

// The in-progress label alone is a claim too — start sets both, but the
// label can outlive the assignee — and the output names what was released.
func TestReopenReleasesLabelOnlyClaim(t *testing.T) {
	closed := issue(1, "Work", "P2", "bug", model.InProgressLabel)
	closed.State = "CLOSED"
	f := newFake(closed)
	app, out, _ := newApp(f)
	if err := app.Reopen(ctx, 1, "r"); err != nil {
		t.Fatal(err)
	}
	if f.byNumber(1).Claimed() {
		t.Errorf("still claimed: %v", f.byNumber(1).Labels)
	}
	if !strings.Contains(out.String(), "reopened #1 (released stale in-progress label)") {
		t.Errorf("output = %q", out.String())
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "RemoveAssignees") {
			t.Errorf("unassigned issue saw an assignee removal: %s", c)
		}
	}
}

// The reopen has landed by the time the claim release runs, so a failure
// there must say the issue is open but still claimed — a retry is a no-op
// reopen and will not clear it; start --force is the remedy.
func TestReopenReportsClaimLeftWhenReleaseFails(t *testing.T) {
	closed := issue(1, "Work", "P2", "bug", model.InProgressLabel)
	closed.State = "CLOSED"
	closed.Assignees = []string{"alice"}
	f := newFake(closed)
	f.failOn["RemoveAssignees"] = errors.New("boom")
	app, _, _ := newApp(f)
	err := app.Reopen(ctx, 1, "r")
	if err == nil || !strings.Contains(err.Error(), "reopened #1 but releasing its stale claim failed") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "still claimed") || !strings.Contains(err.Error(), "start --force") {
		t.Errorf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v", err)
	}
	if got := f.byNumber(1); got.State != "OPEN" || !got.Claimed() {
		t.Errorf("state = %s claimed = %v; want OPEN and still claimed", got.State, got.Claimed())
	}
	// The label goes first, so a failure there leaves both halves of the
	// claim in place and never reaches the assignee call.
	closed = issue(1, "Work", "P2", "bug", model.InProgressLabel)
	closed.State = "CLOSED"
	closed.Assignees = []string{"alice"}
	f = newFake(closed)
	f.failOn["RemoveLabel"] = errors.New("label boom")
	app, _, _ = newApp(f)
	err = app.Reopen(ctx, 1, "r")
	if err == nil || !strings.Contains(err.Error(), "still claimed") || !strings.Contains(err.Error(), "label boom") {
		t.Fatalf("err = %v", err)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "RemoveAssignees") {
			t.Errorf("assignee removal ran after the label removal failed: %s", c)
		}
	}
	if got := f.byNumber(1); !got.InProgress() || len(got.Assignees) != 1 {
		t.Errorf("claim partially released: labels=%v assignees=%v", got.Labels, got.Assignees)
	}
}

func TestBlock(t *testing.T) {
	f := newFake(issue(1, "A", "P2", "bug"), issue(2, "B", "P2", "bug"))
	app, out, _ := newApp(f)
	if err := app.Block(ctx, 1, 2, false); err != nil {
		t.Fatal(err)
	}
	i := f.byNumber(1)
	if len(i.BlockedBy) != 1 || i.BlockedBy[0].Number != 2 {
		t.Errorf("blockedBy = %v", i.BlockedBy)
	}
	if !strings.Contains(out.String(), "blocked #1 on #2") {
		t.Errorf("output = %q", out.String())
	}
}

func TestBlockRefusesCycle(t *testing.T) {
	b := issue(2, "B", "P2", "bug")
	b.BlockedBy = []model.Ref{{Number: 3, State: "OPEN"}}
	c := issue(3, "C", "P2", "bug")
	c.BlockedBy = []model.Ref{{Number: 1, State: "OPEN"}}
	f := newFake(issue(1, "A", "P2", "bug"), b, c)
	app, _, _ := newApp(f)
	err := app.Block(ctx, 1, 2, false)
	if err == nil || !strings.Contains(err.Error(), "cycle #1 → #2 → #3 → #1") {
		t.Errorf("err = %v", err)
	}
	if len(f.byNumber(1).BlockedBy) != 0 {
		t.Error("edge added despite refusal")
	}
}

func TestBlockRefusesSelfBlock(t *testing.T) {
	f := newFake(issue(1, "A", "P2", "bug"))
	app, _, _ := newApp(f)
	err := app.Block(ctx, 1, 1, false)
	if err == nil || !strings.Contains(err.Error(), "cycle #1 → #1") {
		t.Errorf("err = %v", err)
	}
	if len(f.byNumber(1).BlockedBy) != 0 {
		t.Error("self-edge added despite refusal")
	}
}

func TestBlockRefusesTwoCycle(t *testing.T) {
	// GitHub's API would reject this direct back-edge itself, but the
	// client-side check must catch it first so the refusal is uniform.
	b := issue(2, "B", "P2", "bug")
	b.BlockedBy = []model.Ref{{Number: 1, State: "OPEN"}}
	f := newFake(issue(1, "A", "P2", "bug"), b)
	app, _, _ := newApp(f)
	err := app.Block(ctx, 1, 2, false)
	if err == nil || !strings.Contains(err.Error(), "cycle #1 → #2 → #1") {
		t.Errorf("err = %v", err)
	}
	if len(f.byNumber(1).BlockedBy) != 0 {
		t.Error("edge added despite refusal")
	}
}

func TestBlockRefusesWhenCycleCheckUnverifiable(t *testing.T) {
	// #2's blocker list was capped, so the transitive cycle check from it is
	// blind to hidden blockers; Block must refuse rather than risk creating
	// a cycle GitHub won't catch.
	b := issue(2, "B", "P2", "bug")
	b.BlockedBy = []model.Ref{{Number: 3, State: "OPEN"}}
	b.BlockedByTotal = 25
	f := newFake(issue(1, "A", "P2", "bug"), b, issue(3, "C", "P2", "bug"))
	app, _, _ := newApp(f)
	err := app.Block(ctx, 1, 2, false)
	if err == nil || !strings.Contains(err.Error(), "cannot verify") {
		t.Errorf("err = %v", err)
	}
	if len(f.byNumber(1).BlockedBy) != 0 {
		t.Error("edge added despite unverifiable check")
	}
}

func TestBlockAlreadyBlocked(t *testing.T) {
	a := issue(1, "A", "P2", "bug")
	a.BlockedBy = []model.Ref{{Number: 2, State: "OPEN"}}
	f := newFake(a, issue(2, "B", "P2", "bug"))
	app, out, _ := newApp(f)
	if err := app.Block(ctx, 1, 2, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "already blocked") {
		t.Errorf("output = %q", out.String())
	}
	if len(f.byNumber(1).BlockedBy) != 1 {
		t.Error("duplicate edge added")
	}
}

func TestBlockRequiresOpenIssues(t *testing.T) {
	closed := issue(2, "Closed", "P2", "bug")
	closed.State = "CLOSED"
	f := newFake(issue(1, "A", "P2", "bug"), closed)
	app, _, _ := newApp(f)
	if err := app.Block(ctx, 1, 2, false); err == nil || !strings.Contains(err.Error(), "closed blockers don't block") {
		t.Errorf("err = %v", err)
	}
	if err := app.Block(ctx, 99, 1, false); err == nil || !strings.Contains(err.Error(), "not an open issue") {
		t.Errorf("err = %v", err)
	}
}

// TestBlockRefusesClosedTarget covers #39. Block's old message for a closed
// target was "not an open issue in <repo>", which reads as "no such issue" —
// the caller cannot tell a typo'd number from a target that was closed out
// from under them, and only one of those is worth retrying with --closed.
func TestBlockRefusesClosedTarget(t *testing.T) {
	closed := issue(1, "A", "P2", "bug")
	closed.State = "CLOSED"
	closed.StateReason = "NOT_PLANNED"
	f := newFake(closed, issue(2, "B", "P2", "bug"))
	app, _, _ := newApp(f)

	err := app.Block(ctx, 1, 2, false)
	if err == nil || !strings.Contains(err.Error(), "#1 is closed (not planned)") {
		t.Errorf("err = %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "--closed") {
		t.Errorf("refusal omits the override flag: %v", err)
	}
	if got := f.byNumber(1).BlockedBy; len(got) != 0 {
		t.Errorf("edge added to a closed issue: %v", got)
	}
}

// TestUnblockRefusesClosedTarget covers #39. Unblock is the case where a silent
// success is most misleading: the edge really is gone from a closed issue, so
// nothing looks wrong until someone reopens it.
func TestUnblockRefusesClosedTarget(t *testing.T) {
	closed := issue(1, "A", "P2", "bug")
	closed.State = "CLOSED"
	closed.StateReason = "COMPLETED"
	closed.BlockedBy = []model.Ref{{Number: 2, State: "OPEN"}}
	f := newFake(closed, issue(2, "B", "P2", "bug"))
	app, _, _ := newApp(f)

	err := app.Unblock(ctx, 1, 2, false)
	if err == nil || !strings.Contains(err.Error(), "#1 is closed (completed)") {
		t.Errorf("err = %v", err)
	}
	if got := f.byNumber(1).BlockedBy; len(got) != 1 {
		t.Errorf("edge removed from a closed issue: %v", got)
	}
	for _, call := range f.calls {
		if strings.HasPrefix(call, "RemoveBlockedBy") {
			t.Errorf("refused unblock still mutated: %s", call)
		}
	}
}

// TestWriteCommandsEditClosedWithOverride is the other half of the guard: the
// rare deliberate edit of a closed issue has to remain possible, or the guard
// just moves the work to the web UI where none of the conventions apply.
func TestWriteCommandsEditClosedWithOverride(t *testing.T) {
	closed := issue(1, "Work", "P2", "bug")
	closed.State = "CLOSED"
	closed.StateReason = "COMPLETED"
	f := newFake(closed, issue(2, "B", "P2", "bug"))
	app, _, _ := newApp(f)

	if err := app.Set(ctx, 1, SetOpts{Priority: "P0", AllowClosed: true}); err != nil {
		t.Fatalf("set with override: %v", err)
	}
	if got := f.byNumber(1).Labels; !slices.Contains(got, "P0") {
		t.Errorf("override set did not apply: %v", got)
	}
	if err := app.Block(ctx, 1, 2, true); err != nil {
		t.Fatalf("block with override: %v", err)
	}
	if got := f.byNumber(1).BlockedBy; len(got) != 1 {
		t.Errorf("override block did not apply: %v", got)
	}
	if err := app.Unblock(ctx, 1, 2, true); err != nil {
		t.Fatalf("unblock with override: %v", err)
	}
	if got := f.byNumber(1).BlockedBy; len(got) != 0 {
		t.Errorf("override unblock did not apply: %v", got)
	}
}

func TestUnblock(t *testing.T) {
	a := issue(1, "A", "P2", "bug")
	a.BlockedBy = []model.Ref{{Number: 2, State: "OPEN"}}
	f := newFake(a, issue(2, "B", "P2", "bug"))
	app, out, _ := newApp(f)
	if err := app.Unblock(ctx, 1, 2, false); err != nil {
		t.Fatal(err)
	}
	if len(f.byNumber(1).BlockedBy) != 0 {
		t.Errorf("blockedBy = %v", f.byNumber(1).BlockedBy)
	}
	if !strings.Contains(out.String(), "unblocked #1 from #2") {
		t.Errorf("output = %q", out.String())
	}
}

func TestUnblockNotBlocked(t *testing.T) {
	f := newFake(issue(1, "A", "P2", "bug"), issue(2, "B", "P2", "bug"))
	app, out, _ := newApp(f)
	if err := app.Unblock(ctx, 1, 2, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "not blocked") {
		t.Errorf("output = %q", out.String())
	}
}

func TestEpicCreate(t *testing.T) {
	f := newFake(issue(1, "Child A", "P2", "task"), issue(2, "Child B", "P2", "task"))
	app, out, _ := newApp(f)
	if err := app.EpicCreate(ctx, EpicCreateOpts{Title: "Big feature", Children: []int{1, 2}}); err != nil {
		t.Fatal(err)
	}
	epic := f.byNumber(101)
	if epic.Title != "Epic: Big feature" {
		t.Errorf("title = %q", epic.Title)
	}
	if len(epic.SubIssues) != 2 {
		t.Errorf("subIssues = %v", epic.SubIssues)
	}
	if f.byNumber(1).Parent.Number != 101 {
		t.Errorf("child parent = %v", f.byNumber(1).Parent)
	}
	if !strings.Contains(out.String(), "created epic #101: Epic: Big feature (2 children)") {
		t.Errorf("output = %q", out.String())
	}
}

func TestEpicCreateKeepsExistingPrefix(t *testing.T) {
	f := newFake()
	app, _, _ := newApp(f)
	if err := app.EpicCreate(ctx, EpicCreateOpts{Title: "Epic: already prefixed"}); err != nil {
		t.Fatal(err)
	}
	if got := f.byNumber(101).Title; got != "Epic: already prefixed" {
		t.Errorf("title = %q", got)
	}
	exitCode(t, app.EpicCreate(ctx, EpicCreateOpts{}), ExitUsage)
}

func TestEpicCreateSections(t *testing.T) {
	f := newFake()
	app, _, _ := newApp(f)
	err := app.EpicCreate(ctx, EpicCreateOpts{
		Title:    "Big feature",
		Sections: conventions.Sections{Goal: "The narrative", DoneWhen: []string{"all children closed"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "### Goal\n\nThe narrative\n\n### Done when\n\n- [ ] all children closed"
	if got := f.byNumber(101).Body; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestEpicCreateBodyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(path, []byte("### Goal\n\nlong-form\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := newFake()
	app, _, _ := newApp(f)
	if err := app.EpicCreate(ctx, EpicCreateOpts{Title: "Big", BodyFile: path}); err != nil {
		t.Fatal(err)
	}
	if got := f.byNumber(101).Body; !strings.Contains(got, "long-form") {
		t.Errorf("body = %q", got)
	}
}

func TestEpicCreateEdit(t *testing.T) {
	f := newFake()
	app, _, _ := newApp(f)
	var seeded string
	app.Edit = func(initial string) (string, error) {
		seeded = initial
		return "### Goal\n\nfilled in\n\n### Approach\n\n\n### Done when\n\n- [ ] \n", nil
	}
	if err := app.EpicCreate(ctx, EpicCreateOpts{Title: "Big", Edit: true}); err != nil {
		t.Fatal(err)
	}
	// Epics are containers, not bugs: the seeded skeleton uses the
	// goal/approach wording.
	if !strings.Contains(seeded, "### Goal") || strings.Contains(seeded, "### Problem") {
		t.Errorf("editor seeded with wrong template: %q", seeded)
	}
	body := f.byNumber(101).Body
	if !strings.Contains(body, "filled in") || strings.Contains(body, "Approach") {
		t.Errorf("empty sections not stripped: %q", body)
	}
}

func TestEpicCreateSectionValidation(t *testing.T) {
	f := newFake()
	app, _, _ := newApp(f)
	usageError(t, app.EpicCreate(ctx, EpicCreateOpts{Title: "Big",
		Sections: conventions.Sections{Goal: "G"}, BodyFile: "f"}), "section flags")
	usageError(t, app.EpicCreate(ctx, EpicCreateOpts{Title: "Big",
		Sections: conventions.Sections{Problem: "P", Goal: "G"}}), "--problem and --goal are mutually exclusive")
	usageError(t, app.EpicCreate(ctx, EpicCreateOpts{Title: "Big",
		BodyFile: "f", Edit: true}), "--body-file and --edit are mutually exclusive")
	if len(f.calls) != 0 {
		t.Errorf("refused epic creates still called the API: %v", f.calls)
	}
}

func TestSetValidatesBeforeMutating(t *testing.T) {
	f := newFake(issue(1, "Work", "P2", "bug"))
	app, _, _ := newApp(f)
	// Valid --priority but invalid --type: must exit usage without having
	// swapped the priority label first.
	exitCode(t, app.Set(ctx, 1, SetOpts{Priority: "P0", Type: "bogus"}), ExitUsage)
	labels := f.byNumber(1).Labels
	if !slices.Contains(labels, "P2") || slices.Contains(labels, "P0") {
		t.Errorf("priority mutated before type validation failed: %v", labels)
	}
}

func TestSetReportsPartialApplication(t *testing.T) {
	f := newFake(issue(1, "Work", "P2", "bug"))
	f.failOn["EditTitle"] = errors.New("boom")
	app, _, _ := newApp(f)
	err := app.Set(ctx, 1, SetOpts{Priority: "P0", Title: "Renamed"})
	if err == nil || !strings.Contains(err.Error(), "partially updated (applied priority)") {
		t.Errorf("err = %v", err)
	}
}

func TestCloseReportsCommentedWhenCloseFails(t *testing.T) {
	f := newFake(issue(1, "A", "P2", "bug"))
	f.failOn["CloseIssue"] = errors.New("boom")
	app, _, _ := newApp(f)
	err := app.Close(ctx, 1, "wontfix", false, 0)
	if err == nil || !strings.Contains(err.Error(), "posted the reason comment") {
		t.Errorf("err = %v", err)
	}
}

func TestReportMutationWrapsRefetchFailure(t *testing.T) {
	f := newFake(issue(1, "A", "P2", "bug"), issue(2, "B", "P2", "bug"))
	f.failOn["GetIssue"] = errors.New("boom") // only the JSON re-fetch calls it here
	app, _, _ := newApp(f)
	app.JSON = true
	err := app.Block(ctx, 1, 2, false)
	if err == nil || !strings.Contains(err.Error(), "was updated, but fetching the result") {
		t.Errorf("err = %v", err)
	}
	if len(f.byNumber(1).BlockedBy) != 1 {
		t.Error("mutation not applied before the re-fetch failed")
	}
}

func TestSetFailurePropagates(t *testing.T) {
	f := newFake(issue(1, "Work", "P2", "bug"))
	f.failOn["RemoveLabel"] = errors.New("boom")
	app, _, _ := newApp(f)
	if err := app.Set(ctx, 1, SetOpts{Priority: "P0"}); err == nil {
		t.Error("RemoveLabel failure swallowed")
	}
	f2 := newFake(issue(1, "Work", "P2", "bug"))
	f2.failOn["EditTitle"] = errors.New("boom")
	app2, _, _ := newApp(f2)
	if err := app2.Set(ctx, 1, SetOpts{Title: "X"}); err == nil {
		t.Error("EditTitle failure swallowed")
	}
}

func TestSetParentMissing(t *testing.T) {
	f := newFake(issue(1, "Work", "P2", "bug"))
	app, _, _ := newApp(f)
	if err := app.Set(ctx, 1, SetOpts{Parent: 99}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v", err)
	}
}

// TestCreateRefusesAMissingBlocker covers #232: a dependency edge names an
// existing issue, so a typo must fail before the issue is filed rather than
// leave a stray issue carrying a dangling edge.
func TestCreateRefusesAMissingBlocker(t *testing.T) {
	eachBackend(t, func(t *testing.T, store gh.IssueStore) {
		app, _, _ := newLocalApp(store)
		err := app.Create(ctx, CreateOpts{Title: "T", Type: "bug", BlockedBy: []int{99}})
		if err == nil {
			t.Fatal("create accepted a --blocked-by issue that does not exist")
		}
		if got := ExitCode(err); got != ExitGeneric {
			t.Errorf("exit code = %d, want %d", got, ExitGeneric)
		}
		assertNoIssues(t, store)
	})
}

// TestCreateRefusesAMissingParent: --parent is the same kind of reference, and
// a bad one must not leave an issue with no parent behind either.
func TestCreateRefusesAMissingParent(t *testing.T) {
	eachBackend(t, func(t *testing.T, store gh.IssueStore) {
		app, _, _ := newLocalApp(store)
		err := app.Create(ctx, CreateOpts{Title: "T", Type: "bug", Parent: 99})
		if err == nil {
			t.Fatal("create accepted a --parent issue that does not exist")
		}
		if got := ExitCode(err); got != ExitGeneric {
			t.Errorf("exit code = %d, want %d", got, ExitGeneric)
		}
		assertNoIssues(t, store)
	})
}

// TestEpicCreateRefusesAMissingChild covers #232's third flag: a childless
// epic is indistinguishable from a triage mistake and stays in the queue
// forever, so --children is checked before the epic is filed.
func TestEpicCreateRefusesAMissingChild(t *testing.T) {
	eachBackend(t, func(t *testing.T, store gh.IssueStore) {
		app, _, _ := newLocalApp(store)
		err := app.EpicCreate(ctx, EpicCreateOpts{Title: "Big", Children: []int{99}})
		if err == nil {
			t.Fatal("epic create accepted a --children issue that does not exist")
		}
		if got := ExitCode(err); got != ExitGeneric {
			t.Errorf("exit code = %d, want %d", got, ExitGeneric)
		}
		assertNoIssues(t, store)
	})
}

// TestEpicCreateRefusesAChildWithAParent: AddSubIssue refuses a child that
// already belongs to another epic, and that check has to happen before the
// epic is filed too — otherwise the predictable failure leaves the same stray
// childless epic #232 is about.
func TestEpicCreateRefusesAChildWithAParent(t *testing.T) {
	eachBackend(t, func(t *testing.T, store gh.IssueStore) {
		parent, err := store.CreateIssue(ctx, "Existing epic", "", []string{"P2"})
		if err != nil {
			t.Fatal(err)
		}
		child, err := store.CreateIssue(ctx, "Child", "", []string{"P2", "task"})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.AddSubIssue(ctx, parent.Number, child.Number, false); err != nil {
			t.Fatal(err)
		}
		before, err := store.ListIssues(ctx, []gh.IssueState{gh.StateOpen, gh.StateClosed})
		if err != nil {
			t.Fatal(err)
		}
		app, _, _ := newLocalApp(store)
		err = app.EpicCreate(ctx, EpicCreateOpts{Title: "Big", Children: []int{child.Number}})
		if err == nil {
			t.Fatal("epic create accepted a child that already has a parent")
		}
		if got := ExitCode(err); got != ExitGeneric {
			t.Errorf("exit code = %d, want %d", got, ExitGeneric)
		}
		for _, want := range []string{fmt.Sprintf("#%d", child.Number), fmt.Sprintf("#%d", parent.Number), fmt.Sprintf("hew set %d --no-parent", child.Number)} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to name %s", err, want)
			}
		}
		after, err := store.ListIssues(ctx, []gh.IssueState{gh.StateOpen, gh.StateClosed})
		if err != nil {
			t.Fatal(err)
		}
		if len(after) != len(before) {
			t.Errorf("refused epic create filed %d issue(s)", len(after)-len(before))
		}
	})
}

// A reference can disappear between the pre-check and the mutation, since
// GitHub has no transaction spanning both. The residual failure still has to
// name the issue that was created, so the operator can finish by hand.
func TestCreateReferenceFailureAfterTheCheckNamesTheCreatedIssue(t *testing.T) {
	cases := []struct {
		name   string
		fail   string
		create func(*App) error
		want   string
	}{
		{
			"blocked-by", "AddBlockedBy",
			func(a *App) error {
				return a.Create(ctx, CreateOpts{Title: "T", Type: "bug", BlockedBy: []int{1}})
			},
			"created #101 but --blocked-by 1 failed",
		},
		{
			"parent", "AddSubIssue",
			func(a *App) error {
				return a.Create(ctx, CreateOpts{Title: "T", Type: "bug", Parent: 1})
			},
			"created #101 but --parent 1 failed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(issue(1, "Reference", "P2", "task"))
			f.failOn[tc.fail] = errors.New("boom")
			app, _, _ := newApp(f)
			err := tc.create(app)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestEpicCreateChildFailureAfterTheCheckNamesTheCreatedEpic(t *testing.T) {
	f := newFake(issue(1, "Child", "P2", "task"))
	f.failOn["AddSubIssue"] = errors.New("boom")
	app, _, _ := newApp(f)
	err := app.EpicCreate(ctx, EpicCreateOpts{Title: "Big", Children: []int{1}})
	want := "created epic #101 but attaching #1 failed"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("err = %v, want it to contain %q", err, want)
	}
}

// assertNoIssues is the "created nothing" half of a refused create: the
// backend must hold no issue at all, open or closed.
// TestCreateRefusesAClosedBlocker: block refuses a closed blocker because it
// blocks nothing, and create --blocked-by writes the same edge, so it follows
// the same rule instead of attaching a dead edge.
func TestCreateRefusesAClosedBlocker(t *testing.T) {
	closed := issue(2, "Done", "P2", "bug")
	closed.State = "CLOSED"
	f := newFake(closed)
	app, _, _ := newApp(f)
	err := app.Create(ctx, CreateOpts{Title: "T", Type: "bug", BlockedBy: []int{2}})
	if err == nil || !strings.Contains(err.Error(), "closed blockers don't block") {
		t.Fatalf("err = %v, want a closed-blocker refusal", err)
	}
	if got := ExitCode(err); got != ExitGeneric {
		t.Errorf("exit code = %d, want %d", got, ExitGeneric)
	}
	if f.callCounts["CreateIssue"] != 0 {
		t.Error("refused create still filed an issue")
	}
}

// TestCreateAcceptsAClosedParent: only a blocker has to be open; a closed
// parent or discovered-from issue is still a real issue to reference.
func TestCreateAcceptsAClosedParent(t *testing.T) {
	closed := issue(2, "Done", "P2", "bug")
	closed.State = "CLOSED"
	f := newFake(closed)
	app, _, _ := newApp(f)
	if err := app.Create(ctx, CreateOpts{Title: "T", Type: "bug", Parent: 2, DiscoveredFrom: 2}); err != nil {
		t.Fatalf("create with a closed parent and discovered-from: %v", err)
	}
}

// TestCreateDeduplicatesBlockers: a repeated number is fetched and attached
// once, not twice.
func TestCreateDeduplicatesBlockers(t *testing.T) {
	f := newFake(issue(2, "Blocker", "P2", "bug"))
	app, _, _ := newApp(f)
	if err := app.Create(ctx, CreateOpts{Title: "T", Type: "bug", BlockedBy: []int{2, 2}}); err != nil {
		t.Fatal(err)
	}
	if got := f.callCounts["AddBlockedBy"]; got != 1 {
		t.Errorf("AddBlockedBy called %d times, want 1", got)
	}
	if got := f.callCounts["GetIssue"]; got != 1 {
		t.Errorf("GetIssue called %d times, want 1", got)
	}
}

// TestEpicCreateDeduplicatesChildren: --children 5,5 attaches the child once
// and reports one child.
func TestEpicCreateDeduplicatesChildren(t *testing.T) {
	f := newFake(issue(5, "Child", "P2", "task"))
	app, out, _ := newApp(f)
	if err := app.EpicCreate(ctx, EpicCreateOpts{Title: "Big", Children: []int{5, 5}}); err != nil {
		t.Fatal(err)
	}
	if got := f.callCounts["AddSubIssue"]; got != 1 {
		t.Errorf("AddSubIssue called %d times, want 1", got)
	}
	if !strings.Contains(out.String(), "(1 children)") {
		t.Errorf("out = %q, want one child reported", out.String())
	}
}

func assertNoIssues(t *testing.T, store gh.IssueStore) {
	t.Helper()
	all, err := store.ListIssues(ctx, []gh.IssueState{gh.StateOpen, gh.StateClosed})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Errorf("refused create still filed %d issue(s): %v", len(all), all)
	}
}

func TestInit(t *testing.T) {
	f := newFake()
	app, out, _ := newApp(f)
	if err := app.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.labels) != len(conventions.Labels) {
		t.Errorf("created %d labels, want %d", len(f.labels), len(conventions.Labels))
	}
	for _, want := range []string{"created labels: P0", "hew prime", "CLAUDE.md"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestInitIdempotent(t *testing.T) {
	f := newFake()
	for _, l := range conventions.Labels {
		f.labels = append(f.labels, gh.Label{Name: l.Name, Color: l.Color, Description: l.Description})
	}
	app, out, _ := newApp(f)
	if err := app.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "already exist") {
		t.Errorf("output = %q", out.String())
	}
	for _, call := range f.calls {
		if strings.HasPrefix(call, "CreateLabel") {
			t.Errorf("label recreated: %s", call)
		}
	}
}

func TestInitJSON(t *testing.T) {
	f := newFake()
	app, out, _ := newApp(f)
	app.JSON = true
	if err := app.Init(ctx); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Backend       string
		Path          string
		CreatedLabels []string `json:"createdLabels"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.CreatedLabels) != len(conventions.Labels) {
		t.Errorf("JSON = %s", out.String())
	}
	if got.Backend != "github" || got.Path != "" {
		t.Errorf("JSON = %+v; want the github backend with no local path", got)
	}
}

// create composes issue bodies, and pr later copies those sections into the
// PR verbatim — so the check belongs at the point the text is first written,
// where the remedy (set --body-file) still applies cheaply.
func TestCreateWarnsAboutUnmarkedCodeText(t *testing.T) {
	f := newFake()
	app, _, errOut := newApp(f)
	err := app.Create(ctx, CreateOpts{
		Title: "T", Type: "task",
		Sections: conventions.Sections{
			Goal:     "Rewrite internal/cli/write.go",
			DoneWhen: []string{"go test -race ./... is green"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	warning := errOut.String()
	for _, want := range []string{"internal/cli/write.go", "./...", "hew set"} {
		if !strings.Contains(warning, want) {
			t.Errorf("warning %q does not name %q", warning, want)
		}
	}
	// Reporting, not rewriting: the stored body is exactly what was passed.
	if body := f.byNumber(101).Body; !strings.Contains(body, "Rewrite internal/cli/write.go") {
		t.Errorf("create rewrote the author's text: %q", body)
	}
}

// A --body-file body is checked too. The escape hatch skips composition, not
// the convention — and a warning cannot corrupt what it only reports on.
func TestCreateWarnsAboutUnmarkedCodeTextInABodyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "body.md")
	if err := os.WriteFile(path, []byte("### Goal\n\nTouch internal/gh/client.go"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := newFake()
	app, _, errOut := newApp(f)
	if err := app.Create(ctx, CreateOpts{Title: "T", Type: "task", BodyFile: path}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "internal/gh/client.go") {
		t.Errorf("no warning for a --body-file body: %q", errOut.String())
	}
}

// eachBackend runs the same scenario against the fake and the local store:
// commands are written against gh.IssueStore, so the two must agree.
func eachBackend(t *testing.T, run func(t *testing.T, store gh.IssueStore)) {
	t.Helper()
	t.Run("fake", func(t *testing.T) { run(t, newFake()) })
	t.Run("local", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), local.DirName)
		if err := local.Init(dir); err != nil {
			t.Fatal(err)
		}
		store, err := local.Open(dir, "agent-a")
		if err != nil {
			t.Fatal(err)
		}
		run(t, store)
	})
}

func TestSetRemovesOversizedLegacyArea(t *testing.T) {
	eachBackend(t, func(t *testing.T, store gh.IssueStore) {
		area := strings.Repeat("a", 80)
		work, err := store.CreateIssue(ctx, "Legacy issue", "body", []string{"P2", "bug", area})
		if err != nil {
			t.Fatal(err)
		}
		app, _, _ := newLocalApp(store)
		if err := app.Set(ctx, work.Number, SetOpts{RemoveAreas: []string{area}}); err != nil {
			t.Fatalf("cannot remove legacy area: %v", err)
		}
		after, err := store.GetIssue(ctx, work.Number)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(after.Labels, []string{"P2", "bug"}) {
			t.Fatalf("labels after removal = %v", after.Labels)
		}
		if f, ok := store.(*fakeClient); ok {
			f.calls = nil
		}
		exitCode(t, app.Set(ctx, work.Number, SetOpts{AddAreas: []string{area}, Priority: "P0"}), ExitUsage)
		if f, ok := store.(*fakeClient); ok {
			assertOnlyReads(t, f)
		}
		after, err = store.GetIssue(ctx, work.Number)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(after.Labels, []string{"P2", "bug"}) {
			t.Errorf("refused addition changed labels: %v", after.Labels)
		}
	})
}

func TestWriteFieldValidation(t *testing.T) {
	cases := []struct {
		name, title, area, message string
	}{
		{name: "newline", title: "first\nsecond", message: "single line"},
		{name: "carriage return", title: "first\rsecond", message: "single line"},
		{name: "CRLF", title: "first\r\nsecond", message: "single line"},
		{name: "Unicode line separator", title: "first\u2028second", message: "single line"},
		{name: "long ASCII title", title: strings.Repeat("a", 257), message: "256"},
		{name: "long Unicode title", title: strings.Repeat("界", 257), message: "256"},
		{name: "epic prefix overflow", title: strings.Repeat("a", 251), message: "256"},
		{name: "unprefixed epic boundary", title: strings.Repeat("a", 250)},
		{name: "prefixed title overflow", title: "Epic: " + strings.Repeat("a", 251), message: "256"},
		{name: "long ASCII area", area: strings.Repeat("a", 51), message: "50"},
		{name: "long Unicode area", area: strings.Repeat("界", 51), message: "50"},
		{name: "comma area", area: "x,y", message: "comma"},
		{name: "claim label", area: "in-progress", message: "convention"},
		{name: "mixed case claim label", area: "IN-Progress", message: "convention"},
		{name: "ASCII boundaries", title: strings.Repeat("a", 256), area: strings.Repeat("a", 50)},
		{name: "Unicode boundaries", title: strings.Repeat("界", 256), area: strings.Repeat("界", 50)},
	}
	for _, command := range []string{"create", "set", "epic create", "apply", "apply epic", "apply dry run"} {
		t.Run(command, func(t *testing.T) {
			for _, tc := range cases {
				if strings.Contains(tc.name, "epic") && !strings.Contains(command, "epic") {
					continue
				}
				if strings.Contains(command, "epic") && tc.area != "" && tc.title == "" {
					continue // epic create has no area flag
				}
				t.Run(tc.name, func(t *testing.T) {
					eachBackend(t, func(t *testing.T, store gh.IssueStore) {
						work, err := store.CreateIssue(ctx, "Existing", "body", []string{"P2", "bug"})
						if err != nil {
							t.Fatal(err)
						}
						before, err := store.ListIssues(ctx, []gh.IssueState{gh.StateOpen})
						if err != nil {
							t.Fatal(err)
						}
						labels, err := store.ListLabels(ctx)
						if err != nil {
							t.Fatal(err)
						}
						if f, ok := store.(*fakeClient); ok {
							f.calls = nil
						}
						app, out, _ := newLocalApp(store)
						title := tc.title
						if title == "" {
							title = "Valid title"
						}
						// Keep valid boundary cases at 256 after the cosmetic prefix.
						if strings.Contains(command, "epic") && strings.HasSuffix(tc.name, "boundaries") {
							title = conventions.EpicTitlePrefix + string([]rune(title)[len(conventions.EpicTitlePrefix):])
						}
						storedTitle := title
						if strings.Contains(command, "epic") && !strings.HasPrefix(storedTitle, conventions.EpicTitlePrefix) {
							storedTitle = conventions.EpicTitlePrefix + storedTitle
						}
						areas := []string{}
						if tc.area != "" && command != "epic create" {
							areas = append(areas, tc.area)
						}
						var opts ApplyOpts
						switch command {
						case "create":
							err = app.Create(ctx, CreateOpts{Title: title, Type: "bug", Areas: areas})
						case "set":
							err = app.Set(ctx, work.Number, SetOpts{Title: title, AddAreas: areas, Priority: "P0"})
						case "epic create":
							err = app.EpicCreate(ctx, EpicCreateOpts{Title: title})
						default:
							entry := map[string]any{"title": title, "type": "bug", "areas": areas}
							if command == "apply epic" {
								entry["type"] = "epic"
							}
							data, marshalErr := json.Marshal(entry)
							if marshalErr != nil {
								t.Fatal(marshalErr)
							}
							opts = ApplyOpts{File: filepath.Join(t.TempDir(), "plan.jsonl"), DryRun: command == "apply dry run"}
							// A bad later entry must not leave the first issue or its labels behind.
							data = append([]byte("{\"title\":\"First\",\"type\":\"task\",\"areas\":[\"new-area\"]}\n"), data...)
							if err := os.WriteFile(opts.File, data, 0o600); err != nil {
								t.Fatal(err)
							}
							err = app.Apply(ctx, opts)
						}
						if tc.message != "" {
							if err == nil {
								t.Fatal("invalid input was accepted")
							}
							// The plan parser already refuses an exact convention-label collision.
							if strings.HasPrefix(command, "apply") && strings.EqualFold(tc.area, "in-progress") {
								exitCode(t, err, ExitGeneric)
							} else {
								exitCode(t, err, ExitUsage)
							}
							if !strings.Contains(err.Error(), tc.message) {
								t.Errorf("error = %v, want %q", err, tc.message)
							}
							if out.Len() != 0 {
								t.Errorf("refused write emitted output: %q", out.String())
							}
							if f, ok := store.(*fakeClient); ok {
								assertOnlyReads(t, f)
							}
							if opts.File != "" {
								if _, err := os.Stat(opts.File + ".state.json"); !errors.Is(err, os.ErrNotExist) {
									t.Errorf("refusal wrote a checkpoint: %v", err)
								}
							}
						} else if err != nil {
							t.Fatalf("valid boundary input refused: %v", err)
						}
						after, err := store.ListIssues(ctx, []gh.IssueState{gh.StateOpen})
						if err != nil {
							t.Fatal(err)
						}
						afterLabels, err := store.ListLabels(ctx)
						if err != nil {
							t.Fatal(err)
						}
						if tc.message != "" || opts.DryRun {
							if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(labels, afterLabels) {
								t.Errorf("refused write or dry run changed store: issues=%+v labels=%v", after, afterLabels)
							}
						} else if !slices.ContainsFunc(after, func(i model.Issue) bool {
							return i.Title == storedTitle && (len(areas) == 0 || slices.Contains(i.Labels, tc.area))
						}) {
							t.Errorf("accepted input missing from store: %+v", after)
						}
					})
				})
			}
		})
	}
}

// TestCloseDuplicateOfRefusesBadTargets covers #212: a duplicate of itself or
// of an issue that does not exist is a typo, and closing on it would leave a
// closed issue pointing nowhere.
func TestCloseDuplicateOfRefusesBadTargets(t *testing.T) {
	// A missing issue exits like --parent and --blocked-by naming one: plain
	// failure. Only the self-reference is a usage error.
	cases := []struct {
		name   string
		target func(n int) int
		code   int
	}{
		{"itself", func(n int) int { return n }, ExitUsage},
		{"a nonexistent issue", func(int) int { return 99 }, ExitGeneric},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eachBackend(t, func(t *testing.T, store gh.IssueStore) {
				work, err := store.CreateIssue(ctx, "Work", "", []string{"P2", "bug"})
				if err != nil {
					t.Fatal(err)
				}
				app, _, _ := newLocalApp(store)
				err = app.Close(ctx, work.Number, "", false, tc.target(work.Number))
				if err == nil {
					t.Fatal("close accepted a bad --duplicate-of target")
				}
				if got := ExitCode(err); got != tc.code {
					t.Errorf("exit code = %d, want %d", got, tc.code)
				}
				after, err := store.GetIssue(ctx, work.Number)
				if err != nil {
					t.Fatal(err)
				}
				if !after.IsOpen() {
					t.Errorf("#%d was closed despite the refusal", work.Number)
				}
				if after.CommentsTotal != 0 || len(after.Comments) != 0 {
					t.Errorf("refused close still commented: %v", after.Comments)
				}
			})
		})
	}
}

// TestCloseDuplicateOfNamesTheTarget: --reason is free text, so a custom one
// that omits the original would leave the closed duplicate pointing at
// nothing a reader can follow.
func TestCloseDuplicateOfNamesTheTarget(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		want   string
	}{
		{"custom reason without the reference", "same root cause", "same root cause\n\nDuplicate of #{n}"},
		{"custom reason already naming it", "dup of #{n}, fixed there", "dup of #{n}, fixed there"},
		{"a longer number is not the reference", "see #{n}3", "see #{n}3\n\nDuplicate of #{n}"},
		{"another repository's reference is not this one", "same as other/repo#{n}", "same as other/repo#{n}\n\nDuplicate of #{n}"},
		{"a reference at the start of the reason", "#{n} covers it", "#{n} covers it"},
		{"a reason ending in a newline", "same root cause\n", "same root cause\n\nDuplicate of #{n}"},
		{"no reason", "", "Duplicate of #{n}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eachBackend(t, func(t *testing.T, store gh.IssueStore) {
				dup, err := store.CreateIssue(ctx, "Dup", "", []string{"P2", "bug"})
				if err != nil {
					t.Fatal(err)
				}
				original, err := store.CreateIssue(ctx, "Original", "", []string{"P2", "bug"})
				if err != nil {
					t.Fatal(err)
				}
				n := strconv.Itoa(original.Number)
				reason, want := strings.ReplaceAll(tc.reason, "{n}", n), strings.ReplaceAll(tc.want, "{n}", n)
				app, _, _ := newLocalApp(store)
				if err := app.Close(ctx, dup.Number, reason, false, original.Number); err != nil {
					t.Fatal(err)
				}
				after, err := store.GetIssue(ctx, dup.Number)
				if err != nil {
					t.Fatal(err)
				}
				if len(after.Comments) != 1 || after.Comments[0].Body != want {
					t.Errorf("comments = %v, want one reading %q", after.Comments, want)
				}
				if after.IsOpen() || after.StateReason != "DUPLICATE" {
					t.Errorf("state = %s/%s, want closed as DUPLICATE", after.State, after.StateReason)
				}
			})
		})
	}
}

// TestCreateDiscoveredFromRefusesAMissingIssue covers #213: the flag is a
// provenance link, so a typo must fail before anything is filed rather than
// leave an issue behind carrying a dangling reference.
func TestCreateDiscoveredFromRefusesAMissingIssue(t *testing.T) {
	eachBackend(t, func(t *testing.T, store gh.IssueStore) {
		app, _, _ := newLocalApp(store)
		err := app.Create(ctx, CreateOpts{Title: "Follow-up", Type: "bug", DiscoveredFrom: 99})
		if err == nil {
			t.Fatal("create accepted a --discovered-from for an issue that does not exist")
		}
		if ExitCode(err) == 0 {
			t.Errorf("exit code = 0 for %v", err)
		}
		all, err := store.ListIssues(ctx, []gh.IssueState{gh.StateOpen, gh.StateClosed})
		if err != nil {
			t.Fatal(err)
		}
		if len(all) != 0 {
			t.Errorf("refused create still filed %d issue(s): %v", len(all), all)
		}
	})
}

// Discovery often follows a close, so a closed source is a valid provenance.
func TestCreateDiscoveredFromAcceptsAClosedIssue(t *testing.T) {
	eachBackend(t, func(t *testing.T, store gh.IssueStore) {
		source, err := store.CreateIssue(ctx, "Source", "", []string{"P2", "task"})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.CloseIssue(ctx, source.Number, gh.CloseCompleted); err != nil {
			t.Fatal(err)
		}
		app, _, _ := newLocalApp(store)
		if err := app.Create(ctx, CreateOpts{Title: "Follow-up", Type: "bug", DiscoveredFrom: source.Number}); err != nil {
			t.Fatal(err)
		}
		all, err := store.ListIssues(ctx, []gh.IssueState{gh.StateOpen})
		if err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("Discovered while working on #%d", source.Number)
		if len(all) != 1 || !strings.Contains(all[0].Body, want) {
			t.Errorf("open issues = %v, want one whose body contains %q", all, want)
		}
	})
}

// TestStartRefusesOpenBlockers: ready hides a blocked issue, so start must not
// let an id taken from list or show claim it anyway. The refusal names every
// open blocker, uses its own exit code, and mutates nothing.
func TestStartRefusesOpenBlockers(t *testing.T) {
	blocked := issue(3, "Blocked", "P2", "task")
	blocked.BlockedBy = []model.Ref{{Number: 1, State: "OPEN"}, {Number: 2, State: "CLOSED"}, {Number: 4, State: "OPEN"}}
	f := newFake(issue(1, "Open blocker", "P2", "task"), issue(4, "Another", "P2", "task"), blocked)
	app, _, _ := newApp(f)
	err := app.Start(ctx, 3, StartOpts{})
	exitCode(t, err, ExitBlocked)
	if !strings.Contains(err.Error(), "#1") || !strings.Contains(err.Error(), "#4") || strings.Contains(err.Error(), "#2") {
		t.Errorf("message should name exactly the open blockers: %v", err)
	}
	got := f.byNumber(3)
	if len(got.Assignees) != 0 || slices.Contains(got.Labels, "in-progress") {
		t.Errorf("refused start mutated #3: labels=%v assignees=%v", got.Labels, got.Assignees)
	}
}

func TestStartAllowsClosedBlockers(t *testing.T) {
	done := issue(1, "Done", "P2", "task")
	done.State = "CLOSED"
	blocked := issue(2, "Unblocked", "P2", "task")
	blocked.BlockedBy = []model.Ref{{Number: 1, State: "CLOSED"}}
	f := newFake(done, blocked)
	app, _, errOut := newApp(f)
	if err := app.Start(ctx, 2, StartOpts{}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.byNumber(2).Assignees, "me") {
		t.Errorf("claim not applied: %v", f.byNumber(2).Assignees)
	}
	if errOut.Len() != 0 {
		t.Errorf("unexpected warnings: %q", errOut.String())
	}
}

func TestStartForceOverridesOpenBlockers(t *testing.T) {
	blocked := issue(2, "Blocked", "P2", "task")
	blocked.BlockedBy = []model.Ref{{Number: 1, State: "OPEN"}}
	f := newFake(issue(1, "Open blocker", "P2", "task"), blocked)
	app, _, _ := newApp(f)
	if err := app.Start(ctx, 2, StartOpts{Force: true}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.byNumber(2).Assignees, "me") {
		t.Errorf("--force did not claim: %v", f.byNumber(2).Assignees)
	}
}

// A blocker list the API capped can hide an open blocker, so the check says
// it cannot be sure rather than claiming the issue is clear.
func TestStartWarnsWhenBlockersAreCapped(t *testing.T) {
	blocked := issue(2, "Many blockers", "P2", "task")
	blocked.BlockedBy = []model.Ref{{Number: 1, State: "CLOSED"}}
	blocked.BlockedByTotal = 60
	done := issue(1, "Done", "P2", "task")
	done.State = "CLOSED"
	f := newFake(done, blocked)
	app, _, errOut := newApp(f)
	if err := app.Start(ctx, 2, StartOpts{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "#2") {
		t.Errorf("expected a capped-blockers warning naming #2, got %q", errOut.String())
	}
}

// The claim guard answers first: an issue that is both claimed and blocked
// reports the claim, so a loop resumes its own work (exit 5) instead of being
// told to pick another item.
func TestStartClaimGuardPrecedesBlockerGuard(t *testing.T) {
	blocked := issue(2, "Claimed and blocked", "P2", "task")
	blocked.Assignees = []string{"other"}
	blocked.BlockedBy = []model.Ref{{Number: 1, State: "OPEN"}}
	f := newFake(issue(1, "Open blocker", "P2", "task"), blocked)
	app, _, _ := newApp(f)
	exitCode(t, app.Start(ctx, 2, StartOpts{}), ExitClaimed)
}

// A capped list that already shows an open blocker is still a refusal.
func TestStartRefusesVisibleBlockerInCappedList(t *testing.T) {
	blocked := issue(2, "Many blockers", "P2", "task")
	blocked.BlockedBy = []model.Ref{{Number: 1, State: "OPEN"}}
	blocked.BlockedByTotal = 60
	f := newFake(issue(1, "Open blocker", "P2", "task"), blocked)
	app, _, _ := newApp(f)
	exitCode(t, app.Start(ctx, 2, StartOpts{}), ExitBlocked)
}

// The capped-list warning is for a claim that goes ahead, not for a command
// that then fails validation.
func TestStartCappedBlockersWarningSkippedOnUsageError(t *testing.T) {
	untriaged := issue(2, "Untriaged")
	untriaged.BlockedBy = []model.Ref{{Number: 1, State: "CLOSED"}}
	untriaged.BlockedByTotal = 60
	f := newFake(untriaged)
	app, _, errOut := newApp(f)
	exitCode(t, app.Start(ctx, 2, StartOpts{}), ExitUsage)
	if errOut.Len() != 0 {
		t.Errorf("unexpected warnings: %q", errOut.String())
	}
}

// The code-span convention is not GitHub's: local bodies are Markdown too, and
// `hew migrate local` carries them to a browser later. So the warning fires on
// the local backend, and says nothing that assumes the GitHub one.
func TestCreateWarnsAboutUnmarkedCodeTextOnTheLocalBackendWithoutGitHubWording(t *testing.T) {
	app, _, errOut := newLocalApp(newFake())
	app.Local = true
	err := app.Create(ctx, CreateOpts{
		Title: "T", Type: "task",
		Sections: conventions.Sections{
			Goal:     "Edit main.go and internal/cli/write.go",
			DoneWhen: []string{"go test -race ./... is green"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	warning := errOut.String()
	for _, want := range []string{"not in code spans", "main.go", "internal/cli/write.go", "./..."} {
		if !strings.Contains(warning, want) {
			t.Errorf("local warning %q does not name %q", warning, want)
		}
	}
	for _, banned := range []string{"GitHub", "github", "autolink", "browser"} {
		if strings.Contains(warning, banned) {
			t.Errorf("local warning %q assumes GitHub (%q)", warning, banned)
		}
	}
}

func TestCodeSpanGuidanceDoesNotClaimEveryBodyIsReadInABrowser(t *testing.T) {
	if strings.Contains(conventions.CodeSpanGuidance, "read in a browser") {
		t.Errorf("guidance %q states the GitHub reader as if it were the only one", conventions.CodeSpanGuidance)
	}
	if !strings.Contains(conventions.CodeSpanGuidance, "migrate local") {
		t.Errorf("guidance %q does not say why local bodies follow the convention", conventions.CodeSpanGuidance)
	}
}
