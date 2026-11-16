package tracker

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
	"slices"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/internal/conventions"
	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/local"
)

// labelStore is a GitHub store for the init workflow: it keeps the labels the
// CLI bootstrap used to read and write directly, and can inject a failure on
// either half.
type labelStore struct {
	gh.IssueStore
	login     string
	labels    []gh.Label
	listErr   error
	createErr error
	// failCreateAt fails only that 1-based CreateLabel call; zero fails every
	// call when createErr is set.
	failCreateAt int
	creates      int
	created      []gh.Label
}

func (s *labelStore) Viewer(context.Context) (string, error) { return s.login, nil }

func (s *labelStore) ListLabels(context.Context) ([]gh.Label, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return append([]gh.Label(nil), s.labels...), nil
}

func (s *labelStore) CreateLabel(_ context.Context, label gh.Label) error {
	s.creates++
	if s.createErr != nil && (s.failCreateAt == 0 || s.creates == s.failCreateAt) {
		return s.createErr
	}
	s.labels = append(s.labels, label)
	s.created = append(s.created, label)
	return nil
}

func TestInitLocalCreatesTheTrackerAtTheMainCheckout(t *testing.T) {
	main, worktrees := repoWithWorktrees(t, "wt")
	res, err := Init(context.Background(), InitConfig{Worktree: worktrees[0], Backend: BackendLocal})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(main, local.DirName)
	if res.Backend != BackendLocal || res.Path != want {
		t.Errorf("Init = {%q, %q}, want the local tracker at %s", res.Backend, res.Path, want)
	}
	if res.CreatedLabels == nil || len(res.CreatedLabels) != 0 {
		t.Errorf("CreatedLabels = %v; the local backend creates no labels", res.CreatedLabels)
	}
	if _, err := os.Stat(filepath.Join(want, "config.json")); err != nil {
		t.Errorf("tracker config was not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(worktrees[0], local.DirName)); !os.IsNotExist(err) {
		t.Errorf("tracker was created inside the linked worktree: %v", err)
	}
	if len(res.Notices) != 1 || !strings.Contains(res.Notices[0], "git clean -fdx") {
		t.Errorf("Notices = %v; want the one-time untracked-tracker warning", res.Notices)
	}
}

func TestInitLocalIsIdempotentAndKeepsIssues(t *testing.T) {
	main, _ := repoWithWorktrees(t)
	ctx := context.Background()
	if _, err := Init(ctx, InitConfig{Worktree: main, Backend: BackendLocal}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(main, local.DirName)
	before, err := local.ReadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := local.Open(dir, "me")
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateIssue(ctx, "Keep me", "", []string{"P2", "task"})
	if err != nil {
		t.Fatal(err)
	}

	res, err := Init(ctx, InitConfig{Worktree: main, Backend: BackendLocal})
	if err != nil {
		t.Fatalf("second Init: %v", err)
	}
	if len(res.Notices) != 0 {
		t.Errorf("a rerun over an intact tracker noticed %v", res.Notices)
	}
	after, err := local.ReadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if before.ID == "" || after.ID != before.ID {
		t.Errorf("tracker id = %q after a rerun, want %q", after.ID, before.ID)
	}
	if _, err := store.GetIssue(ctx, created.Number); err != nil {
		t.Errorf("a rerun lost the issue: %v", err)
	}
}

func TestInitLocalRecreatesALostIssuesFileAndSaysSo(t *testing.T) {
	main, _ := repoWithWorktrees(t)
	ctx := context.Background()
	if _, err := Init(ctx, InitConfig{Worktree: main, Backend: BackendLocal}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(main, local.DirName)
	before, err := local.ReadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, local.IssuesFileName)); err != nil {
		t.Fatal(err)
	}

	res, err := Init(ctx, InitConfig{Worktree: main, Backend: BackendLocal})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IssuesRecreated {
		t.Error("a recreated issues file is not reported")
	}
	if len(res.Notices) != 1 || !strings.Contains(res.Notices[0], "backup") || !strings.Contains(res.Notices[0], "new tracker id") {
		t.Errorf("Notices = %v; want the recreated-issues warning", res.Notices)
	}
	if _, err := os.Stat(filepath.Join(dir, local.IssuesFileName)); err != nil {
		t.Errorf("issues.jsonl was not recreated: %v", err)
	}
	after, err := local.ReadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if after.ID == "" || after.ID == before.ID {
		t.Errorf("tracker id = %q, want a new one (was %q) so old checkpoints stop matching", after.ID, before.ID)
	}
}

func TestInitLocalRefusesATrackerOnAnotherBackend(t *testing.T) {
	main, _ := repoWithWorktrees(t)
	ctx := context.Background()
	if _, err := Init(ctx, InitConfig{Worktree: main, Backend: BackendLocal}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(main, local.DirName)
	if err := local.WriteConfig(dir, local.Config{Backend: local.BackendGitHub}); err != nil {
		t.Fatal(err)
	}
	// Damage the archive so a refusal that still ran Init would show: Init
	// recreates a missing .gitignore.
	ignore := filepath.Join(dir, ".gitignore")
	if err := os.Remove(ignore); err != nil {
		t.Fatal(err)
	}

	_, err := Init(ctx, InitConfig{Worktree: main, Backend: BackendLocal})
	var elsewhere *InitializedElsewhereError
	if !errors.As(err, &elsewhere) || elsewhere.Path != dir || elsewhere.Backend != BackendGitHub {
		t.Fatalf("err = %v, want an InitializedElsewhereError for %s on github", err, dir)
	}
	if _, statErr := os.Stat(ignore); !os.IsNotExist(statErr) {
		t.Errorf("refused init still wrote into the archived tracker: %v", statErr)
	}
}

func TestInitLocalReportsADamagedConfigWithoutOverwritingIt(t *testing.T) {
	main, _ := repoWithWorktrees(t)
	ctx := context.Background()
	if _, err := Init(ctx, InitConfig{Worktree: main, Backend: BackendLocal}); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(main, local.DirName, "config.json")
	if err := os.WriteFile(config, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Init(ctx, InitConfig{Worktree: main, Backend: BackendLocal})
	if err == nil || !strings.Contains(err.Error(), config) || strings.Contains(err.Error(), "unexpected end of JSON") {
		t.Errorf("err = %v, want the path and no decoder text", err)
	}
	raw, readErr := os.ReadFile(config)
	if readErr != nil || len(raw) != 0 {
		t.Errorf("damaged config = %q, %v; want it left alone", raw, readErr)
	}
}

// A tracker that lost its config is the damaged state the write commands
// refuse rather than guess around; detection has to report that refusal rather
// than quietly initializing GitHub over the issues.
func TestInitPropagatesTheDamagedTrackerRefusal(t *testing.T) {
	main, _ := repoWithWorktrees(t)
	dir := filepath.Join(main, local.DirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, local.IssuesFileName), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Init(context.Background(), InitConfig{Worktree: main})
	if err == nil || !strings.Contains(err.Error(), "config.json") || !strings.Contains(err.Error(), "hew init --local") {
		t.Errorf("err = %v, want the orphaned-tracker refusal naming the config and the recovery", err)
	}
}

// git failing for a reason other than "not a checkout" is not the caller's
// input mistake, so it keeps its own message rather than becoming an
// InvalidInputError.
func TestInitLocalSurfacesAGitFailureItCannotAttributeToTheWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("PATH", "")
	_, err := Init(context.Background(), InitConfig{Worktree: t.TempDir(), Backend: BackendLocal})
	if err == nil {
		t.Fatal("Init succeeded with no git on PATH")
	}
	var invalid *InvalidInputError
	if errors.As(err, &invalid) {
		t.Errorf("err = %v; a missing git is not invalid input", err)
	}
}

func TestInitLocalRejectsAWorktreeThatIsNotACheckout(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	_, err := Init(context.Background(), InitConfig{Worktree: dir, Backend: BackendLocal})
	var invalid *InvalidInputError
	if !errors.As(err, &invalid) || invalid.Field != "worktree" {
		t.Fatalf("err = %v, want an InvalidInputError for worktree", err)
	}
	for _, want := range []string{"git init", "not a git repository"} {
		if !strings.Contains(invalid.Reason, want) {
			t.Errorf("reason = %q, want it to mention %q", invalid.Reason, want)
		}
	}
}

func TestInitRejectsAnUnusableRequest(t *testing.T) {
	for name, cfg := range map[string]InitConfig{
		"no worktree":     {Backend: BackendLocal},
		"unknown backend": {Worktree: t.TempDir(), Backend: Backend("linear")},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Init(context.Background(), cfg)
			var invalid *InvalidInputError
			if !errors.As(err, &invalid) {
				t.Errorf("err = %v, want an InvalidInputError", err)
			}
		})
	}
}

// With no backend pinned, init follows the same selection every command does:
// a repo that already tracks locally reports its tracker, and a repo whose
// only target is GitHub bootstraps labels.
func TestInitDetectsTheBackend(t *testing.T) {
	ctx := context.Background()
	main, _ := repoWithWorktrees(t)
	if _, err := Init(ctx, InitConfig{Worktree: main, Backend: BackendLocal}); err != nil {
		t.Fatal(err)
	}
	res, err := Init(ctx, InitConfig{Worktree: main})
	if err != nil {
		t.Fatal(err)
	}
	if res.Backend != BackendLocal {
		t.Errorf("a repo with a local tracker detected %q, want local", res.Backend)
	}

	github, _ := repoWithWorktrees(t)
	gitRun(t, github, "remote", "add", "origin", "git@github.com:owner/name.git")
	store := &labelStore{login: "octocat"}
	replaceGitHub(t, func(gh.Repo, http.RoundTripper) (gh.IssueStore, error) { return store, nil })
	res, err = Init(ctx, InitConfig{Worktree: github})
	if err != nil {
		t.Fatal(err)
	}
	if res.Backend != BackendGitHub || res.Path != "" {
		t.Errorf("Init = {%q, %q}, want GitHub with no local path", res.Backend, res.Path)
	}
	if len(res.CreatedLabels) != len(conventions.Labels) {
		t.Errorf("created %d labels, want %d", len(res.CreatedLabels), len(conventions.Labels))
	}
}

func TestInitGitHubCreatesOnlyMissingConventionLabels(t *testing.T) {
	store := &labelStore{login: "octocat", labels: []gh.Label{{Name: conventions.Labels[0].Name}}}
	replaceGitHub(t, func(gh.Repo, http.RoundTripper) (gh.IssueStore, error) { return store, nil })
	ctx := context.Background()
	res, err := Init(ctx, InitConfig{Worktree: t.TempDir(), Backend: BackendGitHub, Repo: "owner/name"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.CreatedLabels) != len(conventions.Labels)-1 {
		t.Errorf("CreatedLabels = %v; want every label but the one already present", res.CreatedLabels)
	}
	for _, created := range store.created {
		if created.Color == "" || created.Description == "" {
			t.Errorf("label %q created without the convention style", created.Name)
		}
	}

	again, err := Init(ctx, InitConfig{Worktree: t.TempDir(), Backend: BackendGitHub, Repo: "owner/name"})
	if err != nil {
		t.Fatal(err)
	}
	if again.CreatedLabels == nil || len(again.CreatedLabels) != 0 {
		t.Errorf("a second init created %v; want a non-nil empty list", again.CreatedLabels)
	}
}

func TestInitGitHubReportsALabelFailureWithWhatLanded(t *testing.T) {
	boom := &gh.AuthError{Err: errors.New("bad credentials")}
	store := &labelStore{login: "octocat", createErr: boom}
	replaceGitHub(t, func(gh.Repo, http.RoundTripper) (gh.IssueStore, error) { return store, nil })
	res, err := Init(context.Background(), InitConfig{Worktree: t.TempDir(), Backend: BackendGitHub, Repo: "owner/name"})
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("err = %v, want an AuthError", err)
	}
	if !strings.Contains(err.Error(), conventions.Labels[0].Name) {
		t.Errorf("err = %v, want the label whose creation failed", err)
	}
	if len(res.CreatedLabels) != 0 {
		t.Errorf("CreatedLabels = %v; the first create failed, so nothing landed", res.CreatedLabels)
	}
}

// An unreadable label list is an auth failure when the credential is the
// problem, and it is reported before anything is created.
func TestInitGitHubReportsAnUnreadableLabelList(t *testing.T) {
	store := &labelStore{login: "octocat", listErr: &gh.AuthError{Err: errors.New("bad credentials")}}
	replaceGitHub(t, func(gh.Repo, http.RoundTripper) (gh.IssueStore, error) { return store, nil })
	res, err := Init(context.Background(), InitConfig{Worktree: t.TempDir(), Backend: BackendGitHub, Repo: "owner/name"})
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("err = %v, want an AuthError", err)
	}
	if len(res.CreatedLabels) != 0 || len(store.created) != 0 {
		t.Errorf("CreatedLabels = %v after a failed read; want nothing written", res.CreatedLabels)
	}
}

// A bootstrap that stops partway reports the labels that did land, because a
// rerun resumes from there.
func TestInitGitHubReportsTheLabelsCreatedBeforeAFailure(t *testing.T) {
	store := &labelStore{
		login:        "octocat",
		labels:       []gh.Label{{Name: conventions.Labels[0].Name}},
		createErr:    errors.New("rate limited"),
		failCreateAt: 2,
	}
	replaceGitHub(t, func(gh.Repo, http.RoundTripper) (gh.IssueStore, error) { return store, nil })
	res, err := Init(context.Background(), InitConfig{Worktree: t.TempDir(), Backend: BackendGitHub, Repo: "owner/name"})
	if err == nil {
		t.Fatal("Init reported success after a failed label create")
	}
	if want := []string{conventions.Labels[1].Name}; !slices.Equal(res.CreatedLabels, want) {
		t.Errorf("CreatedLabels = %v, want %v", res.CreatedLabels, want)
	}
	if !strings.Contains(err.Error(), conventions.Labels[2].Name) {
		t.Errorf("err = %v, want the label that failed", err)
	}
}

func TestInitLocalTrackerIsAlreadyInitialized(t *testing.T) {
	main, _ := repoWithWorktrees(t)
	ctx := context.Background()
	if _, err := Init(ctx, InitConfig{Worktree: main, Backend: BackendLocal}); err != nil {
		t.Fatal(err)
	}
	store, err := local.Open(filepath.Join(main, local.DirName), "me")
	if err != nil {
		t.Fatal(err)
	}
	res, err := WrapAs(store, BackendLocal, "").Init(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Backend != BackendLocal || len(res.CreatedLabels) != 0 || len(res.Notices) != 0 {
		t.Errorf("Init of a local tracker = %+v; want no changes", res)
	}
	labels, err := store.ListLabels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(labels) != 0 {
		t.Errorf("a local tracker gained labels: %v", labels)
	}
}

// rewriteTransport sends every request to the test server, whatever host
// go-gh aimed it at, so the real client runs offline.
type rewriteTransport struct{ target *url.URL }

func (t rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = t.target.Scheme
	clone.URL.Host = t.target.Host
	return http.DefaultTransport.RoundTrip(clone)
}

// offlineGitHub isolates the test from the developer's gh configuration and
// hands the client a token, so no keychain or gh process is reachable.
func offlineGitHub(t *testing.T) {
	t.Helper()
	t.Setenv("GH_TOKEN", "test-token")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	t.Setenv("GH_HOST", "")
	t.Setenv("GH_DEBUG", "")
}

// TestInitGitHubThroughATestTransport is the done-when's GitHub half: the
// bootstrap runs over real HTTP through Config.Transport, against a server
// that records what was asked for.
func TestInitGitHubThroughATestTransport(t *testing.T) {
	offlineGitHub(t)
	var created []map[string]any
	var listed []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/graphql":
			fmt.Fprint(w, `{"data":{"viewer":{"login":"octocat"}}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/name/labels":
			listed = append(listed, r.URL.Path)
			// One convention label already exists, so the run must leave it alone.
			fmt.Fprintf(w, `[{"name":%q,"color":"ededed","description":"existing"}]`, conventions.Labels[0].Name)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/owner/name/labels":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("label body is not JSON: %v", err)
			}
			created = append(created, body)
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	res, err := Init(context.Background(), InitConfig{
		Worktree:  t.TempDir(),
		Backend:   BackendGitHub,
		Repo:      "owner/name",
		Transport: rewriteTransport{target: target},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Backend != BackendGitHub || len(listed) != 1 {
		t.Fatalf("Init = %+v after %d listing calls", res, len(listed))
	}
	want := make([]string, 0, len(conventions.Labels)-1)
	for _, label := range conventions.Labels[1:] {
		want = append(want, label.Name)
	}
	if strings.Join(res.CreatedLabels, ",") != strings.Join(want, ",") {
		t.Errorf("CreatedLabels = %v, want %v", res.CreatedLabels, want)
	}
	if len(created) != len(want) {
		t.Fatalf("created %d labels over HTTP, want %d", len(created), len(want))
	}
	for n, body := range created {
		label := conventions.Labels[n+1]
		if body["name"] != label.Name || body["color"] != label.Color || body["description"] != label.Description {
			t.Errorf("created label %v, want the convention's %+v", body, label)
		}
	}
}

func TestInitGitHubAuthFailureIsATypedError(t *testing.T) {
	offlineGitHub(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"message":"Bad credentials"}`)
	}))
	t.Cleanup(srv.Close)
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Init(context.Background(), InitConfig{
		Worktree:  t.TempDir(),
		Backend:   BackendGitHub,
		Repo:      "owner/name",
		Transport: rewriteTransport{target: target},
	})
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("err = %v, want an AuthError", err)
	}
}

func TestInitGitHubRejectsAMalformedRepo(t *testing.T) {
	_, err := Init(context.Background(), InitConfig{Worktree: t.TempDir(), Backend: BackendGitHub, Repo: "nope"})
	var invalid *InvalidInputError
	if !errors.As(err, &invalid) || invalid.Field != "repo" {
		t.Errorf("err = %v, want an InvalidInputError for repo", err)
	}
}

// The library never prints: init's warnings are data, and an unused Transport
// still runs the whole bootstrap here.
func TestInitWritesToNeitherStdoutNorStderr(t *testing.T) {
	offlineGitHub(t)
	store := &labelStore{login: "octocat"}
	replaceGitHub(t, func(gh.Repo, http.RoundTripper) (gh.IssueStore, error) { return store, nil })
	main, _ := repoWithWorktrees(t)
	ctx := context.Background()

	out, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = out, out
	_, githubErr := Init(ctx, InitConfig{Worktree: t.TempDir(), Backend: BackendGitHub, Repo: "owner/name"})
	_, localErr := Init(ctx, InitConfig{Worktree: main, Backend: BackendLocal})
	os.Stdout, os.Stderr = oldOut, oldErr

	if githubErr != nil || localErr != nil {
		t.Fatalf("Init: github %v, local %v", githubErr, localErr)
	}
	if _, err := out.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	printed, err := io.ReadAll(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(printed) != 0 {
		t.Errorf("Init wrote to the process output: %q", printed)
	}
}
