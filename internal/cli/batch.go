package cli

// Shared machinery for the batch writers (migrate, apply): the checkpoint
// schema and file I/O tracker owns, the state-file identity, and the adapter
// that lets the CLI's store drive the public migration workflows. The workflows
// bootstrap their own labels through the adapter's optional migrate.Labeler
// half.

import (
	"context"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/migrate"
	"github.com/lumberbarons/hew/tracker"
)

// batchStateVersion and batchState are the CLI's spelling of the checkpoint
// schema tracker owns. ApplyPlan lives in tracker and takes a tracker.Checkpoint;
// migrate takes migrate.Checkpoint, which tracker's file and memory checkpoints
// satisfy.
const batchStateVersion = tracker.CheckpointVersion

type batchState = tracker.CheckpointState

// fileDigest computes the sha256 digest a checkpoint binds to.
func fileDigest(data []byte) string { return tracker.FileDigest(data) }

func saveBatchState(path string, state *batchState) error {
	return tracker.SaveCheckpointFile(path, state)
}

// stateRepo is the identity a checkpoint binds to: the GitHub repository, or
// the local tracker's stable id. The local backend cannot use Repo.String():
// it is the checkout's directory name, which two checkouts can share and a
// rename changes.
func (a *App) stateRepo() string {
	if a.RepoKey != "" {
		return a.RepoKey
	}
	return a.Repo.String()
}

// migrationTarget is the CLI's migrate.Target over the store it already
// opened. It names no internal type in the engine's signature: model issues
// are converted to the public migrate.Issue at the boundary.
type migrationTarget struct{ client gh.IssueStore }

func (t migrationTarget) Create(ctx context.Context, req migrate.CreateRequest) (int, error) {
	issue, err := t.client.CreateIssue(ctx, req.Title, req.Body, req.Labels)
	if err != nil {
		return 0, err
	}
	return issue.Number, nil
}

func (t migrationTarget) Get(ctx context.Context, number int) (migrate.Issue, error) {
	issue, err := t.client.GetIssue(ctx, number)
	if err != nil {
		return migrate.Issue{}, err
	}
	return migrate.Issue{
		Number: issue.Number,
		Title:  issue.Title,
		Body:   issue.Body,
		State:  issue.State,
	}, nil
}

func (t migrationTarget) EditTitle(ctx context.Context, number int, title string) error {
	return t.client.EditTitle(ctx, number, title)
}

func (t migrationTarget) EditBody(ctx context.Context, number int, body string) error {
	return t.client.EditBody(ctx, number, body)
}

func (t migrationTarget) Comment(ctx context.Context, number int, body string) error {
	return t.client.Comment(ctx, number, body)
}

func (t migrationTarget) Close(ctx context.Context, number int, reason migrate.CloseReason) error {
	return t.client.CloseIssue(ctx, number, closeReason(reason))
}

func (t migrationTarget) AddSubIssue(ctx context.Context, parent, child int) error {
	return t.client.AddSubIssue(ctx, parent, child, true)
}

func (t migrationTarget) AddBlockedBy(ctx context.Context, issue, blocker int) error {
	return t.client.AddBlockedBy(ctx, issue, blocker)
}

func (t migrationTarget) AddAssignee(ctx context.Context, number int, login string) error {
	return t.client.AddAssignee(ctx, number, login)
}

func (t migrationTarget) Viewer(ctx context.Context) (string, error) {
	return t.client.Viewer(ctx)
}

// ListLabels and CreateLabel are the optional migrate.Labeler half: the store
// can bootstrap the labels a migration's creates reference.
func (t migrationTarget) ListLabels(ctx context.Context) ([]migrate.Label, error) {
	labels, err := t.client.ListLabels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]migrate.Label, len(labels))
	for i, l := range labels {
		out[i] = migrate.Label{Name: l.Name, Color: l.Color, Description: l.Description}
	}
	return out, nil
}

func (t migrationTarget) CreateLabel(ctx context.Context, label migrate.Label) error {
	return t.client.CreateLabel(ctx, gh.Label{Name: label.Name, Color: label.Color, Description: label.Description})
}

// closeReason maps the engine's backend-neutral reason onto the store's.
func closeReason(reason migrate.CloseReason) gh.CloseReason {
	switch reason {
	case migrate.CloseNotPlanned:
		return gh.CloseNotPlanned
	case migrate.CloseDuplicate:
		return gh.CloseDuplicate
	default:
		return gh.CloseCompleted
	}
}

// warningTexts renders the engine's typed warnings for --json, which reports
// the same sentences the text output prints.
func warningTexts(warnings []migrate.Warning) []string {
	out := make([]string, len(warnings))
	for i, w := range warnings {
		out[i] = w.Message
	}
	return out
}

// printWarnings writes the engine's warnings to stderr, the stream the batch
// commands have always narrated warnings on.
func (a *App) printWarnings(warnings []migrate.Warning) {
	for _, w := range warnings {
		a.warnf("%s", w.Message)
	}
}
