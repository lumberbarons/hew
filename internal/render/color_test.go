package render

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/internal/model"
)

func TestColorEnabledContract(t *testing.T) {
	// FORCE_COLOR=1 opts back in unconditionally — off a TTY, past NO_COLOR.
	t.Run("force color wins", func(t *testing.T) {
		t.Setenv("FORCE_COLOR", "1")
		t.Setenv("NO_COLOR", "1")
		t.Setenv("TERM", "dumb")
		var buf bytes.Buffer
		if !ColorEnabled(&buf) {
			t.Error("FORCE_COLOR=1 must enable color even against NO_COLOR/TERM=dumb")
		}
	})
	t.Run("no color opts out", func(t *testing.T) {
		t.Setenv("FORCE_COLOR", "")
		t.Setenv("NO_COLOR", "anything")
		t.Setenv("TERM", "xterm-256color")
		if ColorEnabled(neverTTYFile(t)) {
			t.Error("NO_COLOR must disable color regardless of value")
		}
	})
	t.Run("dumb term disables", func(t *testing.T) {
		t.Setenv("FORCE_COLOR", "")
		t.Setenv("NO_COLOR", "")
		t.Setenv("TERM", "dumb")
		if ColorEnabled(neverTTYFile(t)) {
			t.Error("TERM=dumb must disable color")
		}
	})
	t.Run("non-terminal file disables", func(t *testing.T) {
		t.Setenv("FORCE_COLOR", "")
		t.Setenv("NO_COLOR", "")
		t.Setenv("TERM", "xterm-256color")
		if ColorEnabled(neverTTYFile(t)) {
			t.Error("a regular file must not be treated as a terminal")
		}
	})
	t.Run("non-file writer disables", func(t *testing.T) {
		t.Setenv("FORCE_COLOR", "")
		t.Setenv("NO_COLOR", "")
		t.Setenv("TERM", "xterm-256color")
		var buf bytes.Buffer
		if ColorEnabled(&buf) {
			t.Error("a non-file writer cannot be a terminal")
		}
	})
}

// neverTTYFile is an *os.File that is definitely not a terminal, so the
// TTY probe inside ColorEnabled has something to say no about.
func neverTTYFile(t *testing.T) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "not-a-tty")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func TestStyleWrappers(t *testing.T) {
	s := StyleFor(true)
	if got := s.num(42); got != "\x1b[38;2;242;169;59m#42\x1b[0m" {
		t.Errorf("num = %q", got)
	}
	if got := s.numPadded(7, 3); got != "\x1b[38;2;242;169;59m#7  \x1b[0m" {
		t.Errorf("numPadded = %q", got)
	}
	if got := s.metaPadded("P2 bug", 8); got != "\x1b[38;2;95;214;139mP2\x1b[0m bug  " {
		t.Errorf("metaPadded = %q", got)
	}
	if got := s.dim("hint"); got != "\x1b[38;2;110;125;113mhint\x1b[0m" {
		t.Errorf("dim = %q", got)
	}
	if got := s.dim(""); got != "" {
		t.Errorf("dim of empty text = %q", got)
	}
	if got := s.refNum(9, true); got != "\x1b[38;2;242;169;59m#9\x1b[0m (closed)" {
		t.Errorf("refNum closed = %q", got)
	}

	plain := Style{}
	if got := plain.num(42); got != "#42" {
		t.Errorf("plain num = %q", got)
	}
	if got := plain.metaPadded("P2 bug", 8); got != "P2 bug  " {
		t.Errorf("plain metaPadded = %q", got)
	}
	if got := plain.dim("hint"); got != "hint" {
		t.Errorf("plain dim = %q", got)
	}
}

// The colored renderers wrap exactly the spans the landing page colors: the
// number amber, the priority green, secondary text dim — and nothing else,
// so the title stays in the terminal's default text color.
func TestListColored(t *testing.T) {
	issues := fixtureIssues()
	blocked := model.Issue{
		Number: 121, Title: "Voltgo client for tests", State: "OPEN", CreatedAt: ts(9),
		Labels:    []string{"P2", "enhancement"},
		BlockedBy: []model.Ref{{Number: 120, State: "OPEN"}, {Number: 8, State: "CLOSED"}},
	}
	epic := model.Issue{
		Number: 137, Title: "Epic: Voltgo", State: "OPEN", CreatedAt: ts(5),
		Labels:         []string{"P2"},
		SubIssuesTotal: 6, SubIssuesCompleted: 2,
		SubIssues: []model.Ref{{Number: 120, State: "OPEN"}},
	}
	claimed := model.Issue{
		Number: 124, Title: "Claimed one", State: "OPEN", CreatedAt: ts(8),
		Labels: []string{"P2", "bug", "in-progress"}, Assignees: []string{"lumberbarons"},
	}
	var buf bytes.Buffer
	List(&buf, append(issues, blocked, epic, claimed), StyleFor(true))
	checkGolden(t, "list_color", buf.Bytes())
}

func TestShowColored(t *testing.T) {
	i := model.Issue{
		Number: 42, Title: "Fix the frobnicator", State: "OPEN", CreatedAt: ts(6),
		Labels:    []string{"P1", "bug", "tests"},
		Assignees: []string{"lumberbarons"},
		Parent:    &model.Ref{Number: 137, State: "OPEN"}, ParentTitle: "Epic: Voltgo",
		BlockedBy: []model.Ref{{Number: 7, State: "OPEN"}, {Number: 8, State: "CLOSED"}},
		Body:      "### Where\n\ninternal/frob\n\n### Problem\n\nIt wobbles.",
		Comments: []model.Comment{
			{Author: "alice", CreatedAt: ts(7), Body: "repro attached"},
		},
		CommentsTotal: 12,
	}
	var buf bytes.Buffer
	Show(&buf, i, StyleFor(true))
	checkGolden(t, "show_color", buf.Bytes())
}

func TestPrimeColored(t *testing.T) {
	issues := fixtureIssues()
	inProgress := model.Issue{
		Number: 124, Title: "/api/info verified by substring matching",
		State: "OPEN", CreatedAt: ts(8),
		Labels:    []string{"P2", "bug", "tests", "in-progress"},
		Assignees: []string{"lumberbarons"},
	}
	epic := model.Issue{
		Number: 137, Title: "Voltgo BLE battery controller support",
		State: "OPEN", CreatedAt: ts(5), Labels: []string{"P2"},
		SubIssuesTotal: 6, SubIssuesCompleted: 0,
		SubIssues: []model.Ref{{Number: 120, State: "OPEN"}},
	}
	d := PrimeData{
		Repo:       "lumberbarons/solar-controller",
		Ready:      model.Ready(issues),
		ReadyTotal: 3,
		OpenTotal:  14,
		InProgress: []model.Issue{inProgress},
		Epics:      []model.Issue{epic},
		Untriaged:  7,
	}
	var buf bytes.Buffer
	Prime(&buf, "Workflow: hew ready → hew start <n>.", d, StyleFor(true))
	checkGolden(t, "prime_color", buf.Bytes())
}

// The hostile-text guard still holds under color: sanitization runs before
// the style wraps, and a hostile title cannot smuggle its own sequence past
// the wrap. Strip the renderer's own palette sequences and the remainder
// must be free of anything a terminal would act on.
func TestColoredOutputStillNeutralizesHostileText(t *testing.T) {
	var buf bytes.Buffer
	List(&buf, []model.Issue{hostileIssue()}, StyleFor(true))
	out := buf.String()
	plain := strings.NewReplacer(sgrAmber, "", sgrGreen, "", sgrDim, "", sgrReset, "").Replace(out)
	assertNeutralized(t, "colored List", plain)
	if !strings.Contains(plain, "?[31m") {
		t.Errorf("hostile sequence not neutralized:\n%q", plain)
	}
}

// Golden files with ANSI in them are awkward to eyeball in a diff, so the
// tests above pin the colored bytes directly to the palette constants too.
func TestGoldenFilesExist(t *testing.T) {
	for _, name := range []string{"list_color", "show_color", "prime_color"} {
		path := filepath.Join("testdata", name+".golden")
		if _, err := os.Stat(path); err != nil {
			t.Errorf("missing colored golden %s (run go test ./internal/render -update)", name)
		}
	}
}
