package model

import (
	"slices"
	"unicode"

	"github.com/rivo/uniseg"
)

// TextFindings flags characters that deserve a human look during triage.
// Findings are advisory: legitimate language and mathematical notation can
// contain them too. No text is normalized, hidden, or rejected.
type TextFindings struct {
	ZeroWidth   bool `json:"zeroWidth"`
	BidiControl bool `json:"bidiControl"`
	UnicodeTags bool `json:"unicodeTags"`
	Confusable  bool `json:"confusable"`
}

var asciiConfusableRunes = []rune(asciiConfusables)

// ScanText scans title and body independently and combines their findings.
// ASCII lookalikes use the pinned Unicode confusables subset in textscan_data.go;
// this is an advisory scan of prose, not a UTS #39 identifier validator.
// Recognized emoji clusters are exempt so their joiners, selectors and flag
// tags don't trigger warnings. Extra hidden characters outside those exact
// sequences still do. Canonical decomposition alone is not suspicious:
// decomposed accents are ordinary text, so we deliberately do not flag non-NFC.
func ScanText(title, body string) TextFindings {
	var findings TextFindings
	for _, text := range []string{title, body} {
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
	return findings
}
