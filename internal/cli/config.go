package cli

// BackendConfig is what a read-only `config` query reports: which backend the
// commands in this checkout use, and the identity of its target. Repo is
// owner/name on GitHub; Path and ID are the local tracker's directory and its
// stable id. Every field is carried, empty when it does not apply, so --json
// has one schema on both backends.
type BackendConfig struct {
	Backend string `json:"backend"`
	Repo    string `json:"repo"`
	Path    string `json:"path"`
	ID      string `json:"id"`
}

// Config prints the effective backend selection. Resolving it and opening a
// store or client are the caller's job: this method only renders.
func (a *App) Config(cfg BackendConfig) error {
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
