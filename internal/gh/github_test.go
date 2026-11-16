package gh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/lumberbarons/hew/internal/model"
)

// rewriteTransport sends every request to the test server, whatever host
// go-gh computed.
type rewriteTransport struct{ target *url.URL }

func (t rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = t.target.Scheme
	req.URL.Host = t.target.Host
	return http.DefaultTransport.RoundTrip(req)
}

type recordedRequest struct {
	Method string
	Path   string
	Body   string
	Header http.Header
}

type fakeServer struct {
	*httptest.Server
	// mu guards requests: the handler appends from the server's goroutines,
	// so a test that drives the client concurrently would race with itself.
	mu       sync.Mutex
	requests []recordedRequest
	// graphql maps a substring of the query to a response body.
	graphql map[string]string
	// graphqlFunc maps a substring of the query to a response computed from
	// the request variables; it wins over the static graphql map, letting a
	// test answer node-ID lookups with per-number IDs.
	graphqlFunc map[string]func(vars map[string]any) string
	// rest maps "METHOD path" to status + response body.
	rest map[string]restResponse
}

type restResponse struct {
	status int
	body   string
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{
		graphql:     map[string]string{},
		graphqlFunc: map[string]func(vars map[string]any) string{},
		rest:        map[string]restResponse{},
	}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, recordedRequest{Method: r.Method, Path: r.URL.Path, Body: string(body), Header: r.Header.Clone()})
		f.mu.Unlock()
		if r.URL.Path == "/graphql" {
			var payload struct {
				Query     string         `json:"query"`
				Variables map[string]any `json:"variables"`
			}
			_ = json.Unmarshal(body, &payload)
			for substr, fn := range f.graphqlFunc {
				if strings.Contains(payload.Query, substr) {
					fmt.Fprint(w, fn(payload.Variables))
					return
				}
			}
			for substr, resp := range f.graphql {
				if strings.Contains(payload.Query, substr) {
					fmt.Fprint(w, resp)
					return
				}
			}
			t.Errorf("unexpected GraphQL query: %s", payload.Query)
			fmt.Fprint(w, `{"data":{}}`)
			return
		}
		key := r.Method + " " + r.URL.Path
		if r.URL.RawQuery != "" {
			key += "?" + r.URL.RawQuery
		}
		if resp, ok := f.rest[key]; ok {
			// GitHub declares JSON on every REST response, and go-gh only
			// parses an error body when it does — without this header a
			// failure arrives as a bare status line, so a test asserting on
			// the server's message would be testing the fake, not the client.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.status)
			fmt.Fprint(w, resp.body)
			return
		}
		t.Errorf("unexpected REST request: %s", key)
		w.WriteHeader(500)
	}))
	t.Cleanup(f.Close)
	return f
}

// recorded returns a snapshot of the requests seen so far.
func (f *fakeServer) recorded() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedRequest(nil), f.requests...)
}

func (f *fakeServer) client(t *testing.T) *GitHub {
	t.Helper()
	u, _ := url.Parse(f.URL)
	c, err := NewWithOptions(Repo{Owner: "o", Name: "r"}, api.ClientOptions{
		Host:      "github.com",
		AuthToken: "test-token",
		Transport: rewriteTransport{target: u},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func issueJSON(number int, extra string) string {
	base := fmt.Sprintf(`"id":"NODE%d","number":%d,"title":"Issue %d","state":"OPEN","stateReason":null,
		"createdAt":"2026-07-01T00:00:00Z",
		"labels":{"nodes":[{"name":"P2"},{"name":"bug"}]},
		"assignees":{"nodes":[]},
		"parent":null,
		"subIssues":{"totalCount":0,"nodes":[]},
		"subIssuesSummary":{"total":0,"completed":0},
		"blockedBy":{"totalCount":0,"nodes":[]}`, number, number, number)
	if extra != "" {
		base += "," + extra
	}
	return "{" + base + "}"
}

func decodeBody(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("request body is not JSON: %q: %v", body, err)
	}
	return m
}

// gqlVariables returns the variables of the first recorded GraphQL request
// whose query mentions the given mutation.
func gqlVariables(t *testing.T, f *fakeServer, mutation string) map[string]any {
	t.Helper()
	for _, r := range f.requests {
		if !strings.Contains(r.Body, mutation+"(") {
			continue
		}
		vars, _ := decodeBody(t, r.Body)["variables"].(map[string]any)
		if vars == nil {
			t.Fatalf("%s request has no variables: %s", mutation, r.Body)
		}
		return vars
	}
	t.Fatalf("no %s mutation was sent", mutation)
	return nil
}

func TestViewer(t *testing.T) {
	f := newFakeServer(t)
	f.graphql["viewer"] = `{"data":{"viewer":{"login":"lumberbarons"}}}`
	login, err := f.client(t).Viewer(context.Background())
	if err != nil || login != "lumberbarons" {
		t.Fatalf("Viewer() = %q, %v", login, err)
	}
}

func TestListIssuesPaginates(t *testing.T) {
	f := newFakeServer(t)
	page := 0
	f.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Variables map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(body, &payload)
		page++
		// The states filter is what keeps closed issues out of ready/prime;
		// every page must carry it.
		if got := payload.Variables["states"]; !reflect.DeepEqual(got, []any{"OPEN"}) {
			t.Errorf("page %d states = %v, want [OPEN]", page, got)
		}
		if page == 1 {
			if payload.Variables["cursor"] != nil {
				t.Errorf("first page had cursor %v", payload.Variables["cursor"])
			}
			fmt.Fprintf(w, `{"data":{"repository":{"issues":{"pageInfo":{"hasNextPage":true,"endCursor":"CUR"},"nodes":[%s]}}}}`, issueJSON(1, ""))
			return
		}
		if payload.Variables["cursor"] != "CUR" {
			t.Errorf("second page cursor = %v", payload.Variables["cursor"])
		}
		fmt.Fprintf(w, `{"data":{"repository":{"issues":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[%s]}}}}`, issueJSON(2, ""))
	})
	issues, err := f.client(t).ListIssues(context.Background(), []IssueState{StateOpen})
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 2 || issues[0].Number != 1 || issues[1].Number != 2 {
		t.Fatalf("ListIssues() = %+v", issues)
	}
	if issues[0].ID != "NODE1" || issues[0].Labels[0] != "P2" {
		t.Errorf("issue fields not mapped: %+v", issues[0])
	}
}

func TestGetIssueMapsAllFields(t *testing.T) {
	f := newFakeServer(t)
	extra := `"body":"the body",
		"comments":{"totalCount":7,"nodes":[{"author":{"login":"alice"},"createdAt":"2026-07-02T00:00:00Z","body":"hi"},{"author":null,"createdAt":"2026-07-03T00:00:00Z","body":"ghost"}]}`
	node := `{"id":"NODE9","number":9,"title":"T","state":"OPEN","stateReason":"REOPENED",
		"createdAt":"2026-07-01T00:00:00Z",
		"labels":{"nodes":[{"name":"P1"}]},
		"assignees":{"nodes":[{"login":"bob"}]},
		"parent":{"number":3,"state":"OPEN","title":"Epic: parent"},
		"subIssues":{"totalCount":2,"nodes":[{"number":10,"state":"OPEN"},{"number":11,"state":"CLOSED"}]},
		"subIssuesSummary":{"total":2,"completed":1},
		"blockedBy":{"totalCount":1,"nodes":[{"number":4,"state":"OPEN"}]},` + extra + `}`
	f.graphql["issue(number:"] = `{"data":{"repository":{"issue":` + node + `}}}`

	i, err := f.client(t).GetIssue(context.Background(), 9)
	if err != nil {
		t.Fatal(err)
	}
	if i.ID != "NODE9" || i.Number != 9 || i.Title != "T" || i.State != "OPEN" || i.StateReason != "REOPENED" {
		t.Errorf("identity fields: %+v", i)
	}
	if !i.CreatedAt.Equal(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("CreatedAt = %v", i.CreatedAt)
	}
	if i.Body != "the body" || i.Parent.Number != 3 || i.Parent.State != "OPEN" || i.ParentTitle != "Epic: parent" {
		t.Errorf("core fields: %+v", i)
	}
	if !reflect.DeepEqual(i.Labels, []string{"P1"}) {
		t.Errorf("labels: %+v", i.Labels)
	}
	if i.SubIssuesCompleted != 1 || i.SubIssuesTotal != 2 {
		t.Errorf("sub-issue counters: %+v", i)
	}
	if want := []model.Ref{{Number: 10, State: "OPEN"}, {Number: 11, State: "CLOSED"}}; !reflect.DeepEqual(i.SubIssues, want) {
		t.Errorf("sub-issues: %+v", i.SubIssues)
	}
	if want := []model.Ref{{Number: 4, State: "OPEN"}}; !reflect.DeepEqual(i.BlockedBy, want) || i.BlockedByTotal != 1 {
		t.Errorf("blockers: %+v (total %d)", i.BlockedBy, i.BlockedByTotal)
	}
	if i.CommentsTotal != 7 {
		t.Errorf("CommentsTotal = %d, want 7", i.CommentsTotal)
	}
	want := []model.Comment{
		{Author: "alice", CreatedAt: time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC), Body: "hi"},
		{Author: "", CreatedAt: time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC), Body: "ghost"},
	}
	if !reflect.DeepEqual(i.Comments, want) {
		t.Errorf("comments: %+v", i.Comments)
	}
	if !reflect.DeepEqual(i.Assignees, []string{"bob"}) {
		t.Errorf("assignees: %+v", i.Assignees)
	}

	// The fake echoes canned JSON whatever the query selects, so pin the
	// load-bearing selections in the query text itself.
	query := f.requests[0].Body
	for _, sel := range []string{
		"comments(last:", "blockedBy(first:", "totalCount",
		"subIssuesSummary { total completed }", "parent { number state title }",
	} {
		if !strings.Contains(query, sel) {
			t.Errorf("GetIssue query no longer selects %q", sel)
		}
	}
	// "body" must be selected twice: the issue body and each comment's body.
	if strings.Count(query, "body") < 2 {
		t.Errorf("GetIssue query no longer selects the issue body: %s", query)
	}
}

func TestSearchIssues(t *testing.T) {
	f := newFakeServer(t)
	// The empty object is a non-issue node (a PR matched via user-supplied
	// qualifiers); it must be dropped, not mapped as issue #0.
	f.graphql["search(type: ISSUE"] = fmt.Sprintf(
		`{"data":{"search":{"issueCount":43,"nodes":[%s,{},%s]}}}`,
		issueJSON(7, ""), issueJSON(9, ""))
	issues, total, err := f.client(t).SearchIssues(context.Background(), "retry loop")
	if err != nil {
		t.Fatal(err)
	}
	if total != 43 {
		t.Errorf("total = %d, want 43", total)
	}
	if len(issues) != 2 || issues[0].Number != 7 || issues[1].Number != 9 {
		t.Fatalf("SearchIssues() = %+v", issues)
	}
	if issues[0].Labels[0] != "P2" {
		t.Errorf("issue fields not mapped: %+v", issues[0])
	}
	// The repo scope and is:issue must ride in the search string variable —
	// never interpolated into the query text, where user terms could corrupt
	// the query — with the user's terms appended verbatim.
	req := f.requests[len(f.requests)-1]
	var payload struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.Unmarshal([]byte(req.Body), &payload); err != nil {
		t.Fatalf("request body: %v", err)
	}
	if payload.Variables["q"] != "repo:o/r is:issue retry loop" {
		t.Errorf("search variable q = %v", payload.Variables["q"])
	}
	if strings.Contains(payload.Query, "retry loop") {
		t.Errorf("terms interpolated into query text: %s", payload.Query)
	}
	if !strings.Contains(payload.Query, fmt.Sprintf("first: %d", searchCap)) {
		t.Errorf("search not capped at %d: %s", searchCap, payload.Query)
	}
}

func TestGetIssueNotFound(t *testing.T) {
	f := newFakeServer(t)
	f.graphql["issue(number:"] = `{"data":{"repository":{"issue":null}}}`
	_, err := f.client(t).GetIssue(context.Background(), 404)
	if err == nil || !strings.Contains(err.Error(), "#404 not found") {
		t.Fatalf("err = %v", err)
	}
	var notFound *NotFoundError
	if !errors.As(err, &notFound) || notFound.Issue != 404 {
		t.Errorf("err = %v, want a NotFoundError for #404", err)
	}
}

// The message names the target the store searched: GitHub adds the
// repository, a local store has none to name.
func TestNotFoundErrorMessageNamesTheTarget(t *testing.T) {
	if got, want := (&NotFoundError{Issue: 7}).Error(), "issue #7 not found"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	remote := &NotFoundError{Issue: 7, Repo: Repo{Owner: "o", Name: "r"}}
	if got, want := remote.Error(), "issue #7 not found in o/r"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestCreateIssue(t *testing.T) {
	f := newFakeServer(t)
	f.rest["POST /repos/o/r/issues"] = restResponse{201, `{"node_id":"N1","number":42,"title":"New"}`}
	i, err := f.client(t).CreateIssue(context.Background(), "New", "body text", []string{"P2", "bug"})
	if err != nil || i.Number != 42 || i.ID != "N1" {
		t.Fatalf("CreateIssue() = %+v, %v", i, err)
	}
	got := decodeBody(t, f.requests[len(f.requests)-1].Body)
	want := map[string]any{"title": "New", "body": "body text", "labels": []any{"P2", "bug"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("request payload = %v, want %v", got, want)
	}
}

func TestCreateIssueOmitsEmptyBody(t *testing.T) {
	f := newFakeServer(t)
	f.rest["POST /repos/o/r/issues"] = restResponse{201, `{"node_id":"N1","number":42,"title":"New"}`}
	if _, err := f.client(t).CreateIssue(context.Background(), "New", "", []string{"P2"}); err != nil {
		t.Fatal(err)
	}
	got := decodeBody(t, f.requests[len(f.requests)-1].Body)
	// An empty body must be omitted, not sent as "".
	want := map[string]any{"title": "New", "labels": []any{"P2"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("request payload = %v, want %v", got, want)
	}
}

func TestSimpleRESTMutations(t *testing.T) {
	f := newFakeServer(t)
	f.rest["PATCH /repos/o/r/issues/5"] = restResponse{200, `{}`}
	f.rest["POST /repos/o/r/issues/5/labels"] = restResponse{200, `[]`}
	f.rest["POST /repos/o/r/issues/5/assignees"] = restResponse{201, `{}`}
	f.rest["DELETE /repos/o/r/issues/5/assignees"] = restResponse{200, `{}`}
	f.rest["POST /repos/o/r/issues/5/comments"] = restResponse{201, `{}`}
	c := f.client(t)
	ctx := context.Background()
	if err := c.EditTitle(ctx, 5, "Renamed"); err != nil {
		t.Error(err)
	}
	if err := c.EditBody(ctx, 5, "### Goal\n\nrewritten\n"); err != nil {
		t.Error(err)
	}
	if err := c.AddLabels(ctx, 5, []string{"P1"}); err != nil {
		t.Error(err)
	}
	if err := c.AddAssignee(ctx, 5, "me"); err != nil {
		t.Error(err)
	}
	if err := c.RemoveAssignees(ctx, 5, []string{"other"}); err != nil {
		t.Error(err)
	}
	if err := c.Comment(ctx, 5, "a note"); err != nil {
		t.Error(err)
	}
	// The payloads are the whole point of these thin wrappers: a 200 comes
	// back whatever we send, so compare each request body exactly.
	wantBodies := []map[string]any{
		{"title": "Renamed"},
		{"body": "### Goal\n\nrewritten\n"},
		{"labels": []any{"P1"}},
		{"assignees": []any{"me"}},
		{"assignees": []any{"other"}},
		{"body": "a note"},
	}
	if len(f.requests) != len(wantBodies) {
		t.Fatalf("recorded %d requests, want %d", len(f.requests), len(wantBodies))
	}
	for i, want := range wantBodies {
		got := decodeBody(t, f.requests[i].Body)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s %s payload = %v, want %v", f.requests[i].Method, f.requests[i].Path, got, want)
		}
	}
}

func TestRemoveLabelToleratesMissing(t *testing.T) {
	f := newFakeServer(t)
	f.rest["DELETE /repos/o/r/issues/5/labels/P1"] = restResponse{200, `[]`}
	f.rest["DELETE /repos/o/r/issues/5/labels/gone"] = restResponse{404, `{"message":"Label does not exist"}`}
	c := f.client(t)
	if err := c.RemoveLabel(context.Background(), 5, "P1"); err != nil {
		t.Error(err)
	}
	if err := c.RemoveLabel(context.Background(), 5, "gone"); err != nil {
		t.Errorf("404 should be tolerated: %v", err)
	}
}

func TestGraphQLMutations(t *testing.T) {
	f := newFakeServer(t)
	// The mutation APIs resolve issue numbers to node IDs first. Answer with
	// a distinct ID per number so a swapped edge direction is detectable.
	f.graphqlFunc["issue(number:"] = func(vars map[string]any) string {
		return fmt.Sprintf(`{"data":{"repository":{"issue":{"id":"NODE%v"}}}}`, vars["number"])
	}
	f.graphql["closeIssue"] = `{"data":{"closeIssue":{"clientMutationId":null}}}`
	f.graphql["reopenIssue"] = `{"data":{"reopenIssue":{"clientMutationId":null}}}`
	f.graphql["addBlockedBy"] = `{"data":{"addBlockedBy":{"clientMutationId":null}}}`
	f.graphql["removeBlockedBy"] = `{"data":{"removeBlockedBy":{"clientMutationId":null}}}`
	f.graphql["addSubIssue"] = `{"data":{"addSubIssue":{"clientMutationId":null}}}`
	f.graphql["removeSubIssue"] = `{"data":{"removeSubIssue":{"clientMutationId":null}}}`
	c := f.client(t)
	ctx := context.Background()
	if err := c.CloseIssue(ctx, 1, CloseNotPlanned); err != nil {
		t.Error(err)
	}
	if err := c.ReopenIssue(ctx, 3); err != nil {
		t.Error(err)
	}
	if err := c.AddBlockedBy(ctx, 1, 2); err != nil {
		t.Error(err)
	}
	if err := c.RemoveBlockedBy(ctx, 1, 2); err != nil {
		t.Error(err)
	}
	if err := c.AddSubIssue(ctx, 1, 2, true); err != nil {
		t.Error(err)
	}
	if err := c.RemoveSubIssue(ctx, 1, 2); err != nil {
		t.Error(err)
	}
	// #1 is blocked by #2: issueId must be #1's node, blockingIssueId #2's.
	for _, mutation := range []string{"addBlockedBy", "removeBlockedBy"} {
		got := gqlVariables(t, f, mutation)
		want := map[string]any{"id": "NODE1", "blocking": "NODE2"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s variables = %v, want %v", mutation, got, want)
		}
	}
	// #1 is the parent, #2 the child, and replaceParent must pass through.
	if got, want := gqlVariables(t, f, "addSubIssue"), (map[string]any{"parent": "NODE1", "child": "NODE2", "replace": true}); !reflect.DeepEqual(got, want) {
		t.Errorf("addSubIssue variables = %v, want %v", got, want)
	}
	if got, want := gqlVariables(t, f, "removeSubIssue"), (map[string]any{"parent": "NODE1", "child": "NODE2"}); !reflect.DeepEqual(got, want) {
		t.Errorf("removeSubIssue variables = %v, want %v", got, want)
	}
	// The close reason must ride in the variables as an enum value, not be
	// interpolated into the query text.
	var sawClose bool
	for _, r := range f.requests {
		if !strings.Contains(r.Body, "closeIssue(") {
			continue
		}
		sawClose = true
		if strings.Contains(r.Body, "stateReason: NOT_PLANNED") {
			t.Errorf("stateReason interpolated into query: %s", r.Body)
		}
	}
	if !sawClose {
		t.Error("no closeIssue mutation was sent")
	}
	if got, want := gqlVariables(t, f, "closeIssue"), (map[string]any{"id": "NODE1", "reason": "NOT_PLANNED"}); !reflect.DeepEqual(got, want) {
		t.Errorf("closeIssue variables = %v, want %v", got, want)
	}
	// reopenIssue takes the node ID alone; GitHub records the state reason
	// itself, so there is nothing else to pass or to get wrong.
	if got, want := gqlVariables(t, f, "reopenIssue"), (map[string]any{"id": "NODE3"}); !reflect.DeepEqual(got, want) {
		t.Errorf("reopenIssue variables = %v, want %v", got, want)
	}
}

func TestMutationResolvesNodeIDAndReportsMissing(t *testing.T) {
	f := newFakeServer(t)
	// Node-ID resolution finds no issue: the mutation must surface not-found
	// rather than sending a bad ID to the mutation.
	f.graphql["issue(number:"] = `{"data":{"repository":{"issue":null}}}`
	c := f.client(t)
	err := c.CloseIssue(context.Background(), 404, CloseCompleted)
	if err == nil || !strings.Contains(err.Error(), "#404 not found") {
		t.Fatalf("err = %v", err)
	}
	if err := c.ReopenIssue(context.Background(), 404); err == nil || !strings.Contains(err.Error(), "#404 not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestNodeIDResolutionIsMemoized(t *testing.T) {
	f := newFakeServer(t)
	f.graphql["issue(number:"] = `{"data":{"repository":{"issue":{"id":"NODE"}}}}`
	f.graphql["addBlockedBy"] = `{"data":{"addBlockedBy":{"clientMutationId":null}}}`
	f.graphql["removeBlockedBy"] = `{"data":{"removeBlockedBy":{"clientMutationId":null}}}`
	c := f.client(t)
	ctx := context.Background()
	// Two mutations over the same pair need four node IDs but only two
	// distinct issues; each issue must be resolved once, not per edge.
	if err := c.AddBlockedBy(ctx, 1, 2); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveBlockedBy(ctx, 1, 2); err != nil {
		t.Fatal(err)
	}
	resolutions := 0
	for _, r := range f.requests {
		if strings.Contains(r.Body, "issue(number:") {
			resolutions++
		}
	}
	if resolutions != 2 {
		t.Errorf("node-ID resolutions = %d, want 2 (one per distinct issue)", resolutions)
	}
}

func TestLabelsListAndCreate(t *testing.T) {
	f := newFakeServer(t)
	f.rest["GET /repos/o/r/labels?per_page=100&page=1"] = restResponse{200, `[{"name":"bug","color":"d73a4a","description":"x"}]`}
	f.rest["POST /repos/o/r/labels"] = restResponse{201, `{}`}
	c := f.client(t)
	labels, err := c.ListLabels(context.Background())
	if err != nil || len(labels) != 1 || labels[0].Name != "bug" {
		t.Fatalf("ListLabels() = %+v, %v", labels, err)
	}
	if err := c.CreateLabel(context.Background(), Label{Name: "P0", Color: "b60205", Description: "d"}); err != nil {
		t.Error(err)
	}
}

func TestGetIssueGraphQLNotResolve(t *testing.T) {
	f := newFakeServer(t)
	f.graphql["issue(number:"] = `{"data":{"repository":{"issue":null}},"errors":[{"type":"NOT_FOUND","message":"Could not resolve to an Issue with the number of 404."}]}`
	_, err := f.client(t).GetIssue(context.Background(), 404)
	if err == nil || !strings.Contains(err.Error(), "#404 not found in o/r") {
		t.Fatalf("err = %v", err)
	}
}

func TestGetIssueRepositoryNotFound(t *testing.T) {
	f := newFakeServer(t)
	// A missing/inaccessible repo fails to resolve at the repository path,
	// not the issue path — the message must blame the repo, not the issue.
	f.graphql["issue(number:"] = `{"data":{"repository":null},"errors":[{"type":"NOT_FOUND","path":["repository"],"message":"Could not resolve to a Repository with the name 'o/r'."}]}`
	_, err := f.client(t).GetIssue(context.Background(), 5)
	if err == nil || !strings.Contains(err.Error(), "repository o/r not found") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "issue #5") {
		t.Fatalf("repository error misreported as missing issue: %v", err)
	}
}

func TestViewerAuthError(t *testing.T) {
	f := newFakeServer(t)
	f.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"message":"Bad credentials"}`)
	})
	_, err := f.client(t).Viewer(context.Background())
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected AuthError, got %v", err)
	}
	if authErr.Unwrap() == nil {
		t.Error("AuthError should wrap the underlying error")
	}
}

func TestAuthErrorOn401(t *testing.T) {
	f := newFakeServer(t)
	f.rest["POST /repos/o/r/issues/5/comments"] = restResponse{401, `{"message":"Bad credentials"}`}
	err := f.client(t).Comment(context.Background(), 5, "x")
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected AuthError, got %v", err)
	}
	if !strings.Contains(authErr.Error(), "gh auth login") {
		t.Errorf("AuthError message: %v", authErr)
	}
}

func TestPullRequestContext(t *testing.T) {
	f := newFakeServer(t)
	f.graphql["defaultBranchRef"] = `{"data":{"repository":{
		"defaultBranchRef":{"name":"main"},
		"pullRequests":{"totalCount":2,"nodes":[{"number":12,"headRepository":{"nameWithOwner":"o/r"},"url":"https://github.com/o/r/pull/12","isDraft":true,"title":"feat: original","body":"Original body","baseRefName":"release"}]}}}}`
	got, err := f.client(t).PullRequestContext(context.Background(), "feat/x")
	if err != nil {
		t.Fatal(err)
	}
	if got.DefaultBranch != "main" {
		t.Errorf("DefaultBranch = %q, want main", got.DefaultBranch)
	}
	if got.Existing == nil || got.Existing.Number != 12 || !got.Existing.Draft {
		t.Errorf("Existing = %+v, want the open draft #12", got.Existing)
	}
	if got.Existing != nil && (got.Existing.Title != "feat: original" || got.Existing.Body != "Original body" || got.Existing.Base != "release") {
		t.Errorf("PR edit context missing: %+v", got.Existing)
	}
	if !strings.Contains(f.requests[0].Body, "body") || !strings.Contains(f.requests[0].Body, "baseRefName") {
		t.Errorf("query does not request edit context: %s", f.requests[0].Body)
	}
	if got.OpenCount != 2 || got.Existing == nil || got.Existing.HeadRepository != "o/r" {
		t.Errorf("missing PR selection evidence: %+v, PR: %+v", got, got.Existing)
	}
	for _, field := range []string{"totalCount", "headRepository", "nameWithOwner"} {
		if !strings.Contains(f.requests[0].Body, field) {
			t.Errorf("query omits %s", field)
		}
	}
	vars, _ := decodeBody(t, f.requests[0].Body)["variables"].(map[string]any)
	if vars["head"] != "feat/x" {
		t.Errorf("head filter = %v, want feat/x — an unfiltered query would return someone else's PR", vars["head"])
	}
}

// A branch with no PR yet is the normal case, and an empty nodes list must
// read as "none", not as a zero-valued PR the caller would refuse on.
func TestPullRequestContextWithoutAnExistingPR(t *testing.T) {
	f := newFakeServer(t)
	f.graphql["defaultBranchRef"] = `{"data":{"repository":{
		"defaultBranchRef":{"name":"trunk"},"pullRequests":{"nodes":[]}}}}`
	got, err := f.client(t).PullRequestContext(context.Background(), "feat/x")
	if err != nil {
		t.Fatal(err)
	}
	if got.Existing != nil {
		t.Errorf("Existing = %+v, want nil", got.Existing)
	}
	if got.DefaultBranch != "trunk" {
		t.Errorf("DefaultBranch = %q, want trunk", got.DefaultBranch)
	}
}

// An empty repository has no default branch; that is a nil ref, not a crash.
func TestPullRequestContextWithoutADefaultBranch(t *testing.T) {
	f := newFakeServer(t)
	f.graphql["defaultBranchRef"] = `{"data":{"repository":{
		"defaultBranchRef":null,"pullRequests":{"nodes":[]}}}}`
	got, err := f.client(t).PullRequestContext(context.Background(), "feat/x")
	if err != nil {
		t.Fatal(err)
	}
	if got.DefaultBranch != "" {
		t.Errorf("DefaultBranch = %q, want empty", got.DefaultBranch)
	}
}

// The server's own words are what a caller reads, so assert them: a wrapper
// that swallowed the response detail and returned a bare status would still
// be non-nil, and useless.
func TestPullRequestContextError(t *testing.T) {
	f := newFakeServer(t)
	f.graphql["defaultBranchRef"] = `{"errors":[{"message":"branch ref is invalid"}]}`
	_, err := f.client(t).PullRequestContext(context.Background(), "feat/x")
	if err == nil {
		t.Fatal("PullRequestContext() succeeded on a GraphQL error")
	}
	if !strings.Contains(err.Error(), "branch ref is invalid") {
		t.Errorf("err = %q, want it to carry the server's message", err)
	}
}

func TestCreatePullRequest(t *testing.T) {
	f := newFakeServer(t)
	f.rest["POST /repos/o/r/pulls"] = restResponse{201,
		`{"number":12,"html_url":"https://github.com/o/r/pull/12","draft":true}`}
	pr, err := f.client(t).CreatePullRequest(context.Background(), NewPullRequest{
		Title: "feat: x", Body: "Fixes #30", Head: "feat/x", Base: "main", Draft: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 12 || pr.URL != "https://github.com/o/r/pull/12" || !pr.Draft {
		t.Errorf("CreatePullRequest() = %+v", pr)
	}
	got := decodeBody(t, f.requests[len(f.requests)-1].Body)
	want := map[string]any{
		"title": "feat: x", "body": "Fixes #30",
		"head": "feat/x", "base": "main", "draft": true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("request payload = %v, want %v", got, want)
	}
}

// A 422 that slips past the proactive PullRequestContext check reaches the
// user verbatim, so the message must survive the wrapper — not just the
// fact that something failed.
func TestCreatePullRequestError(t *testing.T) {
	f := newFakeServer(t)
	f.rest["POST /repos/o/r/pulls"] = restResponse{422, `{"message":"A pull request already exists"}`}
	_, err := f.client(t).CreatePullRequest(context.Background(), NewPullRequest{Head: "feat/x", Base: "main"})
	if err == nil {
		t.Fatal("CreatePullRequest() succeeded on a 422")
	}
	if !strings.Contains(err.Error(), "A pull request already exists") {
		t.Errorf("err = %q, want it to carry the server's message", err)
	}
}

func TestUpdatePullRequest(t *testing.T) {
	f := newFakeServer(t)
	f.rest["PATCH /repos/o/r/pulls/12"] = restResponse{200, `{"number":12}`}
	if err := f.client(t).UpdatePullRequest(context.Background(), 12, "feat: revised", "Revised body\n\nFixes #30"); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 1 {
		t.Fatalf("requests = %v, want one PATCH", f.requests)
	}
	req := f.requests[0]
	if req.Method != "PATCH" || req.Path != "/repos/o/r/pulls/12" {
		t.Errorf("request = %+v", req)
	}
	want := map[string]any{"title": "feat: revised", "body": "Revised body\n\nFixes #30"}
	if got := decodeBody(t, req.Body); !reflect.DeepEqual(got, want) {
		t.Errorf("payload = %v, want %v", got, want)
	}
}

func TestUpdatePullRequestErrors(t *testing.T) {
	for _, status := range []int{401, 422} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			f := newFakeServer(t)
			f.rest["PATCH /repos/o/r/pulls/12"] = restResponse{status, `{"message":"update refused"}`}
			err := f.client(t).UpdatePullRequest(context.Background(), 12, "new title", "body")
			if err == nil || !strings.Contains(err.Error(), "update refused") {
				t.Fatalf("error = %v", err)
			}
			if status == 401 {
				var auth *AuthError
				if !errors.As(err, &auth) {
					t.Errorf("error = %T, want AuthError", err)
				}
			}
		})
	}
}

func TestPullRequestContextHeadRepository(t *testing.T) {
	for _, tc := range []struct{ name, repoJSON, want string }{
		{"same repository", `{"nameWithOwner":"o/r"}`, "o/r"},
		{"fork", `{"nameWithOwner":"fork/r"}`, "fork/r"},
		{"deleted repository", `null`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeServer(t)
			f.graphql["defaultBranchRef"] = fmt.Sprintf(`{"data":{"repository":{"defaultBranchRef":{"name":"main"},"pullRequests":{"totalCount":1,"nodes":[{"number":12,"headRepository":%s}]}}}}`, tc.repoJSON)
			got, err := f.client(t).PullRequestContext(context.Background(), "feat/x")
			if err != nil {
				t.Fatal(err)
			}
			if got.OpenCount != 1 || got.Existing == nil || got.Existing.HeadRepository != tc.want {
				t.Errorf("context = %+v, PR = %+v; want one PR with head repository %q", got, got.Existing, tc.want)
			}
		})
	}
}

// A long-lived process shares one client across goroutines: a list command, a
// claim and a dependency write can overlap. The race detector is the assertion
// for the shared caches and client pointers; the values check that sharing did
// not corrupt them.
func TestClientSafeForConcurrentUse(t *testing.T) {
	f := newFakeServer(t)
	f.graphqlFunc["issue(number:"] = func(vars map[string]any) string {
		return fmt.Sprintf(`{"data":{"repository":{"issue":{"id":"NODE%v"}}}}`, vars["number"])
	}
	f.graphql["viewer"] = `{"data":{"viewer":{"login":"lumberbarons"}}}`
	f.graphql["issues(states:"] = fmt.Sprintf(
		`{"data":{"repository":{"issues":{"pageInfo":{"hasNextPage":false},"nodes":[%s]}}}}`, issueJSON(1, ""))
	f.graphql["closeIssue"] = `{"data":{"closeIssue":{"clientMutationId":null}}}`
	f.graphql["addBlockedBy"] = `{"data":{"addBlockedBy":{"clientMutationId":null}}}`
	c := f.client(t)

	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 8; i++ {
		wg.Add(4)
		go func(n int) {
			defer wg.Done()
			if _, err := c.ListIssues(ctx, []IssueState{StateOpen}); err != nil {
				errs <- err
			}
		}(i)
		go func(n int) {
			defer wg.Done()
			if _, err := c.Viewer(ctx); err != nil {
				errs <- err
			}
		}(i)
		go func(n int) {
			defer wg.Done()
			if err := c.CloseIssue(ctx, n+1, CloseCompleted); err != nil {
				errs <- err
			}
		}(i)
		go func(n int) {
			defer wg.Done()
			if err := c.AddBlockedBy(ctx, n+1, n+2); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent call failed: %v", err)
	}

	// The viewer is cached, so eight concurrent callers share one lookup
	// rather than racing to overwrite the cache.
	viewerCalls := 0
	for _, r := range f.recorded() {
		if strings.Contains(r.Body, "viewer") {
			viewerCalls++
		}
	}
	if viewerCalls != 1 {
		t.Errorf("viewer lookups = %d, want 1", viewerCalls)
	}
}

// go-gh resolves the token once, at construction. A client that outlives its
// token has to rebuild from freshly resolved credentials rather than reuse the
// dead one, or every later command fails until the process restarts.
func TestAuthFailureRefreshesTokenAndRetries(t *testing.T) {
	f := newFakeServer(t)
	var mu sync.Mutex
	attempts := 0
	var retryAuth string
	f.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		n := attempts
		if n > 1 {
			retryAuth = r.Header.Get("Authorization")
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"message":"Bad credentials"}`)
			return
		}
		fmt.Fprint(w, `{"data":{"viewer":{"login":"lumberbarons"}}}`)
	})
	c := f.client(t)
	refreshes := 0
	c.tokenSource = func() (string, error) {
		refreshes++
		return "fresh-token", nil
	}

	login, err := c.Viewer(context.Background())
	if err != nil || login != "lumberbarons" {
		t.Fatalf("Viewer() = %q, %v; want a recovered login", login, err)
	}
	if refreshes != 1 {
		t.Errorf("token source consulted %d times, want 1", refreshes)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}
	if retryAuth != "token fresh-token" {
		t.Errorf("retry Authorization = %q, want the refreshed token", retryAuth)
	}
}

// Recovery is one attempt, not a loop: a token that is still refused is a
// credential problem for the caller to fix, and it must arrive as the typed
// AuthError the CLI maps to exit 4.
func TestAuthFailureWithoutRecoveryReturnsAuthError(t *testing.T) {
	f := newFakeServer(t)
	var mu sync.Mutex
	attempts := 0
	f.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"message":"Bad credentials"}`)
	})
	c := f.client(t)
	c.tokenSource = func() (string, error) { return "still-bad", nil }

	_, err := c.Viewer(context.Background())
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("err = %v, want a typed AuthError", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2 (one refresh, then surface)", attempts)
	}
	// Not wedged: the client still answers, with the same typed failure.
	if _, err := c.Viewer(context.Background()); !errors.As(err, &authErr) {
		t.Errorf("second call err = %v, want a typed AuthError", err)
	}
}

// GitHub answers a secondary rate limit with 429 or 403 plus Retry-After. The
// wait it names is the one to take: guessing shorter burns the next attempt on
// the same refusal.
func TestRateLimitedRequestHonoursRetryAfter(t *testing.T) {
	f := newFakeServer(t)
	var mu sync.Mutex
	attempts := 0
	f.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		n := attempts
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"message":"You have exceeded a secondary rate limit"}`)
			return
		}
		fmt.Fprint(w, `{"data":{"viewer":{"login":"lumberbarons"}}}`)
	})
	c := f.client(t)
	var waits []time.Duration
	c.sleep = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}

	if _, err := c.Viewer(context.Background()); err != nil {
		t.Fatalf("Viewer() = %v, want the retry to succeed", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}
	if want := []time.Duration{7 * time.Second}; !reflect.DeepEqual(waits, want) {
		t.Errorf("waits = %v, want %v", waits, want)
	}
}

// Without a Retry-After hint the wait is exponential from the client's base,
// and the attempt count is bounded: a permanent refusal must not spin.
func TestRateLimitedRequestBacksOffExponentially(t *testing.T) {
	f := newFakeServer(t)
	var mu sync.Mutex
	attempts := 0
	f.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"message":"You have exceeded a secondary rate limit"}`)
	})
	c := f.client(t)
	c.retryBase = 10 * time.Millisecond
	var waits []time.Duration
	c.sleep = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}

	if _, err := c.Viewer(context.Background()); err == nil {
		t.Fatal("Viewer() succeeded against a permanent rate limit")
	}
	if attempts != 4 {
		t.Errorf("attempts = %d, want 4 (one try plus three retries)", attempts)
	}
	want := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond}
	if !reflect.DeepEqual(waits, want) {
		t.Errorf("waits = %v, want %v", waits, want)
	}
}

// A 403 that carries no rate-limit signal is a permission failure. Retrying it
// would only delay the error a reviewer needs to see.
func TestForbiddenWithoutRateLimitSignalIsNotRetried(t *testing.T) {
	f := newFakeServer(t)
	var mu sync.Mutex
	attempts := 0
	f.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"Resource not accessible by integration"}`)
	})
	c := f.client(t)
	c.sleep = func(ctx context.Context, d time.Duration) error {
		t.Errorf("slept %v before retrying a permission failure", d)
		return nil
	}

	_, err := c.Viewer(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not accessible") {
		t.Fatalf("err = %v, want the server's permission message", err)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
}

// The list query carries the budget, so a long-lived caller can slow down
// before it is refused instead of discovering the limit by hitting it.
func TestListIssuesCapturesRateLimit(t *testing.T) {
	f := newFakeServer(t)
	f.graphql["issues(states:"] = fmt.Sprintf(
		`{"data":{"rateLimit":{"cost":2,"remaining":4983,"resetAt":"2026-11-15T12:00:00Z"},
			"repository":{"issues":{"pageInfo":{"hasNextPage":false},"nodes":[%s]}}}}`, issueJSON(1, ""))
	c := f.client(t)
	if _, ok := c.RateLimit(); ok {
		t.Fatal("RateLimit() reported a budget before any list query ran")
	}

	if _, err := c.ListIssues(context.Background(), []IssueState{StateOpen}); err != nil {
		t.Fatal(err)
	}
	got, ok := c.RateLimit()
	if !ok {
		t.Fatal("RateLimit() reported nothing after a list query")
	}
	want := RateLimit{Cost: 2, Remaining: 4983, ResetAt: time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)}
	if got.Cost != want.Cost || got.Remaining != want.Remaining || !got.ResetAt.Equal(want.ResetAt) {
		t.Errorf("RateLimit() = %+v, want %+v", got, want)
	}
	if !strings.Contains(f.recorded()[0].Body, "rateLimit { cost remaining resetAt }") {
		t.Errorf("list query does not select the rate limit: %s", f.recorded()[0].Body)
	}
}

// The viewer cannot change without a different token, and every command asks
// for it, so it is resolved once.
func TestViewerIsCached(t *testing.T) {
	f := newFakeServer(t)
	f.graphql["viewer"] = `{"data":{"viewer":{"login":"lumberbarons"}}}`
	c := f.client(t)
	for i := 0; i < 3; i++ {
		login, err := c.Viewer(context.Background())
		if err != nil || login != "lumberbarons" {
			t.Fatalf("Viewer() = %q, %v", login, err)
		}
	}
	if n := len(f.recorded()); n != 1 {
		t.Errorf("viewer requests = %d, want 1", n)
	}
}

// A refresh that cannot produce a token must surface the original auth
// failure, not the refresh's own error: the caller's fix is still
// `gh auth login`, and the typed error is what maps to exit 4.
func TestAuthRefreshFailureSurfacesTheOriginalError(t *testing.T) {
	f := newFakeServer(t)
	var mu sync.Mutex
	attempts := 0
	f.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"message":"Bad credentials"}`)
	})
	c := f.client(t)
	c.tokenSource = func() (string, error) { return "", errors.New("no token in keyring") }

	_, err := c.Viewer(context.Background())
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("err = %v, want a typed AuthError", err)
	}
	if !strings.Contains(err.Error(), "Bad credentials") {
		t.Errorf("err = %v, want the server's auth failure", err)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1 (a failed refresh is not a retry)", attempts)
	}
}

// A caller whose context ends stops the retry loop instead of waiting out the
// backoff: a cancelled command must not keep a process alive.
func TestRateLimitRetryStopsWhenContextEnds(t *testing.T) {
	f := newFakeServer(t)
	var mu sync.Mutex
	attempts := 0
	f.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"message":"You have exceeded a secondary rate limit"}`)
	})
	c := f.client(t)
	c.sleep = func(ctx context.Context, d time.Duration) error { return context.Canceled }

	_, err := c.Viewer(context.Background())
	if err == nil || !strings.Contains(err.Error(), "secondary rate limit") {
		t.Fatalf("err = %v, want the rate-limit failure", err)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
}

// Retry-After arrives as delta-seconds or as an HTTP date, and either form may
// ask for longer than the client is willing to wait.
func TestParseRetryAfter(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  time.Duration
		ok    bool
	}{
		{"seconds", "7", 7 * time.Second, true},
		{"zero", "0", 0, true},
		{"negative", "-5", 0, true},
		{"capped", "3600", maxRetryWait, true},
		{"http date", time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat), 30 * time.Second, true},
		{"past date", "Wed, 21 Oct 2015 07:28:00 GMT", 0, true},
		{"empty", "", 0, false},
		{"garbage", "soon", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseRetryAfter(tc.value)
			if ok != tc.ok {
				t.Fatalf("parseRetryAfter(%q) ok = %v, want %v", tc.value, ok, tc.ok)
			}
			if !ok {
				return
			}
			// A date-derived wait is measured against the wall clock, so
			// allow a second of slack rather than freezing time.
			if diff := got - tc.want; diff > time.Second || diff < -time.Second {
				t.Errorf("parseRetryAfter(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

func TestSleepContextStopsOnCancellation(t *testing.T) {
	if err := sleepContext(context.Background(), 0); err != nil {
		t.Errorf("sleepContext(0) = %v, want nil", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepContext(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("sleepContext(cancelled) = %v, want context.Canceled", err)
	}
}

// GitHub sometimes refuses a secondary rate limit with 403 rather than 429,
// and marks it with Retry-After or an exhausted remaining budget. That is a
// refusal to retry, not the permission failure a bare 403 is.
func TestForbiddenRateLimitIsRetried(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header map[string]string
	}{
		{"retry-after", map[string]string{"Retry-After": "2"}},
		{"exhausted budget", map[string]string{"X-RateLimit-Remaining": "0"}},
		{"message", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeServer(t)
			var mu sync.Mutex
			attempts := 0
			f.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				attempts++
				n := attempts
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if n == 1 {
					for k, v := range tc.header {
						w.Header().Set(k, v)
					}
					w.WriteHeader(http.StatusForbidden)
					fmt.Fprint(w, `{"message":"You have exceeded a secondary rate limit"}`)
					return
				}
				fmt.Fprint(w, `{"data":{"viewer":{"login":"lumberbarons"}}}`)
			})
			c := f.client(t)
			c.sleep = func(ctx context.Context, d time.Duration) error { return nil }

			login, err := c.Viewer(context.Background())
			if err != nil || login != "lumberbarons" {
				t.Fatalf("Viewer() = %q, %v; want the retry to succeed", login, err)
			}
			if attempts != 2 {
				t.Errorf("attempts = %d, want 2", attempts)
			}
		})
	}
}

// A refresh can land on different credentials, so a login cached before one is
// not trusted after it: attributing claims to the previous account would be a
// silent, permanent error in a long-lived process.
func TestViewerCacheIsDroppedAfterCredentialRefresh(t *testing.T) {
	f := newFakeServer(t)
	var mu sync.Mutex
	viewerCalls := 0
	f.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		viewerCalls++
		n := viewerCalls
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"message":"Bad credentials"}`)
			return
		}
		fmt.Fprint(w, `{"data":{"viewer":{"login":"new-account"}}}`)
	})
	c := f.client(t)
	c.tokenSource = func() (string, error) { return "fresh-token", nil }

	for i := 0; i < 3; i++ {
		login, err := c.Viewer(context.Background())
		if err != nil || login != "new-account" {
			t.Fatalf("Viewer() = %q, %v", login, err)
		}
	}
	// The first call costs a refusal plus the recovered lookup; the refresh
	// invalidates that cached login, so the second re-resolves it; the third
	// is served from the cache.
	if viewerCalls != 3 {
		t.Errorf("viewer requests = %d, want 3", viewerCalls)
	}
}
