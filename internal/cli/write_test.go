package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/lumberbarons/hew/internal/conventions"
	"github.com/lumberbarons/hew/internal/gh"
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
	f := newFake()
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
	f := newFake()
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
	if err := app.Start(ctx, 1, "", false); err != nil {
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

func TestStartRefusesClaimed(t *testing.T) {
	assigned := issue(1, "Taken", "P2", "bug")
	assigned.Assignees = []string{"other"}
	f := newFake(assigned, issue(2, "Labeled", "P2", "bug", "in-progress"))
	app, _, _ := newApp(f)
	err := app.Start(ctx, 1, "", false)
	exitCode(t, err, ExitClaimed)
	if !strings.Contains(err.Error(), "@other") {
		t.Errorf("message should name the claimant: %v", err)
	}
	exitCode(t, app.Start(ctx, 2, "", false), ExitClaimed)
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

	err := app.Start(ctx, 1, "", false)
	exitCode(t, err, ExitClaimedByYou)
	if ExitClaimedByYou == ExitClaimed {
		t.Fatal("the two claim refusals must use different exit codes")
	}
	if !strings.Contains(err.Error(), "claimed by you") || !strings.Contains(err.Error(), "@me") {
		t.Errorf("message should say the claim is yours and name the assignee: %v", err)
	}
	// A co-assignment still counts as mine: I am one of the claimants, so
	// resuming — not skipping to the next item — is the right move.
	exitCode(t, app.Start(ctx, 2, "", false), ExitClaimedByYou)
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

	err := app.Start(ctx, 1, "", false)
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
	if err := app.Start(ctx, 1, "", true); err != nil {
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
	if err := app.Start(ctx, 1, "", false); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("closed err = %v", err)
	}
	if err := app.Start(ctx, 2, "", false); err == nil || !strings.Contains(err.Error(), "epic") {
		t.Errorf("epic err = %v", err)
	}
	exitCode(t, app.Start(ctx, 3, "", false), ExitUsage)
	exitCode(t, app.Start(ctx, 3, "P7", false), ExitUsage)
}

func TestStartUntriagedWithPriority(t *testing.T) {
	f := newFake(issue(3, "Untriaged"))
	app, _, _ := newApp(f)
	if err := app.Start(ctx, 3, "P1", false); err != nil {
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
	if err := app.Start(ctx, 1, "P0", false); err != nil {
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
	if err := app.Start(ctx, 1, "", false); err != nil {
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
	if err := app.Start(ctx, 1, "", false); err != nil {
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
	err := app.Start(ctx, 1, "", false)
	exitCode(t, err, ExitClaimed)
	assertNeutralized(t, "claim refusal message", err.Error())
	assertNeutralized(t, "claim refusal warnings", errOut.String())
}

func TestOwnClaimRefusalNeutralizesHostileAssignee(t *testing.T) {
	mine := issue(1, "Mine", "P2", "bug")
	mine.Assignees = []string{"me", "other" + hostile}
	f := newFake(mine)
	app, _, errOut := newApp(f)
	err := app.Start(ctx, 1, "", false)
	exitCode(t, err, ExitClaimedByYou)
	assertNeutralized(t, "own-claim refusal message", err.Error())
	assertNeutralized(t, "own-claim refusal warnings", errOut.String())
}

func TestStartRaceWarningNeutralizesHostileAssignee(t *testing.T) {
	f := newFake(issue(1, "Work", "P2", "bug"))
	f.rivalOnAssign = "rival" + hostile
	app, _, errOut := newApp(f)
	if err := app.Start(ctx, 1, "", false); err != nil {
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
	f := newFake(issue(1, "Work", "P2", "bug"))
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

func TestEpicCreateChildMissing(t *testing.T) {
	f := newFake()
	app, _, _ := newApp(f)
	err := app.EpicCreate(ctx, EpicCreateOpts{Title: "Big", Children: []int{99}})
	if err == nil || !strings.Contains(err.Error(), "attaching #99 failed") {
		t.Errorf("err = %v", err)
	}
}

func TestCreateBlockedByMissing(t *testing.T) {
	f := newFake()
	app, _, _ := newApp(f)
	err := app.Create(ctx, CreateOpts{Title: "T", Type: "bug", BlockedBy: []int{99}})
	if err == nil || !strings.Contains(err.Error(), "--blocked-by 99 failed") {
		t.Errorf("err = %v", err)
	}
	err = app.Create(ctx, CreateOpts{Title: "T", Type: "bug", Parent: 99})
	if err == nil || !strings.Contains(err.Error(), "--parent 99 failed") {
		t.Errorf("err = %v", err)
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
		CreatedLabels []string `json:"createdLabels"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.CreatedLabels) != len(conventions.Labels) {
		t.Errorf("JSON = %s", out.String())
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
