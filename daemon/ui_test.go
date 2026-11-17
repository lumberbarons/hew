package daemon_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lumberbarons/hew/daemon"
	"github.com/lumberbarons/hew/tracker"
)

// serveUI serves a real tracker with ui as the web UI.
func serveUI(t *testing.T, ui *fstest.MapFS) string {
	t.Helper()
	root, target := checkout(t, false)
	tr, err := tracker.Open(context.Background(), tracker.Config{Worktree: root, Actor: "daemon"})
	if err != nil {
		t.Fatal(err)
	}
	srv := daemon.New(tr, nil, target)
	if ui != nil {
		srv.WithUI(*ui)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts.URL
}

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestBuiltUIIsServedUncached(t *testing.T) {
	url := serveUI(t, &fstest.MapFS{
		"index.html":    {Data: []byte("<!doctype html><title>hew</title>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
	})
	for path, want := range map[string]int{"/": 200, "/assets/app.js": 200, "/assets/missing.js": 404} {
		resp, _ := get(t, url+path)
		if resp.StatusCode != want {
			t.Errorf("GET %s = %d, want %d", path, resp.StatusCode, want)
		}
		// The assets are embedded and change with the binary, so a browser
		// must not reuse a stale one. (net/http strips the header from its own
		// error responses, so only successes are checked.)
		if cc := resp.Header.Get("Cache-Control"); want == 200 && cc != "no-cache" {
			t.Errorf("GET %s Cache-Control = %q, want no-cache", path, cc)
		}
	}
}

// TestMissingUIExplainsItself covers a checkout where `npm run build` has not
// run: the page a person opens must say what to do, not answer 404.
func TestMissingUIExplainsItself(t *testing.T) {
	url := serveUI(t, &fstest.MapFS{".gitkeep": {}})
	resp, body := get(t, url+"/")
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, "make web") {
		t.Errorf("GET / = %d %q, want 503 explaining how to build the UI", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want HTML", ct)
	}
	// The API is unaffected.
	if resp, _ = get(t, url+"/api/config"); resp.StatusCode != 200 {
		t.Errorf("GET /api/config = %d, want 200 without a UI", resp.StatusCode)
	}
}

func TestEmbeddedUIDirectoryAlwaysExists(t *testing.T) {
	// With nothing injected the daemon uses its embedded build, which may or
	// may not have been built in this checkout; either way it must answer.
	url := serveUI(t, nil)
	resp, _ := get(t, url+"/")
	if resp.StatusCode != 200 && resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("GET / = %d, want the UI or the not-built page", resp.StatusCode)
	}
}

// bareCheckout is a git checkout with no tracker at all.
func bareCheckout(t *testing.T, remote string) string {
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
	run := func(args ...string) {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "--initial-branch=main")
	if remote != "" {
		run("remote", "add", "origin", remote)
	}
	return root
}

func TestEnsureLocalTrackerStartsOneOnlyWhereNoneExists(t *testing.T) {
	ctx := context.Background()

	empty := bareCheckout(t, "")
	if err := daemon.EnsureLocalTracker(ctx, empty); err != nil {
		t.Fatalf("empty checkout: %v", err)
	}
	if target, err := tracker.Detect(empty); err != nil || target.Backend != tracker.BackendLocal {
		t.Fatalf("after ensure: %+v, %v; want a local tracker", target, err)
	}
	// Running again is a no-op, not an error.
	if err := daemon.EnsureLocalTracker(ctx, empty); err != nil {
		t.Errorf("second ensure: %v", err)
	}

	// A checkout whose issues are on GitHub must not gain a local tracker,
	// which would redirect every later hew command there to an empty one.
	hosted := bareCheckout(t, "git@github.com:o/r.git")
	err := daemon.EnsureLocalTracker(ctx, hosted)
	if err == nil || !strings.Contains(err.Error(), "o/r") {
		t.Fatalf("GitHub checkout: err = %v, want a refusal naming o/r", err)
	}
	if target, _ := tracker.Detect(hosted); target.Backend == tracker.BackendLocal {
		t.Error("a refused ensure still created a local tracker")
	}

	if err := daemon.EnsureLocalTracker(ctx, ""); err == nil {
		t.Error("empty directory: want an error")
	}
}
