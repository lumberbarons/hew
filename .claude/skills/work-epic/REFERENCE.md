# Reference

The driver's module map, state file, card states, the scripts' output schemas, the comment and
filing formats, the Orca shapes it relies on, and the report format for `work-epic`.

## The driver

`scripts/work_epic.py` is the coordinator. It wires real Orca, hew, and gh into `pump.Pump` and runs
the loop; everything it does is covered by `test_pump.py` against a simulated Orca and GitHub.

| Module | What it owns |
|---|---|
| `work_epic.py` | CLI: arguments, preflight, skill-directory discovery, `--dry-run`, the report and exit code |
| `pump.py` | The loop and the agents block: reaping, the review and repair lifecycles, the merge pass, parking, stopping |
| `orca_api.py` | The Orca CLI as typed calls; the envelope, errors, and the shapes captured from a live Orca |
| `findings.py` | Filing a reviewer's findings, the deterministic dedup rule, closing the fixed ones, the round comment |
| `resolve_ready.py` | Which issues spawn, and in what order (read-only planner) |
| `pr_state.py` | Which PRs merge, repair, update, or hold (read-only planner) |
| `local_state.py` | The same two decisions for a local tracker: spawn, and merge/repair/hold over delivered branches |
| `repo_state.py` | Scope resolution and the git/gh probes both planners share |
| `prompts/` | What each agent is told: `worker.md`, `reviewer.md`, `repairer.md` (`${name}` placeholders), and `worker_local.md`, `reviewer_local.md`, `repairer_local.md` for a local tracker |

One tick: reap settled agents, start a reviewer for each delivered PR, run the merge pass, spawn
into free slots. Between ticks the driver waits on the Run's mailbox with `check --wait`, which is
also the CI-poll clock (~2 minutes). It acknowledges each Delivery on its next wait, records
`question` and `escalation` messages in the report, and never answers them. It stops when no agent is
live, nothing can spawn, and every open PR is parked.

## Local tracker mode

`repo_state.local_tracker()` is true when no `--repo` was given and the main checkout's
`.hew/config.json` names the `local` backend — hew's own rule, read without running a hew command
because every one that reports it also writes. It sets `Config.local`; the loop is unchanged and the
differences are these.

| | GitHub | Local |
|---|---|---|
| Delivery | a PR, id = PR number | a committed, unpushed branch, **id = issue number** |
| Delivery id in keys | `wr-<pr>.json`, `pr<pr>:hold`, `--reviewing`/`--repairing` | the same keys, with the issue number |
| Spawn planner | `resolve_ready.resolve` (gh user, open PRs, remote branches) | `local_state.resolve`: claims are the pump's own exactly when `state.json` tracks the issue; delivered branches count as `in_review` |
| Merge planner | `pr_state.plan` | `local_state.plan` over the issues the pump holds in `delivered`, `reviewing`, `reviewed`, or `review_failed` |
| Worktree base | `origin/<default>` | `<default>` |
| Review state | the round comment's marker | `state.json`: `reviewed` |
| Repair attempts | the repair-failure comments | `state.json`: `repair_failures` |
| "CI passed" | the check rollup | `state.json`: `verified_head` equals the branch head |
| Merge | `gh pr merge --squash` | `git merge --squash <verified sha>` and a commit in the main checkout, then `hew close --completed` |
| Cleanup | remote ref, local branches, worktree | local branches, worktree |
| Round comment | posted on the PR | `<outdir>/rounds/wr-<id>.md` |

A branch is `verified` when the head the reviewer (or a repairer) reports as gated is the head the
branch has now. `local_state.plan_branch` is the decision table, in order: `wait` (issue closed,
reviewer live, branch unreadable, or no review round yet); `repair` or `conflict` when
`git merge-tree` says merging would conflict; `hold` on an open P1 or above; `repair` or
`ci_failed` when the branch is behind the default branch or its head is unverified; else `merge`.
`pr_state.schedule` then applies the same queue as for PRs: at most one branch moves toward the
default branch per pass, and a live repairer freezes the rest. Every merge moves the default branch,
so every later branch is behind and takes one repairer run — merge the default branch in, run the
quality gate, report the new head — before its turn. The repairer prompt doubles as that
"verify" step and is told why it was started.

The merge writes into the human's main checkout, so `Pump.main_blocker()` requires it to be on the
default branch with a clean tree, and parks the merge as a gate (`pr<id>:merge`) until it is. A
squash that fails is undone with `git reset --merge` and counted like any failed merge. If the
squash lands but `hew close` fails, the merge stands: the pump parks `issue-<n>` asking for the
close and does not merge again.

`--resume` does not re-offer bare claims here: a local tracker names claimants per worktree
(`<user>-<worktree>-<hash>`), so no assignee ever equals the coordinator's name. A claim the state
never recorded is therefore skipped as claimed; the human releases it with `hew release <n> --force`.

## Agents and prompts

| Role | What it runs | Agent |
|---|---|---|
| **Coordinator** | `work_epic.py` | The terminal the human started it in |
| **Implementer** | `work-issue` in the issue's worktree | `opencode` |
| **Reviewer** | `review-code`, then `review-tests`, fixing what each found, in the same worktree | `claude` |
| **Repairer** | The repair contract in the same worktree | `opencode` |

The agents are `pump.py`'s `IMPLEMENTER`, `REVIEWER`, and `REPAIRER`, and nowhere else. The reviewer
is a different agent from the implementer by construction: that is the second point of view. The
implementer and reviewer prompts load their skill by name, so both agents must have it installed; the
reviewer's fix step and the repairer read the quality-gate table from the work-issue skill
directory, so it must be readable on the host.

The prompts are the contract with each agent. Workers write an outcome file into their own
worktree (`.work-epic/we-<n>.json`, `wr-<pr>.json`, `wc-<pr>.json`) and the driver trusts only that
file, never terminal output. The reviewer runs `review-code` with `--report-only`: critique's
review-code repairs a clean development branch on its own by default, but the reviewer owns the
repair phase here, so the findings file holds every finding rather than only the unresolved ones.
The reviewer's file is review-code's findings file plus `fixed` and
`fixedIn` per finding (and `unfixedReason` when not fixed), and `head` and `gate` at the top.

**The test review** is a second step in the same session, not a second agent. After fixing
review-code's findings the reviewer runs `review-tests --report-only` (to
`.work-epic/wr-<pr>.tests.json`, which the pump never reads), appends its findings to the main file
with `"skill": "tests"` on each, and fixes the P1 and P2 ones by the same procedure, tests only: a
finding that needs a production change stays unfixed. It runs after the code fixes so it reads the
branch as it stands, and there is one push at the end. The implementer works test-first, so its tests
are the part most likely to confirm themselves; review-code drops test defects by design, so
nothing else looks at them. If `review-tests` ends in `error`, the main file's status becomes
`error` and the review is parked like any review that did not run.

The pump decides whether the lens runs, not the agent: `Pump.tests_lens` lists the delivery's files
(`gh pr diff --name-only`, or `git diff --name-only <default>...<branch>` locally) and passes
`run` or `skip` into the prompt as `tests_lens`. `repo_state.touches_code` says `skip` only for a
diff of nothing but prose (`.md`, `.mdx`, `.rst`, `.txt`, `.adoc`); a diff that cannot be listed
runs the lens. The
repairer's is `{status: resolved|blocked|error, head, reason, gate}`. The worker's is work-issue's
own outcome file (`status`, `issues[0].pr`, `issues[0].branch`).

Orca's CLI does not expose an installed-agent list, so the choice is the operator's declaration. A
failed `worker-start` names the agent and returns `failedStage` / `residualResources`; the driver
releases the residual terminal, reports the issue as unscheduled, and never silently tries a
different agent.

## Orca's model, and what the pump uses

| Orca concept | What it is | How work-epic uses it |
|---|---|---|
| **Run** | A durable namespace and mailbox, bound to the coordinator | One per pump invocation (or the bound previous Run under `--resume`); scopes every Task and every `check` |
| **Task** | A unit of work with a status | One per implementer (title prefix `we-`), reviewer (`wr-`), or repairer (`wc-`); Tasks are execution records, not a second plan |
| **Dispatch** | One authoritative attempt at a Task | The id used for `worker-show`, `worker-read`, `worker-release`, and recovery; never address a worker by terminal title |
| **Worktree** | A tracked checkout with metadata, terminals, and a board card | The durable unit per issue: issue link, PR link, status, comment, checkout, and the place review and repair happen |
| **Terminal** | A PTY or chat surface owned by a Dispatch | Never driven directly by the pump; `worker-read`, `worker-show`, and `worker-release` cover both terminals and chats |

The pump never uses `agent launch`, `worktree create --agent`, or `orchestration dispatch` to start
workers. Those are handoffs or unsupervised adoptions: they create no authoritative Dispatch, so the
pump cannot settle, release, or recover them. `worker-start` is the only spawn path. The same rule
applies to the worktree: `worker-start --worktree new-child` is Orca's agent-first worktree
creation, `worker-start --worktree id:<child>` puts a reviewer or repairer in an existing one, and
`worktree set` is how the pump annotates it.

The planners remain the only decision layer. Orca Tasks are bookkeeping for what this pump started;
dependency order lives in hew and `resolve_ready.py`.

## Run binding and state

The Run is `work-epic #<n> #<n> …` — the scope in the order given. A fresh invocation creates one.
`--resume` rebinds the Run recorded in `state.json`, else the one Run whose objective matches
exactly, else stops: no match creates a new Run, and more than one match is ambiguous and stops
rather than guess between two live pumps. A fresh invocation moves a stale `state.json` aside.

`$OUTDIR/state.json` (`<coordinator worktree>/.work-epic/coordinator/`) is written after every
transition and read once at startup. Everything else is re-read from Orca, hew, and GitHub each
tick, so a restart loses nothing it cannot re-derive:

```json
{
  "run": "run_abc123",
  "scope": [21, 22, 25],
  "issues": {
    "21": { "state": "reviewed", "worktree": "repo::/abs/we-21", "path": "/abs/we-21",
            "orca_branch": "lumberbarons/we-21", "branch": "fix/21-x", "pr": 34,
            "large": false, "findings": [41, 42] }
  },
  "agents": {
    "ctx_abc": { "role": "wr", "key": "34", "issue": 21, "pr": 34, "agent": "claude",
                 "worktree": "repo::/abs/we-21", "path": "/abs/we-21", "started": 1790000000.0,
                 "done": true, "archived": ".../outcomes/wr-34.json" }
  },
  "parked": { "pr36:hold": { "text": "...", "issue": 24, "pr": 36, "kind": "gate" } },
  "waiting": { "37": { "sig": ["9f2c…", "pending", "CLEAN", "checks pending"], "since": 1790000000.0 } },
  "merged": [ { "pr": 34, "issue": 21 } ],
  "notes": [ { "type": "question", "id": "msg_…", "subject": "…" } ]
}
```

An issue's `state` is one of `implementing`, `delivered`, `reviewing`, `reviewed`, `merged`,
`failed`, `error`, `noop`, `review_failed`, or `unscheduled`. The pump never spawns an issue it
already tracks, whatever state it is in. `parked` entries of kind `gate` follow the PR planner and
clear themselves when the human lifts the gate (closes the finding, resolves the conflict); kind
`event` entries stay until the run ends. Outcome files are archived to `$OUTDIR/outcomes/` before the
worker is released and removed from the worktree, so a stale file never sits in the checkout the
reviewer reuses. The driver lists `.work-epic/` in the repository's shared `info/exclude`
(local, never committed), so scratch files stay out of every worktree's `git status`.

The worktree comment is the durable fallback if `$OUTDIR` is lost; the issue link is the durable
selector (`orca worktree show --worktree issue:<n>`).

## Worktree conventions

### Names and identity

- **Issue worktree** — `--name "we-<n>"`, display name `#<n> <short title>`. The worktree id (the
  full `<repo-id>::<path>`) is the identity; the display name is for humans. Never use a display
  name as a key. The reviewer and the repairer run in this same worktree: no second checkout, no
  second setup, no branch to collide with.
- **Task titles** — start with the role prefix and carry the agent: `we-<n> · opencode`,
  `wr-<pr> · claude`, `wc-<pr> · opencode`. Filter on the prefix. Task titles carry no uniqueness
  guarantee; the Run scopes them.

Repository setup runs once per issue under `inherit`; pass `--setup skip` only when the human has
said the checkout does not need it. The reviewer and the repairer reuse the setup the implementer
already paid for.

### Comment marker

Every worktree the pump creates carries one line:

```
work-epic · <run-id> · we-<n> · <agent> · <state>
```

The coordinator's own worktree may be the human's, so its line is the shorter
`work-epic · <run-id> · coordinator <state>`; `recover()` and the resume matcher read only the first two
tokens. Example: `work-epic · run_abc123 · we-21 · opencode · reviewed by claude; 2 fixed; ready to
merge`.

Orca has no labels, so this comment is the pump's custom metadata as well as the human status line.
Keep the marker tokens verbatim; resume and orphan adoption read them. The gate goes after the
fourth token; never replace the marker with prose.

### Links, status, and unread

The driver sets `--issue <n>` right after creating a worktree and `--pr <pr>` at delivery. Status transitions and the comment state each carries:

| When | `--workspace-status` | Comment state |
|---|---|---|
| worktree created, worker starting | `in-progress` | `worker started` |
| PR delivered | `in-review` | `PR #<pr> delivered; review pending` |
| reviewer running | `in-review` | `reviewing by <reviewer>` |
| reviewed, nothing held | `in-review` | `reviewed by <reviewer>; <n> fixed, <m> filed open; ready to merge` |
| unfixed P1 holds it | `in-review` + `--unread` | `HOLD: P1 finding #<issue> not fixed (reviewer <reviewer>)` |
| repairing | `in-review` | `repairing PR #<pr> (<repairer>)` |
| conflict / CI failed / protected | `in-review` + `--unread` | `CONFLICT`, `CI FAILED: <check>`, or `PROTECTED: <what>` |
| worker failed / stalled | `in-progress` + `--unread` | `failed: <reason>; branch pushed` |
| worker errored | `in-progress` + `--unread` | `error: <reason>` |
| worker parked on a dialog | `in-progress` + `--unread` | `BLOCKED: <what>` |
| merged | `completed` | `merged` |
| worker returned no work / not eligible | `completed` (then remove if clean) | `no ready work` / `not eligible` |

There is no blocked status: a PR gate is `in-review` with the gate named. The pump raises the
unread dot and never clears a human's dot; the human clears it. The driver keeps the marker tokens when it
rewrites the trailing state, and never changes a coordinator's display name, issue/PR links, or
parent lineage: that worktree may be the human's.

### Lifecycle and cleanup

The issue's worktree is kept from spawn through review, repair, and the final merge. That is
deliberate: the reviewer and repairer reuse the checkout and its setup, and the human keeps a live
card while the PR is open.

- **Delivered, PR open** — keep, `in-review`.
- **Held, conflicted, CI-failed, protected, failed, blocked** — keep, `--unread`, gate in the
  comment.
- **Merged** — only after `gh pr merge` has succeeded; the sequence is under
  [Merge-pass commands](#merge-pass-commands). A merge that fails leaves the card, checkout, and
  state untouched.
- **No ready work / not eligible** — the tree did no work; remove it if clean.
- **Failed worker (branch pushed, no PR)** — parked; the tree is kept, unread, and the branch is on
  origin. The resolver treats the issue as `stalled`; a human decides, never a respawn.
- **Orca's own branch** — `worker-start --worktree new-child --name <n>` makes Orca create a branch
  for the worktree, `<user>/<n>` (for example `lumberbarons/we-21`), and report it as `branch` on
  the receipt. The driver records it. `work-issue` then switches to its `fix|feat|chore/<n>-…` branch, and
  `worktree rm` only deletes the branch that is checked out at removal, so Orca's own branch is
  left behind with nothing on it. Delete it after a successful removal with
  `git -C "$MAIN" branch -d "<orca-branch>"`: lowercase `-d` refuses a branch holding commits the
  default branch lacks, so it cannot discard work. A refusal, or a missing branch, is
  left alone and never forced or guessed from the name. When the agent never
  switched, Orca's branch is the checked-out one and `worktree rm` already handled it.
- **Archive hooks** — `worktree rm` skips repo-defined `orca.yaml` archive hooks unless
  `--run-hooks` is passed. The pump skips them by default. A failed archive hook blocks removal;
  report and leave the tree.

Never remove a worktree with a live terminal, a blocked worker, uncommitted work, or an open PR
that is still being worked. Report retained paths in every stop.

### Resume and orphan adoption

`--resume` rebinds the Run (see Run binding and state) and calls `recover()` before the first tick:

- **A handler that never finished** — an agent recorded as done but not handled (the process died
  after reaping it) is re-handled from its archived outcome file. Filing and closing are idempotent,
  so a review that crashed mid-filing is finished, not repeated.
- **A worker started but never recorded** — a worktree whose comment carries
  `work-epic · <run-id> · we-<n>` with a worker row in this Run but no agent in the state file is
  adopted as the issue's implementer, so it is neither respawned nor orphaned.
- **Another Run's worktrees** — a marker naming a different Run belongs to another pump. The driver
  never adopts, releases, or removes it.

`--resume` also tells the resolver to offer this user's bare claims once, on the first pass; after
that, anything the pump tracks is an agent in flight and is never spawned again. A reviewer or
repairer that was started but never recorded is not adopted: that double failure is rare, and the
planner would start a second one on the same worktree, so check `orca worktree list` after a crash
that interrupted a review or repair.

## Resolver output

One JSON object from `scripts/resolve_ready.py <issue-number>... [--repo owner/name] [--resume]
[--workers N]`:

```json
{
  "scope": [12],
  "mainCheckout": "/abs/path/to/repo",
  "defaultBranch": "main",
  "eligible": [
    { "number": 21, "title": "issue b", "priority": "P1", "type": "task",
      "where": 2, "doneWhen": 3, "large": false, "resume": false }
  ],
  "skipped": [
    { "number": 22, "title": "issue c", "reason": "blocked", "detail": "blocked by #35" },
    { "number": 23, "title": "issue d", "reason": "in_review", "detail": "PR #40 open" },
    { "number": 24, "title": "issue e", "reason": "claimed", "own": true,
      "detail": "in progress @you — pass --resume to pick it up" }
  ],
  "counts": { "eligible": 1, "blocked": 1, "untriaged": 0, "claimed": 1,
              "in_review": 1, "stalled": 0, "closed": 4 },
  "spawnBudget": 2
}
```

- `scope` — the issue numbers as given. An epic number expands to its children (open and closed);
  any other number is that issue. The same numbers go to `pr_state.py`.
- `mainCheckout` — the primary worktree (`git worktree list --porcelain`'s first entry). It is the
  fetch/fast-forward anchor, not the spawn cwd: Orca creates worktrees from `origin/<default>`
  explicitly. `null` when undeterminable — fall back to `$PWD` and say so in the report.
- `defaultBranch` — origin's HEAD branch, falling back to GitHub's default branch; `null` when
  neither answers. The base every worktree and every PR must target.
- `eligible` — spawn order: priority first (`P0` best, missing last), ties toward the oldest
  `createdAt`. `where`/`doneWhen` are bullet counts under the body's `### Where` /
  `### Done when` sections; `large` is work-issue's sizing rule (>3 paths, >5 done-when) — surfaced
  so the report can say why a large issue might be worth a `--workers 1` moment. Only `- ` bullets
  are counted, so a prose `### Where` reads `where: 0` and `large` can under-report; an absent flag
  is not proof the issue is small (tracked in #31). `resume` is true only for an issue `--resume`
  brought back.
- `skipped` — open issues left behind, `reason` one of:
  - `in_review` — an open PR's head branch carries the issue's number. Delivered; the merge pass
    owns it now, whoever holds the claim.
  - `blocked` — open blockers.
  - `untriaged` — missing priority or type.
  - `claimed` — in progress by someone else, or by you with neither PR nor pushed branch (in
    flight, or a crashed run — `--resume` makes those eligible). Yours carry `own: true`.
  - `stalled` — in progress by you, a conventional branch pushed, no open PR: a worker failed
    verification or its PR was closed. Never eligible, even with `--resume`; a human decides.

  "You" is the current `gh api user` login; when it cannot be determined every in-progress issue
  is treated as claimed by someone else (fail-safe).
- `counts` sums every skip reason plus `eligible` and `closed`.
- `spawnBudget` — twice `--workers` minus the scope's open PRs (`in_review`) minus your own bare
  claims (`claimed` with `own: true` — workers in flight, each a PR on its way), floored at zero.
  Parked PRs count. A crashed run's leftover claims count too until `--resume` picks them up — the
  fail-safe direction, spawning less.
- Review findings are not special here. The reviewer fixes what it can in its own session and the
  rest is a human's backlog; a finding issue that reaches the resolver (named in the scope, or an
  old epic child) is ordinary work.
- The PR and branch reads fail the whole run rather than degrade: without them a delivered or
  failed issue would read as eligible.
- Exit codes: `0` success (eligible may be empty), `1` runtime error, `2` usage error.

## Merge-pass output

One JSON object from `scripts/pr_state.py <issue-number>... [--repo owner/name]
[--reviewing <pr>]... [--repairing <pr>]...` (flags in any position):

```json
{
  "scope": [12],
  "defaultBranch": "main",
  "mainCheckout": "/abs/path/to/repo",
  "queueHead": 34,
  "repairHead": null,
  "prs": [
    { "number": 34, "issue": 21, "issueState": "open", "branch": "fix/21-issue-b",
      "head": "9f2c…", "draft": false, "mergeState": "CLEAN", "mergeable": "MERGEABLE",
      "checks": "pass", "behindBy": 2, "reviewed": true,
      "repairFailures": 0, "worstOpenFinding": "P2", "action": "update_branch",
      "note": "2 commits behind" },
    { "number": 35, "issue": 22, "issueState": "open", "branch": "feat/22-issue-c",
      "head": "1d7e…", "draft": false, "mergeState": "CLEAN", "mergeable": "MERGEABLE",
      "checks": "pass", "behindBy": 2, "reviewed": true,
      "repairFailures": 0, "worstOpenFinding": null, "action": "queued",
      "note": "queued behind PR #34" }
  ],
  "unmatched": [ { "number": 99, "scrapedIssue": null,
                   "reason": "no issue number in the head branch", "action": "outside_scope" } ],
  "counts": { "update_branch": 1, "queued": 1, "outside_scope": 1 }
}
```

- PRs are matched to scope issues by scraping the conventional issue number off the head branch
  (`fix|feat|chore/<n>-…`, work-issue's naming rule). A PR that scrapes to nothing, to an issue
  outside the scope, or that targets a base other than the default branch lands in `unmatched` with
  its `reason` — listed for visibility, never touched.
- `checks` collapses `statusCheckRollup`: `pass`, `fail`, `pending`, or `none`. `none` is CI that
  has not reported yet, never green: a repository without CI is not supported.
- `mergeable` is GitHub's conflict verdict, which — unlike `mergeState` — a draft does not mask.
- `behindBy` is how many default-branch commits the PR head lacks, from the compare API
  (`gh api repos/<owner>/<repo>/compare/<default>...<head>`); `null` when the call fails. GitHub's
  own `BEHIND` appears only under a "require branches to be up to date" rule, so without this a
  stale PR on an unprotected repo reads `CLEAN` and merges on old CI. A ready PR with `behindBy`
  unknown never merges.
- `inFlight: true` marks a ready, current PR whose checks are pending or not yet reported — the CI
  run the queue waits on. Absent otherwise.
- `reviewed` is true when the PR carries a review-round comment (a body containing the
  reviewer-agent marker). That comment is the whole review state: a PR is reviewed once, and a head
  that moves afterwards — the reviewer's own fixes, a repair, a human's edit — is not reviewed again.
- `repairFailures` counts the failed-repair comments on the PR (bodies containing the repair-agent
  failure marker). One counter covers conflicts and red CI together: after two, the PR parks.
- `worstOpenFinding` is the worst severity among open issues carrying `review-of: #<issue>` in their
  body (see Findings filing). The reviewer files what it fixed and closes it, so only a finding it
  could not fix is open. At P1 or above it holds the PR; below, it is tracked backlog.
- `note` says why, on `wait` and the gated actions.
- Decision order, first match wins: issue closed → `wait` · a live reviewer (`--reviewing`) →
  `wait` · comments unreadable → `wait` · `DIRTY` or `CONFLICTING` → `repair` when no blocker is
  open and fewer than two failed attempts are recorded, otherwise `conflict` · open blocker →
  `hold` · ready PR behind (`behindBy` > 0 or `BEHIND`) → `update_branch` · no review round →
  `wait` · checks failing → `repair` under the same two-attempt rule, otherwise `ci_failed` ·
  checks pending or absent → `wait` · draft → `mark_ready` · `behindBy` unknown → `wait` ·
  `CLEAN | HAS_HOOKS` → `merge` · `BLOCKED` → `protected` · otherwise `wait`. A hold comes before
  `update_branch` on purpose, and drafts are never updated.
- Then the merge queue: `queueHead` is the lowest-numbered `merge`; else the lowest-numbered
  `inFlight` PR; else the `update_branch` PR whose checks last passed, lowest number first; else
  `null`. Every other `merge` and `update_branch` becomes `queued`. One PR moves toward main at a
  time because each merge puts every other PR behind again — parallel updates are CI spent on
  results the next merge discards, and two merges from one plan land the second on CI that never
  saw the first. A failing PR is never `inFlight`, so it cannot stall the queue.
- Repairs ride the same discipline: `repairHead` is the lowest-numbered `repair` candidate and
  exists only while `queueHead` is `null` — a merge in flight would re-conflict the repair, and
  repairs done side by side are invalidated by the first merge. Every other candidate becomes
  `queued`, behind the repair head or behind the PR whose merge is in flight.
- `--repairing <pr>` (repeatable) names the PRs whose `wc-<pr>` Task is live — the pump passes
  every one, from `task-list`/`worker-list`, because the planner re-runs while the repairer works
  and GitHub shows nothing of it. While any is live the plan freezes around it: that PR is `wait`
  ("repairer wc-<pr> in flight") whatever GitHub reads, `repairHead` is the lowest live one,
  `queueHead` is `null`, and every other `merge`, `update_branch`, and `repair` is `queued` behind
  it.
- `--reviewing <pr>` (repeatable) names the PRs whose `wr-<pr>` Task is live. The PR is `wait`
  ("reviewer in flight") whatever its comments say; nothing else freezes, since a review neither
  moves main nor conflicts with a merge elsewhere.
- Exit codes: `0` success (prs may be empty), `1` runtime error, `2` usage error.

## Review-round comment

Every `reviewed` findings file — clean or not — becomes exactly one comment. Post it **after** the
findings are filed and the fixed ones closed (see below): it quotes the issues' numbers, and a round
comment with no blocker filed behind it reads to the planner as a clean review:

```bash
gh pr comment <pr> --body "$(cat <<'EOF'
review-code findings (work-epic reviewer agent):

### P1 — <finding title>
<location> — <consequence>. Fixed in <sha>. Filed as #<issue> and closed.

### P2 — <finding title>
<location> — <consequence>. Not fixed: <unfixedReason>. Filed as #<issue>.

### P3 — <finding title>
<location> — <consequence>. Fix: <prescription>. Not filed.

gate: <the file's `gate`>
EOF
)"
```

A clean review says so in one line in place of the finding blocks. Keep each finding's own
title/location text; the comment is a relay, not a rewrite. The first line is identity, like
`review-key:` — `pr_state.py` reads it as "reviewed", so it must survive verbatim. A missing round
comment reads as "never reviewed" and the PR is never merged.

## Repair comments

The pump posts exactly one comment per repairer run — never the agent — from its outcome file,
before acting on it:

```bash
# resolved — the success record; the planner doesn't count it
gh pr comment <pr> --body "$(cat <<'EOF'
work-epic repair (repair agent):

<what was repaired: conflict with origin/<default>, failing check <name>> — gate: <command and result>.
repaired-head: <head from the outcome file>
EOF
)"

# anything but a verified resolved push — blocked, error, no outcome file, a spent budget, a
# head mismatch; the planner counts these toward the two-attempt cap
gh pr comment <pr> --body "$(cat <<'EOF'
work-epic repair failed (repair agent):

<reason>

attempt-head: <head at the attempt>
EOF
)"
```

The failure comment's first line is identity, like the review round's: `pr_state.py` counts these
and parks the PR on `conflict` or `ci_failed` after two. The success comment is the timeline's
record. Nothing about a successful repair puts the PR back into review.

## Findings filing

`findings.Filer` turns the reviewer's findings file (critique's FINDINGS.md shape, plus the `fixed`
fields) into issues, filed with no parent: they are the paper trail of this review. It runs before the
review-round comment is posted, so a failure leaves the PR unreviewed rather than mergeable with its
blocker missing. In order, it runs:

```bash
uv run --no-project <raise-issues skill dir>/scripts/findings_to_plan.py \
  "$OUTDIR/outcomes/wr-<pr>.json" --reviewed-issue <n> --reviewed-pr <pr> \
  --at-or-above P2 --out "$OUTDIR/wr-<pr>.plan.jsonl"
hew list --json --bodies --state all        # → wr-<pr>.issues.ndjson
uv run --no-project <raise-issues skill dir>/scripts/dedup_candidates.py \
  "$OUTDIR/wr-<pr>.plan.jsonl" "$OUTDIR/wr-<pr>.issues.ndjson" --reviewed-issue <n>
hew apply "$OUTDIR/wr-<pr>.plan.jsonl" --dry-run
hew apply "$OUTDIR/wr-<pr>.plan.jsonl"
```

The converter derives each line's `review-key: <skill>/<pattern>/<scope>` mechanically (a
finding's own `skill` wins over the file's, which is how test-review findings file under `tests/`), stamps
`review-of: #<n> (PR #<pr>)` into `where`, adds `fixed-in: <sha>` when the finding carries
`fixedIn`, and — with `--at-or-above P2` — leaves P3s to the round comment. Exit 3 means the file's
status is not `reviewed`; the driver parks that case before filing, so it should never reach here.

**The dedup rule is deterministic.** `dedup_candidates.py` finds leads — issues with the same review
key raised against the same review target — and the raise-issues agent flow reads each body to decide.
A driver has no reader, so it drops a plan line only when a candidate has the same key, the same
review target, **and** the same set of paths (`same_paths`); the line then reuses that issue's number
in the round comment. Anything less is filed. This errs toward a duplicate over a lost finding. A
finding raised against another review target is never a duplicate, so a finding about a fix stays
distinct from the finding the fix was for.

Then it closes what the reviewer fixed. Every open issue that names this PR
(`review-of: #<n> (PR #<pr>)`) and carries a `fixed-in:` line is closed as completed, with that
commit as the reason — equivalent to:

```bash
hew list --json --bodies \
  | jq -r --arg of "review-of: #<n> (PR #<pr>)" \
      'select(.body | contains($of) and test("(^|\n)fixed-in: [0-9a-f]+"))
       | "\(.number) \(.body | capture("(^|\n)fixed-in: (?<sha>[0-9a-f]+)").sha)"' \
  | while read -r issue sha; do
      hew close "$issue" --completed --reason "Fixed in $sha on PR #<pr> before merge"
    done
```

Unfixed findings carry no `fixed-in:` line, so they stay open. The step is safe to repeat — a closed
issue no longer lists — which is why `recover()` can re-run a filing a crash interrupted: until the
close lands, a fixed P1 sits open and holds a PR that has no round comment yet, which is the safe
direction.

Each findings issue is filed `blocked-by` the reviewed issue, so an unfixed finding's own fix waits
until the PR merges and the defect exists on main. An open finding at P1 or above holds its PR until
the human closes it — after fixing the branch, or accepting the finding as is. Once it closes, the
next pass readies and merges the PR; nothing re-reviews it.

## Timeout budgets

The budgets are constants at the top of `pump.py`.

- **Mailbox wait:** one `check --wait` of 2 minutes, which is also the cadence at which CI and
  idle agents are re-read. A timeout is a checkpoint, never a failure.
- **Per-agent budget:** continuous working time before the driver parks a flag on the worktree (it
  never kills the agent): implementer ~2 hours, reviewer ~90 minutes, repairer ~1 hour.
- **Outcome grace:** a settled agent's outcome file can trail the settlement; a missing file is
  re-read for ~90 seconds, then counts as an `error`.
- **CI wait:** a PR whose `wait` has shown no state change (head, checks, merge state) for ~60
  minutes is parked and set aside; the pump moves on.
- **Release:** `worker-release` is the only way a terminal is closed, and only after the outcome is
  read and archived. A release that reports anything but success is recorded in the report's notes,
  not acted on; it never earns a `terminal close`.

## Orca shapes the driver relies on

Captured from a live Orca 1.4.x and pinned in `test_orca_api.py`. Every command answers
`{"ok", "result"}` or `{"ok": false, "error": {"code", "message", "data"}}`; `OrcaError` carries the
code, the data, and the partial receipt.

- `worker-start` → `result.{taskId, dispatchId, state: "ready", effects[{kind: "worktree", action,
  id}], residualResources}`. The worktree id Orca created is the `worktree` effect's `id`; an older
  host that omits it is found by the unique comment marker. Exit 0 only for `ready`.
- `worker-list --run` → `result.{workers[], page{hasMore, nextCursor}}`; each row has `dispatchId`,
  `workerState`, `dispatchStatus`, `terminalState`, and `projection.{liveness, attention,
  nextAction}`. A Dispatch is over when `workerState` is `succeeded` or `failed`.
- `check --wait --run --types` → `result.{deliveryId, messages[], count, timedOut}`; a message's
  `payload` is a JSON string (`dispatchId`, `outcome`, `reportPath`).
- `worktree list` rows: `id`, `path`, `branch` (`refs/heads/…`), `comment`, `workspaceStatus`,
  `isUnread`, `linkedIssue` (a number), `linkedPR`.
- `worktree rm` is asynchronous (`removing: true`); the driver polls the list for the worktree to
  disappear before deleting branches.
- `worker-show` → `observation.agentWait`: non-null names a worker parked on a dialog only a human can
  answer. Null means Orca never looked, not that the worker is free.

Not used yet: `--retry-request` on mutations (idempotent replay) and `request-show`. Adding them is
the next hardening step, once the request-id format is confirmed on a live run.

## Merge-pass commands

Per `pr_state.py` action. None of them checks out a PR branch in `$MAIN`:

```bash
# mark_ready — flips the draft so the next pass sees the real mergeState
gh pr ready <pr>

# merge — the planner already verified checks, mergeState, and holds. No --delete-branch:
# the worktree still has the branch checked out. A failure touches nothing else.
gh pr merge <pr> --squash

# after a successful merge only — finish the issue, then the branches
ORCA worktree set --worktree "id:<child-id>" --workspace-status completed \
  --comment "work-epic · <run> · we-<n> · opencode · merged" --json
ORCA worktree rm --worktree "id:<child-id>" --json          # only when clean
gh api -X DELETE "repos/{owner}/{repo}/git/refs/heads/<branch>"   # 422 = already gone
git -C "$MAIN" branch -D "<branch>"                         # skip if the worktree was kept
git -C "$MAIN" branch -d "<orca-branch>"                    # Orca's own branch; -d refuses unmerged work
# worktree rm deletes the local branch only when it can prove it merged, which a squash merge
# defeats — hence the explicit branch -D (the PR is merged; nothing unique is lost).
# branch -d (lowercase) on Orca's own branch is deliberate: it refuses a branch holding commits
# the default branch lacks, which is the safety check.

# update_branch — GitHub merges the base into the PR branch server-side
gh pr update-branch <pr>

# post-merge main fast-forward, only while $MAIN is on the default branch
test "$(git -C "$MAIN" symbolic-ref --short HEAD)" = "$DEFAULT" &&
  git -C "$MAIN" fetch origin &&
  git -C "$MAIN" merge --ff-only "origin/$DEFAULT"
```

If the worktree is dirty or removal fails, do not discard anything: keep it, mark it unread, report
the path, and run only the remote delete (the local branch is still checked out). Branch cleanup
failing after a successful merge is reported, never an escalation.

The merge method is squash, and the repairer's merge commit is squashed away with the rest of the
branch. A failed `update-branch` is re-planned, not filed blind: the next pass reads the PR `DIRTY`
and the `repair` row owns it. A failed `gh pr merge` (a required check appeared, the base moved
between read and merge) is not retried blind — re-run the planner, which classifies the PR's new
state; a second merge failure on the same head is an escalation. `gh pr merge --auto` would
delegate the final trigger to GitHub, but it requires repo-level auto-merge and forfeits the merge
queue and the repair budget; keep the explicit loop. GitHub's own merge queue does the same job
where the plan allows it, but it is not available on every repository, and the pump must work on
all of them.

## Report format

```
Run run_abc123 over #21 #22 #24 #26: STOPPED
  #21: merged; PR #34; findings #41, #42
  #22: reviewed; PR #35
  #24: reviewed; PR #36; findings #44
  #26: failed
  merged: PR #34
  resolver: closed 4, blocked 0, untriaged 0, claimed 0, in_review 2, stalled 1, eligible 0
  spawn budget 2; stopped, not drained
  decisions needed:
    PR #36: an open P1 finding the reviewer could not fix holds it; close the finding to release the PR
    #26: the opencode worker failed — gate would not pass
```

"Drained" means no parked decisions and nothing left open in scope; "stopped" means decisions are
waiting; "halted" means an outage stopped the pump. An epic scope whose children are all closed is
reported as such and left for a human to close. `report.json` beside the state file carries the same
data.

## Script tests

```bash
python3 -m unittest discover <this skill's directory>/scripts
```

- `test_pr_state.py`, `test_resolve_ready.py`, `test_repo_state.py` — the decision tables and the
  scope parser, pure over hew and gh JSON.
- `test_orca_api.py` — the envelope and the captured Orca shapes.
- `test_findings.py` — filing, the dedup rule, closing the fixed, the round comment.
- `test_pump.py` — the whole pump against a simulated Orca, GitHub, and tracker: the happy path,
  review and fix, holds, repairs, halts, stalls, resume, and the mailbox.

A change to a decision, a skip reason, a parser, or the loop lands with the test case that pins it.
The review-once rule is pinned in `test_pr_state.py` (a moved head is not re-reviewed) and in
`test_pump.py` (a repaired PR merges without a second reviewer).

## Worktree reliability notes

- **The issue worktree's branch is work-issue's.** Orca creates the worktree with a base branch;
  work-issue may create its conventional `fix|feat|chore/<n>-…` branch inside it. That is the
  checkout the reviewer and the repairer reuse; the unused Orca branch is cleaned up per
  [Lifecycle and cleanup](#lifecycle-and-cleanup).
- **A server-side `update_branch` moves the PR branch, not the issue's checkout.** A later reviewer
  or repairer must fast-forward the checkout to `origin/<branch>` first; a fast-forward failure is
  an `error`, not a reset.
