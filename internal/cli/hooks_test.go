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
	for _, agent := range []HookAgent{HookAgentClaude, HookAgentCodex} {
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
