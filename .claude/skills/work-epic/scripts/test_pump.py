"""End-to-end tests for the pump against a simulated Orca, GitHub, and tracker.

The real pump loop, the real pr_state decision table, and the real resolver
classifier run; only the outside world is fake. `World` plays Orca (runs,
worktrees, workers that settle on a clock and write outcome files), GitHub (PRs,
comments, merges), and hew (issues). Scenarios are scripts of what each agent
reports. Time is simulated: `check` jumps to the next worker settling.

Run with `python3 -m unittest discover plugins/work-epic/skills/work-epic/scripts`.
"""

import json
import os
import re
import shutil
import tempfile
import unittest

import findings
import local_state
import pr_state
import pump
import resolve_ready
from orca_api import Orca, Proc
from repo_state import branch_issue

GREEN = [{"status": "COMPLETED", "conclusion": "SUCCESS"}]
RED = [{"status": "COMPLETED", "conclusion": "FAILURE"}]
PENDING = [{"status": "IN_PROGRESS", "conclusion": ""}]
ME = "me"
WORK = 60  # seconds every agent takes


def envelope(result=None, error=None):
    env = {"id": "t", "ok": error is None, "result": result or {}}
    if error:
        env["error"] = {"code": error[0], "message": error[1]}
    return Proc(0 if error is None else 1, json.dumps(env), "")


class World:
    """Orca + GitHub (or, when `local`, git) + hew, just enough for the pump.

    On a local tracker there are no PRs: a delivery is an entry in `branches`, keyed by its
    issue number, and a merge is a squash into the main checkout that moves every other
    branch one commit behind.
    """

    def __init__(self, root, issues, blockers=None, local=False):
        self.root, self.t = root, 1_000_000.0
        self.local, self.branches, self.pump_issues = local, {}, dict
        self.main, self.main_head, self.squash_fail, self.close_fail = os.path.join(root, "main"), "main", 0, False
        self.merge_count, self.squashed = 0, None
        self.issues = {n: {"state": "open", "title": f"issue {n}"} for n in issues}
        self.blockers = blockers or {}
        self.claimed = set()
        self.coord_path = os.path.join(root, "coord")
        os.makedirs(self.coord_path)
        self.worktrees = {"repo::" + self.coord_path: self.wt_row("repo::" + self.coord_path, self.coord_path)}
        self.workers, self.prs, self.findings, self.runs = {}, {}, {}, []
        self.script, self.fail_start, self.dirty = {}, set(), set()
        self.calls, self.acked, self.counter, self.sha = [], [], 0, 0
        self.agent_wait = set()
        self.changed, self.specs = {}, []  # files per delivery id; the prompt each agent got

    # --- helpers -------------------------------------------------------------

    def wt_row(self, wid, path):
        return {"id": wid, "path": path, "comment": "", "branch": "refs/heads/x", "workspaceStatus": "in-progress",
                "isUnread": False, "linkedIssue": None, "linkedPR": None, "repoId": "repo"}

    def new_sha(self):
        self.sha += 1
        return f"{self.sha:07x}" + "0" * 33

    def calls_of(self, *prefix):
        return [c for c in self.calls if c[:len(prefix)] == list(prefix)]

    def advance(self, seconds):
        self.t += seconds

    def card(self, n):
        return next(w for w in self.worktrees.values() if w["linkedIssue"] == n)

    # --- the simulated agents ---------------------------------------------------

    def settle_due(self):
        newly = []
        for d, w in self.workers.items():
            if w["state"] == "running" and w["finish"] <= self.t:
                self.finish(d, w)
                newly.append(d)
        return newly

    def write(self, w, name, data):
        d = os.path.join(self.worktrees[w["wid"]]["path"], ".work-epic")
        os.makedirs(d, exist_ok=True)
        with open(os.path.join(d, name), "w") as f:
            json.dump(data, f)

    def finish(self, d, w):
        role, key = w["role"], w["key"]
        out = self.script.get((role, key), "default")
        if role == "we":
            n = int(key)
            if out == "default" and self.local:
                self.branches[n] = {"branch": f"fix/{n}-x", "head": self.new_sha(), "behind": 0,
                                    "conflicts": False}
                out = {"status": "delivered", "issues": [{"number": n, "outcome": "delivered",
                                                          "branch": f"fix/{n}-x"}]}
            elif out == "default":
                pr = 100 + n
                self.prs[pr] = {"number": pr, "issue": n, "branch": f"fix/{n}-x", "draft": True,
                                "merge_state": "CLEAN", "mergeable": "MERGEABLE", "rollup": GREEN,
                                "comments": [], "head": self.new_sha(), "behind": 0}
                out = {"status": "delivered", "issues": [{"number": n, "outcome": "delivered",
                                                          "branch": f"fix/{n}-x", "pr": pr}]}
        elif role == "wr":
            pr = int(key)
            if out == "default":
                out = {"status": "reviewed", "skill": "code", "findings": [], "gate": "ok"}
            if isinstance(out, dict) and out.get("push"):
                (self.branches if self.local else self.prs)[pr]["head"] = self.new_sha()
            if self.local and isinstance(out, dict) and out.get("status") == "reviewed":
                out = dict({"head": self.branches[pr]["head"]}, **out)
        elif role == "wc":
            pr = int(key)
            if out == "default":
                head = self.new_sha()
                out = {"status": "resolved", "head": head, "gate": "ok"}
            if isinstance(out, dict) and out.get("status") == "resolved" and not out.get("_nopush"):
                if self.local:
                    self.branches[pr].update(head=out["head"], behind=0, conflicts=False)
                else:
                    p = self.prs[pr]
                    p.update(head=out["head"], merge_state="CLEAN", mergeable="MERGEABLE", rollup=GREEN)
        w["state"] = "succeeded" if (out is not None and out.get("status") not in ("error",)) else "failed"
        if out is not None:
            self.write(w, f"{role}-{key}.json", out)

    # --- the planners, over this world -------------------------------------------

    def resolve(self, scope, repo, resume, workers):
        children = []
        for n, iss in self.issues.items():
            # hew names a local claimant per worktree, so it never equals the coordinator
            claimant = f"wt-{n}" if self.local else ME
            children.append({"number": n, "title": iss["title"], "state": iss["state"], "priority": "P2",
                             "type": "task", "body": "", "inProgress": n in self.claimed,
                             "assignees": [claimant] if n in self.claimed else [],
                             "openBlockers": [b for b in self.blockers.get(n, []) if self.issues[b]["state"] == "open"]})
        if self.local:
            return local_state.resolve_over(scope, children, "main", self.pump_issues(), workers)
        prs = [{"number": p["number"], "headRefName": p["branch"]} for p in self.prs.values()]
        by_pr, by_branch = resolve_ready.delivery_index(prs, [p["branch"] for p in self.prs.values()])
        elig, skipped, closed = resolve_ready.classify(children, ME, by_pr, by_branch, resume)
        return {"scope": scope, "eligible": elig, "skipped": skipped,
                "counts": resolve_ready.count_by_reason(elig, skipped, closed),
                "spawnBudget": resolve_ready.spawn_budget(skipped, workers)}

    def facts(self, branch):
        b = self.branches.get(branch_issue(branch))
        return b and {"head": b["head"], "behind": b["behind"], "conflicts": b["conflicts"]}

    def plan(self, scope, repo, reviewing=frozenset(), repairing=frozenset()):
        if self.local:
            children = [{"number": n, "state": i["state"]} for n, i in self.issues.items()]
            return local_state.plan_over(
                scope, "main", children, {n: list(f) for n, f in self.findings.items()},
                self.pump_issues(), self.facts, reviewing, repairing)
        planned = []
        for p in self.prs.values():
            raw = {"number": p["number"], "headRefName": p["branch"], "headRefOid": p["head"],
                   "isDraft": p["draft"], "mergeStateStatus": p["merge_state"], "mergeable": p["mergeable"],
                   "statusCheckRollup": p["rollup"], "comments": p["comments"], "behindBy": p["behind"]}
            issue = branch_issue(p["branch"])
            planned.append(pr_state.plan_pr(raw, issue, self.issues[issue]["state"],
                                            list(self.findings.get(issue, [])), p["number"] in reviewing))
        queue, repair = pr_state.schedule(planned, repairing)
        planned.sort(key=lambda x: x["number"])
        return {"prs": planned, "queueHead": queue, "repairHead": repair}

    # --- the outside commands -------------------------------------------------------

    def sh(self, argv, cwd=None, input=None):
        argv = list(argv)
        if argv[0] == "orca":
            return self.orca(argv[1:-1])  # drop the trailing --json
        self.calls.append(argv)
        if argv[0] == "gh":
            return self.gh(argv[1:], input)
        if argv[0] == "git" and "status" in argv:
            path = argv[2]
            return Proc(0, "M file\n" if path in self.dirty else "", "")
        if argv[0] == "git" and argv[3:5] == ["diff", "--name-only"]:
            n = branch_issue(argv[5].split("...")[1])
            return Proc(0, "".join(f + "\n" for f in self.changed.get(n, [])), "")
        if argv[0] == "git" and "symbolic-ref" in argv:
            return Proc(0, f"{self.main_head}\n", "")
        if argv[0] == "git" and argv[3:5] == ["rev-parse", "--verify"]:
            b = self.branches.get(branch_issue(argv[-1].removeprefix("refs/heads/")))
            return Proc(0, b["head"] + "\n", "") if b else Proc(1, "", "")
        if argv[0] == "git" and argv[3:] == ["rev-parse", "HEAD"]:
            return Proc(0, f"{self.merge_count:07x}" + "f" * 33 + "\n", "")
        if argv[0] == "git" and argv[3:5] == ["merge", "--squash"]:
            if self.squash_fail:
                self.squash_fail -= 1
                return Proc(1, "", "CONFLICT (content): Merge conflict in f")
            self.squashed = argv[5]
            return Proc(0, "", "")
        if argv[0] == "git" and argv[3] == "commit":
            self.merge_count += 1  # the default branch moved: everything else is now behind it
            for n in [n for n, b in self.branches.items() if b["head"] == self.squashed]:
                del self.branches[n]
            for b in self.branches.values():
                b["behind"] += 1
            return Proc(0, "", "")
        if argv[0] == "hew" and argv[1] == "close":
            if self.close_fail:
                return Proc(1, "", "hew: the tracker is locked")
            self.issues[int(argv[2])]["state"] = "closed"
        return Proc(0, "", "")

    def gh(self, args, input):
        if args[:2] == ["pr", "merge"]:
            pr = self.prs.pop(int(args[2]))
            self.issues[pr["issue"]]["state"] = "closed"
        elif args[:2] == ["pr", "ready"]:
            self.prs[int(args[2])]["draft"] = False
        elif args[:2] == ["pr", "update-branch"]:
            self.prs[int(args[2])]["behind"] = 0
        elif args[:2] == ["pr", "comment"]:
            self.prs[int(args[2])]["comments"].append({"body": input})
        elif args[:2] == ["pr", "view"]:
            return Proc(0, self.prs[int(args[2])]["head"] + "\n", "")
        elif args[:2] == ["pr", "diff"]:
            return Proc(0, "".join(f + "\n" for f in self.changed.get(int(args[2]), [])), "")
        return Proc(0, "", "")

    def orca(self, a):
        flags = {a[i]: a[i + 1] for i in range(len(a) - 1) if a[i].startswith("--")}
        cmd = " ".join(a[:2])
        self.calls.append(["orca", *a])
        if cmd == "status":
            return envelope({"runtime": {"state": "ready"}})
        if cmd == "worktree list":
            return envelope({"worktrees": list(self.worktrees.values())})
        if cmd == "worktree set":
            w = self.worktrees[flags["--worktree"].removeprefix("id:")]
            for flag, key in (("--comment", "comment"), ("--workspace-status", "workspaceStatus")):
                if flag in flags:
                    w[key] = flags[flag]
            if "--issue" in flags:
                w["linkedIssue"] = int(flags["--issue"])
            if "--pr" in flags:
                w["linkedPR"] = int(flags["--pr"])
            if "--unread" in a:
                w["isUnread"] = True
            if "--read" in a:
                w["isUnread"] = False
            return envelope({"worktree": w})
        if cmd == "worktree rm":
            wid = flags["--worktree"].removeprefix("id:")
            shutil.rmtree(self.worktrees.pop(wid)["path"], ignore_errors=True)
            return envelope({"removed": True, "removing": True})
        if cmd == "orchestration run-list":
            return envelope({"runs": self.runs})
        if cmd == "orchestration run-create":
            self.runs.append({"id": "run_t", "objective": flags["--objective"]})
            return envelope({"run": self.runs[-1]})
        if cmd == "orchestration run-use":
            return envelope({"run": {"id": flags["--id"]}})
        if cmd == "orchestration worker-start":
            return self.worker_start(flags)
        if cmd == "orchestration worker-list":
            self.settle_due()
            rows = [{"dispatchId": d, "taskId": w["task"], "runId": "run_t", "workerState": w["state"],
                     "dispatchStatus": "completed" if w["state"] != "running" else "dispatched",
                     "projection": {"workspace": {"id": w["wid"]},
                                   "attention": {"requiresAction": d in self.agent_wait}}}
                    for d, w in self.workers.items()]
            return envelope({"workers": rows, "page": {"hasMore": False, "nextCursor": None}})
        if cmd == "orchestration worker-show":
            wait = "permission prompt" if flags["--dispatch"] in self.agent_wait else None
            return envelope({"observation": {"status": "working", "agentWait": wait}})
        if cmd == "orchestration worker-release":
            return envelope({"release": "released"})
        if cmd == "orchestration check":
            self.acked.append(flags.get("--ack"))
            running = [w["finish"] for w in self.workers.values() if w["state"] == "running"]
            self.t = min(running) if running else self.t + int(flags["--timeout-ms"]) / 1000
            done = self.settle_due()
            msgs = [{"id": f"msg_{d}", "type": "worker_done", "payload": json.dumps({"dispatchId": d})}
                    for d in done]
            return envelope({"runId": "run_t", "deliveryId": f"del_{len(self.acked)}" if msgs else None,
                             "messages": msgs, "count": len(msgs), "timedOut": not msgs})
        raise AssertionError(f"unexpected orca command: {a}")

    def worker_start(self, flags):
        title = flags["--task-title"]
        role, key = title.split(" ")[0].split("-")
        self.specs.append((role, key, flags.get("--spec", "")))
        if key in self.fail_start:
            return envelope(error=("start_failed", "agent did not launch"))
        effects = []
        if flags["--worktree"] == "new-child":
            wid = f"repo::{self.root}/we-{key}"
            path = os.path.join(self.root, f"we-{key}")
            os.makedirs(path)
            row = self.wt_row(wid, path)
            row["comment"], row["branch"] = flags.get("--comment", ""), f"refs/heads/me/we-{key}"
            self.worktrees[wid] = row
            effects = [{"kind": "worktree", "action": "created_child", "id": wid}]
            self.claimed.add(int(key))
        else:
            wid = flags["--worktree"].removeprefix("id:")
        self.counter += 1
        d = f"ctx_{self.counter}"
        self.workers[d] = {"role": role, "key": key, "wid": wid, "state": "running",
                           "task": f"task_{self.counter}", "finish": self.t + WORK}
        return envelope({"runId": "run_t", "taskId": f"task_{self.counter}", "dispatchId": d,
                         "state": "ready", "effects": effects, "residualResources": []})


class FakeFiler:
    """Files what the reviewer reported: numbers P2+ findings, opens the unfixed ones."""

    def __init__(self, world):
        self.world, self.next, self.error, self.prs = world, 200, None, []

    def file(self, *, pr, issue, outcome_path):
        self.prs.append(pr)
        if self.error:
            raise findings.FilingError(self.error)
        data = findings.read_outcome(outcome_path)
        records = []
        for f in data["findings"]:
            rec = dict(f, number=None)
            if f["priority"] in ("P0", "P1", "P2"):
                self.next += 1
                rec["number"] = self.next
                if not f.get("fixed"):
                    self.world.findings.setdefault(issue, []).append(
                        {"number": self.next, "priority": "P1" if f["priority"] in ("P0", "P1") else "P2",
                         "state": "open", "body": f"review-of: #{issue} (PR #{pr})"})
            records.append(rec)
        return records


def finding(title, priority, **kw):
    return dict({"title": title, "priority": priority, "files": ["a.go"]}, **kw)


class Scenario(unittest.TestCase):
    def build(self, issues, *, blockers=None, workers=2, resume=False, local=False):
        tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, tmp, ignore_errors=True)
        self.world = w = World(tmp, issues, blockers, local=local)
        coord = {"id": "repo::" + w.coord_path, "path": w.coord_path, "repoId": "repo"}
        outdir = os.path.join(tmp, "out")
        os.makedirs(outdir)
        cfg = pump.Config(scope=list(issues), coord=coord, outdir=outdir, work_issue_dir="/skills/work-issue",
                          workers=workers, resume=resume, local=local, main=w.main if local else None)
        self.filer = FakeFiler(w)
        self.logs = []
        self.pump = self.new_pump(cfg)
        return w

    def new_pump(self, cfg):
        w = self.world
        state = pump.State(os.path.join(cfg.outdir, "state.json"))
        w.pump_issues = lambda: state["issues"]  # a local tracker's planners read the pump's table
        return pump.Pump(cfg, Orca(w.sh, command=["orca"]), w.sh, w.resolve, w.plan, self.filer,
                         state, clock=lambda: w.t, sleep=w.advance, log=self.logs.append)

    def rebuild(self, *, resume=True):
        """A fresh process over the same world and state file, as after a crash."""
        cfg = self.pump.cfg
        cfg.resume = resume
        self.pump = self.new_pump(cfg)
        return self.pump

    def run_pump(self):
        self.summary = self.pump.run()
        return self.summary

    def started(self, role):
        return [w for w in self.world.workers.values() if w["role"] == role]


class HappyPath(Scenario):
    def test_two_independent_issues_are_implemented_reviewed_and_merged(self):
        w = self.build([21, 22])
        s = self.run_pump()
        self.assertEqual(s["status"], "drained")
        self.assertEqual({m["pr"] for m in s["merged"]}, {121, 122})
        self.assertEqual(len(w.calls_of("gh", "pr", "merge")), 2)
        for call in w.calls_of("gh", "pr", "merge"):
            self.assertIn("--squash", call)
            self.assertNotIn("--admin", call)
        self.assertEqual(len(self.started("wr")), 2)  # every PR reviewed exactly once
        self.assertEqual(self.started("wc"), [])
        self.assertEqual(sorted(w.issues[n]["state"] for n in w.issues), ["closed", "closed"])

    def test_worktrees_are_removed_after_merge_but_the_coordinators_stays(self):
        w = self.build([21])
        self.run_pump()
        self.assertEqual(list(w.worktrees), ["repo::" + w.coord_path])

    def test_a_blocked_issue_waits_for_its_blocker_to_merge(self):
        w = self.build([21, 22], blockers={22: [21]})
        s = self.run_pump()
        self.assertEqual(s["status"], "drained")
        merges = [c[3] for c in w.calls_of("gh", "pr", "merge")]
        self.assertEqual(merges, ["121", "122"])  # 22 only started after 21 merged
        starts = [c for c in w.calls_of("orca", "orchestration", "worker-start") if "we-22 · opencode" in c]
        self.assertEqual(len(starts), 1)

    def test_workers_cap_limits_concurrency(self):
        w = self.build([21, 22, 23], workers=1)
        seen = []
        original = w.worker_start

        def spy(flags):
            seen.append(sum(1 for x in w.workers.values() if x["state"] == "running" and x["role"] == "we"))
            return original(flags)

        w.worker_start = spy
        self.run_pump()
        self.assertEqual(max(seen), 0)  # never a second implementer while one is running

    def test_the_cards_tell_the_story(self):
        w = self.build([21])
        wid = None
        orig = w.orca

        def watch(a):
            nonlocal wid
            res = orig(a)
            if a[:2] == ["worktree", "set"]:
                row = w.worktrees.get(a[3].removeprefix("id:"))
                if row and row["linkedIssue"] == 21:
                    self.comments = getattr(self, "comments", []) + [row["comment"]]
            return res

        w.orca = watch
        self.run_pump()
        text = "\n".join(self.comments)
        for phrase in ("worker started", "PR #121 delivered; review pending", "reviewing by claude", "merged"):
            self.assertIn(phrase, text)
        self.assertTrue(all(c.startswith("work-epic · run_t · we-21 · opencode · ") for c in self.comments))


def lens(world, role_key):
    """The tests lens the pump put in the reviewer's prompt for one delivery."""
    spec = next(spec for role, key, spec in world.specs if (role, key) == ("wr", role_key))
    return re.search(r"this (?:PR's|branch's) lens: \*\*(\w+)\*\*", spec).group(1)


class ReviewAndFix(Scenario):
    def test_the_test_review_runs_on_code_and_is_skipped_for_documentation_only(self):
        w = self.build([21, 22, 23])
        w.changed = {121: ["svc/a.go", "svc/a_test.go"], 122: ["README.md", "docs/guide.rst"]}
        self.run_pump()  # 123 lists no files: not knowing is never a reason to skip
        self.assertEqual([lens(w, "121"), lens(w, "122"), lens(w, "123")], ["run", "skip", "run"])

    def test_a_branch_gets_the_same_gate_from_its_diff_against_the_default(self):
        w = self.build([21, 22], local=True)
        w.changed = {21: ["svc/a.go"], 22: ["CHANGELOG.md"]}
        self.run_pump()
        self.assertEqual([lens(w, "21"), lens(w, "22")], ["run", "skip"])
        self.assertEqual([c for c in w.calls if c[0] == "gh"], [])  # still nothing on GitHub

    def test_fixed_findings_do_not_hold_and_the_round_comment_is_posted(self):
        w = self.build([21])
        w.script[("wr", "121")] = {"status": "reviewed", "skill": "code", "gate": "ok", "push": True, "findings": [
            finding("Leaky", "P1", fixed=True, fixedIn="abc1234"),
            finding("Naming", "P3")]}
        s = self.run_pump()
        self.assertEqual(s["status"], "drained")
        self.assertEqual(len(self.started("wr")), 1)  # the pushed fix is not re-reviewed
        # the merged PR is gone, but its comment was posted before it merged
        posted = [c for c in w.calls if c[:3] == ["gh", "pr", "comment"]]
        self.assertEqual(len(posted), 1)

    def test_an_unfixed_p1_holds_the_pr_and_parks_a_decision(self):
        w = self.build([21])
        w.script[("wr", "121")] = {"status": "reviewed", "skill": "code", "findings": [
            finding("Needs a schema decision", "P1", fixed=False, unfixedReason="design call")]}
        s = self.run_pump()
        self.assertEqual(s["status"], "stopped")
        self.assertEqual(w.calls_of("gh", "pr", "merge"), [])
        self.assertTrue(w.prs[121]["draft"])
        self.assertIn("pr121:hold", s["parked"])
        self.assertTrue(w.card(21)["isUnread"])
        self.assertIn("HOLD", w.card(21)["comment"])
        self.assertEqual(pump.EXIT_CODES[s["status"]], 10)

    def test_a_hold_does_not_stop_independent_work(self):
        w = self.build([21, 22])
        w.script[("wr", "121")] = {"status": "reviewed", "skill": "code", "findings": [
            finding("Hard call", "P1", fixed=False, unfixedReason="needs a human")]}
        s = self.run_pump()
        self.assertEqual([m["pr"] for m in s["merged"]], [122])
        self.assertEqual(s["status"], "stopped")

    def test_an_unfixed_p2_is_backlog_not_a_hold(self):
        w = self.build([21])
        w.script[("wr", "121")] = {"status": "reviewed", "skill": "code", "findings": [
            finding("Nice to have", "P2", fixed=False, unfixedReason="large")]}
        s = self.run_pump()
        self.assertEqual(s["status"], "drained")
        self.assertEqual(s["issues"]["21"]["findings"], [201])

    def test_closing_the_finding_releases_a_held_pr_without_a_re_review(self):
        w = self.build([21])
        w.script[("wr", "121")] = {"status": "reviewed", "skill": "code", "findings": [
            finding("Hard call", "P1", fixed=False, unfixedReason="needs a human")]}
        first = self.run_pump()
        self.assertEqual(first["status"], "stopped")
        w.findings[21].clear()  # the human closed the finding
        self.pump.progress = False
        again = self.pump.run()
        self.assertEqual(again["status"], "drained")
        self.assertEqual(len(self.started("wr")), 1)

    def test_a_review_that_did_not_run_is_parked_never_merged(self):
        w = self.build([21])
        w.script[("wr", "121")] = {"status": "no_scope", "reason": "empty diff", "findings": []}
        s = self.run_pump()
        self.assertEqual(s["status"], "stopped")
        self.assertEqual(w.calls_of("gh", "pr", "merge"), [])
        self.assertIn("review-121", s["parked"])

    def test_a_filing_failure_posts_no_round_comment(self):
        w = self.build([21])
        self.filer.error = "hew apply failed"
        s = self.run_pump()
        self.assertEqual(s["status"], "stopped")
        self.assertEqual([c for c in w.calls if c[:3] == ["gh", "pr", "comment"]], [])
        self.assertEqual(w.calls_of("gh", "pr", "merge"), [])

    def test_a_dirty_worktree_is_not_reviewed(self):
        w = self.build([21])
        original = w.finish

        def dirty_after_delivery(d, worker):
            original(d, worker)
            if worker["role"] == "we":
                w.dirty.add(w.worktrees[worker["wid"]]["path"])

        w.finish = dirty_after_delivery
        s = self.run_pump()
        self.assertEqual(self.started("wr"), [])
        self.assertIn("review-121", s["parked"])


class Repairs(Scenario):
    def conflicted_after_review(self, w, pr=121):
        original = w.finish

        def conflict(d, worker):
            original(d, worker)
            if worker["role"] == "we" and 100 + int(worker["key"]) == pr:
                w.prs[pr].update(merge_state="DIRTY", mergeable="CONFLICTING")

        w.finish = conflict

    def test_a_conflict_is_repaired_and_the_pr_merges_without_a_re_review(self):
        w = self.build([21])
        self.conflicted_after_review(w)
        s = self.run_pump()
        self.assertEqual(s["status"], "drained")
        self.assertEqual(len(self.started("wc")), 1)
        self.assertEqual(len(self.started("wr")), 1)
        comments = [c[1] if False else c for c in w.calls if c[:3] == ["gh", "pr", "comment"]]
        self.assertEqual(len(comments), 2)  # the round, then the repair record

    def test_red_ci_is_repaired_too(self):
        w = self.build([21])
        original = w.finish

        def red(d, worker):
            original(d, worker)
            if worker["role"] == "we":
                w.prs[121]["rollup"] = RED

        w.finish = red
        s = self.run_pump()
        self.assertEqual(s["status"], "drained")
        self.assertEqual(len(self.started("wc")), 1)

    def test_two_failed_repairs_park_the_pr(self):
        w = self.build([21])
        self.conflicted_after_review(w)
        w.script[("wc", "121")] = {"status": "blocked", "reason": "pkg/api rename clashes with #33"}
        s = self.run_pump()
        self.assertEqual(s["status"], "stopped")
        self.assertEqual(len(self.started("wc")), 2)
        self.assertEqual(w.calls_of("gh", "pr", "merge"), [])
        self.assertTrue(any(k.startswith("pr121:") for k in s["parked"]))

    def test_a_repair_whose_head_does_not_match_is_not_trusted(self):
        w = self.build([21])
        self.conflicted_after_review(w)
        w.script[("wc", "121")] = {"status": "resolved", "head": "deadbeef", "gate": "ok", "_nopush": True}
        s = self.run_pump()
        self.assertEqual(s["status"], "halted")
        self.assertEqual(w.calls_of("gh", "pr", "merge"), [])

    def test_a_pr_the_run_holds_no_worktree_for_is_never_repaired(self):
        w = self.build([21])
        w.prs[121] = {"number": 121, "issue": 21, "branch": "fix/21-x", "draft": False,
                      "merge_state": "DIRTY", "mergeable": "CONFLICTING", "rollup": GREEN,
                      "comments": [{"body": pr_state.REVIEW_MARKER + ":\nclean"}], "head": w.new_sha(), "behind": 0}
        w.claimed.add(21)
        s = self.run_pump()
        self.assertEqual(self.started("wc"), [])
        self.assertIn("pr121:repair", s["parked"])


class Halts(Scenario):
    def test_an_unreadable_orca_halts_and_gives_up_instead_of_spinning(self):
        w = self.build([21])
        original = w.orca
        calls = {"n": 0}

        def flaky(a):
            if a[:2] == ["orchestration", "worker-list"]:
                calls["n"] += 1
                return envelope(error=("runtime_unavailable", "Orca is not reachable"))
            return original(a)

        w.orca = flaky
        s = self.run_pump()
        self.assertEqual(s["status"], "halted")
        self.assertIn("unreadable", s["halted"])
        self.assertLessEqual(calls["n"], 2 * pump.MAX_ORCA_ERRORS)
        self.assertEqual(w.calls_of("gh", "pr", "merge"), [])

    def test_a_worker_error_halts_and_spawns_nothing_new(self):
        w = self.build([21, 22], workers=1)
        w.script[("we", "21")] = {"status": "error", "reason": "gh auth: exit 4"}
        s = self.run_pump()
        self.assertEqual(s["status"], "halted")
        self.assertEqual(pump.EXIT_CODES["halted"], 3)
        self.assertEqual(len(self.started("we")), 1)
        self.assertIn("gh auth", s["halted"])

    def test_a_missing_outcome_file_is_an_error_after_the_grace_period(self):
        w = self.build([21])
        w.script[("we", "21")] = None
        s = self.run_pump()
        self.assertEqual(s["status"], "halted")

    def test_a_failed_worker_parks_its_issue_and_the_rest_continue(self):
        w = self.build([21, 22])
        w.script[("we", "21")] = {"status": "failed", "reason": "gate would not pass"}
        s = self.run_pump()
        self.assertEqual(s["status"], "stopped")
        self.assertEqual([m["pr"] for m in s["merged"]], [122])
        self.assertIn("issue-21", s["parked"])

    def test_a_spawn_that_fails_costs_only_that_issue(self):
        w = self.build([21, 22])
        w.fail_start.add("21")
        s = self.run_pump()
        self.assertEqual(s["issues"]["21"]["state"], "unscheduled")
        self.assertEqual([m["pr"] for m in s["merged"]], [122])
        starts = [c[c.index("--task-title") + 1] for c in w.calls_of("orca", "orchestration", "worker-start")]
        starts = [t for t in starts if t.startswith("we-")]
        self.assertEqual(len(starts), 2)  # 21 tried once and not retried, 22 started

    def test_a_worker_on_a_dialog_is_escalated_and_never_answered(self):
        w = self.build([21])
        w.agent_wait.add("ctx_1")
        s = self.run_pump()
        self.assertIn("ctx_1:dialog", s["parked"])
        self.assertEqual([c for c in w.calls if "send" in c[:3]], [])

    def test_no_work_to_do_removes_the_empty_worktree(self):
        w = self.build([21])
        w.script[("we", "21")] = {"status": "no_ready_work", "reason": "backlog drained"}
        s = self.run_pump()
        self.assertEqual(list(w.worktrees), ["repo::" + w.coord_path])
        self.assertEqual(s["notes"][0]["type"], "no_ready_work")


class Stalls(Scenario):
    def test_ci_that_never_finishes_is_set_aside_after_the_budget(self):
        w = self.build([21])
        original = w.finish

        def pending(d, worker):
            original(d, worker)
            if worker["role"] == "we":
                w.prs[121]["rollup"] = PENDING

        w.finish = pending
        s = self.run_pump()
        self.assertEqual(s["status"], "stopped")
        self.assertIn("pr121:wait", s["parked"])
        self.assertGreaterEqual(w.t, 1_000_000 + pump.CI_BUDGET_S)
        self.assertEqual(w.calls_of("gh", "pr", "merge"), [])

    def test_a_worker_over_its_budget_is_flagged_but_not_killed(self):
        w = self.build([21])
        self.pump.start()
        self.pump.tick()  # spawns the implementer
        (dispatch,) = self.pump.state["agents"]
        self.pump.state["agents"][dispatch]["started"] -= pump.BUDGET_S["we"] + 1
        self.pump.tick()  # still running, now over budget
        self.assertIn(f"{dispatch}:budget", self.pump.state["parked"])
        self.assertTrue(w.card(21)["isUnread"])
        self.assertEqual(w.workers[dispatch]["state"], "running")
        self.assertEqual([c for c in w.calls if c[:3] in (["orca", "orchestration", "worker-stop"],
                                                          ["orca", "orchestration", "worker-abandon"])], [])


class Resume(Scenario):
    def test_resume_rebinds_the_existing_run_instead_of_creating_one(self):
        w = self.build([21], resume=True)
        w.runs.append({"id": "run_old", "objective": "work-epic #21"})
        self.run_pump()
        self.assertEqual(w.calls_of("orca", "orchestration", "run-create"), [])
        self.assertEqual(self.pump.run_id, "run_old")

    def test_two_matching_runs_are_ambiguous(self):
        w = self.build([21], resume=True)
        w.runs += [{"id": "run_a", "objective": "work-epic #21"}, {"id": "run_b", "objective": "work-epic #21"}]
        with self.assertRaises(pump.StopRun):
            self.run_pump()

    def test_a_fresh_run_creates_one_with_the_scope_as_its_objective(self):
        w = self.build([21, 22])
        self.run_pump()
        self.assertEqual(w.runs[0]["objective"], "work-epic #21 #22")


class Recovery(Scenario):
    def test_a_worker_started_but_never_recorded_is_adopted_not_respawned(self):
        w = self.build([21], resume=True)
        w.runs.append({"id": "run_old", "objective": "work-epic #21"})
        # the previous process started we-21 and died before writing state.json
        w.orca(["orchestration", "worker-start", "--task-title", "we-21 · opencode", "--worktree",
                "new-child", "--comment", "work-epic · run_old · we-21 · opencode · worker started"])
        s = self.run_pump()
        self.assertEqual(s["status"], "drained")
        starts = [c[c.index("--task-title") + 1] for c in w.calls_of("orca", "orchestration", "worker-start")]
        self.assertEqual([t for t in starts if t.startswith("we-")], ["we-21 · opencode"])  # once, not twice
        self.assertEqual([m["pr"] for m in s["merged"]], [121])

    def test_a_review_that_crashed_mid_filing_is_finished_on_resume(self):
        w = self.build([21])

        class Crash(Exception):
            pass

        real = self.filer.file
        self.filer.file = lambda **kw: (_ for _ in ()).throw(Crash())
        with self.assertRaises(Crash):
            self.run_pump()
        self.assertEqual(w.calls_of("gh", "pr", "merge"), [])
        self.filer.file = real  # the tracker is back
        self.rebuild(resume=True)
        s = self.run_pump()
        self.assertEqual(s["status"], "drained")
        self.assertEqual(len(self.started("wr")), 1)  # finished from the archive, not reviewed again
        self.assertEqual(len(w.calls_of("gh", "pr", "merge")), 1)

    def test_resume_does_not_touch_another_runs_worktrees(self):
        w = self.build([21], resume=True)
        w.runs.append({"id": "run_old", "objective": "work-epic #21"})
        other = w.wt_row("repo::" + w.root + "/we-21", os.path.join(w.root, "we-21"))
        other["comment"] = "work-epic · run_other · we-21 · opencode · worker started"
        w.worktrees[other["id"]] = other
        os.makedirs(other["path"])
        w.workers["ctx_other"] = {"role": "we", "key": "21", "wid": other["id"], "state": "running",
                                  "task": "task_x", "finish": w.t + 10 ** 9}
        self.pump.start()
        self.assertNotIn("ctx_other", self.pump.state["agents"])


class Mailbox(Scenario):
    def test_every_delivery_is_acknowledged_on_the_next_wait(self):
        w = self.build([21])
        self.run_pump()
        acked = [a for a in w.acked if a]
        self.assertTrue(acked)
        self.assertTrue(all(a.startswith("del_") for a in acked))

    def test_questions_are_reported_not_answered(self):
        w = self.build([21])
        original = w.orca

        def with_question(a):
            res = original(a)
            if a[:2] == ["orchestration", "check"]:
                env = json.loads(res.out)
                env["result"]["messages"].append({"id": "msg_q", "type": "question", "subject": "which db?"})
                env["result"]["deliveryId"] = env["result"]["deliveryId"] or "del_q"
                return Proc(0, json.dumps(env), "")
            return res

        w.orca = with_question
        s = self.run_pump()
        self.assertEqual(s["notes"][0]["type"], "question")
        self.assertEqual([c for c in w.calls if c[:3] == ["orca", "orchestration", "reply"]], [])


def squashes(w):
    return [c for c in w.calls if c[:5] == ["git", "-C", w.main, "merge", "--squash"]]


def closes(w):
    return [c for c in w.calls if c[:2] == ["hew", "close"]]


class LocalMode(Scenario):
    """A local tracker: no GitHub, no PRs, and the merge is a squash into the local default branch."""

    def conflicted(self, w, n=21):
        original = w.finish

        def conflict(d, worker):
            original(d, worker)
            if worker["role"] == "we" and int(worker["key"]) == n:
                w.branches[n]["conflicts"] = True

        w.finish = conflict

    def test_issues_are_implemented_reviewed_gated_and_squashed_into_main(self):
        w = self.build([21, 22], local=True)
        s = self.run_pump()
        self.assertEqual(s["status"], "drained")
        self.assertTrue(s["local"])
        self.assertEqual([m["issue"] for m in s["merged"]], [21, 22])
        self.assertEqual(len(squashes(w)), 2)
        self.assertEqual([c[2] for c in closes(w)], ["21", "22"])  # the merge is what closes them
        for c in closes(w):
            self.assertIn("--completed", c)
        self.assertEqual(sorted(w.issues[n]["state"] for n in w.issues), ["closed", "closed"])
        self.assertEqual(list(w.worktrees), ["repo::" + w.coord_path])

    def test_nothing_touches_github_or_a_remote(self):
        w = self.build([21, 22], local=True)
        self.run_pump()
        self.assertEqual([c for c in w.calls if c[0] == "gh"], [])
        self.assertEqual([c for c in w.calls if "push" in c or "fetch" in c], [])
        self.assertEqual(self.filer.prs, [None, None])  # findings name no PR

    def test_the_first_branch_merges_on_the_reviewers_gate_alone(self):
        self.build([21], local=True)
        s = self.run_pump()
        self.assertEqual(s["status"], "drained")
        self.assertEqual(len(self.started("wc")), 0)

    def test_a_branch_the_default_branch_has_outrun_is_gated_before_it_merges(self):
        w = self.build([21, 22], local=True)
        s = self.run_pump()
        self.assertEqual(s["status"], "drained")
        self.assertEqual(len(self.started("wc")), 1)  # the second, behind the first's merge
        self.assertEqual(len(self.started("wr")), 2)  # reviewed once each, never again
        self.assertEqual(w.branches, {})

    def test_the_squash_names_the_issue_and_merges_the_verified_head(self):
        w = self.build([21], local=True)
        self.run_pump()
        head = w.calls_of("git", "-C", w.main, "merge", "--squash")[0][5]
        self.assertEqual(len(head), 40)
        (commit,) = [c for c in w.calls if c[:4] == ["git", "-C", w.main, "commit"]]
        self.assertTrue(commit[5].startswith("fix: issue 21 (#21)\n"))
        self.assertIn("Closes #21", commit[5])

    def test_the_implementer_is_told_the_tracker_is_local(self):
        w = self.build([21], local=True)
        self.run_pump()
        specs = [c[c.index("--spec") + 1] for c in w.calls_of("orca", "orchestration", "worker-start")
                 if "we-21 · opencode" in c]
        self.assertEqual(len(specs), 1)
        self.assertIn("never run `hew pr`", specs[0])

    def test_workers_branch_from_the_local_default_branch(self):
        w = self.build([21], local=True)
        self.run_pump()
        (start,) = [c for c in w.calls_of("orca", "orchestration", "worker-start") if "--base-branch" in c]
        self.assertEqual(start[start.index("--base-branch") + 1], "main")

    def test_the_review_round_is_kept_as_a_file_not_commented(self):
        self.build([21], local=True)
        self.run_pump()
        self.assertTrue(os.path.exists(os.path.join(self.pump.cfg.outdir, "rounds", "wr-21.md")))

    def test_a_dependent_starts_only_after_the_merge_closes_its_blocker(self):
        w = self.build([21, 22], blockers={22: [21]}, local=True)
        s = self.run_pump()
        self.assertEqual(s["status"], "drained")
        self.assertEqual([c[2] for c in closes(w)], ["21", "22"])
        self.assertEqual(len(self.started("wc")), 0)  # 22 branched after 21 landed: nothing to catch up on

    def test_a_conflict_is_repaired_and_the_branch_merges_without_a_re_review(self):
        w = self.build([21], local=True)
        self.conflicted(w)
        s = self.run_pump()
        self.assertEqual(s["status"], "drained")
        self.assertEqual(len(self.started("wc")), 1)
        self.assertEqual(len(self.started("wr")), 1)

    def test_a_head_the_reviewer_did_not_report_is_gated_again_before_it_merges(self):
        w = self.build([21], local=True)
        w.script[("wr", "21")] = {"status": "reviewed", "skill": "code", "findings": [],
                                   "head": "deadbeefdead", "gate": "ok"}
        s = self.run_pump()
        self.assertEqual(s["status"], "drained")
        self.assertEqual(len(self.started("wc")), 1)

    def test_two_failed_repairs_park_the_branch(self):
        w = self.build([21], local=True)
        self.conflicted(w)
        w.script[("wc", "21")] = {"status": "blocked", "reason": "clashes with #33"}
        s = self.run_pump()
        self.assertEqual(s["status"], "stopped")
        self.assertEqual(len(self.started("wc")), 2)
        self.assertEqual(s["issues"]["21"]["repair_failures"], 2)
        self.assertEqual(squashes(w), [])
        self.assertIn("pr21:conflict", s["parked"])
        self.assertIn("#21's branch", s["parked"]["pr21:conflict"]["text"])

    def test_a_repair_whose_head_is_not_the_branchs_is_not_trusted(self):
        w = self.build([21], local=True)
        self.conflicted(w)
        w.script[("wc", "21")] = {"status": "resolved", "head": "deadbeefdead", "gate": "ok",
                                   "_nopush": True}
        s = self.run_pump()
        self.assertEqual(s["status"], "halted")
        self.assertEqual(squashes(w), [])

    def test_an_unfixed_p1_holds_the_branch(self):
        w = self.build([21], local=True)
        w.script[("wr", "21")] = {"status": "reviewed", "skill": "code", "findings": [
            finding("Needs a schema decision", "P1", fixed=False, unfixedReason="design call")]}
        s = self.run_pump()
        self.assertEqual(s["status"], "stopped")
        self.assertEqual(squashes(w), [])
        self.assertIn("release the branch", s["parked"]["pr21:hold"]["text"])
        self.assertIn("HOLD", w.card(21)["comment"])

    def test_a_dirty_main_checkout_blocks_the_merge_until_it_is_clean(self):
        w = self.build([21], local=True)
        w.dirty.add(w.main)
        s = self.run_pump()
        self.assertEqual(s["status"], "stopped")
        self.assertEqual(squashes(w), [])
        self.assertIn("uncommitted changes", s["parked"]["pr21:merge"]["text"])
        w.dirty.discard(w.main)  # the human committed their work
        self.pump.progress = False
        again = self.pump.run()
        self.assertEqual(again["status"], "drained")
        self.assertEqual(len(squashes(w)), 1)

    def test_a_main_checkout_on_another_branch_blocks_the_merge(self):
        w = self.build([21], local=True)
        w.main_head = "feature-x"
        s = self.run_pump()
        self.assertEqual(s["status"], "stopped")
        self.assertEqual(squashes(w), [])
        self.assertIn("not on main", s["parked"]["pr21:merge"]["text"])

    def test_a_squash_that_fails_twice_parks_and_leaves_the_checkout_as_it_found_it(self):
        w = self.build([21], local=True)
        w.squash_fail = 2
        s = self.run_pump()
        self.assertEqual(s["status"], "stopped")
        self.assertIn("pr21:merge", s["parked"])
        self.assertEqual(len(w.calls_of("git", "-C", w.main, "reset", "--merge")), 2)
        self.assertEqual(closes(w), [])

    def test_a_merge_whose_close_fails_is_still_a_merge_and_asks_for_the_close(self):
        w = self.build([21], local=True)
        w.close_fail = True
        s = self.run_pump()
        self.assertEqual([m["issue"] for m in s["merged"]], [21])
        self.assertEqual(s["status"], "stopped")
        self.assertIn("`hew close` failed", s["parked"]["issue-21"]["text"])
        self.assertEqual(len(squashes(w)), 1)  # not merged a second time

    def test_a_delivery_without_a_branch_that_exists_halts(self):
        w = self.build([21], local=True)
        w.script[("we", "21")] = {"status": "delivered",
                                   "issues": [{"number": 21, "outcome": "delivered", "branch": "fix/21-gone"}]}
        s = self.run_pump()
        self.assertEqual(s["status"], "halted")
        self.assertIn("without a branch that exists", s["halted"])

    def test_the_report_names_issues_not_prs(self):
        self.build([21, 22], local=True)
        report = pump.render_report(self.run_pump())
        self.assertIn("merged: #21, #22", report)
        self.assertNotIn("PR #", report)


class Prompts(unittest.TestCase):
    def test_every_prompt_renders_with_the_values_the_pump_passes(self):
        values = dict(n=21, pr=121, branch="fix/21-x", default="main", work_issue_dir="/skills/work-issue",
                      tests_lens="run")
        for name in ("worker", "reviewer", "repairer"):
            text = pump.render(name, **values)
            self.assertNotIn("${", text)
        self.assertIn("work-epic/we-21.json".replace("work-epic/", ".work-epic/"), pump.render("worker", **values))
        self.assertIn("git push origin HEAD:fix/21-x", pump.render("reviewer", **values))
        self.assertIn("gh pr checks 121", pump.render("repairer", **values))

    def test_reviewers_run_review_code_report_only(self):
        # The reviewer owns the repair phase (commit per finding, fixedIn, gate). review-code
        # auto-fixes a clean development branch unless told otherwise, which would leave
        # uncommitted edits the fix step then commits under the wrong finding or discards.
        values = {"n": 21, "pr": 121, "branch": "fix/21-x", "default": "main",
                  "work_issue_dir": "/skills/work-issue", "tests_lens": "run"}
        for name in ("reviewer", "reviewer_local"):
            text = pump.render(name, **values)
            self.assertRegex(text, r"review-code skill with flags[^\n]*--report-only", name)

    def test_reviewers_run_review_tests_report_only_and_tag_what_they_merge(self):
        # review-tests auto-fixes a clean development branch for the same reason review-code
        # does, and the converter files a merged finding under its own key only when tagged.
        values = {"n": 21, "pr": 121, "branch": "fix/21-x", "default": "main",
                  "work_issue_dir": "/skills/work-issue", "tests_lens": "run"}
        for name in ("reviewer", "reviewer_local"):
            text = pump.render(name, **values)
            self.assertRegex(text, r"review-tests skill with flags[^\n]*--report-only", name)
            self.assertRegex(text, r'"skill": "tests"', name)
            self.assertIn("**run**", text)
            self.assertLess(text.index("review-code skill"), text.index("review-tests skill"), name)

    def test_the_local_prompts_name_no_remote_and_no_pr(self):
        values = {"n": 21, "pr": 21, "branch": "fix/21-x", "default": "main",
                  "work_issue_dir": "/skills/work-issue", "why": "2 commits behind the default branch",
                  "tests_lens": "run"}
        for name in ("worker_local", "reviewer_local", "repairer_local"):
            text = pump.render(name, **values)
            self.assertNotIn("${", text)
            for forbidden in ("git push origin", "git fetch", "origin/", "gh pr"):
                self.assertNotIn(forbidden, text, name)
        self.assertIn(".work-epic/wr-21.json", pump.render("reviewer_local", **values))
        repair = pump.render("repairer_local", **values)
        self.assertIn(".work-epic/wc-21.json", repair)
        self.assertIn("2 commits behind the default branch", repair)


if __name__ == "__main__":
    unittest.main()
