// Package migrate_test exercises the public migration workflows the way an
// external module does: only public imports, a real local tracker created
// through tracker.Init, and an isolated HTTP transport standing in for GitHub.
// Nothing here runs the hew binary, reads a gh credential or reaches the
// network.
package migrate_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lumberbarons/hew/migrate"
	"github.com/lumberbarons/hew/tracker"
)

// ---------------------------------------------------------------------------
// An isolated GitHub: just enough REST and GraphQL for the workflows' writes.
// ---------------------------------------------------------------------------

type fakeIssue struct {
	number    int
	title     string
	body      string
	state     string
	reason    string
	labels    []string
	assignees []string
	parent    int
	blockedBy []int
	comments  []string
}

type githubFake struct {
	mu sync.Mutex
	// issues is every issue the migration created, by number.
	issues map[int]*fakeIssue
	labels []string
	// requests records every request the workflows made, so a dry run can
	// assert it made none.
	requests []string
	// creates counts REST issue creations, including failed attempts; failed
	// injects a 500 at that 1-based create.
	creates  int
	failed   int
	onCreate func()
	next     int
}

func newGitHubFake() *githubFake {
	return &githubFake{issues: map[int]*fakeIssue{}, next: 101}
}

func (f *githubFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	f.mu.Unlock()
	if r.URL.Path == "/graphql" {
		f.graphql(w, body)
		return
	}
	f.rest(w, r, body)
}

func (f *githubFake) rest(w http.ResponseWriter, r *http.Request, body []byte) {
	path := r.URL.Path
	issueSuffix := func(suffix string) (int, bool) {
		if !strings.HasSuffix(path, suffix) {
			return 0, false
		}
		trimmed := strings.TrimSuffix(path, suffix)
		n, err := strconv.Atoi(trimmed[strings.LastIndex(trimmed, "/")+1:])
		return n, err == nil
	}
	switch {
	case r.Method == "GET" && strings.HasSuffix(path, "/labels"):
		f.mu.Lock()
		out := make([]map[string]any, 0, len(f.labels))
		for _, name := range f.labels {
			out = append(out, map[string]any{"name": name, "color": "ededed", "description": ""})
		}
		f.mu.Unlock()
		writeJSON(w, out)
	case r.Method == "POST" && strings.HasSuffix(path, "/labels"):
		var label struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(body, &label)
		f.mu.Lock()
		f.labels = append(f.labels, label.Name)
		f.mu.Unlock()
		writeJSON(w, map[string]any{"name": label.Name})
	case r.Method == "POST" && strings.HasSuffix(path, "/assignees"):
		n, ok := issueSuffix("/assignees")
		if !ok {
			http.Error(w, "bad assignee path", http.StatusBadRequest)
			return
		}
		var payload struct {
			Assignees []string `json:"assignees"`
		}
		_ = json.Unmarshal(body, &payload)
		f.mu.Lock()
		if issue := f.issues[n]; issue != nil {
			issue.assignees = append(issue.assignees, payload.Assignees...)
		}
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, []map[string]any{})
	case r.Method == "POST" && strings.HasSuffix(path, "/comments"):
		n, ok := issueSuffix("/comments")
		if !ok {
			http.Error(w, "bad comment path", http.StatusBadRequest)
			return
		}
		var payload struct {
			Body string `json:"body"`
		}
		_ = json.Unmarshal(body, &payload)
		f.mu.Lock()
		if issue := f.issues[n]; issue != nil {
			issue.comments = append(issue.comments, payload.Body)
		}
		f.mu.Unlock()
		writeJSON(w, map[string]any{"id": n})
	case r.Method == "POST" && strings.HasSuffix(path, "/issues"):
		f.create(w, body)
	case r.Method == "PATCH" && strings.Contains(path, "/issues/"):
		n, ok := issueSuffix("")
		if !ok {
			http.Error(w, "bad issue path", http.StatusBadRequest)
			return
		}
		var payload struct {
			Title *string `json:"title"`
			Body  *string `json:"body"`
		}
		_ = json.Unmarshal(body, &payload)
		f.mu.Lock()
		if issue := f.issues[n]; issue != nil {
			if payload.Title != nil {
				issue.title = *payload.Title
			}
			if payload.Body != nil {
				issue.body = *payload.Body
			}
		}
		f.mu.Unlock()
		writeJSON(w, map[string]any{})
	default:
		http.Error(w, "unexpected REST request: "+r.Method+" "+path, http.StatusBadRequest)
	}
}

func (f *githubFake) create(w http.ResponseWriter, body []byte) {
	var payload struct {
		Title  string   `json:"title"`
		Body   string   `json:"body"`
		Labels []string `json:"labels"`
	}
	_ = json.Unmarshal(body, &payload)

	f.mu.Lock()
	f.creates++
	fail := f.creates == f.failed
	if fail {
		f.mu.Unlock()
		http.Error(w, `{"message":"injected failure"}`, http.StatusInternalServerError)
		return
	}
	number := f.next
	f.next++
	issue := &fakeIssue{number: number, title: payload.Title, body: payload.Body, state: "OPEN", labels: payload.Labels}
	f.issues[number] = issue
	hook := f.onCreate
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	writeJSON(w, map[string]any{"node_id": nodeID(number), "number": number, "title": payload.Title})
}

func (f *githubFake) graphql(w http.ResponseWriter, body []byte) {
	var payload struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	_ = json.Unmarshal(body, &payload)
	query := payload.Query
	number := func(v any) int {
		value, _ := v.(float64)
		return int(value)
	}
	switch {
	case strings.Contains(query, "viewer"):
		writeJSON(w, map[string]any{"data": map[string]any{"viewer": map[string]any{"login": "octocat"}}})
	case strings.Contains(query, "issue(number: $number) { id }"):
		n := number(payload.Variables["number"])
		writeJSON(w, map[string]any{"data": map[string]any{"repository": map[string]any{"issue": map[string]any{"id": nodeID(n)}}}})
	case strings.Contains(query, "closeIssue("):
		n := fromNodeID(payload.Variables["id"])
		reason, _ := payload.Variables["reason"].(string)
		f.mu.Lock()
		if issue := f.issues[n]; issue != nil {
			issue.state = "CLOSED"
			issue.reason = reason
		}
		f.mu.Unlock()
		writeJSON(w, map[string]any{"data": map[string]any{}})
	case strings.Contains(query, "addSubIssue("):
		parent := fromNodeID(payload.Variables["parent"])
		child := fromNodeID(payload.Variables["child"])
		f.mu.Lock()
		if issue := f.issues[child]; issue != nil {
			issue.parent = parent
		}
		f.mu.Unlock()
		writeJSON(w, map[string]any{"data": map[string]any{}})
	case strings.Contains(query, "addBlockedBy("):
		issueNumber := fromNodeID(payload.Variables["id"])
		blocking := fromNodeID(payload.Variables["blocking"])
		f.mu.Lock()
		if issue := f.issues[issueNumber]; issue != nil {
			issue.blockedBy = append(issue.blockedBy, blocking)
		}
		f.mu.Unlock()
		writeJSON(w, map[string]any{"data": map[string]any{}})
	case strings.Contains(query, "comments(last:"):
		n := number(payload.Variables["number"])
		f.mu.Lock()
		issue := f.issues[n]
		var issueValue any
		if issue != nil {
			issueValue = issueNode(issue)
		}
		f.mu.Unlock()
		writeJSON(w, map[string]any{"data": map[string]any{"repository": map[string]any{"issue": issueValue}}})
	default:
		http.Error(w, "unexpected GraphQL query: "+query, http.StatusBadRequest)
	}
}

func nodeID(number int) string { return "NODE" + strconv.Itoa(number) }
func fromNodeID(v any) int {
	s, _ := v.(string)
	n, _ := strconv.Atoi(strings.TrimPrefix(s, "NODE"))
	return n
}

func issueNode(issue *fakeIssue) map[string]any {
	labels := make([]map[string]any, 0, len(issue.labels))
	for _, name := range issue.labels {
		labels = append(labels, map[string]any{"name": name})
	}
	assignees := make([]map[string]any, 0, len(issue.assignees))
	for _, login := range issue.assignees {
		assignees = append(assignees, map[string]any{"login": login})
	}
	blockedBy := make([]map[string]any, 0, len(issue.blockedBy))
	for _, n := range issue.blockedBy {
		blockedBy = append(blockedBy, map[string]any{"number": n, "state": "OPEN"})
	}
	var parent any
	if issue.parent != 0 {
		parent = map[string]any{"number": issue.parent, "state": "OPEN", "title": ""}
	}
	var reason any
	if issue.reason != "" {
		reason = issue.reason
	}
	comments := make([]map[string]any, 0, len(issue.comments))
	for _, body := range issue.comments {
		comments = append(comments, map[string]any{"author": map[string]any{"login": "octocat"}, "createdAt": "2026-01-01T00:00:00Z", "body": body})
	}
	return map[string]any{
		"id": nodeID(issue.number), "number": issue.number, "title": issue.title,
		"state": issue.state, "stateReason": reason, "createdAt": "2026-01-01T00:00:00Z",
		"body":             issue.body,
		"labels":           map[string]any{"nodes": labels},
		"assignees":        map[string]any{"nodes": assignees},
		"parent":           parent,
		"subIssues":        map[string]any{"totalCount": 0, "nodes": []any{}},
		"subIssuesSummary": map[string]any{"total": 0, "completed": 0},
		"blockedBy":        map[string]any{"totalCount": len(blockedBy), "nodes": blockedBy},
		"comments":         map[string]any{"totalCount": len(comments), "nodes": comments},
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

// rewriteTransport sends every API request to the fake server while keeping
// go-gh's host and path, so the workflow's own client is what runs.
type rewriteTransport struct{ target *url.URL }

func (t rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = t.target.Scheme
	clone.URL.Host = t.target.Host
	clone.Host = t.target.Host
	return http.DefaultTransport.RoundTrip(clone)
}

// openFake serves fake and returns the transport and a cleanup.
func openFake(t *testing.T, fake *githubFake) http.RoundTripper {
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
	return rewriteTransport{target: u}
}

// ---------------------------------------------------------------------------
// A real checkout and local tracker.
// ---------------------------------------------------------------------------

func externalCheckout(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "GIT_") {
			t.Setenv(k, os.Getenv(k)) // registers the restore
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
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// localWork creates a real local tracker and returns its main checkout plus
// the tracker handles the workflows read and the probes write through.
func localWork(t *testing.T) (root string, tr *tracker.Tracker) {
	t.Helper()
	root = externalCheckout(t)
	ctx := context.Background()
	if _, err := tracker.Init(ctx, tracker.InitConfig{Worktree: root, Backend: tracker.BackendLocal}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	tr, err := tracker.Open(ctx, tracker.Config{Worktree: root, Actor: "consumer"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return root, tr
}

// migrateConfig is one public launch of the local workflow against a fake.
func migrateConfig(t *testing.T, worktree string, transport http.RoundTripper, dryRun bool) migrate.Config {
	t.Helper()
	dir := t.TempDir()
	return migrate.Config{
		Worktree:       worktree,
		Backend:        tracker.BackendGitHub,
		Repo:           "o/r",
		Transport:      transport,
		CheckpointPath: filepath.Join(dir, "migration-state.json"),
		DryRun:         dryRun,
		Throttle:       0,
	}
}

// ---------------------------------------------------------------------------
// The local workflow.
// ---------------------------------------------------------------------------

// TestExternalConsumerMigratesALocalTracker is done-when's first item for the
// local workflow: a consumer moves a real local tracker to an isolated GitHub
// transport through public APIs, with no hew binary involved.
func TestExternalConsumerMigratesALocalTracker(t *testing.T) {
	root, tr := localWork(t)
	ctx := context.Background()

	epic, err := tr.CreateEpic(ctx, tracker.CreateEpicRequest{Title: "Big"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := tr.Create(ctx, tracker.CreateRequest{Title: "First", Type: "task", Priority: "P1", Parent: epic.Issue.Number})
	if err != nil {
		t.Fatal(err)
	}
	second, err := tr.Create(ctx, tracker.CreateRequest{Title: "Second", Type: "bug", Priority: "P2"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Start(ctx, second.Issue.Number, tracker.StartOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Block(ctx, second.Issue.Number, []int{first.Issue.Number}, tracker.BlockOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Close(ctx, first.Issue.Number, tracker.CloseOptions{Completed: true, Reason: "shipped"}); err != nil {
		t.Fatal(err)
	}
	// A writer opened before the migration must be refused after it.
	writer, err := tracker.Open(ctx, tracker.Config{Worktree: root, Actor: "consumer"})
	if err != nil {
		t.Fatal(err)
	}

	fake := newGitHubFake()
	transport := openFake(t, fake)

	// Dry run: a plan, no writes, either on GitHub or in the tracker.
	dry, err := migrate.Open(ctx, migrateConfig(t, root, transport, true))
	if err != nil {
		t.Fatalf("Open for the dry run: %v", err)
	}
	plan, err := dry.Local(ctx, migrate.LocalRequest{Dir: filepath.Join(root, ".hew")})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if len(plan.Plan) != 3 || plan.Plan[0].Migrated != 0 {
		t.Fatalf("plan = %+v, want three outstanding issues", plan.Plan)
	}
	if len(fake.requests) != 0 {
		t.Errorf("dry run reached GitHub: %v", fake.requests)
	}
	if got, err := tracker.Detect(root); err != nil || got.Backend != tracker.BackendLocal {
		t.Fatalf("Detect after the dry run = %+v, %v; want still local", got, err)
	}

	// The real run: every issue, then the seal.
	m, err := migrate.Open(ctx, migrateConfig(t, root, transport, false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	res, err := m.Local(ctx, migrate.LocalRequest{Dir: filepath.Join(root, ".hew")})
	if err != nil {
		t.Fatalf("Local: %v", err)
	}
	if res.Created != 3 || res.Selected != 3 || res.Failed != 0 || res.Backend != tracker.BackendGitHub {
		t.Fatalf("result = %+v, want three created and the backend flipped", res)
	}

	gEpic, gFirst, gSecond := 101, 102, 103
	fake.mu.Lock()
	if got := fake.issues[gEpic]; got == nil || got.title != "Epic: Big" || !contains(got.labels, "epic") {
		t.Errorf("epic = %+v", got)
	}
	if got := fake.issues[gFirst]; got == nil || got.state != "CLOSED" || got.reason != "COMPLETED" {
		t.Errorf("first = %+v, want closed as completed", got)
	} else if got.parent != gEpic || !strings.Contains(strings.Join(got.comments, "\n"), "shipped") {
		t.Errorf("first = %+v, want its parent and close comment", got)
	}
	if got := fake.issues[gSecond]; got == nil || !containsInt(got.blockedBy, gFirst) {
		t.Errorf("second = %+v, want blocked by first", got)
	} else if !contains(got.assignees, "octocat") || !contains(got.labels, "in-progress") {
		t.Errorf("second = %+v, want the migrating user's claim", got)
	}

	fake.mu.Unlock()

	// The tracker is an archive: it reports GitHub, and its pre-opened writer
	// gets the public migrated error rather than writing to the archive.
	if got, err := tracker.Detect(root); err != nil || got.Backend != tracker.BackendGitHub {
		t.Errorf("Detect after the migration = %+v, %v; want github", got, err)
	}
	_, err = writer.Create(ctx, tracker.CreateRequest{Title: "Late", Type: "task"})
	var migrated *tracker.MigratedError
	if !errors.As(err, &migrated) {
		t.Errorf("a pre-opened local writer's Create err = %T %v, want *tracker.MigratedError", err, err)
	}
	if _, err := tracker.Open(ctx, tracker.Config{Worktree: root, Backend: tracker.BackendLocal}); err == nil {
		t.Error("the migrated tracker reopened as local")
	}
}

// TestExternalConsumerPlansALocalMigrationWithoutATarget is the checkout with
// no GitHub repository: a dry run plans from issues.jsonl alone, and refuses a
// checkpoint it cannot verify.
func TestExternalConsumerPlansALocalMigrationWithoutATarget(t *testing.T) {
	root, tr := localWork(t)
	ctx := context.Background()
	if _, err := tr.Create(ctx, tracker.CreateRequest{Title: "One", Type: "task"}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".hew")

	m, err := migrate.Open(ctx, migrate.Config{
		Worktree: root, Backend: tracker.BackendGitHub, DryRun: true,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	plan, err := m.Local(ctx, migrate.LocalRequest{Dir: dir})
	if err != nil {
		t.Fatalf("Local: %v", err)
	}
	if len(plan.Plan) != 1 || plan.Plan[0].Title != "One" {
		t.Errorf("plan = %+v, want the one issue planned", plan.Plan)
	}

	path := filepath.Join(t.TempDir(), "migration-state.json")
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err = migrate.Open(ctx, migrate.Config{
		Worktree: root, Backend: tracker.BackendGitHub, DryRun: true, CheckpointPath: path,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, err = m.Local(ctx, migrate.LocalRequest{Dir: dir})
	var checkpoint *migrate.CheckpointError
	if !errors.As(err, &checkpoint) {
		t.Fatalf("err = %T %v, want a CheckpointError", err, err)
	}
}

// TestLocalRefusesALocalTarget keeps the one-way rule honest: Local writes to
// GitHub, so a detected local backend is a mistake to name rather than a store
// to copy issues into.
func TestLocalRefusesALocalTarget(t *testing.T) {
	root, _ := localWork(t)
	m, err := migrate.Open(context.Background(), migrate.Config{Worktree: root, Backend: tracker.BackendLocal, Actor: "consumer", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Local(context.Background(), migrate.LocalRequest{Dir: filepath.Join(root, ".hew")})
	var invalid *tracker.InvalidInputError
	if !errors.As(err, &invalid) || invalid.Field != "backend" {
		t.Fatalf("err = %T %v, want an invalid backend", err, err)
	}
}

// TestExternalConsumerFreezesTheTrackerDuringMigration is done-when's third
// item: the run holds the tracker, so a concurrent writer waits and fails
// rather than slipping a change into the archive.
func TestExternalConsumerFreezesTheTrackerDuringMigration(t *testing.T) {
	root, tr := localWork(t)
	ctx := context.Background()
	if _, err := tr.Create(ctx, tracker.CreateRequest{Title: "One", Type: "task"}); err != nil {
		t.Fatal(err)
	}

	fake := newGitHubFake()
	transport := openFake(t, fake)
	var probeErr error
	fake.onCreate = func() {
		wctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		_, probeErr = tr.Create(wctx, tracker.CreateRequest{Title: "Slipped in", Type: "task"})
	}

	m, err := migrate.Open(ctx, migrateConfig(t, root, transport, false))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Local(ctx, migrate.LocalRequest{Dir: filepath.Join(root, ".hew")}); err != nil {
		t.Fatalf("Local: %v", err)
	}
	if !errors.Is(probeErr, context.DeadlineExceeded) {
		t.Errorf("mid-migration write err = %v, want it stuck behind the migration's lock", probeErr)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.issues) != 1 {
		t.Errorf("issues on GitHub = %d, want only the one that existed", len(fake.issues))
	}
}

// TestExternalConsumerFailedLocalRunLeavesTrackerUsableAndRefusesAStaleCheckpoint
// is done-when's second item, first half: an injected mid-run failure leaves
// the tracker usable and local, and a checkpoint bound to an issues.jsonl that
// has since changed is refused rather than resumed. The resume itself is
// TestExternalConsumerResumesAnInterruptedLocalRun.
func TestExternalConsumerFailedLocalRunLeavesTrackerUsableAndRefusesAStaleCheckpoint(t *testing.T) {
	root, tr := localWork(t)
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		if _, err := tr.Create(ctx, tracker.CreateRequest{Title: fmt.Sprintf("Issue %d", i), Type: "task"}); err != nil {
			t.Fatal(err)
		}
	}

	fake := newGitHubFake()
	fake.failed = 2
	transport := openFake(t, fake)
	cfg := migrateConfig(t, root, transport, false)
	m, err := migrate.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Local(ctx, migrate.LocalRequest{Dir: filepath.Join(root, ".hew")}); err == nil {
		t.Fatal("injected failure not reported")
	}
	if got, err := tracker.Detect(root); err != nil || got.Backend != tracker.BackendLocal {
		t.Fatalf("Detect after the failure = %+v, %v; want still local and usable", got, err)
	}
	// The tracker is writable again: the failure released the freeze.
	if _, err := tr.Create(ctx, tracker.CreateRequest{Title: "Still local", Type: "task"}); err != nil {
		t.Fatalf("tracker after a failed run: %v", err)
	}
	// The write above changed issues.jsonl, so the checkpoint is now bound to
	// different contents and must be refused rather than resumed.
	m, err = migrate.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Local(ctx, migrate.LocalRequest{Dir: filepath.Join(root, ".hew")}); err == nil || !strings.Contains(err.Error(), "different issues.jsonl") {
		t.Fatalf("resume after a local change = %v, want the checkpoint refused", err)
	}
}

// TestExternalConsumerResumesAnInterruptedLocalRun is the resume half without
// an intervening local write: the same checkpoint finishes the migration and
// creates nothing twice.
func TestExternalConsumerResumesAnInterruptedLocalRun(t *testing.T) {
	root, tr := localWork(t)
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		if _, err := tr.Create(ctx, tracker.CreateRequest{Title: fmt.Sprintf("Issue %d", i), Type: "task"}); err != nil {
			t.Fatal(err)
		}
	}

	fake := newGitHubFake()
	fake.failed = 2
	transport := openFake(t, fake)
	cfg := migrateConfig(t, root, transport, false)
	m, err := migrate.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Local(ctx, migrate.LocalRequest{Dir: filepath.Join(root, ".hew")}); err == nil {
		t.Fatal("injected failure not reported")
	}

	m, err = migrate.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	res, err := m.Local(ctx, migrate.LocalRequest{Dir: filepath.Join(root, ".hew")})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if res.Created != 2 || res.Failed != 0 {
		t.Errorf("resume = %+v, want only the two missing creates", res)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.issues) != 3 {
		t.Errorf("issues on GitHub = %d, want three (no duplicates)", len(fake.issues))
	}
}

// ---------------------------------------------------------------------------
// The beads workflow.
// ---------------------------------------------------------------------------

// beadsFixture is a snapshot with the shapes the import has to preserve: an
// epic, a child, a blocker chain and a closed bead with a close reason.
const beadsFixture = `{"_type":"issue","id":"b-epic","title":"Voltgo","status":"open","priority":2,"issue_type":"epic","created_at":"2026-01-01T00:00:00Z"}
{"_type":"issue","id":"b-1","title":"Child","status":"open","priority":1,"issue_type":"feature","created_at":"2026-01-02T00:00:00Z","dependencies":[{"issue_id":"b-1","depends_on_id":"b-epic","type":"parent-child"}]}
{"_type":"issue","id":"b-2","title":"Blocked","status":"closed","priority":3,"issue_type":"bug","created_at":"2026-01-03T00:00:00Z","closed_at":"2026-01-04T00:00:00Z","close_reason":"fixed in 1.2","dependencies":[{"issue_id":"b-2","depends_on_id":"b-1","type":"blocks"}]}
`

// TestExternalConsumerMigratesABeadsSnapshot is done-when's first item for the
// beads workflow: a consumer imports a snapshot through the public API, from
// bytes and from a reader, into an isolated GitHub transport.
func TestExternalConsumerMigratesABeadsSnapshot(t *testing.T) {
	ctx := context.Background()
	worktree := t.TempDir()
	fake := newGitHubFake()
	transport := openFake(t, fake)
	checkpoint := filepath.Join(t.TempDir(), "github-migration.json")

	// A dry run from a reader: the plan comes back and nothing is written.
	dry, err := migrate.Open(ctx, migrate.Config{
		Worktree: worktree, Backend: tracker.BackendGitHub, Repo: "o/r",
		Transport: transport, CheckpointPath: checkpoint, DryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := dry.Beads(ctx, migrate.BeadsRequest{Reader: strings.NewReader(beadsFixture), IncludeClosed: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Plan) != 3 || plan.Plan[0].Title != "Epic: Voltgo" || plan.Plan[0].Type != "epic" {
		t.Fatalf("plan = %+v, want the epic first", plan.Plan)
	}
	if len(fake.requests) != 0 {
		t.Errorf("dry run reached GitHub: %v", fake.requests)
	}

	// The real run, from bytes.
	m, err := migrate.Open(ctx, migrate.Config{
		Worktree: worktree, Backend: tracker.BackendGitHub, Repo: "o/r",
		Transport: transport, CheckpointPath: checkpoint,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := m.Beads(ctx, migrate.BeadsRequest{Snapshot: []byte(beadsFixture), IncludeClosed: true})
	if err != nil {
		t.Fatalf("Beads: %v", err)
	}
	if res.Created != 3 || res.Wired != 2 || res.Closed != 1 || res.Skipped != 0 {
		t.Errorf("result = %+v, want 3 created, 2 wired, 1 closed", res)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if got := fake.issues[101]; got == nil || got.title != "Epic: Voltgo" || !contains(got.labels, "epic") {
		t.Errorf("epic = %+v", got)
	}
	if got := fake.issues[102]; got == nil || got.parent != 101 || !strings.Contains(got.body, "Migrated from beads `b-1`") {
		t.Errorf("child = %+v, want its parent and provenance footer", got)
	}
	if got := fake.issues[103]; got == nil || got.state != "CLOSED" || got.reason != "COMPLETED" {
		t.Errorf("closed bead = %+v, want closed as completed", got)
	} else if !containsInt(got.blockedBy, 102) || !contains(got.comments, "fixed in 1.2") {
		t.Errorf("closed bead = %+v, want its blocker and close reason", got)
	}
}

// TestExternalConsumerImportsBeadsIntoALocalTracker covers the other target:
// a consumer detects the repository's local backend and imports the snapshot
// into it, no GitHub involved.
func TestExternalConsumerImportsBeadsIntoALocalTracker(t *testing.T) {
	root, _ := localWork(t)
	ctx := context.Background()
	m, err := migrate.Open(ctx, migrate.Config{Worktree: root, Actor: "consumer", CheckpointPath: filepath.Join(t.TempDir(), "state.json")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if m.Backend() != tracker.BackendLocal {
		t.Fatalf("backend = %q, want the detected local tracker", m.Backend())
	}
	res, err := m.Beads(ctx, migrate.BeadsRequest{Snapshot: []byte(beadsFixture)})
	if err != nil {
		t.Fatalf("Beads: %v", err)
	}
	if res.Created != 2 || res.Skipped != 1 {
		t.Errorf("result = %+v, want the two open beads imported", res)
	}

	tr, err := tracker.Open(ctx, tracker.Config{Worktree: root, Actor: "consumer"})
	if err != nil {
		t.Fatal(err)
	}
	epic, err := tr.Get(ctx, res.Mapping["b-epic"])
	if err != nil {
		t.Fatal(err)
	}
	if epic.Title != "Epic: Voltgo" || !epic.HasLabel("epic") {
		t.Errorf("epic = %+v, want the prefix and marker", epic)
	}
	child, err := tr.Get(ctx, res.Mapping["b-1"])
	if err != nil {
		t.Fatal(err)
	}
	if child.Parent == nil || *child.Parent != epic.Number {
		t.Errorf("child parent = %v, want #%d", child.Parent, epic.Number)
	}
}

// TestExternalConsumerBeadsDryRunWithoutATarget is the targetless plan: with no
// repository to write to, a dry run still reads the source, and refuses a
// checkpoint it cannot verify.
func TestExternalConsumerBeadsDryRunWithoutATarget(t *testing.T) {
	ctx := context.Background()
	worktree := t.TempDir()
	cfg := migrate.Config{Worktree: worktree, DryRun: true}
	m, err := migrate.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	plan, err := m.Beads(ctx, migrate.BeadsRequest{Snapshot: []byte(beadsFixture)})
	if err != nil {
		t.Fatalf("Beads: %v", err)
	}
	if len(plan.Plan) != 2 || plan.Skipped != 1 {
		t.Errorf("plan = %+v, want two open beads planned and the closed one skipped", plan)
	}

	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.CheckpointPath = path
	m, err = migrate.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Beads(ctx, migrate.BeadsRequest{Snapshot: []byte(beadsFixture)})
	var checkpoint *migrate.CheckpointError
	if !errors.As(err, &checkpoint) {
		t.Fatalf("err = %T %v, want a CheckpointError", err, err)
	}
}

// TestOpenRefusesAnUnresolveableTargetWithoutADryRun: the targetless plan is a
// dry-run privilege, not a silent no-op for a real migration.
func TestOpenRefusesAnUnresolveableTargetWithoutADryRun(t *testing.T) {
	_, err := migrate.Open(context.Background(), migrate.Config{Worktree: t.TempDir(), Backend: tracker.BackendGitHub})
	var invalid *tracker.InvalidInputError
	if !errors.As(err, &invalid) || invalid.Field != "repo" {
		t.Fatalf("err = %T %v, want an invalid repo", err, err)
	}
}

// TestValidatorChecksTheCheckpointPath keeps the required input honest: a real
// workflow has somewhere to record progress.
func TestValidatorChecksTheCheckpointPath(t *testing.T) {
	ctx := context.Background()
	m, err := migrate.Open(ctx, migrate.Config{
		Worktree: t.TempDir(), Backend: tracker.BackendGitHub, Repo: "o/r",
		Transport: openFake(t, newGitHubFake()),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Beads(ctx, migrate.BeadsRequest{Snapshot: []byte(beadsFixture)})
	var invalid *tracker.InvalidInputError
	if !errors.As(err, &invalid) || invalid.Field != "checkpoint" {
		t.Fatalf("err = %T %v, want an invalid checkpoint", err, err)
	}
}

// ---------------------------------------------------------------------------
// Helpers.
// ---------------------------------------------------------------------------

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsInt(values []int, want int) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
