// Package pr_test exercises the public pull-request workflow the way an
// external module does: only public imports, an isolated GitHub transport, and
// a real checkout with a pushed branch. Nothing here runs the hew binary or
// reaches the network.
package pr_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lumberbarons/hew/pr"
	"github.com/lumberbarons/hew/tracker"
)

// issueNode is the GraphQL shape the client reads for one issue.
func issueNode(number int, title string, labels []string, assignee string) map[string]any {
	nodes := make([]map[string]any, len(labels))
	for i, label := range labels {
		nodes[i] = map[string]any{"name": label}
	}
	assignees := []map[string]any{}
	if assignee != "" {
		assignees = append(assignees, map[string]any{"login": assignee})
	}
	return map[string]any{
		"id": fmt.Sprintf("NODE%d", number), "number": number, "title": title,
		"state": "OPEN", "stateReason": nil, "createdAt": "2026-07-01T00:00:00Z",
		"body":             "### Goal\n\nExpose the PR workflow.\n\n### Approach\n\nMove the decisions into the package.",
		"labels":           map[string]any{"nodes": nodes},
		"assignees":        map[string]any{"nodes": assignees},
		"parent":           nil,
		"subIssues":        map[string]any{"totalCount": 0, "nodes": []any{}},
		"subIssuesSummary": map[string]any{"total": 0, "completed": 0},
		"blockedBy":        map[string]any{"totalCount": 0, "nodes": []any{}},
	}
}

// createdPull is a pull request the fake server opened.
type createdPull struct {
	number int
	title  string
	body   string
	head   string
	base   string
	draft  bool
}

// githubFake answers the GraphQL queries and REST calls the workflow makes,
// from one in-memory issue set and pull-request state. It is a transport, not
// a process: nothing listens on a public address and no credential is read.
type githubFake struct {
	mu       sync.Mutex
	issues   []map[string]any
	viewer   string
	authFail bool
	openPR   *createdPull
	creates  []createdPull
	updates  []createdPull
}

func (f *githubFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/graphql":
		f.graphQL(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/pulls":
		var payload struct {
			Title string `json:"title"`
			Body  string `json:"body"`
			Head  string `json:"head"`
			Base  string `json:"base"`
			Draft bool   `json:"draft"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		opened := createdPull{number: 501, title: payload.Title, body: payload.Body,
			head: payload.Head, base: payload.Base, draft: payload.Draft}
		f.creates = append(f.creates, opened)
		f.openPR = &opened
		writeJSON(w, map[string]any{
			"number": 501, "html_url": "https://github.com/o/r/pull/501", "draft": payload.Draft,
		})
	case r.Method == http.MethodPatch && r.URL.Path == "/repos/o/r/pulls/501":
		var payload struct {
			Title string `json:"title"`
			Body  string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		edit := createdPull{number: 501, title: payload.Title, body: payload.Body}
		f.updates = append(f.updates, edit)
		if f.openPR != nil {
			f.openPR.title, f.openPR.body = payload.Title, payload.Body
		}
		writeJSON(w, map[string]any{})
	default:
		http.Error(w, "unexpected request "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}
}

func (f *githubFake) graphQL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if f.authFail {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
		return
	}
	switch {
	case strings.Contains(req.Query, "viewer"):
		writeJSON(w, map[string]any{"data": map[string]any{
			"viewer": map[string]any{"login": f.viewer},
		}})
	case strings.Contains(req.Query, "issues("):
		writeJSON(w, map[string]any{"data": map[string]any{
			"rateLimit": map[string]any{"cost": 1, "remaining": 4999, "resetAt": "2026-07-01T00:05:00Z"},
			"repository": map[string]any{"issues": map[string]any{
				"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""},
				"nodes":    f.issues,
			}},
		}})
	case strings.Contains(req.Query, "pullRequests("):
		nodes := []map[string]any{}
		total := 0
		if f.openPR != nil {
			total = 1
			nodes = append(nodes, map[string]any{
				"number": f.openPR.number, "url": "https://github.com/o/r/pull/501",
				"isDraft": f.openPR.draft, "title": f.openPR.title, "body": f.openPR.body,
				"baseRefName":    f.openPR.base,
				"headRepository": map[string]any{"nameWithOwner": "o/r"},
			})
		}
		writeJSON(w, map[string]any{"data": map[string]any{
			"repository": map[string]any{
				"defaultBranchRef": map[string]any{"name": "main"},
				"pullRequests":     map[string]any{"totalCount": total, "nodes": nodes},
			},
		}})
	default:
		http.Error(w, "unexpected query: "+req.Query, http.StatusBadRequest)
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	if err := json.NewEncoder(w).Encode(value); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// rewriteTransport sends every API request to the fake server while keeping
// go-gh's own host and path, which is what makes the transport a stand-in for
// the network rather than a different client.
type rewriteTransport struct{ target *url.URL }

func (t rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = t.target.Scheme
	clone.URL.Host = t.target.Host
	clone.Host = t.target.Host
	return http.DefaultTransport.RoundTrip(clone)
}

// openWorkflow serves the fake and opens the workflow against it.
func openWorkflow(t *testing.T, worktree string, fake *githubFake) (*pr.Client, error) {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_HOST", "github.com")
	t.Setenv("GH_TOKEN", "test-token")
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	return pr.Open(context.Background(), pr.Config{
		Worktree:  worktree,
		Repo:      "o/r",
		Transport: rewriteTransport{target: u},
	})
}

// checkout makes a real repository on a pushed conventional branch, tracking a
// bare remote exactly as a worker's checkout does.
func checkout(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(key, "GIT_") {
			t.Setenv(key, os.Getenv(key))
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	remote := t.TempDir()
	gitRun(t, remote, "init", "--bare", "--initial-branch=main")
	gitRun(t, dir, "init", "--initial-branch=feat/314-pr-workflow")
	gitRun(t, dir, "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "--allow-empty", "-m", "seed")
	gitRun(t, dir, "remote", "add", "origin", remote)
	gitRun(t, dir, "push", "-u", "origin", "feat/314-pr-workflow")
	return dir
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// TestExternalConsumerCreatesAndAmendsAPullRequest is the public workflow from
// outside the package: typed requests and results, an isolated transport, and
// a real checkout, with no hew binary and no network.
func TestExternalConsumerCreatesAndAmendsAPullRequest(t *testing.T) {
	fake := &githubFake{
		viewer: "octocat",
		issues: []map[string]any{issueNode(314, "PR workflow", []string{"P2", "task", "in-progress"}, "octocat")},
	}
	client, err := openWorkflow(t, checkout(t), fake)
	if err != nil {
		t.Fatal(err)
	}

	created, err := client.Create(context.Background(), pr.CreateRequest{
		Body: pr.Body{Sections: pr.Sections{Testing: "`go test ./...` passes"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Number != 501 || created.Fixes != 314 || created.Head != "feat/314-pr-workflow" {
		t.Errorf("created = %+v", created)
	}
	if created.Title != "chore: PR workflow" {
		t.Errorf("title = %q, want the type-derived prefix", created.Title)
	}
	if len(fake.creates) != 1 {
		t.Fatalf("creates = %+v", fake.creates)
	}
	if got := fake.creates[0]; got.head != "feat/314-pr-workflow" || got.base != "main" || !got.draft {
		t.Errorf("transport payload = %+v", got)
	}
	if !strings.HasSuffix(fake.creates[0].body, "Fixes #314") {
		t.Errorf("body = %q", fake.creates[0].body)
	}

	amended, err := client.Amend(context.Background(), pr.AmendRequest{
		Title: ptr("chore: expose the PR workflow"),
		Edit:  pr.BodyEdit{Testing: ptr("`go test -race ./...` passes")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if amended.Number != 501 || amended.Title != "chore: expose the PR workflow" {
		t.Errorf("amended = %+v", amended)
	}
	if len(fake.updates) != 1 {
		t.Fatalf("updates = %+v", fake.updates)
	}
	body := fake.updates[0].body
	if !strings.Contains(body, "`go test -race ./...` passes") || !strings.HasSuffix(body, "Fixes #314") {
		t.Errorf("amended body = %q", body)
	}
}

// A credential failure reaches the consumer as the public tracker type, so an
// orchestrator branches on one error vocabulary whichever handle failed.
func TestExternalConsumerSeesATypedAuthFailure(t *testing.T) {
	fake := &githubFake{viewer: "octocat", authFail: true}
	_, err := openWorkflow(t, checkout(t), fake)
	if err == nil {
		t.Fatal("Open succeeded without credentials")
	}
	var auth *tracker.AuthError
	if !errors.As(err, &auth) {
		t.Fatalf("err = %T (%v), want *tracker.AuthError", err, err)
	}
}

func ptr(value string) *string { return &value }

// A Client outlives the moment it was opened: the branch is pushed, or
// replaced, afterwards. Each operation reads the checkout as it is then, so a
// push after Open is seen and a pull request opens from the branch now checked
// out rather than the one Open found.
func TestExternalConsumerSeesTheCheckoutAsItIsNow(t *testing.T) {
	dir := checkout(t)
	fake := &githubFake{
		viewer: "octocat",
		issues: []map[string]any{issueNode(314, "PR workflow", []string{"P2", "task", "in-progress"}, "octocat")},
	}
	gitRun(t, dir, "checkout", "-b", "feat/314-second")
	client, err := openWorkflow(t, dir, fake)
	if err != nil {
		t.Fatal(err)
	}
	body := pr.Body{Sections: pr.Sections{Testing: "`go test ./...` passes"}}

	_, err = client.Create(context.Background(), pr.CreateRequest{Body: body})
	var noUpstream *pr.NoUpstreamError
	if !errors.As(err, &noUpstream) || noUpstream.Branch != "feat/314-second" {
		t.Fatalf("err = %T (%v), want *pr.NoUpstreamError for feat/314-second", err, err)
	}

	gitRun(t, dir, "push", "-u", "origin", "feat/314-second")
	created, err := client.Create(context.Background(), pr.CreateRequest{Body: body})
	if err != nil {
		t.Fatalf("Create after the push: %v", err)
	}
	if created.Head != "feat/314-second" {
		t.Errorf("head = %q, want the branch pushed after Open", created.Head)
	}
}

// A detached HEAD has no branch name to open a pull request from, and Open
// refuses before the workflow reads an issue.
func TestExternalConsumerOnADetachedHead(t *testing.T) {
	dir := checkout(t)
	gitRun(t, dir, "checkout", "--detach")
	_, err := openWorkflow(t, dir, &githubFake{viewer: "octocat"})
	if err == nil || !strings.Contains(err.Error(), "detached") {
		t.Fatalf("err = %v, want a detached-HEAD refusal", err)
	}
}
