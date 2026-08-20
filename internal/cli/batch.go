package cli

// Shared machinery for the batch writers (migrate, apply): a checkpoint
// state file mapping source keys to created issue numbers, throttled writes,
// and label bootstrapping so batch creates never reference a missing label.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lumberbarons/hew/internal/conventions"
	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/plan"
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
// Checkpoints are bound to the target repository and the source file digest
// so state from an unrelated repo, plan, or snapshot is rejected (#81).
type batchState struct {
	Repo    string          `json:"repo"`
	Digest  string          `json:"digest"`
	Mapping map[string]int  `json:"mapping"`
	Edges   map[string]bool `json:"edges,omitempty"`
}

// fileDigest computes a sha256 digest of source plan or snapshot data.
func fileDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", sum)
}

// edgeKey identifies a wired edge by kind and resolved endpoints, so the
// record survives a plan whose local ids or line numbers moved.
func edgeKey(kind plan.EdgeKind, from, to int) string {
	return fmt.Sprintf("%s:%d->%d", kind, from, to)
}

// applyProvenance returns the tool-generated provenance marker embedded in
// issues created by `hew apply`.
func applyProvenance(key string) string {
	return fmt.Sprintf("<!-- hew:apply key=%s -->", key)
}

// verifyApplyProvenance checks whether an issue body contains the provenance marker
// for the given plan entry key.
func verifyApplyProvenance(body, key string) bool {
	return strings.Contains(body, applyProvenance(key))
}

// beadProvenance returns the tool-generated provenance marker embedded in
// issues created by `hew migrate beads`.
func beadProvenance(id string) string {
	return fmt.Sprintf("Migrated from beads `%s`", id)
}

// verifyBeadProvenance checks whether an issue body contains the provenance marker
// for the given bead ID.
func verifyBeadProvenance(body, id string) bool {
	return strings.Contains(body, beadProvenance(id))
}

// loadBatchState reads a checkpoint file. A missing file is a fresh start; a
// corrupt, unbound, or mismatched one aborts — treating it as empty would duplicate
// every already-created issue, and using an unbound or mismatched one could mutate
// unrelated issues (#81).
func loadBatchState(path string, repo string, digest string) (*batchState, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &batchState{
			Repo:    repo,
			Digest:  digest,
			Mapping: map[string]int{},
			Edges:   map[string]bool{},
		}, nil
	}
	if err != nil {
		return nil, err
	}
	var state batchState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("%s is not a valid resume-state file: %w", path, err)
	}
	if state.Repo == "" || state.Digest == "" {
		return nil, fmt.Errorf("%s is not a valid resume-state file: missing repository or digest binding", path)
	}
	if state.Repo != repo {
		return nil, fmt.Errorf("state file %s is for repository %q, not %q", path, state.Repo, repo)
	}
	if state.Digest != digest {
		return nil, fmt.Errorf("state file %s is for a different plan or snapshot", path)
	}
	if state.Mapping == nil {
		state.Mapping = map[string]int{}
	}
	if state.Edges == nil {
		state.Edges = map[string]bool{}
	}
	return &state, nil
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
