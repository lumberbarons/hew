// Package local is the JSONL-backed issue store for a repo that tracks work
// locally instead of in GitHub Issues. It implements gh.IssueStore, so every
// command runs against it unchanged.
//
// The store is a directory (.hew at the main checkout) holding one
// issues.jsonl. Every mutation takes an exclusive lock, rewrites the file to a
// temp name and renames it into place, so concurrent agents in different
// worktrees never lose each other's writes and a reader never sees a torn
// file. Reads take no lock for the same reason. A writer waits for the lock
// only until its context is done or a timeout passes, and opening the store
// sweeps up temp files a killed writer left behind.
//
// The directory ignores itself, so git treats it as untracked-and-ignored:
// `git clean -fdx` at the main checkout deletes the only copy of the tracker.
package local

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	// ID is the tracker's stable identity, generated once when the tracker is
	// created. Resume checkpoints bind to it rather than the checkout's
	// directory name: two checkouts can share a name, and a rename must not
	// invalidate a checkpoint kept outside the tracker.
	ID string `json:"id,omitempty"`

	// extra holds the fields this build does not know. A newer hew may add
	// them without bumping the format version, so they are read past and
	// written back rather than refused, as the records in issues.jsonl are.
	extra extras
}

// UnmarshalJSON keeps the fields this build does not know, which the typed
// decode would otherwise drop.
func (c *Config) UnmarshalJSON(raw []byte) error {
	type plain Config
	var p plain
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	x, err := unknownFields(raw, p)
	if err != nil {
		return err
	}
	p.extra = x
	*c = Config(p)
	return nil
}

// MarshalJSON writes the fields UnmarshalJSON carried past, after the known ones.
func (c Config) MarshalJSON() ([]byte, error) {
	type plain Config
	return marshalWithExtras(plain(c), c.extra)
}

// ReadConfig reads dir's config. A missing file satisfies errors.Is(err,
// fs.ErrNotExist), which callers treat as "this repo is not local".
func ReadConfig(dir string) (Config, error) {
	var cfg Config
	path := filepath.Join(dir, configFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	// The version is read on its own first: a newer hew may change the shape
	// of other fields, and that must surface as "upgrade", not a type error.
	var head struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	if head.Version > formatVersion {
		cfg.Version = head.Version
		return cfg, fmt.Errorf("%s was written by a newer hew (format %d, this build reads %d)",
			path, head.Version, formatVersion)
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// newID returns a fresh tracker identity: random bytes, hex, so it names one
// tracker without revealing anything about where it lives.
func newID() (string, error) {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return "", fmt.Errorf("creating a tracker id: %w", err)
	}
	return hex.EncodeToString(id), nil
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
	// A fixed order, config last: config.json is what selects the local backend,
	// so a concurrent Locate that sees issues.jsonl without it reads a tracker
	// still being created as one that lost its config.
	for _, f := range []struct {
		name    string
		content func() ([]byte, error)
	}{
		{ignoreFile, func() ([]byte, error) { return []byte("*\n"), nil }},
		{issuesFile, func() ([]byte, error) { return (&data{}).encode() }},
		{configFile, func() ([]byte, error) {
			id, err := newID()
			if err != nil {
				return nil, err
			}
			return marshalLine(Config{Backend: BackendLocal, Version: formatVersion, ID: id})
		}},
	} {
		path := filepath.Join(dir, f.name)
		if _, err := os.Stat(path); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		raw, err := f.content()
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
	id     string
	now    func() time.Time

	// lockTimeout overrides how long a writer waits for the lock; zero means
	// defaultLockTimeout.
	lockTimeout time.Duration

	// notice receives the one-line report that a migration is holding the lock;
	// nil means stderr. noticeAfter is how long a writer waits before looking
	// for one; zero means defaultNoticeAfter. noticeRecheck is how often a
	// waiter that found none looks again; zero means defaultNoticeRecheck.
	notice        io.Writer
	noticeAfter   time.Duration
	noticeRecheck time.Duration
}

var (
	_ gh.IssueStore = (*Store)(nil)
	_ gh.Claimer    = (*Store)(nil)
)

// Open opens the tracker in dir as viewer. It refuses a directory whose config
// no longer says local: after migration the issues are an archive, and writing
// to them would quietly fork the tracker from GitHub.
func Open(dir, viewer string) (*Store, error) {
	cfg, err := readLocalConfig(dir)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(dir, issuesFile)); err != nil {
		return nil, err
	}
	if viewer == "" {
		var err error
		if viewer, err = DefaultViewer(); err != nil {
			return nil, err
		}
	}
	removeStaleTempFiles(dir)
	s := &Store{dir: dir, viewer: viewer, id: cfg.ID, now: func() time.Time { return time.Now().UTC() }}
	if s.id == "" {
		if err := s.backfillID(context.Background()); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// ID is the tracker's stable identity, the same from every checkout and
// across a rename of the directory holding it.
func (s *Store) ID() string { return s.id }

// backfillID gives a tracker created before ids existed one. It runs under the
// lock and re-reads the config, so concurrent opens settle on whichever id
// landed first instead of each inventing their own.
func (s *Store) backfillID(ctx context.Context) error {
	unlock, err := s.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	cfg, err := readLocalConfig(s.dir)
	if err != nil {
		return err
	}
	if cfg.ID == "" {
		if cfg.ID, err = newID(); err != nil {
			return err
		}
		if err := WriteConfig(s.dir, cfg); err != nil {
			return err
		}
	}
	s.id = cfg.ID
	return nil
}

func (s *Store) path() string { return filepath.Join(s.dir, issuesFile) }

// load reads the current file. It takes no lock: writers replace the file by
// rename, so a reader sees the old whole or the new whole, never a mixture.
func (s *Store) load() (*data, error) { return readData(s.path()) }

// update runs fn against the latest data under an exclusive lock and writes
// the result back, unless fn fails — a refused change leaves the file as it was.
func (s *Store) update(ctx context.Context, fn func(d *data) error) error {
	unlock, err := s.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	// Checked under the lock, after Open: a store opened before a migration
	// must not write to what has since become an archive.
	if err := requireLocal(s.dir); err != nil {
		return err
	}
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

// Writers wait for the lock by polling, because a blocking flock cannot be
// abandoned when ctx is cancelled. Holders keep it for one small file rewrite,
// so the poll is short and the timeout generous: reaching it means the holder
// is hung, not busy.
const (
	defaultLockTimeout = 15 * time.Second
	lockPollInterval   = 5 * time.Millisecond

	// defaultNoticeAfter is how long a writer waits before it checks whether the
	// holder is a migration. An ordinary holder lets go within milliseconds, so
	// a writer that is still waiting this long is behind something slower.
	defaultNoticeAfter = 500 * time.Millisecond

	// defaultNoticeRecheck is how often a waiter that found no migration looks again.
	defaultNoticeRecheck = time.Second
)

// lock takes the exclusive lock on the store, waiting until it is free, ctx is
// done or the timeout passes. flock is per open file description, so it
// serialises goroutines in one process as well as separate processes; closing
// the file releases it, including if the process dies.
//
// A writer that has waited noticeAfter and finds a migration marker says so on
// stderr, once, so a hung-looking command explains itself. The marker is only
// believed while the lock is held by someone else: a migration that died took
// its lock with it but could not remove the marker, so whoever next gets the
// lock clears it.
func (s *Store) lock(ctx context.Context) (unlock func(), err error) {
	path := filepath.Join(s.dir, lockFile)
	f, err := openLockFile(path)
	if err != nil {
		return nil, err
	}
	timeout := s.lockTimeout
	if timeout <= 0 {
		timeout = defaultLockTimeout
	}
	noticeAfter := s.noticeAfter
	if noticeAfter <= 0 {
		noticeAfter = defaultNoticeAfter
	}
	noticeRecheck := s.noticeRecheck
	if noticeRecheck <= 0 {
		noticeRecheck = defaultNoticeRecheck
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	waitStart := time.Now()
	noticed := false
	var lastCheck time.Time
	for {
		if err := waitCtx.Err(); err != nil {
			_ = f.Close()
			if ctx.Err() == nil {
				if m, ok := readMigrationMarker(s.dir); ok {
					return nil, fmt.Errorf("timed out after %s waiting for the lock %s: %s; retry once it finishes", timeout, path, m.describe())
				}
				return nil, fmt.Errorf("timed out after %s waiting for the lock %s: another hew process is holding it (a `hew migrate local` holds it for the whole migration; if none is running, `lsof %s` names the holder)", timeout, path, path)
			}
			return nil, fmt.Errorf("waiting for the lock %s: %w", path, err)
		}
		held, err := tryFlock(f)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("locking %s: %w", path, err)
		}
		if held {
			removeMigrationMarker(s.dir)
			return func() { _ = f.Close() }, nil
		}
		if !noticed && time.Since(waitStart) >= noticeAfter && time.Since(lastCheck) >= noticeRecheck {
			lastCheck = time.Now()
			if m, ok := readMigrationMarker(s.dir); ok {
				noticed = true
				w := s.notice
				if w == nil {
					w = os.Stderr
				}
				_, _ = fmt.Fprintf(w, "hew: %s; waiting up to %s more for it to finish\n", m.describe(), (timeout - time.Since(waitStart)).Round(time.Millisecond))
			}
		}
		select {
		case <-waitCtx.Done():
		case <-time.After(lockPollInterval):
		}
	}
}

func openLockFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
}

// tryFlock takes the exclusive lock without blocking and reports whether it
// got it. Contention is not an error.
func tryFlock(f *os.File) (bool, error) {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, syscall.EINTR):
		case errors.Is(err, syscall.EWOULDBLOCK):
			return false, nil
		default:
			return false, err
		}
	}
}

// staleTempAge is how old a temp file must be before cleanup touches it. Update
// holds the lock while it writes one, but Init writes issues.jsonl without it,
// so the lock alone cannot tell a live temp file from an abandoned one; no write
// takes anywhere near this long.
const staleTempAge = time.Minute

// removeStaleTempFiles deletes the issues.jsonl.tmp-* files a writer killed
// between creating its temp file and renaming it leaves behind. Only old files
// go, and only when the lock is free: it is taken without waiting, and if
// someone holds it their temp file is live and stays. Cleanup is best effort —
// a read-only checkout simply keeps its leftovers.
func removeStaleTempFiles(dir string) {
	stale, err := filepath.Glob(filepath.Join(dir, issuesFile+".tmp-*"))
	if err != nil || len(stale) == 0 {
		return
	}
	f, err := openLockFile(filepath.Join(dir, lockFile))
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	if held, err := tryFlock(f); err != nil || !held {
		return
	}
	for _, p := range stale {
		if info, err := os.Stat(p); err == nil && time.Since(info.ModTime()) > staleTempAge {
			_ = os.Remove(p)
		}
	}
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
