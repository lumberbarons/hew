package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/lumberbarons/hew/daemon"
	"github.com/lumberbarons/hew/tracker"
)

// checkout makes an isolated git checkout with a real local tracker and,
// optionally, one hand-written untriaged issue (#1) that the write path itself
// would never produce.
func checkout(t *testing.T, untriaged bool) (string, tracker.Target) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "GIT_") {
			t.Setenv(k, os.Getenv(k))
			if err := os.Unsetenv(k); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "--initial-branch=main").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tracker.Init(context.Background(), tracker.InitConfig{Worktree: root, Backend: tracker.BackendLocal}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if untriaged {
		f, err := os.OpenFile(filepath.Join(root, ".hew", "issues.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		line := `{"_type":"issue","number":1,"title":"raw unvetted report","body":"secret text","state":"OPEN","created_at":"2026-01-01T00:00:00Z"}` + "\n"
		if _, err := f.WriteString(line); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}
	target, err := tracker.Detect(root)
	if err != nil {
		t.Fatal(err)
	}
	return root, target
}

type env struct {
	t    *testing.T
	url  string
	root string
}

func newEnv(t *testing.T, untriaged bool) *env { return newEnvWith(t, untriaged, nil) }

// newEnvWith is newEnv with a pull-request client, so the PR routes can be
// driven without a GitHub checkout. nil means a backend without pull requests.
func newEnvWith(t *testing.T, untriaged bool, prs daemon.PullRequests) *env {
	t.Helper()
	root, target := checkout(t, untriaged)
	tr, err := tracker.Open(context.Background(), tracker.Config{Worktree: root, Actor: "daemon"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	srv := httptest.NewServer(daemon.New(tr, prs, target).Handler())
	t.Cleanup(srv.Close)
	return &env{t: t, url: srv.URL, root: root}
}

// appendRaw writes lines the write path would never produce straight into the
// tracker file: a dependency cycle, hostile text, or garbage that breaks reads.
func (e *env) appendRaw(lines ...string) {
	e.t.Helper()
	f, err := os.OpenFile(filepath.Join(e.root, ".hew", "issues.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			e.t.Fatal(err)
		}
	}
}

// call sends a JSON request as actor and returns the status and decoded body.
func (e *env) call(method, path, actor string, body any) (int, map[string]any) {
	e.t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, e.url+path, rdr)
	if err != nil {
		e.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if actor != "" {
		req.Header.Set("X-Hew-Actor", actor)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	var raw any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err == nil {
		if m, ok := raw.(map[string]any); ok {
			out = m
		} else {
			out = map[string]any{"_list": raw}
		}
	}
	return resp.StatusCode, out
}

func (e *env) create(title string, extra map[string]any) int {
	e.t.Helper()
	body := map[string]any{
		"title": title, "type": "task", "priority": "P2",
		"body": map[string]any{"goal": "g", "approach": "a", "doneWhen": []string{"it works"}},
	}
	for k, v := range extra {
		body[k] = v
	}
	status, res := e.call("POST", "/api/issues", "", body)
	if status != http.StatusCreated {
		e.t.Fatalf("create %q = %d %v", title, status, res)
	}
	return int(res["issue"].(map[string]any)["number"].(float64))
}

func numbers(list any) []int {
	var out []int
	for _, v := range list.([]any) {
		out = append(out, int(v.(map[string]any)["number"].(float64)))
	}
	return out
}

func contains(ns []int, n int) bool {
	for _, x := range ns {
		if x == n {
			return true
		}
	}
	return false
}

func TestConfigAndCreateAppearOnTheBoard(t *testing.T) {
	e := newEnv(t, false)
	status, cfg := e.call("GET", "/api/config", "", nil)
	if status != 200 || cfg["backend"] != "local" || cfg["actorEditable"] != true || cfg["pullRequests"] != false {
		t.Fatalf("config = %d %v", status, cfg)
	}
	n := e.create("First", nil)
	status, board := e.call("GET", "/api/board", "", nil)
	if status != 200 || !contains(numbers(board["ready"]), n) {
		t.Fatalf("board = %d %v, want #%d ready", status, board, n)
	}
	// Empty sections serialise as [], never null: the frontend relies on it.
	if board["claimed"] == nil || board["blocked"] == nil || board["warnings"] == nil {
		t.Errorf("board has null lists: %v", board)
	}
	status, d := e.call("GET", "/api/issues/1", "", nil)
	issue := d["issue"].(map[string]any)
	if status != 200 || issue["title"] != "First" || !strings.Contains(issue["body"].(string), "it works") {
		t.Fatalf("detail = %d %v", status, d)
	}
}

// Ready is the implementation queue on the daemon too: an unclaimed, unblocked
// idea beside an equivalent task is absent from /api/ready, the board and the
// primer. An epic keeps an idea child attached outside hew visible in detail,
// but never selects it as next — with only an idea, nothing is next.
func TestIdeasStayOutOfReadyAndOutOfEpicNext(t *testing.T) {
	e := newEnv(t, false)
	e.appendRaw(
		`{"number":1,"title":"Ship the cache","body":"","state":"OPEN","created_at":"2026-01-01T00:00:00Z","labels":["P1","epic"]}`,
		`{"number":2,"title":"Consider a cache","body":"","state":"OPEN","created_at":"2026-01-01T00:00:00Z","labels":["P0","idea"],"parent":1}`,
		`{"number":3,"title":"Wire the cache","body":"","state":"OPEN","created_at":"2026-01-01T00:00:00Z","labels":["P2","task"],"parent":1}`,
		`{"number":4,"title":"Investigate only","body":"","state":"OPEN","created_at":"2026-01-01T00:00:00Z","labels":["P1","epic"]}`,
		`{"number":5,"title":"Consider only","body":"","state":"OPEN","created_at":"2026-01-01T00:00:00Z","labels":["P1","idea"],"parent":4}`,
	)

	status, ready := e.call("GET", "/api/ready", "", nil)
	if status != 200 || !slices.Equal(numbers(ready["_list"]), []int{3}) {
		t.Fatalf("ready = %d %v, want the task only", status, ready["_list"])
	}
	for _, path := range []string{"/api/board", "/api/primer"} {
		_, res := e.call("GET", path, "", nil)
		if got := numbers(res["ready"]); !slices.Equal(got, []int{3}) {
			t.Errorf("%s ready = %v, want the task only", path, got)
		}
	}

	_, d := e.call("GET", "/api/issues/1", "", nil)
	if got := numbers(d["children"]); !slices.Equal(got, []int{2, 3}) {
		t.Errorf("children = %v, want both the idea and the task", got)
	}
	if next, ok := d["next"].(float64); !ok || int(next) != 3 {
		t.Errorf("next = %v, want the task #3", d["next"])
	}

	_, d = e.call("GET", "/api/issues/4", "", nil)
	if got := numbers(d["children"]); !slices.Equal(got, []int{5}) {
		t.Errorf("children = %v, want the idea child", got)
	}
	if d["next"] != nil {
		t.Errorf("next = %v, want null for an idea-only sequence", d["next"])
	}
}

func TestClaimRefusalsMapToDistinctKinds(t *testing.T) {
	e := newEnv(t, false)
	n := e.create("Contested", nil)
	path := "/api/issues/1/start"

	if status, res := e.call("POST", path, "alice", map[string]any{}); status != 200 || res["claimant"] != "alice" {
		t.Fatalf("alice start = %d %v", status, res)
	}
	status, res := e.call("POST", path, "bob", map[string]any{})
	if status != 409 || res["error"] != "claimed_by_other" {
		t.Errorf("bob start = %d %v, want 409 claimed_by_other (the CLI's exit 3)", status, res)
	}
	status, res = e.call("POST", path, "alice", map[string]any{})
	if status != 409 || res["error"] != "claimed_by_you" {
		t.Errorf("alice again = %d %v, want 409 claimed_by_you (the CLI's exit 5)", status, res)
	}
	if status, res := e.call("POST", "/api/issues/1/release", "bob", map[string]any{}); status != 409 || res["error"] != "not_owner" {
		t.Errorf("bob release = %d %v, want 409 not_owner", status, res)
	}
	if status, res := e.call("POST", "/api/issues/1/release", "alice", map[string]any{}); status != 200 {
		t.Errorf("alice release = %d %v", status, res)
	}
	_ = n
}

func TestBlockedStartAndCycleRefusal(t *testing.T) {
	e := newEnv(t, false)
	a := e.create("A", nil)
	b := e.create("B", map[string]any{"blockedBy": []int{a}})

	status, res := e.call("POST", "/api/issues/2/start", "w", map[string]any{})
	if status != 409 || res["error"] != "blocked" {
		t.Fatalf("start blocked = %d %v, want 409 blocked (exit 6)", status, res)
	}
	if ob := res["fields"].(map[string]any)["openBlockers"].([]any); len(ob) != 1 || int(ob[0].(float64)) != a {
		t.Errorf("openBlockers = %v, want [%d]", ob, a)
	}
	_, ready := e.call("GET", "/api/ready", "", nil)
	if contains(numbers(ready["_list"]), b) {
		t.Errorf("blocked #%d is in ready", b)
	}

	status, res = e.call("POST", "/api/issues/1/blockers", "", map[string]any{"blockers": []int{b}})
	if status != 409 || res["error"] != "cycle" {
		t.Errorf("A blocked-by B = %d %v, want 409 cycle", status, res)
	}

	if status, res = e.call("DELETE", "/api/issues/2/blockers/1", "", nil); status != 200 {
		t.Fatalf("unblock = %d %v", status, res)
	}
	if status, res = e.call("POST", "/api/issues/2/start", "w", map[string]any{}); status != 200 {
		t.Errorf("start after unblock = %d %v", status, res)
	}
}

func TestCloseNeedsReasonAndReopenRoundTrips(t *testing.T) {
	e := newEnv(t, false)
	e.create("Closable", nil)

	status, res := e.call("POST", "/api/issues/1/close", "", map[string]any{})
	if status != 400 || res["error"] != "invalid_input" || res["fields"].(map[string]any)["field"] != "reason" {
		t.Fatalf("close w/o reason = %d %v", status, res)
	}
	if status, res = e.call("POST", "/api/issues/1/close", "", map[string]any{"reason": "done", "completed": true}); status != 200 {
		t.Fatalf("close = %d %v", status, res)
	}
	if status, res = e.call("POST", "/api/issues/1/start", "w", map[string]any{}); status != 409 || res["error"] != "closed" {
		t.Errorf("start closed = %d %v, want 409 closed", status, res)
	}
	if status, res = e.call("POST", "/api/issues/1/reopen", "", map[string]any{"reason": "regressed"}); status != 200 || res["reopened"] != true {
		t.Errorf("reopen = %d %v", status, res)
	}
	if status, _ = e.call("POST", "/api/issues/99/reopen", "", map[string]any{"reason": "x"}); status != 404 {
		t.Errorf("reopen missing = %d, want 404", status)
	}
}

func TestEditSwapsPriorityAndRefusesBlankTitle(t *testing.T) {
	e := newEnv(t, false)
	e.create("Editable", nil)
	if status, res := e.call("PATCH", "/api/issues/1", "", map[string]any{"priority": "P0"}); status != 200 {
		t.Fatalf("edit = %d %v", status, res)
	}
	_, d := e.call("GET", "/api/issues/1", "", nil)
	if p := d["issue"].(map[string]any)["priority"]; p != "P0" {
		t.Errorf("priority = %v, want P0", p)
	}
	if status, res := e.call("PATCH", "/api/issues/1", "", map[string]any{"title": ""}); status != 400 {
		t.Errorf("blank title = %d %v, want 400", status, res)
	}
	if status, _ := e.call("PATCH", "/api/issues/1", "", map[string]any{"bogus": 1}); status != 400 {
		t.Errorf("unknown field = %d, want 400", status)
	}
}

func TestCreateValidationAndDryRunWriteNothing(t *testing.T) {
	e := newEnv(t, false)
	status, res := e.call("POST", "/api/issues", "", map[string]any{"title": "No type", "body": map[string]any{"goal": "g"}})
	if status != 400 || res["error"] != "invalid_input" {
		t.Fatalf("create w/o type = %d %v", status, res)
	}
	ok := map[string]any{"title": "Dry", "type": "bug", "body": map[string]any{"problem": "p", "fix": "f", "doneWhen": []string{"x"}}}
	if status, res = e.call("POST", "/api/issues?dryRun=1", "", ok); status != 200 || res["ok"] != true {
		t.Fatalf("dry run = %d %v", status, res)
	}
	_, issues := e.call("GET", "/api/issues?state=all", "", nil)
	if n := len(issues["_list"].([]any)); n != 0 {
		t.Errorf("dry run wrote %d issues", n)
	}
}

func TestUntriagedTextOnlyThroughTriage(t *testing.T) {
	e := newEnv(t, true)
	e.create("Vetted", nil) // becomes #2

	for _, path := range []string{"/api/ready", "/api/issues", "/api/search?q=raw", "/api/search?q=secret"} {
		_, res := e.call("GET", path, "", nil)
		raw, _ := json.Marshal(res)
		if strings.Contains(string(raw), "raw unvetted") || strings.Contains(string(raw), "secret text") {
			t.Errorf("%s leaked untriaged text: %s", path, raw)
		}
	}
	_, board := e.call("GET", "/api/board", "", nil)
	if board["untriaged"] != float64(1) {
		t.Errorf("board untriaged = %v, want the count 1", board["untriaged"])
	}
	_, primer := e.call("GET", "/api/primer", "", nil)
	if primer["untriaged"] != float64(1) {
		t.Errorf("primer untriaged = %v, want 1", primer["untriaged"])
	}
	_, tri := e.call("GET", "/api/triage", "", nil)
	if got := numbers(tri["issues"]); len(got) != 1 || got[0] != 1 {
		t.Errorf("triage = %v, want [1]", got)
	}
	// Claiming an untriaged issue is triage: it needs a priority.
	if status, res := e.call("POST", "/api/issues/1/start", "w", map[string]any{}); status != 400 || res["error"] != "untriaged" {
		t.Errorf("start untriaged = %d %v, want 400 untriaged", status, res)
	}
	if status, res := e.call("POST", "/api/issues/1/start", "w", map[string]any{"priority": "P2"}); status != 200 {
		t.Errorf("start with priority = %d %v", status, res)
	}
}

func TestEpicsDetailAndApply(t *testing.T) {
	e := newEnv(t, false)
	plan := map[string]any{"entries": []map[string]any{
		{"id": "e", "title": "Epic: Big", "type": "epic", "sections": map[string]any{"goal": "g", "doneWhen": []string{"all"}}},
		{"id": "c1", "title": "Child one", "type": "task", "parent": map[string]any{"id": "e"}},
	}}
	if status, res := e.call("POST", "/api/apply?dryRun=1", "", plan); status != 200 || res["dryRun"] != true || res["created"] != float64(0) {
		t.Fatalf("apply dry run = %d %v", status, res)
	}
	status, res := e.call("POST", "/api/apply", "", plan)
	if status != 200 || res["created"] != float64(2) {
		t.Fatalf("apply = %d %v", status, res)
	}
	_, epics := e.call("GET", "/api/epics", "", nil)
	if len(numbers(epics["_list"])) != 1 {
		t.Fatalf("epics = %v", epics)
	}
	_, d := e.call("GET", "/api/epics/1", "", nil)
	if kids := d["children"].([]any); len(kids) != 1 {
		t.Errorf("epic children = %v, want 1", kids)
	}
	if status, res := e.call("POST", "/api/issues/1/start", "w", map[string]any{}); status != 409 || res["error"] != "epic" {
		t.Errorf("start epic = %d %v, want 409 epic", status, res)
	}
}

func TestBodyComposeAndCheck(t *testing.T) {
	e := newEnv(t, false)
	status, res := e.call("POST", "/api/body/compose", "", map[string]any{"body": map[string]any{"goal": "ship", "doneWhen": []string{"shipped"}}})
	if status != 200 || !strings.Contains(res["body"].(string), "### Goal") {
		t.Fatalf("compose = %d %v", status, res)
	}
	status, res = e.call("POST", "/api/body/check", "", map[string]any{"text": "call the foo_bar() helper"})
	if status != 200 || res["findings"] == nil {
		t.Errorf("check = %d %v", status, res)
	}
}

func TestPullRequestRoutesRefuseOnLocal(t *testing.T) {
	e := newEnv(t, false)
	for _, m := range []string{"POST", "PATCH"} {
		if status, res := e.call(m, "/api/pr", "", map[string]any{}); status != 501 || res["error"] != "no_pull_requests" {
			t.Errorf("%s /api/pr = %d %v, want 501", m, status, res)
		}
	}
}

// TestConcurrentStartsHaveOneWinner is the local store's atomic claim seen
// through HTTP: many workers race one issue and exactly one wins.
func TestConcurrentStartsHaveOneWinner(t *testing.T) {
	e := newEnv(t, false)
	e.create("Race", nil)
	const workers = 8
	statuses := make([]int, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses[i], _ = e.call("POST", "/api/issues/1/start", "worker-"+string(rune('a'+i)), map[string]any{})
		}()
	}
	wg.Wait()
	won := 0
	for _, s := range statuses {
		switch s {
		case 200:
			won++
		case 409:
		default:
			t.Errorf("unexpected status %d", s)
		}
	}
	if won != 1 {
		t.Errorf("%d winners (%v), want exactly 1", won, statuses)
	}
}

func TestGuardRefusesForgeableRequests(t *testing.T) {
	e := newEnv(t, false)
	do := func(mutate func(*http.Request)) int {
		req, _ := http.NewRequest("POST", e.url+"/api/issues/1/start", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		mutate(req)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	e.create("Target", nil)
	if s := do(func(r *http.Request) { r.Host = "evil.example:7777" }); s != 403 {
		t.Errorf("rebound Host = %d, want 403", s)
	}
	if s := do(func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }); s != 403 {
		t.Errorf("cross Origin = %d, want 403", s)
	}
	if s := do(func(r *http.Request) { r.Header.Set("Origin", "null") }); s != 403 {
		t.Errorf("null Origin = %d, want 403", s)
	}
	if s := do(func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }); s != 415 {
		t.Errorf("text/plain = %d, want 415", s)
	}
	if s := do(func(r *http.Request) { r.Header.Set("Origin", e.url) }); s != 200 {
		t.Errorf("same Origin = %d, want 200", s)
	}
}

func TestValidateAddrOnlyAllowsLoopback(t *testing.T) {
	for _, ok := range []string{"127.0.0.1:7777", "localhost:80", "[::1]:9"} {
		if err := daemon.ValidateAddr(ok); err != nil {
			t.Errorf("ValidateAddr(%q) = %v, want ok", ok, err)
		}
	}
	for _, bad := range []string{"0.0.0.0:7777", ":7777", "192.168.1.5:80", "example.com:80", "nonsense"} {
		if err := daemon.ValidateAddr(bad); err == nil {
			t.Errorf("ValidateAddr(%q) accepted a non-loopback address", bad)
		}
	}
}

func TestUnknownAPIRouteIsAProblemNotAPage(t *testing.T) {
	e := newEnv(t, false)
	if status, res := e.call("GET", "/api/nope", "", nil); status != 404 || res["error"] != "no_route" {
		t.Errorf("unknown route = %d %v", status, res)
	}
}

// TestListRoutesOmitBodies pins the one place the daemon deliberately differs
// from the library: snapshot reads carry bodies, list rows must not.
func TestListRoutesOmitBodies(t *testing.T) {
	e := newEnv(t, false)
	e.create("Has a body", nil)
	for _, path := range []string{"/api/ready", "/api/issues", "/api/search?q=body"} {
		_, res := e.call("GET", path, "", nil)
		raw, _ := json.Marshal(res)
		if strings.Contains(string(raw), `"body"`) || strings.Contains(string(raw), "it works") {
			t.Errorf("%s carries a body: %s", path, raw)
		}
	}
	_, board := e.call("GET", "/api/board", "", nil)
	if raw, _ := json.Marshal(board); strings.Contains(string(raw), "it works") {
		t.Errorf("board carries a body: %s", raw)
	}
	_, d := e.call("GET", "/api/issues/1", "", nil)
	if d["issue"].(map[string]any)["body"] == nil {
		t.Error("detail dropped the body")
	}
}
