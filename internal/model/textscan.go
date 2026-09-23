package model

import (
	"slices"
	"unicode"

	"github.com/rivo/uniseg"
)

// TextFindings flags characters that deserve a human look during triage or
// a show read.
// Findings are advisory: legitimate language and mathematical notation can
// contain them too. No text is normalized, hidden, or rejected.
type TextFindings struct {
	ZeroWidth   bool `json:"zeroWidth"`
	BidiControl bool `json:"bidiControl"`
	UnicodeTags bool `json:"unicodeTags"`
	Confusable  bool `json:"confusable"`
}

var asciiConfusableRunes = []rune(asciiConfusables)

// Sources a SourcedFindings can name.
const (
	SourceTitle   = "title"
	SourceBody    = "body"
	SourceComment = "comment"
)

// SourcedFindings attributes findings to the one field they were found in.
type SourcedFindings struct {
	Source string
	// Comment indexes Issue.Comments — the comments actually fetched and
	// displayed, not the server-side thread — when Source is SourceComment.
	Comment int
	TextFindings
}

// ScanIssue scans the title, the body and each fetched comment on its own,
// returning one entry per field with findings, in display order. It is the
// detail view's form of ScanText: the same checks, but attributed, so a
// reader knows which part of the issue to look at. A clean issue yields nil.
func ScanIssue(i Issue) []SourcedFindings {
	var out []SourcedFindings
	add := func(source string, comment int, text string) {
		var f TextFindings
		scanInto(&f, text)
		if f != (TextFindings{}) {
			out = append(out, SourcedFindings{Source: source, Comment: comment, TextFindings: f})
		}
	}
	add(SourceTitle, 0, i.Title)
	add(SourceBody, 0, i.Body)
	for idx, c := range i.Comments {
		add(SourceComment, idx, c.Body)
	}
	return out
}

// ScanText scans title and body independently and combines their findings.
// ASCII lookalikes use the pinned Unicode confusables subset in textscan_data.go;
// this is an advisory scan of prose, not a UTS #39 identifier validator.
// Recognized emoji clusters are exempt so their joiners, selectors and flag
// tags don't trigger warnings. Extra hidden characters outside those exact
// sequences still do. Canonical decomposition alone is not suspicious:
// decomposed accents are ordinary text, so we deliberately do not flag non-NFC.
func ScanText(title, body string) TextFindings {
	var findings TextFindings
	scanInto(&findings, title)
	scanInto(&findings, body)
	return findings
}

// scanInto adds one text's findings to findings. Each text is segmented on
// its own, so an emoji exemption never spans two fields.
func scanInto(findings *TextFindings, text string) {
	clusters := uniseg.NewGraphemes(text)
	for clusters.Next() {
		cluster := clusters.Str()
		if _, ok := scanEmoji[cluster]; ok {
			continue
		}
		for _, r := range cluster {
			switch {
			case unicode.Is(unicode.Bidi_Control, r):
				findings.BidiControl = true
			case r >= 0xe0000 && r <= 0xe007f:
				findings.UnicodeTags = true
			case unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r) || unicode.Is(unicode.Variation_Selector, r):
				findings.ZeroWidth = true
			}
			if r > unicode.MaxASCII {
				if _, found := slices.BinarySearch(asciiConfusableRunes, r); found {
					findings.Confusable = true
				}
			}
		}
	}
}
