package tracker

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/local"
	"github.com/lumberbarons/hew/internal/model"
)

// startFixture is a local tracker with one triaged issue, plus the store that
// backs it, so a test can build Trackers with the identity each case needs.
func startFixture(t *testing.T, labels ...string) (context.Context, *local.Store, model.Issue) {
	t.Helper()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), local.DirName)
	if err := local.Init(dir); err != nil {
		t.Fatal(err)
	}
	store, err := local.Open(dir, "actor-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(labels) == 0 {
		labels = []string{"P2", "task"}
	}
	issue, err := store.CreateIssue(ctx, "Work", "", labels)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, store, issue
}

func TestStartClaimsAsTheActingIdentity(t *testing.T) {
	ctx, store, issue := startFixture(t)
	tr := WrapAs(store, BackendLocal, "actor-a")
	if tr.Backend() != BackendLocal || tr.Identity() != "actor-a" {
		t.Fatalf("WrapAs = backend %q identity %q", tr.Backend(), tr.Identity())
	}

	got, err := tr.Start(ctx, issue.Number, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Claimant != "actor-a" {
		t.Errorf("Claimant = %q, want actor-a", got.Claimant)
	}
	if !slices.Equal(got.Issue.Assignees, []string{"actor-a"}) {
		t.Errorf("assignees = %v, want only the acting identity", got.Issue.Assignees)
	}
	if !got.Issue.InProgress {
		t.Error("claim did not add the in-progress label")
	}
	// The result carries the post-claim read, body included: a caller that
	// renders it must not need a second read.
	if got.Issue.Body == nil {
		t.Error("Body missing from the claim result")
	}
	read, err := tr.Get(ctx, issue.Number)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(read.Assignees, []string{"actor-a"}) || !read.InProgress {
		t.Errorf("re-read assignees = %v, in-progress %v", read.Assignees, read.InProgress)
	}
}

// The local backend's atomic claim is what makes two agents racing on one
// tracker safe: exactly one wins, and the loser is told who holds it.
func TestStartConcurrentActorsHaveExactlyOneWinner(t *testing.T) {
	ctx := context.Background()
	main, worktrees := repoWithWorktrees(t, "agent-one", "agent-two")
	initLocalTracker(t, main)
	seed, err := local.Open(filepath.Join(main, local.DirName), "seed")
	if err != nil {
		t.Fatal(err)
	}
	issue, err := seed.CreateIssue(ctx, "Contested", "", []string{"P2", "task"})
	if err != nil {
		t.Fatal(err)
	}

	one, err := Open(ctx, Config{Worktree: worktrees[0], Actor: "actor-a"})
	if err != nil {
		t.Fatal(err)
	}
	two, err := Open(ctx, Config{Worktree: worktrees[1], Actor: "actor-b"})
	if err != nil {
		t.Fatal(err)
	}

	type outcome struct {
		tracker *Tracker
		result  StartResult
		err     error
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	for _, tr := range []*Tracker{one, two} {
		go func() {
			<-start
			result, err := tr.Start(ctx, issue.Number, StartOptions{})
			results <- outcome{tracker: tr, result: result, err: err}
		}()
	}
	close(start)

	var winners []outcome
	var loser *ClaimedError
	for range 2 {
		got := <-results
		if got.err == nil {
			winners = append(winners, got)
			continue
		}
		if !errors.As(got.err, &loser) {
			t.Fatalf("loser error = %v, want a ClaimedError", got.err)
		}
	}
	if len(winners) != 1 {
		t.Fatalf("winners = %d, want exactly 1", len(winners))
	}
	if loser == nil {
		t.Fatal("no claim refusal: both starts reported success")
	}
	if !slices.Equal(loser.Assignees, []string{winners[0].tracker.Identity()}) {
		t.Errorf("refusal names %v, want the winner %q", loser.Assignees, winners[0].tracker.Identity())
	}
	if loser.ByActor {
		t.Errorf("loser read the winner's claim as its own: %+v", loser)
	}
}

// A claim made as A resumes as A and reads as someone else's claim for B: the
// same actor rule the CLI's HEW_USER spells.
func TestStartClaimReadsAsByActorOnlyForTheClaimant(t *testing.T) {
	ctx := context.Background()
	main, _ := repoWithWorktrees(t)
	initLocalTracker(t, main)
	base, err := Open(ctx, Config{Worktree: main, Actor: "actor-a"})
	if err != nil {
		t.Fatal(err)
	}
	store := base.store.(*local.Store)
	issue, err := store.CreateIssue(ctx, "Claimed by A", "", []string{"P2", "task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base.Start(ctx, issue.Number, StartOptions{}); err != nil {
		t.Fatal(err)
	}

	var claimed *ClaimedError
	if _, err := base.Start(ctx, issue.Number, StartOptions{}); !errors.As(err, &claimed) {
		t.Fatalf("actor A retry = %v, want a ClaimedError", err)
	}
	if !claimed.ByActor || claimed.Actor != "actor-a" {
		t.Errorf("actor A claim = %+v, want ByActor for actor-a", claimed)
	}
	claimed = nil
	if _, err := base.As("actor-b").Start(ctx, issue.Number, StartOptions{}); !errors.As(err, &claimed) {
		t.Fatalf("actor B = %v, want a ClaimedError", err)
	}
	if claimed.ByActor || claimed.Actor != "actor-b" {
		t.Errorf("actor B claim = %+v, want someone else's claim", claimed)
	}
	if !slices.Equal(claimed.Assignees, []string{"actor-a"}) {
		t.Errorf("refusal names %v, want actor-a", claimed.Assignees)
	}
}

func TestStartRefusesClosedAndEpicTargets(t *testing.T) {
	ctx, store, closed := startFixture(t)
	if err := store.CloseIssue(ctx, closed.Number, gh.CloseCompleted); err != nil {
		t.Fatal(err)
	}
	epic, err := store.CreateIssue(ctx, "Epic", "", []string{"P2", "task", "epic"})
	if err != nil {
		t.Fatal(err)
	}
	tr := WrapAs(store, BackendLocal, "actor-a")

	var closedErr *ClosedError
	if _, err := tr.Start(ctx, closed.Number, StartOptions{}); !errors.As(err, &closedErr) {
		t.Fatalf("closed start = %v, want a ClosedError", err)
	}
	if closedErr.Issue != closed.Number || closedErr.Reason != string(gh.CloseCompleted) {
		t.Errorf("ClosedError = %+v", closedErr)
	}
	var epicErr *EpicError
	if _, err := tr.Start(ctx, epic.Number, StartOptions{}); !errors.As(err, &epicErr) {
		t.Fatalf("epic start = %v, want an EpicError", err)
	}
	if epicErr.Issue != epic.Number {
		t.Errorf("EpicError = %+v", epicErr)
	}
}

func TestStartRefusesOpenBlockersAndForceClaims(t *testing.T) {
	ctx, store, issue := startFixture(t)
	blocker, err := store.CreateIssue(ctx, "Blocker", "", []string{"P2", "task"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddBlockedBy(ctx, issue.Number, blocker.Number); err != nil {
		t.Fatal(err)
	}
	tr := WrapAs(store, BackendLocal, "actor-a")

	var blocked *BlockedError
	if _, err := tr.Start(ctx, issue.Number, StartOptions{}); !errors.As(err, &blocked) {
		t.Fatalf("blocked start = %v, want a BlockedError", err)
	}
	if blocked.Issue != issue.Number || !slices.Equal(blocked.OpenBlockers, []int{blocker.Number}) {
		t.Errorf("BlockedError = %+v", blocked)
	}
	if _, err := tr.Start(ctx, issue.Number, StartOptions{Force: true}); err != nil {
		t.Fatalf("forced start: %v", err)
	}
}

func TestStartRequiresAPriorityForAnUntriagedIssue(t *testing.T) {
	ctx, store, issue := startFixture(t, "task")
	tr := WrapAs(store, BackendLocal, "actor-a")

	var untriaged *UntriagedError
	if _, err := tr.Start(ctx, issue.Number, StartOptions{}); !errors.As(err, &untriaged) {
		t.Fatalf("untriaged start = %v, want an UntriagedError", err)
	}
	if untriaged.Issue != issue.Number {
		t.Errorf("UntriagedError = %+v", untriaged)
	}
	var invalid *InvalidInputError
	if _, err := tr.Start(ctx, issue.Number, StartOptions{Priority: "P9"}); !errors.As(err, &invalid) {
		t.Fatalf("bad priority = %v, want an InvalidInputError", err)
	}
	if invalid.Field != "priority" || invalid.Reason != "must be P0..P4" {
		t.Errorf("InvalidInputError = %+v", invalid)
	}
	got, err := tr.Start(ctx, issue.Number, StartOptions{Priority: "P1"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Issue.HasLabel("P1") || !got.Issue.HasLabel("task") {
		t.Errorf("labels = %v, want P1 and task", got.Issue.Labels())
	}
}

// A priority already in place needs no write; a different one is swapped, not
// stacked.
func TestStartSwapsOrKeepsThePriorityLabel(t *testing.T) {
	ctx, store, issue := startFixture(t, "P1", "task")
	tr := WrapAs(store, BackendLocal, "actor-a")
	if _, err := tr.Start(ctx, issue.Number, StartOptions{Priority: "P1"}); err != nil {
		t.Fatal(err)
	}
	if labels := mustGet(t, ctx, tr, issue.Number).Labels(); slices.Contains(labels, "P2") || !slices.Contains(labels, "P1") {
		t.Errorf("labels = %v, want only P1", labels)
	}

	other, err := store.CreateIssue(ctx, "Other", "", []string{"P2", "task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Start(ctx, other.Number, StartOptions{Priority: "P0"}); err != nil {
		t.Fatal(err)
	}
	if labels := mustGet(t, ctx, tr, other.Number).Labels(); slices.Contains(labels, "P2") || !slices.Contains(labels, "P0") {
		t.Errorf("labels = %v, want only P0", labels)
	}
}

func mustGet(t *testing.T, ctx context.Context, tr *Tracker, number int) Issue {
	t.Helper()
	issue, err := tr.Get(ctx, number)
	if err != nil {
		t.Fatal(err)
	}
	return issue
}

// swapFailingStore fails the priority label write that follows a successful
// atomic claim, so a test can see what Start leaves behind.
type swapFailingStore struct {
	*local.Store
	failRemove bool
}

func (s *swapFailingStore) AddLabels(ctx context.Context, number int, labels []string) error {
	for _, l := range labels {
		if _, ok := model.ParsePriority(l); ok {
			return errors.New("disk full")
		}
	}
	return s.Store.AddLabels(ctx, number, labels)
}

func (s *swapFailingStore) RemoveAssignees(ctx context.Context, number int, logins []string) error {
	if s.failRemove {
		return errors.New("lock timeout")
	}
	return s.Store.RemoveAssignees(ctx, number, logins)
}

// Claiming is triage, so a failed priority swap must not leave the issue
// claimed: a retry would be told the claim is already its own instead of
// finishing the triage.
func TestStartReleasesTheClaimWhenThePrioritySwapFails(t *testing.T) {
	for _, tc := range []struct {
		name      string
		prior     []string // earlier claimants, only reachable with Force
		force     bool
		wantAfter []string
	}{
		{"fresh claim", nil, false, nil},
		{"forced claim restores the displaced claimant", []string{"actor-a"}, true, []string{"actor-a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, store, issue := startFixture(t, "task")
			for _, who := range tc.prior {
				if err := store.ClaimIssue(ctx, issue.Number, who, false); err != nil {
					t.Fatal(err)
				}
			}
			tr := WrapAs(&swapFailingStore{Store: store}, BackendLocal, "actor-b")
			_, err := tr.Start(ctx, issue.Number, StartOptions{Priority: "P1", Force: tc.force})
			if !contains(err, "disk full") {
				t.Fatalf("err = %v, want the swap failure", err)
			}
			got, err := store.GetIssue(ctx, issue.Number)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got.Assignees, tc.wantAfter) {
				t.Errorf("assignees = %v, want %v", got.Assignees, tc.wantAfter)
			}
			if got.InProgress() != (len(tc.prior) > 0) {
				t.Errorf("in-progress = %v with prior claimants %v", got.InProgress(), tc.prior)
			}
			if p, _ := got.Priority(); p != model.PriorityUnknown {
				t.Errorf("priority = %v, want the issue still untriaged", p)
			}
		})
	}
}

func TestStartReportsAFailedRollbackAlongsideTheSwapFailure(t *testing.T) {
	ctx, store, issue := startFixture(t, "task")
	tr := WrapAs(&swapFailingStore{Store: store, failRemove: true}, BackendLocal, "actor-b")
	_, err := tr.Start(ctx, issue.Number, StartOptions{Priority: "P1"})
	if err == nil || !contains(err, "disk full", "could not release the claim") {
		t.Errorf("err = %v, want both the swap failure and the failed release", err)
	}
}

// racingStore wins the claim as rival just before delegating, standing in for
// another agent whose claim lands after Start's read — the window an atomic
// claim exists to close.
type racingStore struct {
	*local.Store
	rival string
}

func (r *racingStore) ClaimIssue(ctx context.Context, number int, login string, force bool) error {
	_ = r.Store.ClaimIssue(ctx, number, r.rival, false)
	return r.Store.ClaimIssue(ctx, number, login, force)
}

func TestStartNamesTheAtomicClaimConflictWinner(t *testing.T) {
	for _, tc := range []struct {
		name    string
		login   string
		rival   string
		byActor bool
	}{
		{"another agent won", "actor-b", "actor-a", false},
		{"a sibling session with the same identity won", "actor-a", "actor-a", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, store, issue := startFixture(t)
			tr := WrapAs(&racingStore{Store: store, rival: tc.rival}, BackendLocal, tc.login)
			_, err := tr.Start(ctx, issue.Number, StartOptions{})
			var claimed *ClaimedError
			if !errors.As(err, &claimed) {
				t.Fatalf("err = %v, want a ClaimedError", err)
			}
			if claimed.ByActor != tc.byActor || !slices.Equal(claimed.Assignees, []string{tc.rival}) {
				t.Errorf("claim refusal = %+v, want rival %q (by actor %v)", claimed, tc.rival, tc.byActor)
			}
			got, err := store.GetIssue(ctx, issue.Number)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got.Assignees, []string{tc.rival}) {
				t.Errorf("assignees = %v, want only the race winner %q", got.Assignees, tc.rival)
			}
		})
	}
}

// viewerStore overrides the store's identity lookup, so a test can make it
// fail or count how often Start asks.
type viewerStore struct {
	*local.Store
	calls int
	err   error
}

func (s *viewerStore) Viewer(ctx context.Context) (string, error) {
	s.calls++
	if s.err != nil {
		return "", s.err
	}
	return s.Store.Viewer(ctx)
}

// WrapAs with no actor defers the identity to the store: a refusal that cannot
// name the claimant says so in the typed error, and a closed target is refused
// before the lookup is paid for.
func TestWrapAsDefersTheIdentityUntilAClaimNeedsIt(t *testing.T) {
	ctx, store, issue := startFixture(t)
	boom := errors.New("network down")
	viewer := &viewerStore{Store: store, err: boom}
	tr := WrapAs(viewer, BackendGitHub, "")

	if _, err := tr.Start(ctx, issue.Number, StartOptions{}); !errors.Is(err, boom) {
		t.Fatalf("unclaimed start = %v, want the identity lookup failure", err)
	}
	if viewer.calls != 1 {
		t.Errorf("Viewer calls = %d, want 1", viewer.calls)
	}
	got, err := store.GetIssue(ctx, issue.Number)
	if err != nil {
		t.Fatal(err)
	}
	if got.Claimed() {
		t.Errorf("identity failure still claimed the issue: %+v", got)
	}

	// A closed target is refused before the identity is needed.
	closed, err := store.CreateIssue(ctx, "Closed", "", []string{"P2", "task"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CloseIssue(ctx, closed.Number, gh.CloseCompleted); err != nil {
		t.Fatal(err)
	}
	viewer.calls = 0
	var closedErr *ClosedError
	if _, err := tr.Start(ctx, closed.Number, StartOptions{}); !errors.As(err, &closedErr) {
		t.Fatalf("closed start = %v, want a ClosedError", err)
	}
	if viewer.calls != 0 {
		t.Errorf("viewer lookup made for a closed target: %d calls", viewer.calls)
	}

	// A claim held by somebody is refused, and the error carries why the
	// claimant could not be attributed rather than silently blaming a stranger.
	if err := store.ClaimIssue(ctx, issue.Number, "someone-else", false); err != nil {
		t.Fatal(err)
	}
	viewer.calls = 0
	var claimed *ClaimedError
	if _, err := tr.Start(ctx, issue.Number, StartOptions{}); !errors.As(err, &claimed) {
		t.Fatalf("claimed start = %v, want a ClaimedError", err)
	}
	if !errors.Is(claimed.Err, boom) || claimed.ByActor || claimed.Actor != "" {
		t.Errorf("ClaimedError = %+v, want the lookup failure in Err", claimed)
	}
	if viewer.calls != 1 {
		t.Errorf("Viewer calls = %d, want 1", viewer.calls)
	}
}

// A store whose optional atomic claim is hidden falls back to check-then-act,
// the path a backend without conditional writes uses.
type storeOnly struct{ gh.IssueStore }

func TestStartCheckThenActClaimAndForceSteal(t *testing.T) {
	ctx, store, issue := startFixture(t)
	tr := WrapAs(storeOnly{store}, BackendGitHub, "actor-a")
	if _, err := tr.Start(ctx, issue.Number, StartOptions{}); err != nil {
		t.Fatal(err)
	}

	if err := store.ClaimIssue(ctx, issue.Number, "actor-z", true); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Start(ctx, issue.Number, StartOptions{Force: true}); err != nil {
		t.Fatalf("forced start: %v", err)
	}
	got, err := store.GetIssue(ctx, issue.Number)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Assignees, []string{"actor-a"}) || !got.InProgress() {
		t.Errorf("after force: assignees %v in-progress %v, want only actor-a", got.Assignees, got.InProgress())
	}
}

func TestStartReportsAnUnreadableIssue(t *testing.T) {
	ctx, store, _ := startFixture(t)
	tr := WrapAs(store, BackendLocal, "actor-a")
	if _, err := tr.Start(ctx, 999, StartOptions{}); err == nil {
		t.Fatal("missing issue reported success")
	}
}

// A claim with no assignee is still a claim: the in-progress label is the
// other half, and there is nobody to name or look up.
func TestStartRefusesALabelOnlyClaim(t *testing.T) {
	ctx, store, issue := startFixture(t)
	if err := store.AddLabels(ctx, issue.Number, []string{model.InProgressLabel}); err != nil {
		t.Fatal(err)
	}
	viewer := &viewerStore{Store: store, err: errors.New("network down")}
	var claimed *ClaimedError
	if _, err := WrapAs(viewer, BackendLocal, "").Start(ctx, issue.Number, StartOptions{}); !errors.As(err, &claimed) {
		t.Fatalf("label-only start = %v, want a ClaimedError", err)
	}
	if len(claimed.Assignees) != 0 || claimed.Err != nil || claimed.Actor != "" {
		t.Errorf("ClaimedError = %+v, want no claimant and no lookup", claimed)
	}
	if viewer.calls != 0 {
		t.Errorf("Viewer calls = %d; a label-only refusal has no claimant to resolve", viewer.calls)
	}
}

// instrumented forwards every store method, letting a test fail one step of
// the claim without a fake that drifts from the real backend.
type instrumented struct {
	*local.Store
	addLabels       func(context.Context, int, []string) error
	addAssignee     func(context.Context, int, string) error
	removeLabel     func(context.Context, int, string) error
	removeAssignees func(context.Context, int, []string) error
	claimIssue      func(context.Context, int, string, bool) error
	getIssue        func(context.Context, int) (model.Issue, error)
}

func (s *instrumented) AddLabels(ctx context.Context, number int, labels []string) error {
	if s.addLabels != nil {
		return s.addLabels(ctx, number, labels)
	}
	return s.Store.AddLabels(ctx, number, labels)
}

func (s *instrumented) AddAssignee(ctx context.Context, number int, login string) error {
	if s.addAssignee != nil {
		return s.addAssignee(ctx, number, login)
	}
	return s.Store.AddAssignee(ctx, number, login)
}

func (s *instrumented) RemoveLabel(ctx context.Context, number int, label string) error {
	if s.removeLabel != nil {
		return s.removeLabel(ctx, number, label)
	}
	return s.Store.RemoveLabel(ctx, number, label)
}

func (s *instrumented) RemoveAssignees(ctx context.Context, number int, logins []string) error {
	if s.removeAssignees != nil {
		return s.removeAssignees(ctx, number, logins)
	}
	return s.Store.RemoveAssignees(ctx, number, logins)
}

func (s *instrumented) ClaimIssue(ctx context.Context, number int, login string, force bool) error {
	if s.claimIssue != nil {
		return s.claimIssue(ctx, number, login, force)
	}
	return s.Store.ClaimIssue(ctx, number, login, force)
}

func (s *instrumented) GetIssue(ctx context.Context, number int) (model.Issue, error) {
	if s.getIssue != nil {
		return s.getIssue(ctx, number)
	}
	return s.Store.GetIssue(ctx, number)
}

// Every write Start makes propagates its failure: a claim that cannot finish
// must not report success.
func TestStartPropagatesWriteFailures(t *testing.T) {
	boom := errors.New("write failed")
	for _, tc := range []struct {
		name      string
		atomic    bool
		force     bool
		opts      StartOptions
		configure func(*instrumented)
	}{
		{
			name:      "priority label removal",
			opts:      StartOptions{Priority: "P0"},
			configure: func(s *instrumented) { s.removeLabel = func(context.Context, int, string) error { return boom } },
		},
		{
			name:      "in-progress label",
			configure: func(s *instrumented) { s.addLabels = func(context.Context, int, []string) error { return boom } },
		},
		{
			name:  "displaced claimant removal",
			force: true,
			opts:  StartOptions{Force: true},
			configure: func(s *instrumented) {
				s.removeAssignees = func(context.Context, int, []string) error { return boom }
			},
		},
		{
			name:      "assignee",
			configure: func(s *instrumented) { s.addAssignee = func(context.Context, int, string) error { return boom } },
		},
		{
			name: "final re-read",
			configure: func(s *instrumented) {
				calls := 0
				s.getIssue = func(ctx context.Context, number int) (model.Issue, error) {
					if calls++; calls == 2 {
						return model.Issue{}, boom
					}
					return s.Store.GetIssue(ctx, number)
				}
			},
		},
		{
			name:   "atomic claim failure",
			atomic: true,
			configure: func(s *instrumented) {
				s.claimIssue = func(context.Context, int, string, bool) error { return boom }
			},
		},
		{
			name:   "conflict re-read",
			atomic: true,
			configure: func(s *instrumented) {
				s.claimIssue = func(context.Context, int, string, bool) error {
					return fmt.Errorf("wrapped: %w", gh.ErrClaimConflict)
				}
				calls := 0
				s.getIssue = func(ctx context.Context, number int) (model.Issue, error) {
					if calls++; calls == 2 {
						return model.Issue{}, boom
					}
					return s.Store.GetIssue(ctx, number)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, store, issue := startFixture(t)
			number := issue.Number
			if tc.force {
				displaced, err := store.CreateIssue(ctx, "Displaced", "", []string{"P2", "task"})
				if err != nil {
					t.Fatal(err)
				}
				if err := store.ClaimIssue(ctx, displaced.Number, "someone-else", false); err != nil {
					t.Fatal(err)
				}
				number = displaced.Number
			}
			instrument := &instrumented{Store: store}
			tc.configure(instrument)
			var wrapped gh.IssueStore = instrument
			if !tc.atomic {
				wrapped = storeOnly{instrument}
			}
			tr := WrapAs(wrapped, BackendLocal, "actor-a")
			if _, err := tr.Start(ctx, number, tc.opts); !errors.Is(err, boom) {
				t.Fatalf("err = %v, want the write failure", err)
			}
		})
	}
}

func contains(err error, fragments ...string) bool {
	if err == nil {
		return false
	}
	for _, fragment := range fragments {
		if !strings.Contains(err.Error(), fragment) {
			return false
		}
	}
	return true
}
