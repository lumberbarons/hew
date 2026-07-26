# issues

An opinionated, agentic-first CLI for tracking work in GitHub Issues. Inspired by
[beads](https://github.com/steveyegge/beads), backed entirely by GitHub — native
sub-issues and dependencies, priority/type/area labels, ready-work detection, and an
`issues prime` command that injects tracker conventions and live state into a coding
agent's context at session start.

GitHub Issues stays the single source of truth: humans get the web UI, PRs auto-close
issues via `Fixes #n`, and nothing needs syncing.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/lumberbarons/issues/main/install.sh | bash
```

Installs to `~/.local/bin` (override with `INSTALL_DIR`); never uses sudo.

Or, with a Go toolchain (1.25+):

```sh
go install github.com/lumberbarons/issues/cmd/issues@latest
```

Authentication comes from the [`gh` CLI](https://cli.github.com/) — run
`gh auth login` once and `issues` reuses its stored credentials. The target
repository is detected from the git remote (`--repo owner/name` overrides).

## Quickstart

```sh
issues init          # bootstrap the label set in a repo; prints a CLAUDE.md snippet
issues hooks install # Claude Code SessionStart hook: `issues prime` at session start
issues prime         # session-start context: conventions + ready work + live state
issues ready         # what should I work on? (priority-sorted, zero open blockers)
issues start 42      # claim it: assign @me + in-progress (refuses claimed work, exit 3)
# ...branch (feat/|fix/|chore/), commit, push...
issues pr            # draft PR for the claimed issue, body composed, "Fixes #42" enforced
```

`issues ready` prints one line per issue, so you can tell it worked:

```
#42 P1 bug  Retry loop hammers the API when offline
```

If any command exits `4`, authenticate first with `gh auth login`.

## Commands

```
issues prime                      # session-start context for agents
issues ready                      # open, non-epic, zero open blockers; P0→P4 then P?
issues list [--label X] [--epic N] [--closed]
            [--bodies]           # with --json: body on every line, dedup in one call
issues show <n>                   # body, deps, parent, children, recent comments
issues search <terms>             # text search, open+closed, best-match order —
                                  # check for an existing issue before filing one
issues create --type bug|enhancement|task --title "..."
              [--where X] [--problem|--goal "..."] [--fix|--approach "..."]
              [--done-when "..."]...   # section flags compose the body template
              [--priority P0..P4] [--area X] [--blocked-by N] [--parent N]
              [--discovered-from N]
              [--body-file F | --edit] # long-form escape hatch / $EDITOR
issues start <n> [--priority P0..P4] [--force]
issues triage                     # issues missing priority/type labels
issues set <n> [--priority ..] [--type ..] [--add-area X] [--remove-area X]
           [--parent N | --no-parent] [--title "..."]
issues pr [--for N] [--title "..."]
          [--what "..."] [--why "..."] [--testing "..."]
                                  # draft PR for the claimed issue: body composed from
                                  # the issue (What/Why default to its Fix/Approach and
                                  # Problem/Goal), exactly one "Fixes #n", base is the
                                  # repo default. The title defaults to the issue title
                                  # under the type's commit prefix (bug → "fix: ...");
                                  # --title is passed through. Push the branch first.
          [--body-file F]         # long-form escape hatch (missing Fixes/Part of
                                  # trailers are appended, never duplicated)
          [--base BRANCH] [--ready]
issues close <n> --reason "..." [--completed | --duplicate-of M]
issues block <n> --on <m>         # native dependency, cycle-checked
issues unblock <n> --from <m>
issues epic create --title "..." [--children N,N]
                   [--goal "..." --done-when "..." | --body-file F | --edit]
issues epic status [<n>]
issues apply <plan.jsonl> [--dry-run] [--state F] [--throttle D]
                                 # batch-create a whole set of issues from a JSONL
                                 # plan — labels, bodies, parents, dependencies —
                                 # checkpointed and resumable (see "Plan files")
issues init
issues hooks install|remove      # Claude Code SessionStart hook running `issues prime`
issues migrate beads [--file F] [--state F] [--throttle D]
                     [--dry-run] [--include-closed]
                                 # import a beads (bd) database: priorities, types,
                                 # deps, epics, in-progress state; resumable
                                 # defaults: --file .beads/issues.jsonl, --state
                                 # github-migration.json next to it, --throttle 500ms
```

Output is one compact line per issue, annotated with whatever keeps it from
being plain ready work (`[blocked by #120]`, `[epic 2/6]`, `[in progress @you]`);
`list` sorts ready work first, then claimed, blocked, and epics. Every command
takes `--json` (stable flat schema; list commands emit NDJSON so output survives
truncation and grep); every GitHub-touching command also takes `--repo owner/name`
(`hooks` is local-only). Exit codes are meaningful: `3`
means "already claimed, pick the next ready item", `4` means "run `gh auth login`".

### Plan files

`issues apply` turns a multi-issue workflow — decomposing a spec into phase
epics and tasks, filing a batch of review findings — into: write a plan,
dry-run it, apply it. One JSON object per line:

```jsonl
{"id":"epic1","title":"Voltgo support","type":"epic","priority":"P1","goal":"..."}
{"id":"scaffold","title":"Scaffold the driver","type":"task","parent":"epic1","done-when":["driver builds"]}
{"title":"Wire the collector","type":"task","parent":"epic1","blocked-by":["scaffold",42],"areas":["ble"]}
```

`type` is `bug|enhancement|task`, or `epic` for a parent issue (no type label,
`Epic: ` title prefix). `priority` defaults to P2. `parent` and `blocked-by`
take either a local `id` — a string, resolved to the created issue's number,
so entries can reference each other before numbers exist — or an existing
issue number. `discovered-from` adds the same origin link the create flag
does. Bodies come from the same section fields the create flags use —
`where`, `problem` or `goal`, `fix` or `approach`, `done-when` (a list, one
checklist item each) — composed into the body template; `body` carries raw
long-form text instead (mutually exclusive with the section fields). Creation is checkpointed to the `--state` file after every write, so a
failed run resumes without creating duplicates; unknown fields, dangling
references, and dependency cycles between entries are all rejected before
anything is written.

## Design

See [DESIGN.md](DESIGN.md) for the conventions the tool enforces, the read-path
normalization rules, and the API strategy.
