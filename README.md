# hew

An opinionated, agentic-first CLI for tracking work in GitHub Issues. Inspired by
[beads](https://github.com/steveyegge/beads), backed entirely by GitHub — native
sub-issues and dependencies, priority/type/area labels, ready-work detection, and a
`hew prime` command that injects tracker conventions and live state into a coding
agent's context at session start.

GitHub Issues stays the single source of truth: humans get the web UI, PRs auto-close
issues via `Fixes #n`, and nothing needs syncing.

## Install

Needs `git`. The target repo is read from the local checkout, `hew pr` reads the
current branch, and [local mode](#local-mode) finds its tracker through git, so
hew works only inside a git checkout (`git init` first in a new directory).

GitHub use also needs the [`gh` CLI](https://cli.github.com/) for authentication.
The install script needs `curl` and `tar`.

```sh
curl -fsSL https://lumberbarons.github.io/hew/install.sh | bash
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

No GitHub tracker? A repo can track issues locally instead — see [Local mode](#local-mode).

## Quickstart

```sh
hew init          # bootstrap the label set in a repo; prints a CLAUDE.md snippet
hew init --local  # or track issues in a local .hew directory instead of GitHub
hew hooks install claude # agent session-start hook running `hew prime`; also codex, cursor, opencode
hew prime         # session-start context: conventions + ready work + live state
hew ready         # what should I work on? (priority-sorted, zero open blockers)
hew start 42 --show  # claim it and read it: assign @me + in-progress, then the issue's
                  #   detail (refuses claimed work: exit 3, or exit 5 when already yours; open blockers: exit 6)
# ...branch (feat/|fix/|chore/), commit, push...
hew pr            # draft PR for the claimed issue, body composed, "Fixes #42" enforced
hew pr --amend    # revise the open PR's title/body; omitted sections are preserved
```

## Session-start agents

`hew prime` works with Claude Code, Codex, Cursor, and opencode. Choose the
agent explicitly:

- **Claude Code:** `hew hooks install claude` adds a SessionStart hook to the
  project's `.claude/settings.json`.
- **Codex:** `hew hooks install codex` adds the equivalent hook to the
  project's `.codex/hooks.json`. Codex requires project hooks to be trusted;
  review and enable it with `/hooks`.
- **Cursor:** `hew hooks install cursor` adds a `sessionStart` hook to the
  project's `.cursor/hooks.json`. Cursor's stdout contract differs from the
  others': the hook must print valid JSON and the primer travels in the
  `additional_context` field (snake_case — Cursor ignores Claude Code's
  camelCase `additionalContext`), so the installed command is
  `hew prime --hook-format cursor`, which JSON-encodes the primer. Don't copy
  a Claude or Codex hook entry verbatim: bare `hew prime` text fails Cursor's
  JSON parse and the primer never lands.
- **opencode:** `hew hooks install opencode` writes an auto-discovered plugin
  to `.opencode/plugins/hew-prime.js`; it injects the primer into system
  context, so it is present before the first turn without appearing in chat.

The Claude Code and Codex hooks are guarded: they run `hew prime` only when
`hew` is on `PATH`, and otherwise print a one-line hint, addressed to the
agent, saying hew is missing and how to install it. A contributor without hew
gets a normal session and a nudge instead of a red hook error on every start,
so the shared settings file has no reason to lose the hook. Only a missing
binary is absorbed: hew's own failures, such as exit `4` before `gh auth
login`, still surface. The guard is POSIX sh, which cmd and PowerShell cannot
run, so the Codex entry also carries a `commandWindows` override of plain
`hew prime`; Windows contributors without hew see the hook error. The Cursor
hook is not guarded and has no hint: Cursor has no Windows-only override, runs
hook commands through PowerShell there, and needs JSON on stdout. The opencode
plugin is silent without hew. `hew hooks install` upgrades the bare `hew prime`
entry older releases wrote for Claude Code and Codex in place; a command you
phrased yourself is left alone, and install says so.

All of them refuse to write through a symlink. A checkout is untrusted input, so hew
never follows a link standing where it expects its own directory or settings
file — including one that stays inside the checkout. If yours are symlinked
into a dotfiles repo, edit the file the link resolves to directly.

`hew ready` prints one line per issue, so you can tell it worked:

```
#42 P1 bug  Retry loop hammers the API when offline
```

If any command exits `4`, authenticate first with `gh auth login`.

## Local mode

For a project that has no GitHub tracker yet, or an agent that cannot reach one,
`hew` can keep the issues in the repo's own checkout. Everything else works the
same — the conventions, `ready`, `start`, epics, dependencies, `--json`. It needs a
git checkout (run `git init` in a new directory first), but no remote and no `gh`.

```sh
hew init --local          # creates .hew/ at the main checkout and selects the local backend
hew prime                 # the local primer: same workflow, no pull-request step
hew create --type task --title "..." --goal "..." --done-when "..."
hew ready
hew start 1 --show
hew close 1 --completed --reason "..."   # finishing work: there is no `hew pr`
```

- **One tracker per repository.** It lives in `.hew/` at the *main* checkout, so
  every linked worktree shares it. `.hew/` ignores itself, so it never shows in
  `git status` — and it is untracked, so `git clean -fdx` at the main checkout
  deletes the only copy of your issues. Back it up if it matters. Nothing
  outside `.hew/` records that the repo is local, so once it is deleted hew can no
  longer tell, and falls back to the git remote's GitHub issues — write commands
  then act on the real repository. (A `.hew/` that survives with an
  `issues.jsonl` but has lost its `config.json`, or names an unknown `backend`, is
  refused with the file named instead; `hew init --local` restores the config and
  keeps the issues.)
- **Parallel agents are safe.** Writes are serialized under a lock and replaced
  atomically, and `start` has exactly one winner. Agents are told apart per
  worktree; set `HEW_USER` to a unique value per agent when sessions share one
  checkout.
- **No pull requests.** `hew pr` refuses on a local tracker. `--repo owner/name`
  always targets GitHub instead.
- **Moving to GitHub.** `hew migrate local` creates every issue — closed ones
  too, with close reasons and comments — wires parents and blockers, and rewrites
  `#n` references to the new numbers. Add `--dry-run` to see the plan first. Once
  every step has finished it records the local → GitHub number map in
  `.hew/migrated.json` and switches the repo to GitHub; `issues.jsonl` stays as a
  read-only archive. A failed run leaves the tracker local, and rerunning resumes
  it — so don't edit local issues between the two. The target is the git remote's
  repository, or `--repo`.

## Commands

```
hew prime                      # session-start context for agents
hew ready [--limit N]          # open, non-epic, triaged, zero open blockers; P0→P4
                                  # capped at 30 by default (0 for all); truncation warns
hew list [--label X] [--epic N] [--state open|closed|all]
            [--bodies]           # with --json: body on every line, dedup in one call;
                                   # untriaged issues are omitted — hew triage holds them
                                   # (--closed is an alias for --state closed)
hew show <n>                   # body, deps, parent, children, recent comments
hew search <terms>             # text search over vetted issues, open+closed, best-match
                                   # order — check for an existing issue before filing one
hew create --type bug|enhancement|task --title "..."
              [--where X] [--problem|--goal "..."] [--fix|--approach "..."]
              [--done-when "..."]...   # section flags compose the body template
              [--priority P0..P4] [--area X] [--blocked-by N] [--parent N]
              [--discovered-from N]
              [--body-file F | --edit] # long-form escape hatch / $EDITOR
hew draft [section flags] [--out F]   # compose a body locally (stdout without --out)
hew draft --check F                   # revalidate a draft; neither touches GitHub
hew start <n> [--priority P0..P4] [--force] [--show]
                                   # --show appends the issue's detail (as show would);
                                   # --json includes the body too (without text-findings annotations)
hew triage [--search <terms>]  # issues missing priority/type labels; --search runs
                                   # the same search over untriaged titles and bodies
                                   # (open+closed) — the untriaged half of dedup
hew set <n> [--priority ..] [--type ..] [--add-area X] [--remove-area X]
           [--parent N | --no-parent] [--title "..."]
           [--body-file F]        # replace the body (an empty file is refused)
           [--closed]             # set/block/unblock refuse a closed target — an
                                  # edit landing on one is almost always stale
                                  # state; this is the override
hew pr [--for N ...] [--title "..."] [--amend]
          [--what "..."] [--why "..."] [--testing "..."] [--risk "..."]
                                  # draft PR for the claimed issue: body composed from
                                  # the issue (What/Why default to its Fix/Approach and
                                  # Problem/Goal), exactly one "Fixes #n" (repeat --for
                                  # to close a declared set of coupled issues), base is the
                                  # repo default. Testing is evidence on Before:/After:
                                  # lines; Risk is a Reversibility: line and a Blast
                                  # radius: line. The title defaults to the
                                  # issue title under the type's commit prefix
                                  # (bug → "fix: ..."); --title is passed through.
                                  # Every section flag repeats: several values
                                  # become a bullet list, one value stays a paragraph.
                                  # Push the branch first. --amend revises the open
                                  # PR instead: omitted sections and the title are
                                  # preserved, an explicitly empty flag removes its
                                  # section.
          [--body-file F]         # long-form escape hatch (missing Fixes/Part of
                                  # trailers are appended, never duplicated)
          [--base BRANCH] [--ready]  # creation-only: --amend refuses both
hew close <n> --reason "..." [--completed | --duplicate-of M]
hew reopen <n> --reason "..."          # comment + reopen; releases a stale claim,
                                       # no-op on an open issue
hew block <n> --on <m> [--closed]      # native dependency, cycle-checked
hew unblock <n> --from <m> [--closed]
hew epic create --title "..." [--children N,N]
                   [--goal "..." --done-when "..." | --body-file F | --edit]
hew epic status [<n>]                    # rollup per open epic; with <n>, that epic's
                                         # children plus "next:" — the next workable
                                         # child (open, unblocked, in child order);
                                         # also reachable as: hew epic list
hew apply <plan.jsonl> [--dry-run] [--state F] [--throttle D]
                                 # batch-create a whole set of issues from a JSONL
                                 # plan — labels, bodies, parents, dependencies —
                                 # checkpointed and resumable (see "Plan files")
                                 # defaults: --state <plan>.state.json, --throttle 500ms
hew init [--local]            # --local: create a local-only tracker in .hew
hew hooks install|remove <claude|codex|cursor|opencode>
                              # add/remove a session-start hook running `hew prime`
                              # in the selected agent's project configuration;
                              # preserves the rest of the file; needs a git repo
hew migrate beads [--file F] [--state F] [--throttle D]
                     [--dry-run] [--include-closed]
                                 # import a beads (bd) database: priorities, types,
                                 # deps, epics, in-progress state; resumable
                                 # defaults: --file .beads/issues.jsonl, --state
                                 # github-migration.json next to it, --throttle 500ms
hew migrate local [--state F] [--dry-run] [--throttle D]
                                 # move a local tracker to GitHub (the --repo or git
                                 # remote repository): every issue, close reasons,
                                 # comments, edges, #n references rewritten; then
                                 # .hew/migrated.json records the number map and the
                                 # repo switches to GitHub, keeping the local file as
                                 # an archive. Resumable; default --state
                                 # .hew/migration-state.json
```

Output is one compact line per issue, annotated with whatever keeps it from
being plain ready work (`[blocked by #120]`, `[epic 2/6]`, `[in progress @you]`);
`list` sorts ready work first, then claimed, blocked, and epics. Every command
takes `--json` (stable flat schema; list commands emit NDJSON so output survives
truncation and grep); every GitHub-touching command also takes `--repo owner/name`
(`hooks` is local-only). Exit codes are meaningful: `3`
means "already claimed by someone else, pick the next ready item", `5` means
"already claimed by you, resume that work", `6` means "blocked by open issues,
pick the next ready item", `4` means "run `gh auth login`".

### Drafting a body before publishing

`hew create` warns about code-shaped text outside backticks, but only after the issue is
written. To fix the body first, compose it locally — no GitHub credentials, network access,
or writes — revise the file, check it, and publish those same bytes:

```sh
hew draft --goal "..." --approach "..." --done-when "..." --out draft.md
$EDITOR draft.md                     # wrap flagged commands and paths in backticks
hew draft --check draft.md           # names any text still outside code spans
hew create --type task --title "..." --body-file draft.md
```

`draft` takes the section flags `create` takes and composes the same body; it reports findings
but never inserts backticks for you, and `--out` refuses a file that already exists so a
revised draft is not overwritten. `hew epic create --body-file draft.md` publishes an epic body
the same way. `create` itself still only warns.

### Writing a PR body

`hew pr` composes `### What` / `### Why` / `### Testing`, plus `### Risk` when you pass
`--risk`. What and Why default to the issue's `Fix`/`Approach` and `Problem`/`Goal`.
Lay each section out one claim per line, repeating the flag once per line, so it reads as
a list on GitHub rather than a dense paragraph:

- Testing: give Before: and After: each its own line — the test or output that failed, then the same thing passing — then a short list of the checks run; "tests pass" is a claim, not evidence
- Risk: give a Reversibility: line (one-way or two-way door) and a Blast radius: line (what breaks if this is wrong), with anything else on its own line

For a diff sketch, file tree or diagram, compose the body in a file and use
`--body-file`; the section flags are for short text.

`--body-file` gets the same section warnings as the flags path, on create and on
`--amend`. `--amend` preserves `Risk` like the other sections; an explicitly empty
`--risk` removes it. A draft may omit `Risk`; `--ready` (or amending a PR already open
for review) prompts for it, because that is when a reviewer starts weighing the merge.

### Checking for duplicates

Untriaged issues (missing a priority or type label) are held behind `hew triage`,
so "does this already exist?" is answered in two calls, one per population:

1. `hew search <terms>` — the default. Server-side, cheap, open and closed,
   vetted issues only, so "already fixed" answers the question as well as
   "already filed". Results are capped; it warns rather than paging, and when
   none of the matches it saw was triaged it says so — scoped to the fetched
   page when the results were capped — and names `hew triage --search`
   instead of pretending there is nothing to dedup against.
2. `hew triage --search <terms>` — the same search over the untriaged queue,
   open and closed. Run it when the user asks, or in a scoped triage agent
   (see below); a coding agent that never runs it still dedups correctly
   against vetted work.
3. `hew show <n>` — only to read a specific candidate the first two surfaced.

Then file with `hew create ... --discovered-from <n>`.

Both triage modes scan titles and bodies for suspicious Unicode and annotate
the issue line with findings such as `[contains zero-width characters]`,
`[contains bidi controls]`, `[contains Unicode tags]`, or
`[contains confusable characters]`. Confusables are non-ASCII characters that
resemble ASCII letters or digits, including Greek/Cyrillic lookalikes and
mathematical alphabets. Recognized Unicode 17 emoji sequences are exempt,
including their joiners and flag tags; ordinary accents and punctuation are
allowed. These are review hints: legitimate language or notation may trigger
them, and issues are never hidden or rejected. Under `--json`, flagged issues
carry a `textFindings` object with `zeroWidth`, `bidiControl`, `unicodeTags`,
and `confusable` booleans; clean issues omit it.

`hew show <n>` runs the same scan over the title, the body, and each displayed
comment separately, and says which one to look at. Text output adds a block
ahead of the body:

```text
text findings:
  title: contains confusable characters
  comment 2 (@mallory): contains bidi controls
```

Comments are numbered in the order shown, so when the thread is capped the
number counts the comments displayed, not the whole thread. Under `--json`, a
`textFindings` array carries one entry per flagged field: `source` is
`title`, `body`, or `comment`, a comment entry adds `commentIndex` (its
position in the `comments` array), and each carries the same four booleans.
Clean issues omit both the block and the array. The text itself is rendered
exactly as before. Other reads and write commands' `--json` output don't
scan.

### Denying untriaged content in a harness

`hew triage` is the only command that emits untriaged titles and bodies (`hew
show <n>` reading one issue by number is the deliberate exception), and the
primer instructs agents never to run it unless the user explicitly asks. So
the command name is the unit a harness deny list can key on: deny it, and an
agent cannot reach unvetted content even by passing its own flags.

`hew hooks install` writes no permissions block — silently restricting an
agent from a hook installer would be surprising. Add the deny entries
yourself; in Claude Code's `.claude/settings.json`, for example:

```json
{
  "permissions": {
    "deny": [
      "Bash(hew triage *)",
      "Bash(hew * triage *)"
    ]
  }
}
```

Two rules because `hew`'s global flags parse in either position: the first
covers `hew triage` and trailing flags, the second covers the same command
with `--json` or `--repo owner/name` leading — `hew --repo o/r triage` would
sail past the first rule's prefix. The `*` in the second rule matches
anything between `hew` and `triage`, including more than one flag. Together
they cover every valid arrangement of the command; a test in `cmd/hew` keeps
the documented rules honest against the real flag surface.

The instruction and the deny entries are the two layers of the same boundary:
the primer line holds for harnesses with no deny mechanism, and the deny
entries enforce it where one exists. The natural split is two agent shapes —
a **coding agent** denies `hew triage` and dedups with `hew search`, the right
scope for an agent that can only act on vetted work; a **scoped triage agent**
(like the auto-triage workflow below) is allowed triage plus `show` and does
the untriaged half of dedup with `hew triage --search`.

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
does, and must name an existing issue (closed is fine; a pull request number is
not an issue): `apply` refuses a plan with a missing one before any write,
`--dry-run` included. Bodies come from the same section fields the create
flags use — `where`, `problem` or `goal`, `fix` or `approach`, `done-when`
(a list, one checklist item each) — composed into the body template; `body` carries raw
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
again. The same rules apply to `migrate beads` and its snapshot, and to `migrate local` and `.hew/issues.jsonl`.

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
- The tool allowlist is a handful of `hew` subcommands — including `hew triage`,
  since this is the scoped triage agent that runs the untriaged half of dedup —
  plus `gh issue comment` and `gh label list`. No `create`, no `start`, no shell
  beyond that.
- It assigns P2–P4 only; anything it thinks is P0/P1 it labels P2 and flags for a
  human to upgrade. Reports too vague to classify are left untriaged
  (`hew triage` still lists them) rather than mislabelled.
- Bot-filed issues and issues from anyone with write access are skipped, so
  `hew create` output and the workflow's own writes can't feed it back.

Issue bodies are untrusted input; the permission scope and the allowlist are the
mitigation, not the prompt's instructions.

The same assumption shapes the read path: `ready`, `prime`, `list` and `search`
all omit untriaged issues — `prime` runs as a SessionStart hook, and `list` and
`search` are calls an agent can reach on its own initiative, flags included.
GitHub drops labels from non-collaborators, which makes a priority and type
label evidence that a maintainer saw the issue, and evidence a stranger cannot
forge. Untriaged content is emitted by exactly one list-shaped command,
`hew triage`, and the primer tells agents never to run it unless the user
asks; the command boundary is what a deny list enforces (see above). The
primer still reports how much is waiting.

## Design

See [DESIGN.md](DESIGN.md) for the conventions the tool enforces, the read-path
normalization rules, and the API strategy.

The token-lean claim is measured, not asserted: `hew ready` and `hew list` cost
16–25 tokens per open issue against 75–84 for the leanest equivalent GraphQL query
and 104–141 for `gh issue list --json`, which cannot answer readiness at all.
The "Token efficiency" section of [DESIGN.md](DESIGN.md) has the full table,
including the one command where the tool currently loses (`list --json`, filed
as #62); [evals/](evals/) is the harness that produced it.
