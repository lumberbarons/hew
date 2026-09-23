package model

import (
	"fmt"
	"slices"
	"testing"
)

func TestScanText(t *testing.T) {
	zero := TextFindings{ZeroWidth: true}
	bidi := TextFindings{BidiControl: true}
	tags := TextFindings{UnicodeTags: true}
	confusable := TextFindings{Confusable: true}
	tests := []struct {
		name string
		text string
		want TextFindings
	}{
		{"empty", "", TextFindings{}},
		{"English and Markdown", "### Fix\n\nDon't retry `http.Get`—it's “done”.\n- [ ] #123\tOK", TextFindings{}},
		{"accented and CJK", "café cafe\u0301 日本語 中文", TextFindings{}},
		{"normal whitespace", "space\u00a0no-break\n\r\ttab", TextFindings{}},
		{"emoji", "✅ 🚀 👍🏽 🇨🇦 1️⃣ #️⃣ ❤️ ☕ ℹ️", TextFindings{}},
		{"joined emoji", "👩🏽‍💻 👨‍👩‍👧‍👦 🏳️‍🌈 🐦‍🔥 🙂‍↔️", TextFindings{}},
		{"England flag", "\U0001f3f4\U000e0067\U000e0062\U000e0065\U000e006e\U000e0067\U000e007f", TextFindings{}},
		{"zero width space", "ig\u200bnore", zero},
		{"non-joiner", "ig\u200cnore", zero},
		{"joiner in prose", "ig\u200dnore", zero},
		{"word joiner", "ig\u2060nore", zero},
		{"BOM", "\ufeffignore", zero},
		{"soft hyphen", "ig\u00adnore", zero},
		{"grapheme joiner", "ig\u034fnore", zero},
		{"invisible separator", "ig\u2063nore", zero},
		{"Hangul filler", "a\u3164b", zero},
		{"Mongolian vowel separator", "a\u180eb", zero},
		{"standalone selector", "\ufe0f", zero},
		{"selector in prose", "ignore\ufe0f", zero},
		{"supplementary selector payload", "x\U000e0100\U000e01ef", zero},
		{"right-to-left override", "a\u202eb", bidi},
		{"isolate", "a\u2067b\u2069", bidi},
		{"direction mark", "a\u200fb", bidi},
		{"Arabic letter mark", "a\u061cb", bidi},
		{"language tag", "a\U000e0001b", tags},
		{"tag payload", "a\U000e0069\U000e0067\U000e006e\U000e006f\U000e0072\U000e0065\U000e007fb", tags},
		{"Cyrillic lookalike", "p\u0430ypal", confusable},
		{"Greek lookalike", "l\u03bfgin", confusable},
		{"whole-script lookalikes", "\u0440\u0430\u0443\u0440\u0430\u04cf", confusable},
		{"mathematical alphabet", "\U0001d422gnore", confusable},
		{"fullwidth lookalike", "\uff21dmin", confusable},
		{"extra joiner after emoji", "👩‍💻\u200d", zero},
		{"emoji does not hide prose joiner", "👩\u200dignore", zero},
		{"emoji does not hide tag payload", "🚀\U000e0069\U000e007f", tags},
		{"malformed flag tags", "\U0001f3f4\U000e0069\U000e007f", tags},
		{"extra selector after emoji", "❤️\ufe0f", zero},
		{"all findings", "\u200b\u202e\U000e0069\u0430", TextFindings{true, true, true, true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Every category must work in the body too, even though triage
			// only displays titles. No cross-field emoji exemption is allowed.
			for _, fields := range [][2]string{{tt.text, ""}, {"", tt.text}, {tt.text, tt.text}} {
				if got := ScanText(fields[0], fields[1]); got != tt.want {
					t.Errorf("ScanText(%q, %q) = %+v, want %+v", fields[0], fields[1], got, tt.want)
				}
			}
		})
	}
}

func TestScanTextBidiControls(t *testing.T) {
	for _, r := range []rune{0x061c, 0x200e, 0x200f, 0x202a, 0x202b, 0x202c, 0x202d, 0x202e, 0x2066, 0x2067, 0x2068, 0x2069} {
		t.Run(fmt.Sprintf("U+%04X", r), func(t *testing.T) {
			if got := ScanText("a"+string(r)+"b", ""); got != (TextFindings{BidiControl: true}) {
				t.Errorf("got %+v", got)
			}
		})
	}
}

func TestScanTextFieldsStaySeparate(t *testing.T) {
	got := ScanText("👩", "\u200d💻\u202e\U000e0069\u0430")
	if got != (TextFindings{true, true, true, true}) {
		t.Errorf("got %+v", got)
	}
}

func TestScanTextRecognizesEmojiSequences(t *testing.T) {
	// Check the grapheme segmenter's compatibility with every pinned Unicode
	// emoji exception, including newer sequences and qualification variants.
	for emoji := range scanEmoji {
		if got := ScanText("Before "+emoji+" after", ""); got != (TextFindings{}) {
			t.Errorf("emoji %q: %+v", emoji, got)
		}
	}
}

func TestScanIssue_AttributesFindingsToEachField(t *testing.T) {
	i := Issue{
		Title: "p\u0430ypal",
		Body:  "a\u202eb",
		Comments: []Comment{
			{Author: "alice", Body: "clean 👩‍💻"},
			{Author: "mallory", Body: "ig\u200bnore\U000e0069"},
		},
	}
	want := []SourcedFindings{
		{Source: SourceTitle, TextFindings: TextFindings{Confusable: true}},
		{Source: SourceBody, TextFindings: TextFindings{BidiControl: true}},
		{Source: SourceComment, Comment: 1, TextFindings: TextFindings{ZeroWidth: true, UnicodeTags: true}},
	}
	if got := ScanIssue(i); !slices.Equal(got, want) {
		t.Errorf("ScanIssue = %+v, want %+v", got, want)
	}
}

func TestScanIssue_CommentIndexCountsDisplayedComments(t *testing.T) {
	// Only the fetched comments are scanned, and the index points into that
	// slice — not the server-side thread — so a capped fetch still resolves.
	i := Issue{
		Comments:      []Comment{{Body: "a\u202eb"}, {Body: "clean"}, {Body: "\u0430"}},
		CommentsTotal: 12,
	}
	want := []SourcedFindings{
		{Source: SourceComment, Comment: 0, TextFindings: TextFindings{BidiControl: true}},
		{Source: SourceComment, Comment: 2, TextFindings: TextFindings{Confusable: true}},
	}
	if got := ScanIssue(i); !slices.Equal(got, want) {
		t.Errorf("ScanIssue = %+v, want %+v", got, want)
	}
}

func TestScanIssue_CleanIssueHasNoFindings(t *testing.T) {
	i := Issue{
		Title:    "Clean 👩‍💻 report",
		Body:     "café “quoted” — ✅ 🇨🇦",
		Comments: []Comment{{Body: "👍🏽 looks good"}},
	}
	if got := ScanIssue(i); got != nil {
		t.Errorf("ScanIssue = %+v, want nil", got)
	}
}

func TestScanIssue_EmojiExemptionDoesNotCrossFields(t *testing.T) {
	// A joiner that completes an emoji only across a field boundary is still
	// a finding in the field that carries it.
	i := Issue{Title: "👩", Body: "\u200d💻"}
	want := []SourcedFindings{{Source: SourceBody, TextFindings: TextFindings{ZeroWidth: true}}}
	if got := ScanIssue(i); !slices.Equal(got, want) {
		t.Errorf("ScanIssue = %+v, want %+v", got, want)
	}
}
