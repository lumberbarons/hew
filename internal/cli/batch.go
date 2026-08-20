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
	"sort"
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

// batchStateVersion is the current checkpoint schema. It exists so a state
// file this build cannot safely interpret is named as such instead of being
// reported as corrupt: version 0 is any file written before checkpoints
// carried a repository and source binding (#81).
const batchStateVersion = 1

// batchState is what a batch write resumes from: which source keys became
// which issues, and which dependency edges were already wired. Edges are
// checkpointed because re-attempting one is not free — GitHub answers a
// duplicate edge with an error the tool has to report, so a clean resume of
// a finished plan buried its "0 created" summary in warnings (#46).
// Checkpoints are bound to the target repository and the source file digest
// so state from an unrelated repo, plan, or snapshot is rejected (#81).
type batchState struct {
	Version int             `json:"version"`
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

// restartHint is appended to every rejection. Naming the remedy matters more
// here than usual: the output is read by agents, and the obvious move —
// deleting the state file — silently re-creates everything the interrupted
// run already created, which is the one outcome the checkpoint exists to
// prevent.
func restartHint(path string) string {
	return fmt.Sprintf("pass --state with a new path to start a fresh run (every issue the interrupted run already created will be created again), or delete %s", path)
}

// loadBatchState reads a checkpoint file. A missing file is a fresh start; a
// corrupt, unbound, or mismatched one aborts — treating it as empty would
// duplicate every already-created issue, and using an unbound or mismatched
// one could mutate unrelated issues (#81).
func loadBatchState(path string, repo string, digest string, sourceNoun string) (*batchState, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &batchState{
			Version: batchStateVersion,
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
		return nil, fmt.Errorf("%s is not a valid resume-state file: %w; %s", path, err, restartHint(path))
	}
	switch {
	case state.Version == 0 || state.Repo == "" || state.Digest == "":
		return nil, fmt.Errorf("%s was written by an older hew and cannot be resumed safely: it records no repository or %s binding, so its issue numbers cannot be trusted; %s",
			path, sourceNoun, restartHint(path))
	case state.Version > batchStateVersion:
		return nil, fmt.Errorf("%s was written by a newer hew (state version %d, this build understands %d); upgrade hew to resume it",
			path, state.Version, batchStateVersion)
	}
	if !strings.EqualFold(state.Repo, repo) {
		return nil, fmt.Errorf("state file %s belongs to repository %q, not %q; %s", path, state.Repo, repo, restartHint(path))
	}
	if state.Digest != digest {
		return nil, fmt.Errorf("state file %s was written for a different %s — the file's contents changed since the run that created it, and checkpoints are bound to those contents; restore the original %s to resume, or %s",
			path, sourceNoun, sourceNoun, restartHint(path))
	}
	if state.Mapping == nil {
		state.Mapping = map[string]int{}
	}
	if state.Edges == nil {
		state.Edges = map[string]bool{}
	}
	return &state, nil
}

// verifyBatchState checks every mapping in a checkpoint against GitHub before
// the caller mutates anything: the key must belong to this source file, and
// the issue must carry the provenance marker this batch kind writes, bound to
// this key and this source digest. Both commands run it as one pass up front
// so a rejection costs no writes at all (#81).
//
// Mappings are walked in sorted order so a state file with more than one bad
// entry fails on the same one every time — an unstable error message is a
// bad bug report.
func (a *App) verifyBatchState(ctx context.Context, state *batchState, kind string, valid map[string]bool, keyNoun string) error {
	keys := make([]string, 0, len(state.Mapping))
	for key := range state.Mapping {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		number := state.Mapping[key]
		if !valid[key] {
			return fmt.Errorf("state file maps %s %q to issue #%d, but %q is not in this run's selection — it was removed from the source, or a flag is filtering it out", keyNoun, key, number, key)
		}
		issue, err := a.Client.GetIssue(ctx, number)
		if err != nil {
			return fmt.Errorf("verifying mapped issue #%d for %s: %w", number, key, err)
		}
		if !conventions.HasProvenanceMarker(issue.Body, kind, key, state.Digest) {
			return fmt.Errorf("refusing to modify issue #%d: it does not carry the hew provenance marker for %s %q from this source file, so this run did not create it", number, keyNoun, key)
		}
	}
	return nil
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
