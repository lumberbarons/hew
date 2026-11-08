---
name: work-epic
description: Burn through a hew epic or a set of issues end to end with Orca-managed worktrees and supervised agents — a driver script resolves the ready issues, creates one Orca worktree per issue, starts a supervised implementer running work-issue in it, has a different agent review each delivered PR (or, on a local hew tracker, branch) and fix what it finds in that same session, files every finding as an issue for the paper trail, repairs merge conflicts and red CI, and merges once CI is green (on a local tracker, once the quality gate passes). Runs unattended and stops only for a decision a human has to make. Use whenever the user wants issues worked by orchestrating agents — "work on this epic", "drain epic 12", "work issues 21 22 25", "fix all the bugs in the parser", "orchestrate #42", "pump the epic and merge it". A single issue goes to work-issue unless the user explicitly wants the review-and-merge pipeline around it; never for closing issues or the epic itself.
argument-hint: "Issue numbers, an epic number, or a description of the work (e.g. 'fix all the bugs in the importer'). Flags: --workers N (default 2), --dry-run, --resume"
---

# Work Epic

A set of issues, burned down: the driver script resolves the ready ones, creates an Orca worktree
per issue, starts a supervised implementer in it, has a different agent review each delivered PR
and fix what it finds in the same session, repairs conflicts and red CI, merges on green, and
re-pumps until nothing else can move.

**The coordinator is code.** Every decision the pump makes — which issues spawn, which PRs merge,
what the agents are told, how Orca is driven — lives in [scripts/](scripts/), where it is
tested against a simulated Orca and GitHub. This skill's job is the part that needs a person's
judgement: settle the scope with the user, launch the driver, and relay what it reports. Do not
re-implement the loop by hand; if the driver cannot do something, stop and say so.

**The roles are separate agents.** The implementer runs `work-issue`; the reviewer runs
`review-code` and `review-tests` in turn and fixes what each found; the repairer resolves a conflict or a red CI run. The
reviewer is a **different** agent from the implementer — a second point of view — and the fix
happens in the session that found the problem, so nothing is re-reviewed: a PR is reviewed once.
The agents are one block in [scripts/pump.py](scripts/pump.py) (`IMPLEMENTER`, `REVIEWER`,
`REPAIRER`). Changing one is a source edit in a checkout of this plugin: an installed copy is a
cache that updates overwrite, so if the user asks for different agents, say that instead of
patching the cache.

> [!IMPORTANT]
> [REFERENCE.md](REFERENCE.md) carries the driver's module map, state file, card states, the
> scripts' output schemas, the comment and filing formats, and the Orca command shapes it relies
> on. Read it before debugging a run.

## Prerequisites

The driver proves most of these itself and stops with a clear message; check them first when
something fails to start.

- Run from inside an Orca-managed terminal and worktree of the target repository, with the runtime
  ready. Prefer a dedicated worktree for the coordinator: the driver writes a `.work-epic/`
  scratch directory and its status card there. The repository must be registered in Orca.
- `hew` on PATH and `uv`. On GitHub, `hew` must be authenticated (`gh auth login`) and `gh`
  installed, and the repository needs CI: a PR with no checks reads as "CI has not reported yet"
  and is never merged.
- On a **local hew tracker** (`hew init --local`, no `--repo`) none of GitHub applies: no `gh`, no
  authentication, no remote, no CI. The driver detects this itself; see
  [Local trackers](#local-trackers).
- The `work-issue` skill (hew plugin) installed for the implementer and `review-code` and `review-tests` (critique
  plugin) for the reviewer. The driver finds the work-issue and raise-issues skill directories in
  the repo layout or the plugin cache; set `WORK_EPIC_WORK_ISSUE_DIR` or
  `WORK_EPIC_RAISE_ISSUES_DIR` to override.
- The Orca CLI: `ORCA_CLI_COMMAND`, else the first of `orca-dev`, `orca-ide`, `orca` on PATH.

## Steps

### 1. Settle the scope

The scope is one or more issue numbers (`21 22 25`, `#21,#22`), an epic number (which stands for
its children), or a description of the work.

A description ("fix all the bugs in the importer") is turned into issue numbers read-only, then
confirmed before anything runs:

- Search with `hew search <terms>` and `hew list --label <area>` — never `hew triage`.
- Show the user the candidate list (number, title, priority, type, blockers) and what the run will
  do with it: implement each, review each with a different agent, fix what the review finds, merge
  on CI green (on a local tracker, once the quality gate passes).
- Wait for the go-ahead. They may drop or add issues. The confirmed list is the scope for the whole
  run; the driver never widens it. Issue-number and epic scopes are explicit and need no
  confirmation.

### 2. Launch the driver

```bash
uv run --no-project "<this skill's directory>/scripts/work_epic.py" <scope> [--workers N] [--resume] [--dry-run]
# add --repo owner/name only if the user said so
```

- **`--workers N`** — live implementers and repairers, default 2. More than 3–4 only moves the
  bottleneck into review and merge. Open PRs plus workers in flight never exceed twice this.
- **`--dry-run`** — read-only: which issues would start and what the merge pass would do. Creates
  nothing, needs no Orca. Offer it before a first run on a large scope.
- **`--resume`** — rebind the previous Run for this scope and adopt its state after a crash.
  Explicit instruction only.

There are no other flags. Merges are squash, conflicts and red CI are repaired, and a P1 the
reviewer could not fix holds its PR — these are the policy, not options.

Run it in the coordinator's terminal and let it run to its exit: a scope can take hours, and it
logs what it starts, parks, and merges as it goes. Many harnesses cap how long one command may run
(ten minutes is common), and a driver killed by that cap leaves workers settling with nobody to
reap them. So start it the way your harness runs a long command to completion — a background or
detached run that reports its exit — and wait for that exit instead of polling its output. If it
was cut off anyway, tell the user; `--resume` is theirs to ask for. Do not start a second driver
over the same issues (two pumps on one `gh` login only meet at `hew start`'s claim lock).

### 3. Relay the report

The driver prints a report and writes `report.json` beside its state. Pass the report on, with the
decisions needed first. Exit codes:

| exit | meaning | what to tell the user |
|---|---|---|
| 0 | drained — every issue in scope is done | Say so. If the scope was an epic and every child is closed, suggest a human close the epic; never close it here |
| 10 | stopped — decisions are parked for a human | List each decision with its PR/worktree; say which items the pump finished and which it left |
| 3 | halted on an outage (auth, a worker error, an unreadable Orca) | Say what failed; nothing new was started; `--resume` after it is fixed |
| 1 | could not start | Relay the message (Orca not ready, ambiguous resume, missing skill) |
| 2 | usage error | Relay the usage line |

A parked item is a decision, not a failure: its worktree card is marked unread with the gate in the
comment, and the pump kept draining everything independent of it.

## Local trackers

When hew keeps the issues in a local `.hew`, the driver runs the same loop without GitHub. There is
no flag; hew's own backend rule picks the mode, and `--repo` always means GitHub.

- A delivery is a committed, unpushed branch, not a PR. The reviewer reviews and fixes it in the
  issue's worktree, and findings are filed as issues exactly as before.
- There is no CI, so the quality gate is the check. The reviewer's gate covers a branch that is
  current with the default branch. After any merge, every other branch is behind, and the repairer
  merges the default branch in and runs the gate on the result before that branch's turn. That
  costs one agent run per merge after the first; nothing merges on a gate that has not seen the
  current default branch.
- The merge is a squash into the local default branch **in the main checkout**, so the pump needs
  that checkout on the default branch with a clean tree, and parks the merge as a decision until it
  is. It writes into your checkout: do not leave work uncommitted there while a run is merging.
- The merge closes the issue (`hew close --completed`), which is what a `Fixes #n` does on GitHub
  and what unblocks dependents.
- `--resume` rebinds the Run and adopts its state as before, but does not re-offer a bare claim: a
  local tracker names claimants per worktree, so the pump knows its own claims only from its state.
  A claim the state never recorded (a crash between `hew start` and the pump saving it) is skipped
  as claimed by someone else; `hew release <n> --force` it (its owner is a worktree, not you), then run again.
- Branch protection does not exist here, so the *Protected* stop below cannot occur.
- The implementer is told the tracker is local: the delivery is the committed branch, never
  `hew pr` or a push, and the issue stays claimed until the merge closes it.

## What stops the pump

The driver halts for a human only when one of these is true, and says which in the report:

1. **An unfixed P1.** The reviewer could not fix a finding at P1 or above safely — it needs a
   design decision, reaches outside the PR, or its fix failed the gate. It is filed open; the open
   finding holds the PR until the human closes it (after fixing the branch, or accepting the
   finding as is).
2. **A repair that failed twice.** A conflict or a red CI run two repair attempts could not resolve
   — or a failure the repairer found is not the PR's fault (a flaky test, main already red).
3. **An outage.** Un-auth, a worker that errored, an unreadable Orca or tracker.
4. **A review that did not run.** A missing or unreadable outcome, or a status other than
   `reviewed`: an unreviewed PR is never merged and never read as clean.
5. **Protected.** Branch protection wants something the pump cannot give, typically an approving
   review.

Items 1, 2, 4 and 5 *park* the item; only an outage (3) halts the whole pump. A worker parked on a
permission dialog or a question only a human can answer is also reported and **never answered**.

## Hard gates

The driver never closes an issue or an epic (the exceptions: a review finding it filed after the
reviewer fixed it, and, on a local tracker, the issue a merge delivers), never force-claims,
never answers a worker's dialog or question, never edits code itself, never merges a PR (on a
local tracker, a branch) outside the scope it was given, never passes `--admin`, and never
force-pushes. Autonomous mode moved the *merge* off the human's list, not the judgement.

## Anti-patterns

- Driving Orca by hand (`worker-start`, `check`, `worktree set`) alongside a running driver, or
  re-implementing a step the driver owns. Fix the driver and its tests instead
- Spawning for an issue the resolver skipped; the claim is the authority, never `--force`
- Widening the scope mid-run, or working a finding issue the run filed: findings are fixed by the
  reviewer in its own session or handed to the human
- Re-reviewing a PR (or local branch) whose head moved. A delivery is reviewed once; the gate and
  CI check what lands after
- Closing an open finding to lift a hold — only the human lifts a hold
- Closing the epic because its children are done — report it; a human closes it
