"""The local-mode planners: spawn and merge decisions over a tracker with no GitHub.

When hew keeps the issues in a local `.hew` (repo_state.local_tracker), nothing is a
PR: a delivery is a committed, unpushed branch, and "merged" means squashed into the
local default branch. The pump runs the same loop over it, and these two functions
stand in for resolve_ready.resolve and pr_state.plan. They return the same shapes, so
the pump does not branch on the planners — only on the effects.

A delivery's id is its issue number, which is how every key the pump already builds
(`wr-<id>.json`, `pr<id>:hold`, the reviewing and repairing sets) keeps working.

What GitHub knew that a local repository does not, the pump's own state supplies:

  reviewed        the issue's state is `reviewed` — no comment marker to read
  verified_head   the head the quality gate last passed on (the reviewer's, or a
                  repairer's); there is no CI, so "checks pass" means this equals
                  the branch head
  repair_failures failed repair attempts, counted by the pump instead of read from
                  comments

and git supplies the rest, read-only: the branch head, how far behind the default
branch it is, and whether merging it would conflict (`git merge-tree`).

The merge decision table is plan_branch, in decision order (first match wins):

  wait            issue closed, or a reviewer is in flight, or the branch cannot
                  be read, or it has no review round yet
  repair          merging it into the default branch would conflict, and fewer than
                  two attempts failed — a repairer merges the default branch in
  conflict        the same, but a blocker is open or the attempts are spent
  hold            an unfixed blocking finding is open — human gate
  repair          behind the default branch, or the gate has not passed on this
                  exact head, and fewer than two attempts failed — a repairer
                  merges the default branch in and runs the gate
  ci_failed       the same, but the attempts are spent — human gate
  merge           reviewed, current, verified, nothing open at P1 or above

pr_state.schedule then lets at most one branch move toward the default branch per
pass and freezes everything behind a live repairer, exactly as it does for PRs. Every
merge moves the default branch, so every other delivery is behind and gets a gate run
on its merged result before its turn: that is the local stand-in for CI.
"""

import json

import resolve_ready
from pr_state import (
    BLOCKING,
    MAX_REPAIR_ATTEMPTS,
    findings_by_issue,
    schedule,
    severity,
)
from repo_state import (
    CommandError,
    branch_head,
    commits_behind,
    conflicts_with,
    local_default_branch,
    main_checkout,
    run,
    scope_issues,
)

DELIVERED = ("delivered", "reviewing", "reviewed", "review_failed")  # a branch awaits its merge
IN_FLIGHT = ("implementing",)  # an implementer holds the claim


def default_or_fail():
    default = local_default_branch()
    if default is None:
        raise CommandError("cannot determine the default branch: no origin/HEAD, main, or master")
    return default


def resolve_over(scope, children, default, issues, workers):
    """resolve_ready.resolve for a local tracker, over `children` already read.

    `issues` is the pump's issue table. Claimants are told apart per worktree here, so a
    claim is the run's own exactly when the pump tracks the issue as in flight or
    delivered; resuming a bare claim is not offered. A delivered branch counts toward the
    backlog the way an open PR does.
    """
    delivered = {int(n): int(n) for n, i in issues.items() if i.get("state") in DELIVERED}
    mine = delivered.keys() | {int(n) for n, i in issues.items() if i.get("state") in IN_FLIGHT}
    out = resolve_ready.assemble(scope, children, default, workers, me=None,
                                 pr_by_issue=delivered, branch_by_issue={}, resume=False,
                                 mine=mine)
    for s in out["skipped"]:
        if s["reason"] == "in_review":
            s["detail"] = "branch delivered, awaiting merge"
    return out


def resolve(scope, issues, workers):
    return resolve_over(scope, scope_issues(scope), default_or_fail(), issues, workers)


def branch_facts(default, branch):
    """What git says about a delivered branch; None when it cannot be read.

    Unknown pieces stay None inside the dict so plan_branch can refuse to act on them.
    """
    head = branch_head(branch)
    if head is None:
        return None
    return {"head": head, "behind": commits_behind(default, head),
            "conflicts": conflicts_with(default, head)}


def plan_branch(n, iss, issue_state, findings, facts, reviewing=False):
    """Decision table for one delivered branch. See the module docstring."""
    reviewed = iss.get("state") == "reviewed"
    failures = iss.get("repair_failures", 0)
    worst = min(findings, key=severity).get("priority") if findings else None
    blocking = worst is not None and severity({"priority": worst}) <= BLOCKING

    known = bool(facts) and facts["behind"] is not None and facts["conflicts"] is not None
    head = facts["head"] if facts else ""
    behind = (facts or {}).get("behind")
    conflicts = (facts or {}).get("conflicts")
    verified = known and iss.get("verified_head") == head

    base = {
        "number": n,
        "issue": n,
        "issueState": issue_state,
        "branch": iss.get("branch"),
        "head": head,
        "draft": False,
        "mergeState": "DIRTY" if conflicts else "BEHIND" if behind else "CLEAN",
        "mergeable": "CONFLICTING" if conflicts else "MERGEABLE",
        "checks": "pass" if verified else "none",
        "behindBy": behind,
        "reviewed": reviewed,
        "repairFailures": failures,
        "worstOpenFinding": worst,
    }

    def act(action, note=None):
        base["action"] = action
        if note:
            base["note"] = note
        return base

    if issue_state != "open":
        return act("wait", "issue closed while its branch is unmerged")
    if reviewing:
        return act("wait", "reviewer in flight")
    if not known:
        return act("wait", "branch state unreadable — not known to merge cleanly")
    if not reviewed:
        return act("wait", "no review round yet — reviewer in flight or failed")
    can_repair = not blocking and failures < MAX_REPAIR_ATTEMPTS
    if conflicts:
        return act("repair", "conflicts with the default branch") if can_repair else act("conflict")
    if blocking:
        return act("hold", f"open {worst} finding the reviewer could not fix")
    if behind or not verified:
        why = (f"{behind} commits behind the default branch" if behind
               else "the gate has not passed on this head")
        if failures < MAX_REPAIR_ATTEMPTS:
            return act("repair", why)
        return act("ci_failed", f"{why}, repair attempts spent")
    return act("merge")


def plan_over(scope, default, children, findings, issues, facts_for, reviewing=frozenset(),
              repairing=frozenset()):
    """pr_state.plan for a local tracker: one record per delivered branch in scope.

    `findings` is findings_by_issue over every open issue and `facts_for(branch)` is
    branch_facts for the default branch — both passed in so the whole table runs without git.
    """
    child_by_number = {c["number"]: c for c in children}
    planned = []
    for key, iss in issues.items():
        n = int(key)
        if iss.get("state") not in DELIVERED or n not in child_by_number or not iss.get("branch"):
            continue
        planned.append(plan_branch(
            n, iss, child_by_number[n].get("state", "open"), findings.get(n) or [],
            facts_for(iss["branch"]), n in reviewing))
    queue_head, repair_head = schedule(planned, repairing)
    counts = {}
    for p in planned:
        counts[p["action"]] = counts.get(p["action"], 0) + 1
    planned.sort(key=lambda p: p["number"])
    return {"scope": list(scope), "defaultBranch": default, "mainCheckout": main_checkout(),
            "queueHead": queue_head, "repairHead": repair_head, "prs": planned,
            "unmatched": [], "counts": counts}


def plan(scope, issues, reviewing=frozenset(), repairing=frozenset()):
    default = default_or_fail()
    open_issues = [json.loads(line) for line in run(["hew", "list", "--json", "--bodies"]).splitlines()
                   if line.strip()]
    return plan_over(scope, default, scope_issues(scope), findings_by_issue(open_issues), issues,
                     lambda branch: branch_facts(default, branch), reviewing, repairing)


def resolver(state):
    """The pump's resolve_fn over a local tracker, reading the pump's issue table."""
    return lambda scope, repo, resume, workers: resolve(scope, state["issues"], workers)


def planner(state):
    """The pump's plan_fn over a local tracker, reading the pump's issue table."""
    return lambda scope, repo, reviewing=frozenset(), repairing=frozenset(): plan(
        scope, state["issues"], reviewing, repairing)
