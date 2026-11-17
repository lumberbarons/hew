package daemon

import (
	"sync"

	"github.com/lumberbarons/hew/tracker"
)

// An apply that stops partway has already created some issues. Sending the
// same plan again must resume after them, not create them a second time, and
// the library does exactly that when it is handed the same checkpoint. A
// checkpoint made per request would be thrown away with the request, so the
// server keeps the checkpoint of every plan that did not finish, keyed by a
// digest of the plan.

// maxApplyRuns bounds how many unfinished plans are remembered. An evicted
// plan simply starts over, which is what happened before they were kept.
const maxApplyRuns = 16

type applyRun struct {
	// mu serialises runs of one plan: MemoryCheckpoint has no lock, and two
	// concurrent sends of the same plan (a double click) must not both create
	// its entries. The second waits, then finds them all created.
	mu sync.Mutex
	cp tracker.Checkpoint
}

// created reports what the run has made so far, by plan entry key.
func (r *applyRun) created() map[string]int {
	out := map[string]int{}
	for _, e := range r.cp.CreatedEntries() {
		out[e.Key] = e.Number
	}
	return out
}

type applyRuns struct {
	mu    sync.Mutex
	runs  map[string]*applyRun
	order []string // oldest first

	// newCheckpoint makes the record a new plan resumes from. Nil means an
	// in-memory one; see Server.WithCheckpoints.
	newCheckpoint func() tracker.Checkpoint
}

// get returns the run for a plan digest, starting one if none is remembered.
func (a *applyRuns) get(digest string) *applyRun {
	a.mu.Lock()
	defer a.mu.Unlock()
	if run, ok := a.runs[digest]; ok {
		return run
	}
	if a.runs == nil {
		a.runs = map[string]*applyRun{}
	}
	for len(a.order) >= maxApplyRuns {
		delete(a.runs, a.order[0])
		a.order = a.order[1:]
	}
	run := &applyRun{cp: tracker.NewMemoryCheckpoint()}
	if a.newCheckpoint != nil {
		run.cp = a.newCheckpoint()
	}
	a.runs[digest] = run
	a.order = append(a.order, digest)
	return run
}

// forget drops a plan that finished, so sending it again later is a new plan.
func (a *applyRuns) forget(digest string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.runs, digest)
	for i, d := range a.order {
		if d == digest {
			a.order = append(a.order[:i], a.order[i+1:]...)
			break
		}
	}
}

// applyError is a failed apply that also says what it created before failing,
// so the person looking at the error knows the plan is part-done.
type applyError struct {
	err     error
	created map[string]int
}

func (e *applyError) Error() string { return e.err.Error() }
func (e *applyError) Unwrap() error { return e.err }
