package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/lumberbarons/hew/pr"
	"github.com/lumberbarons/hew/tracker"
)

// problem is the JSON body of every error response. Kind is a stable
// machine-readable name; the frontend keys its messages on it, the way the
// CLI's callers key on exit codes.
type problem struct {
	Kind    string         `json:"error"`
	Message string         `json:"message"`
	Fields  map[string]any `json:"fields,omitempty"`
}

// classify maps a typed library error to an HTTP status and problem. The
// order matters: ClaimedError can wrap an AuthError, so the more specific
// type is tested first.
func classify(err error) (int, problem) {
	p := problem{Message: err.Error()}
	set := func(status int, kind string, fields map[string]any) (int, problem) {
		p.Kind, p.Fields = kind, fields
		return status, p
	}

	var (
		tooLarge     *http.MaxBytesError
		claimed      *tracker.ClaimedError
		blocked      *tracker.BlockedError
		invalid      *tracker.InvalidInputError
		untriaged    *tracker.UntriagedError
		notFound     *tracker.NotFoundError
		auth         *tracker.AuthError
		closed       *tracker.ClosedError
		epic         *tracker.EpicError
		discovery    *tracker.DiscoveryIntentError
		notClaimed   *tracker.NotClaimedError
		notOwner     *tracker.NotOwnerError
		openChildren *tracker.OpenChildrenError
		cycle        *tracker.CycleError
		unverifiable *tracker.UnverifiableCycleError
		notOpen      *tracker.NotOpenError
		parented     *tracker.ParentedError
		lock         *tracker.LockTimeoutError
		migrated     *tracker.MigratedError
		partialWrite *tracker.PartialWriteError
		partialCreat *tracker.PartialCreateError
		partialEdit  *tracker.PartialEditError
		partialRel   *tracker.PartialReleaseError
		partialClose *tracker.PartialCloseError
		partialReop  *tracker.PartialReopenError
		noPRs        *pr.NoPullRequestsError
		noUpstream   *pr.NoUpstreamError
		inference    *pr.IssueInferenceError
		existingPR   *pr.ExistingPullRequestError
		noOpenPR     *pr.NoOpenPullRequestError
		ambiguousPR  *pr.AmbiguousPullRequestError
	)

	switch {
	case errors.Is(err, context.Canceled):
		return set(499, "canceled", nil)
	case errors.Is(err, context.DeadlineExceeded):
		return set(http.StatusGatewayTimeout, "timeout", nil)
	case errors.As(err, &tooLarge):
		return set(http.StatusRequestEntityTooLarge, "too_large", map[string]any{"limit": tooLarge.Limit})
	case errors.As(err, &claimed):
		// The kind is the library's start outcome: the CLI's exit 5 (yours:
		// resume) versus exit 3 (theirs: pick next), under its recorded names.
		outcome, _ := tracker.StartOutcomeOf(err)
		return set(http.StatusConflict, string(outcome), map[string]any{
			"issue": claimed.Issue, "assignees": orEmpty(claimed.Assignees), "actor": claimed.Actor,
		})
	case errors.As(err, &blocked):
		// StartBlocked is the library's name for the CLI's exit 6.
		return set(http.StatusConflict, string(tracker.StartBlocked), map[string]any{
			"issue": blocked.Issue, "openBlockers": orEmpty(blocked.OpenBlockers),
		})
	case errors.As(err, &untriaged):
		return set(http.StatusBadRequest, "untriaged", map[string]any{"issue": untriaged.Issue})
	case errors.As(err, &auth):
		return set(http.StatusUnauthorized, "auth", nil)
	case errors.As(err, &notFound):
		return set(http.StatusNotFound, "not_found", map[string]any{"issue": notFound.Issue})
	case errors.As(err, &closed):
		return set(http.StatusConflict, "closed", map[string]any{"issue": closed.Issue, "reason": closed.Reason})
	case errors.As(err, &epic):
		return set(http.StatusConflict, "epic", map[string]any{"issue": epic.Issue})
	case errors.As(err, &discovery):
		// The CLI's usage refusal: the client has to declare intent by setting
		// forDiscovery, which the error's own message names.
		return set(http.StatusConflict, "discovery_intent", map[string]any{"issue": discovery.Issue})
	case errors.As(err, &notClaimed):
		return set(http.StatusConflict, "not_claimed", map[string]any{"issue": notClaimed.Issue})
	case errors.As(err, &notOwner):
		return set(http.StatusConflict, "not_owner", map[string]any{
			"issue": notOwner.Issue, "assignees": orEmpty(notOwner.Assignees), "actor": notOwner.Actor,
		})
	case errors.As(err, &openChildren):
		return set(http.StatusConflict, "open_children", map[string]any{"issue": openChildren.Issue, "open": openChildren.Open})
	case errors.As(err, &cycle):
		return set(http.StatusConflict, "cycle", map[string]any{"cycle": orEmpty(cycle.Cycle)})
	case errors.As(err, &unverifiable):
		return set(http.StatusConflict, "unverifiable_cycle", map[string]any{"issue": unverifiable.Issue, "blocker": unverifiable.Blocker})
	case errors.As(err, &notOpen):
		return set(http.StatusConflict, "not_open", map[string]any{"issue": notOpen.Issue, "blocker": notOpen.Blocker})
	case errors.As(err, &parented):
		return set(http.StatusConflict, "already_parented", map[string]any{"issue": parented.Issue, "parent": parented.Parent})
	case errors.As(err, &migrated):
		return set(http.StatusConflict, "migrated", nil)
	case errors.As(err, &lock):
		return set(http.StatusServiceUnavailable, "lock_timeout", nil)
	case errors.As(err, &partialWrite):
		return set(http.StatusInternalServerError, "partial_write", map[string]any{
			"issue": partialWrite.Issue, "landed": orEmpty(partialWrite.Landed),
			"failed": partialWrite.Failed, "notAttempted": orEmpty(partialWrite.NotAttempted),
		})
	case errors.As(err, &partialCreat):
		return set(http.StatusInternalServerError, "partial_create", map[string]any{
			"issue": partialCreat.Issue.Number, "stage": string(partialCreat.Stage),
		})
	case errors.As(err, &partialEdit):
		// A partial edit keeps its applied-field report only when the failure
		// has no more specific classification; the cases above already took
		// the actionable causes (auth, lock, closed), so an invalid field is
		// the one this branch is really for.
		return set(http.StatusInternalServerError, "partial_edit", map[string]any{
			"issue": partialEdit.Issue, "applied": orEmpty(partialEdit.Applied), "failed": partialEdit.Failed,
		})
	case errors.As(err, &invalid):
		return set(http.StatusBadRequest, "invalid_input", map[string]any{"field": invalid.Field, "reason": invalid.Reason})
	case errors.As(err, &partialRel):
		return set(http.StatusInternalServerError, "partial_release", map[string]any{"issue": partialRel.Issue})
	case errors.As(err, &partialClose):
		return set(http.StatusInternalServerError, "partial_close", map[string]any{"issue": partialClose.Issue})
	case errors.As(err, &partialReop):
		return set(http.StatusInternalServerError, "partial_reopen", map[string]any{
			"issue": partialReop.Issue, "stage": string(partialReop.Stage),
		})
	case errors.As(err, &noPRs):
		return set(http.StatusNotImplemented, "no_pull_requests", nil)
	case errors.As(err, &noUpstream):
		return set(http.StatusConflict, "no_upstream", map[string]any{"branch": noUpstream.Branch})
	case errors.As(err, &inference):
		return set(http.StatusBadRequest, "issue_inference", map[string]any{"branch": inference.Branch, "claimed": orEmpty(inference.Claimed)})
	case errors.As(err, &existingPR):
		return set(http.StatusConflict, "existing_pull_request", map[string]any{"issue": existingPR.Issue, "number": existingPR.Number})
	case errors.As(err, &noOpenPR):
		return set(http.StatusNotFound, "no_open_pull_request", map[string]any{"issue": noOpenPR.Issue})
	case errors.As(err, &ambiguousPR):
		return set(http.StatusConflict, "ambiguous_pull_request", map[string]any{"head": ambiguousPR.Head, "open": ambiguousPR.OpenCount})
	}
	return set(http.StatusInternalServerError, "internal", nil)
}

// writeJSON sends v as the response body.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr sends the problem a library error classifies to. A failed apply
// also says what it created before failing.
func writeErr(w http.ResponseWriter, err error) {
	status, p := classify(err)
	var failed *applyError
	if errors.As(err, &failed) {
		fields := map[string]any{}
		for k, v := range p.Fields {
			fields[k] = v
		}
		fields["created"] = failed.created
		p.Fields = fields
	}
	writeJSON(w, status, p)
}

// orEmpty keeps a nil slice from marshaling as null, so the frontend never
// has to distinguish absent from empty.
func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
