package render

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/lumberbarons/hew/internal/model"
)

// hostile is what a GitHub user can put in any free-text field: an SGR colour
// change, an OSC 52 clipboard write, a bare CR that overwrites the line
// already printed, a C1 CSI both as valid UTF-8 and as a raw byte, a newline
// that forges a line of its own, and a DEL.
const hostile = "a\x1b[31mb\x1b]52;c;aGVsbG8=\x07c\rd\x9be\u009bf\ng\x7fh"

// assertNeutralized fails when text output still carries anything a terminal
// would act on. Newline and tab are the renderer's own formatting.
func assertNeutralized(t *testing.T, label, s string) {
	t.Helper()
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			t.Errorf("%s: invalid UTF-8 byte %#x at offset %d in %q", label, s[i], i, s)
		case r == '\n' || r == '\t':
		case unicode.IsControl(r):
			t.Errorf("%s: control character %#x at offset %d in %q", label, r, i, s)
		}
		i += size
	}
}

func TestSanitizeInline(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain text untouched", "Reopen command symmetric with close", "Reopen command symmetric with close"},
		{"non-ASCII untouched", "café → ✓ 日本語", "café → ✓ 日本語"},
		{"SGR colour", "\x1b[31mred\x1b[0m", "?[31mred?[0m"},
		{"OSC 52 clipboard write", "\x1b]52;c;aGVsbG8=\x07", "?]52;c;aGVsbG8=?"},
		{"bare carriage return", "real\rfake", "real?fake"},
		{"newline forging a line", "title\n#99 P0 bug  fake", "title?#99 P0 bug  fake"},
		{"tab", "a\tb", "a?b"},
		{"DEL", "a\x7fb", "a?b"},
		{"C1 CSI as UTF-8", "\u009b31m", "?31m"},
		{"C1 CSI as a raw byte", "\x9b31m", "?31m"},
		{"NUL", "a\x00b", "a?b"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeInline(tt.in); got != tt.want {
				t.Errorf("sanitizeInline(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSanitizeBlock(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"newline is formatting", "### Where\n\ninternal/render/text.go", "### Where\n\ninternal/render/text.go"},
		{"tab is formatting", "- [ ] a\n\tindented", "- [ ] a\n\tindented"},
		{"SGR colour", "body\x1b[31m", "body?[31m"},
		{"bare carriage return", "real\rfake", "real?fake"},
		{"C1 CSI as a raw byte", "\x9b31m", "?31m"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeBlock(tt.in); got != tt.want {
				t.Errorf("sanitizeBlock(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// hostileValidUTF8 is the same attack surface without the deliberately
// invalid byte: encoding/json substitutes U+FFFD for invalid UTF-8, so the
// round-trip assertion below needs input that is valid to begin with.
var hostileValidUTF8 = "a\x1b[31mb\x1b]52;c;aGVsbG8=\x07c\rd" + string(rune(0x9b)) + "e\nf\x7fg"

// hostileIssueWith populates every GitHub-derived field with the given text:
// title, label-derived area, assignee, body, parent title, comment author and
// comment body.
func hostileIssueWith(s string) model.Issue {
	return model.Issue{
		Number: 42, Title: "title " + s, State: "OPEN", CreatedAt: ts(1),
		Labels:      []string{"P1", "bug", "area " + s},
		Assignees:   []string{"login" + s},
		Body:        "### Problem\n\nbody " + s,
		Parent:      &model.Ref{Number: 7, State: "OPEN"},
		ParentTitle: "parent " + s,
		Comments: []model.Comment{{
			Author: "commenter" + s, CreatedAt: ts(2), Body: "comment " + s,
		}},
		CommentsTotal: 1,
	}
}

func hostileIssue() model.Issue { return hostileIssueWith(hostile) }

func TestShow_NeutralizesHostileFields(t *testing.T) {
	var buf bytes.Buffer
	Show(&buf, hostileIssue())
	assertNeutralized(t, "Show", buf.String())
	// Neutralized, not dropped: the read path never hides what GitHub holds.
	if !strings.Contains(buf.String(), "?[31m") {
		t.Errorf("Show dropped the offending text instead of neutralizing it:\n%s", buf.String())
	}
	for _, want := range []string{"title ", "area ", "login", "body ", "parent ", "commenter", "comment "} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("Show lost the %q field:\n%s", want, buf.String())
		}
	}
}

func TestLine_NeutralizesHostileTitle(t *testing.T) {
	assertNeutralized(t, "Line", Line(hostileIssue()))
}

func TestList_NeutralizesHostileFields(t *testing.T) {
	for _, tt := range []struct {
		name  string
		write func(w *bytes.Buffer, issues []model.Issue)
	}{
		{"List", func(w *bytes.Buffer, issues []model.Issue) { List(w, issues) }},
		{"ListWithAssignees", func(w *bytes.Buffer, issues []model.Issue) { ListWithAssignees(w, issues) }},
		{"EpicList", func(w *bytes.Buffer, issues []model.Issue) { EpicList(w, issues) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			tt.write(&buf, []model.Issue{hostileIssue()})
			assertNeutralized(t, tt.name, buf.String())
		})
	}
}

// A hostile assignee reaches text output through the annotation path too.
func TestList_NeutralizesHostileAnnotations(t *testing.T) {
	claimed := hostileIssue()
	claimed.Labels = append(claimed.Labels, "in-progress")
	claimed.State = "CLOSED"
	var buf bytes.Buffer
	List(&buf, []model.Issue{claimed})
	assertNeutralized(t, "List annotations", buf.String())
}

func TestEpicStatus_NeutralizesHostileChildTitle(t *testing.T) {
	epic := model.Issue{
		Number: 7, Title: "Epic: " + hostile, State: "OPEN", CreatedAt: ts(1),
		Labels: []string{"P2"}, SubIssuesTotal: 1,
		SubIssues: []model.Ref{{Number: 42, State: "OPEN"}},
	}
	var buf bytes.Buffer
	EpicStatus(&buf, epic, []model.Issue{hostileIssue()})
	assertNeutralized(t, "EpicStatus", buf.String())
}

func TestPrime_NeutralizesHostileFields(t *testing.T) {
	var buf bytes.Buffer
	Prime(&buf, "static conventions", PrimeData{
		Repo: "owner/repo" + hostile, Ready: []model.Issue{hostileIssue()}, ReadyTotal: 1, OpenTotal: 1,
		InProgress: []model.Issue{hostileIssue()},
		Epics: []model.Issue{{
			Number: 7, Title: "Epic: " + hostile, State: "OPEN", CreatedAt: ts(1),
			Labels: []string{"P2"}, SubIssuesTotal: 1, SubIssues: []model.Ref{{Number: 42, State: "OPEN"}},
		}},
	})
	assertNeutralized(t, "Prime", buf.String())
}

// The JSON path keeps the raw value rather than sanitizing it, because its
// consumers are machines: encoding/json escapes every C0 control -- ESC, BEL
// and CR, the bytes a terminal actually acts on -- and a decoder gets back
// exactly what GitHub stored. Asserting that no raw C0 survives and that the
// value round-trips proves the escaping without pinning the encoder's syntax.
//
// C1 controls and DEL do reach the stream as themselves. Neither introduces
// an escape sequence in a UTF-8 terminal, and the text path above is what
// guards the human-readable output.
func TestWriteJSON_EscapesRatherThanSanitizes(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJSON(&buf, ToJSON(hostileIssueWith(hostileValidUTF8), true)); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for i := 0; i < len(out); i++ {
		// The encoder's own line breaks are the only raw C0 it emits.
		if out[i] < 0x20 && out[i] != '\n' {
			t.Errorf("raw C0 byte %#x at offset %d in JSON output:\n%s", out[i], i, out)
		}
	}

	var got IssueJSON
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := hostileIssueWith(hostileValidUTF8)
	if got.Title != want.Title {
		t.Errorf("title round-trip = %q, want %q", got.Title, want.Title)
	}
	if got.Body == nil || *got.Body != want.Body {
		t.Errorf("body round-trip = %v, want %q", got.Body, want.Body)
	}
	if len(got.Comments) != 1 || got.Comments[0].Body != want.Comments[0].Body ||
		got.Comments[0].Author != want.Comments[0].Author {
		t.Errorf("comment round-trip = %+v, want %+v", got.Comments, want.Comments)
	}
	if len(got.Assignees) != 1 || got.Assignees[0] != want.Assignees[0] {
		t.Errorf("assignee round-trip = %v, want %v", got.Assignees, want.Assignees)
	}
}
