---
name: finish-issue
description: Finish a delivered hew issue after implementation — review its draft PR or committed local branch, verify it against the current default branch, then squash merge and confirm issue closure. Use when asked to finish, land, merge, or wrap up a hew issue, including local trackers with no GitHub. Use work-issue to implement an issue that has no delivery yet; work-epic already finishes deliveries inside its own orchestration loop.
---

# Finish Issue

Take an existing delivery through review, verification, merge, and confirmed tracker closure.
`work-issue` ends at a draft PR or committed local branch; this skill starts there. A merge
request authorizes that finish. A request only to review or inspect does not authorize merging.

Use the session's `hew prime` primer, or run it when absent. Consult `hew <command> --help` and
`gh <command> --help` for the installed command surface rather than guessing flags. Detect the
backend with **`hew config --json`**: `backend` decides the path, not whether a remote exists.
Carry an explicit repository selection consistently through every hew and gh call.

## Inputs and target

- **`<n>`** names a hew issue, not a PR number. Without it, infer only when the current branch
  and tracker unambiguously identify one delivery. Otherwise request the missing target.
- **`--dry-run`** resolves and inspects the delivery and reports what would happen; no edits,
  review comments, branch updates, merge, or tracker writes.
- **`--non-interactive`** asks nothing; report a blocked outcome when a decision is needed.
- **`--json <path>`** also writes the outcome below. Do not commit this report.

Read the issue and its acceptance checklist. Refuse an epic: finish a delivered child instead.
Find exactly one linked PR on GitHub, or one committed issue branch locally; use an explicit
branch supplied by the user if needed. Resolve ambiguity before writing. A delivery already
merged is a recovery case, not permission to implement or squash it again.

Preserve unrelated work. Use the repository's checkout/worktree conventions; inspect where
branches are checked out before switching. A dirty checkout cannot take an update or merge.
Never clear a claim, force-push, bypass branch protection, or silently change the merge base.

## Review and verify

1. Read the issue contract and delivery diff. Use an existing completed review when its evidence
   is available; otherwise review the change now with the relevant installed review skill, or
   inspect it directly. A green test run alone is not review. Respect the repository's approval
   requirements. An agent review cannot supply a required human approval.
2. Resolve findings inside this issue's scope, then rerun the checks. File unrelated work using
   the primer's dedup-before-filing workflow. Open P0/P1 findings hold the delivery; also hold
   anything the repository treats as blocking. A fix needing a design decision is `blocked`.
   Bounded repairs are allowed: after two failed repair attempts, report the remaining failure
   instead of looping. Do not weaken checks to land the change.
3. Bring the issue branch current with the actual target default branch, preserving its commits
   by merging the base in. Resolve conflicts in the issue checkout. Recheck the acceptance
   checklist and run **every check the project gates on**: find commands and scope in CI, task
   runners, package scripts, pre-commit config, and project instructions. Record commands and
   results. A missing tool, unavailable credential, skipped required check, or baseline failure
   is not a pass for finishing; report the unmet gate and stop before merging.
4. Record the full issue head and base commit IDs. Evidence applies to that head/base pair.
   After a code repair or base update, rerun affected verification and obtain CI on the new
   head. Honour any repository rule requiring another review. Re-read state before landing;
   if it changed, return to verification.

The backend-specific steps are essential; read only the relevant reference:

- **GitHub:** [references/github.md](references/github.md) — draft handling, CI, exact-head
  squash, merge queues, and closure confirmation.
- **Local:** [references/local.md](references/local.md) — default checkout, verified squash,
  and durable recovery with the bundled `scripts/land_local.py` helper.

## Report the observed result

Report the issue, branch/PR, review evidence, checks, merged commit, and issue state. Distinguish
an accepted merge request from an observed merge. Say what remains when unfinished.

With `--json`, write an object shaped like:

```json
{
  "skill": "finish-issue",
  "status": "finished",
  "backend": "local",
  "issue": 42,
  "branch": "fix/42-example",
  "pr": null,
  "head": "full verified issue commit ID",
  "base_head": "full verified base commit ID",
  "commit": "observed squash commit ID",
  "review": "review evidence",
  "gate": "commands and results",
  "issue_state": "closed"
}
```

Statuses: `finished` only after merge **and** closure are confirmed; `planned` for a dry run;
`blocked` for a missing decision, review, gate, delivery, or permission; `pending` for CI or a
merge queue still running; `closure_pending` when the commit landed but closure is unconfirmed;
`failed` for an operational error. Include `reason` whenever status is not `finished`, and
`commit: null` until a merge is observed. A waiting run has a finite budget (15 minutes by
default); use short polls so it stays interruptible, then report `pending`. A retry must inspect
the existing merge/receipt first. Neither pending status means the issue is finished.
