package local

import (
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lumberbarons/hew/internal/gh"
)

func TestReadSnapshotCarriesWhatMigrationNeeds(t *testing.T) {
	s := newStore(t)
	must(t, s.CreateLabel(ctx, gh.Label{Name: "P1", Color: "ff0000", Description: "urgent"}))
	parent := mustCreate(t, s, "Epic", "the body", "P1")
	child := mustCreate(t, s, "Child", "", "task")
	must(t, s.AddSubIssue(ctx, parent, child, false))
	must(t, s.AddBlockedBy(ctx, child, parent))
	for i := range 7 { // more than GetIssue shows
		must(t, s.Comment(ctx, child, "note "+string(rune('a'+i))))
	}
	must(t, s.CloseIssue(ctx, child, gh.CloseNotPlanned))

	snap, err := ReadSnapshot(s.dir)
	must(t, err)

	if len(snap.Issues) != 2 {
		t.Fatalf("issues = %d, want 2", len(snap.Issues))
	}
	p, c := snap.Issues[0], snap.Issues[1]
	if p.Number != parent || p.Title != "Epic" || p.Body != "the body" || p.State != "OPEN" {
		t.Errorf("parent = %+v", p)
	}
	if c.Parent != parent || len(c.BlockedBy) != 1 || c.BlockedBy[0] != parent {
		t.Errorf("child edges = parent %d blockedBy %v, want both %d", c.Parent, c.BlockedBy, parent)
	}
	if c.State != "CLOSED" || c.StateReason != string(gh.CloseNotPlanned) {
		t.Errorf("child closed as %q/%q, want CLOSED/NOT_PLANNED", c.State, c.StateReason)
	}
	if len(c.Comments) != 7 || c.Comments[0].Body != "note a" || c.Comments[0].Author != "me" {
		t.Errorf("comments = %+v, want all 7 with their authors", c.Comments)
	}
	if !c.Comments[0].CreatedAt.Equal(time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("comment time = %v", c.Comments[0].CreatedAt)
	}
	if len(snap.Labels) != 1 || snap.Labels[0].Color != "ff0000" {
		t.Errorf("labels = %+v, want the defined P1", snap.Labels)
	}
	if !strings.HasPrefix(snap.Digest, "sha256:") {
		t.Errorf("digest = %q", snap.Digest)
	}
}

func TestSnapshotDigestFollowsTheFile(t *testing.T) {
	s := newStore(t)
	mustCreate(t, s, "One", "")
	before, err := ReadSnapshot(s.dir)
	must(t, err)
	again, err := ReadSnapshot(s.dir)
	must(t, err)
	if before.Digest != again.Digest {
		t.Errorf("digest changed with no write: %s vs %s", before.Digest, again.Digest)
	}
	mustCreate(t, s, "Two", "")
	after, err := ReadSnapshot(s.dir)
	must(t, err)
	if before.Digest == after.Digest {
		t.Error("digest did not change after a write")
	}
}

func TestSnapshotRefusesATrackerThatIsNotLocal(t *testing.T) {
	s := newStore(t)
	must(t, WriteConfig(s.dir, Config{Backend: BackendGitHub}))
	for name, open := range map[string]func() error{
		"ReadSnapshot": func() error { _, err := ReadSnapshot(s.dir); return err },
		"Freeze":       func() error { _, err := Freeze(ctx, s.dir); return err },
	} {
		if err := open(); err == nil || !strings.Contains(err.Error(), "not locally") {
			t.Errorf("%s err = %v, want a refusal naming the backend", name, err)
		}
	}
}

func TestSealRecordsTheMappingAndFlipsTheBackend(t *testing.T) {
	s := newStore(t)
	a := mustCreate(t, s, "A", "")
	b := mustCreate(t, s, "B", "")
	fr, err := Freeze(ctx, s.dir)
	must(t, err)
	must(t, fr.Seal("o/r", map[int]int{a: 41, b: 57}))
	fr.Release()

	cfg, err := ReadConfig(s.dir)
	must(t, err)
	if cfg.Backend != BackendGitHub {
		t.Errorf("backend = %q, want github", cfg.Backend)
	}
	m, err := ReadMigrated(s.dir)
	must(t, err)
	if m.Repo != "o/r" || m.Mapping[a] != 41 || m.Mapping[b] != 57 || len(m.Mapping) != 2 {
		t.Errorf("migrated = %+v", m)
	}
	// The map is plain JSON an agent or human can read back.
	var raw struct {
		Mapping map[string]int `json:"mapping"`
	}
	must(t, json.Unmarshal([]byte(readFile(t, filepath.Join(s.dir, MigratedFile))), &raw))
	if raw.Mapping["1"] != 41 {
		t.Errorf("migrated.json mapping = %v, want local 1 → 41", raw.Mapping)
	}
	// The archive is left in place.
	if !strings.Contains(readFile(t, filepath.Join(s.dir, issuesFile)), `"title":"A"`) {
		t.Error("issues.jsonl no longer holds the archived issues")
	}
}

func TestWritesAfterMigrationAreRefused(t *testing.T) {
	s := newStore(t)
	n := mustCreate(t, s, "A", "")
	fr, err := Freeze(ctx, s.dir)
	must(t, err)
	must(t, fr.Seal("o/r", map[int]int{n: 1}))
	fr.Release()
	archived := readFile(t, filepath.Join(s.dir, issuesFile))

	// s was opened while the tracker was local, so only a check at write time
	// can stop it.
	for name, write := range map[string]func() error{
		"CreateIssue": func() error { _, err := s.CreateIssue(ctx, "late", "", nil); return err },
		"Comment":     func() error { return s.Comment(ctx, n, "late") },
		"CloseIssue":  func() error { return s.CloseIssue(ctx, n, gh.CloseCompleted) },
		"CreateLabel": func() error { return s.CreateLabel(ctx, gh.Label{Name: "late"}) },
	} {
		err := write()
		if err == nil || !strings.Contains(err.Error(), "not locally") {
			t.Errorf("%s err = %v, want a refusal", name, err)
		}
	}
	if got := readFile(t, filepath.Join(s.dir, issuesFile)); got != archived {
		t.Errorf("issues.jsonl changed after migration:\n%s", got)
	}
	if _, err := Open(s.dir, "me"); err == nil {
		t.Error("Open succeeded on a migrated tracker")
	}
}

func TestFreezeHoldsWritersUntilReleaseThenRefusesThem(t *testing.T) {
	s := newStore(t)
	n := mustCreate(t, s, "A", "")
	fr, err := Freeze(ctx, s.dir)
	must(t, err)

	var wg sync.WaitGroup
	wg.Add(1)
	var writeErr error
	started := make(chan struct{})
	go func() {
		defer wg.Done()
		close(started)
		writeErr = s.Comment(ctx, n, "racing the migration")
	}()
	<-started
	time.Sleep(50 * time.Millisecond) // let the writer reach the lock
	must(t, fr.Seal("o/r", map[int]int{n: 1}))
	fr.Release()
	wg.Wait()

	if writeErr == nil {
		t.Fatal("a write that raced the migration was accepted after the seal")
	}
	if strings.Contains(readFile(t, filepath.Join(s.dir, issuesFile)), "racing the migration") {
		t.Error("the raced comment reached the archive")
	}
}

func TestFreezeReleaseLeavesAnUnsealedTrackerWritable(t *testing.T) {
	s := newStore(t)
	n := mustCreate(t, s, "A", "")
	fr, err := Freeze(ctx, s.dir)
	must(t, err)
	fr.Release() // a failed migration gives the tracker back
	must(t, s.Comment(ctx, n, "still local"))
}

func TestReadMigratedOfAnUnmigratedTracker(t *testing.T) {
	s := newStore(t)
	if _, err := ReadMigrated(s.dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want a missing-file error", err)
	}
}
