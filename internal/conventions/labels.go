// Package conventions holds the tool's opinions: the label set, the issue
// body template, and the static primer text.
package conventions

import "github.com/lumberbarons/hew/internal/model"

// Label describes one label in the bootstrap set created by `hew init`.
type Label struct {
	Name        string
	Color       string // hex, no leading #
	Description string
}

// labelStyle is the cosmetic half conventions owns — color and description —
// keyed by the label name that model defines. model.LabelVocabulary is the
// source of truth for which names exist; adding one there surfaces it here
// (and fails TestLabelStylesCoverVocabulary until a style is added).
var labelStyle = map[string]struct{ Color, Description string }{
	"P0":                  {"b60205", "Critical: drop everything"},
	"P1":                  {"d93f0b", "High: next up"},
	"P2":                  {"fbca04", "Normal (default)"},
	"P3":                  {"0e8a16", "Low: nice to have"},
	"P4":                  {"c2e0c6", "Backlog: someday"},
	"bug":                 {"d73a4a", "Something isn't working"},
	"enhancement":         {"a2eeef", "New feature or improvement"},
	"task":                {"bfdadc", "Chore, refactor, or process work"},
	model.InProgressLabel: {"1d76db", "Actively being worked (claimed)"},
}

// Labels is the convention label set `hew init` bootstraps: model's
// vocabulary (names) plus the cosmetics above. init creates the missing ones;
// color/description drift on labels that already exist is left alone — names
// are the contract, cosmetics are the repo owner's.
var Labels = buildLabels()

func buildLabels() []Label {
	names := model.LabelVocabulary()
	out := make([]Label, 0, len(names))
	for _, name := range names {
		s := labelStyle[name]
		out = append(out, Label{Name: name, Color: s.Color, Description: s.Description})
	}
	return out
}
