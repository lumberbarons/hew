package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/lumberbarons/hew/internal/conventions"
)

// DraftOpts are the draft command's inputs: either section flags to compose
// (optionally into Out), or Check naming an existing draft to revalidate.
type DraftOpts struct {
	Sections conventions.Sections
	Out      string
	Check    string
}

// Draft composes or checks an issue body locally, before anything is
// published. create reports markup findings only after it has written the
// issue, which leaves the author correcting a body they have to fetch back;
// a draft file is corrected first and published as-is with create
// --body-file. It never touches a.Client, so it needs no credentials or
// network, and it never edits author text — findings are reported, not fixed.
func (a *App) Draft(_ context.Context, opts DraftOpts) error {
	if opts.Check != "" {
		return a.draftCheck(opts)
	}
	return a.draftCompose(opts)
}

func (a *App) draftCompose(opts DraftOpts) error {
	if err := validateBodySource(opts.Sections, "", false); err != nil {
		return err
	}
	if opts.Sections.IsZero() {
		return usageErr("nothing to compose; pass section flags (--where/--problem/--goal/--fix/--approach/--done-when), or --check FILE to revalidate a draft")
	}
	body, err := a.renderBody(bodySource{sections: opts.Sections})
	if err != nil {
		return err
	}
	body += "\n"

	tokens := conventions.UnmarkedCodeText(body)
	if tokens == nil {
		tokens = []string{}
	}
	if opts.Out == "" {
		return a.emitResult(map[string]any{"body": body, "unmarkedCode": tokens}, func() {
			a.warnUnmarkedCode(body, "fix the flag text and compose again")
			a.printf("%s", body)
		})
	}
	// O_EXCL: a draft that already exists may carry the author's revisions,
	// and recomposing from flags would silently discard them.
	f, err := os.OpenFile(opts.Out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return usageErr("%s already exists; edit it and run `hew draft --check %s`, or remove it to compose afresh", opts.Out, opts.Out)
	}
	if err != nil {
		return genericErr("cannot write --out: %v", err)
	}
	_, err = f.WriteString(body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		// A partial draft would block the retry (the file now exists) and
		// could be published truncated, so leave nothing behind.
		_ = os.Remove(opts.Out)
		return genericErr("cannot write --out: %v", err)
	}
	return a.emitResult(map[string]any{"file": opts.Out, "unmarkedCode": tokens}, func() {
		a.warnUnmarkedCode(body, fmt.Sprintf("edit %s, then `hew draft --check %s`", opts.Out, opts.Out))
		a.printf("wrote %s — publish with: hew create --body-file %s\n", opts.Out, opts.Out)
	})
}

func (a *App) draftCheck(opts DraftOpts) error {
	if !opts.Sections.IsZero() || opts.Out != "" {
		return usageErr("--check revalidates an existing draft; it cannot be combined with section flags or --out")
	}
	data, err := os.ReadFile(opts.Check)
	if err != nil {
		return genericErr("cannot read --check: %v", err)
	}
	body := string(data)
	if strings.TrimSpace(body) == "" {
		return usageErr("--check %s is empty; nothing to publish", opts.Check)
	}
	tokens := conventions.UnmarkedCodeText(body)
	if tokens == nil {
		tokens = []string{}
	}
	return a.emitResult(map[string]any{"file": opts.Check, "unmarkedCode": tokens}, func() {
		if len(tokens) == 0 {
			a.printf("%s: no unmarked code text — publish with: hew create --body-file %s\n", opts.Check, opts.Check)
			return
		}
		a.warnUnmarkedCode(body, fmt.Sprintf("edit %s, then `hew draft --check %s` again", opts.Check, opts.Check))
	})
}
