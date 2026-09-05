# hew — an agentic-first CLI for GitHub Issues

## Vision

An opinionated, single-user CLI that makes GitHub Issues work the way `bd` (beads)
works: instant answers to "what should I work on?", conventions enforced by the tool
instead of prose in CLAUDE.md, and token-lean output designed for agent context
windows. GitHub Issues stays the single source of truth — humans get the web UI,
PRs auto-close issues via `Fixes #n`, and nothing needs syncing.

The tool exists because the raw `gh` CLI has (since v2.94.0) all the *primitives* —
native sub-issues, blocked-by dependencies, JSON fields — but none of the *opinions*:
no ready-work detection, no convention enforcement, verbose output, and every agent
session re-derives the same jq pipelines from scratch.

### Non-goals

- Multi-user / team workflows. This is for one human and their agents.
- A local database or offline mode (v1). Every command hits the API; a cache is a
  later milestone only if latency actually hurts.
- General GitHub client. Anything the tool doesn't have an opinion about, use `gh`.

## Concepts borrowed from beads

| beads | this tool | GitHub mechanism |
|-------|-----------|------------------|
| `bd ready` — zero open blockers | `hew ready` | `blockedBy` (native dependencies), filtered + priority-sorted |
| `bd prime` — session-start context injection | `hew prime` | generated from live repo state + built-in conventions |
| hierarchical IDs (`bd-a3f8.1`) for epics | `hew epic` | native sub-issues (`parent` / `subIssues`) |
| `bd update --claim` — claim: assign + in-progress | `hew start` | assign `@me` + `in-progress` label |
| priorities 0–4 | P0–P4 labels | labels (issue *types* are org-only; labels work on personal repos) |
| token-lean, JSON-optional output | same | `--json` on every command, compact text default |
| `bd remember` — persistent insights | deferred (open question) | overlaps with Claude Code's own memory system |

Explicitly *not* borrowed: Dolt/git storage, hash IDs, sync — GitHub is the backend,
so the entire distributed-state problem beads solves disappears.

## Enshrined conventions

These are the conventions already proven in solar-controller's CLAUDE.md, moved from
prose into code. They are **guarantees on the tool's own write path** — anything
`create`/`set`/`epic` touches conforms — and **normalization rules on the read
path**. GitHub has many entry points (web UI, mobile, bots, drive-by bug reports),
so issues that don't follow the conventions are first-class citizens, not defects:
never hidden, never auto-"repaired". `prime` *teaches* the conventions.

- **Priority labels**, every issue gets exactly one: `P0` (critical) → `P4` (backlog).
  Default `P2`.
- **Type labels**, exactly one: `bug`, `enhancement`, `task`.
- **Area labels**, zero or more, flat names (`tests`, `web-ui`, ...). Created
  sparingly.
- **No title prefixes** — type/priority/area live in labels. Exception: `Epic: ` on
  parent issues, added by the tool.
- **Dependencies are native** (`--blocked-by`), never body text. The tool refuses to
  create cycles — GitHub itself only rejects self-blocks and direct two-issue
  cycles, not longer ones (see spike results).
- **Epics are sub-issue trees.** Epics are never worked directly; `ready` excludes
  them.
- **Discovered work** links back: `Discovered while working on #123` in the body,
  via `--discovered-from 123`.
- **Body template**: `### Where` / `### Problem`|`### Goal` / `### Fix`|`### Approach` /
  `### Done when` (checklist). Composed structurally by the create section flags
  (`--where`, `--problem`|`--goal`, `--fix`|`--approach`, repeatable `--done-when`) —
  headings, order, checklist formatting, and empty-section omission are write-path
  guarantees, not taught conventions. `--body-file` is the escape hatch for
  long-form bodies with code blocks. Wording pairs pick one flag; word choice is
  never policed against the type.
- **Code-shaped text is marked up, and the tool checks rather than guesses**:
  commands, flags, branch names, paths and error strings are written as code
  spans. Unlike the rest of the tool's output, issue and PR bodies are read in a
  browser and outlive the branch, so the characters that carry the meaning — the
  slashes, the double dashes, the braces — are worth two backticks. Composition
  never rewrites the author's text: marking up correctly requires knowing what
  the text *means* (is `--body-file` a flag being named, or part of `gh pr edit
  --body-file`?), and that is not in the token stream, so a transform splits
  compound commands and cannot be iterated out of it. Instead `create` and `pr`
  **warn**, naming the unmarked tokens and the remedy. Reporting inverts the cost
  of a false positive — a bad rewrite ships permanently, a bad warning costs a
  line of stderr — which is what lets the check cover paths and identifiers that
  no safe transform could touch. The checked shapes stay high-precision anyway: a
  warning the author skips past is worse than none, so unbounded shapes (a bare
  command like `go vet`) are left to the convention.
- **Workflow**: `ready` → `start` → branch (`feat/`|`fix/`|`chore/`) → PR with
  `Fixes #n`. Closing via PR is the norm; `close` is for wontfix/duplicate.
- **One dedup sequence, two populations**: the read paths that can answer
  "does this already exist?" split along the triage boundary. `search` first —
  server-side, cheap, spans open and closed, and returns triaged issues only.
  `triage --search <terms>` is the same search over the untriaged queue, both
  states, for the scoped triage agent or when the user explicitly asks.
  `show <n>` only to read a specific candidate the first two surfaced. Then
  `create --discovered-from <n>`.
- **Claiming is guarded**: `start` refuses an issue that is already assigned or
  `in-progress` and exits with a distinct code, so an agent loop moves on to the
  next ready item instead of doubling up. GitHub has no conditional writes, so the
  guard is check-then-act with a re-read after claiming — a small race window
  remains (see open questions).
- **Untriaged, not broken**: an issue missing its priority or type label — typical
  for anything filed outside the tool — is *untriaged*, a normal state.
  `hew triage` lists them so a human can label each via `set`; nothing is ever
  stamped with defaults automatically, since auto-labeling someone else's report
  destroys information. Triage state gates every list-shaped read — see
  read-path normalization below.
- **Contradictions** (two priority labels, an in-progress epic, a dependency cycle)
  are the only per-issue warnings `prime` emits; normalization still picks a
  deterministic answer in the meantime. Cycles matter most: their members all have
  open blockers, so they'd otherwise drop out of `ready` without a trace.
- **Closed targets are refused, not silently written.** `set`, `block` and `unblock`
  read the issue before mutating anyway, so they check its state there and refuse a
  closed one, naming the close reason (`#20 is closed (completed) — pass --closed to
  edit anyway`). The motivating case: an issue closed by a PR merge mid-session
  absorbed a later edit with no signal at all, and the staleness surfaced only by
  accident much later. The state is in the message because it distinguishes a target
  that moved underneath the caller from a mistyped number. `--closed` covers the rare
  deliberate edit — a guard with no override just relocates the work to the web UI,
  where none of these conventions are enforced. `close` on an already-closed issue
  reports the existing state for the same reason, and takes no override: re-closing
  has no effect to authorize.

### Read-path normalization

Deterministic rules, implemented pure in `internal/model` and stated in the `prime`
primer so agents know what they're looking at:

- Missing priority → renders as `P?`, sorts after P4. Multiple priority labels →
  highest wins, plus a warning.
- Missing type → shown without one. Multiple → first of bug|enhancement|task wins,
  plus a warning.
- Epic-ness = *has sub-issues*; the `Epic: ` title prefix is cosmetic. `ready`
  excludes any issue with sub-issues.
- Bodies render as-is. The template is scaffolding for `create`, never retrofitted
  onto issues written by others.
- Untriaged issues are excluded from every list-shaped read except `triage`
  itself: `ready`, `prime`, `list`, and `search` all omit them, in any state
  combination, with no flag to re-include. `show <n>` reads one issue by
  number — the deliberate retrieval path the auto-triage workflow uses to
  read the issue that just arrived — and `triage` emits the queue, oldest
  first, or search matches over it with `--search`. `start` on an untriaged
  issue still requires `--priority` — claiming forces triage.

  The reason is a trust boundary rather than tidiness. `prime` runs as a SessionStart
  hook, so whatever it prints lands in an agent's context with no human in between;
  `list` and `search` are the dedup paths an agent reaches for on its own
  initiative, flags included. On a public repo anyone can file an issue whose
  title is an instruction, and a body too — the untriaged surface is the larger
  half of that vector. GitHub silently drops labels from non-collaborators, so a
  priority and type label is evidence a maintainer looked at the issue —
  permission-verified, not self-asserted, which is what makes triage state a gate
  an outsider cannot forge. It costs hew-created issues nothing: the write path
  labels them by construction.

  There is deliberately no `--include-untriaged` escape hatch on any of the
  excluding commands. An agent loop — or an injected one — could pass it and
  reopen the surface itself. What a denial can key on is a command, not a
  convention an agent is asked to honour, so `triage` is the sole emitter and
  the primer instructs agents never to run it unless the user explicitly asks:
  triage is human-initiated, and asking first is the tell that separates
  legitimate triage from an agent reaching for unvetted text on its own. A
  harness with a deny mechanism enforces the same boundary mechanically —
  `README.md` documents the recommended `permissions.deny` entries, one per
  flag arrangement, and `cmd/hew` keeps a regression test that every valid
  spelling of the command matches them; `hooks install` deliberately writes
  no such block itself.

## Command surface (v1)

```
hew prime                      # session-start context (see below)
hew ready                      # open, non-epic, triaged, zero *open* blockers;
                                  # sorted P0→P4, oldest first within a priority
hew list [--label X] [--epic N] [--state open|closed|all]
            [--bodies]           # with --json: body on every line — triaged-tracker
                                  # dedup in a single call instead of a show per candidate.
                                  # Untriaged issues are omitted in every state — the
                                  # untriaged half of dedup lives behind triage --search.
                                  # --state defaults to open (both states under --epic:
                                  # progress means seeing what is done too); --state all
                                  # spans both; --closed is a back-compat alias for
                                  # --state closed; passing both is a usage error, not a
                                  # precedence rule
hew show <n>                   # detail: body, deps, parent, children, recent comments;
                                  # reads an untriaged issue by number — the deliberate
                                  # retrieval path, never gated
hew search <terms>             # repo-scoped text search over open+closed, triaged
                                  # issues in best-match order — the dedupe step before
                                  # filing discovered work ("already fixed" answers the
                                  # question as well as "already filed"); results capped,
                                  # warns on truncation instead of paging through, and
                                  # when none of the matches it saw was triaged it says
                                  # so — scoped to the fetched page, never claiming
                                  # unseen matches are untriaged — and names
                                  # hew triage --search

hew create --type bug|enhancement|task [--priority P0..P4] [--area X]
              [--blocked-by N...] [--parent N] [--discovered-from N]
              --title "..." [--where X] [--problem|--goal "..."]
              [--fix|--approach "..."] [--done-when "..."]...
              [--body-file F | --edit]
hew start <n> [--priority P0..P4] [--force]
                                  # guarded claim: refuses if already assigned or
                                  # in-progress (exit 3 — pick the next ready item;
                                  # exit 5 when the claimant is you — resume that
                                  # work); --force steals; untriaged issues
                                  # require --priority (claim = triage)
hew triage [--search <terms>] # untriaged issues (missing priority/type), oldest
                                  # first — work through them with `set`. The sole
                                  # emitter of untriaged content, the unit a harness
                                  # deny list keys on; never run except on the user's
                                  # explicit request. --search constrains it to search
                                  # matches over untriaged titles and bodies, both
                                  # states — the untriaged half of dedup.
hew set <n> [--priority P0..P4] [--type bug|enhancement|task] [--add-area X]
           [--remove-area X] [--parent N | --no-parent] [--title "..."]
           [--body-file F] [--closed]
                                  # retriage/edit within conventions (swaps the old
                                  # priority/type label, never stacks a second one);
                                  # --body-file replaces the whole body — an empty
                                  # file is refused rather than blanking it;
                                  # a closed target is refused (naming the close
                                  # state) unless --closed — see Enshrined
                                  # conventions
hew pr [--for N] [--title "..."] [--what|--why|--testing "..."]
          [--body-file F] [--base BRANCH] [--ready]
                                  # the PR step of the workflow, composed rather than
                                  # freeform: draft PR for the issue this branch is
                                  # for, body from the What/Why/Testing template with
                                  # exactly one "Fixes #n" (see below)
hew close <n> --reason "..."   # comment + close (not-planned unless --completed
                                  # or --duplicate-of M)
hew reopen <n> --reason "..."  # comment + reopen, close's inverse; releases a
                                  # stale claim (close leaves it), never retriages,
                                  # and is a no-op on an already-open issue
hew block <n> --on <m> [--closed]      # add dependency (cycle-checked)
hew unblock <n> --from <m> [--closed]
hew epic create --title "..." [--children N,N,N]
                   [section flags | --body-file F | --edit]
hew epic status [<n>]          # progress rollup per epic
hew apply <plan.jsonl>         # batch-create from a JSONL plan: one entry per line
                                  # (title/type/priority/areas, the same section
                                  # fields as the create flags or a raw body, parent
                                  # and blocked-by), the migrate machinery generalized.
                                  # Entries carry a local id so later entries can
                                  # reference earlier ones before numbers exist, the
                                  # way migrate resolves bead IDs; "type":"epic" makes
                                  # a parent issue, so epics with bodies work too.
                                  # Checkpointed after every create and every edge
                                  # wired → resumable without duplicates, and re-running
                                  # an unchanged finished plan is a quiet no-op;
                                  # --dry-run plans. The checkpoint is bound to the repo
                                  # and a digest of the plan, and every issue it maps must
                                  # carry apply's provenance marker — see "Checkpoint
                                  # binding".
                                  # Plan-internal
                                  # dependency cycles are rejected up front — a
                                  # complete check, since pre-existing issues can't
                                  # reference entries that don't exist yet.
hew init                       # bootstrap labels in a repo; print CLAUDE.md snippet
hew hooks install|remove <claude|codex|cursor|opencode>
                                  # session-start injection of `hew prime` in the
                                  # selected agent's project configuration: a
                                  # SessionStart hook for claude and codex, a
                                  # sessionStart hook for cursor whose command
                                  # runs `hew prime --hook-format cursor` — the
                                  # JSON additional_context wrapper Cursor's
                                  # stdout contract requires — and an
                                  # auto-discovered opencode plugin that injects
                                  # the primer as system context, never a chat
                                  # message. Refuses a symlinked directory or
                                  # settings file — a checkout is untrusted input,
                                  # so hew never writes through a link it did not
                                  # create, and says so rather than silently
                                  # redirecting. opencode install/remove is
                                  # idempotent by a marker: hew never clobbers or
                                  # deletes a plugin file it did not write.
hew migrate beads              # import a beads (bd) database from .beads/issues.jsonl
                                  # (parsed raw — no bd dependency): P0-P4 and types map
                                  # to labels, blocks→blocked-by, parent-child→sub-issues,
                                  # in_progress→claim, close_reason→closing comment, with
                                  # a provenance footer. Open beads by default (real dbs
                                  # are >95% closed); --include-closed for full history.
                                  # Resumable via a state file bound to the repo and the
                                  # snapshot digest; --dry-run plans.
```

Global flags: `--json` (structured output, stable schema), `--repo owner/name`
(default: detect from git remote via go-gh).

### `hew pr`

The last step of the claim lifecycle, and the only one the tool used not to
cover: `ready → start → branch → PR` was enforced up to the branch, then handed
off to a freeform `gh pr create`. That gap costs twice — description format
drifts, and a forgotten `Fixes #n` leaves the issue claimed-but-orphaned after
the merge, needing a manual close. A repository PR template doesn't close it:
agents pass `--body` directly, which bypasses templates entirely.

`pr` is composition and guards, not a reimplementation of `gh pr`. Reviews,
merges, checks and PR listing stay where they are.

- **Which issue.** The claim is the primary signal — it's tracker state, not a
  naming guess: exactly one open non-epic issue assigned to you is the answer.
  A number in the branch name (`feat/30-pr-command`) breaks ties when several
  are claimed and stands in when none is, but only as a whole `-`/`/`-delimited
  segment, so `fix/http500-retries` doesn't link `#500`. It also vetoes a lone
  claim it contradicts: a branch naming a *different* open issue is two signals
  disagreeing, and preferring the claim would close the other issue on merge
  with nothing in the output saying so. Anything still
  ambiguous is a usage error naming the candidates; `--for <n>` settles it. The
  cost of guessing wrong is closing the wrong issue on merge, so guessing is
  not on the menu.
- **Exactly one `Fixes #n`.** The composed body always writes one, plus
  `Part of #<epic>` when the issue is a sub-issue. A `--body-file` gets
  whichever of those links it doesn't already make, so the escape hatch can't
  quietly lose one — and never a second copy. A body already carrying a
  closing keyword (`fixes`/`closes`/`resolves`, any case — GitHub acts on all
  of them) is refused if it names a different issue, or several.
- **Body template.** `### What / ### Why / ### Testing`, mirroring the issue
  template and lives beside it in `internal/conventions`. What and Why default
  to the issue's own `Fix`/`Approach` and `Problem`/`Goal` sections — the issue
  already says this — so in the common case only `--testing` is worth typing.
  Empty sections are omitted, never left as bare headers.
- **Title convention.** A squash merge makes the PR title the commit subject,
  and the release changelog is grouped by conventional-commit prefix, so the
  default title is `<prefix>: <issue title>` with the prefix derived from the
  type label: `bug` → `fix`, `enhancement` → `feat`, `task` → `chore`. Issue
  titles carry no prefix by convention (the label holds that), which is exactly
  why the type is the right thing to derive it from. An untyped issue gets no
  prefix — there is nothing to derive one from, and an invented one files the
  work under the wrong heading. `--title` is passed through untouched, prefixed
  or not, and a title that already carries one is never given a second.
- **Which branch.** The head is the branch *the remote* knows — the upstream
  name with the remote stripped — not the local one. The two diverge whenever a
  checkout is made under a generated name: an agent harness working in a git
  worktree lands on `worktree-feat+x` tracking `origin/feat/x`, and GitHub
  rejects a PR opened from a head it has never seen. Everything downstream of
  the push check follows the resolved name, including the branch-prefix warning
  (it is what reviewers and the changelog see) and the existing-PR lookup. The
  remote is read from `branch.<name>.remote` rather than assumed to be the
  first path segment, since branch names contain slashes too; a branch tracking
  an upstream in this same repository (`remote = .`) has no remote name to
  strip and keeps the local one, as does a branch with no upstream at all.
- **Guards.** Draft by default (`--ready` opens for review). Refuses an
  unpushed branch (GitHub can only open a PR for a ref it can see), the default
  branch itself, a branch that already has an open PR (named, rather than
  relayed as the API's 422), and an epic as the target. Warns — rather than
  refuses — on a branch outside `feat/|fix/|chore/|docs/` and on an issue
  claimed by someone else: the work is already committed by then, so refusing
  would only strand it.

Local branch state comes from `internal/git`, injected into the App as a
function the way `--edit`'s editor is, so the command stays testable without a
checkout. Two API calls beyond the issue read: one query for the default branch
and any existing PR on the head, one REST create.

### `hew prime`

The session-start ritual, modeled on `bd prime`: one command whose output an agent
injects at the top of a session (via a CLAUDE.md instruction or an agent
hook installed by `hew hooks install`) instead of maintaining hand-written
workflow prose. Three parts:

1. **Static primer** — the conventions and workflow above, compressed to a few
   hundred tokens, including the tool's own command cheatsheet.
2. **Live state** — ready work (top N by priority), in-progress issues and their
   assignee, epics with progress (`#137 Voltgo 2/6`), and open-blocker counts.
3. **Warnings** — contradictions only (`⚠ #42 has two priority labels`,
   `⚠ dependency cycle #3 → #4 → #5 → #3: none will be ready`). Absences
   are not warnings: untriaged work rolls up to a single line (`7 untriaged →
   hew triage`), so a public repo full of drive-by reports doesn't drown the
   primer. Section omitted entirely when the repo is clean.

Sketch:

```
# hew primer — lumberbarons/solar-controller
Workflow: hew ready → hew start <n> → branch (feat/|fix/|chore/) → PR "Fixes #n".
File discovered work with --discovered-from. Never work an epic directly.

## Ready (3 of 14 open)
#120 P2 enhancement  Voltgo BLE battery controller: scaffold, client, collector
#117 P1 bug (tests)  Tautological assertions on state the code cannot modify
#119 P2 enhancement  Proper auth: login flow with sessions, API keys

## In progress (1)
#124 P2 bug (tests)  /api/info verified by substring matching  @lumberbarons

## Epics
#137 Voltgo BLE battery controller support  0/6
```

Everything after the header is one line per issue: `#n priority type (areas) title`.
No URLs, no timestamps, no prose. Target: whole primer under ~600 tokens for a
typical repo.

Agent hooks consume prime differently: Claude Code and Codex inject its stdout
as text, but Cursor's sessionStart hook parses stdout as JSON and reads
`additional_context` (snake_case only — the camelCase `additionalContext`
spelling is ignored). `hew prime --hook-format cursor` therefore renders the
same primer into `{"additional_context": ...}` in Go — no shell wrapper around
`python3` or `jq` — and that flag, not bare `hew prime`, is what
`hew hooks install cursor` writes. prime never reads stdin, so Cursor's hook
payload cannot leak into the primer; the flag refuses `--json`, since the hook
object is neither the text primer nor the data schema.

## Output principles

- Default output is compact fixed-column text, one line per issue, no ANSI when not
  a TTY, no URLs (agents know `#n` + repo), stable sort order. Non-ready state is
  annotated inline (`[blocked by #120]`, `[epic 2/6]`, `[in progress @user]`), and
  `list` sorts ready work first, then claimed, blocked, epics — one call answers
  both "what's actionable" and "what's stuck on what" (solar-controller dogfood
  feedback, 2026-07-11).
- Text output colorizes on a TTY only, with the landing page's palette: issue
  numbers amber `#f2a93b`, priorities green `#5fd68b`, secondary text dim
  `#6e7d71` — truecolor SGR wrapped around semantic spans only, so column
  alignment is computed on the plain strings. The decision is made once in
  `main` against the real stdout (`internal/render.ColorEnabled`): color
  requires a TTY, `TERM` not `dumb`, and `NO_COLOR` unset — `FORCE_COLOR=1`
  opts back in, for tests and pagers. `--json`, pipes, and every golden test
  stay byte-identical.
- `--json` everywhere, with a flat schema (deps as number arrays, not
  `{nodes:[...]}` wrappers — hide GraphQL shapes from consumers). List-shaped
  output is NDJSON, one compact object per line: a truncated JSON array is
  unparseable garbage, a truncated NDJSON stream is just shorter (same feedback).
  The primer states both formats so agents reach for the cheap one.
- Errors are one line, actionable, exit codes meaningful (`ready` with no results
  exits 0 with `no ready work`; `start` on an issue claimed by someone else exits 3
  with `already claimed`; `start` on an issue you already claimed exits 5, because
  the response is to resume that work rather than to pick a different item; auth
  failure exits 4; etc.). The exit code is the signal agents branch on — a
  distinction worth acting on gets a code, not a `--json`-only field.

## Checkpoint binding

The batch writers (`apply`, `migrate beads`) checkpoint a source-key → issue-number
mapping so an interrupted run resumes instead of duplicating. That file is ordinary
repository content: it sits next to the plan or the beads snapshot, and anything an
agent checks out can contain one. Treating it as an authoritative mapping meant a
supplied state file could point an entry at any issue number and the tool would wire
edges onto it, or comment on and close it (#81).

So a checkpoint is trusted local scratch, never a portable input, and three things
have to agree before a mapping is used:

- **Repository.** The file records `owner/name`; a mismatch is refused. Compared
  case-insensitively, since GitHub treats it that way and a differently-cased
  `--repo` should not invalidate a real checkpoint.
- **Source digest.** A sha256 over the exact bytes of the plan or snapshot. Editing
  the source invalidates the checkpoint — deliberately: the mapping is keyed by
  entries whose meaning the edit may have changed.
- **Provenance marker.** Every issue a batch writer creates carries an HTML comment
  binding the batch kind, the source key, and that same digest. Verification requires
  all three, so a mapping cannot be redirected onto an issue the tool merely knows
  about, nor onto a *different* issue the same run created.

The digest is what makes the marker worth anything. A marker is public text, so
anyone can copy one into an issue they control — but they cannot make an issue that
was created from a different source file carry their digest. That is what closes the
`migrate` case, where the snapshot and the state file both live in the repository and
an attacker who supplies both satisfies the first two bindings for free.

Verification is one pass up front, before any write, and `--dry-run` makes it too —
it reads GitHub but writes nothing, and a dry run that vouched for a mapping the real
run would reject is worse than no dry run. Failures are refusals, never warnings.
Keys are escaped into the marker, because they come from the untrusted source file
and an unescaped `-->` would let one entry mint provenance for another.

The state file carries a schema `version`. Anything without one predates the binding
and cannot be resumed safely; the error says so, and says that starting fresh
re-creates whatever the interrupted run already created, since the alternative is an
agent silently duplicating half a plan.

## Architecture

- **Language/runtime**: Go (single static binary, `go install`-able).
- **CLI framework**: `github.com/urfave/cli/v3` (v3.10.1 at time of writing).
- **GitHub access**: `github.com/cli/go-gh/v2` (v2.13.0) — reuses `gh`'s stored
  credentials and host config, provides REST + GraphQL clients and repo detection
  from the git remote. No own auth flow at all; `gh auth login` is a prerequisite.
- **API strategy**: one GraphQL query per command where possible. `ready`/`prime`
  fetch all open issues with `blockedBy`, `parent`, `subIssues`, labels, assignees
  in a single paginated query and filter client-side — avoids N+1 and stays trivially
  inside rate limits for single-user scale. Where the API offers server-side rollups
  (`subIssuesSummary`, `issueDependenciesSummary`), prefer them over fetching nested
  nodes just to count.
- **Layout**:
  - `cmd/hew/` — main, urfave/cli command wiring
  - `internal/gh/` — thin API layer (interface, so commands are testable against a fake)
  - `internal/model/` — Issue/Epic domain types, ready/normalization/cycle logic
    (pure, unit-tested)
  - `internal/render/` — text + JSON renderers (golden-file tests)
  - `internal/conventions/` — labels, body and PR templates, primer text (the opinions
    live here)
  - `internal/git/` — local branch state for `pr` (current branch, its upstream
    name, has-upstream)
- **Testing**: unit tests against a fake API layer; golden files for renderer output.
  An integration smoke test against a real scratch repo (behind a build tag, run
  manually — it needs a token and mutates state, so it stays out of CI) is deferred
  for now.

## Build & distribution

- **CI** (GitHub Actions, actions pinned by SHA at their latest versions): a
  workflow triggered on PRs and pushes to `main`, running `golangci-lint` and the
  full test suite (`go test -race -coverprofile ./...`), plus `shellcheck` on
  `install.sh` — the one thing strangers pipe into bash gets linted like everything
  else. Go version comes from `go.mod`.
- **Coverage gate**: 90% minimum, blocking. Go's toolchain measures *statement*
  coverage only (line-equivalent in practice; there is no native branch coverage —
  see gobco note in open questions), enforced with `go-test-coverage` in CI.
  `cmd/` wiring is excluded so the bar bites on the logic packages
  (`internal/model`, `internal/render`, `internal/conventions`, `internal/gh`).
- **Dependabot** keeps the SHA-pinned actions and Go modules fresh (`github-actions`
  and `gomod` ecosystems), configured with a cooldown so new releases settle before
  we pick them up.
- **Releases are tag-driven**: pushing a `vX.Y.Z` tag runs goreleaser, which builds
  static binaries for linux and macOS (amd64 + arm64, CGO off), stamps the version
  into `hew --version` via ldflags, and publishes the archives plus a checksums
  file as a GitHub Release. Release notes come from goreleaser's changelog grouping
  over commit prefixes (`feat:`/`fix:`/`docs:`/...), which we already write.
- **install.sh** at the repo root, usable as
  `curl -fsSL https://lumberbarons.github.io/hew/install.sh | bash`:
  detects OS/arch via `uname`, resolves the latest release through the GitHub API,
  downloads the matching archive, verifies it against the checksums file, and
  installs to `$HOME/.local/bin` (`INSTALL_DIR` overrides; never sudo), printing a
  PATH hint when needed. `go install .../cmd/hew@latest` remains the
  toolchain-native alternative. The `publish-install` workflow mirrors `install.sh`
  and `site/index.html` to the `gh-pages` branch (GitHub Pages source) whenever
  either changes, so the stable install URL and the landing page at
  https://lumberbarons.github.io/hew/ always serve the same content as `main`.
  The landing page is a self-contained static site — one HTML file plus
  `robots.txt` and `sitemap.xml` — with no build step and no external assets.

## Spike results (2026-07-10)

The design's riskiest assumptions, tested against the live GitHub API before writing
any product code.

- **GraphQL surface exists — pass.** The `Issue` type exposes `blockedBy`, `blocking`,
  `parent`, `subIssues`, and — a bonus the design didn't assume — server-side rollups
  `issueDependenciesSummary` and `subIssuesSummary`, which `epic status` and blocked
  counts should prefer over client-side counting. The mutations `addBlockedBy` /
  `removeBlockedBy` / `addSubIssue` / `removeSubIssue` / `reprioritizeSubIssue` all
  exist, so `block`, `unblock`, and `epic create` have first-class API support. No
  preview/feature headers required for any of it.
- **Single "fetch everything" query — pass.** One request for all open issues with
  labels, assignees, `parent`, `subIssues`, and `blockedBy` against solar-controller
  (19 open issues) completes in ~300 ms. `blockedBy` nodes carry `state`, so `ready`
  treats closed blockers as non-blocking with no extra queries. Rate limits and
  latency are non-issues at single-user scale; the no-cache-in-v1 call stands.
  Caveat confirmed: nested connections don't paginate with the outer issues cursor —
  v1 caps them (`first: 50` sub-issues, `first: 20` blockers) and must warn when
  `totalCount` exceeds the cap rather than silently truncate.
- **go-gh smoke test — pass.** A throwaway `main.go` (~80 lines) using
  `repository.Current()` and `api.DefaultGraphQLClient()` detected the repo from the
  git remote, reused `gh`'s keyring credentials with no auth code of our own, ran the
  query above, and computed 13 ready of 19 open with the client-side filter. This is
  effectively M0's skeleton.
- **`prime` token budget — pass.** A full mock primer
  ([docs/primer-mock.md](docs/primer-mock.md)) for a busy repo — static conventions
  and command cheatsheet plus live Ready / In progress / Blocked / Epics sections —
  measures ~640 tokens (tiktoken `o200k_base`; Claude's tokenizer typically runs
  slightly higher). The split is roughly half static, half live, so the ~600 target
  holds as long as live sections cap at top-N per section. Superseded in part by
  the measurements below: live primers run 885–933 tokens, so the cap-at-top-N
  assumption did not hold on its own.
- **Cycle rejection — partial; client-side check confirmed necessary.** Tested live
  with throwaway issues (deleted afterwards). The API rejects self-blocks (`Target
  issue cannot be the same as the source issue`) and direct two-issue cycles (`this
  dependency would create a cycle where the target is already blocked by the
  source`) as typed GraphQL `VALIDATION` errors — but **accepted a three-issue
  cycle** (A←B, B←C, then C←A) without complaint: the edges are stored, returned by
  `blockedBy`, and counted by `issueDependenciesSummary` as if nothing were wrong.
  Two consequences. First, `block` and `create --blocked-by` must run a transitive
  cycle check client-side before mutating — the fetch-everything query already has
  the whole graph. Second, since cycles can be created outside the tool (web UI,
  raw API), the read path must detect them too: every member of a cycle has an open
  blocker, so a cycle silently excludes all its members from `ready` forever.
  `prime` and `ready` warn when they see one.

## Token efficiency (measured 2026-07-29)

The token-lean claim, measured rather than asserted. The harness lives in
[evals/](evals/): `capture` records both sides' raw output from a live repo into
a committed fixture, `report` tokenizes that fixture offline with tiktoken
`o200k_base` — the same encoding as the `prime` spike above, so the figures are
comparable. No model calls, no variance. Regenerate with:

```sh
cd evals && go run ./cmd/tokens report fixtures/solar-controller fixtures/hew
```

The baseline is deliberately charitable: the leanest hand-rolled GraphQL query
that answers the same question, with no bodies, timestamps, or URLs the question
doesn't need ([evals/cmd/tokens/queries/](evals/cmd/tokens/queries/)). `gh` has
no native equivalent for readiness — `gh issue list` cannot return `blockedBy`,
`parent`, or `subIssues` at all — so rows marked † are the cheaper output that
cannot actually answer the question, priced for comparison.

lumberbarons/solar-controller — 27 open issues:

| command | hew | raw gh | ratio | baseline |
|---|---|---|---|---|
| `hew ready` | 538 | 2120 | 3.9x | gh api graphql (open issues) |
| `hew ready` | 538 | 3621 | 6.7x | gh issue list --json † |
| `hew list` | 606 | 2120 | 3.5x | gh api graphql (open issues) |
| `hew list --json` | 3017 | 2120 | 0.7x | gh api graphql (open issues) |
| `hew prime` | 1161 | 2120 | 1.8x | gh api graphql (open issues) |
| `hew show #123` | 175 | 341 | 1.9x | gh issue view --json + gh api graphql |
| `hew epic status 137` | 130 | 308 | 2.4x | gh api graphql (epic + children) |

Findings, including the ones that don't flatter the tool:

- **The claim holds for the reads an agent actually loops on.** `ready` and
  `list` cost 17–22 tokens per open issue against ~79 for the equivalent GraphQL
  and 112–134 for `gh issue list --json` — 3.5x–4.7x and 5.7x–6.7x respectively
  across both fixtures. This is the whole-tracker read that happens every
  iteration, so it is where the saving compounds.
- **`show` and `epic status` save less: 1.3x–2.4x.** Bodies dominate `show` and
  both sides carry them verbatim; the value there is the deps line, not the
  token count. Worth saying plainly rather than averaging into a headline.
- **`list --json` currently costs *more* than the raw GraphQL it replaces
  (0.7x on both fixtures).** The flat schema writes every key on every line —
  empty arrays, derived booleans, `createdAt` — where the lean query writes seven
  fields. The schema's stability is deliberate (agents parse it), but the token
  cost is a real regression against the tool's own claim, filed as #62.
- **`prime` measures 1113–1161 tokens against its ~600 target.** The spike's ~640
  was a mock; live primers on real repos run 86–94% over. Not the static half
  either: the smaller fixture, at 10 open issues, still lands at 1113. Filed
  as #63. These figures reflect the current static half substituted into the
  fixtures' prime files in place — the live sections needed no re-capture,
  since neither fixture's list output carries an untriaged row to lose. The
  substitution also folds in the lines the previous note priced as addenda
  (+41 for #37, +22 for #98); the triage boundary added for #102 grows the
  static half by 39 tokens (854 → 893 under o200k_base), against #63's budget
  as stated in the issue.

## Milestones

- **M0 — scaffold**: module, urfave/cli v3 skeleton, go-gh auth + repo detection,
  `hew list` (proves the GraphQL query and renderer end-to-end). The query
  includes `parent`/`subIssues`/`blockedBy` from day one — field names, header
  requirements, nested-cap behavior, and cycle semantics are all verified (see
  spike results), so this milestone has no API unknowns left. CI (lint + full
  tests) arrives with the scaffold.
- **M1 — read**: `ready`, `show`, `epic status`, `prime` v1. *This is the payoff
  milestone — adopt in solar-controller immediately.* The first tagged release
  (goreleaser + install.sh) ships here, since adoption needs an installable binary.
- **M2 — write**: `create` (template + label enforcement), `set` (retriage —
  priority changes are the most common tracker operation, and doing them through
  the tool is what keeps the one-label invariants true), `triage`, `block`/`unblock`
  with cycle detection, `start`, `close`, `epic create`.
- **M3 — bootstrap**: `init` (create label set in a fresh repo, emit the CLAUDE.md
  snippet that says little more than "run `hew prime`"). Replace solar-controller's
  hand-written conventions section with it.
- **M4 — polish**: `--json` everywhere, pagination hardening, maybe a read cache,
  maybe `remember`. Distribution is `go install` only — no `gh` extension; agents
  invoke the bare `hew` binary and that's the whole interface.

## Open questions

- **Name — resolved.** Binary and repo are `hew`. The tool shipped through v0.6.0
  as `issues`, which was renamed because the name was effectively unsearchable:
  it competes with the literal English word in every query that matters, so the
  project could never be found by anyone who did not already have the URL. `hew`
  carries the two meanings the tool wants — to shape timber (matching the
  `lumberbarons` namespace) and to *hew to* a standard, which is precisely the
  write-path invariant. It was verified free of collisions on GitHub repo names,
  Homebrew formulae and casks, and `$PATH` before adoption. `tally` and `adze`
  were the runners-up, rejected for collisions (`uber-go/tally`, `davidfowl/tally`,
  and an existing `adze` Homebrew cask). `is` was considered much earlier — no
  shell builtin or POSIX utility conflicts with it — but rejected as ungreppable
  and ambiguous in prose and transcripts (`is block 42 --on 7`). Anyone who wants
  a terse form can alias it locally; agents use the real name.
- **`remember`.** beads couples memory to the tracker; Claude Code has its own
  memory system. Skip, or implement as comments on a pinned "agent notes" issue?
  Deferred to M4 — need real usage first.
- **in-progress signal.** Label (visible, filterable) vs assignee-only (no label
  churn). v1: both — assign is the claim, label is the visibility.
- **Multi-repo prime.** Someday `hew prime --all-repos` for a workspace overview?
  Out of scope for v1.
- **Branch coverage.** The Go toolchain only does statement coverage; `gobco` adds
  branch/condition coverage via source instrumentation but is niche and awkward in
  CI. Revisit if statement coverage starts hiding untested branches in practice.
- **Same-user claim races.** Every agent authenticates as `@me`, so two parallel
  sessions that race `start` inside the guard window are indistinguishable by
  assignee or label — both think they won. If this happens in practice, tie-break
  with a claim comment carrying a session nonce (earliest comment wins, loser backs
  off). Deferred until actually observed. Exit 5 does not address this: it tells a
  caller the claim is `@me`'s, which is exactly the point at which one of *my* own
  sessions cannot tell whether the claimant is itself or a sibling.
