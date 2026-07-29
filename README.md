# hew

An opinionated, agentic-first CLI for tracking work in GitHub Issues. Inspired by
[beads](https://github.com/steveyegge/beads), backed entirely by GitHub — native
sub-issues and dependencies, priority/type/area labels, ready-work detection, and a
`hew prime` command that injects tracker conventions and live state into a coding
agent's context at session start.

GitHub Issues stays the single source of truth: humans get the web UI, PRs auto-close
issues via `Fixes #n`, and nothing needs syncing.

## Install

Needs `git` — the target repo is read from the local checkout, and `hew pr`
reads the current branch — plus the [`gh` CLI](https://cli.github.com/) for
authentication. The install script also needs `curl` and `tar`.

```sh
curl -fsSL https://raw.githubusercontent.com/lumberbarons/hew/main/install.sh | bash
```

Installs to `~/.local/bin` (override with `INSTALL_DIR`); never uses sudo.
Linux and macOS (x86_64 and arm64) only — elsewhere, use `go install` below.

Or, with a Go toolchain (1.25+):

```sh
go install github.com/lumberbarons/hew/cmd/hew@latest
```

Authentication comes from the [`gh` CLI](https://cli.github.com/) — run
`gh auth login` once and `hew` reuses its stored credentials. The target
repository is detected from the git remote (`--repo owner/name` overrides).

## Quickstart

```sh
hew init          # bootstrap the label set in a repo; prints a CLAUDE.md snippet
hew hooks install # SessionStart hook (writes the project's .claude/settings.json)
hew prime         # session-start context: conventions + ready work + live state
hew ready         # what should I work on? (priority-sorted, zero open blockers)
hew start 42      # claim it: assign @me + in-progress (refuses claimed work: exit 3,
                  #   or exit 5 when the claim is already yours)
# ...branch (feat/|fix/|chore/), commit, push...
hew pr            # draft PR for the claimed issue, body composed, "Fixes #42" enforced
```

`hew ready` prints one line per issue, so you can tell it worked:

```
#42 P1 bug  Retry loop hammers the API when offline
```

If any command exits `4`, authenticate first with `gh auth login`.

## Commands

```
hew prime                      # session-start context for agents
hew ready                      # open, non-epic, zero open blockers; P0→P4 then P?
hew list [--label X] [--epic N] [--closed]
            [--bodies]           # with --json: body on every line, dedup in one call
hew show <n>                   # body, deps, parent, children, recent comments
hew search <terms>             # text search, open+closed, best-match order —
                                  # check for an existing issue before filing one
hew create --type bug|enhancement|task --title "..."
              [--where X] [--problem|--goal "..."] [--fix|--approach "..."]
              [--done-when "..."]...   # section flags compose the body template
              [--priority P0..P4] [--area X] [--blocked-by N] [--parent N]
              [--discovered-from N]
              [--body-file F | --edit] # long-form escape hatch / $EDITOR
hew start <n> [--priority P0..P4] [--force]
hew triage                     # issues missing priority/type labels
hew set <n> [--priority ..] [--type ..] [--add-area X] [--remove-area X]
           [--parent N | --no-parent] [--title "..."]
           [--body-file F]        # replace the body (an empty file is refused)
hew pr [--for N] [--title "..."]
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
hew close <n> --reason "..." [--completed | --duplicate-of M]
hew block <n> --on <m>         # native dependency, cycle-checked
hew unblock <n> --from <m>
hew epic create --title "..." [--children N,N]
                   [--goal "..." --done-when "..." | --body-file F | --edit]
hew epic status [<n>]
hew apply <plan.jsonl> [--dry-run] [--state F] [--throttle D]
                                 # batch-create a whole set of issues from a JSONL
                                 # plan — labels, bodies, parents, dependencies —
                                 # checkpointed and resumable (see "Plan files")
                                 # defaults: --state <plan>.state.json, --throttle 500ms
hew init
hew hooks install|remove      # add/remove a Claude Code SessionStart hook running
                                 # `hew prime`. Edits the project's committed
                                 # .claude/settings.json in place, preserving the rest
                                 # of the file; needs a git repo to find the root
hew migrate beads [--file F] [--state F] [--throttle D]
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
means "already claimed by someone else, pick the next ready item", `5` means
"already claimed by you, resume that work", `4` means "run `gh auth login`".

### Plan files

`hew apply` turns a multi-issue workflow — decomposing a spec into phase
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
long-form text instead (mutually exclusive with the section fields).

Creation and dependency wiring are both checkpointed to the `--state` file as
they happen, so a failed run resumes without creating duplicates or
re-attempting edges that already landed; unknown fields, dangling
references, and dependency cycles between entries are all rejected before
anything is written.

## Auto-triage in CI

[`examples/auto-triage.yml`](examples/auto-triage.yml) is a copy-pasteable GitHub
Actions workflow that runs Claude with this CLI on every newly filed issue, so
drive-by reports get deduped and labelled without a human sweep and `ready` stays
truthful. To enable it:

```sh
hew init                        # once: create the convention labels
mkdir -p .github/workflows
curl -fsSL https://raw.githubusercontent.com/lumberbarons/hew/main/examples/auto-triage.yml \
  -o .github/workflows/auto-triage.yml
gh secret set ANTHROPIC_API_KEY    # paste a key from console.anthropic.com
```

The agent reads the new issue, searches open and closed issues for a duplicate,
and either closes it as one or applies a type and priority label plus a short
rationale comment. What it deliberately cannot do is the interesting part:

- `permissions: issues: write` is the only grant — no code, no PRs, no other repos.
- The tool allowlist is a handful of `hew` subcommands plus `gh issue comment`
  and `gh label list`. No `create`, no `start`, no shell beyond that.
- It assigns P2–P4 only; anything it thinks is P0/P1 it labels P2 and flags for a
  human to upgrade. Reports too vague to classify are left untriaged
  (`hew triage` still lists them) rather than mislabelled.
- Bot-filed issues and issues from anyone with write access are skipped, so
  `hew create` output and the workflow's own writes can't feed it back.

Issue bodies are untrusted input; the permission scope and the allowlist are the
mitigation, not the prompt's instructions.

## Design

See [DESIGN.md](DESIGN.md) for the conventions the tool enforces, the read-path
normalization rules, and the API strategy.
