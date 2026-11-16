package cli

import "github.com/lumberbarons/hew/internal/local"

// ConfigTarget is a resolved backend selection in the shape the public
// tracker.Target reports: the backend, the GitHub repository, and the local
// tracker's directory and stable id. The caller resolves it; this package
// only chooses which fields to render.
type ConfigTarget struct {
	Backend string
	Repo    string
	Path    string
	ID      string
}

// backendConfig is what a read-only `config` query reports: which backend the
// commands in this checkout use, and the identity of its target. Repo is
// owner/name on GitHub; Path and ID are the local tracker's directory and its
// stable id. Every field is carried, empty when it does not apply, so --json
// has one schema on both backends.
type backendConfig struct {
	Backend string `json:"backend"`
	Repo    string `json:"repo"`
	Path    string `json:"path"`
	ID      string `json:"id"`
}

// Config prints the effective backend selection. The caller resolves the
// target and opens no store or client: this method only chooses the fields
// that backend has and renders them.
func (a *App) Config(target ConfigTarget) error {
	cfg := backendConfig(target)
	if cfg.Backend == "" {
		cfg.Backend = local.BackendGitHub
	}
	return a.emitResult(cfg, func() {
		a.printf("backend: %s\n", cfg.Backend)
		if cfg.Repo != "" {
			a.printf("repo: %s\n", cfg.Repo)
		}
		if cfg.Path != "" {
			a.printf("path: %s\n", cfg.Path)
		}
		if cfg.ID != "" {
			a.printf("id: %s\n", cfg.ID)
		}
	})
}
