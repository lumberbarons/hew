package daemon_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// update rewrites the committed fixtures: go test ./daemon -run Fixtures -update
var update = flag.Bool("update", false, "rewrite web/src/fixtures from the daemon's real responses")

// The fixtures are the daemon's actual JSON for a deterministic tracker. The
// web UI's tests render from them, so the two sides share one contract: if a
// response changes shape, this test fails until the fixtures are regenerated,
// and the regenerated files then fail the UI's type checks and tests where
// the UI no longer matches.

// normalize makes a response independent of the clock and the temp directory.
func normalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			switch k {
			case "createdAt":
				t[k] = "2026-01-01T00:00:00Z"
			case "dir", "mainCheckout":
				t[k] = "/path/to/checkout"
			case "id":
				if _, isString := x.(string); isString {
					t[k] = "fixture-tracker"
				}
			default:
				t[k] = normalize(x)
			}
		}
	case []any:
		for i := range t {
			t[i] = normalize(t[i])
		}
	}
	return v
}

func TestFixturesMatchTheDaemonsResponses(t *testing.T) {
	e := newEnv(t, false)

	// A small, fixed tracker that exercises every shape the UI renders: a
	// claimed bug, a blocked enhancement, an epic with children, an untriaged
	// report, an idea, an invisible character in a title, and a dependency
	// cycle. The idea is ready-looking (unclaimed, unblocked, triaged, P0) so
	// the fixtures catch any regression that offers it as implementation work.
	body := map[string]any{"goal": "Make it so", "approach": "Carefully", "doneWhen": []string{"It works", "Docs updated"}}
	post := func(path string, v any) {
		t.Helper()
		if status, res := e.call("POST", path, "", v); status >= 300 {
			t.Fatalf("seed POST %s = %d %v", path, status, res)
		}
	}
	post("/api/issues", map[string]any{"title": "Wire up the importer", "type": "task", "priority": "P1", "areas": []string{"importer"}, "body": body})
	post("/api/issues", map[string]any{"title": "Crash on empty input", "type": "bug", "priority": "P0", "areas": []string{"cli"}, "body": map[string]any{"problem": "It panics", "fix": "Guard the slice", "doneWhen": []string{"No panic"}}})
	post("/api/issues", map[string]any{"title": "Add dark mode", "type": "enhancement", "priority": "P3", "blockedBy": []int{1}, "body": body})
	post("/api/epics", map[string]any{"title": "Ship v1", "children": []int{1, 3}, "body": map[string]any{"goal": "Release", "doneWhen": []string{"Released"}}})
	e.appendRaw(
		`{"number":5,"title":"raw unvetted report","body":"unvetted text","state":"OPEN","created_at":"2026-01-01T00:00:00Z"}`,
		`{"number":6,"title":"Odd\u200b title","body":"see #1 and <script>alert(1)</script>","state":"OPEN","created_at":"2026-01-01T00:00:00Z","labels":["P2","task"]}`,
		`{"number":7,"title":"Loops back","body":"x","state":"OPEN","created_at":"2026-01-01T00:00:00Z","labels":["P2","task"],"blocked_by":[8]}`,
		`{"number":8,"title":"Loops forward","body":"y","state":"OPEN","created_at":"2026-01-01T00:00:00Z","labels":["P2","task"],"blocked_by":[7]}`,
		`{"number":9,"title":"Consider a cache","body":"Consider caching repeated reads","state":"OPEN","created_at":"2026-01-01T00:00:00Z","labels":["P0","idea"]}`,
	)
	if status, res := e.call("POST", "/api/issues/2/start", "alice", map[string]any{}); status != 200 {
		t.Fatalf("seed start = %d %v", status, res)
	}

	cases := []struct {
		file, method, path, actor string
		body                      any
	}{
		{"config", "GET", "/api/config", "", nil},
		{"board", "GET", "/api/board", "", nil},
		{"primer", "GET", "/api/primer", "", nil},
		{"search", "GET", "/api/search?q=importer", "", nil},
		{"triage", "GET", "/api/triage", "", nil},
		{"detail-claimed", "GET", "/api/issues/2", "", nil},
		{"detail-blocked", "GET", "/api/issues/3", "", nil},
		{"detail-epic", "GET", "/api/issues/4", "", nil},
		{"detail-odd", "GET", "/api/issues/6", "", nil},
		{"create", "POST", "/api/issues", "", map[string]any{"title": "Fixture issue", "type": "task", "body": body}},
		{"check", "POST", "/api/issues?dryRun=1", "", map[string]any{"title": "Fixture issue", "type": "task", "body": map[string]any{"goal": "Edit internal/cli/pr.go"}}},
		{"apply", "POST", "/api/apply?dryRun=1", "", map[string]any{"entries": []map[string]any{{"id": "a", "title": "First", "type": "task"}}}},
		{"error-claimed-by-other", "POST", "/api/issues/2/start", "bob", map[string]any{}},
		{"error-claimed-by-you", "POST", "/api/issues/2/start", "alice", map[string]any{}},
		{"error-blocked", "POST", "/api/issues/3/start", "carol", map[string]any{}},
		{"error-invalid", "POST", "/api/issues/1/close", "", map[string]any{}},
	}
	dir := filepath.Join("web", "src", "fixtures")
	if *update {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range cases {
		_, res := e.call(c.method, c.path, c.actor, c.body)
		// List responses decode to a wrapper in call(); fixtures want the
		// array itself.
		var v any = res
		if list, ok := res["_list"]; ok {
			v = list
		}
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false) // keep "<script>" readable in the fixture
		if err := enc.Encode(normalize(v)); err != nil {
			t.Fatal(err)
		}
		// An invisible character in a committed file is a trap; spell it out.
		buf = *bytes.NewBuffer(bytes.ReplaceAll(buf.Bytes(), []byte("\u200b"), []byte(`\u200b`)))
		path := filepath.Join(dir, c.file+".json")
		if *update {
			if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v (run: go test ./daemon -run Fixtures -update)", path, err)
		}
		if !bytes.Equal(want, buf.Bytes()) {
			t.Errorf("%s is out of date with the daemon's response to %s %s; run: cd daemon && go test -run Fixtures -update", path, c.method, c.path)
		}
	}
}
