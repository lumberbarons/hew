"""A thin wrapper over the Orca CLI, so the pump never builds argv by hand.

Every Orca command answers with an envelope on stdout — `{"ok": true, "result":
{...}}` or `{"ok": false, "error": {"code", "message", "data"}}` — and keepalive
lines on stderr. `Orca.call` returns `result` or raises `OrcaError`; the typed
helpers below name the handful of commands the pump uses and the fields it
reads from them. Orca 1.4.267 flattened and renamed several of those fields;
`run_id`, `normalise_worker`, and `normalise_show` accept both shapes so the
pump reads one set of fields on every host. Shapes were captured from a live
Orca (1.4.x) and are pinned in test_orca_api.py.

All effects go through the injected `sh`, so tests drive the pump against a fake.
"""

import json
import os
import shlex
import shutil
import subprocess
from typing import NamedTuple


class Proc(NamedTuple):
    rc: int
    out: str
    err: str


def real_sh(argv, cwd=None, input=None):
    p = subprocess.run(argv, capture_output=True, text=True, cwd=cwd, input=input)
    return Proc(p.returncode, p.stdout, p.stderr)


class OrcaError(Exception):
    """An Orca command failed. `code` is Orca's stable error.code."""

    def __init__(self, code, message, data=None, result=None, argv=None):
        super().__init__(f"{code}: {message}")
        self.code, self.message = code, message
        self.data, self.result, self.argv = data or {}, result, argv


def orca_command():
    """The Orca executable as an argv prefix: ORCA_CLI_COMMAND, else the first on PATH.

    `orca-dev` and `orca-ide` come before `orca` because on Linux `orca` can be
    the screen reader.
    """
    override = os.environ.get("ORCA_CLI_COMMAND")
    if override:
        return shlex.split(override)
    for name in ("orca-dev", "orca-ide", "orca"):
        if shutil.which(name):
            return [name]
    raise OrcaError("not_found", "no orca executable on PATH (set ORCA_CLI_COMMAND)")


def parse_envelope(text):
    """The envelope in `text`, tolerating keepalive lines mixed into stdout."""
    kept = [
        line for line in text.splitlines()
        if not line.startswith(('{"_keepalive"', '{"_heartbeat"'))
    ]
    body = "\n".join(kept).strip()
    try:
        return json.loads(body)
    except ValueError:
        start, end = body.find("{"), body.rfind("}")
        if start >= 0 and end > start:
            try:
                return json.loads(body[start:end + 1])
            except ValueError:
                pass
    return None


def find_key(obj, *names):
    """First value under any of `names`, searching nested dicts and lists."""
    if isinstance(obj, dict):
        for name in names:
            if obj.get(name) is not None:
                return obj[name]
        for value in obj.values():
            found = find_key(value, *names)
            if found is not None:
                return found
    elif isinstance(obj, list):
        for value in obj:
            found = find_key(value, *names)
            if found is not None:
                return found
    return None


def payload(message):
    """A message's `payload` — Orca stores it as a JSON string."""
    raw = message.get("payload")
    if isinstance(raw, dict):
        return raw
    try:
        return json.loads(raw) if raw else {}
    except ValueError:
        return {}


# workerState values that mean the Dispatch is over, whatever the terminal did
SETTLED_WORKER_STATES = {"succeeded", "failed"}


def run_id(result):
    """The Run id, from either `runId` or the older nested `run.id`."""
    return result.get("runId") or (result.get("run") or {}).get("id")


def normalise_worker(row):
    """A worker row with the nested keys the pump reads filled from Orca 1.4.267's flat ones.

    Orca removed `projection` and renamed `workerState` to `state`, `attention` to a
    top-level category list, and so on. Old hosts already carry the nested keys, so this
    leaves their rows untouched apart from copying them.
    """
    row = dict(row)
    state = row.get("state")
    if row.get("workerState") is None and state is not None:
        row["workerState"] = state
    if row.get("dispatchStatus") is None:
        if state in ("succeeded", "completed"):
            row["dispatchStatus"] = "completed"
        elif state in ("failed", "stopped", "abandoned"):
            # 1.4.267 treats stopped and abandoned as settled too; the nested rows the
            # pump was written against reported those as a failed Dispatch.
            row["dispatchStatus"] = "failed"

    projection = dict(row.get("projection") or {})
    if isinstance(row.get("workspace"), str) and row["workspace"]:
        workspace = dict(projection.get("workspace") or {})
        workspace.setdefault("id", row["workspace"])
        projection["workspace"] = workspace
    if isinstance(row.get("liveness"), str) and row["liveness"]:
        liveness = dict(projection.get("liveness") or {})
        liveness.setdefault("verdict", row["liveness"])
        projection["liveness"] = liveness
    if isinstance(row.get("attention"), list):
        attention = dict(projection.get("attention") or {})
        attention.setdefault("categories", list(row["attention"]))
        attention.setdefault("requiresAction", bool(row["attention"]))
        projection["attention"] = attention
    if projection:
        row["projection"] = projection
    return row


def normalise_show(result):
    """A `worker-show` result with `observation.agentWait` filled from `waitingOnHuman`.

    `waitingOnHuman` is absent when the worker is free and `"unknown"` when Orca could
    not look; both mean the pump should not raise the parked-on-a-dialog flag.
    """
    result = dict(result)
    observation = dict(result.get("observation") or {})
    if "agentWait" not in observation and "waitingOnHuman" in result:
        wait = result.get("waitingOnHuman")
        observation["agentWait"] = None if wait in (None, "unknown") else wait
    if observation:
        result["observation"] = observation
    return result


def settled(row):
    state = row.get("workerState") or row.get("state")
    return (state in SETTLED_WORKER_STATES
            or row.get("dispatchStatus") in ("completed", "failed"))


class StartReceipt(NamedTuple):
    task: str
    dispatch: str
    worktree: str  # the worktree id Orca created, or None for an existing one
    raw: dict


class Orca:
    def __init__(self, sh=real_sh, command=None, cwd=None):
        self.sh = sh
        self.command = command
        self.cwd = cwd

    def call(self, *args):
        """Run one command and return its `result`; raise OrcaError on failure."""
        argv = [*(self.command or orca_command()), *args, "--json"]
        proc = self.sh(argv, cwd=self.cwd)
        env = parse_envelope(proc.out)
        if env is None:
            raise OrcaError("unparsable", (proc.err or proc.out).strip()[:300], argv=argv)
        if not env.get("ok"):
            err = env.get("error") or {}
            raise OrcaError(err.get("code", "failed"), err.get("message", ""),
                            err.get("data"), env.get("result"), argv)
        if proc.rc != 0:
            raise OrcaError("exit_status", f"exit {proc.rc}", None, env.get("result"), argv)
        return env.get("result") or {}

    # --- runtime ---------------------------------------------------------

    def runtime_ready(self):
        return (self.call("status").get("runtime") or {}).get("state") == "ready"

    def current_worktree(self):
        wt = self.call("worktree", "current").get("worktree") or {}
        return {"id": wt.get("id"), "repoId": wt.get("repoId"), "path": wt.get("path")}

    # --- runs ------------------------------------------------------------

    def run_create(self, objective):
        return run_id(self.call("orchestration", "run-create", "--objective", objective))

    def runs(self):
        return self.call("orchestration", "run-list").get("runs") or []

    def run_use(self, run_id):
        self.call("orchestration", "run-use", "--id", run_id)

    def run_current(self):
        return run_id(self.call("orchestration", "run-current"))

    # --- tasks and workers -------------------------------------------------

    def tasks(self, run_id):
        return self.call("orchestration", "task-list", "--run", run_id).get("tasks") or []

    def workers(self, run_id):
        """Every worker row for the Run, following pages.

        1.4.267 puts `nextCursor` at the top level; older hosts nest it under
        `page` with a `hasMore` flag.
        """
        rows, cursor = [], None
        while True:
            args = ["orchestration", "worker-list", "--run", run_id, "--limit", "100"]
            if cursor:
                args += ["--cursor", cursor]
            result = self.call(*args)
            rows += [normalise_worker(row) for row in result.get("workers") or []]
            page = result.get("page") or {}
            cursor = result.get("nextCursor") or (
                page.get("nextCursor") if page.get("hasMore") else None)
            if not cursor:
                return rows

    def worker_show(self, dispatch):
        return normalise_show(self.call("orchestration", "worker-show", "--dispatch", dispatch))

    def worker_release(self, dispatch):
        return self.call("orchestration", "worker-release", "--dispatch", dispatch)

    def worker_start(self, *, spec, title, agent, worktree, repo=None, name=None,
                     display_name=None, base_branch=None, comment=None, setup=None):
        """Start a supervised worker; raises OrcaError (with the receipt) on failure."""
        args = ["orchestration", "worker-start", "--spec", spec, "--task-title", title,
                "--agent", agent, "--worktree", worktree]
        for flag, value in (("--repo", repo), ("--name", name),
                            ("--display-name", display_name),
                            ("--base-branch", base_branch), ("--comment", comment),
                            ("--setup", setup)):
            if value is not None:
                args += [flag, value]
        result = self.call(*args)
        # 1.4.267 reports the created worktree as a top-level `workspace`; older hosts
        # report it in `effects`.
        created = result.get("workspace")
        if isinstance(created, dict):
            created = created.get("id")
        if not created:
            for effect in result.get("effects") or []:
                if effect.get("kind") == "worktree" and effect.get("id"):
                    created = effect["id"]
                    break
        return StartReceipt(result.get("taskId"), result.get("dispatchId"), created, result)

    def check(self, run_id, *, timeout_ms, ack=None, types="worker_done,escalation,question"):
        """Wait for the Run's next Delivery, acknowledging the previous one first."""
        args = ["orchestration", "check", "--run", run_id, "--wait", "--types", types,
                "--timeout-ms", str(timeout_ms)]
        if ack:
            args += ["--ack", ack]
        return self.call(*args)

    # --- worktrees ---------------------------------------------------------

    def worktrees(self):
        return self.call("worktree", "list").get("worktrees") or []

    def worktree_set(self, wid, *, comment=None, status=None, issue=None, pr=None,
                     unread=None):
        args = ["worktree", "set", "--worktree", f"id:{wid}"]
        if comment is not None:
            args += ["--comment", comment]
        if status is not None:
            args += ["--workspace-status", status]
        if issue is not None:
            args += ["--issue", str(issue)]
        if pr is not None:
            args += ["--pr", str(pr)]
        if unread is not None:
            args.append("--unread" if unread else "--read")
        return self.call(*args).get("worktree") or {}

    def worktree_rm(self, wid):
        self.call("worktree", "rm", "--worktree", f"id:{wid}", "--force")
