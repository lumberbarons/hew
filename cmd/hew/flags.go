package main

import (
	"strings"

	ucli "github.com/urfave/cli/v3"
)

// urfave/cli v3 splits slice-flag values on commas, and the setting that
// disables it (Command.DisableSliceFlagSeparator) is per-command, not
// per-flag. That split is wanted for the flags whose values are lists of
// short tokens — `--area a,b`, `--children 1,2` — but wrong for a flag whose
// values are prose, where a comma is just punctuation: `--done-when "alpha,
// beta"` silently became two checklist items (#47). proseSliceFlag is a
// string-slice flag that takes each occurrence verbatim, so the two can
// coexist in the same command.
type proseSliceFlag = ucli.FlagBase[[]string, ucli.NoConfig, proseSlice]

// proseSlice is both the ValueCreator for proseSliceFlag and the flag.Value
// it creates, mirroring how urfave's own SliceBase is structured.
type proseSlice struct {
	slice      *[]string
	hasBeenSet bool
}

func (proseSlice) Create(val []string, p *[]string, _ ucli.NoConfig) ucli.Value {
	*p = append([]string{}, val...)
	return &proseSlice{slice: p}
}

func (proseSlice) ToString(vals []string) string { return strings.Join(vals, ", ") }

// Set appends the value as a single element. The first occurrence clears any
// default, matching urfave's SliceBase semantics.
func (p *proseSlice) Set(value string) error {
	if !p.hasBeenSet {
		*p.slice = []string{}
		p.hasBeenSet = true
	}
	*p.slice = append(*p.slice, value)
	return nil
}

func (p *proseSlice) String() string {
	if p.slice == nil {
		return ""
	}
	return strings.Join(*p.slice, ", ")
}

func (p *proseSlice) Get() any {
	if p.slice == nil {
		return []string(nil)
	}
	return *p.slice
}
