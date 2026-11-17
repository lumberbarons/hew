package daemon

import (
	"sync"

	"github.com/lumberbarons/hew/tracker"
)

// An apply that stops partway has already created some issues. Sending the
// same plan again must resume after them, not create them a second time, and
// the library does exactly that when it is handed the same checkpoint. A
// checkpoint made per request would be thrown away with the request, so the
// server keeps the checkpoint of every plan that did not finish.
//
// A plan is identified by its content and by who is applying it: the library
// records the acting identity, so one person's retry must not resume
// another's half-done plan.

// maxApplyRuns is how many unfinished plans are remembered. An evicted plan
// simply starts over, which is what happened before they were kept. A plan
// that is running is never evicted, so the bound can be exceeded briefly by
// that many concurrent plans.
const maxApplyRuns = 16

// applyKey identifies a plan: its request body and the identity applying it.
func applyKey(body []byte, actor string) string {
	return tracker.FileDigest(append(append([]byte{}, body...), "\x00"+actor...))
}

type applyRun struct {
	// mu serialises runs of one plan: MemoryCheckpoint has no lock, and two
	// concurrent sends of the same plan (a double click) must not both create
	// its entries. The second waits, then finds them all created.
	mu sync.Mutex
	cp tracker.Checkpoint

	users int // requests holding this run, guarded by applyRuns.mu
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

// acquire returns the run for a plan, starting one if none is remembered, and
// counts the caller as using it. Every acquire is paired with a release.
func (a *applyRuns) acquire(key string) *applyRun {
	a.mu.Lock()
	defer a.mu.Unlock()
	run, ok := a.runs[key]
	if !ok {
		if a.runs == nil {
			a.runs = map[string]*applyRun{}
		}
		a.evict()
		run = &applyRun{cp: tracker.NewMemoryCheckpoint()}
		if a.newCheckpoint != nil {
			run.cp = a.newCheckpoint()
		}
		a.runs[key] = run
		a.order = append(a.order, key)
	}
	run.users++
	return run
}

// evict makes room for one more run by dropping the oldest ones nobody is
// using. A run in use is skipped: dropping it would let a retry of the same
// plan start a second checkpoint and run beside the first.
func (a *applyRuns) evict() {
	for i := 0; len(a.order) >= maxApplyRuns && i < len(a.order); {
		key := a.order[i]
		if a.runs[key].users > 0 {
			i++
			continue
		}
		delete(a.runs, key)
		a.order = append(a.order[:i], a.order[i+1:]...)
	}
}

// release ends one use of a run. A finished plan is forgotten, so sending it
// again later is a new plan, but only if the entry is still this run: a later
// request may already have started a new run under the same key.
func (a *applyRuns) release(key string, run *applyRun, finished bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	run.users--
	if !finished || a.runs[key] != run {
		return
	}
	delete(a.runs, key)
	for i, k := range a.order {
		if k == key {
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
