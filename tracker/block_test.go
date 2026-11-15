package tracker

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/model"
)

// depStore is a stateful in-memory store for the dependency writes: the reads
// Block and Unblock make, plus the two edge mutations, which really mutate so
// a multi-edge call can be checked afterwards.
type depStore struct {
	gh.IssueStore
	issues  []model.Issue
	calls   []string
	listErr error
	getErr  error
	// addErrAt and removeErrAt fail that 1-based call of the method; 0 never
	// fails. The failing call does not mutate.
	addErrAt    int
	removeErrAt int
	adds        int
	removes     int
	cause       error
}

func (s *depStore) ListIssues(_ context.Context, states []gh.IssueState) ([]model.Issue, error) {
	s.calls = append(s.calls, "ListIssues")
	if s.listErr != nil {
		return nil, s.listErr
	}
	var out []model.Issue
	for _, i := range s.issues {
		if slices.Contains(states, gh.IssueState(i.State)) {
			out = append(out, i)
		}
	}
	return out, nil
}

func (s *depStore) GetIssue(_ context.Context, number int) (model.Issue, error) {
	s.calls = append(s.calls, fmt.Sprintf("GetIssue %d", number))
	if s.getErr != nil {
		return model.Issue{}, s.getErr
	}
	for _, i := range s.issues {
		if i.Number == number {
			return i, nil
		}
	}
	return model.Issue{}, fmt.Errorf("issue #%d not found", number)
}

func (s *depStore) AddBlockedBy(_ context.Context, number, blocker int) error {
	s.calls = append(s.calls, fmt.Sprintf("AddBlockedBy %d %d", number, blocker))
	s.adds++
	if s.addErrAt == s.adds {
		return s.cause
	}
	for n := range s.issues {
		if s.issues[n].Number == number {
			s.issues[n].BlockedBy = append(s.issues[n].BlockedBy, model.Ref{Number: blocker, State: "OPEN"})
		}
	}
	return nil
}

func (s *depStore) RemoveBlockedBy(_ context.Context, number, blocker int) error {
	s.calls = append(s.calls, fmt.Sprintf("RemoveBlockedBy %d %d", number, blocker))
	s.removes++
	if s.removeErrAt == s.removes {
		return s.cause
	}
	for n := range s.issues {
		if s.issues[n].Number != number {
			continue
		}
		s.issues[n].BlockedBy = slices.DeleteFunc(s.issues[n].BlockedBy, func(r model.Ref) bool { return r.Number == blocker })
	}
	return nil
}

func (s *depStore) writes() []string {
	var out []string
	for _, c := range s.calls {
		if strings.HasPrefix(c, "AddBlockedBy") || strings.HasPrefix(c, "RemoveBlockedBy") {
			out = append(out, c)
		}
	}
	return out
}

func blockedBy(number int, blockers ...int) model.Issue {
	issue := fixture(number, fmt.Sprintf("Issue %d", number), "P2", "bug")
	for _, b := range blockers {
		issue.BlockedBy = append(issue.BlockedBy, model.Ref{Number: b, State: "OPEN"})
	}
	return issue
}

func closedIssue(number int, reason string) model.Issue {
	issue := fixture(number, fmt.Sprintf("Issue %d", number), "P2", "bug")
	issue.State = "CLOSED"
	issue.StateReason = reason
	return issue
}

// TestBlockRefusesADirectCycle pins the two-issue back-edge: GitHub would
// reject it too, but the library must catch it first so the refusal is
// uniform with the longer cycles GitHub accepts.
func TestBlockRefusesADirectCycle(t *testing.T) {
	store := &depStore{issues: []model.Issue{fixture(1, "A", "P2", "bug"), blockedBy(2, 1)}}
	_, err := WrapWriter(store).Block(context.Background(), 1, []int{2}, BlockOptions{})
	var cycle *CycleError
	if !errors.As(err, &cycle) {
		t.Fatalf("err = %v, want a CycleError", err)
	}
	if want := []int{1, 2, 1}; !slices.Equal(cycle.Cycle, want) {
		t.Errorf("Cycle = %v, want %v", cycle.Cycle, want)
	}
	if got := store.writes(); len(got) != 0 {
		t.Errorf("writes before the refusal: %v", got)
	}
}

// TestBlockRefusesASelfBlock is the shortest direct cycle; GitHub rejects it
// server-side, and the client check has to refuse it before any write too.
func TestBlockRefusesASelfBlock(t *testing.T) {
	store := &depStore{issues: []model.Issue{fixture(1, "A", "P2", "bug")}}
	_, err := WrapWriter(store).Block(context.Background(), 1, []int{1}, BlockOptions{})
	var cycle *CycleError
	if !errors.As(err, &cycle) {
		t.Fatalf("err = %v, want a CycleError", err)
	}
	if want := []int{1, 1}; !slices.Equal(cycle.Cycle, want) {
		t.Errorf("Cycle = %v, want %v", cycle.Cycle, want)
	}
	if got := store.writes(); len(got) != 0 {
		t.Errorf("writes before the refusal: %v", got)
	}
}

// TestBlockRefusesALongerCycle is the case GitHub's API silently accepts, so
// only this client-side check can keep it out of the graph.
func TestBlockRefusesALongerCycle(t *testing.T) {
	store := &depStore{issues: []model.Issue{
		fixture(1, "A", "P2", "bug"),
		blockedBy(2, 3),
		blockedBy(3, 1),
	}}
	_, err := WrapWriter(store).Block(context.Background(), 1, []int{2}, BlockOptions{})
	var cycle *CycleError
	if !errors.As(err, &cycle) {
		t.Fatalf("err = %v, want a CycleError", err)
	}
	if want := []int{1, 2, 3, 1}; !slices.Equal(cycle.Cycle, want) {
		t.Errorf("Cycle = %v, want %v", cycle.Cycle, want)
	}
	if got := store.writes(); len(got) != 0 {
		t.Errorf("writes before the refusal: %v", got)
	}
}

// TestBlockRefusesAnUnverifiableCycle covers the capped blocker list: a
// missing edge could close a cycle the search cannot see, so a nil cycle is
// not proof the edge is safe.
func TestBlockRefusesAnUnverifiableCycle(t *testing.T) {
	blocker := blockedBy(2, 3)
	blocker.BlockedByTotal = 25
	store := &depStore{issues: []model.Issue{
		fixture(1, "A", "P2", "bug"), blocker, fixture(3, "C", "P2", "bug"),
	}}
	_, err := WrapWriter(store).Block(context.Background(), 1, []int{2}, BlockOptions{})
	var unverifiable *UnverifiableCycleError
	if !errors.As(err, &unverifiable) {
		t.Fatalf("err = %v, want an UnverifiableCycleError", err)
	}
	if unverifiable.Issue != 1 || unverifiable.Blocker != 2 {
		t.Errorf("error = %+v, want issue 1 blocker 2", unverifiable)
	}
	if got := store.writes(); len(got) != 0 {
		t.Errorf("writes before the refusal: %v", got)
	}
}

// TestBlockRefusesAClosedTarget is the guard the CLI's --closed overrides; a
// library caller gets the typed error and the same override option.
func TestBlockRefusesAClosedTarget(t *testing.T) {
	store := &depStore{issues: []model.Issue{closedIssue(1, "NOT_PLANNED"), fixture(2, "B", "P2", "bug")}}
	_, err := WrapWriter(store).Block(context.Background(), 1, []int{2}, BlockOptions{})
	var closed *ClosedError
	if !errors.As(err, &closed) {
		t.Fatalf("err = %v, want a ClosedError", err)
	}
	if closed.Issue != 1 || closed.Reason != "NOT_PLANNED" {
		t.Errorf("error = %+v, want issue 1 not planned", closed)
	}
	if got := store.writes(); len(got) != 0 {
		t.Errorf("writes on a refused closed target: %v", got)
	}

	result, err := WrapWriter(store).Block(context.Background(), 1, []int{2}, BlockOptions{AllowClosed: true})
	if err != nil {
		t.Fatalf("Block with AllowClosed: %v", err)
	}
	if !slices.Equal(result.Added, []int{2}) {
		t.Errorf("Added = %v, want [2]", result.Added)
	}
	if got := store.issues[0].BlockedBy; len(got) != 1 || got[0].Number != 2 {
		t.Errorf("closed target blockedBy = %v, want #2", got)
	}
}

// TestBlockRefusesABlockerThatIsNotOpen covers both ways a blocker can be
// absent from the open graph: closed and nonexistent. Neither blocks.
func TestBlockRefusesABlockerThatIsNotOpen(t *testing.T) {
	store := &depStore{issues: []model.Issue{
		fixture(1, "A", "P2", "bug"), closedIssue(2, "COMPLETED"), fixture(3, "C", "P2", "bug"),
	}}
	for _, blocker := range []int{2, 99} {
		_, err := WrapWriter(store).Block(context.Background(), 1, []int{blocker}, BlockOptions{})
		var notOpen *NotOpenError
		if !errors.As(err, &notOpen) {
			t.Fatalf("Block on #%d: err = %v, want a NotOpenError", blocker, err)
		}
		if notOpen.Issue != 1 || notOpen.Blocker != blocker {
			t.Errorf("error = %+v, want issue 1 blocker %d", notOpen, blocker)
		}
	}
	if got := store.writes(); len(got) != 0 {
		t.Errorf("writes before the refusal: %v", got)
	}
}

// TestBlockReportsAMissingTarget distinguishes "no such issue" from "closed":
// the CLI's retry remedy differs, so the library does too.
func TestBlockReportsAMissingTarget(t *testing.T) {
	store := &depStore{issues: []model.Issue{fixture(2, "B", "P2", "bug")}}
	_, err := WrapWriter(store).Block(context.Background(), 99, []int{2}, BlockOptions{})
	var notFound *NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("err = %v, want a NotFoundError", err)
	}
	if notFound.Issue != 99 {
		t.Errorf("Issue = %d, want 99", notFound.Issue)
	}
}

func TestBlockRejectsAnEmptyBlockerList(t *testing.T) {
	store := &depStore{issues: []model.Issue{fixture(1, "A", "P2", "bug")}}
	_, err := WrapWriter(store).Block(context.Background(), 1, nil, BlockOptions{})
	var invalid *InvalidInputError
	if !errors.As(err, &invalid) || invalid.Field != "blockers" {
		t.Fatalf("err = %v, want an InvalidInputError for blockers", err)
	}
	if len(store.calls) != 0 {
		t.Errorf("calls = %v; a usage error must come before any I/O", store.calls)
	}
}

func TestBlockPropagatesAListFailure(t *testing.T) {
	boom := errors.New("backend down")
	store := &depStore{listErr: boom}
	_, err := WrapWriter(store).Block(context.Background(), 1, []int{2}, BlockOptions{})
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
}

// TestBlockAddsOnlyTheMissingEdges is the multi-blocker contract: every
// refusal is decided before the first write, and the result names both what
// was added and what was already there.
func TestBlockAddsOnlyTheMissingEdges(t *testing.T) {
	store := &depStore{issues: []model.Issue{
		blockedBy(1, 2), fixture(2, "B", "P2", "bug"), fixture(3, "C", "P2", "bug"), fixture(4, "D", "P2", "bug"),
	}}
	result, err := WrapWriter(store).Block(context.Background(), 1, []int{2, 3, 3, 4}, BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.Added, []int{3, 4}) || !slices.Equal(result.Existing, []int{2}) {
		t.Errorf("result = added %v, existing %v; want [3 4] and [2]", result.Added, result.Existing)
	}
	want := []string{"AddBlockedBy 1 3", "AddBlockedBy 1 4"}
	if got := store.writes(); !slices.Equal(got, want) {
		t.Errorf("writes = %v, want %v", got, want)
	}
}

func TestBlockAlreadyBlockedIsANoOp(t *testing.T) {
	store := &depStore{issues: []model.Issue{blockedBy(1, 2, 3), fixture(2, "B", "P2", "bug"), fixture(3, "C", "P2", "bug")}}
	result, err := WrapWriter(store).Block(context.Background(), 1, []int{2, 3}, BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Added) != 0 || !slices.Equal(result.Existing, []int{2, 3}) {
		t.Errorf("result = added %v, existing %v; want no adds and [2 3]", result.Added, result.Existing)
	}
	if got := store.writes(); len(got) != 0 {
		t.Errorf("writes for an already-blocked issue: %v", got)
	}
}

// TestBlockNamesTheEdgesThatLanded: a failure after an earlier edge landed
// must not let the caller retry the whole set or assume the rest exists.
func TestBlockNamesTheEdgesThatLanded(t *testing.T) {
	store := &depStore{
		issues: []model.Issue{
			fixture(1, "A", "P2", "bug"), fixture(2, "B", "P2", "bug"),
			fixture(3, "C", "P2", "bug"), fixture(4, "D", "P2", "bug"),
		},
		addErrAt: 2, cause: errors.New("boom"),
	}
	_, err := WrapWriter(store).Block(context.Background(), 1, []int{2, 3, 4}, BlockOptions{})
	var partial *PartialWriteError
	if !errors.As(err, &partial) {
		t.Fatalf("err = %v, want a PartialWriteError", err)
	}
	if !slices.Equal(partial.Landed, []int{2}) || partial.Failed != 3 || !slices.Equal(partial.NotAttempted, []int{4}) {
		t.Errorf("partial = %+v, want landed [2], failed 3, not attempted [4]", partial)
	}
	if !errors.Is(err, store.cause) {
		t.Errorf("err = %v, want the backend cause retained", err)
	}
	if got := store.issues[0].BlockedBy; len(got) != 1 || got[0].Number != 2 {
		t.Errorf("blockedBy = %v, want just #2", got)
	}

	// A failure on the first write changed nothing, so the backend error
	// stands as it is rather than gaining partial-write framing.
	store = &depStore{
		issues:   []model.Issue{fixture(1, "A", "P2", "bug"), fixture(2, "B", "P2", "bug"), fixture(3, "C", "P2", "bug")},
		addErrAt: 1, cause: errors.New("boom"),
	}
	if _, err := WrapWriter(store).Block(context.Background(), 1, []int{2, 3}, BlockOptions{}); err == nil || err.Error() != "boom" {
		t.Errorf("first-write failure = %v, want the bare cause", err)
	}
}

func TestUnblockRemovesOnlyTheEdgesThatExist(t *testing.T) {
	store := &depStore{issues: []model.Issue{
		blockedBy(1, 2, 3), fixture(2, "B", "P2", "bug"), fixture(3, "C", "P2", "bug"),
	}}
	result, err := WrapWriter(store).Unblock(context.Background(), 1, []int{2, 3, 3, 99}, BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.Removed, []int{2, 3}) || !slices.Equal(result.Missing, []int{99}) {
		t.Errorf("result = removed %v, missing %v; want [2 3] and [99]", result.Removed, result.Missing)
	}
	want := []string{"RemoveBlockedBy 1 2", "RemoveBlockedBy 1 3"}
	if got := store.writes(); !slices.Equal(got, want) {
		t.Errorf("writes = %v, want %v", got, want)
	}
}

func TestUnblockNotBlockedIsANoOp(t *testing.T) {
	store := &depStore{issues: []model.Issue{fixture(1, "A", "P2", "bug"), fixture(2, "B", "P2", "bug")}}
	result, err := WrapWriter(store).Unblock(context.Background(), 1, []int{2}, BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Removed) != 0 || !slices.Equal(result.Missing, []int{2}) {
		t.Errorf("result = %+v, want #2 missing", result)
	}
	if got := store.writes(); len(got) != 0 {
		t.Errorf("writes for an unblocked issue: %v", got)
	}
}

func TestUnblockRefusesAClosedTarget(t *testing.T) {
	store := &depStore{issues: []model.Issue{closedIssue(1, "COMPLETED"), fixture(2, "B", "P2", "bug")}}
	store.issues[0].BlockedBy = []model.Ref{{Number: 2, State: "OPEN"}}
	_, err := WrapWriter(store).Unblock(context.Background(), 1, []int{2}, BlockOptions{})
	var closed *ClosedError
	if !errors.As(err, &closed) {
		t.Fatalf("err = %v, want a ClosedError", err)
	}
	if closed.Reason != "COMPLETED" {
		t.Errorf("Reason = %q, want COMPLETED", closed.Reason)
	}
	if got := store.writes(); len(got) != 0 {
		t.Errorf("writes on a refused closed target: %v", got)
	}

	if _, err := WrapWriter(store).Unblock(context.Background(), 1, []int{2}, BlockOptions{AllowClosed: true}); err != nil {
		t.Fatalf("Unblock with AllowClosed: %v", err)
	}
	if got := store.issues[0].BlockedBy; len(got) != 0 {
		t.Errorf("closed target blockedBy = %v, want none", got)
	}
}

func TestUnblockRejectsAnEmptyBlockerList(t *testing.T) {
	store := &depStore{issues: []model.Issue{fixture(1, "A", "P2", "bug")}}
	_, err := WrapWriter(store).Unblock(context.Background(), 1, nil, BlockOptions{})
	var invalid *InvalidInputError
	if !errors.As(err, &invalid) || invalid.Field != "blockers" {
		t.Fatalf("err = %v, want an InvalidInputError for blockers", err)
	}
	if len(store.calls) != 0 {
		t.Errorf("calls = %v; a usage error must come before any I/O", store.calls)
	}
}

func TestUnblockPropagatesAGetFailure(t *testing.T) {
	boom := errors.New("missing")
	store := &depStore{getErr: boom}
	_, err := WrapWriter(store).Unblock(context.Background(), 1, []int{2}, BlockOptions{})
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
}

func TestUnblockNamesTheEdgesThatLanded(t *testing.T) {
	store := &depStore{
		issues:      []model.Issue{blockedBy(1, 2, 3), fixture(2, "B", "P2", "bug"), fixture(3, "C", "P2", "bug")},
		removeErrAt: 2, cause: errors.New("boom"),
	}
	_, err := WrapWriter(store).Unblock(context.Background(), 1, []int{2, 3}, BlockOptions{})
	var partial *PartialWriteError
	if !errors.As(err, &partial) {
		t.Fatalf("err = %v, want a PartialWriteError", err)
	}
	if !slices.Equal(partial.Landed, []int{2}) || partial.Failed != 3 || len(partial.NotAttempted) != 0 {
		t.Errorf("partial = %+v, want landed [2], failed 3, nothing unattempted", partial)
	}
	if !errors.Is(err, store.cause) {
		t.Errorf("err = %v, want the backend cause retained", err)
	}

	store = &depStore{
		issues:      []model.Issue{blockedBy(1, 2, 3), fixture(2, "B", "P2", "bug"), fixture(3, "C", "P2", "bug")},
		removeErrAt: 1, cause: errors.New("boom"),
	}
	if _, err := WrapWriter(store).Unblock(context.Background(), 1, []int{2, 3}, BlockOptions{}); err == nil || err.Error() != "boom" {
		t.Errorf("first-write failure = %v, want the bare cause", err)
	}
}

// A Tracker Open returns must expose the same dependency writes as the
// CLI's Writer seam.
func TestTrackerExposesTheDependencyWrites(t *testing.T) {
	store := &depStore{issues: []model.Issue{fixture(1, "A", "P2", "bug"), fixture(2, "B", "P2", "bug")}}
	tracker := &Tracker{store: store}
	if _, err := tracker.Block(context.Background(), 1, []int{2}, BlockOptions{}); err != nil {
		t.Fatalf("Block: %v", err)
	}
	if _, err := tracker.Unblock(context.Background(), 1, []int{2}, BlockOptions{}); err != nil {
		t.Fatalf("Unblock: %v", err)
	}
	if got := store.issues[0].BlockedBy; len(got) != 0 {
		t.Errorf("blockedBy = %v, want the edge added then removed", got)
	}
}
