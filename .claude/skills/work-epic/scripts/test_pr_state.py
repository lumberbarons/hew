"""Decision-table tests for pr_state.plan_pr and the merge queue.

Run with `python3 -m unittest discover plugins/work-epic/skills/work-epic/scripts`
— stdlib only, no network: plan_pr and queue_merges are pure over gh/hew JSON.
"""

import unittest

from pr_state import (
    REPAIR_FAIL_MARKER,
    REVIEW_MARKER,
    check_rollup_state,
    findings_by_issue,
    parse_args,
    plan_pr,
    queue_merges,
    queue_repairs,
    schedule,
)

HEAD = "9f2c" + "0" * 36
MOVED = "1d7e" + "0" * 36
GREEN = [{"status": "COMPLETED", "conclusion": "SUCCESS"}]
PENDING = [{"status": "IN_PROGRESS", "conclusion": ""}]
FAILING = [{"status": "COMPLETED", "conclusion": "FAILURE"}]


def round_comment():
    return {"body": f"{REVIEW_MARKER}:\n\nclean"}


def fail_comment(reason="conflict in pkg/a.go"):
    return {"body": f"{REPAIR_FAIL_MARKER}:\n\n{reason}"}


def pr(draft=False, merge_state="CLEAN", mergeable="MERGEABLE", rollup=GREEN,
       comments=None, head=HEAD, behind=0, number=34):
    return {
        "number": number,
        "headRefName": "fix/21-child",
        "headRefOid": head,
        "isDraft": draft,
        "mergeStateStatus": merge_state,
        "mergeable": mergeable,
        "statusCheckRollup": rollup,
        "comments": [round_comment()] if comments is None else comments,
        "behindBy": behind,
    }


def action(p, findings=(), issue_state="open", reviewing=False):
    return plan_pr(p, 21, issue_state, list(findings), reviewing)["action"]


class ReviewOnce(unittest.TestCase):
    def test_no_round_waits(self):
        self.assertEqual(action(pr(draft=True, comments=[])), "wait")

    def test_readied_pr_without_round_still_waits(self):
        self.assertEqual(action(pr(comments=[])), "wait")

    def test_a_round_is_the_whole_review_state(self):
        # the reviewer fixed what it could in-session; nothing reviews again
        self.assertEqual(action(pr(draft=True)), "mark_ready")

    def test_a_moved_head_is_not_re_reviewed(self):
        # a fix, a reconcile, or a human edit after the round changes no verdict
        self.assertEqual(action(pr(draft=True, head=MOVED)), "mark_ready")
        self.assertEqual(action(pr(head=MOVED)), "merge")

    def test_unreadable_comments_wait(self):
        p = pr()
        p["comments"] = None
        self.assertEqual(action(p), "wait")

    def test_live_reviewer_parks_the_pr(self):
        # an older round comment must not read as the in-flight review being done
        self.assertEqual(action(pr(), reviewing=True), "wait")

    def test_live_reviewer_parks_a_conflict_too(self):
        self.assertEqual(action(pr(merge_state="DIRTY"), reviewing=True), "wait")

    def test_report_says_reviewed(self):
        self.assertTrue(plan_pr(pr(), 21, "open", [])["reviewed"])
        self.assertFalse(plan_pr(pr(comments=[]), 21, "open", [])["reviewed"])


class Holds(unittest.TestCase):
    def test_unfixed_p1_holds(self):
        self.assertEqual(action(pr(), [{"priority": "P1"}]), "hold")

    def test_unfixed_p2_is_backlog_not_a_hold(self):
        self.assertEqual(action(pr(), [{"priority": "P2"}]), "merge")

    def test_there_is_no_second_round_to_escalate_to(self):
        planned = plan_pr(pr(draft=True), 21, "open", [{"priority": "P1"}])
        self.assertEqual(planned["action"], "hold")

    def test_held_pr_is_not_branch_updated(self):
        p = pr(draft=True, merge_state="BEHIND")
        self.assertEqual(action(p, [{"priority": "P1"}]), "hold")

    def test_held_pr_with_a_conflict_is_reported_not_repaired(self):
        # the human gate owns a held PR
        p = pr(draft=True, merge_state="DIRTY")
        self.assertEqual(action(p, [{"priority": "P1"}]), "conflict")

    def test_closing_the_finding_lifts_the_hold_without_a_re_review(self):
        self.assertEqual(action(pr(draft=True, head=MOVED), []), "mark_ready")

    def test_findings_are_keyed_by_the_reviewed_issue(self):
        issues = [
            {"number": 40, "state": "open", "priority": "P1",
             "body": "review-key: code/x/a.go\nreview-of: #21 (PR #34)"},
            {"number": 41, "state": "closed", "priority": "P1",
             "body": "review-of: #21 (PR #34)\nfixed-in: 9f2c1ab"},
            {"number": 42, "state": "open", "priority": "P2", "body": "no marker"},
        ]
        self.assertEqual([c["number"] for c in findings_by_issue(issues)[21]], [40])


class RepairConflicts(unittest.TestCase):
    def test_a_conflict_is_repaired(self):
        self.assertEqual(action(pr(merge_state="DIRTY")), "repair")

    def test_mergeable_conflicting_is_repaired_even_on_a_draft(self):
        # GitHub masks a draft's mergeState but not its mergeable verdict
        p = pr(draft=True, merge_state="DRAFT", mergeable="CONFLICTING")
        self.assertEqual(action(p), "repair")

    def test_one_failed_attempt_still_repairs(self):
        p = pr(merge_state="DIRTY", comments=[round_comment(), fail_comment()])
        self.assertEqual(action(p), "repair")

    def test_two_failed_attempts_park_it(self):
        p = pr(merge_state="DIRTY",
               comments=[round_comment(), fail_comment(), fail_comment()])
        self.assertEqual(action(p), "conflict")

    def test_failures_are_reported(self):
        planned = plan_pr(pr(merge_state="DIRTY", comments=[fail_comment()]),
                          21, "open", [])
        self.assertEqual(planned["repairFailures"], 1)

    def test_unreadable_comments_never_repair(self):
        # the attempt count lives in the comments; unknown is not zero
        p = pr(merge_state="DIRTY")
        p["comments"] = None
        self.assertEqual(action(p), "wait")


class RepairRedCi(unittest.TestCase):
    def test_failing_checks_on_a_reviewed_pr_are_repaired(self):
        self.assertEqual(action(pr(rollup=FAILING)), "repair")

    def test_a_draft_with_failing_checks_is_repaired_too(self):
        self.assertEqual(action(pr(draft=True, rollup=FAILING)), "repair")

    def test_failing_checks_before_the_review_wait_for_the_review(self):
        self.assertEqual(action(pr(draft=True, rollup=FAILING, comments=[])), "wait")

    def test_attempts_are_shared_with_conflicts(self):
        # two strikes in total, whatever the cause
        p = pr(rollup=FAILING,
               comments=[round_comment(), fail_comment(), fail_comment()])
        self.assertEqual(action(p), "ci_failed")

    def test_a_blocker_is_not_repaired_around(self):
        self.assertEqual(action(pr(rollup=FAILING), [{"priority": "P1"}]), "hold")


class RepairQueue(unittest.TestCase):
    def repaired(self, number, **kw):
        planned = plan_pr(pr(number=number, merge_state="DIRTY", **kw), 21,
                          "open", [])
        self.assertEqual(planned["action"], "repair")
        return planned

    def test_one_repairer_at_a_time(self):
        head = queue_repairs([self.repaired(35), self.repaired(34)], None)
        self.assertEqual(head, 34)

    def test_siblings_queue_behind_the_repair_head(self):
        prs = [self.repaired(34), self.repaired(35)]
        queue_repairs(prs, None)
        self.assertEqual([(p["number"], p["action"]) for p in prs],
                         [(34, "repair"), (35, "queued")])

    def test_no_repair_while_main_is_moving(self):
        # a merge landing mid-repair would re-conflict it
        prs = [self.repaired(34)]
        self.assertIsNone(queue_repairs(prs, 99))
        self.assertEqual(prs[0]["action"], "queued")

    def test_live_repairer_keeps_the_head(self):
        prs = [self.repaired(36), self.repaired(34)]
        self.assertEqual(schedule(prs, {36}), (None, 36))
        self.assertEqual([(p["number"], p["action"]) for p in prs],
                         [(36, "wait"), (34, "queued")])

    def test_live_repairer_holds_the_merge_queue(self):
        ready = plan_pr(pr(number=35), 22, "open", [])
        self.assertEqual(ready["action"], "merge")
        prs = [self.repaired(36), ready]
        self.assertEqual(schedule(prs, {36}), (None, 36))
        self.assertEqual(ready["action"], "queued")

    def test_live_repairer_waits_whatever_github_says(self):
        # pushed but not yet reaped: the pump reads the outcome first
        pushed = plan_pr(pr(number=36, head=MOVED), 21, "open", [])
        self.assertEqual(pushed["action"], "merge")
        schedule([pushed], {36})
        self.assertEqual(pushed["action"], "wait")

    def test_merges_go_before_repairs_without_a_live_repairer(self):
        ready = plan_pr(pr(number=35), 22, "open", [])
        prs = [self.repaired(34), ready]
        self.assertEqual(schedule(prs, set()), (35, None))
        self.assertEqual(prs[0]["action"], "queued")


class Freshness(unittest.TestCase):
    def test_behind_without_strict_protection_is_updated_not_merged(self):
        # without "require up to date" GitHub reports a stale PR as CLEAN
        self.assertEqual(action(pr(merge_state="CLEAN", behind=3)), "update_branch")

    def test_behind_draft_is_readied_not_updated(self):
        p = pr(draft=True, merge_state="DRAFT", behind=3)
        self.assertEqual(action(p), "mark_ready")

    def test_unknown_behind_count_never_merges(self):
        self.assertEqual(action(pr(behind=None)), "wait")

    def test_current_pr_with_running_ci_is_in_flight(self):
        planned = plan_pr(pr(rollup=PENDING), 21, "open", [])
        self.assertEqual((planned["action"], planned.get("inFlight")), ("wait", True))

    def test_failing_pr_is_not_in_flight(self):
        planned = plan_pr(pr(rollup=FAILING), 21, "open", [])
        self.assertIsNone(planned.get("inFlight"))


def planned(number, **kw):
    return plan_pr(pr(number=number, **kw), 21, "open", [])


class Queue(unittest.TestCase):
    def actions(self, prs):
        head = queue_merges(prs)
        return head, {p["number"]: p["action"] for p in prs}

    def test_one_merge_per_plan(self):
        # merging both lands the second on CI that never saw the first
        got = self.actions([planned(35), planned(34)])
        self.assertEqual(got, (34, {34: "merge", 35: "queued"}))

    def test_merge_queues_every_update(self):
        got = self.actions([planned(34, behind=1), planned(35)])
        self.assertEqual(got, (35, {34: "queued", 35: "merge"}))

    def test_running_ci_holds_the_queue(self):
        got = self.actions([planned(34, rollup=PENDING), planned(35, behind=2)])
        self.assertEqual(got, (34, {34: "wait", 35: "queued"}))

    def test_failing_pr_does_not_hold_the_queue(self):
        got = self.actions([planned(34, rollup=FAILING), planned(35, behind=2)])
        self.assertEqual(got, (35, {34: "repair", 35: "update_branch"}))

    def test_update_prefers_passing_checks_then_oldest(self):
        got = self.actions([planned(34, behind=1, rollup=FAILING),
                            planned(36, behind=1), planned(35, behind=1)])
        self.assertEqual(got, (35, {34: "queued", 35: "update_branch", 36: "queued"}))

    def test_held_and_draft_prs_are_outside_the_queue(self):
        held = plan_pr(pr(number=34, behind=1), 21, "open", [{"priority": "P1"}])
        got = self.actions([held, planned(35, draft=True, merge_state="DRAFT"),
                            planned(36, behind=1)])
        self.assertEqual(got, (36, {34: "hold", 35: "mark_ready", 36: "update_branch"}))

    def test_nothing_moving(self):
        self.assertEqual(self.actions([planned(34, rollup=PENDING, draft=True)]),
                         (None, {34: "wait"}))


class Checks(unittest.TestCase):
    def test_no_checks_wait(self):
        # a repo without CI is not supported: no checks reads as "not reported yet"
        self.assertEqual(action(pr(rollup=[])), "wait")

    def test_rollup_collapse(self):
        self.assertEqual(check_rollup_state([]), "none")
        self.assertEqual(check_rollup_state([{"state": "PENDING"}] + GREEN), "pending")
        self.assertEqual(check_rollup_state([{"state": "ERROR"}] + GREEN), "fail")


class MergeState(unittest.TestCase):
    def test_draft_is_readied_not_merged(self):
        # GitHub reports DRAFT for a draft, masking BEHIND/BLOCKED
        self.assertEqual(action(pr(draft=True, merge_state="DRAFT")), "mark_ready")

    def test_blocked_with_green_checks_is_protected(self):
        self.assertEqual(action(pr(merge_state="BLOCKED")), "protected")

    def test_unknown_waits(self):
        self.assertEqual(action(pr(merge_state="UNKNOWN")), "wait")

    def test_closed_issue_waits(self):
        self.assertEqual(action(pr(), issue_state="closed"), "wait")


class Args(unittest.TestCase):
    def test_flags_after_the_scope(self):
        self.assertEqual(parse_args(["12", "--repo", "o/r"]),
                         ([12], "o/r", frozenset(), frozenset()))

    def test_flags_before_the_scope(self):
        self.assertEqual(parse_args(["--repo", "o/r", "12"]),
                         ([12], "o/r", frozenset(), frozenset()))

    def test_a_set_of_issues(self):
        self.assertEqual(parse_args(["12", "13,14", "#15"])[0], [12, 13, 14, 15])

    def test_agents_flags_are_repeatable(self):
        # one per live wr-<pr> / wc-<pr> agent
        parsed = parse_args(["12", "--reviewing", "34", "--repairing", "36",
                             "--repairing", "40"])
        self.assertEqual(parsed[2:], (frozenset({34}), frozenset({36, 40})))

    def test_agent_flags_need_a_pr_number(self):
        with self.assertRaises(SystemExit) as e:
            parse_args(["12", "--repairing", "wc-36"])
        self.assertEqual(e.exception.code, 2)

    def test_removed_flags_are_not_silently_accepted_as_scope(self):
        with self.assertRaises(SystemExit) as e:
            parse_args(["12", "--block-on", "P2"])
        self.assertEqual(e.exception.code, 2)


if __name__ == "__main__":
    unittest.main()
