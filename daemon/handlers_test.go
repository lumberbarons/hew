package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/pr"
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
		for _, bad := range []string{"abc", "0", "-3"} {
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
		Warnings: []pr.Warning{{Kind: pr.WarnMissingTesting}},
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
	if w := res["warnings"].([]any); len(w) != 1 || w[0] != "missing-testing" {
		t.Errorf("warnings = %v, want [missing-testing]", w)
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
