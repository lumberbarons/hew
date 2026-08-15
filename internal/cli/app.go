// Package cli implements the commands against the gh.Client interface, so
// cmd/hew is pure wiring and everything with behavior is testable
// against a fake.
package cli

import (
	"fmt"
	"io"

	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/git"
	"github.com/lumberbarons/hew/internal/model"
	"github.com/lumberbarons/hew/internal/render"
)

// Exit codes, kept meaningful so agent loops can branch on them.
const (
	ExitGeneric = 1
	ExitUsage   = 2
	// ExitClaimed is start's "already claimed" refusal: pick the next
	// ready item instead of doubling up.
	ExitClaimed = 3
	ExitAuth    = 4
	// ExitClaimedByYou splits the claim refusal: the claimant is the
	// authenticated user, so the work is already yours and the response is
	// to resume it, not to pick a different item. The guard is unchanged —
	// this only says whose claim blocked it.
	ExitClaimedByYou = 5
)

// ExitError carries a specific exit code to main.
type ExitError struct {
	Code    int
	Message string
}

func (e *ExitError) Error() string { return e.Message }

func usageErr(format string, args ...any) *ExitError {
	return &ExitError{Code: ExitUsage, Message: fmt.Sprintf(format, args...)}
}

// UsageError lets command wiring report a usage failure with the same exit
// contract as command-layer validation.
func UsageError(format string, args ...any) *ExitError { return usageErr(format, args...) }

func genericErr(format string, args ...any) *ExitError {
	return &ExitError{Code: ExitGeneric, Message: fmt.Sprintf(format, args...)}
}

// App holds the dependencies every command needs.
type App struct {
	Client gh.Client
	Repo   gh.Repo
	Out    io.Writer
	ErrOut io.Writer
	JSON   bool
	// Edit opens an editor seeded with initial text and returns the result;
	// wired to $EDITOR by main, injected by tests.
	Edit func(initial string) (string, error)
	// Git reads the local branch state `pr` opens from; wired to the git
	// binary by main, injected by tests.
	Git func() (git.State, error)
}

func (a *App) printf(format string, args ...any) {
	fmt.Fprintf(a.Out, format, args...)
}

func (a *App) warnf(format string, args ...any) {
	fmt.Fprintf(a.ErrOut, "⚠ "+format+"\n", args...)
}

// progressf narrates long-running work: stdout normally, stderr under
// --json so the machine-readable stream stays clean.
func (a *App) progressf(format string, args ...any) {
	if a.JSON {
		fmt.Fprintf(a.ErrOut, format, args...)
		return
	}
	fmt.Fprintf(a.Out, format, args...)
}

// The --json contract lives here so every command honors it identically
// instead of re-deciding per command:
//   - an issue collection  → NDJSON, one compact object per line, so the
//     stream stays parseable under head/grep and output truncation;
//   - a single issue       → one indented JSON object;
//   - a command result     → one indented JSON object.
//
// Text output is the human form of the same data. Commands describe what to
// emit through these helpers rather than branching on a.JSON themselves.

// emitList renders an issue collection: NDJSON under --json, otherwise the
// given text renderer, or emptyMsg when there are none.
func (a *App) emitList(issues []model.Issue, emptyMsg string, renderText func(io.Writer, []model.Issue)) error {
	return a.emitListBodies(issues, emptyMsg, renderText, false)
}

// emitListBodies is emitList with the body carried on every NDJSON line
// (list --bodies); text output is unaffected.
func (a *App) emitListBodies(issues []model.Issue, emptyMsg string, renderText func(io.Writer, []model.Issue), withBodies bool) error {
	if a.JSON {
		return render.JSONList(a.Out, issues, withBodies)
	}
	if len(issues) == 0 {
		a.printf("%s\n", emptyMsg)
		return nil
	}
	renderText(a.Out, issues)
	return nil
}

// emitIssue renders one issue in full.
func (a *App) emitIssue(issue model.Issue) error {
	if a.JSON {
		return render.JSONIssue(a.Out, issue)
	}
	render.Show(a.Out, issue)
	return nil
}

// emitEpicStatus renders one epic and its children.
func (a *App) emitEpicStatus(epic model.Issue, children []model.Issue) error {
	if a.JSON {
		return render.JSONEpicStatus(a.Out, epic, children)
	}
	render.EpicStatus(a.Out, epic, children)
	return nil
}

// emitEpicStatus and emitList/emitIssue cover reads; the rest cover writes.

// emitMutation reports a mutation whose resulting issue is already loaded:
// the full issue as JSON, otherwise the given text line.
func (a *App) emitMutation(issue model.Issue, format string, args ...any) error {
	if a.JSON {
		return render.JSONIssue(a.Out, issue)
	}
	a.printf(format, args...)
	return nil
}

// emitPrime renders the session-start primer.
func (a *App) emitPrime(static string, d render.PrimeData) error {
	if a.JSON {
		return render.JSONPrime(a.Out, d)
	}
	render.Prime(a.Out, static, d)
	return nil
}

// emitResult reports a non-issue command outcome: an indented JSON object
// under --json, otherwise the text written by writeText.
func (a *App) emitResult(jsonObj any, writeText func()) error {
	if a.JSON {
		return render.WriteJSON(a.Out, jsonObj)
	}
	writeText()
	return nil
}
