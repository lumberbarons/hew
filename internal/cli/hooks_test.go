package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	app, out, root := hooksApp(t)
	app.JSON = true
	if err := app.HooksInstall(root, HookAgentClaude); err != nil {
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
	if got.Agent != string(HookAgentClaude) || !got.Installed || !strings.HasSuffix(got.Path, filepath.Join(".claude", "settings.json")) {
		t.Errorf("JSON = %+v", got)
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

func TestParseHookAgent(t *testing.T) {
	for _, agent := range []HookAgent{HookAgentClaude, HookAgentCodex} {
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
