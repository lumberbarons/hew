package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/daemon"
	"github.com/lumberbarons/hew/pr"
	"github.com/lumberbarons/hew/tracker"
)

// rawCall sends body verbatim, so a test can send what a client should not.
func (e *env) rawCall(method, path, body string) (int, map[string]any) {
	e.t.Helper()
	req, err := http.NewRequest(method, e.url+path, bytes.NewReader([]byte(body)))
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// bodyRoutes are the routes that read a JSON body. {n} is an issue number.
var bodyRoutes = []struct{ method, path string }{
	{"POST", "/api/issues"},
	{"POST", "/api/epics"},
	{"PATCH", "/api/issues/{n}"},
	{"POST", "/api/issues/{n}/start"},
	{"POST", "/api/issues/{n}/release"},
	{"POST", "/api/issues/{n}/close"},
	{"POST", "/api/issues/{n}/reopen"},
	{"POST", "/api/issues/{n}/blockers"},
	{"POST", "/api/body/compose"},
	{"POST", "/api/body/check"},
	{"POST", "/api/apply"},
	{"POST", "/api/pr"},
	{"PATCH", "/api/pr"},
}

func fill(path, n string) string { return strings.ReplaceAll(path, "{n}", n) }

func TestMalformedAndUnknownBodiesAreRefusedOnEveryRoute(t *testing.T) {
	e := newEnvWith(t, false, &stubPRs{})
	e.create("Exists", nil)
	for _, r := range bodyRoutes {
		for _, bad := range []string{`{not json`, `{"definitelyNotAField": 1}`} {
			status, res := e.rawCall(r.method, fill(r.path, "1"), bad)
			if status != 400 || res["error"] != "invalid_input" {
				t.Errorf("%s %s with %q = %d %v, want 400 invalid_input", r.method, r.path, bad, status, res)
			}
		}
	}
}

func TestBadIssueNumbersAreRefusedOnEveryNumberedRoute(t *testing.T) {
	e := newEnv(t, false)
	routes := []struct{ method, path string }{
		{"GET", "/api/issues/{n}"}, {"GET", "/api/epics/{n}"}, {"PATCH", "/api/issues/{n}"},
		{"POST", "/api/issues/{n}/start"}, {"POST", "/api/issues/{n}/release"},
		{"POST", "/api/issues/{n}/close"}, {"POST", "/api/issues/{n}/reopen"},
		{"POST", "/api/issues/{n}/blockers"}, {"DELETE", "/api/issues/{n}/blockers/1"},
		{"DELETE", "/api/issues/1/blockers/{n}"},
	}
	for _, r := range routes {
		for _, bad := range []string{"abc", "0", "-3", "12abc", "1x", "+5", "007", "1.5"} {
			status, res := e.rawCall(r.method, fill(r.path, bad), `{}`)
			if status != 400 || res["error"] != "invalid_input" {
				t.Errorf("%s %s (n=%s) = %d %v, want 400 invalid_input", r.method, r.path, bad, status, res)
			}
		}
	}
}

func TestMissingIssuesAre404OnEveryNumberedRoute(t *testing.T) {
	e := newEnv(t, false)
	body := map[string]string{
		"PATCH /api/issues/{n}":         `{"priority":"P1"}`,
		"POST /api/issues/{n}/close":    `{"reason":"x"}`,
		"POST /api/issues/{n}/reopen":   `{"reason":"x"}`,
		"POST /api/issues/{n}/blockers": `{"blockers":[1]}`,
	}
	routes := []struct{ method, path string }{
		{"GET", "/api/issues/{n}"}, {"GET", "/api/epics/{n}"}, {"PATCH", "/api/issues/{n}"},
		{"POST", "/api/issues/{n}/start"}, {"POST", "/api/issues/{n}/release"},
		{"POST", "/api/issues/{n}/close"}, {"POST", "/api/issues/{n}/reopen"},
		{"POST", "/api/issues/{n}/blockers"}, {"DELETE", "/api/issues/{n}/blockers/1"},
	}
	for _, r := range routes {
		b := body[r.method+" "+r.path]
		if b == "" {
			b = `{}`
		}
		status, res := e.rawCall(r.method, fill(r.path, "999"), b)
		if status != 404 || res["error"] != "not_found" {
			t.Errorf("%s %s = %d %v, want 404 not_found", r.method, r.path, status, res)
		}
	}
}

// TestBackendFailureIs500OnEveryRoute corrupts the tracker file under a
// running server. Every route that touches the store must surface that as a
// 500 with the generic kind rather than panic or leak a partial answer.
func TestBackendFailureIs500OnEveryRoute(t *testing.T) {
	e := newEnv(t, false)
	e.create("Soon unreadable", nil)
	e.appendRaw(`this is not json`)

	reads := []string{
		"/api/primer", "/api/board", "/api/ready", "/api/issues", "/api/issues/1", "/api/epics",
		"/api/epics/1", "/api/search?q=x", "/api/triage",
	}
	for _, path := range reads {
		if status, res := e.call("GET", path, "", nil); status != 500 || res["error"] != "internal" {
			t.Errorf("GET %s = %d %v, want 500 internal", path, status, res)
		}
	}

	good := map[string]any{"goal": "g", "doneWhen": []string{"x"}}
	writes := []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/issues", map[string]any{"title": "t", "type": "task", "body": good}},
		{"POST", "/api/issues?dryRun=1", map[string]any{"title": "t", "type": "task", "blockedBy": []int{1}, "body": good}},
		{"POST", "/api/epics", map[string]any{"title": "t", "children": []int{1}, "body": good}},
		{"POST", "/api/epics?dryRun=1", map[string]any{"title": "t", "children": []int{1}, "body": good}},
		{"PATCH", "/api/issues/1", map[string]any{"priority": "P1"}},
		{"POST", "/api/issues/1/start", map[string]any{}},
		{"POST", "/api/issues/1/release", map[string]any{}},
		{"POST", "/api/issues/1/close", map[string]any{"reason": "x"}},
		{"POST", "/api/issues/1/reopen", map[string]any{"reason": "x"}},
		{"POST", "/api/issues/1/blockers", map[string]any{"blockers": []int{2}}},
		{"DELETE", "/api/issues/1/blockers/2", nil},
		{"POST", "/api/apply", map[string]any{"entries": []map[string]any{{"id": "a", "title": "a", "type": "task"}}}},
	}
	for _, w := range writes {
		if status, res := e.call(w.method, w.path, "", w.body); status != 500 || res["error"] != "internal" {
			t.Errorf("%s %s = %d %v, want 500 internal", w.method, w.path, status, res)
		}
	}
}

func TestListQueryParameters(t *testing.T) {
	e := newEnv(t, false)
	e.create("Alpha", map[string]any{"areas": []string{"cli"}})
	e.create("Beta", nil)
	e.create("Gamma", nil)
	e.call("POST", "/api/issues/3/close", "", map[string]any{"reason": "done", "completed": true})

	count := func(path string) int {
		t.Helper()
		status, res := e.call("GET", path, "", nil)
		if status != 200 {
			t.Fatalf("GET %s = %d %v", path, status, res)
		}
		list, _ := res["_list"].([]any)
		return len(list)
	}
	for path, want := range map[string]int{
		"/api/issues":                   2,
		"/api/issues?state=open":        2,
		"/api/issues?state=closed":      1,
		"/api/issues?state=all":         3,
		"/api/issues?label=cli":         1,
		"/api/issues?state=all&limit=2": 2,
		"/api/ready?limit=1":            1,
		"/api/ready?limit=bogus":        2,
		"/api/epics?state=all":          0,
	} {
		if got := count(path); got != want {
			t.Errorf("GET %s returned %d issues, want %d", path, got, want)
		}
	}
	for _, path := range []string{"/api/issues?state=bogus", "/api/issues?epic=abc", "/api/issues?epic=0", "/api/epics?state=bogus", "/api/search?q=a&state=bogus", "/api/search?q="} {
		if status, res := e.call("GET", path, "", nil); status != 400 || res["error"] != "invalid_input" {
			t.Errorf("GET %s = %d %v, want 400 invalid_input", path, status, res)
		}
	}
	// The default search spans open and closed; state narrows it.
	status, res := e.call("GET", "/api/search?q=Gamma", "", nil)
	if status != 200 || len(res["issues"].([]any)) != 1 {
		t.Errorf("default search = %d %v, want the closed match", status, res)
	}
	if _, res = e.call("GET", "/api/search?q=Gamma&state=open", "", nil); len(res["issues"].([]any)) != 0 {
		t.Errorf("open-only search = %v, want no matches", res)
	}
	if _, res = e.call("GET", "/api/search?q=Gamma&state=closed", "", nil); len(res["issues"].([]any)) != 1 {
		t.Errorf("closed-only search = %v, want the closed match", res)
	}
}

func TestEpicCreateAndItsRefusals(t *testing.T) {
	e := newEnv(t, false)
	child := e.create("Child", nil)
	body := map[string]any{"goal": "Ship", "doneWhen": []string{"shipped"}}

	if status, res := e.call("POST", "/api/epics?dryRun=1", "", map[string]any{"title": "Big", "children": []int{child}, "body": body}); status != 200 || res["ok"] != true {
		t.Fatalf("epic dry run = %d %v", status, res)
	}
	if _, d := e.call("GET", "/api/issues/1", "", nil); d["issue"].(map[string]any)["parent"] != nil {
		t.Fatal("a dry run attached a parent")
	}
	status, res := e.call("POST", "/api/epics", "", map[string]any{"title": "Big", "children": []int{child}, "body": body})
	if status != 201 {
		t.Fatalf("epic create = %d %v", status, res)
	}
	epic := int(res["issue"].(map[string]any)["number"].(float64))
	if _, d := e.call("GET", "/api/epics/2", "", nil); len(d["children"].([]any)) != 1 {
		t.Errorf("epic #%d children = %v, want the one child", epic, d["children"])
	}

	// The child already has a parent, and a missing child is not found.
	if status, res = e.call("POST", "/api/epics", "", map[string]any{"title": "Second", "children": []int{child}, "body": body}); status != 409 || res["error"] != "already_parented" {
		t.Errorf("re-parenting = %d %v, want 409 already_parented", status, res)
	}
	if status, res = e.call("POST", "/api/epics?dryRun=1", "", map[string]any{"title": "Third", "children": []int{99}, "body": body}); status != 404 {
		t.Errorf("missing child = %d %v, want 404", status, res)
	}
	if status, res = e.call("POST", "/api/epics", "", map[string]any{"title": "", "body": body}); status != 400 {
		t.Errorf("blank epic title = %d %v, want 400", status, res)
	}
}

func TestCreateRefusals(t *testing.T) {
	e := newEnv(t, false)
	good := map[string]any{"goal": "g", "doneWhen": []string{"x"}}
	if status, res := e.call("POST", "/api/issues?dryRun=1", "", map[string]any{"title": "t", "type": "task", "blockedBy": []int{99}, "body": good}); status != 404 || res["error"] != "not_found" {
		t.Errorf("dry run with a missing blocker = %d %v, want 404", status, res)
	}
	if status, res := e.call("POST", "/api/issues?dryRun=1", "", map[string]any{"title": "t", "type": "task", "body": map[string]any{"goal": "g", "text": "also raw"}}); status != 400 {
		t.Errorf("dry run with conflicting body sources = %d %v, want 400", status, res)
	}
	if status, res := e.call("POST", "/api/issues", "", map[string]any{"title": "t", "type": "task", "blockedBy": []int{99}, "body": good}); status != 404 {
		t.Errorf("create with a missing blocker = %d %v, want 404", status, res)
	}
}

func TestComposeAndCheckBodies(t *testing.T) {
	e := newEnv(t, false)
	if status, res := e.call("POST", "/api/body/compose", "", map[string]any{"body": map[string]any{"goal": "g", "text": "raw"}}); status != 400 || res["error"] != "invalid_input" {
		t.Errorf("conflicting compose = %d %v, want 400", status, res)
	}
	status, res := e.call("POST", "/api/body/compose", "", map[string]any{"body": map[string]any{"goal": "Fix `internal/cli/pr.go`, then edit internal/cli/app.go with --title"}})
	if status != 200 || res["body"] == "" {
		t.Fatalf("compose = %d %v", status, res)
	}
	// Only the unmarked path and flag are reported; the code span is not.
	cf, _ := res["findings"].([]any)
	if len(cf) != 1 {
		t.Fatalf("compose findings = %v, want one finding", res["findings"])
	}
	if toks := cf[0].(map[string]any)["tokens"].([]any); len(toks) != 2 {
		t.Errorf("compose tokens = %v, want the unmarked path and flag", toks)
	}
	// Findings are advice and carry their tokens; a clean body has none.
	status, res = e.call("POST", "/api/body/check", "", map[string]any{"text": "run go test ./... and TestSomethingElse"})
	findings, _ := res["findings"].([]any)
	if status != 200 || len(findings) == 0 || findings[0].(map[string]any)["tokens"] == nil {
		t.Errorf("check with unmarked code = %d %v, want findings with tokens", status, res)
	}
	if _, res = e.call("POST", "/api/body/check", "", map[string]any{"text": "plain prose"}); len(res["findings"].([]any)) != 0 {
		t.Errorf("check of plain prose = %v, want no findings", res)
	}
}

func TestBlockAndUnblockEdgeCases(t *testing.T) {
	e := newEnv(t, false)
	a := e.create("A", nil)
	b := e.create("B", nil)

	status, res := e.call("POST", "/api/issues/2/blockers", "", map[string]any{"blockers": []int{a}})
	if status != 200 || len(res["added"].([]any)) != 1 {
		t.Fatalf("block = %d %v", status, res)
	}
	// Blocking again is a no-op reported as existing, not an error.
	if _, res = e.call("POST", "/api/issues/2/blockers", "", map[string]any{"blockers": []int{a}}); len(res["existing"].([]any)) != 1 || len(res["added"].([]any)) != 0 {
		t.Errorf("repeat block = %v, want existing=[%d]", res, a)
	}
	if status, res = e.call("POST", "/api/issues/2/blockers", "", map[string]any{"blockers": []int{99}}); status != 409 || res["error"] != "not_open" {
		t.Errorf("missing blocker = %d %v, want 409 not_open", status, res)
	}
	if status, res = e.call("POST", "/api/issues/2/blockers", "", map[string]any{"blockers": []int{2}}); status != 409 || res["error"] != "cycle" {
		t.Errorf("self block = %d %v, want 409 cycle", status, res)
	}
	// Unblocking something that was never a blocker reports it as missing.
	if status, res = e.call("DELETE", "/api/issues/2/blockers/99", "", nil); status != 200 || len(res["missing"].([]any)) != 1 {
		t.Errorf("unblock never-blocked = %d %v, want missing=[99]", status, res)
	}
	// A closed target is refused unless the caller opts in.
	e.call("POST", "/api/issues/2/close", "", map[string]any{"reason": "dup"})
	if status, res = e.call("DELETE", "/api/issues/2/blockers/1", "", nil); status != 409 || res["error"] != "closed" {
		t.Errorf("unblock on closed = %d %v, want 409 closed", status, res)
	}
	if status, res = e.call("DELETE", "/api/issues/2/blockers/1?allowClosed=1", "", nil); status != 200 || len(res["removed"].([]any)) != 1 {
		t.Errorf("unblock allowClosed = %d %v, want removed=[1]", status, res)
	}
	_ = b
}

func TestApplyWiresDependenciesAndRejectsBadPlans(t *testing.T) {
	e := newEnv(t, false)
	e.create("Existing", nil)
	plan := map[string]any{"entries": []map[string]any{
		{"id": "a", "title": "First", "type": "task", "sections": map[string]any{"fix": "f", "problem": "p", "doneWhen": []string{"x"}}},
		{"id": "b", "title": "Second", "type": "task", "blockedBy": []map[string]any{{"id": "a"}, {"number": 1}}},
		{"id": "c", "title": "Third", "type": "task", "parent": map[string]any{"number": 1}, "body": "raw body"},
	}}
	// A dry run reports an empty mapping, never null.
	if status, res := e.call("POST", "/api/apply?dryRun=1", "", plan); status != 200 || res["mapping"] == nil {
		t.Errorf("dry run mapping = %v (status %d), want {} not null", res["mapping"], status)
	}
	status, res := e.call("POST", "/api/apply", "", plan)
	if status != 200 || res["created"] != float64(3) || res["wired"] == float64(0) {
		t.Fatalf("apply = %d %v", status, res)
	}
	if mapping := res["mapping"].(map[string]any); len(mapping) != 3 {
		t.Errorf("mapping = %v, want every entry", mapping)
	}
	_, d := e.call("GET", "/api/issues/3", "", nil) // "b"
	if ob := d["issue"].(map[string]any)["blockedBy"].([]any); len(ob) != 2 {
		t.Errorf("b is blocked by %v, want a and #1", ob)
	}

	dangling := map[string]any{"entries": []map[string]any{{"id": "x", "title": "X", "type": "task", "blockedBy": []map[string]any{{"id": "nowhere"}}}}}
	if status, res = e.call("POST", "/api/apply", "", dangling); status != 400 {
		t.Errorf("dangling reference = %d %v, want 400", status, res)
	}
	if status, res = e.call("POST", "/api/apply", "", map[string]any{"entries": []map[string]any{{"id": "a", "title": "", "type": "task"}}}); status != 400 {
		t.Errorf("blank title = %d %v, want 400", status, res)
	}
}

// TestHostileAndOddTextSurfacesAsFindings covers the read paths that carry
// warnings and text findings, which only a hand-written tracker can produce:
// a dependency cycle and a title with an invisible character.
func TestHostileAndOddTextSurfacesAsFindings(t *testing.T) {
	e := newEnv(t, false)
	e.appendRaw(
		`{"number":1,"title":"loops\u200b back","body":"x","state":"OPEN","created_at":"2026-01-01T00:00:00Z","labels":["P2","task"],"blocked_by":[2]}`,
		`{"number":2,"title":"loops forward","body":"y","state":"OPEN","created_at":"2026-01-01T00:00:00Z","labels":["P2","task"],"blocked_by":[1]}`,
	)
	for _, path := range []string{"/api/board", "/api/primer"} {
		_, res := e.call("GET", path, "", nil)
		ws, _ := res["warnings"].([]any)
		if len(ws) == 0 {
			t.Fatalf("GET %s has no warnings for a dependency cycle: %v", path, res)
		}
		w := ws[0].(map[string]any)
		if w["kind"] != "dependency-cycle" || len(w["cycle"].([]any)) < 2 {
			t.Errorf("GET %s warning = %v, want the cycle members", path, w)
		}
	}
	_, d := e.call("GET", "/api/issues/1", "", nil)
	tf, _ := d["textFindings"].([]any)
	if len(tf) == 0 || tf[0].(map[string]any)["zeroWidth"] != true || tf[0].(map[string]any)["source"] != "title" {
		t.Errorf("textFindings = %v, want a zero-width finding on the title", d["textFindings"])
	}
	// The cycle also keeps both members out of ready, which is why it warns.
	_, ready := e.call("GET", "/api/ready", "", nil)
	if n := len(ready["_list"].([]any)); n != 0 {
		t.Errorf("ready has %d issues, want the cycle's members hidden", n)
	}
}

func TestLoopbackHostNamesAreAccepted(t *testing.T) {
	e := newEnv(t, false)
	for _, host := range []string{"localhost:7777", "localhost", "[::1]:7777", "127.0.0.1"} {
		req, _ := http.NewRequest("GET", e.url+"/api/config", nil)
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("Host %q = %d, want 200", host, resp.StatusCode)
		}
	}
}

// ---- pull requests, through a stub ----

type stubPRs struct {
	created pr.CreateRequest
	amended pr.AmendRequest
	err     error
}

func (s *stubPRs) Create(_ context.Context, req pr.CreateRequest) (pr.CreateResult, error) {
	s.created = req
	if s.err != nil {
		return pr.CreateResult{}, s.err
	}
	return pr.CreateResult{
		Number: 12, URL: "https://example.test/pull/12", Draft: !req.Ready, Fixes: 1, FixedIssues: []int{1},
		Head: "feat/x", Base: "main", Title: "feat: x",
		Warnings: []pr.Warning{
			{Kind: pr.WarnUnclaimed, Issue: 9, Assignees: []string{"alice"}, Actor: "bob"},
			{Kind: pr.WarnMissingTesting},
		},
	}, nil
}

func (s *stubPRs) Amend(_ context.Context, req pr.AmendRequest) (pr.AmendResult, error) {
	s.amended = req
	if s.err != nil {
		return pr.AmendResult{}, s.err
	}
	return pr.AmendResult{Number: 12, Title: "feat: amended"}, nil
}

func TestPullRequestRoutesMapRequestsAndResults(t *testing.T) {
	stub := &stubPRs{}
	e := newEnvWith(t, false, stub)
	if _, cfg := e.call("GET", "/api/config", "", nil); cfg["pullRequests"] != true {
		t.Errorf("config pullRequests = %v, want true with a client", cfg["pullRequests"])
	}

	status, res := e.call("POST", "/api/pr", "", map[string]any{
		"for": []int{1}, "title": "feat: x", "what": "w", "why": "y", "testing": "t", "risk": "r", "ready": true, "base": "main",
	})
	if status != 201 || res["number"] != float64(12) || res["draft"] != false || res["url"] != "https://example.test/pull/12" {
		t.Fatalf("create = %d %v", status, res)
	}
	// Warnings are the library's typed values, so the client reads the kind
	// instead of a pre-rendered string — and reads each warning's subject, so
	// an advisory names the issue, assignees and actor it is about.
	if w := res["warnings"].([]any); len(w) != 2 {
		t.Fatalf("warnings = %v, want two structured warnings", w)
	} else if unclaimed, ok := w[0].(map[string]any); !ok {
		t.Errorf("warnings[0] = %v, want an object naming the issue and its assignees", w[0])
	} else if unclaimed["kind"] != "unclaimed" || unclaimed["issue"] != float64(9) ||
		unclaimed["actor"] != "bob" || fmt.Sprint(unclaimed["assignees"]) != "[alice]" {
		t.Errorf("unclaimed warning = %v, want its issue, assignees and actor", unclaimed)
	} else if missing, ok := w[1].(map[string]any); !ok || missing["kind"] != "missing-testing" {
		t.Errorf("warnings[1] = %v, want missing-testing", w[1])
	} else if missing["issues"] == nil || missing["assignees"] == nil || missing["groups"] == nil {
		t.Errorf("warnings[1] = %v, want empty lists rather than null", missing)
	}
	if res["findings"] == nil || res["fixedIssues"] == nil {
		t.Errorf("create left a list null: %v", res)
	}
	c := stub.created
	if len(c.For) != 1 || c.For[0] != 1 || c.Title != "feat: x" || !c.Ready || c.Base != "main" ||
		c.Body.Sections.What != "w" || c.Body.Sections.Why != "y" || c.Body.Sections.Testing != "t" || c.Body.Sections.Risk != "r" {
		t.Errorf("request mapped to %+v", c)
	}

	title, what := "feat: amended", "new what"
	status, res = e.call("PATCH", "/api/pr", "", map[string]any{"title": title, "what": what})
	if status != 200 || res["title"] != "feat: amended" {
		t.Fatalf("amend = %d %v", status, res)
	}
	a := stub.amended
	if a.Title == nil || *a.Title != title || a.Edit.What == nil || *a.Edit.What != what || a.Edit.Why != nil {
		t.Errorf("amend mapped to %+v; unset fields must stay nil so they are preserved", a)
	}
}

func TestPullRequestErrorsAreClassified(t *testing.T) {
	stub := &stubPRs{err: &pr.ExistingPullRequestError{Issue: 1, Number: 7}}
	e := newEnvWith(t, false, stub)
	if status, res := e.call("POST", "/api/pr", "", map[string]any{"for": []int{1}}); status != 409 || res["error"] != "existing_pull_request" {
		t.Errorf("create = %d %v, want 409 existing_pull_request", status, res)
	}
	stub.err = &pr.NoOpenPullRequestError{Issue: 1}
	if status, res := e.call("PATCH", "/api/pr", "", map[string]any{"title": "x"}); status != 404 || res["error"] != "no_open_pull_request" {
		t.Errorf("amend = %d %v, want 404 no_open_pull_request", status, res)
	}
	stub.err = errors.New("boom")
	if status, _ := e.call("POST", "/api/pr", "", map[string]any{}); status != 500 {
		t.Errorf("unclassified failure = %d, want 500", status)
	}
}

func TestAnActorTooLongToUseIsRefusedNotReplaced(t *testing.T) {
	e := newEnv(t, false)
	e.create("Claimable", nil)

	// Ignoring the header would act as the daemon's own identity, so a claim
	// meant for someone else would be recorded under the wrong name.
	long := strings.Repeat("a", 101)
	status, res := e.call("POST", "/api/issues/1/start", long, map[string]any{})
	if status != 400 || res["error"] != "invalid_input" || res["fields"].(map[string]any)["field"] != "X-Hew-Actor" {
		t.Fatalf("start as a 101-byte actor = %d %v, want 400 naming the header", status, res)
	}
	_, d := e.call("GET", "/api/issues/1", "", nil)
	if as := d["issue"].(map[string]any)["assignees"].([]any); len(as) != 0 {
		t.Errorf("a refused request still claimed the issue for %v", as)
	}

	boundary := strings.Repeat("a", 100)
	if status, res = e.call("POST", "/api/issues/1/start", boundary, map[string]any{}); status != 200 || res["claimant"] != boundary {
		t.Errorf("start as a 100-byte actor = %d %v, want it accepted as that actor", status, res)
	}
}

// An idea is claimable only for investigation: the REST refusal names the
// field that grants it and leaves the issue untouched, and the same route
// claims once forDiscovery is set.
func TestAnIdeaIsClaimedOnlyForDiscovery(t *testing.T) {
	e := newEnv(t, false)
	n := e.create("Consider a cache", map[string]any{"type": "idea"})
	path := fmt.Sprintf("/api/issues/%d/start", n)

	status, res := e.call("POST", path, "daemon", map[string]any{})
	if status != http.StatusConflict || res["error"] != "discovery_intent" {
		t.Fatalf("start idea = %d %v, want 409 discovery_intent", status, res)
	}
	if msg, _ := res["message"].(string); !strings.Contains(msg, "forDiscovery") {
		t.Errorf("message = %q, want it to explain how to claim for investigation", msg)
	}
	if fields, _ := res["fields"].(map[string]any); fields["issue"] != float64(n) {
		t.Errorf("fields = %v, want issue #%d", res["fields"], n)
	}
	_, d := e.call("GET", fmt.Sprintf("/api/issues/%d", n), "", nil)
	i := d["issue"].(map[string]any)
	if len(i["assignees"].([]any)) != 0 || i["inProgress"] != false {
		t.Errorf("refused start mutated the idea: %v", i)
	}

	status, res = e.call("POST", path, "daemon", map[string]any{"forDiscovery": true})
	if status != http.StatusOK || res["claimant"] != "daemon" {
		t.Errorf("discovery start = %d %v, want 200 claimed as daemon", status, res)
	}
}

// The type query is the normalized type, not a literal label: an idea opens
// through create with a problem and optional questions, a legacy task with an
// idea label stays a task, and an epic is not a type match. State still
// narrows open and closed.
func TestListTypeQueryAndIdeaCapture(t *testing.T) {
	e := newEnv(t, false)
	idea := e.create("Open idea", map[string]any{
		"type": "idea",
		"body": map[string]any{
			"problem":       "Ideas get lost in the backlog.",
			"openQuestions": []string{"Which command filters ideas?", "How does hewd browse them?"},
		},
	})
	e.create("A task", nil)
	e.appendRaw(
		`{"number":900,"title":"Legacy task","body":"x","state":"OPEN","created_at":"2026-01-01T00:00:00Z","labels":["P2","task","idea"]}`,
		`{"number":901,"title":"Epic idea","body":"x","state":"OPEN","created_at":"2026-01-01T00:00:00Z","labels":["P2","idea","epic"]}`,
		`{"number":902,"title":"Epic child","body":"x","state":"OPEN","created_at":"2026-01-01T00:00:00Z","labels":["P2","task"],"parent":901}`,
	)

	// The create route writes the idea's problem and questions.
	_, d := e.call("GET", fmt.Sprintf("/api/issues/%d", idea), "", nil)
	body := d["issue"].(map[string]any)["body"].(string)
	for _, want := range []string{"### Problem", "### Open questions", "- Which command filters ideas?"} {
		if !strings.Contains(body, want) {
			t.Errorf("idea body %q missing %q", body, want)
		}
	}
	for _, unwanted := range []string{"### Approach", "### Done when"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("idea body grew %q:\n%s", unwanted, body)
		}
	}

	count := func(path string) []int {
		t.Helper()
		status, res := e.call("GET", path, "", nil)
		if status != 200 {
			t.Fatalf("GET %s = %d %v", path, status, res)
		}
		return numbers(res["_list"])
	}

	if got, want := count("/api/issues?type=idea"), []int{idea}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("?type=idea = %v, want %v", got, want)
	}
	if got, want := count("/api/issues?label=idea"), []int{idea, 900, 901}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("?label=idea = %v, want the literal-label matches %v", got, want)
	}

	if status, res := e.call("POST", fmt.Sprintf("/api/issues/%d/close", idea), "", map[string]any{"reason": "done", "completed": true}); status != 200 {
		t.Fatalf("close idea = %d %v", status, res)
	}
	if got, want := count("/api/issues?type=idea&state=closed"), []int{idea}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("?type=idea&state=closed = %v, want %v", got, want)
	}
	if got := count("/api/issues?type=idea"); len(got) != 0 {
		t.Errorf("default open ?type=idea = %v, want none", got)
	}

	if status, res := e.call("GET", "/api/issues?type=story", "", nil); status != 400 || res["error"] != "invalid_input" {
		t.Errorf("?type=story = %d %v, want 400 invalid_input", status, res)
	}
}

// An apply plan entry carries open questions through to the composed body, the
// same as the create route: a batch of ideas is shaped like a single one.
func TestApplyComposesOpenQuestions(t *testing.T) {
	e := newEnv(t, false)
	plan := map[string]any{"entries": []map[string]any{
		{"id": "a", "title": "Batch idea", "type": "idea", "sections": map[string]any{
			"problem":       "Ideas get lost.",
			"openQuestions": []string{"Which command filters ideas?"},
		}},
	}}
	if status, res := e.call("POST", "/api/apply", "", plan); status != 200 || res["created"] != float64(1) {
		t.Fatalf("apply = %d %v", status, res)
	}
	_, d := e.call("GET", "/api/issues/1", "", nil)
	body := d["issue"].(map[string]any)["body"].(string)
	for _, want := range []string{"### Problem", "### Open questions", "- Which command filters ideas?"} {
		if !strings.Contains(body, want) {
			t.Errorf("applied idea body %q missing %q", body, want)
		}
	}
}

func TestEpicDryRunGivesTheSameAdviceAsAnIssue(t *testing.T) {
	e := newEnv(t, false)
	body := map[string]any{"goal": "Edit internal/cli/pr.go", "doneWhen": []string{"x"}}
	for path, payload := range map[string]map[string]any{
		"/api/epics?dryRun=1":  {"title": "Big", "body": body},
		"/api/issues?dryRun=1": {"title": "Small", "type": "task", "body": body},
	} {
		status, res := e.call("POST", path, "", payload)
		findings, _ := res["findings"].([]any)
		if status != 200 || len(findings) != 1 {
			t.Errorf("POST %s = %d %v, want the unmarked-path advice", path, status, res)
		}
	}
}

func TestAnOversizedBodyIs413(t *testing.T) {
	e := newEnv(t, false)
	huge := `{"title":"` + strings.Repeat("a", 2<<20) + `"}`
	status, res := e.rawCall("POST", "/api/issues", huge)
	if status != http.StatusRequestEntityTooLarge || res["error"] != "too_large" {
		t.Errorf("a 2 MiB body = %d %v, want 413 too_large", status, res)
	}
}

func TestResolveTargetFollowsTheBackendBeingServed(t *testing.T) {
	_, local := checkout(t, false)
	if local.Backend != tracker.BackendLocal || local.ID == "" {
		t.Fatalf("setup: local target = %+v", local)
	}
	root := local.MainCheckout

	got, err := daemon.ResolveTarget(root, "")
	if err != nil || got.Backend != tracker.BackendLocal || got.ID != local.ID {
		t.Errorf("without --repo = %+v, %v; want the local tracker", got, err)
	}
	// --repo serves GitHub, so describing the checkout's local tracker would
	// report a directory and id the daemon is not using.
	got, err = daemon.ResolveTarget(root, "o/r")
	if err != nil || got.Backend != tracker.BackendGitHub || got.Repo != "o/r" || got.Dir != "" || got.ID != "" {
		t.Errorf("with --repo = %+v, %v; want GitHub o/r with no local dir or id", got, err)
	}
}

// TestResolveTargetDoesNotNeedDetectionWhenGitHubIsForced: forcing GitHub is
// how a checkout with a damaged local config is served anyway, so a Detect
// failure must not stop it starting.
func TestResolveTargetDoesNotNeedDetectionWhenGitHubIsForced(t *testing.T) {
	root := bareCheckout(t, "")
	if err := os.MkdirAll(filepath.Join(root, ".hew"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".hew", "config.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := tracker.Detect(root); err == nil {
		t.Fatal("setup: Detect should fail on a damaged config")
	}

	if _, err := daemon.ResolveTarget(root, ""); err == nil {
		t.Error("without --repo a damaged config should still be reported")
	}
	got, err := daemon.ResolveTarget(root, "o/r")
	if err != nil || got.Backend != tracker.BackendGitHub || got.Repo != "o/r" {
		t.Errorf("with --repo = %+v, %v; want GitHub o/r despite the damaged config", got, err)
	}
}

func TestValidateOptionsRefusesContradictoryFlags(t *testing.T) {
	if err := daemon.ValidateOptions("127.0.0.1:7777", "", true); err != nil {
		t.Errorf("--init-local alone: %v", err)
	}
	if err := daemon.ValidateOptions("127.0.0.1:7777", "o/r", false); err != nil {
		t.Errorf("--repo alone: %v", err)
	}
	if err := daemon.ValidateOptions("127.0.0.1:7777", "o/r", true); err == nil {
		t.Error("--repo with --init-local was accepted")
	}
	if err := daemon.ValidateOptions("0.0.0.0:7777", "", false); err == nil {
		t.Error("a non-loopback address was accepted")
	}
}

// TestDryRunIsRefusedWhereItWouldBeIgnored: a client that adds ?dryRun=1 to
// check a close must not get the close.
func TestDryRunIsRefusedWhereItWouldBeIgnored(t *testing.T) {
	e := newEnv(t, false)
	e.create("Open and staying open", nil)

	for _, tc := range []struct{ method, path string }{
		{"POST", "/api/issues/1/close?dryRun=1"},
		{"PATCH", "/api/issues/1?dryRun=1"},
		{"POST", "/api/issues/1/start?dryRun=1"},
		{"POST", "/api/issues/1/blockers?dryRun=1"},
		{"GET", "/api/board?dryRun=1"},
	} {
		status, res := e.call(tc.method, tc.path, "", map[string]any{"reason": "x"})
		if status != 400 || res["error"] != "invalid_input" {
			t.Errorf("%s %s = %d %v, want 400 invalid_input", tc.method, tc.path, status, res)
		}
	}
	_, d := e.call("GET", "/api/issues/1", "", nil)
	if d["issue"].(map[string]any)["state"] != "open" || len(d["issue"].(map[string]any)["assignees"].([]any)) != 0 {
		t.Errorf("a refused dry run still changed the issue: %v", d["issue"])
	}

	// A flag that is not exactly 1 on a route that does support it is refused,
	// not read as false, which would write for real.
	before := len(titles(e))
	body := map[string]any{"title": "Nope", "type": "task", "body": map[string]any{"goal": "g"}}
	if status, _ := e.call("POST", "/api/issues?dryRun=true", "", body); status != 400 {
		t.Errorf("dryRun=true = %d, want 400", status)
	}
	if after := len(titles(e)); after != before {
		t.Errorf("dryRun=true created an issue (%d -> %d)", before, after)
	}
	if status, _ := e.call("POST", "/api/issues?dryRun=1", "", body); status != 200 {
		t.Errorf("dryRun=1 on create = %d, want 200", status)
	}
}

// List responses are rows, not documents: the library omits bodies on its
// list-shaped reads, so the daemon has nothing to strip. Only the detail
// route carries text.
func TestListRoutesCarryNoIssueBodies(t *testing.T) {
	e := newEnv(t, true)
	text := map[string]any{"goal": "Ship it", "doneWhen": []string{"shipped"}}
	ready := e.create("Ready with a body", map[string]any{"body": text})
	status, res := e.call("POST", "/api/epics", "", map[string]any{"title": "Ship v1", "children": []int{ready}, "body": text})
	if status != http.StatusCreated {
		t.Fatalf("create epic = %d %v", status, res)
	}
	epic := int(res["issue"].(map[string]any)["number"].(float64))

	lists := []struct {
		path   string
		issues func(map[string]any) []any
	}{
		{"/api/board", func(res map[string]any) []any {
			var all []any
			for _, key := range []string{"ready", "claimed", "blocked", "epics"} {
				all = append(all, res[key].([]any)...)
			}
			return all
		}},
		{"/api/ready", func(res map[string]any) []any { return res["_list"].([]any) }},
		{"/api/issues", func(res map[string]any) []any { return res["_list"].([]any) }},
		{"/api/epics", func(res map[string]any) []any { return res["_list"].([]any) }},
		{"/api/search?q=Ready", func(res map[string]any) []any { return res["issues"].([]any) }},
		{"/api/triage", func(res map[string]any) []any { return res["issues"].([]any) }},
		{fmt.Sprintf("/api/issues/%d", epic), func(res map[string]any) []any { return res["children"].([]any) }},
	}
	for _, list := range lists {
		status, res := e.call("GET", list.path, "", nil)
		if status != http.StatusOK {
			t.Fatalf("%s = %d %v", list.path, status, res)
		}
		issues := list.issues(res)
		if len(issues) == 0 {
			t.Errorf("%s listed no issues to check", list.path)
		}
		for _, v := range issues {
			issue := v.(map[string]any)
			if body, ok := issue["body"]; ok {
				t.Errorf("%s listed #%v with a body: %v", list.path, issue["number"], body)
			}
		}
	}

	// The detail route is the one that carries text, so the check above has
	// something to contrast with.
	status, detail := e.call("GET", fmt.Sprintf("/api/issues/%d", ready), "", nil)
	if status != http.StatusOK {
		t.Fatalf("detail = %d %v", status, detail)
	}
	if body, ok := detail["issue"].(map[string]any)["body"]; !ok || body == "" {
		t.Errorf("detail issue body = %v, want the composed body", body)
	}
}

func TestRecordIdeaOutcome(t *testing.T) {
	e := newEnv(t, false)
	n := e.create("Consider caching", map[string]any{"type": "idea"})
	path := fmt.Sprintf("/api/issues/%d", n)
	const original = "### Problem\n\nReads are slow.\n\n### Open questions\n\n- How slow?\n\n### Outcome\n\nFirst decision.\n\n### Notes\n\nLeave me here."
	if code, res := e.call("PATCH", path, "", map[string]any{"body": original}); code != 200 {
		t.Fatalf("seed body: %d %v", code, res)
	}
	for _, input := range []map[string]any{
		{"outcome": "  ", "title": "must not change"},
		{"outcome": "See #2.", "body": "replacement"},
	} {
		if code, res := e.call("PATCH", path, "", input); code != 400 {
			t.Errorf("bad edit = %d %v", code, res)
		}
	}
	if code, res := e.call("PATCH", path, "", map[string]any{"outcome": "Pursued as #2."}); code != 200 {
		t.Errorf("record outcome = %d %v", code, res)
	}
	_, d := e.call("GET", path, "", nil)
	got := d["issue"].(map[string]any)
	want := strings.Replace(original, "First decision.", "First decision.\n\nPursued as #2.", 1)
	if got["body"] != want || got["title"] != "Consider caching" {
		t.Errorf("issue = %v, want body %q and original title", got, want)
	}
}
