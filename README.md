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

Or, with a Go toolchain — the minimum is the `go` directive in `go.mod`, which
tracks the latest patched release:

```sh
go install github.com/lumberbarons/hew/cmd/hew@latest
```

Authentication comes from the [`gh` CLI](https://cli.github.com/) — run
`gh auth login` once and `hew` reuses its stored credentials. The target
repository is detected from the git remote (`--repo owner/name` overrides).

## Quickstart

```sh
hew init          # bootstrap the label set in a repo; prints a CLAUDE.md snippet
hew hooks install claude # SessionStart hook for Claude Code (or use codex)
hew prime         # session-start context: conventions + ready work + live state
hew ready         # what should I work on? (priority-sorted, zero open blockers)
hew start 42      # claim it: assign @me + in-progress (refuses claimed work: exit 3,
                  #   or exit 5 when the claim is already yours)
# ...branch (feat/|fix/|chore/), commit, push...
hew pr            # draft PR for the claimed issue, body composed, "Fixes #42" enforced
```

## Session-start agents

`hew prime` works with both Claude Code and Codex. Choose the agent explicitly:

- **Claude Code:** `hew hooks install claude` adds a SessionStart hook to the
  project's `.claude/settings.json`.
- **Codex:** `hew hooks install codex` adds the equivalent hook to the
  project's `.codex/hooks.json`. Codex requires project hooks to be trusted;
  review and enable it with `/hooks`.

`hew ready` prints one line per issue, so you can tell it worked:

```
#42 P1 bug  Retry loop hammers the API when offline
```

If any command exits `4`, authenticate first with `gh auth login`.

## Commands

```
hew prime                      # session-start context for agents
hew ready                      # open, non-epic, zero open blockers; P0→P4 then P?
hew list [--label X] [--epic N] [--state open|closed|all]
            [--bodies]           # with --json: body on every line, dedup in one call
                                  # (--closed is an alias for --state closed)
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
           [--closed]             # set/block/unblock refuse a closed target — an
                                  # edit landing on one is almost always stale
                                  # state; this is the override
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
hew block <n> --on <m> [--closed]      # native dependency, cycle-checked
hew unblock <n> --from <m> [--closed]
hew epic create --title "..." [--children N,N]
                   [--goal "..." --done-when "..." | --body-file F | --edit]
hew epic status [<n>]
hew apply <plan.jsonl> [--dry-run] [--state F] [--throttle D]
                                 # batch-create a whole set of issues from a JSONL
                                 # plan — labels, bodies, parents, dependencies —
                                 # checkpointed and resumable (see "Plan files")
                                 # defaults: --state <plan>.state.json, --throttle 500ms
hew init
hew hooks install|remove <claude|codex>
                              # add/remove a SessionStart hook running `hew prime`
                              # in the selected agent's project configuration;
                              # preserves the rest of the file; needs a git repo
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

### Checking for duplicates

Three read paths can answer "does this already exist?", so the tool prescribes
an order rather than leaving an agent to pick:

1. `hew search <terms>` — the default. Server-side, cheap, and covers open and
   closed issues, so "already fixed" answers the question as well as "already
   filed". Results are capped; it warns rather than paging.
2. `hew list --json --bodies --state all` — when exhaustiveness matters or the
   search index may be stale. One call carries every issue's body in both
   states; `--bodies` requires `--json`.
3. `hew show <n>` — only to read a specific candidate the first two surfaced.

Then file with `hew create ... --discovered-from <n>`.

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

The state file is trusted local scratch, not an input you can hand around: it
records the repository and a digest of the plan it was written for, and every
issue it maps is checked for a marker `apply` embedded in the body it created.
A state file from another repository or another plan, one written by an older
hew, or one pointing at an issue this plan did not create is refused before
any write — including under `--dry-run`, so the plan-only pass reaches the same
verdict as the real one. Editing the plan between runs invalidates the
checkpoint by design; if you need to change it mid-flight, start a fresh run
with a new `--state` path and expect the entries already created to be created
again. The same rules apply to `migrate beads` and its snapshot.

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

The token-lean claim is measured, not asserted: `hew ready` and `hew list` cost
17–22 tokens per open issue against ~79 for the leanest equivalent GraphQL query
and 112–134 for `gh issue list --json`, which cannot answer readiness at all.
[DESIGN.md](DESIGN.md#token-efficiency-measured-2026-07-29) has the full table,
including the two commands where the tool currently loses; [evals/](evals/) is
the harness that produced it.
