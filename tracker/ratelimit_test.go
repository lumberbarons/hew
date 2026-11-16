package tracker

import (
	"testing"
	"time"

	"github.com/lumberbarons/hew/internal/gh"
)

// budgetStore carries a GitHub budget through the optional capability the
// client implements. The plain fakeStore in open_test.go embeds gh.IssueStore
// without it, which is how a backend with no budget behaves.
type budgetStore struct {
	gh.IssueStore
	budget gh.RateLimit
	known  bool
}

func (s *budgetStore) RateLimit() (gh.RateLimit, bool) { return s.budget, s.known }

func TestRateLimitReportsTheBudgetTheStoreObserved(t *testing.T) {
	reset := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	tr := WrapAs(&budgetStore{
		budget: gh.RateLimit{Cost: 3, Remaining: 4997, ResetAt: reset},
		known:  true,
	}, BackendGitHub, "")

	budget, known := tr.RateLimit()
	if !known {
		t.Fatal("known = false, want the budget the store reported")
	}
	if budget.Cost != 3 || budget.Remaining != 4997 || !budget.ResetAt.Equal(reset) {
		t.Errorf("RateLimit = %+v, want cost 3, 4997 remaining, reset %v", budget, reset)
	}
}

// A store that has not run a query yet reports no budget, and so does a backend
// that has none to report: both say false rather than a zero value a consumer
// would read as exhausted.
func TestRateLimitIsUnknownWithoutABudget(t *testing.T) {
	for name, tr := range map[string]*Tracker{
		"no query yet":  WrapAs(&budgetStore{}, BackendGitHub, ""),
		"no capability": WrapAs(&fakeStore{}, BackendGitHub, ""),
	} {
		t.Run(name, func(t *testing.T) {
			budget, known := tr.RateLimit()
			if known {
				t.Errorf("known = true (%+v), want false", budget)
			}
			if budget != (RateLimit{}) {
				t.Errorf("RateLimit = %+v, want the zero value", budget)
			}
		})
	}
}
