package migrate

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/tracker"
)

const beadsFixture = `{"_type":"issue","id":"b-1","title":"Open work","status":"open","priority":1,"issue_type":"feature","created_at":"2026-01-01T00:00:00Z","labels":["front,back"]}
{"_type":"issue","id":"b-2","title":"Odd status","status":"blocked","priority":2,"issue_type":"task","created_at":"2026-01-02T00:00:00Z"}
{"_type":"issue","id":"b-3","title":"Closed work","status":"closed","priority":3,"issue_type":"bug","created_at":"2026-01-03T00:00:00Z"}
`

func beadsMigrator(t *testing.T, target Target) *Migrator {
	t.Helper()
	return Wrap(target, Config{Checkpoint: tracker.NewMemoryCheckpoint()})
}

// Warnings about the source are values on the result, and the mapping still
// migrates what it can: an unknown status means open, a comma becomes a hyphen.
func TestBeadsReportsSourceWarnings(t *testing.T) {
	target := newFakeTarget()
	res, err := beadsMigrator(t, target).Beads(context.Background(), BeadsRequest{Snapshot: []byte(beadsFixture)})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []WarningKind
	for _, w := range res.Warnings {
		kinds = append(kinds, w.Kind)
	}
	if !slices.Contains(kinds, WarnUnknownStatus) {
		t.Errorf("warnings = %+v, want an unknown-status warning", res.Warnings)
	}
	if !slices.Contains(kinds, WarnLabelRenamed) {
		t.Errorf("warnings = %+v, want a label-rename warning", res.Warnings)
	}
	if res.Selected != 2 || res.Skipped != 1 {
		t.Errorf("selected/skipped = %d/%d, want 2/1", res.Selected, res.Skipped)
	}
	if got := target.byNumber(101); got == nil || !slices.Contains(got.Labels, "front-back") || slices.Contains(got.Labels, "front,back") {
		t.Errorf("first issue labels = %+v, want the renamed area", got)
	}
	if got := target.byNumber(102); got == nil || got.Title != "Odd status" {
		t.Errorf("unknown-status bead was not migrated: %+v", got)
	}
}

// A snapshot with nothing selected is a real result: it reads no checkpoint
// and touches no target, and the caller can say what it skipped.
func TestBeadsNothingSelectedTouchesNothing(t *testing.T) {
	target := newFakeTarget()
	res, err := beadsMigrator(t, target).Beads(context.Background(), BeadsRequest{Snapshot: []byte(beadsFixture)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Selected != 2 {
		t.Fatalf("selected = %d, want the two open beads", res.Selected)
	}

	onlyClosed := `{"_type":"issue","id":"b-3","title":"Closed","status":"closed","priority":3,"issue_type":"bug","created_at":"2026-01-03T00:00:00Z"}` + "\n"
	target.calls = nil
	res, err = beadsMigrator(t, target).Beads(context.Background(), BeadsRequest{Snapshot: []byte(onlyClosed)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Selected != 0 || res.Skipped != 1 || len(res.Plan) != 0 {
		t.Errorf("result = %+v, want nothing selected and one skipped", res)
	}
	if len(target.calls) != 0 {
		t.Errorf("a snapshot with nothing selected wrote: %v", target.calls)
	}
}

func TestBeadsRequiresASnapshot(t *testing.T) {
	_, err := beadsMigrator(t, newFakeTarget()).Beads(context.Background(), BeadsRequest{})
	if err == nil || !strings.Contains(err.Error(), "snapshot") {
		t.Fatalf("err = %T %v, want an invalid snapshot", err, err)
	}
}

func TestBeadsReaderAndSnapshotAgree(t *testing.T) {
	fromBytes, err := beadsMigrator(t, newFakeTarget()).Beads(context.Background(), BeadsRequest{Snapshot: []byte(beadsFixture), IncludeClosed: true})
	if err != nil {
		t.Fatal(err)
	}
	fromReader, err := beadsMigrator(t, newFakeTarget()).Beads(context.Background(), BeadsRequest{Reader: strings.NewReader(beadsFixture), IncludeClosed: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(fromBytes.Plan) != len(fromReader.Plan) || fromBytes.Selected != fromReader.Selected {
		t.Errorf("reader and bytes disagree: %+v / %+v", fromBytes, fromReader)
	}
}
