package migrate

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/local"
	"github.com/lumberbarons/hew/tracker"
)

func TestOpenRejectsInputsItCannotWorkFrom(t *testing.T) {
	ctx := context.Background()
	cases := map[string]struct {
		cfg   Config
		field string
	}{
		"no worktree":     {cfg: Config{}, field: "worktree"},
		"unknown backend": {cfg: Config{Worktree: t.TempDir(), Backend: "svn"}, field: "backend"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Open(ctx, tc.cfg)
			var invalid *tracker.InvalidInputError
			if !errors.As(err, &invalid) || invalid.Field != tc.field {
				t.Fatalf("err = %T %v, want invalid %s", err, err, tc.field)
			}
		})
	}
}

// An explicit repository pins GitHub without detection, and the store the
// workflow writes through is built over the caller's transport.
func TestOpenPinsGitHubWithAnExplicitRepo(t *testing.T) {
	transport := http.DefaultTransport
	var got gh.Repo
	original := openGitHubStore
	openGitHubStore = func(repo gh.Repo, tr http.RoundTripper) (gh.IssueStore, error) {
		got = repo
		if tr != transport {
			t.Errorf("transport not passed through")
		}
		return nil, nil
	}
	t.Cleanup(func() { openGitHubStore = original })

	m, err := Open(context.Background(), Config{
		Worktree: t.TempDir(), Backend: tracker.BackendGitHub, Repo: "o/r", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "o/r" || m.Backend() != tracker.BackendGitHub || m.Repo() != "o/r" {
		t.Errorf("repo = %q, migrator = %+v", got, m)
	}
	if _, ok := m.target.(storeTarget); !ok {
		t.Errorf("target = %T, want the store adapter", m.target)
	}
}

func TestOpenRefusesAMalformedRepo(t *testing.T) {
	_, err := Open(context.Background(), Config{
		Worktree: t.TempDir(), Backend: tracker.BackendGitHub, Repo: "not-a-repo",
	})
	var invalid *tracker.InvalidInputError
	if !errors.As(err, &invalid) || invalid.Field != "repo" {
		t.Fatalf("err = %T %v, want an invalid repo", err, err)
	}
}

// A local tracker is a valid target (beads imports into it), and its
// checkpoint binds to the tracker's stable id rather than the directory name.
func TestOpenPinsLocalAndBindsToTheTrackerID(t *testing.T) {
	worktree := t.TempDir()
	gitInit(t, worktree)
	dir := filepath.Join(worktree, local.DirName)
	if err := local.Init(dir); err != nil {
		t.Fatal(err)
	}
	m, err := Open(context.Background(), Config{Worktree: worktree, Backend: tracker.BackendLocal, Actor: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if m.Backend() != tracker.BackendLocal || m.Repo() != "" {
		t.Errorf("migrator = %+v, want a local target", m)
	}
	cfg, err := local.ReadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.repoKey != "local/"+cfg.ID {
		t.Errorf("repoKey = %q, want the tracker's id", m.repoKey)
	}
}

func TestOpenRefusesAPinnedLocalBackendWithoutATracker(t *testing.T) {
	_, err := Open(context.Background(), Config{Worktree: t.TempDir(), Backend: tracker.BackendLocal})
	var invalid *tracker.InvalidInputError
	if !errors.As(err, &invalid) || invalid.Field != "backend" {
		t.Fatalf("err = %T %v, want an invalid backend", err, err)
	}
}

// A dry run is the only mode that may plan without a target; the workflow
// still refuses a real one.
func TestOpenTargetlessOnlyForADryRun(t *testing.T) {
	worktree := t.TempDir()
	if _, err := Open(context.Background(), Config{Worktree: worktree}); err == nil {
		t.Fatal("a real run opened without a repository")
	}
	m, err := Open(context.Background(), Config{Worktree: worktree, DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if m.target != nil {
		t.Errorf("target = %v, want none", m.target)
	}
}

// labelerFake records the labels a bootstrap creates.
type labelerFake struct {
	existing []Label
	created  []Label
}

func (f *labelerFake) ListLabels(context.Context) ([]Label, error) { return f.existing, nil }
func (f *labelerFake) CreateLabel(_ context.Context, label Label) error {
	f.created = append(f.created, label)
	return nil
}

func TestEnsureLabelsIsCaseInsensitiveAndIdempotent(t *testing.T) {
	fake := &labelerFake{existing: []Label{{Name: "area:cli"}, {Name: "P0"}}}
	if err := ensureLabels(context.Background(), fake, []Label{{Name: "Area:CLI"}, {Name: "New"}, {Name: "new"}}); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, l := range fake.created {
		names = append(names, l.Name)
	}
	if slices.Contains(names, "Area:CLI") || slices.Contains(names, "new") {
		t.Errorf("created %v, want the existing names skipped", names)
	}
	if !slices.Contains(names, "New") {
		t.Errorf("created %v, want the distinct new label", names)
	}
}

// gitInit makes dir a plain git checkout, which is what lets the backend
// detection find the tracker beside it.
func gitInit(t *testing.T, dir string) {
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
	if out, err := exec.Command("git", "-C", dir, "init", "--initial-branch=main").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
}
