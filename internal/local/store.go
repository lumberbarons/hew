// Package local is the JSONL-backed issue store for a repo that tracks work
// locally instead of in GitHub Issues. It implements gh.IssueStore, so every
// command runs against it unchanged.
//
// The store is a directory (.hew at the main checkout) holding one
// issues.jsonl. Every mutation takes an exclusive lock, rewrites the file to a
// temp name and renames it into place, so concurrent agents in different
// worktrees never lose each other's writes and a reader never sees a torn
// file. Reads take no lock for the same reason.
package local

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/git"
)

// DirName is the store directory, created at the root of the main checkout.
const DirName = ".hew"

const (
	configFile = "config.json"
	issuesFile = "issues.jsonl"
	lockFile   = "lock"
	// ignoreFile makes the directory ignore itself, so a local tracker never
	// shows up in git status and the repo's own .gitignore stays untouched.
	ignoreFile = ".gitignore"

	// formatVersion is bumped when the record schema changes incompatibly.
	formatVersion = 1
)

// Backend names recorded in config.json.
const (
	BackendLocal  = "local"
	BackendGitHub = "github"
)

// Config says which backend owns a repo. It is the switch the commands read:
// migrating to GitHub flips it, leaving the local issues behind as an archive.
type Config struct {
	Backend string `json:"backend"`
	Version int    `json:"version"`
}

// ReadConfig reads dir's config. A missing file satisfies errors.Is(err,
// fs.ErrNotExist), which callers treat as "this repo is not local".
func ReadConfig(dir string) (Config, error) {
	var cfg Config
	raw, err := os.ReadFile(filepath.Join(dir, configFile))
	if err != nil {
		return cfg, err
	}
	if err := strictUnmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", filepath.Join(dir, configFile), err)
	}
	if cfg.Version > formatVersion {
		return cfg, fmt.Errorf("%s was written by a newer hew (format %d, this build reads %d)",
			filepath.Join(dir, configFile), cfg.Version, formatVersion)
	}
	return cfg, nil
}

// WriteConfig replaces dir's config.
func WriteConfig(dir string, cfg Config) error {
	cfg.Version = formatVersion
	raw, err := marshalLine(cfg)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, configFile), raw)
}

// Init creates a local tracker in dir, leaving anything already there alone so
// that running it twice is harmless. It never overwrites issues.
func Init(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, content := range map[string]func() ([]byte, error){
		ignoreFile: func() ([]byte, error) { return []byte("*\n"), nil },
		configFile: func() ([]byte, error) { return marshalLine(Config{Backend: BackendLocal, Version: formatVersion}) },
		issuesFile: func() ([]byte, error) { return (&data{}).encode() },
	} {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		raw, err := content()
		if err != nil {
			return err
		}
		if err := writeFileAtomic(path, raw); err != nil {
			return err
		}
	}
	return nil
}

// DefaultViewer is who the local tracker says this agent is. HEW_USER wins
// when set: it is the escape hatch for a harness, and it must be unique per
// concurrent agent, because a shared identity cannot tell "this claim is
// mine" from "a sibling session holds it". Otherwise the OS login is
// qualified with the current linked worktree, so two agents under one OS
// account in different worktrees are distinct claimants and start's exit codes
// stay meaningful; the main checkout keeps the bare login. It is login-shaped
// on purpose — it lands in assignee lists and "@user" output, where a git
// display name with spaces would read badly.
//
// It fails when the checkout cannot be determined rather than falling back to
// the bare login: the identity would then change between invocations, and a
// claim of your own would read as someone else's.
func DefaultViewer() (string, error) {
	return viewerFor(os.Getenv, func() (string, error) { return git.LinkedWorktree("") })
}

// viewerFor is the identity rule with the environment and checkout injected,
// so its fallbacks and qualification are testable without either. worktree
// returns the linked worktree's root path, empty for the main checkout; it is
// only consulted when HEW_USER does not decide the answer.
func viewerFor(getenv func(string) string, worktree func() (string, error)) (string, error) {
	if v := getenv("HEW_USER"); v != "" {
		return v, nil
	}
	login := getenv("USER")
	if login == "" {
		login = "local"
	}
	root, err := worktree()
	if err != nil {
		return "", fmt.Errorf("cannot tell which agent this is (set HEW_USER to name it): %w", err)
	}
	if root == "" {
		return login, nil
	}
	return login + "-" + worktreeTag(root), nil
}

// worktreeTag names a linked worktree for an assignee handle: its directory
// name, for readability, plus a short digest of the full path, because names
// repeat across parents (/a/work, /b/work) and collapse once sanitised
// (feature/one, feature-one). The digest keeps those distinct and the result
// stable for as long as the worktree stays where it is.
func worktreeTag(root string) string {
	sum := sha256.Sum256([]byte(root))
	tag := hex.EncodeToString(sum[:3])
	if name := identityPart(filepath.Base(root)); name != "" {
		return name + "-" + tag
	}
	return tag
}

// identityPart reduces a worktree directory name to the characters an
// assignee handle can carry: a path may contain spaces or punctuation that
// would read as several names in "@user" output.
func identityPart(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// Store is an open local tracker.
type Store struct {
	dir    string
	viewer string
	now    func() time.Time
}

var (
	_ gh.IssueStore = (*Store)(nil)
	_ gh.Claimer    = (*Store)(nil)
)

// Open opens the tracker in dir as viewer. It refuses a directory whose config
// no longer says local: after migration the issues are an archive, and writing
// to them would quietly fork the tracker from GitHub.
func Open(dir, viewer string) (*Store, error) {
	cfg, err := ReadConfig(dir)
	if err != nil {
		return nil, err
	}
	if cfg.Backend != BackendLocal {
		return nil, fmt.Errorf("%s tracks issues in %q, not locally", dir, cfg.Backend)
	}
	if _, err := os.Stat(filepath.Join(dir, issuesFile)); err != nil {
		return nil, err
	}
	if viewer == "" {
		if viewer, err = DefaultViewer(); err != nil {
			return nil, err
		}
	}
	return &Store{dir: dir, viewer: viewer, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (s *Store) path() string { return filepath.Join(s.dir, issuesFile) }

// load reads the current file. It takes no lock: writers replace the file by
// rename, so a reader sees the old whole or the new whole, never a mixture.
func (s *Store) load() (*data, error) { return readData(s.path()) }

// update runs fn against the latest data under an exclusive lock and writes
// the result back, unless fn fails — a refused change leaves the file as it was.
func (s *Store) update(fn func(d *data) error) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	d, err := s.load()
	if err != nil {
		return err
	}
	if err := fn(d); err != nil {
		return err
	}
	raw, err := d.encode()
	if err != nil {
		return err
	}
	return writeFileAtomic(s.path(), raw)
}

// lock takes the exclusive lock on the store. flock is per open file
// description, so it serialises goroutines in one process as well as separate
// processes; closing the file releases it, including if the process dies.
func (s *Store) lock() (unlock func(), err error) {
	f, err := os.OpenFile(filepath.Join(s.dir, lockFile), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("locking %s: %w", s.dir, err)
	}
	return func() { _ = f.Close() }, nil
}

// writeFileAtomic writes via a temp file in the same directory and renames it
// over path, so the destination is never observed half-written.
func writeFileAtomic(path string, content []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
