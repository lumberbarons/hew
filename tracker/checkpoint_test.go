package tracker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMemoryCheckpointZeroValue(t *testing.T) {
	var checkpoint MemoryCheckpoint
	if _, ok := checkpoint.Created("a"); ok {
		t.Error("an empty checkpoint reports a created entry")
	}
	if checkpoint.Wired(ParentEdge, 1, 2) {
		t.Error("an empty checkpoint reports a wired edge")
	}
	if err := checkpoint.RecordCreated("a", 10); err != nil {
		t.Fatal(err)
	}
	if err := checkpoint.RecordWired(BlockedByEdge, 10, 2); err != nil {
		t.Fatal(err)
	}
	if n, ok := checkpoint.Created("a"); !ok || n != 10 {
		t.Errorf("Created = %d, %v", n, ok)
	}
	if !checkpoint.Wired(BlockedByEdge, 10, 2) {
		t.Error("a recorded edge is not wired")
	}
	entries := checkpoint.CreatedEntries()
	if len(entries) != 1 || entries[0].Key != "a" || entries[0].Number != 10 {
		t.Errorf("CreatedEntries = %+v", entries)
	}
}

// The file checkpoint has to read and write the state file `hew apply` and
// `migrate` already use: the same schema, version, repository binding and
// digest binding.
func TestFileCheckpointRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	checkpoint, err := OpenFileCheckpoint(path, "o/r", "sha256:test", "plan")
	if err != nil {
		t.Fatal(err)
	}
	if err := checkpoint.RecordCreated("a", 10); err != nil {
		t.Fatal(err)
	}
	if err := checkpoint.RecordWired(ParentEdge, 11, 10); err != nil {
		t.Fatal(err)
	}

	reloaded, err := OpenFileCheckpoint(path, "o/r", "sha256:test", "plan")
	if err != nil {
		t.Fatal(err)
	}
	if n, ok := reloaded.Created("a"); !ok || n != 10 {
		t.Errorf("reloaded Created = %d, %v", n, ok)
	}
	if !reloaded.Wired(ParentEdge, 11, 10) {
		t.Error("reloaded checkpoint lost the wired edge")
	}
	records := reloaded.CreatedEntries()
	if len(records) != 1 || records[0].Key != "a" || records[0].Number != 10 {
		t.Errorf("CreatedEntries = %+v", records)
	}
	if reloaded.Digest() != "sha256:test" {
		t.Errorf("Digest = %q, want the file's binding", reloaded.Digest())
	}
	if reloaded.Wired(BlockedByEdge, 11, 10) {
		t.Error("edge kind was not part of the record")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state CheckpointState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if state.Version != CheckpointVersion || state.Repo != "o/r" || state.Digest != "sha256:test" {
		t.Errorf("state = %+v, want the bound schema", state)
	}
	if state.Mapping["a"] != 10 {
		t.Errorf("mapping = %v", state.Mapping)
	}
	if !state.Edges[EdgeKey(ParentEdge, 11, 10)] {
		t.Errorf("edges = %v, want %q", state.Edges, EdgeKey(ParentEdge, 11, 10))
	}
}

func TestFileCheckpointRefusals(t *testing.T) {
	digest := FileDigest([]byte(`{"title":"x"}`))
	tests := map[string]struct {
		content string
		repo    string
		digest  string
		want    string
	}{
		"corrupt": {
			content: "{not json",
			repo:    "o/r", digest: digest,
			want: "not a valid resume-state file",
		},
		"unbound": {
			content: `{"mapping":{"a":1}}`,
			repo:    "o/r", digest: digest,
			want: "written by an older hew",
		},
		"newer version": {
			content: `{"version":99,"repo":"o/r","digest":"` + digest + `","mapping":{}}`,
			repo:    "o/r", digest: digest,
			want: "written by a newer hew",
		},
		"other repo": {
			content: `{"version":1,"repo":"other/repo","digest":"` + digest + `","mapping":{}}`,
			repo:    "o/r", digest: digest,
			want: "belongs to repository",
		},
		"other digest": {
			content: `{"version":1,"repo":"o/r","digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000","mapping":{}}`,
			repo:    "o/r", digest: digest,
			want: "was written for a different plan",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := OpenFileCheckpoint(path, tc.repo, tc.digest, "plan")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// JSON can say "no mapping" by omitting the key or writing null, and a null
// map would panic the first create.
func TestFileCheckpointReadsPartialState(t *testing.T) {
	digest := FileDigest([]byte("{}"))
	for name, content := range map[string]string{
		"mapping omitted": `{"version":1,"repo":"o/r","digest":"` + digest + `","edges":{"parent:2->1":true}}`,
		"nulls":           `{"version":1,"repo":"o/r","digest":"` + digest + `","mapping":null,"edges":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			checkpoint, err := OpenFileCheckpoint(path, "o/r", digest, "plan")
			if err != nil {
				t.Fatal(err)
			}
			if err := checkpoint.RecordCreated("a", 1); err != nil {
				t.Fatal(err)
			}
			if err := checkpoint.RecordWired(ParentEdge, 1, 2); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFileDigestDependsOnContent(t *testing.T) {
	if FileDigest([]byte("one")) == FileDigest([]byte("two")) {
		t.Error("different content shares a digest")
	}
}
