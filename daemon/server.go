// Package daemon serves a hew tracker over a REST API and a small embedded web
// UI. It is a scratch proof that the public tracker and pr packages are
// enough to build a long-running consumer: it imports nothing from internal/.
//
// There is no authentication. The server is for one person on one machine, so
// it only ever listens on loopback and rejects requests a web page on another
// origin could forge (see guard).
package daemon

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/lumberbarons/hew/pr"
	"github.com/lumberbarons/hew/tracker"
)

// webFS is the built UI. `npm run build` in web/ writes dist; a committed
// .gitkeep keeps the directory present so the daemon always compiles, and a
// missing index.html is reported at runtime instead (see uiHandler).
//
//go:embed all:dist
var webFS embed.FS

// PullRequests is the slice of *pr.Client the server uses. It is an interface
// so the HTTP mapping can be tested without a GitHub checkout; the real client
// satisfies it, and consumer/ covers the client itself.
type PullRequests interface {
	Create(ctx context.Context, req pr.CreateRequest) (pr.CreateResult, error)
	Amend(ctx context.Context, req pr.AmendRequest) (pr.AmendResult, error)
}

// Server routes HTTP requests onto one open tracker.
type Server struct {
	tr     *tracker.Tracker
	prc    PullRequests // nil when the backend has no pull requests
	target tracker.Target
	ui     fs.FS // nil means the embedded build
}

// New wires a Server. prc must be a nil interface, not a nil *pr.Client, when
// the backend has no pull requests; its routes then answer 501.
func New(tr *tracker.Tracker, prc PullRequests, target tracker.Target) *Server {
	return &Server{tr: tr, prc: prc, target: target}
}

// ValidateAddr refuses any listen address that is not loopback, so the
// no-auth server can never be reached from another machine.
func ValidateAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid address %q: %w", addr, err)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("refusing to listen on %q: the daemon has no auth and only serves loopback", addr)
}

// WithUI serves ui instead of the embedded build. It is for tests, and for
// pointing the daemon at a UI built elsewhere.
func (s *Server) WithUI(ui fs.FS) *Server {
	s.ui = ui
	return s
}

// Handler returns the full route table behind the loopback guard.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/config", s.handleConfig)
	mux.HandleFunc("GET /api/primer", s.handlePrimer)
	mux.HandleFunc("GET /api/board", s.handleBoard)
	mux.HandleFunc("GET /api/ready", s.handleReady)
	mux.HandleFunc("GET /api/issues", s.handleList)
	mux.HandleFunc("GET /api/issues/{n}", s.handleDetail)
	mux.HandleFunc("GET /api/epics", s.handleEpics)
	mux.HandleFunc("GET /api/epics/{n}", s.handleDetail)
	mux.HandleFunc("GET /api/search", s.handleSearch)
	mux.HandleFunc("GET /api/triage", s.handleTriage)

	mux.HandleFunc("POST /api/issues", s.handleCreate)
	mux.HandleFunc("POST /api/epics", s.handleCreateEpic)
	mux.HandleFunc("PATCH /api/issues/{n}", s.handleEdit)
	mux.HandleFunc("POST /api/issues/{n}/start", s.handleStart)
	mux.HandleFunc("POST /api/issues/{n}/release", s.handleRelease)
	mux.HandleFunc("POST /api/issues/{n}/close", s.handleClose)
	mux.HandleFunc("POST /api/issues/{n}/reopen", s.handleReopen)
	mux.HandleFunc("POST /api/issues/{n}/blockers", s.handleBlock)
	mux.HandleFunc("DELETE /api/issues/{n}/blockers/{m}", s.handleUnblock)
	mux.HandleFunc("POST /api/body/compose", s.handleCompose)
	mux.HandleFunc("POST /api/body/check", s.handleCheck)
	mux.HandleFunc("POST /api/apply", s.handleApply)
	mux.HandleFunc("POST /api/pr", s.handlePRCreate)
	mux.HandleFunc("PATCH /api/pr", s.handlePRAmend)

	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, problem{Kind: "no_route", Message: "no such endpoint"})
	})

	mux.Handle("/", s.uiHandler())

	return guard(mux)
}

// guard is the whole security model of a no-auth localhost server. A page on
// any website can make the browser send requests to 127.0.0.1, so it rejects
// the three ways that works:
//   - DNS rebinding: the Host header must be a loopback name.
//   - Cross-site writes: an Origin header, when present, must be this server.
//   - Form-style CSRF: a request with a body must be application/json, which
//     a cross-origin page cannot send without a preflight this server never
//     answers.
func guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackHost(r.Host) {
			writeJSON(w, http.StatusForbidden, problem{Kind: "forbidden_host", Message: "host must be loopback"})
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Host != r.Host {
				writeJSON(w, http.StatusForbidden, problem{Kind: "forbidden_origin", Message: "cross-origin request refused"})
				return
			}
		}
		if r.ContentLength != 0 && (r.Method == http.MethodPost || r.Method == http.MethodPatch || r.Method == http.MethodDelete) {
			if ct := r.Header.Get("Content-Type"); ct != "application/json" && !strings.HasPrefix(ct, "application/json;") {
				writeJSON(w, http.StatusUnsupportedMediaType, problem{Kind: "unsupported_media_type", Message: "Content-Type must be application/json"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// loopbackHost reports whether a Host header names this machine.
func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// acting returns the tracker a request acts as. On a local tracker the
// X-Hew-Actor header names the worker; on GitHub As is a documented no-op and
// every request acts as the authenticated login.
func (s *Server) acting(r *http.Request) *tracker.Tracker {
	if actor := strings.TrimSpace(r.Header.Get("X-Hew-Actor")); actor != "" && len(actor) <= 100 {
		return s.tr.As(actor)
	}
	return s.tr
}

// decode reads a JSON request body, refusing unknown fields so a typo in a
// client is an error rather than a silently ignored option.
func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) { // empty body = zero value
		return &tracker.InvalidInputError{Field: "body", Reason: err.Error()}
	}
	return nil
}

// number parses a path-value issue number.
func number(r *http.Request, name string) (int, error) {
	var n int
	if _, err := fmt.Sscanf(r.PathValue(name), "%d", &n); err != nil || n <= 0 {
		return 0, &tracker.InvalidInputError{Field: name, Reason: "must be a positive issue number"}
	}
	return n, nil
}

// uiHandler serves the built UI. The assets are embedded and change with the
// binary, so a browser must never reuse a stale script or stylesheet from a
// previous build. When the UI has not been built it says so, rather than
// answering 404 for the page a person just opened.
func (s *Server) uiHandler() http.Handler {
	ui := s.ui
	if ui == nil {
		sub, err := fs.Sub(webFS, "dist")
		if err != nil {
			panic(err) // the embed directive guarantees the directory
		}
		ui = sub
	}
	static := http.FileServerFS(ui)
	_, missing := fs.Stat(ui, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		if missing != nil && r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(notBuilt))
			return
		}
		static.ServeHTTP(w, r)
	})
}

const notBuilt = `<!doctype html><meta charset="utf-8"><title>hew</title>
<body style="font:16px/1.5 system-ui;max-width:40rem;margin:4rem auto;padding:0 1rem">
<h1>The web UI is not built</h1>
<p>Run <code>make web</code> (or <code>npm ci &amp;&amp; npm run build</code> in <code>daemon/web</code>), then rebuild the daemon.</p>
<p>The REST API is up: try <a href="/api/config">/api/config</a>.</p>
`

// EnsureLocalTracker initialises a local tracker in dir when there is none, so
// the daemon can be pointed at an empty checkout. It refuses a checkout whose
// issues live on GitHub: starting a local tracker beside it would switch every
// later hew command in that checkout to the new, empty one.
func EnsureLocalTracker(ctx context.Context, dir string) error {
	target, err := tracker.Detect(dir)
	if err != nil {
		return err
	}
	if target.Backend == tracker.BackendLocal {
		return nil
	}
	if target.Repo != "" {
		return fmt.Errorf("%s tracks its issues on GitHub (%s); refusing to start a local tracker beside them", dir, target.Repo)
	}
	_, err = tracker.Init(ctx, tracker.InitConfig{Worktree: dir, Backend: tracker.BackendLocal})
	return err
}
