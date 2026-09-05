package cli

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// opencodePluginHarness drives the embedded plugin under node. It lives
// outside the asset so the file hew installs stays exactly what opencode
// loads, with no test scaffolding in it.
//
//go:embed testdata/opencode_plugin_harness.mjs
var opencodePluginHarness []byte

func hooksApp(t *testing.T) (*App, *bytes.Buffer, string) {
	t.Helper()
	app, out, _ := newApp(newFake())
	return app, out, t.TempDir()
}

func readHooksFile(t *testing.T, root string, agent HookAgent) map[string]any {
	t.Helper()
	data, err := os.ReadFile(hooksPath(root, agent))
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("settings.json invalid: %v\n%s", err, data)
	}
	return settings
}

func TestHooksInstallFresh(t *testing.T) {
	app, out, root := hooksApp(t)
	if err := app.HooksInstall(root, HookAgentClaude); err != nil {
		t.Fatal(err)
	}
	settings := readHooksFile(t, root, HookAgentClaude)
	entries := settings["hooks"].(map[string]any)["SessionStart"].([]any)
	hook := entries[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	if hook["command"] != "hew prime" || hook["type"] != "command" {
		t.Errorf("hook = %v", hook)
	}
	if !strings.Contains(out.String(), "installed") {
		t.Errorf("output = %q", out.String())
	}
}

func TestHooksInstallIdempotent(t *testing.T) {
	app, _, root := hooksApp(t)
	if err := app.HooksInstall(root, HookAgentClaude); err != nil {
		t.Fatal(err)
	}
	if err := app.HooksInstall(root, HookAgentClaude); err != nil {
		t.Fatal(err)
	}
	settings := readHooksFile(t, root, HookAgentClaude)
	entries := settings["hooks"].(map[string]any)["SessionStart"].([]any)
	if len(entries) != 1 {
		t.Errorf("hook duplicated: %v", entries)
	}
}

func TestHooksInstallPreservesExistingSettings(t *testing.T) {
	app, _, root := hooksApp(t)
	existing := `{
  "permissions": {"allow": ["Bash(go test:*)"]},
  "hooks": {
    "SessionStart": [
      {"hooks": [{"type": "command", "command": "echo hello"}]}
    ],
    "PostToolUse": [
      {"matcher": "Edit", "hooks": [{"type": "command", "command": "gofmt -w ."}]}
    ]
  }
}`
	if err := os.MkdirAll(filepath.Join(root, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".claude", "settings.json"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := app.HooksInstall(root, HookAgentClaude); err != nil {
		t.Fatal(err)
	}
	settings := readHooksFile(t, root, HookAgentClaude)
	if _, ok := settings["permissions"]; !ok {
		t.Error("permissions dropped")
	}
	hooks := settings["hooks"].(map[string]any)
	if _, ok := hooks["PostToolUse"]; !ok {
		t.Error("PostToolUse hooks dropped")
	}
	entries := hooks["SessionStart"].([]any)
	if len(entries) != 2 {
		t.Fatalf("SessionStart entries = %v", entries)
	}
	first := entries[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	if first["command"] != "echo hello" {
		t.Errorf("existing hook disturbed: %v", first)
	}
}

func TestHooksInstallRefusesMalformed(t *testing.T) {
	app, _, root := hooksApp(t)
	if err := os.MkdirAll(filepath.Join(root, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".claude", "settings.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := app.HooksInstall(root, HookAgentClaude)
	if err == nil || !strings.Contains(err.Error(), "refusing to modify") {
		t.Errorf("err = %v", err)
	}
}

func TestHooksRemove(t *testing.T) {
	app, _, root := hooksApp(t)
	if err := app.HooksInstall(root, HookAgentClaude); err != nil {
		t.Fatal(err)
	}
	if err := app.HooksRemove(root, HookAgentClaude); err != nil {
		t.Fatal(err)
	}
	settings := readHooksFile(t, root, HookAgentClaude)
	if _, ok := settings["hooks"]; ok {
		t.Errorf("empty hooks object not pruned: %v", settings)
	}
}

func TestHooksRemoveKeepsOtherHooks(t *testing.T) {
	app, _, root := hooksApp(t)
	existing := `{
  "hooks": {
    "SessionStart": [
      {"hooks": [{"type": "command", "command": "echo hello"}]},
      {"hooks": [{"type": "command", "command": "hew prime"}]}
    ]
  }
}`
	if err := os.MkdirAll(filepath.Join(root, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".claude", "settings.json"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := app.HooksRemove(root, HookAgentClaude); err != nil {
		t.Fatal(err)
	}
	settings := readHooksFile(t, root, HookAgentClaude)
	entries := settings["hooks"].(map[string]any)["SessionStart"].([]any)
	if len(entries) != 1 {
		t.Fatalf("entries = %v", entries)
	}
	kept := entries[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	if kept["command"] != "echo hello" {
		t.Errorf("wrong hook removed: %v", kept)
	}
}

func TestHooksRemoveNothingInstalled(t *testing.T) {
	app, _, root := hooksApp(t)
	if err := app.HooksRemove(root, HookAgentClaude); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Error("remove created a settings file")
	}
}

func TestHooksJSONOutput(t *testing.T) {
	for _, agent := range []HookAgent{HookAgentClaude, HookAgentCodex, HookAgentOpencode} {
		t.Run(string(agent), func(t *testing.T) {
			app, out, root := hooksApp(t)
			app.JSON = true
			if err := app.HooksInstall(root, agent); err != nil {
				t.Fatal(err)
			}
			var got struct {
				Agent     string `json:"agent"`
				Installed bool   `json:"installed"`
				Path      string `json:"path"`
			}
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Agent != string(agent) || !got.Installed || !strings.HasSuffix(got.Path, hooksPath("", agent)) {
				t.Errorf("JSON = %+v", got)
			}
		})
	}
}

func TestHooksInstallCodex(t *testing.T) {
	app, _, root := hooksApp(t)
	if err := app.HooksInstall(root, HookAgentCodex); err != nil {
		t.Fatal(err)
	}
	codexPath := filepath.Join(root, ".codex", "hooks.json")
	if _, err := os.Stat(codexPath); err != nil {
		t.Fatalf("Codex hook file missing: %v", err)
	}
	claudePath := filepath.Join(root, ".claude", "settings.json")
	if _, err := os.Stat(claudePath); !os.IsNotExist(err) {
		t.Fatalf("Codex install wrote Claude settings: %v", err)
	}
	if err := app.HooksInstall(root, HookAgentCodex); err != nil {
		t.Fatal(err)
	}
	settings := readHooksFile(t, root, HookAgentCodex)
	entries := settings["hooks"].(map[string]any)["SessionStart"].([]any)
	if len(entries) != 1 {
		t.Fatalf("hook duplicated: %v", entries)
	}
	hook := entries[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	if hook["command"] != "hew prime" || hook["type"] != "command" {
		t.Errorf("hook = %v", hook)
	}
}

func TestHooksRemoveCodexKeepsOtherHooks(t *testing.T) {
	app, _, root := hooksApp(t)
	path := hooksPath(root, HookAgentCodex)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo hello"}]},{"hooks":[{"type":"command","command":"hew prime"}]}]}}`
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := app.HooksRemove(root, HookAgentCodex); err != nil {
		t.Fatal(err)
	}
	settings := readHooksFile(t, root, HookAgentCodex)
	entries := settings["hooks"].(map[string]any)["SessionStart"].([]any)
	if len(entries) != 1 {
		t.Fatalf("entries = %v", entries)
	}
	kept := entries[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	if kept["command"] != "echo hello" {
		t.Errorf("wrong hook removed: %v", kept)
	}
}

func TestHooksInstallOpencode(t *testing.T) {
	app, out, root := hooksApp(t)
	if err := app.HooksInstall(root, HookAgentOpencode); err != nil {
		t.Fatal(err)
	}
	// Spelled out rather than composed from hookPathParts: this exact path
	// is the one opencode auto-discovers, and a test that asks the code
	// under test where it wrote would follow a typo straight into a plugin
	// opencode never loads.
	got, err := os.ReadFile(filepath.Join(root, ".opencode", "plugins", "hew-prime.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, opencodePlugin) {
		t.Errorf("plugin content = %s", got)
	}
	if !strings.Contains(out.String(), "installed") {
		t.Errorf("output = %q", out.String())
	}
}

func TestHooksInstallOpencodeIdempotent(t *testing.T) {
	app, out, root := hooksApp(t)
	if err := app.HooksInstall(root, HookAgentOpencode); err != nil {
		t.Fatal(err)
	}
	if err := app.HooksInstall(root, HookAgentOpencode); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "already installed") {
		t.Errorf("output = %q", out.String())
	}
}

func TestHooksInstallOpencodePreservesNeighbors(t *testing.T) {
	app, _, root := hooksApp(t)
	plugins := filepath.Join(root, ".opencode", "plugins")
	if err := os.MkdirAll(plugins, 0o755); err != nil {
		t.Fatal(err)
	}
	sibling := []byte("export const Other = async () => ({})\n")
	if err := os.WriteFile(filepath.Join(plugins, "other.js"), sibling, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := app.HooksInstall(root, HookAgentOpencode); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(plugins, "other.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, sibling) {
		t.Errorf("sibling plugin disturbed: %s", got)
	}
}

func TestHooksInstallOpencodeRefusesForeignFile(t *testing.T) {
	app, _, root := hooksApp(t)
	foreign := []byte("export const Mine = async () => ({})\n")
	if err := os.MkdirAll(filepath.Dir(opencodePluginPath(root)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(opencodePluginPath(root), foreign, 0o644); err != nil {
		t.Fatal(err)
	}
	err := app.HooksInstall(root, HookAgentOpencode)
	if err == nil || !strings.Contains(err.Error(), "not managed by hew") {
		t.Errorf("err = %v", err)
	}
	got, readErr := os.ReadFile(opencodePluginPath(root))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(got, foreign) {
		t.Errorf("foreign plugin modified: %s", got)
	}
}

func TestHooksRemoveOpencode(t *testing.T) {
	app, _, root := hooksApp(t)
	if err := app.HooksInstall(root, HookAgentOpencode); err != nil {
		t.Fatal(err)
	}
	if err := app.HooksRemove(root, HookAgentOpencode); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(opencodePluginPath(root)); !os.IsNotExist(err) {
		t.Error("plugin file still exists")
	}
	if _, err := os.Stat(filepath.Join(root, ".opencode")); !os.IsNotExist(err) {
		t.Error("empty .opencode not pruned")
	}
}

func TestHooksRemoveOpencodeKeepsNeighbors(t *testing.T) {
	app, _, root := hooksApp(t)
	if err := app.HooksInstall(root, HookAgentOpencode); err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(root, ".opencode", "plugins", "other.js")
	if err := os.WriteFile(sibling, []byte("export const Other = async () => ({})\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := app.HooksRemove(root, HookAgentOpencode); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Errorf("sibling plugin removed: %v", err)
	}
}

func TestHooksRemoveOpencodeNothingInstalled(t *testing.T) {
	app, out, root := hooksApp(t)
	if err := app.HooksRemove(root, HookAgentOpencode); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no opencode plugin") {
		t.Errorf("output = %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".opencode")); !os.IsNotExist(err) {
		t.Error("remove created .opencode")
	}
}

func TestHooksRemoveOpencodeRefusesForeignFile(t *testing.T) {
	app, _, root := hooksApp(t)
	foreign := []byte("export const Mine = async () => ({})\n")
	if err := os.MkdirAll(filepath.Dir(opencodePluginPath(root)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(opencodePluginPath(root), foreign, 0o644); err != nil {
		t.Fatal(err)
	}
	err := app.HooksRemove(root, HookAgentOpencode)
	if err == nil || !strings.Contains(err.Error(), "not managed by hew") {
		t.Errorf("err = %v", err)
	}
	got, readErr := os.ReadFile(opencodePluginPath(root))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(got, foreign) {
		t.Errorf("foreign plugin removed: %s", got)
	}
}

// stripJSComments drops // line comments so a token that appears only in the
// asset's header prose cannot satisfy a search for runtime behavior — the
// header mentions `hew prime` and system context in English. The asset has no
// block comments and no // inside a string, regex or template literal, which
// is what lets this stay a line-oriented scan; TestOpencodePluginRuns is the
// check that does not depend on reading the source at all.
func stripJSComments(src string) string {
	var code strings.Builder
	for line := range strings.SplitSeq(src, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		code.WriteString(line)
		code.WriteString("\n")
	}
	return code.String()
}

// The marker lives in the header comment on purpose: it is what install and
// remove use to tell hew's plugin from a user's own file of the same name.
func TestOpencodePluginCarriesMarker(t *testing.T) {
	if !strings.Contains(string(opencodePlugin), opencodeMarker) {
		t.Errorf("plugin source missing the %q marker", opencodeMarker)
	}
}

// A source-level guard against the injection route regressing to a chat
// message. TestOpencodePluginRuns proves the primer reaches system context;
// it cannot prove nothing *else* also sends it to the transcript.
func TestOpencodePluginDoesNotPrompt(t *testing.T) {
	if strings.Contains(stripJSComments(string(opencodePlugin)), "session.prompt") {
		t.Error("plugin must inject via system context, not session.prompt")
	}
}

// TestOpencodePluginRuns executes the asset under node the way opencode does,
// against a stub shell runner, so the claim the grep above can only gesture at
// — the primer reaches the model as system context, once per session, before
// the first turn — is actually exercised. A syntax error fails here too.
//
// The asset ships as .js and is copied to .mjs to run: with no package.json
// beside it node parses a .js file as CommonJS, and a CommonJS parse accepts
// syntax an ESM parse rejects, so `node --check hew-prime.js` is not a gate.
func TestOpencodePluginRuns(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	dir := t.TempDir()
	plugin := filepath.Join(dir, "hew-prime.mjs")
	if err := os.WriteFile(plugin, opencodePlugin, 0o600); err != nil {
		t.Fatal(err)
	}
	harness := filepath.Join(dir, "harness.mjs")
	if err := os.WriteFile(harness, opencodePluginHarness, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, harness, plugin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		t.Fatalf("plugin failed to run: %v\n%s", err, stderr.String())
	}
	var got struct {
		Created     []string `json:"created"`
		Cached      []string `json:"cached"`
		Lazy        []string `json:"lazy"`
		Anonymous   []string `json:"anonymous"`
		AfterDelete []string `json:"afterDelete"`
		Calls       []struct {
			Command string `json:"command"`
			Cwd     string `json:"cwd"`
		} `json:"calls"`
	}
	if err := json.Unmarshal(stdout, &got); err != nil {
		t.Fatalf("harness output not JSON: %v\n%s", err, stdout)
	}
	// The primer itself, trimmed, is what lands in system context.
	for _, tc := range []struct {
		name string
		got  []string
	}{
		{"announced session", got.Created},
		{"same session again", got.Cached},
		{"session never announced", got.Lazy},
		{"session announced again after deletion", got.AfterDelete},
	} {
		if len(tc.got) != 1 || tc.got[0] != "PRIMER-SENTINEL" {
			t.Errorf("%s: system context = %q, want [PRIMER-SENTINEL]", tc.name, tc.got)
		}
	}
	if len(got.Anonymous) != 0 {
		t.Errorf("transform without a session contributed %q", got.Anonymous)
	}
	// Three sessions were primed — s1, the unannounced one, and s1 again
	// once deletion evicted it — and the cached second transform of s1 adds
	// no fourth: `hew prime` runs once per session, in the worktree.
	if len(got.Calls) != 3 {
		t.Errorf("hew prime ran %d times, want 3 (once per session): %+v", len(got.Calls), got.Calls)
	}
	for _, call := range got.Calls {
		if call.Command != primeHookCommand || call.Cwd != "/stub/worktree" {
			t.Errorf("plugin ran %q in %q, want %q in the worktree", call.Command, call.Cwd, primeHookCommand)
		}
	}
}

func opencodePluginPath(root string) string {
	return hooksPath(root, HookAgentOpencode)
}

func TestParseHookAgent(t *testing.T) {
	for _, agent := range []HookAgent{HookAgentClaude, HookAgentCodex, HookAgentOpencode} {
		got, err := ParseHookAgent(string(agent))
		if err != nil || got != agent {
			t.Errorf("ParseHookAgent(%q) = %q, %v", agent, got, err)
		}
	}
	if _, err := ParseHookAgent("other"); err == nil || ExitCode(err) != ExitUsage {
		t.Errorf("invalid agent error = %v", err)
	}
}

func TestFindProjectRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := FindProjectRoot(nested)
	if err != nil {
		t.Fatal(err)
	}
	// Resolve symlinks: macOS TempDir lives under /var -> /private/var.
	wantResolved, _ := filepath.EvalSymlinks(root)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != wantResolved {
		t.Errorf("FindProjectRoot = %q, want %q", got, root)
	}
}

func TestFindProjectRootWorktreeGitFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /elsewhere"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := FindProjectRoot(root); err != nil {
		t.Errorf("worktree .git file not accepted: %v", err)
	}
}

func TestFindProjectRootNotARepo(t *testing.T) {
	if _, err := FindProjectRoot(t.TempDir()); err == nil {
		t.Error("expected error outside a repository")
	}
}

// targetFile writes a valid settings file in dir — the kind of file a
// symlink would aim hew at, standing in for a global agent configuration.
func targetFile(t *testing.T, dir string) (file string, before []byte) {
	t.Helper()
	file = filepath.Join(dir, "settings.json")
	before = []byte(`{"permissions":{"allow":["Bash(rm:*)"]}}` + "\n")
	if err := os.WriteFile(file, before, 0o644); err != nil {
		t.Fatal(err)
	}
	return file, before
}

// externalTarget puts that file outside the project root — the reach
// os.Root's containment already refuses on its own.
func externalTarget(t *testing.T) (dir, file string, before []byte) {
	t.Helper()
	dir = t.TempDir()
	file, before = targetFile(t, dir)
	return dir, file, before
}

// internalTarget puts it inside the checkout instead. Containment says
// nothing about these: a relative link to one resolves within the root, so
// os.Root follows it happily and only the lstat pass stands in the way.
func internalTarget(t *testing.T, root string) (dir, file string, before []byte) {
	t.Helper()
	dir = filepath.Join(root, "elsewhere")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file, before = targetFile(t, dir)
	return dir, file, before
}

// linkSettings plants one of the two symlink shapes the fix has to refuse:
// the directory hew owns replaced by a link, or the settings file itself.
// The target is absolute unless relative is set, which exercises the ../
// form rather than the absolute one.
func linkSettings(t *testing.T, root string, agent HookAgent, shape string, dir, file string, relative bool) {
	t.Helper()
	settings := hooksPath(root, agent)
	parent := filepath.Dir(settings)
	dest, from := dir, filepath.Dir(parent)
	if shape == "file" {
		dest, from = file, parent
	}
	if relative {
		rel, err := filepath.Rel(from, dest)
		if err != nil {
			t.Fatal(err)
		}
		dest = rel
	}
	if err := os.MkdirAll(filepath.Dir(parent), 0o755); err != nil {
		t.Fatal(err)
	}
	switch shape {
	case "dir":
		if err := os.Symlink(dest, parent); err != nil {
			t.Fatal(err)
		}
	case "file":
		if err := os.MkdirAll(parent, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(dest, settings); err != nil {
			t.Fatal(err)
		}
	}
}

var symlinkShapes = []struct {
	name     string
	agent    HookAgent
	shape    string
	relative bool
}{
	{"claude dir", HookAgentClaude, "dir", false},
	{"claude file", HookAgentClaude, "file", false},
	{"claude dir relative", HookAgentClaude, "dir", true},
	{"claude file relative", HookAgentClaude, "file", true},
	{"codex dir", HookAgentCodex, "dir", false},
	{"codex file", HookAgentCodex, "file", false},
	{"codex dir relative", HookAgentCodex, "dir", true},
	{"codex file relative", HookAgentCodex, "file", true},
	{"opencode dir", HookAgentOpencode, "dir", false},
	{"opencode file", HookAgentOpencode, "file", false},
	{"opencode dir relative", HookAgentOpencode, "dir", true},
	{"opencode file relative", HookAgentOpencode, "file", true},
}

var hookOps = []string{"install", "remove"}

// assertRefused requires op to fail, to say why, and to leave the link's
// target byte-identical.
func assertRefused(t *testing.T, app *App, root string, agent HookAgent, op, target string, before []byte) {
	t.Helper()
	var err error
	if op == "install" {
		err = app.HooksInstall(root, agent)
	} else {
		err = app.HooksRemove(root, agent)
	}
	if err == nil {
		t.Fatalf("%s followed the symlink", op)
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("error does not name the cause: %v", err)
	}
	after, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("target modified:\n%s", after)
	}
}

func TestHooksRefuseSymlinkedSettings(t *testing.T) {
	for _, tc := range symlinkShapes {
		for _, op := range hookOps {
			t.Run(tc.name+" "+op, func(t *testing.T) {
				app, _, root := hooksApp(t)
				dir, file, before := externalTarget(t)
				linkSettings(t, root, tc.agent, tc.shape, dir, file, tc.relative)
				assertRefused(t, app, root, tc.agent, op, file, before)
			})
		}
	}
}

// A link that stays inside the checkout is refused too, and this is the case
// only the lstat pass catches — remove that pass and every subtest here goes
// green with hew writing through the link. The target is always relative on
// purpose: an absolute one escapes the root, which containment rejects by
// itself, so an absolute variant would still pass with the guard gone.
func TestHooksRefuseSymlinkInsideProject(t *testing.T) {
	for _, tc := range symlinkShapes {
		if !tc.relative {
			continue
		}
		for _, op := range hookOps {
			t.Run(tc.name+" "+op, func(t *testing.T) {
				app, _, root := hooksApp(t)
				dir, file, before := internalTarget(t, root)
				linkSettings(t, root, tc.agent, tc.shape, dir, file, true)
				assertRefused(t, app, root, tc.agent, op, file, before)
			})
		}
	}
}

// os.Root reports failures against the relative name it was handed, which
// does not say which checkout they came from. Every such error names the
// absolute path instead.
func TestHooksErrorsNameTheProjectPath(t *testing.T) {
	for _, tc := range []struct {
		name  string
		plant func(t *testing.T, root string)
	}{
		// A directory standing where the settings file goes: the lstat pass
		// sees a plain directory and lets it through, then the read fails.
		{"settings file unreadable", func(t *testing.T, root string) {
			if err := os.MkdirAll(hooksPath(root, HookAgentClaude), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		// A file where the directory goes: the lstat pass itself fails.
		{"parent not a directory", func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, ".claude"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, _, root := hooksApp(t)
			tc.plant(t, root)
			err := app.HooksInstall(root, HookAgentClaude)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), hooksPath(root, HookAgentClaude)) {
				t.Errorf("error does not name the checkout: %v", err)
			}
		})
	}
}
