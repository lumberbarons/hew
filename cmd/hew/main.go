// Command hew is an agentic-first issue tracker CLI, on GitHub Issues or a
// local tracker. All behavior
// lives in internal/; this file is flag wiring — parsing, exit-code mapping,
// and editor invocation are delegated to internal packages so they can be
// tested.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	ucli "github.com/urfave/cli/v3"

	appcli "github.com/lumberbarons/hew/internal/cli"
	"github.com/lumberbarons/hew/internal/conventions"
	"github.com/lumberbarons/hew/internal/editor"
	"github.com/lumberbarons/hew/internal/gh"
	"github.com/lumberbarons/hew/internal/git"
	"github.com/lumberbarons/hew/internal/local"
	"github.com/lumberbarons/hew/internal/render"

	"github.com/cli/go-gh/v2/pkg/repository"
)

// version is stamped by goreleaser via ldflags.
var version = "dev"

func main() {
	os.Exit(run(context.Background(), os.Args, os.Stdout, os.Stderr))
}

// run executes hew and returns the process exit code, printing a failure to
// stderr exactly once. It is separate from main so the exit-code contract is
// testable end to end.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	cmd := root()
	cmd.Writer, cmd.ErrWriter = stdout, stderr
	// Without this the framework calls os.Exit itself for any ExitCoder it
	// raises, bypassing the exit-code mapping below.
	cmd.ExitErrHandler = func(context.Context, *ucli.Command, error) {}
	err := cmd.Run(ctx, args)
	if err == nil {
		return 0
	}
	fmt.Fprintln(stderr, err)
	return exitCode(err)
}

// exitCode is appcli.ExitCode plus the one thing only the framework can raise:
// an error that carries its own exit code (the help command's "No help topic"
// is exit 3). hew never builds one itself, so any that reaches here is a
// command-line mistake, and 3 is `start`'s "claimed by someone else" — it must
// read as usage (2) instead.
func exitCode(err error) int {
	// hew's own codes (start's 3, 5 and 6 among them) win over the framework
	// mapping, even if an ExitError ever grows an ExitCode method.
	var own *appcli.ExitError
	var coder ucli.ExitCoder
	if !errors.As(err, &own) && errors.As(err, &coder) {
		return appcli.ExitUsage
	}
	return appcli.ExitCode(err)
}

// usageError turns a flag-parse or required-flag failure into the usage exit
// code with one line and a pointer to --help. The framework's own handling
// prints the message twice, dumps the full help, and exits 1.
func usageError(_ context.Context, cmd *ucli.Command, err error, _ bool) error {
	return appcli.UsageError("%v; see `%s --help`", err, cmd.FullName())
}

// wireUsageErrors installs usageError on cmd and every command below it, since
// OnUsageError is looked up on the failing command alone.
func wireUsageErrors(cmd *ucli.Command) {
	cmd.OnUsageError = usageError
	for _, sub := range cmd.Commands {
		wireUsageErrors(sub)
	}
}

// groupAction is the action of a command that only dispatches to
// subcommands. With no further arguments it shows its help; with one, nothing
// matched it, so the framework default (help for an unknown topic, exit 3) is
// replaced by a usage error.
func groupAction(_ context.Context, cmd *ucli.Command) error {
	if cmd.Args().Present() {
		return appcli.UsageError("unknown command %q; see `%s --help`", cmd.Args().First(), cmd.FullName())
	}
	if cmd.Root() == cmd {
		// As the framework default does: a help-render failure is not a command failure.
		_ = ucli.ShowRootCommandHelp(cmd)
		return nil
	}
	return ucli.ShowSubcommandHelp(cmd)
}

// globalFlags are declared on the root command only. urfave/cli v3 flags
// are persistent by default, so they parse in either position (`hew
// --json ready` and `hew ready --json`) and leaf actions resolve them
// via lineage lookup. Re-declaring them on a leaf would shadow the root's
// parsed value with an empty one (#25), so leaves must not repeat them.
func globalFlags() []ucli.Flag {
	return []ucli.Flag{
		&ucli.BoolFlag{Name: "json", Usage: "structured output with a stable schema; list-shaped output is NDJSON (one object per line)"},
		&ucli.StringFlag{Name: "repo", Usage: "target `owner/name` (default: detect from git remote)"},
	}
}

// buildApp picks the backend. --repo always means GitHub. Otherwise a
// `.hew/config.json` at the main checkout that says local selects the local
// store, and a missing `.hew` or a config naming github is GitHub exactly as
// before. A `.hew` that looks like a local tracker without a usable config is
// an error from Locate, never GitHub.
func buildApp(cmd *ucli.Command) (*appcli.App, error) {
	if cmd.String("repo") == "" {
		tracker, isLocal, err := local.Locate("")
		if err != nil {
			return nil, err
		}
		if isLocal {
			return buildLocalApp(cmd, tracker)
		}
	}
	return buildGitHubApp(cmd, true)
}

// buildGitHubApp talks to GitHub whatever the checkout's config says: the
// repository is --repo, or the git remote's.
func buildGitHubApp(cmd *ucli.Command, offerLocal bool) (*appcli.App, error) {
	var repo gh.Repo
	if spec := cmd.String("repo"); spec != "" {
		parsed, err := appcli.ParseRepoSpec(spec)
		if err != nil {
			return nil, err
		}
		repo = parsed
	} else {
		current, err := repository.Current()
		if err != nil {
			return nil, appcli.RepoDetectError(err, offerLocal)
		}
		repo.Owner, repo.Name = current.Owner, current.Name
	}
	client, err := gh.New(repo)
	if err != nil {
		return nil, err
	}
	app := newApp(cmd)
	app.Client = client
	app.PRs = client
	app.Repo = repo
	return app, nil
}

// buildMigrateLocalApp is buildGitHubApp, except that a dry run with no target
// — no --repo and no git remote — gets an app with no client: the plan comes
// from issues.jsonl alone, and MigrateLocal refuses the one case that would
// need GitHub, a checkpoint to verify.
func buildMigrateLocalApp(cmd *ucli.Command) (*appcli.App, error) {
	if cmd.Bool("dry-run") && cmd.String("repo") == "" {
		if _, err := repository.Current(); err != nil {
			return newApp(cmd), nil
		}
	}
	return buildGitHubApp(cmd, false)
}

// buildLocalApp opens the local store at tracker. There are no pull requests
// to open, so PRs stays nil and `pr` refuses. The displayed repo is the main
// checkout's directory name, so every worktree of one repository reads alike.
func buildLocalApp(cmd *ucli.Command, tracker string) (*appcli.App, error) {
	store, err := local.Open(tracker, "")
	if err != nil {
		return nil, err
	}
	app := newApp(cmd)
	app.Client = store
	app.Repo = gh.Repo{Owner: "local", Name: filepath.Base(filepath.Dir(tracker))}
	// Checkpoints bind to the tracker's id, not the name above: the name is
	// what every worktree reads alike, and two checkouts can share it.
	app.RepoKey = "local/" + store.ID()
	app.Local = true
	return app, nil
}

// newApp carries what every backend shares: streams, output mode, and the
// injected editor and git state. The caller supplies the backend.
func newApp(cmd *ucli.Command) *appcli.App {
	return &appcli.App{
		Out:    os.Stdout,
		ErrOut: os.Stderr,
		JSON:   cmd.Bool("json"),
		// The TTY/NO_COLOR/TERM/FORCE_COLOR decision is made once, here,
		// against the real stdout; tests inject their own.
		Color: render.ColorEnabled(os.Stdout),
		Edit:  editor.Edit,
		Git:   git.Current,
	}
}

// numberArg parses the required positional issue number after fixedArgs has
// checked the command's argument count.
func numberArg(cmd *ucli.Command, usage string) (int, error) {
	return appcli.ParseIssueNumber(cmd.Args().First(), usage)
}

// fixedArgs wires validation ahead of app construction, so malformed input
// cannot reach GitHub or the filesystem. Search alone takes arbitrary words.
func fixedArgs(min, max int) ucli.BeforeFunc {
	return func(ctx context.Context, cmd *ucli.Command) (context.Context, error) {
		usage := strings.TrimSpace(cmd.FullName() + " " + cmd.ArgsUsage)
		if err := appcli.ValidateArgCount(cmd.Args().Slice(), min, max, usage); err != nil {
			return ctx, err
		}
		for _, name := range []string{"parent", "epic", "duplicate-of", "discovered-from", "on", "from"} {
			if cmd.IsSet(name) {
				if err := appcli.ValidateIssueNumbers(name, cmd.Int(name)); err != nil {
					return ctx, err
				}
			}
		}
		for _, name := range []string{"blocked-by", "children", "for"} {
			if cmd.IsSet(name) {
				if err := appcli.ValidateIssueNumbers(name, cmd.IntSlice(name)...); err != nil {
					return ctx, err
				}
			}
		}
		return ctx, nil
	}
}

func root() *ucli.Command {
	cmd := &ucli.Command{
		Name:    "hew",
		Usage:   "agentic-first issue tracker for coding agents, in GitHub Issues or a local .hew tracker",
		Version: version,
		Flags:   globalFlags(),
		Action:  groupAction,
		Commands: []*ucli.Command{
			primeCmd(), readyCmd(), listCmd(), showCmd(), searchCmd(),
			createCmd(), draftCmd(), startCmd(), triageCmd(), setCmd(), prCmd(), closeCmd(),
			reopenCmd(), blockCmd(), unblockCmd(), epicCmd(), applyCmd(), initCmd(),
			hooksCmd(), migrateCmd(),
		},
	}
	wireUsageErrors(cmd)
	return cmd
}

func primeCmd() *ucli.Command {
	return &ucli.Command{
		Name:  "prime",
		Usage: "session-start context: conventions, ready work, live state",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "hook-format", Usage: "emit for a session-start hook instead of text: `cursor` JSON-wraps the primer in additional_context"},
		},
		Before: fixedArgs(0, 0),
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			app, err := buildApp(cmd)
			if err != nil {
				return err
			}
			return app.Prime(ctx, appcli.PrimeOpts{HookFormat: cmd.String("hook-format")})
		},
	}
}

func readyCmd() *ucli.Command {
	return &ucli.Command{
		Name:  "ready",
		Usage: "open, non-epic, triaged issues with zero open blockers, priority-sorted",
		Flags: []ucli.Flag{
			&ucli.IntFlag{
				Name:  "limit",
				Value: appcli.DefaultReadyLimit,
				Usage: "print at most `N` issues (0 for all); truncation warns on stderr",
			},
		},
		Before: fixedArgs(0, 0),
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			app, err := buildApp(cmd)
			if err != nil {
				return err
			}
			return app.Ready(ctx, appcli.ReadyOpts{Limit: cmd.Int("limit")})
		},
	}
}

func listCmd() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "list issues (open by default)",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "label", Usage: "only issues with this label (case-insensitive)"},
			&ucli.IntFlag{Name: "epic", Usage: "only children of epic `N`"},
			&ucli.StringFlag{Name: "state", Usage: "issue state: `open`, closed, or all"},
			&ucli.BoolFlag{Name: "closed", Usage: "alias for --state closed"},
			&ucli.BoolFlag{Name: "bodies", Usage: "include issue bodies (requires --json) for single-call dedup"},
		},
		Before: fixedArgs(0, 0),
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			app, err := buildApp(cmd)
			if err != nil {
				return err
			}
			return app.List(ctx, appcli.ListOpts{
				Label:  cmd.String("label"),
				Epic:   cmd.Int("epic"),
				State:  cmd.String("state"),
				Closed: cmd.Bool("closed"),
				Bodies: cmd.Bool("bodies"),
			})
		},
	}
}

func showCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "show",
		Usage:     "issue detail: body, deps, parent, children, recent comments",
		ArgsUsage: "<n>",
		Before:    fixedArgs(1, 1),
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			n, err := numberArg(cmd, "hew show <n>")
			if err != nil {
				return err
			}
			app, err := buildApp(cmd)
			if err != nil {
				return err
			}
			return app.Show(ctx, n)
		},
	}
}

func searchCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "search",
		Usage:     "text search over open and closed issues — dedupe before filing",
		ArgsUsage: "<terms>",
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			app, err := buildApp(cmd)
			if err != nil {
				return err
			}
			return app.Search(ctx, strings.Join(cmd.Args().Slice(), " "))
		},
	}
}

// sectionFlags compose the issue body template. They are shared by create,
// epic create and draft so the three accept exactly the same section input.
func sectionFlags() []ucli.Flag {
	return []ucli.Flag{
		&ucli.StringFlag{Name: "where", Usage: "body section: where the work lives"},
		&ucli.StringFlag{Name: "problem", Usage: "body section: the problem (or use --goal)"},
		&ucli.StringFlag{Name: "goal", Usage: "body section: the goal (or use --problem)"},
		&ucli.StringFlag{Name: "fix", Usage: "body section: the fix (or use --approach)"},
		&ucli.StringFlag{Name: "approach", Usage: "body section: the approach (or use --fix)"},
		&proseSliceFlag{Name: "done-when", Usage: "acceptance checklist item (repeatable)"},
	}
}

// bodyFlags are the body paths shared by create and epic create: section
// flags compose the template; --body-file and --edit are the long-form
// escape hatches.
func bodyFlags() []ucli.Flag {
	return append(sectionFlags(),
		&ucli.StringFlag{Name: "body-file", Usage: "read the whole body from `FILE` (for long-form bodies)"},
		&ucli.BoolFlag{Name: "edit", Usage: "open $EDITOR seeded with the body template"},
	)
}

func sectionsFromFlags(cmd *ucli.Command) conventions.Sections {
	return conventions.Sections{
		Where:    cmd.String("where"),
		Problem:  cmd.String("problem"),
		Goal:     cmd.String("goal"),
		Fix:      cmd.String("fix"),
		Approach: cmd.String("approach"),
		DoneWhen: cmd.StringSlice("done-when"),
	}
}

func createCmd() *ucli.Command {
	return &ucli.Command{
		Name:        "create",
		Usage:       "file a new issue within the conventions",
		Description: conventions.CodeSpanGuidance + "\n\n" + draftPointer,
		Flags: append([]ucli.Flag{
			&ucli.StringFlag{Name: "title", Usage: "issue title (required)"},
			&ucli.StringFlag{Name: "type", Usage: "bug|enhancement|task (required)"},
			&ucli.StringFlag{Name: "priority", Usage: "P0..P4 (default P2)"},
			&ucli.StringSliceFlag{Name: "area", Usage: "area label (repeatable; commas separate areas)"},
			&ucli.IntSliceFlag{Name: "blocked-by", Usage: "blocking issue `N` (repeatable)"},
			&ucli.IntFlag{Name: "parent", Usage: "attach as sub-issue of epic `N`"},
			&ucli.IntFlag{Name: "discovered-from", Usage: "link back to existing issue `N` this was discovered under (closed is fine)"},
		}, bodyFlags()...),
		Before: fixedArgs(0, 0),
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			app, err := buildApp(cmd)
			if err != nil {
				return err
			}
			return app.Create(ctx, appcli.CreateOpts{
				Title:          cmd.String("title"),
				Type:           cmd.String("type"),
				Priority:       cmd.String("priority"),
				Areas:          cmd.StringSlice("area"),
				BlockedBy:      cmd.IntSlice("blocked-by"),
				Parent:         cmd.Int("parent"),
				DiscoveredFrom: cmd.Int("discovered-from"),
				Sections:       sectionsFromFlags(cmd),
				BodyFile:       cmd.String("body-file"),
				Edit:           cmd.Bool("edit"),
			})
		},
	}
}

// draftPointer is the create help's route to the local preflight, so a body
// can be corrected before the issue exists rather than after.
const draftPointer = "To check a body before publishing, compose it locally with `hew draft --out FILE`,\n" +
	"revise FILE, validate it with `hew draft --check FILE`, then `hew create --body-file FILE`."

func draftCmd() *ucli.Command {
	return &ucli.Command{
		Name:  "draft",
		Usage: "compose or check an issue body locally, before publishing",
		Description: `Composes an issue body from the same section flags create takes and writes it
to --out FILE (stdout without --out), or revalidates an existing draft with
--check FILE. Neither needs GitHub credentials or network access, and neither
writes to GitHub. Findings are reported, never auto-fixed: edit FILE, check it
again, then publish the same bytes with ` + "`hew create --body-file FILE`" + `. --out
refuses a file that already exists, so a revised draft is never overwritten.
Epic bodies compose the same way: use ` + "`hew epic create --body-file FILE`" + `.

` + conventions.CodeSpanGuidance,
		Flags: append(sectionFlags(),
			&ucli.StringFlag{Name: "out", Usage: "write the composed body to `FILE` instead of stdout"},
			&ucli.StringFlag{Name: "check", Usage: "revalidate the existing draft `FILE` instead of composing"},
		),
		Before: fixedArgs(0, 0),
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			// An explicitly empty value (an unset shell variable) must not
			// fall through to the other mode.
			for _, name := range []string{"out", "check"} {
				if cmd.IsSet(name) && cmd.String(name) == "" {
					return appcli.UsageError("--%s needs a file name", name)
				}
			}
			app := &appcli.App{Out: os.Stdout, ErrOut: os.Stderr, JSON: cmd.Bool("json"), Color: render.ColorEnabled(os.Stdout)}
			return app.Draft(ctx, appcli.DraftOpts{
				Sections: sectionsFromFlags(cmd),
				Out:      cmd.String("out"),
				Check:    cmd.String("check"),
			})
		},
	}
}

func startCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "start",
		Usage:     "claim an issue: assign @me + in-progress (refuses claimed issues: exit 3, or 5 when already yours; open blockers: exit 6)",
		ArgsUsage: "<n>",
		Description: "Claim and read in one call: --show follows the claim with the issue's detail (body,\n" +
			"dependencies, recent comments, text-findings annotations); --json also includes the\n" +
			"body, without the annotations. Either replaces a separate `hew show <n>` after claiming.",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "priority", Usage: "P0..P4; required when the issue is untriaged"},
			&ucli.BoolFlag{Name: "force", Usage: "steal an already-claimed issue, or claim one with open blockers"},
			&ucli.BoolFlag{Name: "show", Usage: "print the claimed issue's detail after the claim, as show would (--json includes the body but no annotations)"},
		},
		Before: fixedArgs(1, 1),
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			n, err := numberArg(cmd, "hew start <n>")
			if err != nil {
				return err
			}
			app, err := buildApp(cmd)
			if err != nil {
				return err
			}
			return app.Start(ctx, n, appcli.StartOpts{
				Priority: cmd.String("priority"),
				Force:    cmd.Bool("force"),
				Show:     cmd.Bool("show"),
			})
		},
	}
}

func triageCmd() *ucli.Command {
	return &ucli.Command{
		Name:  "triage",
		Usage: "untriaged issues (missing priority/type); --search dedups over them",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "search", Usage: "match untriaged issues by `terms`, open and closed"},
		},
		Before: fixedArgs(0, 0),
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			app, err := buildApp(cmd)
			if err != nil {
				return err
			}
			return app.Triage(ctx, appcli.TriageOpts{Search: cmd.String("search")})
		},
	}
}

func setCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "set",
		Usage:     "retriage/edit within conventions (swaps labels, never stacks)",
		ArgsUsage: "<n>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "priority", Usage: "P0..P4"},
			&ucli.StringFlag{Name: "type", Usage: "bug|enhancement|task"},
			&ucli.StringSliceFlag{Name: "add-area", Usage: "add area label (repeatable; commas separate areas)"},
			&ucli.StringSliceFlag{Name: "remove-area", Usage: "remove area label (repeatable; commas separate areas)"},
			&ucli.IntFlag{Name: "parent", Usage: "move under epic `N`"},
			&ucli.BoolFlag{Name: "no-parent", Usage: "detach from its epic"},
			&ucli.StringFlag{Name: "title", Usage: "new title"},
			&ucli.StringFlag{Name: "body-file", Usage: "replace the body with the contents of `FILE`"},
			closedOverrideFlag(),
		},
		Before: fixedArgs(1, 1),
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			n, err := numberArg(cmd, "hew set <n>")
			if err != nil {
				return err
			}
			app, err := buildApp(cmd)
			if err != nil {
				return err
			}
			return app.Set(ctx, n, appcli.SetOpts{
				Priority:    cmd.String("priority"),
				Type:        cmd.String("type"),
				AddAreas:    cmd.StringSlice("add-area"),
				RemoveAreas: cmd.StringSlice("remove-area"),
				Parent:      cmd.Int("parent"),
				NoParent:    cmd.Bool("no-parent"),
				Title:       cmd.String("title"),
				TitleSet:    cmd.IsSet("title"),
				BodyFile:    cmd.String("body-file"),
				AllowClosed: cmd.Bool("closed"),
			})
		},
	}
}

func prCmd() *ucli.Command {
	return &ucli.Command{
		Name:  "pr",
		Usage: "open a draft PR for the claimed issue, or revise it with --amend",
		Description: `Composes the PR the workflow prescribes: the linked issue is inferred
from what you have claimed (a number in the branch name breaks ties, and
--for <n> settles it outright), exactly one "Fixes #n" so the merge closes
the issue, and the base is the repo's default branch. Push the branch first —
GitHub can only open a PR for a ref it can see.

Repeat --for to declare a small set of coupled issues one PR closes together:
the body then carries one "Fixes #n" per declared issue, and a --body-file
must close exactly that set. Several issues are only ever declared, never
inferred. One issue per branch stays the default; a set gets no What/Why
defaults, since one issue's Fix does not describe the others.

` + conventions.PRBodyGuidance + `

Use --amend to revise the open PR on this branch. Omitted sections and the
title keep their existing values; an explicitly empty section flag removes
that section. --body-file replaces the whole body and appends missing tracker
trailers, just as on creation. Section edits require a losslessly readable
What/Why/Testing/Risk body without fenced code; use --body-file for other Markdown.
Amendment requires exactly one matching open PR whose head repository is this
repository. --ready and --base are creation-only flags.

` + conventions.CodeSpanGuidance,
		Flags: []ucli.Flag{
			&ucli.BoolFlag{Name: "amend", Usage: "revise the open PR title/body; preserve omitted sections"},
			&ucli.IntSliceFlag{Name: "for", Usage: "link issue `N` instead of inferring it; repeat to declare a set one PR closes together"},
			&ucli.StringFlag{Name: "title", Usage: "PR title (default: derived from the issue type/title; preserved with --amend)"},
			&proseSliceFlag{Name: "what", Usage: "body section: what the change does (default: the issue's Fix/Approach); repeat for a bullet list"},
			&proseSliceFlag{Name: "why", Usage: "body section: why (default: the issue's Problem/Goal); repeat for a bullet list"},
			&proseSliceFlag{Name: "testing", Usage: "body section: evidence — a before and after, not \"tests pass\"; repeat for a bullet list"},
			&proseSliceFlag{Name: "risk", Usage: "body section: one-way/two-way door + blast radius; repeat for a bullet list"},
			&ucli.StringFlag{Name: "body-file", Usage: "read the whole body from `FILE` (Fixes #n is appended if absent)"},
			&ucli.StringFlag{Name: "base", Usage: "target `BRANCH` (default: the repo's default branch)"},
			&ucli.BoolFlag{Name: "ready", Usage: "open for review instead of as a draft"},
		},
		Before: fixedArgs(0, 0),
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			app, err := buildApp(cmd)
			if err != nil {
				return err
			}
			return app.PR(ctx, appcli.PROpts{
				Amend:        cmd.Bool("amend"),
				SectionFlags: prSectionFlagsSet(cmd),
				For:          cmd.IntSlice("for"),
				Title:        cmd.String("title"),
				Sections:     prSectionsFromFlags(cmd),
				BodyFile:     cmd.String("body-file"),
				Base:         cmd.String("base"),
				Ready:        cmd.Bool("ready"),
			})
		},
	}
}

// prSectionsFromFlags reads the pr body sections. Every section flag is
// repeatable and compose a bullet list from several values; one value stays a
// plain paragraph.
func prSectionsFromFlags(cmd *ucli.Command) conventions.PRSections {
	return conventions.PRSections{
		What:    conventions.PRList(cmd.StringSlice("what")),
		Why:     conventions.PRList(cmd.StringSlice("why")),
		Testing: conventions.PRList(cmd.StringSlice("testing")),
		Risk:    conventions.PRList(cmd.StringSlice("risk")),
	}
}

// prSectionFlagsSet names the section flags given explicitly, empty values
// included, so --amend can tell "remove this section" from "leave it alone".
func prSectionFlagsSet(cmd *ucli.Command) []string {
	var set []string
	for _, name := range []string{"what", "why", "testing", "risk"} {
		if cmd.IsSet(name) {
			set = append(set, name)
		}
	}
	return set
}

func closeCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "close",
		Usage:     "comment + close (not-planned unless --completed or --duplicate-of)",
		ArgsUsage: "<n>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "reason", Usage: "closing comment (required unless --duplicate-of, which appends \"Duplicate of #N\" if the reason does not name it)"},
			&ucli.BoolFlag{Name: "completed", Usage: "close as completed"},
			&ucli.IntFlag{Name: "duplicate-of", Usage: "close as duplicate of existing issue `N` (not the issue itself)"},
		},
		Before: fixedArgs(1, 1),
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			n, err := numberArg(cmd, "hew close <n> --reason \"...\"")
			if err != nil {
				return err
			}
			app, err := buildApp(cmd)
			if err != nil {
				return err
			}
			return app.Close(ctx, n, cmd.String("reason"), cmd.Bool("completed"), cmd.Int("duplicate-of"))
		},
	}
}

func reopenCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "reopen",
		Usage:     "comment + reopen (no-op on an already-open issue)",
		ArgsUsage: "<n>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "reason", Usage: "reopening comment (required)"},
		},
		Before: fixedArgs(1, 1),
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			n, err := numberArg(cmd, "hew reopen <n> --reason \"...\"")
			if err != nil {
				return err
			}
			app, err := buildApp(cmd)
			if err != nil {
				return err
			}
			return app.Reopen(ctx, n, cmd.String("reason"))
		},
	}
}

func blockCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "block",
		Usage:     "add a native dependency (cycle-checked)",
		ArgsUsage: "<n> --on <m>",
		Flags: []ucli.Flag{
			&ucli.IntFlag{Name: "on", Usage: "blocking issue `N` (required)", Required: true},
			closedOverrideFlag(),
		},
		Before: fixedArgs(1, 1),
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			n, err := numberArg(cmd, "hew block <n> --on <m>")
			if err != nil {
				return err
			}
			app, err := buildApp(cmd)
			if err != nil {
				return err
			}
			return app.Block(ctx, n, cmd.Int("on"), cmd.Bool("closed"))
		},
	}
}

func unblockCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "unblock",
		Usage:     "remove a dependency",
		ArgsUsage: "<n> --from <m>",
		Flags: []ucli.Flag{
			&ucli.IntFlag{Name: "from", Usage: "blocking issue `N` (required)", Required: true},
			closedOverrideFlag(),
		},
		Before: fixedArgs(1, 1),
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			n, err := numberArg(cmd, "hew unblock <n> --from <m>")
			if err != nil {
				return err
			}
			app, err := buildApp(cmd)
			if err != nil {
				return err
			}
			return app.Unblock(ctx, n, cmd.Int("from"), cmd.Bool("closed"))
		},
	}
}

func epicCmd() *ucli.Command {
	return &ucli.Command{
		Name:   "epic",
		Usage:  "manage epics (sub-issue trees)",
		Action: groupAction,
		Commands: []*ucli.Command{
			{
				Name:  "create",
				Usage: "create a parent issue and attach children",
				Flags: append([]ucli.Flag{
					&ucli.StringFlag{Name: "title", Usage: "epic title (required)"},
					&ucli.IntSliceFlag{Name: "children", Usage: "existing issues to attach"},
				}, bodyFlags()...),
				Before: fixedArgs(0, 0),
				Action: func(ctx context.Context, cmd *ucli.Command) error {
					app, err := buildApp(cmd)
					if err != nil {
						return err
					}
					return app.EpicCreate(ctx, appcli.EpicCreateOpts{
						Title:    cmd.String("title"),
						Children: cmd.IntSlice("children"),
						Sections: sectionsFromFlags(cmd),
						BodyFile: cmd.String("body-file"),
						Edit:     cmd.Bool("edit"),
					})
				},
			},
			{
				Name:    "status",
				Aliases: []string{"list"},
				Usage:   "progress rollup for all epics, or one epic's children",
				Description: `With a number: the epic's rollup, the next workable child (next:),
then every child. Without one: one rollup line per open epic.`,
				ArgsUsage: "[<n>]",
				Before:    fixedArgs(0, 1),
				Action: func(ctx context.Context, cmd *ucli.Command) error {
					n := 0
					if cmd.Args().First() != "" {
						var err error
						if n, err = numberArg(cmd, "hew epic status [<n>]"); err != nil {
							return err
						}
					}
					app, err := buildApp(cmd)
					if err != nil {
						return err
					}
					return app.EpicStatus(ctx, n)
				},
			},
		},
	}
}

// hooksApp builds an App for the hooks commands, which touch only the
// local filesystem: no GitHub client, no repo detection.
func hooksApp(cmd *ucli.Command) (*appcli.App, string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, "", err
	}
	root, err := appcli.FindProjectRoot(cwd)
	if err != nil {
		return nil, "", err
	}
	return &appcli.App{Out: os.Stdout, ErrOut: os.Stderr, JSON: cmd.Bool("json"), Color: render.ColorEnabled(os.Stdout)}, root, nil
}

func hooksCmd() *ucli.Command {
	return &ucli.Command{
		Name:   "hooks",
		Usage:  "manage agent hooks that run `hew prime` (claude, codex, cursor, or opencode)",
		Action: groupAction,
		Commands: []*ucli.Command{
			{
				Name:      "install",
				Usage:     "add the hook for `claude`, `codex`, `cursor`, or `opencode`",
				ArgsUsage: "<claude|codex|cursor|opencode>",
				Before:    fixedArgs(1, 1),
				Action: func(ctx context.Context, cmd *ucli.Command) error {
					agent, err := hookAgentArg(cmd)
					if err != nil {
						return err
					}
					app, root, err := hooksApp(cmd)
					if err != nil {
						return err
					}
					return app.HooksInstall(root, agent)
				},
			},
			{
				Name:      "remove",
				Usage:     "remove the hook for `claude`, `codex`, `cursor`, or `opencode`",
				ArgsUsage: "<claude|codex|cursor|opencode>",
				Before:    fixedArgs(1, 1),
				Action: func(ctx context.Context, cmd *ucli.Command) error {
					agent, err := hookAgentArg(cmd)
					if err != nil {
						return err
					}
					app, root, err := hooksApp(cmd)
					if err != nil {
						return err
					}
					return app.HooksRemove(root, agent)
				},
			},
		},
	}
}

func hookAgentArg(cmd *ucli.Command) (appcli.HookAgent, error) {
	return appcli.ParseHookAgent(cmd.Args().First())
}

func applyCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "apply",
		Usage:     "batch-create issues from a JSONL plan file (dry-runnable, resumable)",
		ArgsUsage: "<plan.jsonl>",
		Description: `One JSON object per line, each an issue to create:

   {"id":"epic1","title":"Voltgo support","type":"epic","priority":"P1","goal":"..."}
   {"id":"scaffold","title":"Scaffold the driver","type":"task","parent":"epic1","done-when":["driver builds"]}
   {"title":"Collector","type":"task","parent":"epic1","blocked-by":["scaffold",42],"areas":["ble"]}

Fields and their shapes:
  title            string, required
  type             bug|enhancement|task|epic, required (epic means a parent
                   issue — no type label, "Epic: " title prefix)
  priority         P0..P4, default P2
  areas            list of strings
  id               string, a local name other entries refer to
  parent           one local id (string) or one existing issue number
  blocked-by       list of local ids and/or existing issue numbers
  discovered-from  one existing issue number, not a list
  where, problem, goal, fix, approach
                   strings, composed into the body template (problem or goal,
                   fix or approach, not both)
  done-when        list of strings, one checklist item each
  body             string of raw long-form text, instead of the section fields

Creation is checkpointed after every write, so a failed run resumes without
duplicates; dependency cycles between entries are rejected before anything is
written. The state file is local scratch bound to this repository and to a
digest of the plan: edit the plan, move repositories, or bring a state file
from elsewhere and it is refused rather than trusted.

` + conventions.CodeSpanGuidance,
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "state", Usage: "resume-state `FILE`, bound to this repo and plan (default: <plan>.state.json)"},
			&ucli.BoolFlag{Name: "dry-run", Usage: "print the plan without creating anything"},
			&ucli.DurationFlag{Name: "throttle", Usage: "pause between writes", Value: 500 * time.Millisecond},
		},
		Before: fixedArgs(1, 1),
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			app, err := buildApp(cmd)
			if err != nil {
				return err
			}
			return app.Apply(ctx, appcli.ApplyOpts{
				File:      cmd.Args().First(),
				StatePath: cmd.String("state"),
				DryRun:    cmd.Bool("dry-run"),
				Throttle:  cmd.Duration("throttle"),
			})
		},
	}
}

func migrateCmd() *ucli.Command {
	return &ucli.Command{
		Name:   "migrate",
		Usage:  "import issues from another tracker, or move the local tracker to GitHub",
		Action: groupAction,
		Commands: []*ucli.Command{
			{
				Name:  "beads",
				Usage: "migrate a beads (bd) database: labels, deps, epics, in-progress state",
				Flags: []ucli.Flag{
					&ucli.StringFlag{Name: "file", Usage: "beads snapshot (default: <project>/.beads/issues.jsonl)"},
					&ucli.StringFlag{Name: "state", Usage: "resume-state `FILE`, bound to this repo and snapshot (default: alongside the snapshot)"},
					&ucli.BoolFlag{Name: "dry-run", Usage: "print the plan without creating anything"},
					&ucli.BoolFlag{Name: "include-closed", Usage: "also migrate closed beads (create, comment, close)"},
					&ucli.DurationFlag{Name: "throttle", Usage: "pause between writes", Value: 500 * time.Millisecond},
				},
				Before: fixedArgs(0, 0),
				Action: func(ctx context.Context, cmd *ucli.Command) error {
					file := cmd.String("file")
					if file == "" {
						cwd, err := os.Getwd()
						if err != nil {
							return err
						}
						root, err := appcli.FindProjectRoot(cwd)
						if err != nil {
							return fmt.Errorf("%w (or pass --file)", err)
						}
						file = filepath.Join(root, ".beads", "issues.jsonl")
					}
					app, err := buildApp(cmd)
					if err != nil {
						return err
					}
					return app.MigrateBeads(ctx, appcli.MigrateOpts{
						File:          file,
						StatePath:     cmd.String("state"),
						DryRun:        cmd.Bool("dry-run"),
						IncludeClosed: cmd.Bool("include-closed"),
						Throttle:      cmd.Duration("throttle"),
					})
				},
			},
			{
				Name:  "local",
				Usage: "move the local tracker's issues to GitHub and keep .hew/issues.jsonl as an archive",
				Description: `Creates every local issue on GitHub, closed ones included, with their close
reasons and comments, wires parents and blockers, and rewrites #n references in
bodies and comments to the new numbers (code spans and fenced blocks are left
alone). On success .hew/migrated.json records the local → GitHub number map and
.hew/config.json is switched to github, after which the local file takes no
writes and every command uses GitHub.

The target is --repo, or the git remote; --dry-run needs neither, and past
twenty issues prints a summary (use --json for every one). The local tracker
is locked for the whole run. If a step fails the tracker stays local and rerunning resumes where
it stopped; the checkpoint is bound to the repository and to the exact contents
of issues.jsonl, so changing local issues between runs is refused.`,
				Flags: []ucli.Flag{
					&ucli.StringFlag{Name: "state", Usage: "resume-state `FILE`, bound to this repo and issues.jsonl (default: .hew/migration-state.json)"},
					&ucli.BoolFlag{Name: "dry-run", Usage: "print the plan without creating anything"},
					&ucli.DurationFlag{Name: "throttle", Usage: "pause between writes", Value: 500 * time.Millisecond},
				},
				Before: fixedArgs(0, 0),
				Action: func(ctx context.Context, cmd *ucli.Command) error {
					tracker, err := local.MainDir("")
					if err != nil {
						return err
					}
					app, err := buildMigrateLocalApp(cmd)
					if err != nil {
						return err
					}
					return app.MigrateLocal(ctx, appcli.MigrateLocalOpts{
						Dir:       tracker,
						StatePath: cmd.String("state"),
						DryRun:    cmd.Bool("dry-run"),
						Throttle:  cmd.Duration("throttle"),
					})
				},
			},
		},
	}
}

func initCmd() *ucli.Command {
	return &ucli.Command{
		Name:  "init",
		Usage: "bootstrap convention labels; print the CLAUDE.md snippet",
		Description: "--local needs a git checkout: hew finds the main checkout, where the tracker\n" +
			"lives in .hew, through git. In a directory that is not a repository yet, run `git init` first.",
		Flags: []ucli.Flag{
			&ucli.BoolFlag{Name: "local", Usage: "create a local-only tracker in .hew at the main checkout (needs a git checkout) instead of using GitHub Issues"},
		},
		Before: fixedArgs(0, 0),
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			if cmd.Bool("local") {
				if cmd.String("repo") != "" {
					return appcli.UsageError("--local and --repo are alternatives: --repo always targets GitHub")
				}
				return newApp(cmd).InitLocal("")
			}
			app, err := buildApp(cmd)
			if err != nil {
				return err
			}
			return app.Init(ctx)
		},
	}
}
