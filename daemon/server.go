// Package daemon serves a hew tracker over a REST API and a small embedded web
// UI. It is a scratch proof that the public tracker and pr packages are
// enough to build a long-running consumer: it imports nothing from internal/.
//
// There is no authentication. The server is for one person on one machine, so
// it only ever listens on loopback and rejects requests a web page on another
// origin could forge (see guard).
package daemon

import (
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

//go:embed web
var webFS embed.FS

// Server routes HTTP requests onto one open tracker.
type Server struct {
	tr     *tracker.Tracker
	prc    *pr.Client // nil when the backend has no pull requests
	target tracker.Target
}

// New wires a Server. prc may be nil; its routes then answer 501.
func New(tr *tracker.Tracker, prc *pr.Client, target tracker.Target) *Server {
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

	web, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err) // the embed directive guarantees the directory
	}
	mux.Handle("/", http.FileServerFS(web))

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
