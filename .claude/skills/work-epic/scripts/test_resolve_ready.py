"""Classification tests for resolve_ready.classify.

Run with `python3 -m unittest discover plugins/work-epic/skills/work-epic/scripts`
— stdlib only, no network: classify is pure over hew JSON plus the PR and
branch indexes.
"""

import unittest
from unittest.mock import patch

from resolve_ready import (
    classify,
    count_by_reason,
    delivery_index,
    parse_args,
    resolve,
    spawn_budget,
)

ME = "orchestrator"


def child(n, **kw):
    c = {"number": n, "title": f"child {n}", "state": "open", "priority": "P2",
         "type": "task", "body": ""}
    c.update(kw)
    return c


def mine(n):
    return child(n, inProgress=True, assignees=[ME])


def reasons(children, prs=(), branches=(), resume=False, me=ME):
    pr_by_issue, branch_by_issue = delivery_index(list(prs), list(branches))
    eligible, skipped, _ = classify(children, me, pr_by_issue, branch_by_issue, resume)
    out = {e["number"]: "eligible" for e in eligible}
    out.update({s["number"]: s["reason"] for s in skipped})
    return out


class OwnClaims(unittest.TestCase):
    def test_delivered_child_is_in_review(self):
        prs = [{"number": 34, "headRefName": "fix/21-child"}]
        self.assertEqual(reasons([mine(21)], prs=prs), {21: "in_review"})

    def test_open_pr_wins_over_other_claims(self):
        prs = [{"number": 34, "headRefName": "feat/21-child"}]
        c = child(21, inProgress=True, assignees=["someone"])
        self.assertEqual(reasons([c], prs=prs), {21: "in_review"})

    def test_failed_worker_is_stalled_even_with_resume(self):
        self.assertEqual(reasons([mine(21)], branches=["fix/21-child"], resume=True),
                         {21: "stalled"})

    def test_in_flight_claim_is_not_respawned(self):
        self.assertEqual(reasons([mine(21)]), {21: "claimed"})

    def test_resume_opts_bare_claim_back_in(self):
        pr_by_issue, branch_by_issue = delivery_index([], [])
        eligible, _, _ = classify([mine(21)], ME, pr_by_issue, branch_by_issue, True)
        self.assertEqual([(e["number"], e["resume"]) for e in eligible], [(21, True)])

    def test_unknown_user_treats_claims_as_foreign(self):
        self.assertEqual(reasons([mine(21)], resume=True, me=None), {21: "claimed"})

    def test_a_claim_the_caller_vouches_for_is_its_own_whatever_the_assignee_reads(self):
        c = child(21, inProgress=True, assignees=["jim-wt21"])
        _, skipped, _ = classify([c], None, {}, {}, False, mine={21})
        self.assertEqual([(s["reason"], s.get("own")) for s in skipped], [("claimed", True)])
        _, skipped, _ = classify([c], None, {}, {}, False)
        self.assertEqual([(s["reason"], s.get("own")) for s in skipped], [("claimed", None)])

    def test_a_vouched_claim_with_a_branch_is_stalled(self):
        c = child(21, inProgress=True, assignees=["jim-wt21"])
        _, skipped, _ = classify([c], None, {}, {21: "fix/21-child"}, False, mine={21})
        self.assertEqual([s["reason"] for s in skipped], ["stalled"])


class Basics(unittest.TestCase):
    def test_unclaimed_ready_child_is_eligible(self):
        self.assertEqual(reasons([child(21)]), {21: "eligible"})

    def test_blocked_and_untriaged(self):
        got = reasons([child(21, openBlockers=[20]), child(22, untriaged=True)])
        self.assertEqual(got, {21: "blocked", 22: "untriaged"})

    def test_unrelated_branches_are_ignored(self):
        got = reasons([child(21)], branches=["main", "worktree-fix+x", "fix/210-other"])
        self.assertEqual(got, {21: "eligible"})

    def test_a_review_finding_is_just_an_issue(self):
        # findings are fixed in the review session; one that reaches the
        # resolver (an explicit scope, an old epic child) is ordinary work
        body = "review-key: code/god-function/a.go\n\nreview-of: #20"
        self.assertEqual(reasons([child(30, body=body)]), {30: "eligible"})

    def test_counts(self):
        pr_by_issue, branch_by_issue = delivery_index([], [])
        eligible, skipped, closed = classify(
            [child(20), child(21, state="closed"), child(22, openBlockers=[20])],
            ME, pr_by_issue, branch_by_issue)
        counts = count_by_reason(eligible, skipped, closed)
        self.assertEqual(counts, {"closed": 1, "blocked": 1, "untriaged": 0,
                                  "claimed": 0, "in_review": 0, "stalled": 0,
                                  "eligible": 1})


def budget(children, prs=(), workers=2, resume=False):
    pr_by_issue, branch_by_issue = delivery_index(list(prs), [])
    _, skipped, _ = classify(children, ME, pr_by_issue, branch_by_issue, resume)
    return spawn_budget(skipped, workers)


class SpawnBudget(unittest.TestCase):
    def test_backlog_is_twice_the_workers(self):
        self.assertEqual(budget([child(21)], workers=2), 4)
        self.assertEqual(budget([child(21)], workers=3), 6)

    def test_open_prs_and_own_in_flight_claims_count(self):
        prs = [{"number": 34, "headRefName": "fix/21-a"}]
        self.assertEqual(budget([mine(21), mine(22), child(23)], prs=prs), 2)

    def test_foreign_claims_do_not_count(self):
        c = child(21, inProgress=True, assignees=["someone"])
        self.assertEqual(budget([c, child(22)]), 4)

    def test_resumed_claims_are_spawns_not_backlog(self):
        self.assertEqual(budget([mine(21)], resume=True), 4)

    def test_floors_at_zero(self):
        prs = [{"number": 30 + n, "headRefName": f"fix/{n}-x"} for n in range(21, 27)]
        self.assertEqual(budget([child(n) for n in range(21, 27)], prs=prs), 0)


class Args(unittest.TestCase):
    def test_defaults(self):
        self.assertEqual(parse_args(["12"]), ([12], None, False, 2))

    def test_a_set_of_issues(self):
        self.assertEqual(parse_args(["12", "13,14", "#15"])[0], [12, 13, 14, 15])

    def test_workers_and_resume_parse(self):
        self.assertEqual(parse_args(["12", "--workers", "3", "--resume"]),
                         ([12], None, True, 3))

    def test_workers_rejects_zero(self):
        with self.assertRaises(SystemExit) as raised:
            parse_args(["12", "--workers", "0"])
        self.assertEqual(raised.exception.code, 2)

    def test_no_scope_is_a_usage_error(self):
        with self.assertRaises(SystemExit) as raised:
            parse_args(["--resume"])
        self.assertEqual(raised.exception.code, 2)

    def test_removed_flags_are_usage_errors(self):
        for flag in ("--finding-depth", "--max-open-prs"):
            with self.assertRaises(SystemExit) as raised:
                parse_args(["12", flag, "2"])
            self.assertEqual(raised.exception.code, 2)


class Resolve(unittest.TestCase):
    """resolve() wires scope, delivery reads, and the budget together."""

    def test_scope_flows_through_to_the_plan(self):
        rows = [child(21, createdAt="2026-01-02"), child(22, createdAt="2026-01-01"),
                child(23, openBlockers=[21])]
        with patch("resolve_ready.scope_issues", return_value=rows) as scope, \
             patch("resolve_ready.open_prs", return_value=[]), \
             patch("resolve_ready.remote_branches", return_value=[]), \
             patch("resolve_ready.main_checkout", return_value="/tmp/repo"), \
             patch("resolve_ready.default_branch", return_value="main"), \
             patch("resolve_ready.current_user", return_value=ME):
            out = resolve([12, 22], None, False, workers=3)
        scope.assert_called_once_with([12, 22], None)
        self.assertEqual(out["scope"], [12, 22])
        # same priority: the oldest child goes first
        self.assertEqual([e["number"] for e in out["eligible"]], [22, 21])
        self.assertEqual(out["counts"]["blocked"], 1)
        self.assertEqual(out["spawnBudget"], 6)


if __name__ == "__main__":
    unittest.main()
