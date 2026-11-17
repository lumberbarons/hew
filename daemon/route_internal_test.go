package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// serveOnce runs one request through a route whose behaviour is fn, and
// returns the status and decoded body.
func serveOnce(t *testing.T, h http.HandlerFunc, req *http.Request) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, req)
	var body map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&body)
	return rec.Code, body
}

// TestAWriteOutlivesItsClient: a create is an issue and then its edges.
// Abandoning it when the browser goes away leaves the issue without them, so
// writes run to the end; reads, which have nothing to finish, still stop.
func TestAWriteOutlivesItsClient(t *testing.T) {
	var seen error
	var hasDeadline bool
	h := route(&Server{}, func(q *request[none]) (reply, error) {
		seen = q.ctx.Err()
		_, hasDeadline = q.ctx.Deadline()
		return ok(1), nil
	})
	gone, cancel := context.WithCancel(context.Background())
	cancel() // the client has already disconnected

	if status, _ := serveOnce(t, h, httptest.NewRequestWithContext(gone, http.MethodPost, "/x", strings.NewReader(`{}`))); status != 200 {
		t.Fatalf("write status = %d", status)
	}
	if seen != nil {
		t.Errorf("a write saw its context cancelled (%v) when the client left", seen)
	}
	if !hasDeadline {
		t.Error("a write has no deadline, so a hung backend would hold it forever")
	}

	serveOnce(t, h, httptest.NewRequestWithContext(gone, http.MethodGet, "/x", nil))
	if seen == nil {
		t.Error("a read kept running after its client left")
	}
}

func TestDryRunIsAcceptedOnlyWhereItIsHonoured(t *testing.T) {
	behaviour := func(q *request[none]) (reply, error) { return ok(map[string]any{"dry": q.dry}), nil }
	supports := route(&Server{}, behaviour, dryRunnable)
	plain := route(&Server{}, behaviour)

	cases := []struct {
		name   string
		h      http.HandlerFunc
		query  string
		status int
		dry    any
	}{
		{"supported and asked for", supports, "?dryRun=1", 200, true},
		{"supported and not asked for", supports, "", 200, false},
		{"not supported, not asked for", plain, "", 200, false},
		// A route that ignored the flag would do the real write.
		{"not supported but asked for", plain, "?dryRun=1", 400, nil},
		{"true is not 1", supports, "?dryRun=true", 400, nil},
		{"empty is not 1", supports, "?dryRun=", 400, nil},
		{"0 is not 1", supports, "?dryRun=0", 400, nil},
		{"twice is ambiguous", supports, "?dryRun=1&dryRun=1", 400, nil},
	}
	for _, c := range cases {
		status, body := serveOnce(t, c.h, httptest.NewRequest(http.MethodPost, "/x"+c.query, strings.NewReader(`{}`)))
		if status != c.status {
			t.Errorf("%s: status = %d %v, want %d", c.name, status, body, c.status)
			continue
		}
		if c.status == 200 && body["dry"] != c.dry {
			t.Errorf("%s: dry = %v, want %v", c.name, body["dry"], c.dry)
		}
		if c.status == 400 && body["error"] != "invalid_input" {
			t.Errorf("%s: error = %v, want invalid_input", c.name, body["error"])
		}
	}
}
