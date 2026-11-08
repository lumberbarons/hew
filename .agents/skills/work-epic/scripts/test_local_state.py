"""The local-mode planners: git probes over a real repository, the decision table, both planners.

The probes run against a throwaway repository because what they promise is git's
behaviour (what `merge-tree` calls a conflict, what `--no-merged` leaves out); the
tables run on plain dicts.

Run with `python3 -m unittest discover plugins/work-epic/skills/work-epic/scripts`.
"""

import json
import os
import shutil
import subprocess
import tempfile
import unittest

import local_state
import repo_state

ENV = dict(os.environ, GIT_AUTHOR_NAME="t", GIT_AUTHOR_EMAIL="t@t",
           GIT_COMMITTER_NAME="t", GIT_COMMITTER_EMAIL="t@t")


class GitRepo(unittest.TestCase):
    """A scratch repository on `main`, and the process standing inside it."""

    def setUp(self):
        self.dir = os.path.realpath(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.dir, ignore_errors=True)
        previous = os.getcwd()
        os.chdir(self.dir)
        self.addCleanup(os.chdir, previous)
        self.git("init", "-q", "-b", "main")
        self.commit("README", "base\n", "base")

    def git(self, *args):
        return subprocess.run(["git", *args], cwd=self.dir, env=ENV, check=True,
                              capture_output=True, text=True).stdout.strip()

    def commit(self, name, text, message):
        with open(os.path.join(self.dir, name), "w") as f:
            f.write(text)
        self.git("add", name)
        self.git("commit", "-q", "-m", message)

    def branch_with(self, branch, name, text):
        """A branch off main carrying one commit; the checkout returns to main."""
        self.git("switch", "-q", "-c", branch, "main")
        self.commit(name, text, f"work on {branch}")
        self.git("switch", "-q", "main")


class Probes(GitRepo):
    def test_the_default_branch_is_main_without_an_origin(self):
        self.assertEqual(repo_state.local_default_branch(), "main")

    def test_origin_head_wins_when_it_is_set(self):
        self.git("update-ref", "refs/remotes/origin/trunk", "HEAD")
        self.git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")
        self.assertEqual(repo_state.local_default_branch(), "trunk")

    def test_master_is_the_fallback_when_there_is_no_main(self):
        self.git("branch", "-m", "main", "master")
        self.assertEqual(repo_state.local_default_branch(), "master")

    def test_only_unmerged_issue_branches_are_listed(self):
        self.branch_with("feat/21-add", "a", "a\n")
        self.branch_with("fix/22-mend", "b", "b\n")
        self.branch_with("scratch", "c", "c\n")  # not named for an issue
        self.git("branch", "chore/30-done", "main")  # already contained in main
        self.assertEqual(repo_state.unmerged_issue_branches("main"),
                         {21: "feat/21-add", 22: "fix/22-mend"})

    def test_a_squash_merged_branch_is_still_unmerged_until_it_is_deleted(self):
        self.branch_with("feat/21-add", "a", "a\n")
        self.git("merge", "--squash", "feat/21-add")
        self.git("commit", "-q", "-m", "squash")
        self.assertIn(21, repo_state.unmerged_issue_branches("main"))
        self.git("branch", "-D", "feat/21-add")
        self.assertEqual(repo_state.unmerged_issue_branches("main"), {})

    def test_branch_head_is_the_tip_or_none(self):
        self.branch_with("feat/21-add", "a", "a\n")
        self.assertEqual(repo_state.branch_head("feat/21-add"), self.git("rev-parse", "feat/21-add"))
        self.assertIsNone(repo_state.branch_head("feat/99-missing"))

    def test_commits_behind_counts_what_the_default_branch_gained(self):
        self.branch_with("feat/21-add", "a", "a\n")
        head = repo_state.branch_head("feat/21-add")
        self.assertEqual(repo_state.commits_behind("main", head), 0)
        self.commit("later", "x\n", "main moves")
        self.assertEqual(repo_state.commits_behind("main", head), 1)
        self.assertIsNone(repo_state.commits_behind("main", "nonsense"))

    def test_conflicts_are_found_without_touching_the_checkout(self):
        self.branch_with("feat/21-add", "README", "branch side\n")
        self.commit("README", "main side\n", "main edits the same line")
        before = self.git("status", "--porcelain"), self.git("rev-parse", "HEAD")
        self.assertTrue(repo_state.conflicts_with("main", repo_state.branch_head("feat/21-add")))
        self.assertEqual((self.git("status", "--porcelain"), self.git("rev-parse", "HEAD")), before)

    def test_disjoint_changes_do_not_conflict(self):
        self.branch_with("feat/21-add", "a", "a\n")
        self.commit("b", "b\n", "main adds another file")
        self.assertFalse(repo_state.conflicts_with("main", repo_state.branch_head("feat/21-add")))

    def test_an_unreadable_ref_is_unknown_never_clean(self):
        self.assertIsNone(repo_state.conflicts_with("main", "nonsense"))


class Backend(GitRepo):
    def config(self, text):
        os.makedirs(os.path.join(self.dir, ".hew"), exist_ok=True)
        with open(os.path.join(self.dir, ".hew", "config.json"), "w") as f:
            f.write(text)

    def test_no_hew_directory_means_github(self):
        self.assertFalse(repo_state.local_tracker())

    def test_a_local_config_at_the_main_checkout_means_local(self):
        self.config(json.dumps({"backend": "local", "version": 1}))
        self.assertTrue(repo_state.local_tracker())

    def test_a_linked_worktree_finds_the_main_checkouts_config(self):
        self.config(json.dumps({"backend": "local"}))
        linked = os.path.join(self.dir, "wt")
        self.git("worktree", "add", "-q", linked, "-b", "wt-branch")
        os.chdir(linked)
        self.assertTrue(repo_state.local_tracker())

    def test_repo_always_means_github(self):
        self.config(json.dumps({"backend": "local"}))
        self.assertFalse(repo_state.local_tracker("owner/name"))

    def test_a_migrated_or_unreadable_config_means_github(self):
        for text in (json.dumps({"backend": "github"}), "", "{not json", "[]"):
            self.config(text)
            self.assertFalse(repo_state.local_tracker(), text)


def iss(**kw):
    return dict({"state": "reviewed", "branch": "feat/21-a", "verified_head": "h1",
                 "repair_failures": 0}, **kw)


GOOD = {"head": "h1", "behind": 0, "conflicts": False}


def decide(iss_, facts=GOOD, findings=(), issue_state="open", reviewing=False):
    return local_state.plan_branch(21, iss_, issue_state, list(findings), facts, reviewing)


class DecisionTable(unittest.TestCase):
    def test_a_reviewed_current_verified_branch_merges(self):
        p = decide(iss())
        self.assertEqual(p["action"], "merge")
        self.assertEqual((p["number"], p["issue"], p["branch"], p["head"]), (21, 21, "feat/21-a", "h1"))
        self.assertEqual((p["checks"], p["mergeState"], p["reviewed"]), ("pass", "CLEAN", True))

    def test_a_closed_issue_waits(self):
        self.assertEqual(decide(iss(), issue_state="closed")["action"], "wait")

    def test_a_live_reviewer_waits(self):
        self.assertEqual(decide(iss(state="reviewing"), reviewing=True)["note"], "reviewer in flight")

    def test_no_review_round_yet_waits(self):
        p = decide(iss(state="delivered"))
        self.assertEqual((p["action"], p["reviewed"]), ("wait", False))
        self.assertEqual(decide(iss(state="review_failed"))["action"], "wait")

    def test_a_branch_git_cannot_read_waits(self):
        for facts in (None, {"head": "h1", "behind": None, "conflicts": False},
                      {"head": "h1", "behind": 0, "conflicts": None}):
            self.assertEqual(decide(iss(), facts)["action"], "wait", facts)

    def test_a_conflict_is_repaired(self):
        p = decide(iss(), dict(GOOD, conflicts=True))
        self.assertEqual((p["action"], p["mergeState"]), ("repair", "DIRTY"))

    def test_a_conflict_with_spent_attempts_is_a_human_gate(self):
        p = decide(iss(repair_failures=2), dict(GOOD, conflicts=True))
        self.assertEqual(p["action"], "conflict")

    def test_a_conflict_beside_a_blocking_finding_is_not_repaired(self):
        p = decide(iss(), dict(GOOD, conflicts=True), [{"priority": "P1"}])
        self.assertEqual(p["action"], "conflict")

    def test_an_open_p1_holds(self):
        p = decide(iss(), findings=[{"priority": "P1"}, {"priority": "P3"}])
        self.assertEqual((p["action"], p["worstOpenFinding"]), ("hold", "P1"))

    def test_an_open_p2_is_backlog_not_a_hold(self):
        self.assertEqual(decide(iss(), findings=[{"priority": "P2"}])["action"], "merge")

    def test_a_branch_behind_is_repaired_because_the_gate_must_see_the_merged_result(self):
        p = decide(iss(), dict(GOOD, behind=2))
        self.assertEqual((p["action"], p["mergeState"]), ("repair", "BEHIND"))
        self.assertIn("2 commits behind", p["note"])

    def test_a_head_the_gate_never_passed_on_is_repaired(self):
        for verified in (None, "older"):
            p = decide(iss(verified_head=verified))
            self.assertEqual((p["action"], p["checks"]), ("repair", "none"), verified)
            self.assertIn("gate has not passed", p["note"])

    def test_spent_attempts_stop_a_branch_that_cannot_be_gated(self):
        p = decide(iss(repair_failures=2, verified_head=None))
        self.assertEqual(p["action"], "ci_failed")
        self.assertEqual(decide(iss(repair_failures=2), dict(GOOD, behind=1))["action"], "ci_failed")

    def test_a_blocking_finding_beats_an_unverified_head(self):
        p = decide(iss(verified_head=None), findings=[{"priority": "P0"}])
        self.assertEqual(p["action"], "hold")


def child(n, state="open", **kw):
    return dict({"number": n, "title": f"issue {n}", "state": state, "priority": "P2", "type": "task",
                 "body": "", "inProgress": False, "assignees": [], "openBlockers": []}, **kw)


class Planners(unittest.TestCase):
    def facts(self, table):
        return lambda branch: table.get(branch)

    def test_only_delivered_branches_in_scope_are_planned(self):
        issues = {"21": iss(), "22": iss(branch="fix/22-b", state="implementing"),
                  "23": iss(branch="fix/23-c", state="merged"), "99": iss(branch="fix/99-z")}
        plan = local_state.plan_over(
            [21, 22, 23], "main", [child(21), child(22), child(23)], {}, issues,
            self.facts({"feat/21-a": GOOD}))
        self.assertEqual([p["number"] for p in plan["prs"]], [21])
        self.assertEqual(plan["counts"], {"merge": 1})
        self.assertEqual(plan["defaultBranch"], "main")

    def test_one_branch_moves_toward_main_per_pass(self):
        issues = {"21": iss(), "22": iss(branch="fix/22-b", verified_head="h2")}
        plan = local_state.plan_over(
            [21, 22], "main", [child(21), child(22)], {}, issues,
            self.facts({"feat/21-a": GOOD, "fix/22-b": dict(GOOD, head="h2")}))
        actions = {p["number"]: p["action"] for p in plan["prs"]}
        self.assertEqual(actions, {21: "merge", 22: "queued"})
        self.assertEqual(plan["queueHead"], 21)

    def test_a_live_repairer_freezes_everything_behind_it(self):
        issues = {"21": iss(), "22": iss(branch="fix/22-b", verified_head="h2")}
        plan = local_state.plan_over(
            [21, 22], "main", [child(21), child(22)], {}, issues,
            self.facts({"feat/21-a": dict(GOOD, behind=1), "fix/22-b": dict(GOOD, head="h2")}),
            repairing=frozenset({21}))
        actions = {p["number"]: p["action"] for p in plan["prs"]}
        self.assertEqual(actions, {21: "wait", 22: "queued"})
        self.assertEqual(plan["repairHead"], 21)

    def test_open_findings_are_matched_to_their_issue(self):
        plan = local_state.plan_over(
            [21], "main", [child(21)], {21: [{"priority": "P1"}]}, {"21": iss()},
            self.facts({"feat/21-a": GOOD}))
        self.assertEqual(plan["prs"][0]["action"], "hold")


class Resolver(unittest.TestCase):
    def test_the_pumps_own_claims_are_told_from_everyone_elses(self):
        # hew names a claimant per worktree, so no assignee here equals the coordinator
        children = [
            child(21, inProgress=True, assignees=["jim-wt21"]),   # delivered
            child(22, inProgress=True, assignees=["jim-wt22"]),   # implementing
            child(23, inProgress=True, assignees=["jim-wt23"]),   # failed: no longer in flight
            child(24),                                            # free
            child(25, inProgress=True, assignees=["someone"]),    # not ours at all
            child(26, state="closed"),
        ]
        issues = {"21": {"state": "reviewed"}, "22": {"state": "implementing"}, "23": {"state": "failed"}}
        out = local_state.resolve_over([21, 22, 23, 24, 25, 26], children, "main", issues, workers=2)
        self.assertEqual([e["number"] for e in out["eligible"]], [24])
        reasons = {s["number"]: s["reason"] for s in out["skipped"]}
        self.assertEqual(reasons, {21: "in_review", 22: "claimed", 23: "claimed", 25: "claimed"})
        detail = {s["number"]: s for s in out["skipped"]}
        self.assertEqual(detail[21]["detail"], "branch delivered, awaiting merge")
        self.assertTrue(detail[22]["own"])
        self.assertNotIn("own", detail[23])
        # the delivered branch and the implementer in flight both count toward the backlog cap
        self.assertEqual(out["spawnBudget"], 2 * 2 - 2)
        self.assertEqual(out["defaultBranch"], "main")

    def test_a_blocker_closed_by_a_merge_unblocks_its_dependent(self):
        children = [child(21, state="closed"), child(22, openBlockers=[])]
        out = local_state.resolve_over([21, 22], children, "main", {}, workers=2)
        self.assertEqual([e["number"] for e in out["eligible"]], [22])


if __name__ == "__main__":
    unittest.main()
