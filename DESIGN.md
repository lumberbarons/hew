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
- A cache or offline mirror of GitHub. Against GitHub every command hits the API; a
  cache is a later milestone only if latency actually hurts. A repo with no GitHub
  tracker at all is a different thing — the local backend, below — not a cache of one.
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
so the entire distributed-state problem beads solves disappears. The local backend
keeps that: one file at one checkout, never synced anywhere.

## Enshrined conventions

These are the conventions already proven in solar-controller's CLAUDE.md, moved from
prose into code. They are **guarantees on the tool's own write path** — anything
`create`/`set`/`epic` touches conforms — and **normalization rules on the read
path**. GitHub has many entry points (web UI, mobile, bots, drive-by bug reports),
so issues that don't follow the conventions are first-class citizens, not defects:
never hidden, never auto-"repaired". `prime` *teaches* the conventions.

- **Priority labels**, every issue gets exactly one: `P0` (critical) → `P4` (backlog).
  Default `P2`.
- **Type labels**, exactly one: `bug`, `enhancement`, `task`, or `idea` for
  discovery work. `idea` sits last in the normalization precedence. A legacy
  issue with a priority and `idea` as its only recognized type now reads as a
  triaged idea. If it also carries an implementation type, that type wins and
  the read reports the usual multiple-type warning. Existing labels stay
  intact, but normalized areas omit `idea` and new area writes reserve it for
  the type.
- **Area labels**, zero or more, flat names (`tests`, `web-ui`, ...). Created
  sparingly.
- **No title prefixes** — type/priority/area live in labels. Exception: `Epic: ` on
  parent issues, added by the tool.
- **Dependencies are native** (`--blocked-by`), never body text. The tool refuses to
  create cycles — GitHub itself only rejects self-blocks and direct two-issue
  cycles, not longer ones (see spike results).
- **Epics are sub-issue trees.** Epics are never worked directly; `ready` excludes
  them. Every epic the tool creates (`epic create`, `apply` epic entries,
  `migrate beads`) also carries the `epic` label, which is what makes a childless
  one an epic — see read-path normalization.
- **Ideas are investigation work, not implementation.** `create --type idea`
  captures a possible improvement; `ready`, the primer's Ready section and hewd's
  Ready queue exclude it, because those are the automatic paths where an agent
  acts on what it is offered. `start <n> --for-discovery` is the explicit claim
  path and records the caller's intent — an ordinary start refuses an idea,
  forced or not. An investigation produces research, an ADR or a spec; approved
  implementation is handed off as separate work issues (`--discovered-from <n>`),
  since the idea's own scope is a question rather than a diff. An idea associates
  with an epic through an issue reference, not as a sub-issue, so it stays out of
  that epic's progress and next-child sequence. `set --outcome` and hewd's
  Record outcome action append Markdown under `### Outcome` through the shared
  edit operation, which preserves the rest of the issue body. An idea child
  attached outside hew remains visible in epic detail — reads preserve existing relationships —
  but is never selected as the epic's next child.
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
  next ready item instead of doubling up. It likewise refuses an issue with open
  blockers (exit 6); `ready` is not the only blocker filter. GitHub has no conditional writes, so its
  guard is check-then-act with a re-read after claiming — a small race window
  remains (see open questions). The local backend holds an exclusive lock and
  implements the optional atomic claim (`gh.Claimer`), so simultaneous starts
  there have exactly one winner; its default identity is per worktree, which is
  what keeps exit 3 and exit 5 distinguishable under one OS account.
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
- Missing type → shown without one. Multiple → first of bug|enhancement|task|idea
  wins, plus a warning.
- Epic-ness = *has sub-issues, or carries the `epic` label*; the `Epic: ` title
  prefix is cosmetic. `ready` excludes every epic, `start` refuses one, and an
  epic is exempt from triage (it needs no type label). The label exists for the
  window before the first child is attached: `epic create` files the epic, then
  attaches children, so a childless epic is a normal intermediate state, and
  `epic create` with no `--children` is a normal way to start one (#252). Without
  a marker that issue — priority label, no type, no sub-issues — is
  indistinguishable from a drive-by report and falls into the untriaged gate, so
  `epic create` reported success on an issue that `list`, `search` and `epic
  status` then hid. Alternatives rejected: a warning at creation (leaves the
  epic hidden, and `start` would still claim it); giving epics a type label
  (types mean work, so `ready` would offer it); recognising the `Epic: ` title
  prefix (anyone can type a title, which would open the untriaged trust boundary
  below — a label is permission-verified like priority and type). The write path
  enforces the label, the read path never repairs its absence, `epic` is
  reserved from areas like the other convention labels, and the one cost is
  that a repo already using an `epic` label for something else has those issues
  read as epics. Two upgrade edges follow from "never repaired": a childless epic
  filed before the label existed stays hidden until it gets a child (or someone
  adds the label on GitHub), and a repo bootstrapped earlier gets the label
  auto-created grey and undescribed by the first `epic create` until `hew init`
  is re-run to create it with the convention's colour and description.
- Label matching is case-insensitive, as it is on GitHub: `list --label upper`
  finds an issue labelled `UPPER` on both backends, agreeing with the write path,
  which already de-duplicates areas that way.
- Convention labels (priority, type, `in-progress`) are recognized in any case, as
  GitHub treats them as the same label: `p0`, `Bug` and `In-Progress` count as `P0`,
  `bug` and `in-progress` for priority, type, claim and `Areas()`, and a pair like
  `P3` + `p0` is a multiple-priority contradiction. The write path refuses them as
  areas in any case, for the same reason. Flag values (`--type`, `--priority`) are not
  label names and stay exact.
- Areas are listed comma-separated, so `(x,y)` means two areas. `--area x,y` splits
  into two; `apply` plans and direct callers that supply a name verbatim are refused
  a comma in it. `migrate` turns a comma in a source label into a hyphen, with a
  warning. A label created on GitHub with a comma in its name is shown as it is, and
  is the one case that still reads ambiguously. The write path trims whitespace
  around an area name and refuses one that is blank afterwards (exit 2), in `create`,
  `set --add-area` and `apply` alike. In `list`-shaped text the meta column
  is capped at a fixed width in characters; a row whose areas overflow it goes
  unaligned rather than padding every other row.
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

  Both `triage` modes scan raw titles and bodies for zero-width/default-ignorable
  characters, bidi controls, Unicode tags, and non-ASCII characters with ASCII
  alphanumeric skeletons in Unicode's confusables data. Findings are advisory
  annotations, never a filter or an automatic rejection. Text output appends
  the categories once per issue; NDJSON adds `textFindings` only when nonempty,
  with `zeroWidth`, `bidiControl`, `unicodeTags`, and `confusable` booleans.
  `show` is the other deliberate read of untriaged text, so it scans too, but
  per field: title, body, and each displayed comment separately, since a
  finding in a long thread is useless without saying where. Text output adds a
  `text findings:` block ahead of the body, one line per flagged field, with
  comments numbered in display order. `show --json` adds a `textFindings`
  array only when nonempty; each entry names its `source` (`title`, `body`,
  `comment`), a `commentIndex` into the emitted `comments` array for
  comments, and the same four booleans. The comment cap is unchanged, and so
  is the rendering of the text itself: findings annotate, never rewrite.
  Scanning lives pure in `internal/model` and is called only by the triage and
  show renderers. Automatic paths, their token fixtures, and write commands'
  `--json` (which shares show's detail schema) are unaffected.

  The confusables subset and exact emoji exceptions are generated from pinned
  Unicode 17.0.0 data; `internal/model/generate_textscan.py` records the sources
  and regeneration procedure. Emoji exceptions are matched per grapheme cluster,
  allowing ordinary joiners, selectors, and subdivision flag tags without
  exempting extra invisible characters. Curly quotes, dashes, normal accents,
  and decomposed accents are ordinary prose: punctuation lookalikes and non-NFC
  text alone are deliberately not flagged. This is not a full UTS #39 identifier
  validator or a semantic prompt-injection detector; legitimate scripts and
  notation can contain flagged characters and still require human judgment.

## Command surface

The commands and their flags are documented where they run: `hew --help` and
`hew <cmd> --help` are exact, the README is the user-facing tour, and the primer
is what agents see. This document does not restate them — a listing here only
drifts. What follows is the reasoning behind the command behaviors that are not
obvious from a flag name. The public operation behind each command is in
[Tracker library](#tracker-library).

- **Reads.** `ready` is open, non-epic, non-idea, triaged issues with zero *open*
  blockers, sorted P0→P4 and oldest first within a priority. `list` hides
  untriaged issues in every state, and `--state` defaults to open — except
  under `--epic`, where progress means seeing what is done too. `list --bodies`
  with `--json` carries every body so dedup over the triaged tracker is one
  call instead of a `show` per candidate. `show <n>` reads an untriaged issue by
  number: the deliberate retrieval path, never gated.
- **`search`** is the dedupe step before filing discovered work, scoped to the
  repo and spanning open and closed triaged issues in best-match order —
  "already fixed" answers the question as well as "already filed". Results are
  capped and truncation is warned about rather than paged through. When none of
  the matches it saw was triaged it says so, scoped to the fetched page and never
  claiming unseen matches are untriaged, and names `triage --search`.
- **`triage`** is the sole emitter of untriaged content, which makes it the unit
  a harness deny list keys on; it is run only on the user's explicit request.
  `--search` constrains it to untriaged titles and bodies in both states — the
  untriaged half of dedup.
- **`start`** is a guarded claim. It refuses an issue that is assigned or
  in-progress: exit 3 means pick the next ready item, exit 5 means the claimant
  is you and the response is to resume that work. It also refuses an issue with
  open blockers — the issues `ready` hides — with exit 6 naming them, so an id taken
  from `list` or `show` cannot claim work whose prerequisite is unfinished; closed
  blockers do not count, and a capped blocker list warns instead of refusing on
  what it cannot see. `--force` steals a claim or overrides the blockers (the way
  to take an issue stuck in a dependency cycle). An idea requires
  `--for-discovery`, forced or not: investigation is a decision the caller
  declares rather than a routine claim. An untriaged
  issue requires `--priority`, because claiming is triage. `--show` (or
  `--json`) returns the body from the post-claim read with no extra fetch, so a
  separate `show` after claiming is unnecessary; like every mutation it omits
  text-findings annotations.
- **`set`** retriages within the conventions, swapping the old priority or type
  label and never stacking a second. `--body-file` replaces the whole body, and an
  empty file is refused rather than blanking it. A closed target is refused,
  naming the close state, unless `--closed` — see Enshrined conventions. `block`
  and `unblock` follow the same closed-target rule, and `block` is cycle-checked.
  Both take `--on` / `--from` repeatedly (or comma-separated), as `create
  --blocked-by` does. `block` validates every blocker — open, not already an
  edge, cycle-free against the graph as it stands — before the first write, so a
  refusal never leaves half a set; each new edge starts at the target, so a cycle
  through two of them would revisit it and is already a cycle through one. A write
  that fails after others landed says which landed. An edge already present (or,
  for `unblock`, absent) is named and skipped, as a lone one is a no-op.
- **`release`** gives up a claim without re-claiming or changing the issue's
  state, body, or triage. The sole assignee can release it; `--force` explicitly
  allows clearing any owner's claim, including label-only and mixed-owner
  claims. An unclaimed issue is refused. The local backend's optional
  `gh.ClaimReleaser` checks ownership and clears both halves under one lock, so
  a concurrent forced start cannot be cleared using a stale owner read. GitHub
  uses the existing label/assignee mutations and has the same check-then-act
  race limitation as `start`. Removed or moved worktrees leave their local
  claims in place; a surviving checkout can recover them with `release --force`.
- **`reopen`** is `close`'s inverse: comment and reopen. It releases a stale
  claim, which `close` leaves behind, and never retriages. On an already-open
  issue it is a no-op rather than an error, so a retry does not look like a
  failure.
- **`epic status`** with `<n>` lists the children plus the next workable child
  (`next: #k`): the first open, unblocked, triaged, non-idea child in creation
  order, which is the sequence head for epics whose children chain by blocked-by.
  An idea child — only possible through a relationship made outside hew — is
  listed so the relationship stays visible, but never becomes next: the
  sequence is implementation work. Children
  come from parent backlinks over both states, so the view survives a capped
  sub-issue connection, and a capped blocker list on a child warns that `next`
  may be wrong, matching `ready`'s honesty. `show` of an epic names the same
  line — its one two-query read, for epics only.
- **`apply`** batch-creates from a JSONL plan: the migrate machinery
  generalized. Entries carry a local id so later entries can reference earlier
  ones before issue numbers exist, and `"type":"epic"` makes a parent issue.
  Progress is checkpointed after every create and every edge wired, so a run is
  resumable without duplicates and re-running a finished plan is a quiet no-op.
  Plan-internal dependency cycles are rejected up front, a complete check since
  pre-existing issues cannot reference entries that do not exist yet. Numeric
  references (`parent`, `blocked-by`, `discovered-from`) are looked up before the
  first write, and an edge that still fails to wire exits non-zero. See
  Checkpoint binding.
- **`migrate beads`** imports `.beads/issues.jsonl`, parsed raw with no bd
  dependency: P0–P4 and types map to labels, blocks to blocked-by, parent-child
  to sub-issues, in_progress to a claim, and close_reason to the closing comment,
  with a provenance footer. It takes open beads by default (real databases are
  over 95% closed); `--include-closed` brings the full history.
- **`migrate local`** moves a local tracker to GitHub: every issue, closed ones
  included, keeping close reasons and comments (a lead line credits the original
  author and date, the author in a code span so a local handle never mentions a
  real account), wiring parents and blockers, and rewriting `#n` references in
  bodies and comments to the new numbers outside code: inline code spans, fenced
  blocks (including ones nested in list items and blockquotes), and indented
  code blocks. `--dry-run` reads only `issues.jsonl`, so it needs no GitHub
  target — it refuses only when a checkpoint exists, which can be vouched for
  against GitHub alone — and its text plan collapses to a summary past twenty
  issues (`--json` keeps every entry).
  Numbers are unknown until the issues exist, so bodies are created as written and
  edited once all are created; a body a person changed in between is left alone
  with a warning. The tracker is locked for the whole run, so nothing written
  locally is lost behind the snapshot; the run leaves a `.hew/migrating` marker
  beside the lock, and a writer still waiting after half a second reports the
  migration on stderr and, past its bounded wait, fails naming it rather than
  hanging. The marker is advisory and is believed only while the lock is held, so
  one left by a killed run is cleared by the next writer to take the lock. Only
  when every step finished does it write `.hew/migrated.json` (local → GitHub
  number map) and flip `config.json` to `github`; the local file stays as an archive and refuses writes, including from a
  store opened before the flip. A run with unfinished steps leaves the tracker
  local so a rerun can finish, resuming from a checkpoint bound like the other
  batch writers' — which means local issues cannot change between a failed run and
  its rerun.
- **`hooks install|remove`** injects `hew prime` at session start in the selected
  agent's project configuration. Claude and codex get a `SessionStart` hook
  guarded with `command -v hew`, so a contributor without hew gets an install
  hint rather than a red hook error; codex adds a `commandWindows` override of
  bare `hew prime`, as cmd and PowerShell cannot run the guard, and cursor stays
  unguarded for lack of one. Cursor's hook runs `hew prime --hook-format cursor`,
  the JSON `additional_context` wrapper its stdout contract requires. opencode
  gets an auto-discovered plugin that injects the primer as system context, never
  a chat message. A symlinked directory or settings file is refused: a checkout
  is untrusted input, so hew never writes through a link it did not create, and
  says so rather than silently redirecting. opencode install and remove are
  idempotent by a marker, so hew never clobbers or deletes a plugin file it did
  not write.
- **`config`** answers which backend the commands in this checkout would use,
  read-only. It resolves the same target as every other command — `--repo`
  wins, then `.hew/config.json` at the main checkout, then the git remote — but
  opens neither store nor client, so it needs no credentials or network, and
  unlike `init --json` it bootstraps nothing. `--json` is one flat object
  (`backend`, `repo`, `path`, `id`) with empty fields where they do not apply;
  text omits those fields. The stable identity is `repo` on GitHub and the
  tracker `id` locally, because two checkouts can share a directory name and a
  rename must not change it.

Global flags: `--json` (structured output, stable schema), `--repo owner/name`
(default: detect from git remote via go-gh).

### CLI surface compatibility

Command and flag spellings are contract, like exit codes and `--json` output:
scripts and agent loops bind to them, and a rename that lands silently shows
up first as a broken loop. `cmd/hew/testdata/surface.golden` snapshots every
command path, every name a flag answers to (primary and aliases) with its type
and repeatability, and each command's positional usage. `go test ./cmd/hew`
fails when a snapshot entry is gone from the live tree; an addition passes.

The policy the gate enforces: within a minor release a rename keeps the old
spelling as an alias (renaming `--closed` to `--state` is `Name: "state",
Aliases: []string{"closed"}`), and a removal waits for a major release. A failing diff is then
actionable — add the alias — instead of noisy. An intended breaking change
regenerates the golden with `go test ./cmd/hew -run TestSurfaceCompat -update`,
which puts the removal in the PR diff where a reviewer sees it.

Scope is the surface only. A flag that keeps its name but changes meaning, and
parse-position bugs, are invisible to a surface diff; they need behavior-level
tests.

One shape change is not a removal: a single-value flag that becomes repeatable
(`block --on`) still accepts every command line it did, so the differ treats
`int repeatable` as covering an old `int`. Narrowing it back fails.

A flag name means one thing across commands. `--state` is an issue-state filter
(`list`) and `--closed` is the closed-target override (`set`, `block`,
`unblock`); a batch writer's resume file is `--checkpoint` (`apply`,
`migrate beads`, `migrate local`), and `apply` and `migrate beads` take their
input as an argument or `--file`, never both. The spellings that used to
collide (`list --closed`, `--state` on the batch writers) stay as hidden flags
of their own, not aliases: an alias answers to both names through one value, so
nothing could tell which was typed, and the stderr note naming the replacement
has to fire only for the old one. `TestFlagNamesKeepOneMeaning` pins the
vocabulary; the hidden flags leave with the next major release.

### `hew pr`

The last step of the claim lifecycle, and the only one the tool used not to
cover: `ready → start → branch → PR` was enforced up to the branch, then handed
off to a freeform `gh pr create`. That gap costs twice — description format
drifts, and a forgotten `Fixes #n` leaves the issue claimed-but-orphaned after
the merge, needing a manual close. A repository PR template doesn't close it:
agents pass `--body` directly, which bypasses templates entirely.

`pr` is composition and guards, not a reimplementation of `gh pr`. Reviews,
merges, checks and PR listing stay where they are.

- **Which issue.** Inference excludes epics and issues whose normalized type is
  `idea`. Among the remaining open issues, a lone claim assigned to you is the
  primary signal.
  A number in the branch name (`feat/30-pr-command`) breaks ties when several
  are claimed and stands in when none is, but only as a whole `-`/`/`-delimited
  segment, so `fix/http500-retries` doesn't link `#500`. Hew refuses to infer a
  target when a lone claim and a branch number point to different eligible
  issues. Ambiguity also produces a usage error naming the candidates.
  Discovery has its own
  completion workflow, so an idea's claim and branch number are ignored in
  inference, including in the conflict check. Use `--for <n>` to name an idea
  explicitly when merging a documentation PR should close it.
- **Exactly one `Fixes #n`.** The composed body always writes one, plus
  `Part of #<epic>` when the issue is a sub-issue. A `--body-file` gets
  whichever of those links it doesn't already make, so the escape hatch can't
  quietly lose one — and never a second copy. A body already carrying a
  closing keyword (`fixes`/`closes`/`resolves`, any case — GitHub acts on all
  of them) is refused if it names a different issue, or several.
- **A declared set.** `--for` repeats, and then the PR closes exactly that set:
  one `Fixes #n` per declared issue, `--body-file` must close the same set (a
  missing, extra, repeated or different ref is refused), and `--json` reports
  the scalar `fixes` (the first issue, kept for compatibility) plus the full
  `fixedIssues` array. Inference still resolves at most one issue — several are
  always declared, which is what keeps the accidental-closure protection. A set
  gets no What/Why defaults, because one issue's Fix does not describe the
  others. Issues with no shared parent, area label or dependency draw a warning
  rather than a refusal: whether a combined PR stays reviewable is a judgement
  hew cannot make. The trade-off is that issue-to-PR links stop being 1:1 — a PR
  closes exactly the declared set — so one issue per branch stays the default
  workflow, and this only makes the deliberate combined case first-class instead
  of a `gh pr edit` bypass the guard never sees (#134).
- **Body template.** `### What / ### Why / ### Testing / ### Risk`, mapping
  onto the issue template and living beside it in `internal/conventions`. What and Why
  default to the issue's own `Fix`/`Approach` and `Problem`/`Goal` sections — the
  issue already says this — so in the common case only `--testing` and `--risk`
  are worth typing. `--what`, `--why`, `--testing` and `--risk` repeat — several values
  compose a bullet list, one value stays a plain paragraph so existing callers'
  bodies are unchanged. Testing is evidence on `Before:` and `After:` lines (the failing
  test or output, then the same thing passing) plus a short list of checks run, not
  "tests pass". Risk is a `Reversibility:` line (one-way or two-way door) and a
  `Blast radius:` line; each claim gets its own line so GitHub renders a list, not a
  paragraph (#180). Risk is prompted on
  `--ready` and when amending a PR already open for review, not on a draft. Empty
  sections are omitted, never left as bare headers. The contract is stated in
  `pr --help` and in one clause of the primer's `pr` entry, which is the token
  budget's short form; the tutorial stays in help and in the composition warnings,
  which also cover `--body-file`.
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
2. **Live state** — ready work, in-progress issues and their assignee, and
   epics with progress, each capped at the top five by priority with a pointer
   at the command that lists the rest (`hew ready`, `hew list`, `hew epic
   status`) — so the live half's size is bounded by section count, not repo
   size. Plus open-blocker counts via warnings, and the untriaged rollup.
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
No URLs, no timestamps, no prose. Target: whole primer under ~1200 tokens for a
typical repo — measured 1014–1025 on the two fixtures below, ~860 of it the
static conventions half (see the token-efficiency section).

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
- Dates in text output are the reader's local day, with the zone named where it
  first appears (`created: 2026-10-29 EDT` in `show`; its comment dates share
  it). A UTC date reads a day ahead for a late-evening user in a western zone.
  `--json` keeps UTC timestamps, and the bodies `migrate` writes to GitHub keep
  a UTC date, since they are a durable record read in any zone.
- `--json` everywhere, with a flat schema (deps as number arrays, not
  `{nodes:[...]}` wrappers — hide GraphQL shapes from consumers). List-shaped
  output is NDJSON, one compact object per line: a truncated JSON array is
  unparseable garbage, a truncated NDJSON stream is just shorter (same feedback).
  The primer states both formats so agents reach for the cheap one.
  Every exit-0 path honours `--json`, including the idempotent ones an agent
  hits on a retry. A write whose requested state already held (`reopen` on an
  open issue, `block` on an existing edge, `unblock` on an absent one) emits the
  same full issue document a changed write does, plus `"unchanged": true`; the
  field is absent, not false, on a write that changed something. `apply` and
  `migrate beads` answer an empty plan in their normal result shape, and
  `--dry-run` emits `{dryRun: true, wouldCreate | wouldMigrate: [...],
  alreadyCreated | alreadyMigrated: {key: number}}` — an empty plan under
  `--dry-run` keeps that shape. Plan entries carry every key on every entry,
  with `null` for an absent parent or type, so one schema parses them all.
- Errors are one line, actionable, exit codes meaningful (`ready` with no results
  exits 0 with `no ready work`; `start` on an issue claimed by someone else exits 3
  with `already claimed`; `start` on an issue you already claimed exits 5, because
  the response is to resume that work rather than to pick a different item; auth
  failure exits 4; `start` on an issue with open blockers exits 6; etc.). The exit code is the signal agents branch on — a
  distinction worth acting on gets a code, not a `--json`-only field. Exits 3, 5 and 6
  belong to `start` alone: an unknown command, an unknown or unparsable flag, and a missing
  required flag all exit 2 with one line and a pointer to `--help`, never the framework's
  defaults (an unknown command exits 3 and a flag error exits 1 with the full help dump).

## Checkpoint binding

The batch writers (`apply`, `migrate beads`, `migrate local`) checkpoint a source-key → issue-number
mapping so an interrupted run resumes instead of duplicating. That file is ordinary
repository content: it sits next to the plan or the beads snapshot, and anything an
agent checks out can contain one. Treating it as an authoritative mapping meant a
supplied state file could point an entry at any issue number and the tool would wire
edges onto it, or comment on and close it (#81).

So a checkpoint is trusted local scratch, never a portable input, and three things
have to agree before a mapping is used:

- **Repository.** The file records `owner/name`; a mismatch is refused. Compared
  case-insensitively, since GitHub treats it that way and a differently-cased
  `--repo` should not invalidate a real checkpoint. On the local backend it
  records `local/<id>` instead, where the id is the tracker's own identity from
  `.hew/config.json`: the checkout's directory name is neither unique (two
  checkouts can share it) nor stable (a rename would invalidate its own state).
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
The `migrate` package's engine owns that pass for the pipeline it runs, so a
library caller gets the same three bindings; `migrate.Options.BeforeCreate` is
where the CLI bootstraps labels, ordered after verification so a refused
checkpoint costs no label write either.
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
  - `tracker/`, `pr/`, `migrate/` — the public in-process workflow API: reads,
    issue lifecycle, pull requests and migrations over an explicit worktree,
    backend and actor (see [Tracker library](#tracker-library)). `internal/cli`
    adapts flags, files, rendering and exit codes over these packages; the
    dependency runs one way, and no public package imports the CLI or renderers.
  - `cmd/hew/` — main, urfave/cli command wiring
  - `internal/gh/` — thin API layer, split in two so commands are testable against
    a fake and a backend need not be GitHub: `IssueStore` is everything every
    command but `pr` needs; `PullRequests` is `pr`'s alone; `Client` is their union,
    which the go-gh implementation satisfies. The CLI's `App` holds the halves
    separately, and `App.PRs` is nil on a backend with no pull requests (the local
    store implements `IssueStore` only), where `pr` refuses rather than guesses.
    Optional capabilities (`Claimer` and `ClaimReleaser`, atomic claim and
    release) are separate interfaces the store may also implement.
  - `internal/model/` — Issue/Epic domain types, ready/normalization/cycle logic
    (pure, unit-tested)
  - `internal/render/` — text + JSON renderers (golden-file tests)
  - `internal/conventions/` — labels, body and PR templates, primer text (the opinions
    live here)
  - `internal/git/` — local branch state for `pr` (current branch, its upstream
    name, has-upstream)
  - `internal/local/` — the JSONL-backed `IssueStore` for repos that track work
    locally: one `issues.jsonl` in a self-ignoring `.hew/` at the main checkout.
    The directory is untracked and ignored, so `git clean -fdx` at the main
    checkout deletes the only copy of the tracker.
  - **Backend selection** (`buildApp`): `--repo` always means GitHub. Otherwise
    `local.Locate` asks the main checkout — so every linked worktree agrees — for
    `.hew/config.json`; a config saying `local` opens the local store, and the
    primer, `init` snippet and `pr` refusal follow it. No `.hew`, a `.hew` that is
    not a directory, a config naming `github` (a migrated repo keeps its issues as
    an archive) or a directory outside any git checkout all mean GitHub, as
    before. A `.hew` that carries evidence of a local tracker but not the config
    that says so is refused instead: a `backend` this build does not know, and an
    `issues.jsonl` or `lock` with no `config.json`, are errors naming the file and
    the recovery (`hew init --local` rewrites a missing config and keeps the
    issues). A `config.json` that is empty or will not decode is refused the same
    way, naming the file and the recovery rather than the decoder's text; on a
    migrated repo (`migrated.json` present) the advice is to restore the config,
    because `init --local` would make the archive a live local tracker again. A
    config beside no `issues.jsonl` is a tracker that lost its issues: `Open`
    refuses it with the path and the recovery, and `hew init --local` recreates
    the file empty with a new tracker id (so `apply` and `migrate` checkpoints
    bound to the old one start over) and warns on stderr that it did. Selection asks the filesystem first — a main checkout or linked
    worktree whose `.hew` is absent cannot be local — so a repo that never opted
    in pays no `git worktree list`; git answers whenever a `.hew` exists and for
    layouts the shortcut cannot read. Anything else — git failing for another
    reason, or a config that exists but cannot be read — is an error, never a
    silent fall back to GitHub, since that would send writes to the wrong tracker.
- **Testing**: unit tests against a fake API layer; golden files for renderer output;
  the external-consumer module in `consumer/`, which imports only the public
  packages and runs their workflows against a real local tracker and an isolated
  GitHub transport. An integration smoke test against a real scratch repo (behind a
  build tag, run manually — it needs a token and mutates state, so it stays out of
  CI) is deferred for now.

## Local backend

GitHub is the default and the source of truth. A repo can instead track work
locally — no GitHub account, remote or network — for a project that has not
earned a tracker yet, or an agent working somewhere GitHub is out of reach. It is
a second `gh.IssueStore`, not a cache: the commands, conventions, readiness rules
and renderers are the same, and nothing is ever synced back and forth. Moving to
GitHub later is a one-way migration.

- **Opting in.** `hew init --local` writes `.hew/config.json` naming the `local`
  backend and creates the store. It is idempotent and keeps existing issues, and
  refuses a repo whose config names another backend. A bare `hew init` in a local
  repo is the same command: it reports the tracker and the local CLAUDE.md
  snippet rather than bootstrapping GitHub labels. `init --json` is one shape on
  both backends (`backend`, `path`, `createdLabels`). `hew config` is the
  read-only way to ask which backend is active, without bootstrapping anything.
  Backend selection is in Architecture.
- **Where it lives.** One `issues.jsonl` and a `config.json` in `.hew/` at the
  *main* checkout, so every linked worktree of a repository shares one tracker.
  The config carries a random `id` generated when the tracker is created; batch
  checkpoints bind to it, so two checkouts with the same directory name cannot
  resume each other's runs and a rename keeps its own. The directory carries
  its own `.gitignore`, so the repo's is untouched and git never shows it —
  which also means it is untracked: `git clean -fdx` at the main checkout
  deletes the only copy.
- **A wholly absent `.hew` falls back to GitHub — a recorded limitation, not an
  oversight.** A repo that never opted in and a local repo whose `.hew` was
  deleted (`git clean -fdx`, `rm -rf`) are indistinguishable from the checkout
  alone, so a deleted tracker makes write commands target the remote's real
  issues. A durable opt-in marker outside `.hew` (a `git config` key, a file under
  `.git`) was considered and not added: it would be a second source of truth for
  backend selection that must agree with `config.json` in every worktree, it would
  not survive a fresh clone or re-clone — the very case where a repo should start
  GitHub-only — and the loss it guards against already destroyed the tracker's
  contents, which no marker restores. The limitation is stated where a local user
  will meet it: `hew init --local` prints it, and the README says so next to the
  `git clean -fdx` caveat. Revisit if a marker earns its keep, e.g. by also
  guarding `hew migrate local`.
- **File format.** One JSON object per line; a record's `_type` picks its shape
  (`meta`, `label`, and an untyped issue record). Relationships are stored once, on
  the issue that owns them (its parent, its blockers); sub-issues, ref states and
  rollups are derived on load, so the file cannot disagree with itself. Labels
  are stored, so the convention set is the same one GitHub carries. A `config.json`
  or file written with a newer format version is refused rather than half-read; fields
  an older build does not know, in either file, are read past and written back.
- **Locking.** Every mutation takes an exclusive `flock` on `.hew/lock`, re-reads
  the file, applies the change and writes a temp file renamed into place, so
  concurrent agents in different worktrees never lose each other's writes and a
  change that fails validation leaves the file as it was. Reads take no lock; the
  rename means they see the old whole or the new whole.
- **Claims.** The store implements the atomic claim, so `start` has exactly one
  winner. Identity is per worktree (see open questions), which is what keeps
  exit 3 and exit 5 apart under one OS account; `HEW_USER` overrides it.
- **What is absent.** There are no pull requests, so the backend does not
  satisfy the PR workflow and `pr` refuses with the contract's
  `NoPullRequestsError`, and the local primer finishes work with
  `hew close <n> --completed` instead of a `Fixes #n` merge. `--repo` always
  means GitHub.
- **Migration.** `hew migrate local` is the only way out, and its rules are the
  `migrate local` entry under Command surface: it freezes the tracker, creates
  every issue on GitHub, and only then seals it — `.hew/migrated.json` records the
  local → GitHub number map, `config.json` flips to `github`, and
  `issues.jsonl` stays as a read-only archive that refuses writes, including from
  a store opened before the flip. A failed run leaves the tracker local to be
  rerun. The target is the `--repo` or git-remote repository.

## Tracker library

A Go program can use hew in-process instead of shelling out to the binary. The
public packages are `github.com/lumberbarons/hew/tracker`, `.../pr` and
`.../migrate`; every operation takes the worktree, backend, actor and repository
as arguments — never the process's working directory or environment — returns
typed results and typed errors, and neither prints to stdout nor calls
`os.Exit`. The first consumer is birl, an orchestrator that drives many workers
against one tracker. `internal/cli` is an adapter over these packages: flags,
input files, the editor, hooks, rendering and exit codes, with no workflow
decisions of its own.

This section is the contract. It maps every command to the public operation that
performs its workflow, or to the integration concern that justifies keeping it
in the adapter; it names the request, result, warning and error types an external
module can refer to; and it states the concurrency, identity, backend and
authentication guarantees those operations carry. The extraction landed in
#309–#317, and every operation below ships: `consumer/` is a separate Go module
that imports only `tracker`, `pr` and `migrate` and runs these workflows in CI,
so a command that still owned a workflow, or an operation that needed an
internal type, would fail there rather than pass its own unit tests. Changing
this contract changes the compatibility policy (#171) as much as it changes
code.

### Package layout

| Package | Holds | Imports within the module |
|---|---|---|
| `tracker` | Issue workflows: detection and opening, reads and queries, primer data, creation, editing and reopening, claims, closure, dependency writes, apply plans and checkpoints, initialization. | `internal/model`, `internal/gh`, `internal/local`, `internal/conventions`; never `internal/cli`, `internal/render`, `cmd/hew`, `pr` or `migrate` |
| `pr` | Pull-request workflows: issue inference, body composition, creation and amendment. | `tracker`, `internal/git`, `internal/gh`, `internal/conventions`; never `internal/cli`, `internal/render` or `cmd/hew` |
| `migrate` | The source-neutral migration engine and the beads and local-to-GitHub workflows, including source mapping, checkpointed resume and the local tracker's seal. | `tracker`, `internal/beads`, `internal/local`, `internal/gh`, `internal/model`, `internal/conventions`; never `internal/cli`, `internal/render` or `cmd/hew` |

The table is the destination every extraction issue shared, and every row now
holds: no workflow lives in `internal/cli` any more, which the consumer module
in `consumer/` demonstrates by importing only the three public packages. The
row for each command in the operation inventory below names the operation it
calls and what the adapter keeps.

The dependency rule is one-way: `internal/cli` may import every public package,
and no public package may import the CLI, the renderers or the command wiring.
Except for the module-internal seams below, every public signature names public
types: `tracker.Issue`, never `internal/model.Issue`; `http.RoundTripper`, never
a go-gh option struct. A public package may use `internal/conventions`,
`internal/gh` and `internal/local` inside its implementation — an external module
cannot, and does not have to.

Convention enforcement belongs to the public operation, not the adapter: exactly
one priority and one type label, the template body, title and area rules, the
epic marker, native dependencies and the mandatory client-side cycle check are
applied by `tracker`, `pr` or `migrate`; the adapter never re-derives them from
flags. A consumer that calls a write operation gets the same enforcement the
command does.

`tracker.Wrap`, `tracker.WrapAs` and `tracker.WrapWriter` are the module-internal
seam `internal/cli` uses to delegate over a store it already opened. Their store
parameter is an internal interface, so an external module cannot call them;
`migrate.Wrap` is the same seam for the migration workflows, taking the `Target`
the command already opened and a `Config` naming the checkpoint path and repo
identity it resolved; `pr.Wrap` is the same seam for the
pull-request workflow, taking the store, the `PullRequests` half and the branch
state the command already has. `tracker.Open`, `tracker.Detect`,
`tracker.Init`, `pr.Open` and `migrate.Open` are the only constructors in the
external contract. The exported-surface check records the seams like every other
exported symbol, but they are not consumer entry points.

### Operation inventory

Every command path in the CLI surface appears here, including the `epic list`
alias; the group commands (`epic`, `migrate`, `hooks`) have no workflow of their
own, and the global flags belong to the adapter. "Adapter keeps" names what stays
in `internal/cli`; where it is empty the command only renders values the library
returned. Error types are in [Errors and exit codes](#errors-and-exit-codes);
warnings in [Warnings and findings](#warnings-and-findings).

| Command | Public operation | Adapter keeps |
|---|---|---|
| `prime` | `(*tracker.Tracker).Primer(ctx) (tracker.PrimerResult, error)` | section caps, text layout, `--hook-format cursor` JSON wrapper |
| `ready` | `(*tracker.Tracker).Snapshot(ctx, tracker.SnapshotOptions{}).Ready() []tracker.Issue` | `--limit` truncation, text and NDJSON rendering |
| `list` | `(*tracker.Tracker).Snapshot(ctx, tracker.SnapshotOptions{States: …})` then `(*tracker.Snapshot).List(tracker.ListFilter) []tracker.Issue` | `--state`/`--epic` default mapping, `--closed` deprecation, `--bodies` rendering |
| `show <n>` | `(*tracker.Tracker).Detail(ctx, n) (tracker.IssueDetail, error)` | text annotations and `--json` rendering |
| `search <terms>` | `(*tracker.Tracker).Search(ctx, tracker.SearchRequest) (tracker.SearchResult, error)` | `--state` mapping onto `SearchRequest.States`, `--limit` truncation, no-triaged-match and truncation wording |
| `create` | `(*tracker.Tracker).Create(ctx, tracker.CreateRequest) (tracker.CreateResult, error)`; `(*tracker.Tracker).CheckCreate(ctx, tracker.CreateRequest) error` is the same validation and reference reads with no write, so `--edit` refuses a bad flag or reference before the editor opens | title/body/area flags, `--body-file`, `--edit` |
| `draft` | `tracker.ComposeBody(tracker.Body) (tracker.ComposedBody, error)`, `tracker.CheckBody(string) (tracker.BodyCheck, error)` | `--out`/`--check` file I/O and rendering |
| `start <n>` | `(*tracker.Tracker).Start(ctx, n, tracker.StartOptions) (tracker.StartResult, error)` | `--show` rendering, wording and exit 3/5/6 for each `tracker.StartOutcome` |
| `release <n>` | `(*tracker.Tracker).Release(ctx, n, tracker.ReleaseOptions) error` | rendering and exit mapping |
| `triage` | `(*tracker.Tracker).Triage(ctx, tracker.TriageRequest) (tracker.TriageResult, error)` | `--search` flag and rendering; this is the deliberate untriaged-list API |
| `set <n>` | `(*tracker.Tracker).Edit(ctx, tracker.EditRequest) (tracker.EditResult, error)` | `--body-file` input, `--outcome` and `--closed` flag mapping, rendering |
| `pr` | `pr.Open(ctx, pr.Config)` then `(*pr.Client).Create(ctx, pr.CreateRequest) (pr.CreateResult, error)` or `(*pr.Client).Amend(ctx, pr.AmendRequest) (pr.AmendResult, error)` | section flags to `pr.Sections`, `--body-file`, `--title`/`--base`/`--ready` mapping, rendering |
| `close <n>` | `(*tracker.Tracker).Close(ctx, n, tracker.CloseOptions) (tracker.CloseResult, error)` | wording and exit mapping |
| `reopen <n>` | `(*tracker.Tracker).Reopen(ctx, n, tracker.ReopenOptions) (tracker.ReopenResult, error)` | wording and exit mapping |
| `block <n> --on <m>` | `(*tracker.Tracker).Block(ctx, n, blockers, tracker.BlockOptions) (tracker.BlockResult, error)` | wording and exit mapping |
| `unblock <n> --from <m>` | `(*tracker.Tracker).Unblock(ctx, n, blockers, tracker.BlockOptions) (tracker.UnblockResult, error)` | wording and exit mapping |
| `epic create` | `(*tracker.Tracker).CreateEpic(ctx, tracker.CreateEpicRequest) (tracker.CreateResult, error)`; `(*tracker.Tracker).CheckCreateEpic(ctx, tracker.CreateEpicRequest) error` is its no-write pre-flight, called before `--edit` opens the editor | body input and rendering |
| `epic status [<n>]` (alias `epic list`) | `(*tracker.Snapshot).Epics() []tracker.Issue` (over the states the snapshot fetched) or `(*tracker.Tracker).Detail(ctx, n)` | `--state` mapping onto `SnapshotOptions.States`, rollup layout, `next:` line and warning wording |
| `apply <plan.jsonl>` | `(*tracker.Tracker).ApplyPlan(ctx, []tracker.PlanEntry, tracker.ApplyOptions) (tracker.ApplyResult, error)` | JSONL codec (`internal/plan`), default checkpoint path, dry-run rendering |
| `config` | `tracker.Detect(dir) (tracker.Target, error)` | `--repo` selection, text and JSON rendering |
| `init` | `tracker.Init(ctx, tracker.InitConfig) (tracker.InitResult, error)` | `--local` flag mapping, CLAUDE.md snippet, rendering |
| `hooks install <agent>` | — | Edits the selected agent's project configuration (`.claude/settings.json`, `.codex/hooks.json`, the opencode plugin) and refuses symlinks. It configures an editor session, not a tracker workflow. |
| `hooks remove <agent>` | — | The inverse of the above, and adapter-only for the same reason. |
| `migrate beads [<file>]` | `migrate.Open(ctx, migrate.Config)` then `(*migrate.Migrator).Beads(ctx, migrate.BeadsRequest) (migrate.Result, error)` | source-file discovery and default checkpoint path, `--include-closed` mapping, dry-run rendering |
| `migrate local` | `migrate.Open(ctx, migrate.Config)` then `(*migrate.Migrator).Local(ctx, migrate.LocalRequest) (migrate.Result, error)` | worktree discovery, default checkpoint path, dry-run summary |

Two supporting capabilities have no command of their own but are part of the
contract because a workflow above needs them: `migrate.Target`'s `Comment` and
`Close` (a migration posts the source's comments and reproduces its close
reason) and `CreateRequest.Assignees` (tracker creation preserves source claims).
The migration workflows
write through a narrow `migrate.Target` interface — create, edit, comment,
close, dependency edges, read — which the CLI adapts over the store it already
opened and a test can fake, so the engine names no internal store; an optional
`migrate.Labeler` half bootstraps the labels a create references when the
target's backend has labels to create. `migrate.Open` supplies the same target
over a GitHub or local store it resolved itself, with `Config.Transport` for an
isolated test. `apply`'s JSONL file format
stays an adapter input deliberately: a library caller builds
`[]tracker.PlanEntry` and is not tied to a file schema. `pr`'s and `migrate`'s
GitHub operations go through the same transport seam as `tracker`'s, so a
consumer's tests need no network and no `gh` process.

The untriaged boundary is part of the contract, not a rendering preference. Every
list-shaped query — `Snapshot.Ready`, `Snapshot.List`, `Snapshot.Epics`,
`Search`, `Primer` — omits untriaged titles and bodies. `Get` and `Detail` on an
explicitly named issue, the children of an explicitly named epic, and the
deliberate `Triage` API are the only operations that return them. `Snapshot.Issues`
is the raw fetched set behind those queries; a caller that surfaces it directly
takes on the boundary, which is why the commands use the query methods.
`Snapshot.Untriaged` reports how many open issues the fetch holds untriaged — the
count the primer rolls up — without returning any of their text.

Body presence follows the list/document split rather than the fetch. The
list-shaped reads — `Snapshot.Issues`, `Ready`, `List`, `Blocked`, `Claimed`,
`Epics` and `Children`, plus `Search`, `Triage` and `Primer`'s sections — return
rows without bodies or comments, which is what a list view renders and what keeps
its payload from scaling with total body length. A caller that wants the text
asks: `SnapshotOptions.Bodies`, `SearchRequest.Bodies` or `TriageRequest.Bodies`.
The single-issue reads carry it unconditionally — `Get`, `Detail`, and the write
results (`Create`, `Start`, `Edit`) — and `Detail`'s children stay rows.

### Types an external module names

The values below are the contract, not an implementation sketch: a consumer
imports `tracker`, `pr` or `migrate` and never an `internal` path.

| Package | Requests and inputs | Results and values |
|---|---|---|
| `tracker` | `Config`, `SnapshotOptions`, `ListFilter`, `SearchRequest`, `TriageRequest`, `Body`, `Sections`, `CreateRequest`, `CreateEpicRequest`, `EditRequest`, `ReopenOptions`, `StartOptions`, `ReleaseOptions`, `CloseOptions`, `BlockOptions`, `InitConfig`, `PlanEntry`, `PlanRef`, `Checkpoint`, `ApplyOptions` | `Target`, `Issue`, `Ref`, `Comment`, `TextFinding`, `Snapshot`, `IssueDetail`, `SearchResult`, `TriageResult`, `PrimerResult`, `ComposedBody`, `BodyCheck`, `CreateResult`, `EditResult`, `ReopenResult`, `StartResult`, `StartOutcome`, `CloseResult`, `BlockResult`, `UnblockResult`, `InitResult`, `ApplyResult`, `PlannedEntry`, `AppliedEntry`, `CheckpointState`, `CheckpointEntry`, `MemoryCheckpoint`, `FileCheckpoint`, `RateLimit`, `Edge`, `Backend`, `State`, `EdgeKind`, `ReopenStage` |
| `pr` | `Config`, `CreateRequest`, `AmendRequest`, `Sections`, `Body`, `BodyEdit` | `Client`, `CreateResult`, `AmendResult`, `ComposedBody`, `BodyCheck` |
| `migrate` | `Config`, `BeadsRequest`, `LocalRequest`, `RunRequest`, `Item`, `Options`, `Target`, `Labeler` | `Migrator`, `Result`, `PlanStep`, `PlannedItem`, `Label` |

`IssueDetail` carries the issue, an epic's children, the next workable child
(`Next`), source-attributed `TextFinding` values and snapshot warnings, so
`show` and `epic status` assemble them the same way. `SearchResult` and
`TriageResult` carry the matches plus `Total` and `Fetched`; `TriageResult`
also carries the advisory `TextFinding` values for the untriaged text it
returns. `PrimerResult` carries the live sections and counts, the warnings and
the backend's static convention text, so the CLI renders a primer without
owning any of its policy. `Target` is the read-only backend report — `Backend`,
`Repo`, `Dir`, `MainCheckout`, and the local tracker's stable `ID` — and
`RateLimit` is the GitHub client's last observed budget.

`InitResult` reports the backend initialization ran against, the local tracker's
directory, the convention labels the run created and the notices it would have
printed; the notices are data because the library never writes to stdout or
stderr. Initializing a repo whose tracker belongs to another backend is refused
with `InitializedElsewhereError` rather than creating a second tracker no command
would read, so a migrated repo's archive is never made live again.

`pr.Config` takes the same worktree, repository, backend pin, actor, notice and
transport inputs as `tracker.Config`; `pr.Open` opens the tracker it reads and
the client it writes pull requests through, so a caller does not wire two
handles together. `migrate.Config` adds the target `tracker.Config` to the
checkpoint, throttle, dry-run mode and notice writer the run uses. `pr` also
exposes `ComposeBody`/`CheckBody` with `ComposedBody` and `BodyCheck`, the PR
half of the composition API `draft` and `create` use for issues.

Composition separates a refusal from advice. A rule that rejects the text —
mutually exclusive wording, an empty checklist item, a body source combined with
another, a closing set that does not match the declared issues — is
`*tracker.InvalidInputError`, with the field named in library terms. A markup
finding is a `Finding` on the result and never fails the call, so
`draft --check` reports unmarked code without refusing the body.

`tracker.Issue` is shaped like the flat `--json` schema — dependencies as
number lists, GraphQL shapes hidden — not like `internal/model.Issue`. The
library's shape is therefore the schema consumers can already read, and
`internal/model` stays free to change. A value rebuilt by unmarshaling that
schema carries the exported fields only; the accessors that expose reference
states, raw labels, the parent's title and the close reason come from a library
read.

Every other exported result and warning type carries the same schema: camelCase
keys, and an empty slice or map rather than null, so a caller can serve a result
as its API's response without a mapping layer of its own. `consumer/` pins the
shape of each one, and the daemon returns them directly.

### Warnings and findings

A warning is a typed value on the result, never a line printed by a library call
and never an error. A finding is advisory input about text — written by a
caller, or read from an issue — and never blocks a write or rewrites the
author's words. Warnings carry a `Kind`; advisory `TextFinding` values carry
the independent flags the scan tests, so a consumer can filter on the
dimension it cares about.

| Package | Type | Kinds |
|---|---|---|
| `tracker` | `Warning` + `WarningKind` | `WarnMultiPriority`, `WarnMultiType`, `WarnInProgressEpic` (primer contradictions), `WarnDependencyCycle`, `WarnBlockersCapped`, `WarnSubIssuesCapped` |
| `tracker` | `TextFinding` | Source-attributed advisory character findings, flagged independently: zero-width, bidi controls, Unicode tags, confusable characters |
| `tracker` | `Finding` + `FindingKind` | `FindingUnmarkedCode` (prose that looks like code and should be in a backtick span) |
| `pr` | `Warning` + `WarningKind` | `WarnBranchPrefix`, `WarnUnclaimed`, `WarnUnrelatedSet`, `WarnNoSectionDefaults`, `WarnPrimaryTitle`, `WarnMissingTesting`, `WarnMissingRisk` |
| `migrate` | `Warning` + `WarningKind` | `WarnEdgeDropped`, `WarnWriteFailed`, `WarnBodyEdited`, `WarnAlreadyClosed`, `WarnLabelRenamed`, `WarnUnknownStatus` |

Truncation a caller can act on is a count, not prose: `SearchResult` and
`TriageResult` carry `Total` and `Fetched`, and `Snapshot.Warnings()` names a
capped connection. The adapter decides the sentence; the library keeps the fact.
`pr`'s Risk warning is conditional exactly as the command is: required once the
PR is ready for review, permitted to be absent on a draft.

### Errors and exit codes

The library returns typed errors a caller matches with `errors.As`/`errors.Is`;
it never returns an exit code, and no library code calls `os.Exit`. The CLI maps
each type — and each `StartOutcome` — to the code it has always used, and that
mapping lives in `internal/cli` alone:

| Error | CLI exit | Meaning |
|---|---|---|
| claimed by someone else (carries the claimant) | 3 | pick the next ready item |
| claimed by the acting identity (carries the actor) | 5 | resume that work |
| open blockers (carries the blockers) | 6 | pick the next ready item |
| discovery intent required (`DiscoveryIntentError`) | 2 | use `StartOptions.ForDiscovery` in the library or `--for-discovery` in the CLI |
| authentication failure | 4 | `gh auth login` |
| invalid input: a malformed argument, or a plan entry whose own fields are invalid | 2 | fix the call |
| not found, a closed target, a dependency cycle (a self-block included), a plan that fails to parse or resolve, anything unclassified | 1 | report it |

The claimant and actor ride in the error so the CLI's message and a consumer's
own handling read the same field instead of parsing a string. A new typed error
that the CLI cannot map is a bug in the mapping, not a reason to widen the table.

The three `start` distinctions are one public decision, not three private
readings of the typed errors: `tracker.StartOutcomeOf(err)` returns a
`tracker.StartOutcome` — `StartClaimedByOther` (pick the next ready item),
`StartClaimedByYou` (resume that work) or `StartBlocked` (pick the next ready
item) — and reports whether the error is one of them. The facts stay on
`ClaimedError` and `BlockedError`; the outcome names the decision they ask for.
The CLI maps the outcome to exits 3, 5 and 6, and the daemon maps it to its
`claimed_by_other`, `claimed_by_you` and `blocked` kinds, so a consumer does not
re-derive it from `ByActor`.
The separate `DiscoveryIntentError` maps to HTTP 409 with kind
`discovery_intent` in the daemon.

`tracker` owns the vocabulary every package shares: `AuthError`,
`InvalidInputError`, `NotFoundError`, `ClosedError`, `ClaimedError`,
`BlockedError`, `CycleError`, `UnverifiableCycleError`, `NotOpenError`,
`EpicError`, `UntriagedError`, `DiscoveryIntentError`, `NotClaimedError`, `NotOwnerError`,
`OpenChildrenError`, `PartialWriteError`, `PartialReleaseError`,
`PartialCloseError`, `LockTimeoutError`, `MigratedError`,
`InitializedElsewhereError`. A workflow in another
package that adds a refusal names its own type and wraps the tracker cause
rather than inventing a second auth or not-found error:

| Package | Errors |
|---|---|
| `tracker` | `PartialCreateError`, `PartialEditError`, `PartialReopenError` — the created issue or the applied field names ride in the error |
| `pr` | `NoPullRequestsError`, `NoUpstreamError`, `IssueInferenceError`, `ExistingPullRequestError`, `NoOpenPullRequestError`, `AmbiguousPullRequestError` |
| `migrate` | `CheckpointError`, `SourceChangedError`, `SealError`, `IncompleteError`, `NotLocalError`, `DuplicateKeyError`, `ItemError` |

Cancellation keeps its identity through every wrapper: `errors.Is(err,
context.Canceled)` and `errors.Is(err, context.DeadlineExceeded)` hold even when
the cause is inside a partial-operation error. A refusal the CLI reports as a
usage mistake is `InvalidInputError` with the field named in library terms, not
in flag terms; the adapter supplies the flag spelling. Exit 4 is auth from any
package, 2 is invalid input, 3/5/6 are `start`'s distinctions, and every other
workflow refusal is 1.

### Concurrency and cancellation

Every operation that reads or writes a backend takes a `context.Context` and
honors it through to the backend and to a local lock wait. No operation starts a
goroutine, keeps a background loop, or caches across calls beyond the open
client; a returned `Snapshot`, `Issue`, result or warning value is immutable, and
independent goroutines may read one at the same time. A `*tracker.Tracker` and a
`*pr.Client` are safe for concurrent use; a `*migrate.Migrator` owns a
checkpoint and a run, so callers run one migration at a time per migrator.

- **Local backend.** Every mutation takes the exclusive `flock` on `.hew/lock`,
  re-reads, applies and atomically renames, so concurrent writers in different
  worktrees cannot lose each other's writes. Reads take no lock and see the old
  whole file or the new one. The lock wait honors the caller's context and is
  bounded (15 seconds by default); expiry is `LockTimeoutError`. The local store
  implements the claim and release atomically, so two `Start` calls have exactly
  one winner and a `Release` cannot clear a concurrent forced claim from a stale
  read. A long-running `migrate local` holds the lock for the run and writes the
  advisory `.hew/migrating` marker; a writer that waits reports it and then fails
  with `LockTimeoutError` rather than proceeding.
- **GitHub backend.** One `Open` builds one client shared by every operation on
  it. The client is safe for concurrent use: the node-ID cache is synchronized,
  `Viewer` is cached, an expired token is refreshed or rebuilt on an auth error,
  and a 403/429 secondary limit is retried with `Retry-After` backoff up to the
  caller's deadline; the observed budget is available as `tracker.RateLimit`.
  `Config.Transport` replaces the HTTP transport the client sends through, so a
  consumer's tests run `Open` and `Init` with neither the network nor a `gh`
  process.
  GitHub offers no transaction across calls: a claim is check-then-act, `Start`
  re-reads the issue afterwards, and two workers racing the same issue can both
  observe success; with a shared login the tracker cannot even tell them apart.
  `StartResult.Claimant` is always the acting identity, never a lookup of who
  won; the re-read `StartResult.Issue` carries the resulting assignees, which a
  caller compares with `Claimant` to notice a racing claim, and `ClaimedError`
  carries the existing claimant when a claim is refused up front. The library
  does not pretend to prevent the race.
- **Multi-write operations.** Create (issue, then edges), edit (a sequence of
  scalar and label writes), close and reopen (comment then state, plus release),
  block/unblock (validated, then edge by edge), apply and migrations (every
  write checkpointed) can stop partway. Each reports what landed: the
  `Partial*Error` types, `ApplyResult.Unwired`, or `migrate.Result`'s counts and
  warnings. Cancellation is not special-cased — the partial state is the same,
  and a checkpointed run resumes from it. A checkpoint record that cannot be
  saved after its issue was created is reported rather than hidden; a rerun then
  duplicates that one issue, which is the failure the digest and provenance
  checks are designed to make visible rather than silent.
- **Reads.** A `Snapshot` is the result of one paginated fetch, not a
  transaction: issues created or edited while pages are being read may or may not
  appear, and the result never claims a point-in-time view it did not take. Its
  query methods are pure and call the backend no further. `Get` and `Detail` are
  live reads.

### Identity, backend and authentication

- **Local identity is explicit.** The CLI resolves a local claimant from
  `HEW_USER`, else the OS login qualified with the linked worktree's name and a
  short digest of its path (the main checkout keeps the bare login), and errors
  rather than guessing when git cannot say which checkout this is. The library
  reads neither cwd nor environment after `Open`: `Config.Actor` and `As(actor)`
  are the in-process spelling of the same identity. It is per worktree, not per
  session: two sessions sharing a worktree, or both in the main checkout, are one
  claimant, and the second `Start` gets "claimed by you" rather than a conflict.
  `HEW_USER` must be unique per agent wherever sessions share a checkout.
  `Identity()` reports the effective claimant.
- **GitHub identity is the login, and `As` is a documented no-op there.** Every
  claim on GitHub is the authenticated login; `As` returns a tracker whose claims
  are still made as that login, and `Identity()` reports it, so a caller can see
  that the override took no effect. It follows that "claimed by you" cannot tell
  workers apart: every worker sharing the login gets the same answer, and two
  sessions racing a claim are indistinguishable to the tracker. A consumer such
  as birl keeps its own record of which worker holds which issue and treats
  `StartResult.Claimant` as the identity the claim was made as, not as that
  record or as proof this worker won.
- **The backend is pinned at startup.** `Detect(dir)` reports the backend and
  where it lives — `Backend`, `Repo`, `Dir`, `MainCheckout`, and the local
  tracker's stable `ID` — from the filesystem and git, reading no store. `Open`
  detects once and fixes the backend for the life of the tracker; setting
  `Config.Backend` pins it without consulting the other backend. `BackendLocal`
  refuses to fall back to GitHub when the tracker is gone, and `BackendGitHub`
  opens GitHub even where a local tracker exists (the CLI's `--repo`). A
  long-lived process detects once, pins what it expects, and treats a `Detect`
  that disagrees as an error rather than following it: a local tracker deleted
  underneath it must not silently send writes to the remote's real issues.
- **Authentication is the `gh` CLI's, not hew's.** The GitHub backend reuses the
  host's `gh auth login` credentials (including `GH_TOKEN`/`GITHUB_TOKEN`, which
  go-gh honors) and has no auth flow, token parameter or credential store of its
  own; the CLI's `--repo` selects a repository, never a credential. A missing or
  expired credential is `AuthError`, which the CLI maps to exit 4. The local
  backend needs no `gh`, credentials or network. `Config.Transport`
  (`http.RoundTripper`, on `tracker.Config` and `pr.Config`) is the test seam:
  a consumer's tests serve GitHub responses from an `httptest` transport with no
  network and no `gh` process, and the CLI leaves it nil. github.com is the only
  host in the contract; an enterprise host would be a new field and a new
  guarantee, not a hidden fallback.

### The CLI adapter boundary

The adapter is `cmd/hew` plus `internal/cli`, and it owns everything whose input
or output is a terminal or a local file the user chose. None of it is a workflow
decision; when a line here grows one, it moves into a public package. The
extraction preserves the CLI contract: text and `--json`/NDJSON output, the
warning surface, exit codes and both backends' guarantees are what the commands
have today, and the existing tests pin them. A command that delegates renders the
same bytes and maps the same typed error to the same exit code.

- **Surface.** Flag and positional parsing, help text, deprecation aliases,
  `--json`, `--repo` (mapped to `Config.Repo`) and the exit-code mapping.
- **Input the user chose.** `--body-file`, `--file`, stdin, `--edit` and its
  `$EDITOR` launch, `--out`/`--check`, default source paths (`.beads/issues.jsonl`,
  the checkpoint beside a plan), and the checkpoint path itself.
- **Rendering.** Text columns, colour, NDJSON, `--bodies`, `--limit`, the primer's
  section caps and `--hook-format cursor` wrapper, progress lines, dry-run
  summaries, and the CLAUDE.md snippet `init` prints.
- **Agent configuration.** `hooks install|remove` edits `.claude/settings.json`,
  `.codex/hooks.json` or the opencode plugin, preserving unknown fields and
  refusing symlinks. It is editor configuration, not tracker state.
- **File formats that are not API.** The `apply` JSONL codec lives in
  `internal/plan`, and choosing or defaulting an input path (the plan, a beads
  snapshot, `issues.jsonl`) is the adapter's. `migrate.Beads` accepts the bytes or
  a reader and owns the source conversion; a consumer builds `migrate.Item`s for
  the engine instead of filling in a file.

### Stability

All three public packages are experimental until 1.0: the API may change in any
v0.x minor release. A breaking change ships as a `feat!:`/`fix!:` commit, so it
lands under Breaking changes in the release notes rather than Features, and the
CI API-surface check diffs every public package — `tracker`, `pr` and `migrate`
— against the base commit so the change is deliberate and visible in review.
The external-consumer module in `consumer/` is the other half of the gate: it
compiles and runs the supported workflows using only those packages. From 1.0
the compatibility policy (#171) governs the same surface. The minimum toolchain
is the `go` directive in `go.mod`, and `consumer/go.mod` declares the same
minimum; a consumer that pins an older toolchain than that is out of contract.

## Build & distribution

- **CI** (GitHub Actions, actions pinned by SHA at their latest versions): a
  workflow triggered on PRs and pushes to `main`, running `golangci-lint` and the
  full test suite (`go test -race -coverprofile ./...`), plus `shellcheck` on
  `install.sh` — the one thing strangers pipe into bash gets linted like everything
  else. Go version comes from `go.mod`.
- **The external-consumer gate**: `consumer/` is its own module with a local
  replace to the checkout, importing only `tracker`, `pr` and `migrate`. CI runs
  it with the race detector on a PATH without a hew binary, and checks that the
  module is tidy, that its go directive matches the root's, and that the public
  packages' production dependency graph excludes `internal/cli`,
  `internal/render` and `cmd/hew`. Its tests are the contract's scenarios:
  initialization, queries, creation, claims, dependencies, plans, pull requests
  and both migrations, plus typed refusals, concurrency, rate-limit retry and
  auth recovery against an isolated transport.
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
  detects OS/arch via `uname`, resolves the latest release through the GitHub API
  (or, when `HEW_VERSION` is set, takes that version as the tag and skips the API
  call; the value is validated as a semver because it lands in a URL),
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
- **`prime` token budget — superseded.** A full mock primer
  ([docs/primer-mock.md](docs/primer-mock.md)) for a busy repo — static conventions
  and command cheatsheet plus live Ready / In progress / Blocked / Epics sections —
  measures ~640 tokens (tiktoken `o200k_base`; Claude's tokenizer typically runs
  slightly higher). The real primer does not: the static conventions half alone
  costs ~860 tokens, because the cheatsheet documents every command and flag
  (`TestPrimerMatchesCommandSurface` pins that surface), and live primers
  measure 1014–1025. The budget was restated to ~1200 on the measurement (#63),
  and the live sections are each capped at five with pointers (#63 again), so
  the bound holds for repos larger than the fixtures too.
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

## Token efficiency (measured 2026-11-21)

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

lumberbarons/solar-controller — 14 open issues:

| command | hew | raw gh | ratio | baseline |
|---|---|---|---|---|
| `hew ready` | 324 | 1172 | 3.6x | gh api graphql (open issues) |
| `hew ready` | 324 | 1969 | 6.1x | gh issue list --json † |
| `hew list` | 353 | 1172 | 3.3x | gh api graphql (open issues) |
| `hew list --json` | 1607 | 1172 | 0.7x | gh api graphql (open issues) |
| `hew prime` | 1163 | 1172 | 1.0x | gh api graphql (open issues) |
| `hew show #119` | 431 | 590 | 1.4x | gh issue view --json + gh api graphql |

Findings, including the ones that don't flatter the tool:

- **The claim holds for the reads an agent actually loops on.** `ready` and
  `list` cost 16–25 tokens per open issue against 76–84 for the equivalent
  GraphQL and 107–141 for `gh issue list --json` — 3.3x–4.8x and 5.4x–6.8x
  respectively across both fixtures. This is the whole-tracker read that
  happens every iteration, so it is where the saving compounds.
- **`show` saves less: 1.4x–1.5x.** Bodies dominate `show` and both sides carry
  them verbatim; the value there is the deps line, not the token count. Worth
  saying plainly rather than averaging into a headline.
- **`list --json` currently costs *more* than the raw GraphQL it replaces
  (0.7x on both fixtures).** The flat schema writes every key on every line —
  empty arrays, derived booleans, `createdAt` — where the lean query writes seven
  fields. The schema's stability is deliberate (agents parse it), but the token
  cost is a real regression against the tool's own claim, filed as #62.
- **`prime` measures 1163 tokens on solar-controller and 1178 on hew.**
  Both fit the ~1200-token target tracked by the eval harness. The primer
  combines workflow guidance and a command cheatsheet with capped live
  sections. The exact count varies with the tracker state and issue titles.

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
- **Same-user claim races — narrowed for the local backend.** Every GitHub agent
  authenticates as `@me`, so two parallel sessions that race `start` inside the
  guard window are indistinguishable by assignee or label — both think they won.
  Exit 5 does not address this: it tells a caller the claim is `@me`'s, which is
  exactly the point at which one of *my* own sessions cannot tell whether the
  claimant is itself or a sibling. The local backend narrows both halves. Its
  default identity is `HEW_USER` when set, else the OS login qualified with the
  linked worktree's name and a short digest of its path (the main checkout keeps
  the bare login), so agents under one account in different worktrees are
  different claimants and exit 3/5 stay meaningful. It errors rather than guess
  when git cannot say which checkout this is, since a silently different identity
  would turn your own claim into someone else's. The identity is per worktree, not
  per session: two sessions sharing a worktree, or both in the main checkout,
  remain one claimant, and the later `start` gets exit 5 ("resume") instead of 3.
  `HEW_USER` must be unique per agent wherever sessions share a checkout. Its
  store also implements an atomic claim under the lock it already holds, so two
  starts that both passed the pre-read still have exactly one winner and the
  loser gets the conflict. GitHub
  keeps check-then-act plus the post-claim warning; if that becomes a real problem,
  the claim-comment nonce remains the fallback.
