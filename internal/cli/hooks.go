package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// primeHookCommand is what the SessionStart hook runs; its stdout is
// injected into the agent's context at session start, which is the whole
// point of prime.
const primeHookCommand = "hew prime"

const hookEvent = "SessionStart"

// HookAgent is an agent whose project-level session-start configuration hew
// manages.
type HookAgent string

const (
	HookAgentClaude HookAgent = "claude"
	HookAgentCodex  HookAgent = "codex"
)

// ParseHookAgent accepts the agents whose hook file formats hew supports.
func ParseHookAgent(value string) (HookAgent, error) {
	switch HookAgent(value) {
	case HookAgentClaude, HookAgentCodex:
		return HookAgent(value), nil
	default:
		return "", usageErr("agent must be claude or codex")
	}
}

// FindProjectRoot walks up from start until it finds the directory
// containing .git (a directory in a normal checkout, a file in a
// worktree). Project-level Claude Code settings live at its .claude/.
func FindProjectRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("not in a git repository; hooks are installed at the project root")
		}
		dir = parent
	}
}

// HooksInstall adds a SessionStart hook running `hew prime` to the selected
// agent's project configuration, creating the file if needed and leaving
// everything else in it untouched. Idempotent.
func (a *App) HooksInstall(projectRoot string, agent HookAgent) error {
	var err error
	agent, err = ParseHookAgent(string(agent))
	if err != nil {
		return err
	}
	path := hooksPath(projectRoot, agent)
	settings, err := readSettings(path)
	if err != nil {
		return err
	}
	changed := addPrimeHook(settings)
	if changed {
		if err := writeSettings(path, settings); err != nil {
			return err
		}
	}
	return a.emitResult(map[string]any{"agent": agent, "installed": changed, "path": path}, func() {
		if changed {
			a.printf("installed %s %s hook running `%s` in %s\n", agent, hookEvent, primeHookCommand, path)
		} else {
			a.printf("%s %s hook already installed in %s\n", agent, hookEvent, path)
		}
	})
}

// HooksRemove strips the selected agent's `hew prime` hook again, pruning any
// structures it leaves empty.
func (a *App) HooksRemove(projectRoot string, agent HookAgent) error {
	var err error
	agent, err = ParseHookAgent(string(agent))
	if err != nil {
		return err
	}
	path := hooksPath(projectRoot, agent)
	settings, err := readSettings(path)
	if err != nil {
		return err
	}
	changed := removePrimeHook(settings)
	if changed {
		if err := writeSettings(path, settings); err != nil {
			return err
		}
	}
	return a.emitResult(map[string]any{"agent": agent, "removed": changed, "path": path}, func() {
		if changed {
			a.printf("removed %s %s hook from %s\n", agent, hookEvent, path)
		} else {
			a.printf("no %s %s hook found in %s\n", agent, hookEvent, path)
		}
	})
}

func hooksPath(projectRoot string, agent HookAgent) string {
	if agent == HookAgentCodex {
		return filepath.Join(projectRoot, ".codex", "hooks.json")
	}
	return filepath.Join(projectRoot, ".claude", "settings.json")
}

// readSettings parses the settings file into a generic map so fields this
// tool knows nothing about survive the round-trip. A missing file is an
// empty settings object; a malformed one is an error — never clobber a
// file we can't parse.
func readSettings(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON, refusing to modify it: %w", path, err)
	}
	if settings == nil {
		settings = map[string]any{}
	}
	return settings, nil
}

func writeSettings(path string, settings map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// addPrimeHook appends the hook entry unless any SessionStart hook already
// runs hew prime (however the user phrased its entry). Reports whether
// it changed anything.
func addPrimeHook(settings map[string]any) bool {
	if hasPrimeHook(settings) {
		return false
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
		settings["hooks"] = hooks
	}
	entries, _ := hooks[hookEvent].([]any)
	hooks[hookEvent] = append(entries, map[string]any{
		"hooks": []any{
			map[string]any{"type": "command", "command": primeHookCommand},
		},
	})
	return true
}

func hasPrimeHook(settings map[string]any) bool {
	for _, entry := range sessionStartEntries(settings) {
		m, _ := entry.(map[string]any)
		inner, _ := m["hooks"].([]any)
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			if cmd, _ := hm["command"].(string); strings.Contains(cmd, primeHookCommand) {
				return true
			}
		}
	}
	return false
}

// removePrimeHook deletes every issues-prime hook and prunes entries, the
// SessionStart list, and the hooks object when they end up empty.
func removePrimeHook(settings map[string]any) bool {
	entries := sessionStartEntries(settings)
	changed := false
	var keptEntries []any
	for _, entry := range entries {
		m, ok := entry.(map[string]any)
		if !ok {
			keptEntries = append(keptEntries, entry)
			continue
		}
		inner, _ := m["hooks"].([]any)
		var keptHooks []any
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			if cmd, _ := hm["command"].(string); strings.Contains(cmd, primeHookCommand) {
				changed = true
				continue
			}
			keptHooks = append(keptHooks, h)
		}
		if len(keptHooks) == 0 && len(inner) > 0 {
			continue // entry existed only for our hook
		}
		if keptHooks != nil {
			m["hooks"] = keptHooks
		}
		keptEntries = append(keptEntries, entry)
	}
	if !changed {
		return false
	}
	hooks := settings["hooks"].(map[string]any)
	if len(keptEntries) == 0 {
		delete(hooks, hookEvent)
	} else {
		hooks[hookEvent] = keptEntries
	}
	if len(hooks) == 0 {
		delete(settings, "hooks")
	}
	return true
}

func sessionStartEntries(settings map[string]any) []any {
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		return nil
	}
	entries, _ := hooks[hookEvent].([]any)
	return entries
}
