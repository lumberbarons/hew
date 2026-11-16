package migrate

import (
	"context"
	"fmt"
	"strings"

	"github.com/lumberbarons/hew/internal/conventions"
	"github.com/lumberbarons/hew/internal/gh"
)

// Label is one label a migration may need to create before it files issues, in
// the backend-neutral shape a Target that can bootstrap labels understands.
type Label struct {
	Name        string
	Color       string
	Description string
}

// Labeler is the optional half of a Target that can ensure labels exist.
//
// The writes the engine needs are the Target interface; a workflow's creates
// also reference convention and source labels a repository may not have yet,
// and creating them is a backend concern rather than an item's. A Target that
// implements Labeler has them bootstrapped once, after the checkpoint and
// provenance checks pass and before the first create; one that does not leaves
// label management to its own backend, which is what a consumer's in-memory
// fake usually wants.
type Labeler interface {
	// ListLabels returns the labels the target already has.
	ListLabels(ctx context.Context) ([]Label, error)
	// CreateLabel creates one missing label. Creating one that already exists
	// under another case is the caller's to avoid.
	CreateLabel(ctx context.Context, label Label) error
}

// ensureLabels creates every missing convention label plus the extras a source
// contributes. Names are matched case-insensitively, because GitHub treats
// them that way.
func ensureLabels(ctx context.Context, target Labeler, extras []Label) error {
	existing, err := target.ListLabels(ctx)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, l := range existing {
		have[strings.ToLower(l.Name)] = true
	}
	want := make([]Label, 0, len(conventions.Labels)+len(extras))
	for _, l := range conventions.Labels {
		want = append(want, Label{Name: l.Name, Color: l.Color, Description: l.Description})
	}
	want = append(want, extras...)
	for _, l := range want {
		if have[strings.ToLower(l.Name)] {
			continue
		}
		if err := target.CreateLabel(ctx, l); err != nil {
			return fmt.Errorf("creating label %q: %w", l.Name, err)
		}
		have[strings.ToLower(l.Name)] = true
	}
	return nil
}

// storeTarget adapts a gh.IssueStore to the engine's narrow Target: the store
// names no internal type in a public signature, and this adapter is what Open
// writes through.
type storeTarget struct{ client gh.IssueStore }

func (t storeTarget) Create(ctx context.Context, req CreateRequest) (int, error) {
	issue, err := t.client.CreateIssue(ctx, req.Title, req.Body, req.Labels)
	if err != nil {
		return 0, err
	}
	return issue.Number, nil
}

func (t storeTarget) Get(ctx context.Context, number int) (Issue, error) {
	issue, err := t.client.GetIssue(ctx, number)
	if err != nil {
		return Issue{}, err
	}
	return Issue{Number: issue.Number, Title: issue.Title, Body: issue.Body, State: issue.State}, nil
}

func (t storeTarget) EditTitle(ctx context.Context, number int, title string) error {
	return t.client.EditTitle(ctx, number, title)
}

func (t storeTarget) EditBody(ctx context.Context, number int, body string) error {
	return t.client.EditBody(ctx, number, body)
}

func (t storeTarget) Comment(ctx context.Context, number int, body string) error {
	return t.client.Comment(ctx, number, body)
}

func (t storeTarget) Close(ctx context.Context, number int, reason CloseReason) error {
	return t.client.CloseIssue(ctx, number, closeReason(reason))
}

func (t storeTarget) AddSubIssue(ctx context.Context, parent, child int) error {
	return t.client.AddSubIssue(ctx, parent, child, true)
}

func (t storeTarget) AddBlockedBy(ctx context.Context, issue, blocker int) error {
	return t.client.AddBlockedBy(ctx, issue, blocker)
}

func (t storeTarget) AddAssignee(ctx context.Context, number int, login string) error {
	return t.client.AddAssignee(ctx, number, login)
}

func (t storeTarget) Viewer(ctx context.Context) (string, error) {
	return t.client.Viewer(ctx)
}

func (t storeTarget) ListLabels(ctx context.Context) ([]Label, error) {
	labels, err := t.client.ListLabels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Label, len(labels))
	for i, l := range labels {
		out[i] = Label{Name: l.Name, Color: l.Color, Description: l.Description}
	}
	return out, nil
}

func (t storeTarget) CreateLabel(ctx context.Context, label Label) error {
	return t.client.CreateLabel(ctx, gh.Label{Name: label.Name, Color: label.Color, Description: label.Description})
}

// closeReason maps the engine's backend-neutral reason onto the store's.
func closeReason(reason CloseReason) gh.CloseReason {
	switch reason {
	case CloseNotPlanned:
		return gh.CloseNotPlanned
	case CloseDuplicate:
		return gh.CloseDuplicate
	default:
		return gh.CloseCompleted
	}
}

// labeler returns the target's optional label half.
func (m *Migrator) labeler() (Labeler, bool) {
	l, ok := m.target.(Labeler)
	return l, ok
}
