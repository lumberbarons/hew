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

// barePrimeCommand is the plain `hew prime`. It is what releases before the
// guard wrote (install rewrites that exact string in place; anything else a
// user phrased by hand is left as theirs), what Codex runs on Windows, and the
// substring install and remove match on to recognise a prime hook however it
// is phrased.
const barePrimeCommand = "hew prime"

// missingHewHint is what a session sees when hew is not on the hook's PATH.
// SessionStart stdout becomes agent context, so the text is addressed to the
// agent: it can tell the user, where a red hook error invites deleting the
// hook. Single-quoted into sh, so it holds no apostrophes.
const missingHewHint = "hew is not installed or not on PATH, so the tracker primer was not loaded. " +
	"Tell the user once that this repo tracks work with hew and how to install it: " +
	"curl -fsSL https://lumberbarons.github.io/hew/install.sh | bash " +
	"(or: go install github.com/lumberbarons/hew/cmd/hew@latest)."

// guardHookCommand wraps run so that a contributor without hew gets the hint
// instead of `hew: command not found`: SessionStart cannot block, but an
// unguarded failure shows as a red hook error on every start. Only a missing
// binary is absorbed — the if's status is run's own, so hew's auth (4) and
// network (1) failures stay loud. POSIX sh only: Codex on Windows runs
// `command` through cmd or PowerShell, so its entry also carries
// primeHookCommandWindows.
func guardHookCommand(run string) string {
	return "if command -v hew >/dev/null 2>&1; then " + run + "; else echo '" + missingHewHint + "'; fi"
}

// primeHookCommand is what the SessionStart hook runs; its stdout is
// injected into the agent's context at session start, which is the whole
// point of prime.
var primeHookCommand = guardHookCommand(barePrimeCommand)

// primeHookCommandWindows is Codex's Windows-only override (its hook schema's
// commandWindows field). It is the bare command, valid in both cmd and
// PowerShell; there is no portable one-line guard for those shells, so a
// Windows contributor without hew keeps the visible hook error.
const primeHookCommandWindows = barePrimeCommand

const hookEvent = "SessionStart"

// HookAgent is an agent whose project-level session-start configuration hew
// manages.
type HookAgent string

const (
	HookAgentClaude   HookAgent = "claude"
	HookAgentCodex    HookAgent = "codex"
	HookAgentCursor   HookAgent = "cursor"
	HookAgentOpencode HookAgent = "opencode"
)

// ParseHookAgent accepts the agents whose hook file formats hew supports.
func ParseHookAgent(value string) (HookAgent, error) {
	switch HookAgent(value) {
	case HookAgentClaude, HookAgentCodex, HookAgentCursor, HookAgentOpencode:
		return HookAgent(value), nil
	default:
		return "", usageErr("agent must be claude, codex, cursor, or opencode")
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
// agent's project configuration — a JSON settings entry for claude, codex,
// and cursor, an auto-discovered plugin file for opencode — creating the
// file if needed and leaving everything else in it untouched. Idempotent.
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
	result := addHookEntry(agent, settings)
	changed := result == hookAdded || result == hookUpgraded
	if changed {
		if err := file.write(settings); err != nil {
			return err
		}
	}
	event, command := hookEntryDetails(agent)
	return a.emitResult(map[string]any{"agent": agent, "installed": changed, "upgraded": result == hookUpgraded, "path": file.path}, func() {
		switch result {
		case hookAdded:
			a.printf("installed %s %s hook running `%s` in %s\n", agent, event, command, file.path)
		case hookUpgraded:
			a.printf("upgraded %s %s hook to run `%s` in %s\n", agent, event, command, file.path)
		case hookCustom:
			a.printf("%s %s hook already installed in %s with a command hew did not write; left unmodified\n", agent, event, file.path)
		default:
			a.printf("%s %s hook already installed in %s\n", agent, event, file.path)
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
	changed := removeHookEntry(agent, settings)
	if changed {
		if err := file.write(settings); err != nil {
			return err
		}
	}
	event, _ := hookEntryDetails(agent)
	return a.emitResult(map[string]any{"agent": agent, "removed": changed, "path": file.path}, func() {
		if changed {
			a.printf("removed %s %s hook from %s\n", agent, event, file.path)
		} else {
			a.printf("no %s %s hook found in %s\n", agent, event, file.path)
		}
	})
}

// hookInstallResult is what addHookEntry did to the settings.
type hookInstallResult int

const (
	hookUnchanged hookInstallResult = iota
	hookAdded
	hookUpgraded // an existing bare entry was rewritten to the guarded command
	hookCustom   // an entry runs prime in a form hew did not write; left alone
)

// addHookEntry appends the session-start hook in the agent's own shape —
// claude and codex share the nested {"hooks": [...]} form, cursor writes
// flat entries under its own event key — via the agent's hookShape, so a
// third shape is one new hookShape value rather than a new pair of
// traversal functions. An entry already running prime is not duplicated; if it
// is the exact bare command older releases wrote, it is upgraded in place.
func addHookEntry(agent HookAgent, settings map[string]any) hookInstallResult {
	shape := hookShapeFor(agent)
	if shape.ensureDefaults != nil {
		shape.ensureDefaults(settings)
	}
	if hasHookMatch(shape, settings) {
		upgraded, custom := false, false
		for _, entry := range hookEventEntries(shape.eventKey, settings) {
			if !shape.matches(entry) {
				continue
			}
			if shape.upgrade != nil && shape.upgrade(entry) {
				upgraded = true
			}
			if !shape.current(entry) {
				custom = true
			}
		}
		switch {
		case upgraded:
			return hookUpgraded
		case custom:
			return hookCustom
		}
		return hookUnchanged
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
		settings["hooks"] = hooks
	}
	entries, _ := hooks[shape.eventKey].([]any)
	hooks[shape.eventKey] = append(entries, shape.newEntry())
	return hookAdded
}

// removeHookEntry strips the session-start hook, again via the agent's
// hookShape, pruning entries, the event's list, and the hooks object when
// they end up empty.
func removeHookEntry(agent HookAgent, settings map[string]any) bool {
	shape := hookShapeFor(agent)
	entries := hookEventEntries(shape.eventKey, settings)
	changed := false
	var kept []any
	for _, entry := range entries {
		if !shape.matches(entry) {
			kept = append(kept, entry)
			continue
		}
		changed = true
		if stripped := shape.strip(entry); stripped != nil {
			kept = append(kept, stripped)
		}
	}
	if !changed {
		return false
	}
	hooks := settings["hooks"].(map[string]any)
	if len(kept) == 0 {
		delete(hooks, shape.eventKey)
	} else {
		hooks[shape.eventKey] = kept
	}
	if len(hooks) == 0 {
		delete(settings, "hooks")
	}
	return true
}

func hasHookMatch(shape hookShape, settings map[string]any) bool {
	for _, entry := range hookEventEntries(shape.eventKey, settings) {
		if shape.matches(entry) {
			return true
		}
	}
	return false
}

func hookEventEntries(eventKey string, settings map[string]any) []any {
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		return nil
	}
	entries, _ := hooks[eventKey].([]any)
	return entries
}

// hookEntryDetails is the event name and command to report for the agent's
// hook; cursor's differs in both.
func hookEntryDetails(agent HookAgent) (event, command string) {
	if agent == HookAgentCursor {
		return cursorHookEvent, cursorHookCommand
	}
	return hookEvent, primeHookCommand
}

// hooksInstallOpencode writes the auto-discovered opencode plugin
// .opencode/plugins/hew-prime.js. Unlike the JSON settings files, the whole
// file is hew's: installing is idempotent by the hew marker, a file without
// it — a user's own plugin of the same name — is refused, never clobbered,
// and a hew-managed file from an older release is refreshed in place so a
// plugin fix ships on the next `hooks install` rather than needing a manual
// remove/install round trip.
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
		if bytes.Equal(existing, opencodePlugin) {
			return a.emitResult(map[string]any{"agent": HookAgentOpencode, "installed": false, "path": file.path}, func() {
				a.printf("opencode plugin already installed in %s\n", file.path)
			})
		}
		if err := file.writeRaw(opencodePlugin); err != nil {
			return err
		}
		return a.emitResult(map[string]any{"agent": HookAgentOpencode, "installed": true, "path": file.path}, func() {
			a.printf("updated opencode plugin in %s\n", file.path)
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
	case HookAgentCursor:
		return []string{".cursor", "hooks.json"}
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
	// HTML escaping off: the guarded command carries `>` and `&`, which the
	// default encoder would write as \u003e and \u0026 — valid, but unreadable
	// in a checked-in file and a rewrite of any such text already in it.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(settings); err != nil {
		return err
	}
	return pathErr("writing", h.path, h.root.WriteFile(h.rel, buf.Bytes(), 0o644))
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

// hookShape describes one agent's on-disk session-start hook entry format,
// so addHookEntry/removeHookEntry above run once for every shape instead of
// each shape carrying its own copy of the traversal and pruning logic.
type hookShape struct {
	eventKey string
	// ensureDefaults sets schema fields a shape requires before its first
	// hook is written (nil if the format has none).
	ensureDefaults func(settings map[string]any)
	// newEntry builds the entry install appends.
	newEntry func() any
	// matches reports whether a top-level entry runs hew prime, however
	// the user phrased it.
	matches func(entry any) bool
	// upgrade rewrites the legacy bare command in entry to the guarded one,
	// reporting whether it changed anything (nil if the format has nothing to
	// upgrade). Called only when matches(entry) is true.
	upgrade func(entry any) bool
	// current reports whether entry's prime hook is the command hew installs,
	// as opposed to one a user phrased by hand. Called only when
	// matches(entry) is true, after upgrade.
	current func(entry any) bool
	// strip returns entry with its hew-prime hook(s) removed, or nil if
	// nothing should remain and the whole entry should be dropped. Called
	// only when matches(entry) is true.
	strip func(entry any) any
}

func hookShapeFor(agent HookAgent) hookShape {
	switch agent {
	case HookAgentCursor:
		return cursorHookShape
	case HookAgentCodex:
		return nestedHookShapeWith(primeHookCommandWindows)
	}
	return nestedHookShapeWith("")
}

// nestedHookShapeWith is claude's and codex's shared SessionStart shape:
// entries are {"hooks": [{"type": "command", "command": ...}, ...]} objects,
// so a single entry can carry hooks besides ours that a removal must preserve.
// A non-empty windowsCommand is written as the hook's commandWindows override.
func nestedHookShapeWith(windowsCommand string) hookShape {
	return hookShape{
		eventKey: hookEvent,
		newEntry: func() any {
			hook := map[string]any{"type": "command", "command": primeHookCommand}
			if windowsCommand != "" {
				hook["commandWindows"] = windowsCommand
			}
			return map[string]any{"hooks": []any{hook}}
		},
		matches: nestedHookMatches,
		upgrade: func(entry any) bool { return nestedHookUpgrade(entry, windowsCommand) },
		current: nestedHookCurrent,
		strip:   nestedHookStrip,
	}
}

func nestedHookMatches(entry any) bool {
	m, _ := entry.(map[string]any)
	inner, _ := m["hooks"].([]any)
	for _, h := range inner {
		hm, _ := h.(map[string]any)
		if cmd, _ := hm["command"].(string); strings.Contains(cmd, barePrimeCommand) {
			return true
		}
	}
	return false
}

// nestedHookUpgrade brings hew's own earlier phrasings — the bare command, or
// the guarded one without the Windows override — up to the current form.
func nestedHookUpgrade(entry any, windowsCommand string) bool {
	m, _ := entry.(map[string]any)
	inner, _ := m["hooks"].([]any)
	upgraded := false
	for _, h := range inner {
		hm, _ := h.(map[string]any)
		if hm["command"] != barePrimeCommand && hm["command"] != primeHookCommand {
			continue
		}
		if hm["command"] != primeHookCommand {
			hm["command"] = primeHookCommand
			upgraded = true
		}
		if windowsCommand != "" && hm["commandWindows"] != windowsCommand {
			hm["commandWindows"] = windowsCommand
			upgraded = true
		}
	}
	return upgraded
}

// nestedHookCurrent is true when every prime hook in entry runs hew's own
// command.
func nestedHookCurrent(entry any) bool {
	m, _ := entry.(map[string]any)
	inner, _ := m["hooks"].([]any)
	for _, h := range inner {
		hm, _ := h.(map[string]any)
		cmd, _ := hm["command"].(string)
		if strings.Contains(cmd, barePrimeCommand) && cmd != primeHookCommand {
			return false
		}
	}
	return true
}

func nestedHookStrip(entry any) any {
	m := entry.(map[string]any)
	inner, _ := m["hooks"].([]any)
	var kept []any
	for _, h := range inner {
		hm, _ := h.(map[string]any)
		if cmd, _ := hm["command"].(string); strings.Contains(cmd, barePrimeCommand) {
			continue
		}
		kept = append(kept, h)
	}
	if len(kept) == 0 {
		return nil // entry existed only for our hook
	}
	m["hooks"] = kept
	return m
}

// Cursor's hooks.json shape differs from claude's and codex's on every axis:
// the event key is sessionStart (leading lowercase), entries are flat
// {"command": ...} objects rather than the nested {"hooks": [...]} form, and
// — the real incompatibility — stdout must be valid JSON with the context in
// additional_context (snake_case; Cursor ignores Claude Code's camelCase
// additionalContext). A bare `hew prime` text output fails Cursor's JSON
// parse and the primer never lands, so the hook runs the built-in JSON
// emission path instead of a shell wrapper around python3 or jq. Cursor also
// writes the hook payload on stdin; prime never reads stdin, so that JSON
// cannot leak into the primer. sessionStart is fire-and-forget, so a timeout
// bounds a stuck prime run. The command is deliberately unguarded: Cursor has
// no Windows-only command override, and on Windows its launcher runs the
// command through PowerShell (or bash under Git Bash), so no one-line POSIX
// guard works there. A contributor without hew sees a hook error instead.
const (
	cursorHookEvent   = "sessionStart"
	cursorHookCommand = "hew prime --hook-format cursor"
	cursorHookTimeout = 30
)

// cursorHookShape is Cursor's flat SessionStart shape: an entry is the hook,
// so a match always drops the whole entry rather than pruning inside it.
var cursorHookShape = hookShape{
	eventKey: cursorHookEvent,
	ensureDefaults: func(settings map[string]any) {
		if _, ok := settings["version"]; !ok {
			settings["version"] = 1 // the schema default; an explicit value is kept
		}
	},
	newEntry: func() any {
		return map[string]any{
			"command": cursorHookCommand,
			"timeout": cursorHookTimeout,
		}
	},
	matches: func(entry any) bool {
		m, _ := entry.(map[string]any)
		cmd, _ := m["command"].(string)
		return strings.Contains(cmd, barePrimeCommand)
	},
	current: func(entry any) bool {
		m, _ := entry.(map[string]any)
		return m["command"] == cursorHookCommand
	},
	strip: func(entry any) any { return nil },
}
