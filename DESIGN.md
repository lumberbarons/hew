# issues — an agentic-first CLI for GitHub Issues

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
| `bd ready` — zero open blockers | `issues ready` | `blockedBy` (native dependencies), filtered + priority-sorted |
| `bd prime` — session-start context injection | `issues prime` | generated from live repo state + built-in conventions |
| hierarchical IDs (`bd-a3f8.1`) for epics | `issues epic` | native sub-issues (`parent` / `subIssues`) |
| `bd update --claim` — claim: assign + in-progress | `issues start` | assign `@me` + `in-progress` label |
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
- **Workflow**: `ready` → `start` → branch (`feat/`|`fix/`|`chore/`) → PR with
  `Fixes #n`. Closing via PR is the norm; `close` is for wontfix/duplicate.
- **Claiming is guarded**: `start` refuses an issue that is already assigned or
  `in-progress` and exits with a distinct code, so an agent loop moves on to the
  next ready item instead of doubling up. GitHub has no conditional writes, so the
  guard is check-then-act with a re-read after claiming — a small race window
  remains (see open questions).
- **Untriaged, not broken**: an issue missing its priority or type label — typical
  for anything filed outside the tool — is *untriaged*, a normal state. `issues
  triage` lists them so a human or agent can label each via `set`; nothing is ever
  stamped with defaults automatically, since auto-labeling someone else's report
  destroys information.
- **Contradictions** (two priority labels, an in-progress epic, a dependency cycle)
  are the only per-issue warnings `prime` emits; normalization still picks a
  deterministic answer in the meantime. Cycles matter most: their members all have
  open blockers, so they'd otherwise drop out of `ready` without a trace.

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
- Untriaged issues do appear in `ready` (invisible work is the failure mode), sorted
  after explicitly-prioritized work. `start` on an untriaged issue requires
  `--priority` — claiming forces triage.

## Command surface (v1)

```
issues prime                      # session-start context (see below)
issues ready                      # open, non-epic, zero *open* blockers; sorted
                                  # P0→P4 then P?, oldest first within a priority
issues list [--label X] [--epic N] [--closed]
            [--bodies]           # with --json: body on every line — whole-tracker
                                  # dedup in a single call instead of a show per candidate
issues show <n>                   # detail: body, deps, parent, children, recent comments
issues search <terms>             # repo-scoped text search over open+closed issues in
                                  # best-match order — the dedupe step before filing
                                  # discovered work ("already fixed" answers the question
                                  # as well as "already filed"); results capped, warns
                                  # on truncation instead of paging through

issues create --type bug|enhancement|task [--priority P0..P4] [--area X]
              [--blocked-by N...] [--parent N] [--discovered-from N]
              --title "..." [--where X] [--problem|--goal "..."]
              [--fix|--approach "..."] [--done-when "..."]...
              [--body-file F | --edit]
issues start <n> [--priority P0..P4] [--force]
                                  # guarded claim: refuses if already assigned or
                                  # in-progress (distinct exit code — pick the next
                                  # ready item); --force steals; untriaged issues
                                  # require --priority (claim = triage)
issues triage                     # untriaged issues (missing priority/type), oldest
                                  # first — work through them with `set`
issues set <n> [--priority P0..P4] [--type bug|enhancement|task] [--add-area X]
           [--remove-area X] [--parent N | --no-parent] [--title "..."]
                                  # retriage/edit within conventions (swaps the old
                                  # priority/type label, never stacks a second one)
issues pr [--for N] [--title "..."] [--what|--why|--testing "..."]
          [--body-file F] [--base BRANCH] [--ready]
                                  # the PR step of the workflow, composed rather than
                                  # freeform: draft PR for the issue this branch is
                                  # for, body from the What/Why/Testing template with
                                  # exactly one "Fixes #n" (see below)
issues close <n> --reason "..."   # comment + close (not-planned unless --completed
                                  # or --duplicate-of M)
issues block <n> --on <m>         # add dependency (cycle-checked)
issues unblock <n> --from <m>
issues epic create --title "..." [--children N,N,N]
                   [section flags | --body-file F | --edit]
issues epic status [<n>]          # progress rollup per epic
issues apply <plan.jsonl>         # batch-create from a JSONL plan: one entry per line
                                  # (title/type/priority/areas, the same section
                                  # fields as the create flags or a raw body, parent
                                  # and blocked-by), the migrate machinery generalized.
                                  # Entries carry a local id so later entries can
                                  # reference earlier ones before numbers exist, the
                                  # way migrate resolves bead IDs; "type":"epic" makes
                                  # a parent issue, so epics with bodies work too.
                                  # Checkpointed after every create → resumable
                                  # without duplicates; --dry-run plans. Plan-internal
                                  # dependency cycles are rejected up front — a
                                  # complete check, since pre-existing issues can't
                                  # reference entries that don't exist yet.
issues init                       # bootstrap labels in a repo; print CLAUDE.md snippet
issues hooks install|remove       # Claude Code SessionStart hook running `issues prime`
                                  # in the project's .claude/settings.json — the hook
                                  # variant of prime's "CLAUDE.md instruction or hook"
issues migrate beads              # import a beads (bd) database from .beads/issues.jsonl
                                  # (parsed raw — no bd dependency): P0-P4 and types map
                                  # to labels, blocks→blocked-by, parent-child→sub-issues,
                                  # in_progress→claim, close_reason→closing comment, with
                                  # a provenance footer. Open beads by default (real dbs
                                  # are >95% closed); --include-closed for full history.
                                  # Resumable via a state file; --dry-run plans.
```

Global flags: `--json` (structured output, stable schema), `--repo owner/name`
(default: detect from git remote via go-gh).

### `issues pr`

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

### `issues prime`

The session-start ritual, modeled on `bd prime`: one command whose output an agent
injects at the top of a session (via CLAUDE.md instruction or hook) instead of
maintaining hand-written workflow prose. Three parts:

1. **Static primer** — the conventions and workflow above, compressed to a few
   hundred tokens, including the tool's own command cheatsheet.
2. **Live state** — ready work (top N by priority), in-progress issues and their
   assignee, epics with progress (`#137 Voltgo 2/6`), and open-blocker counts.
3. **Warnings** — contradictions only (`⚠ #42 has two priority labels`,
   `⚠ dependency cycle #3 → #4 → #5 → #3: none will be ready`). Absences
   are not warnings: untriaged work rolls up to a single line (`7 untriaged →
   issues triage`), so a public repo full of drive-by reports doesn't drown the
   primer. Section omitted entirely when the repo is clean.

Sketch:

```
# issues primer — lumberbarons/solar-controller
Workflow: issues ready → issues start <n> → branch (feat/|fix/|chore/) → PR "Fixes #n".
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

## Output principles

- Default output is compact fixed-column text, one line per issue, no ANSI when not
  a TTY, no URLs (agents know `#n` + repo), stable sort order. Non-ready state is
  annotated inline (`[blocked by #120]`, `[epic 2/6]`, `[in progress @user]`), and
  `list` sorts ready work first, then claimed, blocked, epics — one call answers
  both "what's actionable" and "what's stuck on what" (solar-controller dogfood
  feedback, 2026-07-11).
- `--json` everywhere, with a flat schema (deps as number arrays, not
  `{nodes:[...]}` wrappers — hide GraphQL shapes from consumers). List-shaped
  output is NDJSON, one compact object per line: a truncated JSON array is
  unparseable garbage, a truncated NDJSON stream is just shorter (same feedback).
  The primer states both formats so agents reach for the cheap one.
- Errors are one line, actionable, exit codes meaningful (`ready` with no results
  exits 0 with `no ready work`; `start` on a claimed issue exits 3 with
  `already claimed`; auth failure exits 4; etc.).

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
  - `cmd/issues/` — main, urfave/cli command wiring
  - `internal/gh/` — thin API layer (interface, so commands are testable against a fake)
  - `internal/model/` — Issue/Epic domain types, ready/normalization/cycle logic
    (pure, unit-tested)
  - `internal/render/` — text + JSON renderers (golden-file tests)
  - `internal/conventions/` — labels, body and PR templates, primer text (the opinions
    live here)
  - `internal/git/` — local branch state for `pr` (current branch, has-upstream)
- **Testing**: unit tests against a fake API layer; golden files for renderer output.
  An integration smoke test against a real scratch repo (behind a build tag, run
  manually — it needs a token and mutates state, so it stays out of CI) is deferred
  for now.

## Build & distribution

- **CI** (GitHub Actions, actions pinned by SHA at their latest versions): one
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
  into `issues --version` via ldflags, and publishes the archives plus a checksums
  file as a GitHub Release. Release notes come from goreleaser's changelog grouping
  over commit prefixes (`feat:`/`fix:`/`docs:`/...), which we already write.
- **install.sh** at the repo root, usable as
  `curl -fsSL https://raw.githubusercontent.com/lumberbarons/issues/main/install.sh | bash`:
  detects OS/arch via `uname`, resolves the latest release through the GitHub API,
  downloads the matching archive, verifies it against the checksums file, and
  installs to `$HOME/.local/bin` (`INSTALL_DIR` overrides; never sudo), printing a
  PATH hint when needed. `go install .../cmd/issues@latest` remains the
  toolchain-native alternative.

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
  holds as long as live sections cap at top-N per section.
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

## Milestones

- **M0 — scaffold**: module, urfave/cli v3 skeleton, go-gh auth + repo detection,
  `issues list` (proves the GraphQL query and renderer end-to-end). The query
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
  snippet that says little more than "run `issues prime`"). Replace solar-controller's
  hand-written conventions section with it.
- **M4 — polish**: `--json` everywhere, pagination hardening, maybe a read cache,
  maybe `remember`. Distribution is `go install` only — no `gh` extension; agents
  invoke the bare `issues` binary and that's the whole interface.

## Open questions

- **Name — resolved.** Binary and repo are `issues`. `is` was considered — no shell
  builtin, POSIX utility, or popular tool conflicts with it — but rejected as
  ungreppable and ambiguous in prose and transcripts (`is block 42 --on 7`). Anyone
  who wants the terse form can `alias is=issues` locally; agents use the real name.
- **`remember`.** beads couples memory to the tracker; Claude Code has its own
  memory system. Skip, or implement as comments on a pinned "agent notes" issue?
  Deferred to M4 — need real usage first.
- **in-progress signal.** Label (visible, filterable) vs assignee-only (no label
  churn). v1: both — assign is the claim, label is the visibility.
- **Multi-repo prime.** Someday `issues prime --all-repos` for a workspace overview?
  Out of scope for v1.
- **Branch coverage.** The Go toolchain only does statement coverage; `gobco` adds
  branch/condition coverage via source instrumentation but is niche and awkward in
  CI. Revisit if statement coverage starts hiding untested branches in practice.
- **Same-user claim races.** Every agent authenticates as `@me`, so two parallel
  sessions that race `start` inside the guard window are indistinguishable by
  assignee or label — both think they won. If this happens in practice, tie-break
  with a claim comment carrying a session nonce (earliest comment wins, loser backs
  off). Deferred until actually observed.
