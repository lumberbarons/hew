package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lumberbarons/hew/tracker"
)

// maxActorBytes bounds X-Hew-Actor. A longer value is refused rather than
// ignored: ignoring it would act as the daemon's own identity instead.
const maxActorBytes = 100

// maxBodyBytes bounds a request body.
const maxBodyBytes = 1 << 20

// writeTimeout bounds a write that has been cut loose from its client. A
// write is several backend calls (an issue, then its edges); abandoning it
// when the browser goes away leaves the issue without them, so it runs to the
// end, but not forever if the backend hangs.
const writeTimeout = 5 * time.Minute

// none is the input of a route that reads no body.
type none = struct{}

// request is what a route's behaviour is given: everything the HTTP layer
// parsed and validated, and nothing of the HTTP layer itself, so a handler is
// a function from typed input to a typed reply.
type request[In any] struct {
	ctx   context.Context
	tr    *tracker.Tracker // acting as the caller
	n, m  int              // the {n} and {m} path values; 0 when the route has none
	query url.Values
	dry   bool // ?dryRun=1: validate and write nothing
	in    In
}

// reply is a status and the JSON value to send with it.
type reply struct {
	status int
	body   any
}

func ok(v any) reply      { return reply{http.StatusOK, v} }
func created(v any) reply { return reply{http.StatusCreated, v} }

// routeOption declares something a route supports.
type routeOption func(*routeConfig)

type routeConfig struct{ dryRun bool }

// dryRunnable marks a route that can validate a request and write nothing.
// ?dryRun=1 is refused everywhere else: a route that ignored it would perform
// the real write for a client that asked only to check.
func dryRunnable(c *routeConfig) { c.dryRun = true }

// route adapts a behaviour to an http.HandlerFunc. It owns the steps every
// route repeated: parse the path numbers, resolve who the request acts as,
// decode the body, and turn a returned error into its classified response.
// Anything cross-cutting (logging, timeouts, body-limit mapping) goes here,
// once.
func route[In any](s *Server, fn func(*request[In]) (reply, error), opts ...routeOption) http.HandlerFunc {
	var cfg routeConfig
	for _, o := range opts {
		o(&cfg)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if r.Method != http.MethodGet {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
			defer cancel()
		}
		req := &request[In]{ctx: ctx, query: r.URL.Query()}

		var err error
		if req.dry, err = dryRunFlag(req.query, cfg.dryRun); err != nil {
			writeErr(w, err)
			return
		}
		if req.n, err = pathNumber(r, "n"); err != nil {
			writeErr(w, err)
			return
		}
		if req.m, err = pathNumber(r, "m"); err != nil {
			writeErr(w, err)
			return
		}
		if req.tr, err = s.acting(r); err != nil {
			writeErr(w, err)
			return
		}
		if r.Method != http.MethodGet {
			if err = decode(w, r, &req.in); err != nil {
				writeErr(w, err)
				return
			}
		}
		res, err := fn(req)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, res.status, res.body)
	}
}

// dryRunFlag reads ?dryRun. Absent is false. Present, it must be exactly 1 and
// the route must support it; anything else is refused rather than guessed at.
func dryRunFlag(query url.Values, supported bool) (bool, error) {
	values, present := query["dryRun"]
	if !present {
		return false, nil
	}
	if !supported {
		return false, &tracker.InvalidInputError{Field: "dryRun", Reason: "is not supported by this route"}
	}
	if len(values) != 1 || values[0] != "1" {
		return false, &tracker.InvalidInputError{Field: "dryRun", Reason: "must be 1"}
	}
	return true, nil
}

// acting returns the tracker a request acts as. On a local tracker the
// X-Hew-Actor header names the worker; on GitHub As is a documented no-op and
// every request acts as the authenticated login.
func (s *Server) acting(r *http.Request) (*tracker.Tracker, error) {
	actor := strings.TrimSpace(r.Header.Get("X-Hew-Actor"))
	switch {
	case actor == "":
		return s.tr, nil
	case len(actor) > maxActorBytes:
		return nil, &tracker.InvalidInputError{
			Field:  "X-Hew-Actor",
			Reason: fmt.Sprintf("must be at most %d bytes", maxActorBytes),
		}
	}
	return s.tr.As(actor), nil
}

// decode reads a JSON request body, refusing unknown fields so a typo in a
// client is an error rather than a silently ignored option. An empty body is
// the zero value.
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return err
		}
		return &tracker.InvalidInputError{Field: "body", Reason: err.Error()}
	}
	return nil
}

// pathNumber parses a path value as an issue number, or returns 0 when the
// route has no such value. It accepts only the canonical form: "1x", "+5" and
// "007" are refused instead of read as another issue.
func pathNumber(r *http.Request, name string) (int, error) {
	raw := r.PathValue(name)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 || strconv.Itoa(n) != raw {
		return 0, &tracker.InvalidInputError{Field: name, Reason: "must be a positive issue number"}
	}
	return n, nil
}
