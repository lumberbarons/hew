package local

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
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
	before, err := ReadConfig(s.dir)
	must(t, err)
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
	if cfg.ID != before.ID {
		t.Errorf("id after seal = %q, want the tracker's %q", cfg.ID, before.ID)
	}
	m, err := ReadMigrated(s.dir)
	must(t, err)
	if m.Repo != "o/r" || m.Mapping[a] != 41 || m.Mapping[b] != 57 || len(m.Mapping) != 2 {
		t.Errorf("migrated = %+v", m)
	}
	if m.Version != formatVersion {
		t.Errorf("migrated version = %d, want %d", m.Version, formatVersion)
	}
	if age := time.Since(m.MigratedAt); age < 0 || age > time.Minute {
		t.Errorf("migrated_at = %v, want the moment of the seal", m.MigratedAt)
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

// Seal writes the number map before it flips the backend: a process that dies
// between the two leaves a tracker that is still local and can be migrated
// again, where the other order would strand a migrated tracker with no record
// of where its issues went. A map that cannot be written stands in for the
// death, and the backend must not have moved.
func TestSealLeavesTheTrackerLocalWhenTheMapCannotBeWritten(t *testing.T) {
	s := newStore(t)
	n := mustCreate(t, s, "A", "")
	fr, err := Freeze(ctx, s.dir)
	must(t, err)
	defer fr.Release()
	// A directory where migrated.json belongs makes the rename onto it fail.
	must(t, os.Mkdir(filepath.Join(s.dir, MigratedFile), 0o755))

	if err := fr.Seal("o/r", map[int]int{n: 1}); err == nil {
		t.Fatal("Seal succeeded though the number map could not be written")
	}
	cfg, err := ReadConfig(s.dir)
	must(t, err)
	if cfg.Backend != BackendLocal {
		t.Errorf("backend = %q after a failed Seal, want local so the migration can be rerun", cfg.Backend)
	}

	// The rerun the ordering exists for: clear the obstacle and seal again.
	must(t, os.Remove(filepath.Join(s.dir, MigratedFile)))
	must(t, fr.Seal("o/r", map[int]int{n: 1}))
	if cfg, err := ReadConfig(s.dir); err != nil || cfg.Backend != BackendGitHub {
		t.Errorf("config after the retry = %+v, %v; want github", cfg, err)
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

func TestReadMigratedRejectsACorruptRecord(t *testing.T) {
	for name, content := range map[string]string{
		"not json":      "nope",
		"unknown field": `{"version":1,"repo":"o/r","mapping":{},"surprise":1}`,
		"bad mapping":   `{"version":1,"repo":"o/r","mapping":{"x":1}}`,
	} {
		s := newStore(t)
		path := filepath.Join(s.dir, MigratedFile)
		must(t, os.WriteFile(path, []byte(content), 0o644))
		_, err := ReadMigrated(s.dir)
		if err == nil || errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), path) {
			t.Errorf("%s: err = %v, want a decode error naming %s", name, err, path)
		}
	}
}

func TestWriterBlockedByAMigrationSaysSoWithinTheBoundedWait(t *testing.T) {
	s := newStore(t)
	fr, err := Freeze(ctx, s.dir)
	must(t, err)
	defer fr.Release()

	var notice strings.Builder
	s.notice = &notice
	s.noticeAfter = 10 * time.Millisecond
	// Rechecking far more often than the wait is long gives a notice that is
	// not guarded against repeating many chances to repeat.
	s.noticeRecheck = 5 * time.Millisecond
	s.lockTimeout = 200 * time.Millisecond
	start := time.Now()
	_, err = s.CreateIssue(ctx, "x", "", nil)
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("gave up after %v, want the bounded wait", took)
	}

	if !strings.Contains(notice.String(), "migration is in progress") || !strings.Contains(notice.String(), fmt.Sprintf("pid %d", os.Getpid())) {
		t.Errorf("stderr notice = %q, want it to say a migration is in progress and name it", notice.String())
	}
	if got := strings.Count(notice.String(), "migration is in progress"); got != 1 {
		t.Errorf("notice said so %d times, want once: %q", got, notice.String())
	}
	if !strings.Contains(notice.String(), "waiting up to") {
		t.Errorf("stderr notice = %q, want it to say how much longer the wait can last", notice.String())
	}
	if err == nil || !strings.Contains(err.Error(), "migration is in progress") || strings.Contains(err.Error(), "lsof") {
		t.Errorf("err = %v, want a timeout that names the migration rather than telling the user to hunt for a holder", err)
	}
}

func TestPlainLockContentionGetsNoMigrationNotice(t *testing.T) {
	s := newStore(t)
	unlock, err := s.lock(ctx)
	must(t, err)
	defer unlock()

	var notice strings.Builder
	s.notice = &notice
	s.noticeAfter = 10 * time.Millisecond
	s.lockTimeout = 100 * time.Millisecond
	if _, err := s.CreateIssue(ctx, "x", "", nil); err == nil || strings.Contains(err.Error(), "migration is in progress") {
		t.Errorf("err = %v, want the generic timeout", err)
	}
	if notice.Len() != 0 {
		t.Errorf("stderr notice = %q, want none when no migration holds the lock", notice.String())
	}
}

func TestMigrationMarkerIsGoneAfterReleaseSealedOrNot(t *testing.T) {
	for name, seal := range map[string]bool{"failed": false, "sealed": true} {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			n := mustCreate(t, s, "A", "")
			fr, err := Freeze(ctx, s.dir)
			must(t, err)
			marker := filepath.Join(s.dir, migratingFile)
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("no marker while frozen: %v", err)
			}
			if seal {
				must(t, fr.Seal("o/r", map[int]int{n: 1}))
			}
			fr.Release()
			fr.Release() // idempotent
			if _, err := os.Stat(marker); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("marker after Release: err = %v, want it removed", err)
			}
		})
	}
}

// A migration killed mid-run frees the lock but cannot remove its marker. The
// marker must neither stall writers nor survive the next one.
func TestCrashedMigrationsMarkerDoesNotBlockWriters(t *testing.T) {
	s := newStore(t)
	marker := filepath.Join(s.dir, migratingFile)
	must(t, os.WriteFile(marker, []byte(`{"pid":999999,"started_at":"2026-07-01T11:00:00Z"}`), 0o644))

	var notice strings.Builder
	s.notice = &notice
	s.noticeAfter = time.Millisecond
	s.lockTimeout = time.Second
	mustCreate(t, s, "after the crash", "")

	if notice.Len() != 0 {
		t.Errorf("stderr notice = %q, want none: the lock was free", notice.String())
	}
	if _, err := os.Stat(marker); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("stale marker survived a writer: err = %v", err)
	}
}

func TestGarbledMigrationMarkerStillExplainsTheWait(t *testing.T) {
	s := newStore(t)
	unlock, err := s.lock(ctx)
	must(t, err)
	defer unlock()
	must(t, os.WriteFile(filepath.Join(s.dir, migratingFile), []byte("not json"), 0o644))

	s.lockTimeout = 30 * time.Millisecond
	_, err = s.CreateIssue(ctx, "x", "", nil)
	if err == nil || !strings.Contains(err.Error(), "migration is in progress") {
		t.Errorf("err = %v, want the migration named even when its marker is unreadable", err)
	}
	if err != nil && strings.Contains(err.Error(), "pid") {
		t.Errorf("err = %v, invents a pid the unreadable marker never gave", err)
	}
}
