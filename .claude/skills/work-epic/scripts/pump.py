"""The work-epic pump: resolve, spawn, reap, review, merge, repair — until nothing else can move.

This is the coordinator as code. It owns no policy: which issues spawn comes from
resolve_ready.py, which PRs merge or get repaired from pr_state.py, and what the
agents are told from prompts/. The pump only does what those say, in Orca.

One tick: reap settled agents, start reviewers for delivered PRs, run the merge
pass, spawn into free slots. Between ticks it waits on the Run's mailbox, which
doubles as the CI-poll clock. It stops when no agent is live, nothing can spawn,
and every open PR is parked on a decision only a human can make.

State is `state.json` in the coordinator's scratch directory: the agents it
started, the issues' PR/review phases, and the decisions parked. Everything
else is re-read from Orca, hew, and GitHub each tick, so a restart loses nothing.

On a local tracker (`Config.local`) there is no GitHub: a delivery is a committed
branch whose id is its issue number, "merged" is a squash into the local default
branch, and local_state.py supplies the two planners. The loop is the same.
"""

import json
import os
import string
import sys
import time

import findings
import orca_api
from orca_api import OrcaError, settled
from pr_state import REPAIR_FAIL_MARKER
from repo_state import CommandError, touches_code

# The agents. The only place an agent id appears; edit here to change one.
IMPLEMENTER = "opencode"
REVIEWER = "claude"  # reviews, then fixes what it found
REPAIRER = "opencode"  # repairs conflicts and red CI

BUDGET_S = {"we": 2 * 3600, "wr": 90 * 60, "wc": 3600}  # continuous working, per agent
CI_BUDGET_S = 3600  # a PR with no state change for this long is set aside
OUTCOME_GRACE_S = 90  # the outcome file can trail the settlement
CHECK_TIMEOUT_MS = 120_000  # one mailbox wait, which is also the CI poll interval
MAX_ORCA_ERRORS = 3
GATE_ACTIONS = {"hold", "conflict", "ci_failed", "protected"}
ROLE_NAMES = {"we": "worker", "wr": "reviewer", "wc": "repairer"}
PROMPT_DIR = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "prompts")


def default_log(message):
    """Progress goes to stderr so stdout stays the report."""
    print(time.strftime("%H:%M:%S"), message, file=sys.stderr, flush=True)


class StopRun(Exception):
    """The run cannot start or continue (ambiguous resume, unreadable runtime)."""


def render(name, **values):
    with open(os.path.join(PROMPT_DIR, f"{name}.md")) as f:
        return string.Template(f.read()).substitute(**values)


class State:
    """state.json: written after every transition, read once at startup."""

    def __init__(self, path):
        self.path = path
        self.data = {"issues": {}, "agents": {}, "parked": {}, "waiting": {},
                     "notes": [], "seen_messages": [], "merged": []}
        if os.path.exists(path):
            with open(path) as f:
                self.data.update(json.load(f))

    def __getitem__(self, key):
        return self.data[key]

    def __setitem__(self, key, value):
        self.data[key] = value

    def get(self, key, default=None):
        return self.data.get(key, default)

    def issue(self, n):
        return self.data["issues"].setdefault(str(n), {})

    def save(self):
        tmp = self.path + ".tmp"
        with open(tmp, "w") as f:
            json.dump(self.data, f, indent=2)
        os.replace(tmp, self.path)


class Config:
    def __init__(self, *, scope, coord, outdir, work_issue_dir, workers=2, repo=None,
                 resume=False, main=None, default="main", local=False):
        self.scope, self.workers, self.repo, self.resume = scope, workers, repo, resume
        self.coord, self.outdir, self.work_issue_dir = coord, outdir, work_issue_dir
        self.main, self.default, self.local = main, default, local


class Pump:
    def __init__(self, cfg, orca, sh, resolve_fn, plan_fn, filer, state, *,
                 clock=time.time, sleep=time.sleep, log=default_log):
        self.cfg, self.orca, self.sh = cfg, orca, sh
        self.resolve_fn, self.plan_fn, self.filer = resolve_fn, plan_fn, filer
        self.state, self.clock, self.sleep, self.log = state, clock, sleep, log
        self.run_id = None
        self.halted = None
        self.pending_ack = None
        self.last_plan = {"prs": []}
        self.last_resolve = {"eligible": [], "spawnBudget": 0, "counts": {}}
        self.spawnable = []
        self.resumed = False
        self.progress = False
        self.errors = 0

    # --- small helpers -------------------------------------------------------

    def _sh(self, *argv, input=None):
        return self.sh(list(argv), cwd=self.cfg.coord["path"], input=input)

    def _gh(self, *args, input=None):
        suffix = ["-R", self.cfg.repo] if self.cfg.repo else []
        return self._sh("gh", *args, *suffix, input=input)

    def label(self, pr):
        """How a delivery reads in a card or a decision: a PR, or on a local tracker its branch."""
        return f"#{pr}'s branch" if self.cfg.local else f"PR #{pr}"

    def marker(self, key, agent, text):
        return f"work-epic · {self.run_id} · {key} · {agent} · {text}"

    def card(self, n, text, *, status=None, unread=None, pr=None):
        """Rewrite an issue's worktree card, keeping the marker tokens."""
        iss = self.state.issue(n)
        if iss.get("worktree"):
            self.orca.worktree_set(iss["worktree"], comment=self.marker(f"we-{n}", IMPLEMENTER, text),
                                   status=status, unread=unread, pr=pr)

    def park(self, key, text, *, issue=None, pr=None, kind="event", card=None):
        """Record a decision a human must make. Idempotent per key."""
        if key in self.state["parked"]:
            return
        self.state["parked"][key] = {"text": text, "issue": issue, "pr": pr, "kind": kind}
        self.log(f"PARKED {key}: {text}")
        if issue is not None:
            self.card(issue, card or text, unread=True)
        self.state.save()

    def halt(self, reason):
        if not self.halted:
            self.halted = reason
            self.log(f"HALT: {reason}")
            self.park("halt", reason)

    def live(self, role=None):
        return [(d, a) for d, a in self.state["agents"].items()
                if not a["done"] and (role is None or a["role"] == role)]

    # --- start ---------------------------------------------------------------

    def start(self):
        objective = "work-epic " + " ".join(f"#{n}" for n in self.cfg.scope)
        runs = [r for r in self.orca.runs() if r.get("objective") == objective]
        if self.cfg.resume:
            known = self.state.get("run")
            if known:
                self.run_id = known
            elif len(runs) == 1:
                self.run_id = runs[0]["id"]
            elif runs:
                raise StopRun(f"{len(runs)} Runs match {objective!r}; pass the one to resume")
            if self.run_id:
                self.orca.run_use(self.run_id)
        if not self.run_id:
            self.run_id = self.orca.run_create(objective)
        self.state["run"], self.state["scope"] = self.run_id, list(self.cfg.scope)
        self.state.save()
        if self.cfg.resume:
            self.recover()
        self.orca.worktree_set(
            self.cfg.coord["id"], status="in-progress",
            comment=f"work-epic · {self.run_id} · coordinator starting")
        self.sync_main()

    def recover(self):
        """After a crash: finish handlers that never completed, adopt agents never recorded."""
        for d, a in list(self.state["agents"].items()):
            if a["done"] and not a.get("handled") and a.get("archived"):
                out = findings.read_outcome(a["archived"]) or {
                    "status": "error", "reason": "the archived outcome is unreadable"}
                self.log(f"recovering the unfinished {ROLE_NAMES[a['role']]} handler for {d}")
                self.handle(a, out)
        by_workspace = {}
        for row in self.orca.workers(self.run_id):  # newest first
            by_workspace.setdefault(((row.get("projection") or {}).get("workspace") or {}).get("id"), row)
        prefix = f"work-epic · {self.run_id} · we-"
        tracked = {a["worktree"] for a in self.state["agents"].values()}
        for wt in self.orca.worktrees():
            comment = wt.get("comment") or ""
            row = by_workspace.get(wt["id"])
            if not comment.startswith(prefix) or wt["id"] in tracked or row is None:
                continue
            n = int(comment[len(prefix):].split(" ")[0])
            self.log(f"adopting the worker for #{n} that was started but never recorded")
            self.state["agents"][row["dispatchId"]] = {
                "role": "we", "key": str(n), "issue": n, "pr": None, "task": row.get("taskId"),
                "agent": IMPLEMENTER, "worktree": wt["id"], "path": wt["path"],
                "started": self.clock(), "done": False}
            self.state.issue(n).update(
                state="implementing", worktree=wt["id"], path=wt["path"],
                orca_branch=(wt.get("branch") or "").removeprefix("refs/heads/") or None)
        self.state.save()

    def sync_main(self):
        """Fetch, and fast-forward the human's checkout only while it sits on the default branch."""
        main = self.cfg.main
        if not main or self.cfg.local:  # a local tracker has no origin to follow
            return
        self.sh(["git", "-C", main, "fetch", "origin"])
        head = self.sh(["git", "-C", main, "symbolic-ref", "--short", "HEAD"])
        if head.rc == 0 and head.out.strip() == self.cfg.default:
            ff = self.sh(["git", "-C", main, "merge", "--ff-only", f"origin/{self.cfg.default}"])
            if ff.rc != 0:
                self.park("main-ff", f"fast-forward of {main} failed: {ff.err.strip()[:120]}")

    # --- the loop ---------------------------------------------------------------

    def run(self):
        self.start()
        while True:
            failed = False
            try:
                self.progress = False
                self.tick()
                self.errors = 0
            except (OrcaError, CommandError) as e:
                failed = True
                self.errors += 1
                self.log(f"tick error ({self.errors}): {e}")
                if self.errors >= MAX_ORCA_ERRORS:
                    self.halt(f"Orca or the tracker is unreadable: {e}")
                if self.errors >= 2 * MAX_ORCA_ERRORS:
                    # live agents can no longer be reaped; leave them for --resume rather than spin
                    self.state.save()
                    return self.summary()
                self.sleep(5)
            # a tick that failed saw nothing, so it cannot prove the work is done
            if (not failed or self.halted) and self.finished():
                return self.summary()
            if not self.progress and not failed:
                self.wait_mailbox()

    def tick(self):
        rows = self.orca.workers(self.run_id)
        self.reap(rows)
        if not self.halted:
            for n, iss in list(self.state["issues"].items()):
                if iss.get("state") == "delivered" and not self.reviewer_live(iss.get("pr")):
                    self.start_reviewer(int(n))
            self.merge_pass()
            self.spawn()
        self.state.save()

    def wait_mailbox(self):
        try:
            res = self.orca.check(self.run_id, timeout_ms=CHECK_TIMEOUT_MS, ack=self.pending_ack)
        except OrcaError as e:
            self.log(f"check failed: {e}")
            self.sleep(5)
            return
        self.pending_ack = None
        for m in res.get("messages") or []:
            if m.get("type") in ("question", "escalation") and m["id"] not in self.state["seen_messages"]:
                self.state["seen_messages"].append(m["id"])
                self.state["notes"].append(
                    {"type": m["type"], "id": m["id"], "subject": m.get("subject", "")})
                self.log(f"{m['type'].upper()} from a worker: {m.get('subject', '')} ({m['id']})")
        if res.get("messages"):
            self.pending_ack = res.get("deliveryId")
        self.state.save()

    # --- reaping -------------------------------------------------------------------

    def reap(self, rows):
        by_dispatch = {r.get("dispatchId"): r for r in rows}
        for d, a in self.live():
            row = by_dispatch.get(d)
            if row is None:
                continue
            if settled(row):
                self.settle(d, a)
            else:
                self.watch(d, a, row)

    def outcome_path(self, a):
        return os.path.join(a["path"], ".work-epic", f"{a['role']}-{a['key']}.json")

    def settle(self, d, a):
        path = self.outcome_path(a)
        out = findings.read_outcome(path)
        if out is None:
            first = a.setdefault("settled_seen", self.clock())
            if self.clock() - first < OUTCOME_GRACE_S:
                return
            out = {"status": "error", "reason": "no outcome file after the agent settled"}
        archive = os.path.join(self.cfg.outdir, "outcomes", f"{a['role']}-{a['key']}.json")
        os.makedirs(os.path.dirname(archive), exist_ok=True)
        if os.path.exists(path):
            os.replace(path, archive)
        a["archived"] = archive
        try:
            self.orca.worker_release(d)
        except OrcaError as e:
            self.log(f"release of {d} needs attention: {e}")
            self.state["notes"].append({"type": "release", "id": d, "subject": str(e)})
        a["done"] = True
        self.progress = True
        self.state.save()  # a crash inside the handler is finished by recover(), not repeated blind
        self.handle(a, out)
        self.state.save()

    def handle(self, a, out):
        {"we": self.on_worker, "wr": self.on_review, "wc": self.on_repair}[a["role"]](a, out)
        a["handled"] = True

    def watch(self, d, a, row):
        """A live agent: over budget, or parked on a dialog only a human can answer."""
        if self.clock() - a["started"] > BUDGET_S[a["role"]] and not a.get("over_budget"):
            a["over_budget"] = True
            self.park(f"{d}:budget", f"{ROLE_NAMES[a['role']]} for #{a['issue']} is over its time budget",
                      issue=a["issue"], kind="event")
        attention = (row.get("projection") or {}).get("attention") or {}
        if attention.get("requiresAction") and not a.get("escalated"):
            wait = (self.orca.worker_show(d).get("observation") or {}).get("agentWait")
            if wait:
                a["escalated"] = True
                self.park(f"{d}:dialog", f"{ROLE_NAMES[a['role']]} for #{a['issue']} is parked on a "
                          f"dialog only a human can answer: {wait}", issue=a["issue"],
                          card="BLOCKED: waiting on a human answer")

    def start_agent(self, role, key, issue, *, spec, title, agent, worktree, wid, path, pr=None,
                    **extra):
        rec = self.orca.worker_start(spec=spec, title=title, agent=agent, worktree=worktree,
                                     **extra)
        wid = wid or rec.worktree
        if not wid:  # an older host that omits the worktree effect: find it by its marker
            for w in self.orca.worktrees():
                if (w.get("comment") or "").startswith(f"work-epic · {self.run_id} · {title.split(' ')[0]}"):
                    wid = w["id"]
        self.state["agents"][rec.dispatch] = {
            "role": role, "key": key, "issue": issue, "pr": pr, "task": rec.task, "agent": agent,
            "worktree": wid, "path": path, "started": self.clock(), "done": False}
        self.progress = True
        return rec, wid

    # --- implementers ------------------------------------------------------------

    def spawn(self):
        # --resume re-offers this user's bare claims, so it applies to the first pass only; after
        # that a claim of this Run is an agent in flight. Anything the pump already tracks —
        # in flight, delivered, failed, or unscheduled — is never spawned again.
        res = self.resolve_fn(self.cfg.scope, self.cfg.repo, self.cfg.resume and not self.resumed,
                              self.cfg.workers)
        self.resumed = True
        self.last_resolve = res
        self.spawnable = [e for e in res["eligible"] if str(e["number"]) not in self.state["issues"]]
        slots = min(self.cfg.workers - self.writers(), res["spawnBudget"])
        for e in self.spawnable[:max(0, slots)]:
            self.start_worker(e)

    def writers(self):
        return len(self.live("we")) + len(self.live("wc"))

    def start_worker(self, e):
        n, title = e["number"], e.get("title", "")
        name = f"we-{n}"
        try:
            rec, wid = self.start_agent(
                "we", str(n), n, spec=render("worker_local" if self.cfg.local else "worker", n=n), title=f"{name} · {IMPLEMENTER}",
                agent=IMPLEMENTER, worktree="new-child", wid=None, path=None,
                repo=f"id:{self.cfg.coord['repoId']}", name=name,
                display_name=f"#{n} {title[:50]}", base_branch=self.cfg.default if self.cfg.local else f"origin/{self.cfg.default}",
                comment=self.marker(name, IMPLEMENTER, "worker started"), setup="inherit")
        except OrcaError as e:
            return self.unscheduled(n, e)
        row = next((w for w in self.orca.worktrees() if w["id"] == wid), {})
        path = row.get("path")
        self.state["agents"][rec.dispatch].update(worktree=wid, path=path)
        iss = self.state.issue(n)
        iss.update(state="implementing", worktree=wid, path=path, large=e.get("large", False),
                   title=title,
                   orca_branch=(row.get("branch") or "").removeprefix("refs/heads/") or None)
        self.orca.worktree_set(wid, issue=n, status="in-progress")
        self.log(f"started {name} ({IMPLEMENTER}) in {path}")

    def unscheduled(self, n, err):
        """One failed spawn costs only that issue: free what it left, report, move on."""
        self.state.issue(n)["state"] = "unscheduled"
        dispatch = orca_api.find_key(err.result or {}, "dispatchId")
        if dispatch:
            try:
                self.orca.worker_release(dispatch)
            except OrcaError:
                pass
        for w in self.orca.worktrees():
            if (w.get("comment") or "").startswith(f"work-epic · {self.run_id} · we-{n} "):
                self.remove_worktree(w["id"], w.get("path"))
        self.park(f"issue-{n}", f"#{n}: the {IMPLEMENTER} worker could not start — {err}")

    def on_worker(self, a, out):
        n, status = a["issue"], out.get("status")
        iss = self.state.issue(n)
        if status == "delivered":
            entry = (out.get("issues") or [{}])[0]
            pr, branch = entry.get("pr"), entry.get("branch")
            if self.cfg.local:
                return self.delivered_locally(n, branch)
            if not pr and branch:
                found = self._gh("pr", "list", "--head", branch, "--json", "number", "--jq", ".[0].number")
                pr = int(found.out.strip()) if found.rc == 0 and found.out.strip().isdigit() else None
            if not pr:
                return self.halt(f"#{n} reported delivered without a PR")
            iss.update(state="delivered", pr=pr, branch=branch)
            self.card(n, f"PR #{pr} delivered; review pending", status="in-review", pr=pr)
        elif status == "failed":
            iss["state"] = "failed"
            self.park(f"issue-{n}", f"#{n}: the worker failed — {out.get('reason', 'no reason given')}",
                      issue=n, card=f"failed: {out.get('reason', '')[:100]}; branch pushed")
        elif status in ("no_ready_work", "not_eligible"):
            iss["state"] = "noop"
            self.state["notes"].append({"type": status, "id": str(n), "subject": out.get("reason", "")})
            self.remove_worktree(iss.get("worktree"), iss.get("path"))
        else:
            iss["state"] = "error"
            self.park(f"issue-{n}", f"#{n}: worker error — {out.get('reason', status)}", issue=n,
                      card=f"error: {out.get('reason', status)}"[:140])
            self.halt(f"worker for #{n} errored: {out.get('reason', status)}")

    def delivered_locally(self, n, branch):
        """A local delivery is a committed branch, and its id is the issue number."""
        iss = self.state.issue(n)
        if not branch or self.branch_head(iss["path"], branch) is None:
            return self.halt(f"#{n} reported delivered without a branch that exists")
        iss.update(state="delivered", pr=n, branch=branch)
        self.card(n, "branch delivered; review pending", status="in-review")

    def branch_head(self, path, branch):
        """The commit a local branch points at, as seen from a worktree, or None."""
        proc = self.sh(["git", "-C", path, "rev-parse", "--verify", "--quiet", f"refs/heads/{branch}"])
        return (proc.out.strip() or None) if proc.rc == 0 else None

    def verify(self, n, claimed):
        """Record the head the gate passed on, when the agent's claim is the branch's actual head."""
        iss = self.state.issue(n)
        actual = self.branch_head(iss["path"], iss["branch"])
        if actual and claimed and len(claimed) >= 7 and actual.startswith(claimed):
            iss["verified_head"] = actual
            return True
        return False

    # --- reviewers ------------------------------------------------------------------

    def reviewer_live(self, pr):
        return any(a["pr"] == pr for _, a in self.live("wr"))

    def clean(self, path):
        status = self.sh(["git", "-C", path, "status", "--porcelain"])
        return status.rc == 0 and not status.out.strip()

    def tests_lens(self, iss):
        """`run` when the delivery changes more than prose, else `skip` the test review."""
        if self.cfg.local:
            proc = self.sh(["git", "-C", iss["path"], "diff", "--name-only",
                            f"{self.cfg.default}...{iss['branch']}"])
        else:
            proc = self._gh("pr", "diff", str(iss["pr"]), "--name-only")
        paths = proc.out.splitlines() if proc.rc == 0 else []
        return "run" if touches_code(paths) else "skip"

    def start_reviewer(self, n):
        iss = self.state.issue(n)
        pr = iss["pr"]
        if not self.clean(iss["path"]):
            iss["state"] = "review_failed"
            return self.park(f"review-{pr}", f"{self.label(pr)}: the worktree is dirty, so it was not reviewed",
                             issue=n, pr=pr, card="DIRTY: not reviewed")
        spec = render("reviewer_local" if self.cfg.local else "reviewer", n=n, pr=pr,
                      branch=iss["branch"], default=self.cfg.default,
                      work_issue_dir=self.cfg.work_issue_dir, tests_lens=self.tests_lens(iss))
        try:
            self.start_agent("wr", str(pr), n, spec=spec, title=f"wr-{pr} · {REVIEWER}",
                             agent=REVIEWER, worktree=f"id:{iss['worktree']}", wid=iss["worktree"],
                             path=iss["path"], pr=pr)
        except OrcaError as e:
            iss["state"] = "review_failed"
            return self.park(f"review-{pr}", f"{self.label(pr)}: the reviewer could not start — {e}",
                             issue=n, pr=pr, card="REVIEW FAILED: reviewer did not start")
        iss["state"] = "reviewing"
        self.card(n, f"reviewing by {REVIEWER}", status="in-review")
        self.log(f"started wr-{pr} ({REVIEWER})")

    def on_review(self, a, out):
        n, pr = a["issue"], a["pr"]
        iss = self.state.issue(n)
        if out.get("status") != "reviewed":
            iss["state"] = "review_failed"
            return self.park(f"review-{pr}", f"{self.label(pr)}: the review did not run — "
                             f"{out.get('reason', out.get('status'))}", issue=n, pr=pr,
                             card="REVIEW FAILED: not reviewed")
        try:
            records = self.filer.file(pr=None if self.cfg.local else pr, issue=n,
                                      outcome_path=a["archived"])
        except findings.FilingError as e:
            iss["state"] = "review_failed"
            return self.park(f"review-{pr}", f"{self.label(pr)}: filing the findings failed — {e}",
                             issue=n, pr=pr, card="FILING FAILED: not reviewed")
        body = findings.round_comment(records, out.get("gate"))
        if self.cfg.local:  # no PR to comment on; keep the round beside the outcomes
            rounds = os.path.join(self.cfg.outdir, "rounds")
            os.makedirs(rounds, exist_ok=True)
            with open(os.path.join(rounds, f"wr-{pr}.md"), "w") as f:
                f.write(body)
        else:
            posted = self._gh("pr", "comment", str(pr), "--body-file", "-", input=body)
            if posted.rc != 0:
                iss["state"] = "review_failed"
                return self.park(f"review-{pr}", f"PR #{pr}: posting the round comment failed",
                                 issue=n, pr=pr, card="COMMENT FAILED: not reviewed")
        fixed = sum(1 for r in records if r.get("fixed"))
        iss.update(state="reviewed", findings=[r["number"] for r in records if r.get("number")])
        if self.cfg.local:  # the reviewer ran the gate on the head it reports; no CI will
            self.verify(n, out.get("head"))
        blocker = findings.unfixed_blocker(records)
        if blocker:
            self.card(n, f"HOLD: {blocker.get('priority')} finding #{blocker.get('number')} not fixed "
                         f"(reviewer {REVIEWER})", unread=True)
        else:
            open_count = sum(1 for r in records if r.get("number") and not r.get("fixed"))
            self.card(n, f"reviewed by {REVIEWER}; {fixed} fixed, {open_count} filed open; ready to merge")

    # --- the merge pass ----------------------------------------------------------------

    def merge_pass(self):
        for _ in range(8):
            reviewing = frozenset(a["pr"] for _, a in self.live("wr"))
            repairing = frozenset(int(a["key"]) for _, a in self.live("wc"))
            plan = self.plan_fn(self.cfg.scope, self.cfg.repo, reviewing, repairing)
            self.last_plan = plan
            self.track_waits(plan["prs"])
            wrote = False
            for p in plan["prs"]:
                wrote = self.act(p) or wrote
            if not wrote:
                break
            self.progress = True
        gates = {f"pr{p['number']}:{p['action']}" for p in self.last_plan["prs"] if p.get("parked")}
        gates |= {f"pr{p['number']}:merge" for p in self.last_plan["prs"] if p.get("parked")}
        for key in [k for k, v in self.state["parked"].items() if v["kind"] == "gate" and k not in gates]:
            del self.state["parked"][key]  # the human lifted it

    def track_waits(self, prs):
        """A `wait` with no state change for CI_BUDGET_S is set aside."""
        live = {str(p["number"]) for p in prs if p["action"] == "wait"}
        for p in prs:
            if p["action"] != "wait":
                continue
            sig = [p["head"], p["checks"], p["mergeState"], p.get("note")]
            entry = self.state["waiting"].get(str(p["number"]))
            if not entry or entry["sig"] != sig:
                entry = self.state["waiting"][str(p["number"])] = {"sig": sig, "since": self.clock()}
            p["budgetSpent"] = self.clock() - entry["since"] >= CI_BUDGET_S
        for k in [k for k in self.state["waiting"] if k not in live]:
            del self.state["waiting"][k]

    def act(self, p):
        """Execute one PR's classified action. True when it wrote something."""
        pr, n, action = p["number"], p["issue"], p["action"]
        key = f"pr{pr}:{action}"
        p["parked"] = action in GATE_ACTIONS
        if p.get("budgetSpent"):
            p["parked"] = True
            self.park(key, f"{self.label(pr)}: no progress for {CI_BUDGET_S // 60} minutes "
                      f"({p.get('note')})", issue=n, pr=pr, kind="gate", card="STALLED: " + str(p.get("note")))
        if action == "merge":
            return self.merge(p)
        if action in ("mark_ready", "update_branch"):
            return self.simple(p, "ready" if action == "mark_ready" else "update-branch")
        if action == "repair":
            return self.start_repairer(p)
        label, thing = self.label(pr), "the branch" if self.cfg.local else "the PR"
        gate_text = {
            "hold": f"{label}: an open {p.get('worstOpenFinding')} finding the reviewer could not fix "
                    f"holds it; close the finding to release {thing}",
            "conflict": f"{label}: conflicts with the default branch and cannot be repaired automatically",
            "ci_failed": f"{label}: could not be brought up to date and gated, and repair attempts "
                         "are spent" if self.cfg.local
                         else f"PR #{pr}: checks are failing and repair attempts are spent",
            "protected": f"{label}: branch protection wants something the pump cannot give "
                         "(typically an approving review)",
        }
        if action in gate_text:
            self.park(key, gate_text[action], issue=n, pr=pr, kind="gate",
                      card={"hold": f"HOLD: {p.get('worstOpenFinding')} finding not fixed",
                            "conflict": "CONFLICT", "ci_failed": "CI FAILED",
                            "protected": "PROTECTED"}[action])
        return False

    def simple(self, p, verb):
        """`gh pr ready` / `gh pr update-branch`; three failures in a row park the PR."""
        pr, key = p["number"], f"pr{p['number']}:{p['action']}"
        done = self._gh("pr", verb, str(pr))
        fails = self.state["action_failures"] = self.state.get("action_failures", {})
        if done.rc == 0:
            fails.pop(key, None)
            return True
        fails[key] = fails.get(key, 0) + 1
        if fails[key] >= 3:
            p["parked"] = True
            self.park(key, f"PR #{pr}: `gh pr {verb}` keeps failing — {done.err.strip()[:160]}",
                      issue=p["issue"], pr=pr, kind="gate", card=f"{verb} FAILED")
        return False

    def merge(self, p):
        if self.cfg.local:
            return self.merge_local(p)
        pr, n = p["number"], p["issue"]
        done = self._gh("pr", "merge", str(pr), "--squash")
        if done.rc != 0:
            return self.merge_failed(p, done.err)
        self.state["merged"].append({"pr": pr, "issue": n})
        self.finish(n, p["branch"])
        return True

    def merge_failed(self, p, err):
        """Count a failed merge; the second in a row parks the delivery."""
        pr, n = p["number"], p["issue"]
        fails = self.state["merge_failures"] = self.state.get("merge_failures", {})
        fails[str(pr)] = fails.get(str(pr), 0) + 1
        if fails[str(pr)] >= 2:
            p["parked"] = True
            self.park(f"pr{pr}:merge", f"{self.label(pr)}: merge failed twice — {err.strip()[:160]}",
                      issue=n, pr=pr, kind="gate", card="MERGE FAILED")
        return False

    def main_blocker(self):
        """Why the main checkout cannot take a merge now, or None.

        The default branch is checked out there, so that is where it moves: on the default
        branch and clean, the same bar sync_main sets for a fast-forward.
        """
        main = self.cfg.main
        if not main:
            return "no main checkout was found"
        head = self.sh(["git", "-C", main, "symbolic-ref", "--short", "HEAD"])
        if head.rc != 0 or head.out.strip() != self.cfg.default:
            return f"the main checkout is not on {self.cfg.default}"
        if not self.clean(main):
            return "the main checkout has uncommitted changes"
        return None

    def merge_local(self, p):
        """Squash the verified head into the default branch, then close the issue it delivers."""
        pr = n = p["number"]
        blocker = self.main_blocker()
        if blocker:
            p["parked"] = True
            self.park(f"pr{pr}:merge", f"{self.label(pr)} is ready to merge but {blocker}",
                      issue=n, pr=pr, kind="gate", card=f"MERGE BLOCKED: {blocker}"[:140])
            return False
        main, branch = self.cfg.main, p["branch"]
        prefix = branch.split("/")[0] if branch.split("/")[0] in ("fix", "feat", "chore") else "chore"
        title = self.state.issue(n).get("title") or f"issue #{n}"
        # the exact head the planner verified, not whatever the branch name points at by now
        done = self.sh(["git", "-C", main, "merge", "--squash", p["head"]])
        if done.rc == 0:
            done = self.sh(["git", "-C", main, "commit", "-m", f"{prefix}: {title} (#{n})\n\nCloses #{n}\n"])
        if done.rc != 0:
            self.sh(["git", "-C", main, "reset", "--merge"])
            return self.merge_failed(p, done.err or done.out)
        sha = self.sh(["git", "-C", main, "rev-parse", "HEAD"]).out.strip()
        self.state["merged"].append({"pr": pr, "issue": n, "sha": sha})
        # a merge is what closes the issue; there is no `Fixes #n` for a tracker to act on
        closed = self._sh("hew", "close", str(n), "--completed", "--reason",
                          f"Squash-merged {sha[:7]} into {self.cfg.default}")
        if closed.rc != 0:
            self.park(f"issue-{n}", f"#{n}: merged as {sha[:7]} but `hew close` failed — "
                      f"{closed.err.strip()[:160]}", issue=n)
        self.finish(n, branch)
        return True

    def finish(self, n, branch):
        """After a successful merge only: close out the card, then the branches."""
        iss = self.state.issue(n)
        iss["state"] = "merged"
        self.card(n, "merged", status="completed")
        removed = self.remove_worktree(iss.get("worktree"), iss.get("path"))
        if not self.cfg.local:  # a local branch has no remote ref
            self._sh("gh", "api", "-X", "DELETE", f"repos/{{owner}}/{{repo}}/git/refs/heads/{branch}")
        main = self.cfg.main
        if main and removed:
            self.sh(["git", "-C", main, "branch", "-D", branch])
            if iss.get("orca_branch"):
                self.sh(["git", "-C", main, "branch", "-d", iss["orca_branch"]])
        elif not removed:
            self.state["notes"].append({"type": "retained", "id": str(n), "subject": iss.get("path")})
        self.sync_main()

    def remove_worktree(self, wid, path):
        """Remove a clean, agentless worktree; True once Orca has dropped it."""
        if not wid or not path or not self.clean(path):
            return False
        try:
            self.orca.worktree_rm(wid)
        except OrcaError as e:
            self.log(f"could not remove {path}: {e}")
            return False
        for _ in range(30):  # removal is asynchronous
            if not any(w["id"] == wid for w in self.orca.worktrees()):
                return True
            self.sleep(1)
        return False

    # --- repairers ----------------------------------------------------------------------

    def start_repairer(self, p):
        pr, n = p["number"], p["issue"]
        iss = self.state.issue(n)
        key = f"pr{pr}:repair"
        if not iss.get("worktree") or not iss.get("path"):
            # a PR this Run holds no worktree for is reported, never edited
            p["parked"] = True
            self.park(key, f"{self.label(pr)}: needs a repair but this run holds no worktree for it",
                      issue=n, pr=pr, kind="gate")
            return False
        if self.writers() >= self.cfg.workers:
            return False  # waits for a free writer slot
        if not self.clean(iss["path"]):
            p["parked"] = True
            self.park(key, f"{self.label(pr)}: needs a repair but its worktree is dirty",
                      issue=n, pr=pr, kind="gate", card="DIRTY: not repaired")
            return False
        spec = render("repairer_local" if self.cfg.local else "repairer", n=n, pr=pr,
                      branch=p["branch"], default=self.cfg.default,
                      work_issue_dir=self.cfg.work_issue_dir, why=p.get("note") or "")
        try:
            self.start_agent("wc", str(pr), n, spec=spec, title=f"wc-{pr} · {REPAIRER}",
                             agent=REPAIRER, worktree=f"id:{iss['worktree']}", wid=iss["worktree"],
                             path=iss["path"], pr=pr)
        except OrcaError as e:
            p["parked"] = True
            self.park(key, f"{self.label(pr)}: the repairer could not start — {e}", issue=n, pr=pr,
                      kind="gate", card="REPAIR FAILED: repairer did not start")
            return False
        self.card(n, f"repairing {self.label(pr)} ({REPAIRER})", status="in-review")
        self.log(f"started wc-{pr} ({REPAIRER}): {p.get('note')}")
        return True

    def on_repair(self, a, out):
        n, pr = a["issue"], int(a["key"])
        status = out.get("status")
        if status == "resolved" and self.cfg.local:
            if self.verify(n, out.get("head")):  # the repairer ran the gate on the merged result
                return self.card(n, f"repaired {self.label(pr)}; ready to merge", status="in-review")
            out = {"status": "error", "reason": "the outcome's head is not the branch's head"}
        elif status == "resolved":
            head = self._gh("pr", "view", str(pr), "--json", "headRefOid", "--jq", ".headRefOid").out.strip()
            if head and head.startswith((out.get("head") or "\0")):
                self._gh("pr", "comment", str(pr), "--body-file", "-", input=(
                    "work-epic repair (repair agent):\n\n"
                    f"Repaired — gate: {out.get('gate', 'n/a')}.\n"
                    f"repaired-head: {out.get('head')}\n"))
                return self.card(n, f"repaired PR #{pr}; checking CI", status="in-review")
            out = {"status": "error", "reason": "the outcome's head is not the PR's head"}
        reason = out.get("reason") or f"repairer reported {status}"
        if self.cfg.local:  # no comment to count, so the pump counts the attempt itself
            iss = self.state.issue(n)
            iss["repair_failures"] = iss.get("repair_failures", 0) + 1
        else:
            self._gh("pr", "comment", str(pr), "--body-file", "-", input=(
                f"{REPAIR_FAIL_MARKER}:\n\n{reason}\n\nattempt-head: {out.get('head', 'unknown')}\n"))
        self.card(n, f"repair failed: {reason}"[:140], status="in-review")
        if status not in ("blocked",):
            self.halt(f"repairer for {self.label(pr)} errored: {reason}")

    # --- finishing --------------------------------------------------------------------------

    def parked_pr(self, p):
        if p.get("parked"):
            return True
        if p["action"] == "wait" and not p.get("reviewed", True):
            iss = self.state["issues"].get(str(p["issue"]), {})
            return iss.get("state") in (None, "review_failed", "failed", "error")
        return False

    def finished(self):
        if self.live():
            return False
        if self.halted:
            return True
        if self.progress:
            return False
        if any(i.get("state") == "delivered" for i in self.state["issues"].values()):
            return False
        if self.spawnable and self.last_resolve["spawnBudget"] > 0:
            return False
        return all(self.parked_pr(p) for p in self.last_plan["prs"])

    def summary(self):
        counts = self.last_resolve.get("counts") or {}
        open_left = sum(v for k, v in counts.items() if k not in ("closed",))
        parked = self.state["parked"]
        status = ("halted" if self.halted else "stopped" if parked or open_left else "drained")
        return {"status": status, "run": self.run_id, "scope": list(self.cfg.scope),
                "local": self.cfg.local,
                "issues": self.state["issues"], "merged": self.state["merged"],
                "parked": parked, "notes": self.state["notes"], "counts": counts,
                "spawnBudget": self.last_resolve.get("spawnBudget"),
                "openPrs": [{"number": p["number"], "action": p["action"], "note": p.get("note")}
                            for p in self.last_plan["prs"]],
                "halted": self.halted}

    def stop_card(self, summary):
        text = f"coordinator stopped: {summary['status']}"
        self.orca.worktree_set(self.cfg.coord["id"], status="in-review",
                               comment=f"work-epic · {self.run_id} · {text}",
                               unread=True if summary["parked"] else None)


def render_report(s):
    """The human-facing end-of-run report."""
    lines = [f"Run {s['run']} over {' '.join('#%d' % n for n in s['scope'])}: {s['status'].upper()}"]
    for n, iss in sorted(s["issues"].items(), key=lambda kv: int(kv[0])):
        bits = [iss.get("state", "?")]
        if iss.get("pr") and not s.get("local"):
            bits.append(f"PR #{iss['pr']}")
        if iss.get("large"):
            bits.append("large")
        if iss.get("findings"):
            bits.append("findings " + ", ".join(f"#{f}" for f in iss["findings"]))
        lines.append(f"  #{n}: " + "; ".join(bits))
    if s["merged"]:
        lines.append("  merged: " + ", ".join(
            f"#{m['issue']}" if s.get("local") else f"PR #{m['pr']}" for m in s["merged"]))
    c = s["counts"]
    if c:
        lines.append("  resolver: " + ", ".join(f"{k} {v}" for k, v in c.items()))
        lines.append(f"  spawn budget {s['spawnBudget']}; "
                     f"{'stopped, not drained' if s['status'] != 'drained' else 'drained'}")
        if not any(v for k, v in c.items() if k != "closed"):
            lines.append("  every issue in scope is closed — a human closes the epic")
    if s["parked"]:
        lines.append("  decisions needed:")
        lines += [f"    {v['text']}" for v in s["parked"].values()]
    for note in s["notes"]:
        lines.append(f"  note: {note['type']} {note['id']} {note.get('subject', '')}".rstrip())
    return "\n".join(lines)


EXIT_CODES = {"drained": 0, "stopped": 10, "halted": 3}
