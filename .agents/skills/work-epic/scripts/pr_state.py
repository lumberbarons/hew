#!/usr/bin/env python3
"""Plan the merge pass for a run's open PRs — read-only, decide nothing.

Runs `hew show` (and `hew list --epic` for an epic), `hew list --bodies` once
for the open findings, `gh pr list`, and one `gh pr view` and one compare call
per matched PR; never writes. The scope is one or more issue numbers; an epic
number stands for its children. Prints one JSON object to stdout:

  {
    "scope": [<n>, ...], "defaultBranch": "main",
    "mainCheckout": "/abs/path",
    "queueHead": <pr number or null>,
    "repairHead": <pr number or null>,
    "prs": [ { "number", "issue", "issueState", "branch", "head", "draft",
               "mergeState", "mergeable", "checks", "behindBy", "reviewed",
               "repairFailures", "worstOpenFinding", "action", "note" } ... ],
    "unmatched": [ {"number", "scrapedIssue", "reason",
                    "action": "outside_scope"} ... ],
    "counts": { "<action>": n, ... }
  }

A PR is matched to an issue in scope by scraping the conventional issue number
from its head branch (`fix|feat|chore/<n>-<slug>`, work-issue's naming rule).
A PR is reviewed once: the reviewer fixes what it can in the same session and
leaves one PR comment carrying the reviewer-agent marker; that comment is the
whole review state, and nothing re-reviews a head that moves afterwards.
Findings the reviewer could not fix are filed open with a `review-of: #<issue>`
marker in their body; an open one at P1 or above holds the PR. Fixed findings
are filed and closed, so only an unfixed one can hold.

Actions, in decision order (first match wins):
  wait            issue closed, or a reviewer for the PR is in flight, or the
                  PR's comments could not be read
  repair          mergeState DIRTY or mergeable CONFLICTING, no blocker open,
                  and fewer than two failed attempts recorded — a repair
                  agent merges the default branch in and reconciles
  conflict        the same, but a blocker is open or the attempts are spent —
                  escalated to the human, never resolved by hand
  hold            an unfixed blocking finding is open — human gate
  update_branch   ready PR behind the default branch (behindBy > 0, or
                  mergeState BEHIND) — merge the default branch in. Drafts
                  are never updated: they are not next to merge
  wait            no review round yet (reviewer in flight or failed)
  repair          checks failing and fewer than two failed attempts recorded —
                  a repair agent diagnoses the failure and fixes it
  ci_failed       checks failing and the attempts are spent — human gate
  wait            checks pending or absent
  mark_ready      draft and otherwise green — GitHub reports a draft's merge
                  state as DRAFT, hiding BEHIND/BLOCKED until it is readied
  wait            behindBy unknown — never merge a PR not known to be current
  merge           checks green, mergeState CLEAN or HAS_HOOKS
  protected       mergeState BLOCKED with green checks — branch protection
                  wants something the pump cannot give (an approval)
  wait            anything else (UNKNOWN while GitHub computes, UNSTABLE)
  outside_scope   unmatched PR — listed for visibility, never touched

Then the merge queue: at most one PR moves toward main per pass. Every merge
puts every other open PR behind again, so updating them side by side spends a
CI run each on a result the next merge throws away, and merging two from one
plan lands the second on CI that never saw the first. The queue head is, in
order: the lowest-numbered `merge`; else the lowest-numbered ready PR that is
current and waiting on its checks (its CI is the one that counts); else the
`update_branch` PR whose checks last passed, lowest number first. Every other
`merge` and `update_branch` becomes

  queued          waiting its turn behind `queueHead` — no action, and no CI
                  budget: it moves when the head merges or parks

Repairs are scheduled the same way: at most one `repair` survives, only while
`queueHead` is null (a merge landing mid-repair would re-conflict it), the
lowest-numbered candidate; every other candidate becomes `queued` too.

GitHub cannot say a repairer is running, so the pump passes one
`--repairing <pr>` per live `wc-<pr>` agent. While any is live the plan
freezes around it: that PR is `wait`, `repairHead` is the lowest live one,
`queueHead` is null, and every other `merge`, `update_branch`, and `repair`
is `queued` behind it — main does not move and no second repairer starts.
`--reviewing <pr>` (one per live `wr-<pr>` agent) only parks that PR at `wait`:
its review is not complete, whatever older comments say.

Usage: pr_state.py <issue-number>... [--repo owner/name]
                   [--reviewing <pr>]... [--repairing <pr>]...
Exit: 0 ok (prs may be empty) · 1 runtime error · 2 usage error
"""

import json
import re
import sys

from repo_state import (
    CommandError,
    behind_by,
    branch_issue,
    default_branch,
    main_checkout,
    open_prs,
    parse_scope,
    run,
    scope_issues,
)

REVIEW_MARKER = "review-code findings (work-epic reviewer agent)"
REPAIR_FAIL_MARKER = "work-epic repair failed (repair agent)"
MAX_REPAIR_ATTEMPTS = 2
FINDING_MARKER_RE = re.compile(r"review-of: #(\d+)\b")
PRIORITY_RE = re.compile(r"^P(\d+)$")

BLOCKING = 1  # a finding at P1 or above holds its PR
# mergeStateStatus values a ready (non-draft) PR merges from
MERGEABLE_STATES = {"CLEAN", "HAS_HOOKS"}
PR_FIELDS = (
    "number,headRefName,headRefOid,baseRefName,isDraft,mergeStateStatus,"
    "mergeable,statusCheckRollup"
)
USAGE = ("usage: pr_state.py <issue-number>... [--repo owner/name] "
         "[--reviewing <pr>]... [--repairing <pr>]...")

CHECK_OK_CONCLUSIONS = {"SUCCESS", "NEUTRAL", "SKIPPED"}
CHECK_BAD_CONCLUSIONS = {
    "FAILURE",
    "TIMED_OUT",
    "CANCELLED",
    "ACTION_REQUIRED",
    "STARTUP_FAILURE",
}
CONTEXT_BAD_STATES = {"FAILURE", "ERROR"}


def fail(msg, code=1):
    print(f"pr_state: {msg}", file=sys.stderr)
    sys.exit(code)


def check_rollup_state(rollup):
    """Collapse gh's statusCheckRollup into pass | fail | pending | none."""
    if not rollup:
        return "none"
    states = []
    for entry in rollup:
        if "state" in entry:  # legacy StatusContext
            state = entry.get("state", "")
            states.append("fail" if state in CONTEXT_BAD_STATES else
                          "pass" if state == "SUCCESS" else "pending")
            continue
        status = entry.get("status", "")
        conclusion = entry.get("conclusion", "")
        if conclusion in CHECK_BAD_CONCLUSIONS or status == "STARTUP_FAILURE":
            states.append("fail")
        elif status == "COMPLETED" and conclusion in CHECK_OK_CONCLUSIONS:
            states.append("pass")
        else:
            states.append("pending")
    if "fail" in states:
        return "fail"
    if all(s == "pass" for s in states):
        return "pass"
    return "pending"


def severity(item):
    m = PRIORITY_RE.match(item.get("priority") or "")
    return int(m.group(1)) if m else 99


def findings_by_issue(open_issues):
    """Open findings keyed by the issue they were reviewed against.

    The converter stamps each finding's `### Where` with `review-of: #<n>`
    naming the reviewed issue; that marker is the deterministic link — hew's
    JSON carries no discovered-from field. Findings are not children of the
    scope, so the caller passes every open issue.
    """
    by_issue = {}
    for c in open_issues:
        if c.get("state") != "open":
            continue
        for m in FINDING_MARKER_RE.finditer(c.get("body") or ""):
            by_issue.setdefault(int(m.group(1)), []).append(c)
    return by_issue


def plan_pr(pr, issue, issue_state, findings, reviewing=False):
    """Decision table for one matched PR. First match wins.

    `pr["comments"]` is None when the comments could not be read — the review
    and repair-attempt state is then unknown, which is never a reason to act.
    `pr["behindBy"]` is the compare API's count of default-branch commits the
    head lacks, None when unknown. `reviewing` is true while a reviewer agent
    for this PR is live.
    """
    comments = pr.get("comments")
    reviewed = any(REVIEW_MARKER in (c.get("body") or "") for c in comments or [])
    repair_failures = sum(
        1 for c in comments or []
        if REPAIR_FAIL_MARKER in (c.get("body") or "")
    )

    worst = None
    if findings:
        worst = min(findings, key=severity).get("priority")
    blocking = worst is not None and severity({"priority": worst}) <= BLOCKING

    merge_state = pr.get("mergeStateStatus") or "UNKNOWN"
    mergeable = pr.get("mergeable") or "UNKNOWN"
    checks = check_rollup_state(pr.get("statusCheckRollup") or [])
    draft = bool(pr.get("isDraft"))
    behind_count = pr.get("behindBy")
    behind = merge_state == "BEHIND" or (behind_count or 0) > 0

    base = {
        "number": pr["number"],
        "issue": issue,
        "issueState": issue_state,
        "branch": pr.get("headRefName"),
        "head": (pr.get("headRefOid") or "")[:40],
        "draft": draft,
        "mergeState": merge_state,
        "mergeable": mergeable,
        "checks": checks,
        "behindBy": behind_count,
        "reviewed": reviewed,
        "repairFailures": repair_failures,
        "worstOpenFinding": worst,
    }

    def act(action, note=None):
        base["action"] = action
        if note:
            base["note"] = note
        return base

    if issue_state != "open":
        return act("wait", "issue closed while PR open")
    if reviewing:
        return act("wait", "reviewer in flight")
    if comments is None:
        return act("wait", "PR comments unreadable — review state unknown")
    can_repair = not blocking and repair_failures < MAX_REPAIR_ATTEMPTS
    if merge_state == "DIRTY" or mergeable == "CONFLICTING":
        if can_repair:
            return act("repair", "conflict with the default branch")
        return act("conflict")
    if blocking:
        return act("hold", f"open {worst} finding the reviewer could not fix")
    if behind and not draft:
        return act("update_branch", f"{behind_count} commits behind"
                   if behind_count else "mergeState BEHIND")
    if not reviewed:
        return act("wait", "no review round yet — reviewer in flight or failed")
    if checks == "fail":
        if can_repair:
            return act("repair", "checks failing")
        return act("ci_failed", "checks failing, repair attempts spent")
    if checks != "pass":
        # a current, ready PR whose CI is still running (or has not reported
        # yet) is the one the queue waits on
        if not draft and behind_count == 0:
            base["inFlight"] = True
        return act("wait", {"pending": "checks pending",
                            "none": "no checks reported yet"}[checks])
    if draft:
        return act("mark_ready")
    if behind_count is None:
        return act("wait", "behind count unknown — not known to be current")
    if merge_state in MERGEABLE_STATES:
        return act("merge")
    if merge_state == "BLOCKED":
        return act("protected", "branch protection blocks the merge despite green checks")
    return act("wait", f"mergeState {merge_state}")


def queue_merges(prs):
    """Let at most one PR move toward main; queue the rest behind it.

    Mutates the planned PRs in place and returns the head's number, or None
    when nothing is moving. Order rules are in the module docstring.
    """
    merges = [p for p in prs if p["action"] == "merge"]
    in_flight = [p for p in prs if p.get("inFlight")]
    updates = [p for p in prs if p["action"] == "update_branch"]
    if merges:
        head = min(merges, key=lambda p: p["number"])
    elif in_flight:
        head = min(in_flight, key=lambda p: p["number"])
    elif updates:
        head = min(updates, key=lambda p: (p["checks"] != "pass", p["number"]))
    else:
        return None
    for p in merges + updates:
        if p is not head:
            p["action"] = "queued"
            p["note"] = f"queued behind PR #{head['number']}"
    return head["number"]


def queue_repairs(prs, merge_head):
    """Let at most one repairer run, and only while nothing moves toward main.

    A merge landing mid-repair re-conflicts it, and repairs done side by side
    are invalidated by the first merge; so the lowest-numbered `repair`
    survives only when `merge_head` is None, and every other candidate waits
    its turn. Mutates in place and returns the repair head's number, or None
    when no repairer should run.
    """
    candidates = [p for p in prs if p["action"] == "repair"]
    if not candidates:
        return None
    if merge_head is not None:
        for p in candidates:
            p["action"] = "queued"
            p["note"] = f"queued behind PR #{merge_head}"
        return None
    head = min(candidates, key=lambda p: p["number"])
    for p in candidates:
        if p is not head:
            p["action"] = "queued"
            p["note"] = f"queued behind repair of PR #{head['number']}"
    return head["number"]


def schedule(prs, repairing):
    """Order the pass: the merge queue, then repairs — or neither.

    `repairing` is the PR numbers of live `wc-<pr>` agents. While any is
    live, its PR waits on the repairer whatever GitHub reads, and every other
    `merge`, `update_branch`, and `repair` is queued behind the lowest live
    one: a merge would re-conflict the repair, and a second repairer is the
    one the planner exists to prevent. Mutates in place and returns
    `(queueHead, repairHead)`.
    """
    if not repairing:
        merge_head = queue_merges(prs)
        return merge_head, queue_repairs(prs, merge_head)
    head = min(repairing)
    for p in prs:
        if p["number"] in repairing:
            p["action"] = "wait"
            p["note"] = f"repairer wc-{p['number']} in flight"
        elif p["action"] in ("merge", "update_branch", "repair"):
            p["action"] = "queued"
            p["note"] = f"queued behind repair of PR #{head}"
    return None, head


def parse_args(args):
    """Flags in any position — the skill passes them after the issue numbers."""
    args = list(args)
    repo, reviewing, repairing, positional = None, set(), set(), []
    while args:
        a = args.pop(0)
        if a == "--repo":
            if not args:
                fail("--repo needs owner/name", 2)
            repo = args.pop(0)
        elif a in ("--reviewing", "--repairing"):
            if not args or not args[0].isdigit():
                fail(f"{a} needs a PR number", 2)
            (reviewing if a == "--reviewing" else repairing).add(int(args.pop(0)))
        else:
            positional.append(a)
    scope = parse_scope(positional)
    if scope is None:
        fail(USAGE, 2)
    return scope, repo, frozenset(reviewing), frozenset(repairing)


def plan(scope, repo, reviewing=frozenset(), repairing=frozenset()):
    suffix = ["--repo", repo] if repo else []
    gh_suffix = ["-R", repo] if repo else []

    children = scope_issues(scope, repo)
    child_by_number = {c["number"]: c for c in children}
    open_issues = [
        json.loads(line)
        for line in run(["hew", "list", "--json", "--bodies", *suffix]).splitlines()
        if line.strip()
    ]
    findings = findings_by_issue(open_issues)
    default = default_branch(repo)

    prs_out, unmatched = [], []
    for raw in open_prs(PR_FIELDS, repo):
        issue = branch_issue(raw.get("headRefName"))
        reason = None
        if issue is None:
            reason = "no issue number in the head branch"
        elif issue not in child_by_number:
            reason = f"#{issue} is not in this run's scope"
        elif default and raw.get("baseRefName") != default:
            reason = f"targets {raw.get('baseRefName')}, not {default}"
        if reason:
            unmatched.append({"number": raw["number"], "scrapedIssue": issue,
                              "reason": reason, "action": "outside_scope"})
            continue
        try:
            view = json.loads(
                run(["gh", "pr", "view", str(raw["number"]), "--json", "comments",
                     *gh_suffix])
            )
            raw["comments"] = view.get("comments") or []
        except (CommandError, ValueError):
            raw["comments"] = None
        raw["behindBy"] = behind_by(default, raw.get("headRefOid"), repo)
        prs_out.append(
            plan_pr(
                raw,
                issue,
                child_by_number[issue].get("state", "open"),
                findings.get(issue) or [],
                raw["number"] in reviewing,
            )
        )
    queue_head, repair_head = schedule(prs_out, repairing)
    counts = {}
    for p in prs_out:
        counts[p["action"]] = counts.get(p["action"], 0) + 1
    counts["outside_scope"] = len(unmatched)
    prs_out.sort(key=lambda p: (p["issue"], p["number"]))

    return {
        "scope": list(scope),
        "defaultBranch": default,
        "mainCheckout": main_checkout(),
        "queueHead": queue_head,
        "repairHead": repair_head,
        "prs": prs_out,
        "unmatched": sorted(unmatched, key=lambda u: u["number"]),
        "counts": counts,
    }


def main():
    scope, repo, reviewing, repairing = parse_args(sys.argv[1:])
    try:
        out = plan(scope, repo, reviewing, repairing)
    except CommandError as e:
        fail(str(e))
    print(json.dumps(out, indent=2))


if __name__ == "__main__":
    main()
