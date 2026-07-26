// Command issues is an agentic-first CLI for GitHub Issues. All behavior
// lives in internal/; this file is flag wiring — parsing, exit-code mapping,
// and editor invocation are delegated to internal packages so they can be
// tested.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	ucli "github.com/urfave/cli/v3"

	appcli "github.com/lumberbarons/issues/internal/cli"
	"github.com/lumberbarons/issues/internal/conventions"
	"github.com/lumberbarons/issues/internal/editor"
	"github.com/lumberbarons/issues/internal/gh"
	"github.com/lumberbarons/issues/internal/git"

	"github.com/cli/go-gh/v2/pkg/repository"
)

// version is stamped by goreleaser via ldflags.
var version = "dev"

func main() {
	if err := root().Run(context.Background(), os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(appcli.ExitCode(err))
	}
}

// globalFlags are declared on the root command only. urfave/cli v3 flags
// are persistent by default, so they parse in either position (`issues
// --json ready` and `issues ready --json`) and leaf actions resolve them
// via lineage lookup. Re-declaring them on a leaf would shadow the root's
// parsed value with an empty one (#25), so leaves must not repeat them.
func globalFlags() []ucli.Flag {
	return []ucli.Flag{
		&ucli.BoolFlag{Name: "json", Usage: "structured output with a stable schema"},
		&ucli.StringFlag{Name: "repo", Usage: "target `owner/name` (default: detect from git remote)"},
	}
}

func buildApp(cmd *ucli.Command) (*appcli.App, error) {
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
			return nil, fmt.Errorf("cannot detect repository (use --repo owner/name): %w", err)
		}
		repo.Owner, repo.Name = current.Owner, current.Name
	}
	client, err := gh.New(repo)
	if err != nil {
		return nil, err
	}
	return &appcli.App{
		Client: client,
		Repo:   repo,
		Out:    os.Stdout,
		ErrOut: os.Stderr,
		JSON:   cmd.Bool("json"),
		Edit:   editor.Edit,
		Git:    git.Current,
	}, nil
}

// numberArg parses the required positional issue number.
func numberArg(cmd *ucli.Command, usage string) (int, error) {
	return appcli.ParseIssueNumber(cmd.Args().First(), usage)
}

func root() *ucli.Command {
	return &ucli.Command{
		Name:    "issues",
		Usage:   "agentic-first CLI for GitHub Issues",
		Version: version,
		Flags:   globalFlags(),
		Commands: []*ucli.Command{
			primeCmd(), readyCmd(), listCmd(), showCmd(), searchCmd(),
			createCmd(), startCmd(), triageCmd(), setCmd(), prCmd(), closeCmd(),
			blockCmd(), unblockCmd(), epicCmd(), applyCmd(), initCmd(),
			hooksCmd(), migrateCmd(),
		},
	}
}

func primeCmd() *ucli.Command {
	return &ucli.Command{
		Name:  "prime",
		Usage: "session-start context: conventions, ready work, live state",
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			app, err := buildApp(cmd)
			if err != nil {
				return err
			}
			return app.Prime(ctx)
		},
	}
}

func readyCmd() *ucli.Command {
	return &ucli.Command{
		Name:  "ready",
		Usage: "open, non-epic issues with zero open blockers, priority-sorted",
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			app, err := buildApp(cmd)
			if err != nil {
				return err
			}
			return app.Ready(ctx)
		},
	}
}

func listCmd() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "list issues (open by default)",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "label", Usage: "only issues with this label"},
			&ucli.IntFlag{Name: "epic", Usage: "only children of epic `N`"},
			&ucli.BoolFlag{Name: "closed", Usage: "show closed issues instead"},
			&ucli.BoolFlag{Name: "bodies", Usage: "include issue bodies (requires --json) for single-call dedup"},
		},
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			app, err := buildApp(cmd)
			if err != nil {
				return err
			}
			return app.List(ctx, appcli.ListOpts{
				Label:  cmd.String("label"),
				Epic:   cmd.Int("epic"),
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
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			n, err := numberArg(cmd, "issues show <n>")
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

// bodyFlags are the body paths shared by create and epic create: section
// flags compose the template; --body-file and --edit are the long-form
// escape hatches.
func bodyFlags() []ucli.Flag {
	return []ucli.Flag{
		&ucli.StringFlag{Name: "where", Usage: "body section: where the work lives"},
		&ucli.StringFlag{Name: "problem", Usage: "body section: the problem (or use --goal)"},
		&ucli.StringFlag{Name: "goal", Usage: "body section: the goal (or use --problem)"},
		&ucli.StringFlag{Name: "fix", Usage: "body section: the fix (or use --approach)"},
		&ucli.StringFlag{Name: "approach", Usage: "body section: the approach (or use --fix)"},
		&proseSliceFlag{Name: "done-when", Usage: "acceptance checklist item (repeatable)"},
		&ucli.StringFlag{Name: "body-file", Usage: "read the whole body from `FILE` (for long-form bodies)"},
		&ucli.BoolFlag{Name: "edit", Usage: "open $EDITOR seeded with the body template"},
	}
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
		Name:  "create",
		Usage: "file a new issue within the conventions",
		Flags: append([]ucli.Flag{
			&ucli.StringFlag{Name: "title", Usage: "issue title (required)"},
			&ucli.StringFlag{Name: "type", Usage: "bug|enhancement|task (required)"},
			&ucli.StringFlag{Name: "priority", Usage: "P0..P4 (default P2)"},
			&ucli.StringSliceFlag{Name: "area", Usage: "area label (repeatable)"},
			&ucli.IntSliceFlag{Name: "blocked-by", Usage: "blocking issue `N` (repeatable)"},
			&ucli.IntFlag{Name: "parent", Usage: "attach as sub-issue of epic `N`"},
			&ucli.IntFlag{Name: "discovered-from", Usage: "link back to issue `N` this was discovered under"},
		}, bodyFlags()...),
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

func startCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "start",
		Usage:     "claim an issue: assign @me + in-progress (refuses claimed issues, exit 3)",
		ArgsUsage: "<n>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "priority", Usage: "P0..P4; required when the issue is untriaged"},
			&ucli.BoolFlag{Name: "force", Usage: "steal an already-claimed issue"},
		},
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			n, err := numberArg(cmd, "issues start <n>")
			if err != nil {
				return err
			}
			app, err := buildApp(cmd)
			if err != nil {
				return err
			}
			return app.Start(ctx, n, cmd.String("priority"), cmd.Bool("force"))
		},
	}
}

func triageCmd() *ucli.Command {
	return &ucli.Command{
		Name:  "triage",
		Usage: "issues missing priority/type labels, oldest first",
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			app, err := buildApp(cmd)
			if err != nil {
				return err
			}
			return app.Triage(ctx)
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
			&ucli.StringSliceFlag{Name: "add-area", Usage: "add area label (repeatable)"},
			&ucli.StringSliceFlag{Name: "remove-area", Usage: "remove area label (repeatable)"},
			&ucli.IntFlag{Name: "parent", Usage: "move under epic `N`"},
			&ucli.BoolFlag{Name: "no-parent", Usage: "detach from its epic"},
			&ucli.StringFlag{Name: "title", Usage: "new title"},
		},
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			n, err := numberArg(cmd, "issues set <n>")
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
			})
		},
	}
}

func prCmd() *ucli.Command {
	return &ucli.Command{
		Name:  "pr",
		Usage: "open a draft PR for the claimed issue, body composed from tracker state",
		Description: `Composes the PR the workflow prescribes: the linked issue is inferred
from what you have claimed (a number in the branch name breaks ties, and
--for <n> settles it outright), the body is the What/Why/Testing template
with exactly one "Fixes #n" so the merge closes the issue, and the base is
the repo's default branch. What and Why default to the issue's own
Fix/Approach and Problem/Goal sections. Push the branch first — GitHub can
only open a PR for a ref it can see.`,
		Flags: []ucli.Flag{
			&ucli.IntFlag{Name: "for", Usage: "link issue `N` instead of inferring it"},
			&ucli.StringFlag{Name: "title", Usage: "PR title (default: the issue title)"},
			&ucli.StringFlag{Name: "what", Usage: "body section: what the change does (default: the issue's Fix/Approach)"},
			&ucli.StringFlag{Name: "why", Usage: "body section: why (default: the issue's Problem/Goal)"},
			&ucli.StringFlag{Name: "testing", Usage: "body section: how it was verified"},
			&ucli.StringFlag{Name: "body-file", Usage: "read the whole body from `FILE` (Fixes #n is appended if absent)"},
			&ucli.StringFlag{Name: "base", Usage: "target `BRANCH` (default: the repo's default branch)"},
			&ucli.BoolFlag{Name: "ready", Usage: "open for review instead of as a draft"},
		},
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			app, err := buildApp(cmd)
			if err != nil {
				return err
			}
			return app.PR(ctx, appcli.PROpts{
				For:   cmd.Int("for"),
				Title: cmd.String("title"),
				Sections: conventions.PRSections{
					What:    cmd.String("what"),
					Why:     cmd.String("why"),
					Testing: cmd.String("testing"),
				},
				BodyFile: cmd.String("body-file"),
				Base:     cmd.String("base"),
				Ready:    cmd.Bool("ready"),
			})
		},
	}
}

func closeCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "close",
		Usage:     "comment + close (not-planned unless --completed or --duplicate-of)",
		ArgsUsage: "<n>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "reason", Usage: "closing comment (required unless --duplicate-of)"},
			&ucli.BoolFlag{Name: "completed", Usage: "close as completed"},
			&ucli.IntFlag{Name: "duplicate-of", Usage: "close as duplicate of issue `N`"},
		},
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			n, err := numberArg(cmd, "issues close <n> --reason \"...\"")
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

func blockCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "block",
		Usage:     "add a native dependency (cycle-checked)",
		ArgsUsage: "<n> --on <m>",
		Flags: []ucli.Flag{
			&ucli.IntFlag{Name: "on", Usage: "blocking issue `N` (required)", Required: true},
		},
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			n, err := numberArg(cmd, "issues block <n> --on <m>")
			if err != nil {
				return err
			}
			app, err := buildApp(cmd)
			if err != nil {
				return err
			}
			return app.Block(ctx, n, cmd.Int("on"))
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
		},
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			n, err := numberArg(cmd, "issues unblock <n> --from <m>")
			if err != nil {
				return err
			}
			app, err := buildApp(cmd)
			if err != nil {
				return err
			}
			return app.Unblock(ctx, n, cmd.Int("from"))
		},
	}
}

func epicCmd() *ucli.Command {
	return &ucli.Command{
		Name:  "epic",
		Usage: "manage epics (sub-issue trees)",
		Commands: []*ucli.Command{
			{
				Name:  "create",
				Usage: "create a parent issue and attach children",
				Flags: append([]ucli.Flag{
					&ucli.StringFlag{Name: "title", Usage: "epic title (required)"},
					&ucli.IntSliceFlag{Name: "children", Usage: "existing issues to attach"},
				}, bodyFlags()...),
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
				Name:      "status",
				Usage:     "progress rollup for all epics, or one epic's children",
				ArgsUsage: "[<n>]",
				Action: func(ctx context.Context, cmd *ucli.Command) error {
					n := 0
					if cmd.Args().First() != "" {
						var err error
						if n, err = numberArg(cmd, "issues epic status [<n>]"); err != nil {
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
	return &appcli.App{Out: os.Stdout, ErrOut: os.Stderr, JSON: cmd.Bool("json")}, root, nil
}

func hooksCmd() *ucli.Command {
	return &ucli.Command{
		Name:  "hooks",
		Usage: "manage the Claude Code SessionStart hook that runs `issues prime`",
		Commands: []*ucli.Command{
			{
				Name:  "install",
				Usage: "add the hook to this project's .claude/settings.json",
				Action: func(ctx context.Context, cmd *ucli.Command) error {
					app, root, err := hooksApp(cmd)
					if err != nil {
						return err
					}
					return app.HooksInstall(root)
				},
			},
			{
				Name:  "remove",
				Usage: "remove the hook again",
				Action: func(ctx context.Context, cmd *ucli.Command) error {
					app, root, err := hooksApp(cmd)
					if err != nil {
						return err
					}
					return app.HooksRemove(root)
				},
			},
		},
	}
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

Fields: title (required), type bug|enhancement|task|epic (required; epic means
a parent issue — no type label, "Epic: " title prefix), priority P0..P4
(default P2), areas, parent, blocked-by, discovered-from, id. parent and
blocked-by take a local id (string) or an existing issue number. Bodies come
from the section fields — where, problem or goal, fix or approach, done-when
(a list, one checklist item each) — composed into the body template; body
holds raw long-form text instead (mutually exclusive with sections). Creation
is checkpointed after every write, so a failed run resumes without duplicates;
dependency cycles between entries are rejected before anything is written.`,
		Flags: append(globalFlags(),
			&ucli.StringFlag{Name: "state", Usage: "resume-state `FILE` (default: <plan>.state.json)"},
			&ucli.BoolFlag{Name: "dry-run", Usage: "print the plan without creating anything"},
			&ucli.DurationFlag{Name: "throttle", Usage: "pause between writes", Value: 500 * time.Millisecond},
		),
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
		Name:  "migrate",
		Usage: "import issues from another tracker",
		Commands: []*ucli.Command{
			{
				Name:  "beads",
				Usage: "migrate a beads (bd) database: labels, deps, epics, in-progress state",
				Flags: []ucli.Flag{
					&ucli.StringFlag{Name: "file", Usage: "beads snapshot (default: <project>/.beads/issues.jsonl)"},
					&ucli.StringFlag{Name: "state", Usage: "resume-state `FILE` (default: alongside the snapshot)"},
					&ucli.BoolFlag{Name: "dry-run", Usage: "print the plan without creating anything"},
					&ucli.BoolFlag{Name: "include-closed", Usage: "also migrate closed beads (create, comment, close)"},
					&ucli.DurationFlag{Name: "throttle", Usage: "pause between writes", Value: 500 * time.Millisecond},
				},
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
					state := cmd.String("state")
					if state == "" {
						state = filepath.Join(filepath.Dir(file), "github-migration.json")
					}
					app, err := buildApp(cmd)
					if err != nil {
						return err
					}
					return app.MigrateBeads(ctx, appcli.MigrateOpts{
						File:          file,
						StatePath:     state,
						DryRun:        cmd.Bool("dry-run"),
						IncludeClosed: cmd.Bool("include-closed"),
						Throttle:      cmd.Duration("throttle"),
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
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			app, err := buildApp(cmd)
			if err != nil {
				return err
			}
			return app.Init(ctx)
		},
	}
}
