package cli

// Shared machinery for the batch writers (migrate, apply): a checkpoint
// state file mapping source keys to created issue numbers, throttled writes,
// and label bootstrapping so batch creates never reference a missing label.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/lumberbarons/issues/internal/conventions"
	"github.com/lumberbarons/issues/internal/gh"
	"github.com/lumberbarons/issues/internal/plan"
)

// ensureLabels creates any missing convention labels plus the given extras.
func (a *App) ensureLabels(ctx context.Context, extras []gh.Label) error {
	existing, err := a.Client.ListLabels(ctx)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, l := range existing {
		have[l.Name] = true
	}
	var want []gh.Label
	for _, l := range conventions.Labels {
		want = append(want, gh.Label{Name: l.Name, Color: l.Color, Description: l.Description})
	}
	want = append(want, extras...)
	for _, l := range want {
		if have[l.Name] {
			continue
		}
		if err := a.Client.CreateLabel(ctx, l); err != nil {
			return fmt.Errorf("creating label %q: %w", l.Name, err)
		}
	}
	return nil
}

// batchState is what a batch write resumes from: which source keys became
// which issues, and which dependency edges were already wired. Edges are
// checkpointed because re-attempting one is not free — GitHub answers a
// duplicate edge with an error the tool has to report, so a clean resume of
// a finished plan buried its "0 created" summary in warnings (#46).
type batchState struct {
	Mapping map[string]int  `json:"mapping"`
	Edges   map[string]bool `json:"edges,omitempty"`
}

// edgeKey identifies a wired edge by kind and resolved endpoints, so the
// record survives a plan whose local ids or line numbers moved.
func edgeKey(kind plan.EdgeKind, from, to int) string {
	return fmt.Sprintf("%s:%d->%d", kind, from, to)
}

// loadBatchState reads a checkpoint file. A missing file is a fresh start; a
// corrupt one aborts — treating it as empty would duplicate every
// already-created issue.
func loadBatchState(path string) (*batchState, error) {
	state := &batchState{Mapping: map[string]int{}, Edges: map[string]bool{}}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return nil, err
	}
	// State files written before edges were checkpointed are a bare
	// key→number map. Reading them keeps a batch that is mid-flight across
	// an upgrade resumable instead of duplicating everything it created.
	var legacy map[string]int
	if err := json.Unmarshal(data, &legacy); err == nil {
		state.Mapping = legacy
		return state, nil
	}
	if err := json.Unmarshal(data, state); err != nil {
		return nil, fmt.Errorf("%s is not a valid resume-state file: %w", path, err)
	}
	if state.Mapping == nil {
		state.Mapping = map[string]int{}
	}
	if state.Edges == nil {
		state.Edges = map[string]bool{}
	}
	return state, nil
}

func saveBatchState(path string, state *batchState) error {
	data, err := json.MarshalIndent(state, "", "  ") // map keys marshal sorted
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func sleep(d time.Duration) {
	if d > 0 {
		time.Sleep(d)
	}
}
