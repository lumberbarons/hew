package tracker

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path/filepath"

	"github.com/lumberbarons/hew/internal/conventions"
	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/git"
	"github.com/lumberbarons/hew/internal/local"
)

// InitConfig configures Init. Like Config it names the worktree and the target
// explicitly: initialization never reads the process working directory or its
// environment.
type InitConfig struct {
	// Worktree is the directory the caller works from. Local initialization
	// creates the tracker at the main checkout containing it, so every linked
	// worktree of the repository shares the one tracker.
	Worktree string
	// Backend optionally pins the backend, with Config.Backend's meaning:
	// empty detects it from the worktree, BackendLocal creates or keeps the
	// local tracker, and BackendGitHub bootstraps GitHub even when the repo
	// tracks issues locally.
	Backend Backend
	// Repo is the GitHub repository as owner/name. Empty means detect it from
	// the worktree's origin remote. Ignored by a local tracker.
	Repo string
	// Transport overrides the HTTP transport the GitHub client sends through,
	// so a caller's tests need neither the network nor a gh process. Nil uses
	// go-gh's default. Ignored by a local tracker.
	Transport http.RoundTripper
}

// InitResult reports what Init found and changed, so a caller can render it
// without asking the filesystem or the API again.
type InitResult struct {
	// Backend is the backend initialization ran against.
	Backend Backend `json:"backend"`
	// Path is the local tracker directory (the .hew directory). Empty on
	// GitHub, which keeps no state in the checkout.
	Path string `json:"path"`
	// CreatedLabels names the convention labels the run created, in the
	// conventions' order. It is non-nil and empty when none were missing, and
	// always empty on the local backend, which carries its labels in the
	// tracker itself.
	CreatedLabels []string `json:"createdLabels"`
	// IssuesRecreated reports a local tracker whose issues.jsonl was missing
	// beside an existing config. The file is empty now and the tracker has a
	// new id, so resume checkpoints bound to the old tracker start over.
	IssuesRecreated bool `json:"issuesRecreated"`
	// Notices are the warnings the run would have printed, in the order they
	// happened. They are data because the library never writes to stdout or
	// stderr; a caller renders or discards them.
	Notices []string `json:"notices"`
}

// Init bootstraps the tracker for the checkout containing cfg.Worktree, or for
// cfg.Repo, without going through the hew executable: on the local backend it
// creates .hew at the main checkout, and on GitHub it creates the convention
// labels the write path assumes.
//
// It is idempotent. A local tracker keeps its issues, its configuration and its
// id across runs, and recreating a lost issues.jsonl is reported rather than
// hidden; a GitHub repository that already carries every convention label is
// left untouched.
//
// A repository whose tracker belongs to another backend is refused with
// InitializedElsewhereError instead of being initialized locally, so a migrated
// repo's archive is never made live again. The other refusals are typed as the
// contract promises: InvalidInputError names a bad worktree, backend or
// repository, and an unusable GitHub credential is AuthError. Init never prints
// to stdout or stderr; the notices it would have printed come back in
// InitResult.Notices.
func Init(ctx context.Context, cfg InitConfig) (InitResult, error) {
	switch cfg.Backend {
	case "", BackendLocal, BackendGitHub:
	default:
		return InitResult{}, &InvalidInputError{Field: "backend", Reason: fmt.Sprintf("unknown backend %q", cfg.Backend)}
	}
	if cfg.Worktree == "" {
		return InitResult{}, &InvalidInputError{Field: "worktree", Reason: "must name the directory to initialize"}
	}
	backend := cfg.Backend
	if backend == "" {
		// The backend selection every command uses: a local tracker already
		// chosen by the repo is reported and left alone, and everything else
		// means GitHub — including a repo that never opted in.
		target, err := Detect(cfg.Worktree)
		if err != nil {
			return InitResult{}, err
		}
		backend = target.Backend
	}
	if backend == BackendLocal {
		return initLocal(cfg)
	}
	// Open resolves the repository, builds the client and turns an unusable
	// credential into AuthError, so init shares one GitHub setup with every
	// other operation.
	tr, err := Open(ctx, Config{
		Worktree:  cfg.Worktree,
		Backend:   BackendGitHub,
		Repo:      cfg.Repo,
		Transport: cfg.Transport,
	})
	if err != nil {
		return InitResult{}, err
	}
	return tr.Init(ctx)
}

// Init makes sure an open tracker has the backend furniture its workflows
// assume. On GitHub that is the convention labels: every missing one is
// created, and the ones already present are left alone, so a second call
// changes nothing. The set is conventions.Labels, which model.LabelVocabulary
// derives from the type vocabulary, so a newly recognized type such as idea is
// bootstrapped by the same call. The local backend carries its labels in the
// tracker itself, so a local tracker is already initialized and this reports
// it unchanged.
//
// A create that fails returns the labels created so far in CreatedLabels; a
// rerun picks up the ones still missing, because the bootstrap is idempotent.
// An authentication failure is AuthError.
func (t *Tracker) Init(ctx context.Context) (InitResult, error) {
	res := InitResult{
		Backend:       t.backend,
		CreatedLabels: []string{},
		Notices:       []string{},
	}
	if t.backend == BackendLocal {
		return res, nil
	}
	existing, err := t.store.ListLabels(ctx)
	if err != nil {
		return InitResult{}, asWriteError(err)
	}
	have := make(map[string]bool, len(existing))
	for _, label := range existing {
		have[label.Name] = true
	}
	for _, label := range conventions.Labels {
		if have[label.Name] {
			continue
		}
		err := t.store.CreateLabel(ctx, gh.Label{Name: label.Name, Color: label.Color, Description: label.Description})
		if err != nil {
			return res, fmt.Errorf("creating label %q: %w", label.Name, asWriteError(err))
		}
		res.CreatedLabels = append(res.CreatedLabels, label.Name)
	}
	return res, nil
}

// initLocal creates the local tracker at the worktree's main checkout. It reads
// the existing config before writing anything, so a repo that moved to another
// backend is refused without touching its archive, and a config it cannot read
// is reported with its own recovery text rather than overwritten.
func initLocal(cfg InitConfig) (InitResult, error) {
	dir, err := local.MainDir(cfg.Worktree)
	if err != nil {
		if git.IsNotRepo(err) {
			return InitResult{}, &InvalidInputError{
				Field:  "worktree",
				Reason: fmt.Sprintf("the local tracker needs a git checkout (run `git init` first): %v", err),
			}
		}
		return InitResult{}, err
	}
	res := InitResult{
		Backend:       BackendLocal,
		Path:          dir,
		CreatedLabels: []string{},
		Notices:       []string{},
	}
	// Read before writing: a repo that moved to another backend must be refused
	// without touching its archived tracker.
	fresh := false
	switch cfgRead, readErr := local.ReadConfig(dir); {
	case readErr == nil && cfgRead.Backend != local.BackendLocal:
		return InitResult{}, &InitializedElsewhereError{Path: dir, Backend: Backend(cfgRead.Backend)}
	case errors.Is(readErr, fs.ErrNotExist):
		fresh = true
	case readErr != nil:
		return InitResult{}, readErr
	}
	created, err := local.InitTracker(dir)
	if err != nil {
		return InitResult{}, fmt.Errorf("creating %s: %w", dir, err)
	}
	res.IssuesRecreated = created.IssuesRecreated
	if created.IssuesRecreated {
		res.Notices = append(res.Notices, local.RecreatedIssuesNotice(filepath.Join(dir, local.IssuesFileName)))
	}
	if fresh {
		res.Notices = append(res.Notices, local.UntrackedNotice(dir))
	}
	return res, nil
}
