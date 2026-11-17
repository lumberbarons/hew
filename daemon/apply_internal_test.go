package daemon

import (
	"fmt"
	"testing"

	"github.com/lumberbarons/hew/tracker"
)

func TestApplyRunsHandOutOneRunPerPlan(t *testing.T) {
	var runs applyRuns
	a := runs.get("sha256:a")
	if runs.get("sha256:a") != a {
		t.Error("the same plan got a different run, so a retry would start over")
	}
	if runs.get("sha256:b") == a {
		t.Error("two plans shared a run")
	}
	runs.forget("sha256:a")
	if runs.get("sha256:a") == a {
		t.Error("a forgotten plan kept its run, so sending it again would be a no-op")
	}
	runs.forget("sha256:missing") // forgetting what is not there is harmless
}

func TestApplyRunsRememberAtMostAFewUnfinishedPlans(t *testing.T) {
	var runs applyRuns
	first := runs.get("plan-0")
	for i := 1; i <= maxApplyRuns; i++ {
		runs.get(fmt.Sprintf("plan-%d", i))
	}
	if len(runs.runs) > maxApplyRuns || len(runs.order) > maxApplyRuns {
		t.Errorf("remembers %d plans, want at most %d", len(runs.runs), maxApplyRuns)
	}
	if runs.get("plan-0") == first {
		t.Error("the oldest plan was not the one evicted")
	}
}

func TestApplyRunReportsWhatItCreated(t *testing.T) {
	var runs applyRuns
	run := runs.get("p")
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
	if runs.get("p").cp != custom {
		t.Error("the injected checkpoint was not used")
	}
}
