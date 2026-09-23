package render

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lumberbarons/hew/internal/model"
)

// suspiciousIssue carries a finding in the title, the body and one of two
// displayed comments, with the comment thread capped server-side.
func suspiciousIssue() model.Issue {
	return model.Issue{
		Number: 12, Title: "Report p\u0430ypal", State: "OPEN", CreatedAt: ts(2),
		Body: "Steps\u200b to reproduce",
		Comments: []model.Comment{
			{Author: "alice", CreatedAt: ts(3), Body: "same here 👩‍💻"},
			{Author: "mallory", CreatedAt: ts(4), Body: "a\u202eb\U000e0069"},
		},
		CommentsTotal: 5,
	}
}

func TestShow_TextFindings(t *testing.T) {
	var buf bytes.Buffer
	Show(&buf, suspiciousIssue(), nil, Style{})
	checkGolden(t, "show_findings", buf.Bytes())
}

func TestJSONShow_TextFindings(t *testing.T) {
	var buf bytes.Buffer
	if err := JSONShow(&buf, suspiciousIssue(), nil); err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "show_findings_json", buf.Bytes())
}

func TestShow_TextFindingsNameEachSource(t *testing.T) {
	var buf bytes.Buffer
	Show(&buf, suspiciousIssue(), nil, Style{})
	out := buf.String()
	for _, want := range []string{
		"  title: contains confusable characters\n",
		"  body: contains zero-width characters\n",
		"  comment 2 (@mallory): contains bidi controls; contains Unicode tags\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Show missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "comment 1 (@alice)") {
		t.Errorf("clean emoji comment was flagged:\n%s", out)
	}
	// The cap is untouched: findings index the comments shown, and the header
	// still says how many exist.
	if !strings.Contains(out, "comments (showing last 2 of 5):") {
		t.Errorf("capped-comment header changed:\n%s", out)
	}
}

func TestJSONShow_TextFindingsNameEachSource(t *testing.T) {
	var buf bytes.Buffer
	if err := JSONShow(&buf, suspiciousIssue(), nil); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Comments      []CommentJSON     `json:"comments"`
		CommentsTotal int               `json:"commentsTotal"`
		TextFindings  []TextFindingJSON `json:"textFindings"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	commentIndex := 1
	want := []TextFindingJSON{
		{Source: model.SourceTitle, TextFindings: model.TextFindings{Confusable: true}},
		{Source: model.SourceBody, TextFindings: model.TextFindings{ZeroWidth: true}},
		{Source: model.SourceComment, CommentIndex: &commentIndex, TextFindings: model.TextFindings{BidiControl: true, UnicodeTags: true}},
	}
	if len(got.TextFindings) != len(want) {
		t.Fatalf("textFindings = %+v, want %+v", got.TextFindings, want)
	}
	for n, f := range got.TextFindings {
		w := want[n]
		if f.Source != w.Source || f.TextFindings != w.TextFindings ||
			(f.CommentIndex == nil) != (w.CommentIndex == nil) ||
			(f.CommentIndex != nil && *f.CommentIndex != *w.CommentIndex) {
			t.Errorf("finding %d = %+v, want %+v", n, f, w)
		}
	}
	// commentIndex resolves against the comments array actually emitted.
	if len(got.Comments) != 2 || got.Comments[commentIndex].Author != "mallory" || got.CommentsTotal != 5 {
		t.Errorf("comments = %+v (total %d)", got.Comments, got.CommentsTotal)
	}
}

func TestShow_CleanIssueHasNoFindings(t *testing.T) {
	clean := model.Issue{
		Number: 1, Title: "Clean 👩‍💻 report", State: "OPEN", CreatedAt: ts(1),
		Body:     "café “quoted” — ✅ 🇨🇦",
		Comments: []model.Comment{{Author: "alice", CreatedAt: ts(2), Body: "👍🏽 thanks"}},
	}
	var buf bytes.Buffer
	Show(&buf, clean, nil, Style{})
	if strings.Contains(buf.String(), "text findings") || strings.Contains(buf.String(), "contains ") {
		t.Errorf("clean issue was flagged:\n%s", buf.String())
	}
	buf.Reset()
	if err := JSONShow(&buf, clean, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "textFindings") {
		t.Errorf("clean issue carries textFindings:\n%s", buf.String())
	}
}

func TestShow_FindingsKeepContentInspectable(t *testing.T) {
	// Findings annotate; they never strip or rewrite. The suspicious runes
	// still reach the output, and the existing control neutralization still
	// applies — including to the comment author echoed in a finding.
	i := hostileIssueWith(hostileValidUTF8 + "\u200b\u0430")
	var buf bytes.Buffer
	Show(&buf, i, nil, Style{})
	out := buf.String()
	assertNeutralized(t, "Show", out)
	for _, want := range []string{
		SanitizeInline(i.Title),
		"  title: contains zero-width characters; contains confusable characters\n",
		"  body: contains zero-width characters; contains confusable characters\n",
		"  comment 1 (@" + SanitizeInline(i.Comments[0].Author) + "): contains zero-width characters; contains confusable characters\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Show missing %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "\u200b\u0430") < 3 {
		t.Errorf("suspicious runes were removed from title, body or comment:\n%s", out)
	}
	buf.Reset()
	if err := JSONShow(&buf, i, nil); err != nil {
		t.Fatal(err)
	}
	var got IssueJSON
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Title != i.Title || got.Body == nil || *got.Body != i.Body || got.Comments[0].Body != i.Comments[0].Body {
		t.Errorf("JSON rewrote suspicious text: %s", buf.String())
	}
}

func TestJSONShow_KeepsEpicNext(t *testing.T) {
	epic := model.Issue{
		Number: 137, Title: "Epic: Voltgo", State: "OPEN", CreatedAt: ts(5),
		SubIssuesTotal: 1, SubIssues: []model.Ref{{Number: 120, State: "OPEN"}},
	}
	children := []model.Issue{{Number: 120, Title: "Open child", State: "OPEN", CreatedAt: ts(4), Labels: []string{"P1", "bug"}}}
	var buf bytes.Buffer
	if err := JSONShow(&buf, epic, children); err != nil {
		t.Fatal(err)
	}
	var got IssueJSON
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Next == nil || *got.Next != 120 {
		t.Errorf("next = %v, want 120: %s", got.Next, buf.String())
	}
}

func TestJSONIssue_MutationsCarryNoFindings(t *testing.T) {
	// JSONIssue backs write-command output (start, set, ...), which stays as
	// it was: only the explicit show read annotates.
	var buf bytes.Buffer
	if err := JSONIssue(&buf, suspiciousIssue(), nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "textFindings") {
		t.Errorf("JSONIssue carries textFindings:\n%s", buf.String())
	}
}

func TestJSONShowWriterError(t *testing.T) {
	want := errors.New("write failed")
	if got := JSONShow(triageErrorWriter{want}, suspiciousIssue(), nil); !errors.Is(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
