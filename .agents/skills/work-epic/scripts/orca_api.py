"""A thin wrapper over the Orca CLI, so the pump never builds argv by hand.

Every Orca command answers with an envelope on stdout — `{"ok": true, "result":
{...}}` or `{"ok": false, "error": {"code", "message", "data"}}` — and keepalive
lines on stderr. `Orca.call` returns `result` or raises `OrcaError`; the typed
helpers below name the handful of commands the pump uses and the fields it
reads from them. Shapes were captured from a live Orca (1.4.x) and are pinned
in test_orca_api.py.

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


def settled(row):
    return (row.get("workerState") in SETTLED_WORKER_STATES
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
        run = self.call("orchestration", "run-create", "--objective", objective).get("run") or {}
        return run["id"]

    def runs(self):
        return self.call("orchestration", "run-list").get("runs") or []

    def run_use(self, run_id):
        self.call("orchestration", "run-use", "--id", run_id)

    def run_current(self):
        return (self.call("orchestration", "run-current").get("run") or {}).get("id")

    # --- tasks and workers -------------------------------------------------

    def tasks(self, run_id):
        return self.call("orchestration", "task-list", "--run", run_id).get("tasks") or []

    def workers(self, run_id):
        """Every worker row for the Run, following pages."""
        rows, cursor = [], None
        while True:
            args = ["orchestration", "worker-list", "--run", run_id, "--limit", "100"]
            if cursor:
                args += ["--cursor", cursor]
            result = self.call(*args)
            rows += result.get("workers") or []
            page = result.get("page") or {}
            cursor = page.get("nextCursor")
            if not (page.get("hasMore") and cursor):
                return rows

    def worker_show(self, dispatch):
        return self.call("orchestration", "worker-show", "--dispatch", dispatch)

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
        wt = None
        for effect in result.get("effects") or []:
            if effect.get("kind") == "worktree" and effect.get("id"):
                wt = effect["id"]
        return StartReceipt(result.get("taskId"), result.get("dispatchId"), wt, result)

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
