package cli

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// opencodePlugin is the auto-discovered opencode plugin hew installs as
// .opencode/plugins/hew-prime.js: it runs `hew prime` once per session and
// injects the primer as system context, not a chat message.
//
//go:embed opencode/hew-prime.js
var opencodePlugin []byte

// opencodeMarker marks the plugin file as hew's: install reports a file
// carrying it as already installed, remove only deletes one.
const opencodeMarker = "@hew-managed"

// primeHookCommand is what the SessionStart hook runs; its stdout is
// injected into the agent's context at session start, which is the whole
// point of prime.
const primeHookCommand = "hew prime"

const hookEvent = "SessionStart"

// HookAgent is an agent whose project-level session-start configuration hew
// manages.
type HookAgent string

const (
	HookAgentClaude   HookAgent = "claude"
	HookAgentCodex    HookAgent = "codex"
	HookAgentOpencode HookAgent = "opencode"
)

// ParseHookAgent accepts the agents whose hook file formats hew supports.
func ParseHookAgent(value string) (HookAgent, error) {
	switch HookAgent(value) {
	case HookAgentClaude, HookAgentCodex, HookAgentOpencode:
		return HookAgent(value), nil
	default:
		return "", usageErr("agent must be claude, codex, or opencode")
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

// HooksInstall adds a session-start hook running `hew prime` to the selected
// agent's project configuration — a JSON settings entry for claude and
// codex, an auto-discovered plugin file for opencode — creating the file if
// needed and leaving everything else in it untouched. Idempotent.
func (a *App) HooksInstall(projectRoot string, agent HookAgent) error {
	var err error
	agent, err = ParseHookAgent(string(agent))
	if err != nil {
		return err
	}
	if agent == HookAgentOpencode {
		return a.hooksInstallOpencode(projectRoot)
	}
	file, err := openHookSettings(projectRoot, agent)
	if err != nil {
		return err
	}
	defer file.close()
	settings, err := file.read()
	if err != nil {
		return err
	}
	changed := addPrimeHook(settings)
	if changed {
		if err := file.write(settings); err != nil {
			return err
		}
	}
	return a.emitResult(map[string]any{"agent": agent, "installed": changed, "path": file.path}, func() {
		if changed {
			a.printf("installed %s %s hook running `%s` in %s\n", agent, hookEvent, primeHookCommand, file.path)
		} else {
			a.printf("%s %s hook already installed in %s\n", agent, hookEvent, file.path)
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
	if agent == HookAgentOpencode {
		return a.hooksRemoveOpencode(projectRoot)
	}
	file, err := openHookSettings(projectRoot, agent)
	if err != nil {
		return err
	}
	defer file.close()
	settings, err := file.read()
	if err != nil {
		return err
	}
	changed := removePrimeHook(settings)
	if changed {
		if err := file.write(settings); err != nil {
			return err
		}
	}
	return a.emitResult(map[string]any{"agent": agent, "removed": changed, "path": file.path}, func() {
		if changed {
			a.printf("removed %s %s hook from %s\n", agent, hookEvent, file.path)
		} else {
			a.printf("no %s %s hook found in %s\n", agent, hookEvent, file.path)
		}
	})
}

// hooksInstallOpencode writes the auto-discovered opencode plugin
// .opencode/plugins/hew-prime.js. Unlike the JSON settings files, the whole
// file is hew's: installing is idempotent by the hew marker, and a file
// without it — a user's own plugin of the same name — is refused, never
// clobbered.
func (a *App) hooksInstallOpencode(projectRoot string) error {
	file, err := openHookSettings(projectRoot, HookAgentOpencode)
	if err != nil {
		return err
	}
	defer file.close()
	existing, err := file.readRaw()
	if err != nil {
		return err
	}
	if existing != nil {
		if !bytes.Contains(existing, []byte(opencodeMarker)) {
			return fmt.Errorf("%s already exists and is not managed by hew, refusing to modify it", file.path)
		}
		return a.emitResult(map[string]any{"agent": HookAgentOpencode, "installed": false, "path": file.path}, func() {
			a.printf("opencode plugin already installed in %s\n", file.path)
		})
	}
	if err := file.writeRaw(opencodePlugin); err != nil {
		return err
	}
	return a.emitResult(map[string]any{"agent": HookAgentOpencode, "installed": true, "path": file.path}, func() {
		a.printf("installed opencode plugin in %s\n", file.path)
	})
}

// hooksRemoveOpencode deletes the plugin file and prunes the directory
// components hew created beneath the project root as long as they are
// empty. A file hew did not install is refused, never deleted.
func (a *App) hooksRemoveOpencode(projectRoot string) error {
	file, err := openHookSettings(projectRoot, HookAgentOpencode)
	if err != nil {
		return err
	}
	defer file.close()
	existing, err := file.readRaw()
	if err != nil {
		return err
	}
	if existing == nil {
		return a.emitResult(map[string]any{"agent": HookAgentOpencode, "removed": false, "path": file.path}, func() {
			a.printf("no opencode plugin found in %s\n", file.path)
		})
	}
	if !bytes.Contains(existing, []byte(opencodeMarker)) {
		return fmt.Errorf("%s is not managed by hew, refusing to remove it", file.path)
	}
	if err := file.removeAndPrune(); err != nil {
		return err
	}
	return a.emitResult(map[string]any{"agent": HookAgentOpencode, "removed": true, "path": file.path}, func() {
		a.printf("removed opencode plugin from %s\n", file.path)
	})
}

// hookPathParts is the settings file's location relative to the project
// root: the components hew creates, and so exactly the ones a checkout
// could have replaced with symlinks before hew got there.
func hookPathParts(agent HookAgent) []string {
	switch agent {
	case HookAgentCodex:
		return []string{".codex", "hooks.json"}
	case HookAgentOpencode:
		return []string{".opencode", "plugins", "hew-prime.js"}
	}
	return []string{".claude", "settings.json"}
}

func hooksPath(projectRoot string, agent HookAgent) string {
	return filepath.Join(append([]string{projectRoot}, hookPathParts(agent)...)...)
}

// hookSettings is one agent's settings file, anchored to the project root
// so that no operation on it can reach outside the checkout.
type hookSettings struct {
	root *os.Root
	rel  string
	path string // absolute; os.Root names errors after rel, messages restore this
}

// openHookSettings anchors the settings file beneath the project root and
// refuses any symlink among the components hew owns.
//
// A checkout is untrusted input: making .claude, or settings.json inside
// it, a symlink to a valid JSON file elsewhere on the machine — a global
// agent configuration, say — would otherwise redirect an explicit `hew
// hooks install` onto that file. os.Root cannot resolve out of the root at
// all, which is the containment guarantee for every read and write below;
// the lstat pass then rejects the links that stay inside the checkout,
// which os.Root would happily follow.
func openHookSettings(projectRoot string, agent HookAgent) (*hookSettings, error) {
	root, err := os.OpenRoot(projectRoot)
	if err != nil {
		return nil, err
	}
	parts := hookPathParts(agent)
	file := &hookSettings{
		root: root,
		rel:  filepath.Join(parts...),
		path: hooksPath(projectRoot, agent),
	}
	if err := rejectSymlinks(root, projectRoot, parts); err != nil {
		file.close()
		return nil, err
	}
	return file, nil
}

func rejectSymlinks(root *os.Root, projectRoot string, parts []string) error {
	rel := ""
	for _, part := range parts {
		rel = filepath.Join(rel, part)
		abs := filepath.Join(projectRoot, rel)
		info, err := root.Lstat(rel)
		if errors.Is(err, os.ErrNotExist) {
			return nil // hew creates the rest itself; there is nothing to follow
		}
		if err != nil {
			return pathErr("checking", abs, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink, refusing to modify it: edit the file it resolves to directly, or replace the link with a real path", abs)
		}
	}
	return nil
}

func (h *hookSettings) close() {
	_ = h.root.Close()
}

// pathErr names the file a failed operation was aimed at. Every read and
// write below goes through os.Root, whose errors carry only the relative
// name it was given — which does not say which checkout failed, and this
// tool is routinely pointed at several.
func pathErr(verb, path string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s %s: %w", verb, path, err)
}

// read parses the settings file into a generic map so fields this tool
// knows nothing about survive the round-trip. A missing file is an empty
// settings object; a malformed one is an error — never clobber a file we
// can't parse.
func (h *hookSettings) read() (map[string]any, error) {
	data, err := h.root.ReadFile(h.rel)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, pathErr("reading", h.path, err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON, refusing to modify it: %w", h.path, err)
	}
	if settings == nil {
		settings = map[string]any{}
	}
	return settings, nil
}

func (h *hookSettings) write(settings map[string]any) error {
	if dir := filepath.Dir(h.rel); dir != "." {
		if err := h.root.MkdirAll(dir, 0o755); err != nil {
			return pathErr("creating", filepath.Dir(h.path), err)
		}
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return pathErr("writing", h.path, h.root.WriteFile(h.rel, append(data, '\n'), 0o644))
}

// readRaw returns the file's bytes, or nil when it does not exist — the
// opencode plugin file is JavaScript, not the JSON the other agents use.
func (h *hookSettings) readRaw() ([]byte, error) {
	data, err := h.root.ReadFile(h.rel)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, pathErr("reading", h.path, err)
	}
	return data, nil
}

func (h *hookSettings) writeRaw(data []byte) error {
	if dir := filepath.Dir(h.rel); dir != "." {
		if err := h.root.MkdirAll(dir, 0o755); err != nil {
			return pathErr("creating", filepath.Dir(h.path), err)
		}
	}
	return pathErr("writing", h.path, h.root.WriteFile(h.rel, data, 0o644))
}

// removeAndPrune deletes the file, then removes its parent directories
// toward the project root as long as they are empty — anything the user put
// beside the plugin keeps its directory alive.
func (h *hookSettings) removeAndPrune() error {
	if err := h.root.Remove(h.rel); err != nil {
		return pathErr("removing", h.path, err)
	}
	dir := filepath.Dir(h.rel)
	abs := filepath.Dir(h.path)
	for dir != "." {
		empty, err := h.dirEmpty(dir)
		if err != nil {
			return err
		}
		if !empty {
			return nil
		}
		if err := h.root.Remove(dir); err != nil {
			return pathErr("removing", abs, err)
		}
		dir = filepath.Dir(dir)
		abs = filepath.Dir(abs)
	}
	return nil
}

func (h *hookSettings) dirEmpty(rel string) (bool, error) {
	dir, err := h.root.Open(rel)
	if err != nil {
		return false, pathErr("reading", filepath.Dir(h.path), err)
	}
	defer func() { _ = dir.Close() }()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return false, pathErr("reading", filepath.Dir(h.path), err)
	}
	return len(entries) == 0, nil
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
