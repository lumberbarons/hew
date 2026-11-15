package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	ucli "github.com/urfave/cli/v3"

	appcli "github.com/lumberbarons/hew/internal/cli"
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

// closedOverrideFlag is the escape hatch shared by the write commands that
// refuse a closed target (#39). Declared once so the three commands cannot
// drift apart in name or wording — the refusal message names the flag, and a
// command spelling it differently would send the caller somewhere that does
// not exist.
func closedOverrideFlag() ucli.Flag {
	return &ucli.BoolFlag{Name: "closed", Usage: "edit the issue even though it is closed"}
}

// Renamed flags keep their old spelling working (DESIGN.md, "CLI surface
// compatibility"), but as a hidden flag of its own rather than an alias of the
// replacement: an alias answers to both names through one value, so nothing
// could tell which spelling was typed, and the note saying what replaced it
// has to fire only for the old one. Hidden keeps --help teaching one
// vocabulary; the usage text is still what names the replacement.

// deprecatedFlag is the hidden spelling of a renamed string flag.
func deprecatedStringFlag(name, use string) ucli.Flag {
	return &ucli.StringFlag{Name: name, Hidden: true, Usage: "deprecated: use " + use}
}

// deprecationNote tells the caller, on stderr, that the spelling they typed
// has a replacement. It goes to the framework's error writer so a test (or a
// caller that redirected it) sees the same stream as every other message.
func deprecationNote(cmd *ucli.Command, old, use string) {
	var w io.Writer = os.Stderr
	if ew := cmd.Root().ErrWriter; ew != nil {
		w = ew
	}
	fmt.Fprintf(w, "⚠ --%s is deprecated; use %s\n", old, use)
}

// noteDeprecated is the Before hook for a deprecated flag that stands alone:
// say what replaced it when it is used.
func noteDeprecated(old, use string) ucli.BeforeFunc {
	return func(ctx context.Context, cmd *ucli.Command) (context.Context, error) {
		if cmd.IsSet(old) {
			deprecationNote(cmd, old, use)
		}
		return ctx, nil
	}
}

// before runs hooks in order, stopping at the first error. Validation
// belongs here, ahead of the action, so malformed input cannot reach GitHub
// or the filesystem.
func before(hooks ...ucli.BeforeFunc) ucli.BeforeFunc {
	return func(ctx context.Context, cmd *ucli.Command) (context.Context, error) {
		for _, hook := range hooks {
			var err error
			if ctx, err = hook(ctx, cmd); err != nil {
				return ctx, err
			}
		}
		return ctx, nil
	}
}

// checkpointFlag names the file a batch writer (apply, migrate beads, migrate
// local) resumes from. Declared once so the three commands cannot drift apart
// in name or meaning; each says what its own default is.
func checkpointFlag(binding, defaultPath string) ucli.Flag {
	return &ucli.StringFlag{Name: "checkpoint", Usage: "resume checkpoint `FILE`, bound to this repo and " + binding + " (default: " + defaultPath + ")"}
}

// checkpointBefore guards the checkpoint flag and its deprecated spelling
// --state, which was a resume file here and an issue state on list.
func checkpointBefore(ctx context.Context, cmd *ucli.Command) (context.Context, error) {
	if !cmd.IsSet("state") {
		return ctx, nil
	}
	if cmd.IsSet("checkpoint") {
		return ctx, appcli.UsageError("--state is the deprecated spelling of --checkpoint; pass only --checkpoint")
	}
	deprecationNote(cmd, "state", "--checkpoint")
	return ctx, nil
}

// checkpointPath is the checkpoint file the caller named, by either spelling;
// empty means the command's default.
func checkpointPath(cmd *ucli.Command) string {
	if cmd.IsSet("state") && !cmd.IsSet("checkpoint") {
		return cmd.String("state")
	}
	return cmd.String("checkpoint")
}

// inputBefore validates the two ways apply and migrate beads take their
// input — as an argument or with --file — so both read alike. required says
// whether naming neither is an error (apply) or falls back to a default
// (migrate beads).
func inputBefore(required bool) ucli.BeforeFunc {
	return func(ctx context.Context, cmd *ucli.Command) (context.Context, error) {
		if cmd.IsSet("file") {
			if cmd.String("file") == "" {
				return ctx, appcli.UsageError("--file needs a file name")
			}
			if cmd.Args().Present() {
				return ctx, appcli.UsageError("%s takes its input as an argument or with --file, not both", cmd.FullName())
			}
			return ctx, nil
		}
		if required && !cmd.Args().Present() {
			return ctx, appcli.UsageError("usage: %s (or --file FILE)", strings.TrimSpace(cmd.FullName()+" "+cmd.ArgsUsage))
		}
		return ctx, nil
	}
}

// inputFile is the input the caller named, as an argument or with --file;
// empty means they named neither.
func inputFile(cmd *ucli.Command) string {
	if cmd.IsSet("file") {
		return cmd.String("file")
	}
	return cmd.Args().First()
}
