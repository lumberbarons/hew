package daemon

import (
	"fmt"
	"testing"

	"github.com/lumberbarons/hew/tracker"
)

func TestApplyKeyDependsOnThePlanAndWhoIsApplyingIt(t *testing.T) {
	plan := []byte(`{"entries":[]}`)
	first, again := applyKey(plan, "alice"), applyKey(plan, "alice")
	if first != again {
		t.Error("the same plan by the same actor got two keys, so a retry would not resume")
	}
	if applyKey(plan, "alice") == applyKey(plan, "bob") {
		t.Error("two actors share a key: one would resume the other's half-done plan")
	}
	if applyKey(plan, "alice") == applyKey([]byte(`{"entries":[1]}`), "alice") {
		t.Error("two plans share a key")
	}
	// "ab"+"c" and "a"+"bc" must not collide through the separator.
	if applyKey([]byte("ab"), "c") == applyKey([]byte("a"), "bc") {
		t.Error("the plan and actor run together without a separator")
	}
}

func TestApplyRunsHandOutOneRunPerPlan(t *testing.T) {
	var runs applyRuns
	a := runs.acquire("a")
	if runs.acquire("a") != a {
		t.Error("the same plan got a different run, so a retry would start over")
	}
	if runs.acquire("b") == a {
		t.Error("two plans shared a run")
	}
}

func TestAFinishedPlanIsForgottenButAnUnfinishedOneIsKept(t *testing.T) {
	var runs applyRuns
	run := runs.acquire("p")
	runs.release("p", run, false)
	if runs.acquire("p") != run {
		t.Error("an unfinished plan was forgotten, so its retry would repeat it")
	}
	runs.release("p", run, true)
	if runs.acquire("p") == run {
		t.Error("a finished plan was kept, so sending it again would be a no-op")
	}
}

// TestFinishingAnOldRunDoesNotForgetANewerOne: two requests share a run; the
// first finishes and the plan is forgotten; a third starts a fresh run under
// the same key; then the second finishes. It must not delete the third's run.
func TestFinishingAnOldRunDoesNotForgetANewerOne(t *testing.T) {
	var runs applyRuns
	first := runs.acquire("p")
	second := runs.acquire("p") // a double click: same run
	runs.release("p", first, true)
	third := runs.acquire("p") // arrives after the plan was forgotten
	if third == second {
		t.Fatal("setup: the third request should start a new run")
	}
	runs.release("p", second, true)
	if runs.acquire("p") != third {
		t.Error("finishing the old run forgot the newer run, so the newer plan lost its checkpoint")
	}
}

func TestApplyRunsRememberAtMostAFewIdlePlans(t *testing.T) {
	var runs applyRuns
	first := runs.acquire("plan-0")
	runs.release("plan-0", first, false)
	for i := 1; i <= maxApplyRuns; i++ {
		key := fmt.Sprintf("plan-%d", i)
		runs.release(key, runs.acquire(key), false)
	}
	if len(runs.runs) > maxApplyRuns || len(runs.order) > maxApplyRuns {
		t.Errorf("remembers %d plans, want at most %d", len(runs.runs), maxApplyRuns)
	}
	if runs.acquire("plan-0") == first {
		t.Error("the oldest idle plan was not the one evicted")
	}
}

// TestARunInUseIsNeverEvicted: if it were, a retry of that plan would start a
// second checkpoint and run beside the first, creating its issues twice.
func TestARunInUseIsNeverEvicted(t *testing.T) {
	var runs applyRuns
	busy := runs.acquire("busy") // held: a request is still applying it
	for i := 0; i < 2*maxApplyRuns; i++ {
		key := fmt.Sprintf("other-%d", i)
		runs.release(key, runs.acquire(key), false)
	}
	if runs.acquire("busy") != busy {
		t.Error("a plan that was still running was evicted")
	}
}

func TestApplyRunReportsWhatItCreated(t *testing.T) {
	var runs applyRuns
	run := runs.acquire("p")
	if got := run.created(); got == nil || len(got) != 0 {
		t.Errorf("created = %v, want an empty non-nil map", got)
	}
	_ = run.cp.RecordCreated("a", 7)
	if got := run.created(); got["a"] != 7 {
		t.Errorf("created = %v, want a=7", got)
	}
}

func TestApplyRunsUseTheInjectedCheckpoint(t *testing.T) {
	custom := tracker.NewMemoryCheckpoint()
	runs := applyRuns{newCheckpoint: func() tracker.Checkpoint { return custom }}
	if runs.acquire("p").cp != custom {
		t.Error("the injected checkpoint was not used")
	}
}
