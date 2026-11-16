package tracker

import (
	"time"

	"github.com/lumberbarons/hew/internal/gh"
)

// RateLimit is the GitHub client's last observed API budget: the cost of the
// query that reported it, the calls remaining, and when the window resets. A
// consumer uses it to pace a long run without asking the API a second time.
// The local backend never reports one, so Known is false there.
type RateLimit struct {
	Cost      int
	Remaining int
	ResetAt   time.Time
}

// rateLimiter is the optional capability a store implements when its backend
// has a budget to report. It is unexported because a consumer names the public
// RateLimit value, not the seam.
type rateLimiter interface {
	RateLimit() (gh.RateLimit, bool)
}

// RateLimit reports the budget captured from the last list query, and false
// when no query has run on this tracker or the backend has no budget. A
// Tracker is safe for concurrent use, so a caller may read it while other
// operations run.
func (t *Tracker) RateLimit() (RateLimit, bool) {
	limiter, ok := t.store.(rateLimiter)
	if !ok {
		return RateLimit{}, false
	}
	observed, known := limiter.RateLimit()
	if !known {
		return RateLimit{}, false
	}
	return RateLimit{Cost: observed.Cost, Remaining: observed.Remaining, ResetAt: observed.ResetAt}, true
}
