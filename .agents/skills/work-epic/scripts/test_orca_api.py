"""Tests for orca_api: the envelope, and the shapes captured from a live Orca 1.4.x.

Run with `python3 -m unittest discover plugins/work-epic/skills/work-epic/scripts`
— stdlib only; `sh` is a scripted fake.
"""

import json
import os
import unittest
from unittest.mock import patch

from orca_api import (Orca, OrcaError, Proc, find_key, orca_command, parse_envelope,
                      payload, settled)


def ok(result):
    return Proc(0, json.dumps({"id": "x", "ok": True, "result": result}), "")


def err(code, message="m", data=None, result=None, rc=1):
    env = {"id": "x", "ok": False, "error": {"code": code, "message": message, "data": data}}
    if result is not None:
        env["result"] = result
    return Proc(rc, json.dumps(env), "")


class Scripted:
    """An sh fake that returns queued responses and records each argv."""

    def __init__(self, *responses):
        self.responses, self.calls = list(responses), []

    def __call__(self, argv, cwd=None, input=None):
        self.calls.append(argv)
        return self.responses.pop(0)


def orca(*responses):
    sh = Scripted(*responses)
    return Orca(sh, command=["orca"]), sh


class Envelope(unittest.TestCase):
    def test_keepalive_lines_do_not_break_parsing(self):
        text = '{"_keepalive": true}\n{"ok": true, "result": {"a": 1}}\n'
        self.assertEqual(parse_envelope(text)["result"], {"a": 1})

    def test_result_is_returned_and_json_is_requested(self):
        o, sh = orca(ok({"runtime": {"state": "ready"}}))
        self.assertTrue(o.runtime_ready())
        self.assertEqual(sh.calls[0], ["orca", "status", "--json"])

    def test_error_carries_code_data_and_receipt(self):
        o, _ = orca(err("run_required", "No Run is bound", data={"effectsApplied": False},
                        result={"stage": "x"}))
        with self.assertRaises(OrcaError) as raised:
            o.tasks("run_1")
        e = raised.exception
        self.assertEqual((e.code, e.data["effectsApplied"], e.result), ("run_required", False, {"stage": "x"}))

    def test_garbage_is_unparsable_not_a_crash(self):
        o, _ = orca(Proc(1, "Segmentation fault", ""))
        with self.assertRaises(OrcaError) as raised:
            o.runs()
        self.assertEqual(raised.exception.code, "unparsable")

    def test_nonzero_exit_with_ok_true_is_still_a_failure(self):
        o, _ = orca(Proc(1, json.dumps({"ok": True, "result": {}}), ""))
        with self.assertRaises(OrcaError) as raised:
            o.runs()
        self.assertEqual(raised.exception.code, "exit_status")


class Executable(unittest.TestCase):
    def test_override_wins(self):
        with patch.dict(os.environ, {"ORCA_CLI_COMMAND": "orca-dev --flag"}):
            self.assertEqual(orca_command(), ["orca-dev", "--flag"])

    def test_dev_and_ide_come_before_the_screen_reader_name(self):
        with patch.dict(os.environ, {}, clear=False), \
             patch("shutil.which", side_effect=lambda n: "/bin/" + n if n in ("orca", "orca-ide") else None):
            os.environ.pop("ORCA_CLI_COMMAND", None)
            self.assertEqual(orca_command(), ["orca-ide"])

    def test_none_found_is_an_error(self):
        with patch.dict(os.environ, {}), patch("shutil.which", return_value=None):
            os.environ.pop("ORCA_CLI_COMMAND", None)
            with self.assertRaises(OrcaError):
                orca_command()


class Workers(unittest.TestCase):
    def test_start_receipt_takes_the_worktree_from_effects(self):
        receipt = {"runId": "run_1", "taskId": "task_1", "dispatchId": "ctx_1", "state": "ready",
                   "effects": [{"kind": "terminal", "action": "created", "id": "term_1"},
                               {"kind": "worktree", "action": "created_child", "id": "repo::/wt/we-21"}],
                   "residualResources": []}
        o, sh = orca(ok(receipt))
        got = o.worker_start(spec="do it", title="we-21 · opencode", agent="opencode",
                             worktree="new-child", repo="id:repo", name="we-21",
                             base_branch="origin/main", comment="c", setup="inherit")
        self.assertEqual((got.task, got.dispatch, got.worktree), ("task_1", "ctx_1", "repo::/wt/we-21"))
        argv = sh.calls[0]
        for flag, value in (("--worktree", "new-child"), ("--agent", "opencode"),
                            ("--task-title", "we-21 · opencode"), ("--setup", "inherit")):
            self.assertEqual(argv[argv.index(flag) + 1], value)
        self.assertNotIn("--display-name", argv)

    def test_existing_worktree_receipt_has_no_worktree_effect(self):
        o, _ = orca(ok({"taskId": "t", "dispatchId": "d", "effects": []}))
        got = o.worker_start(spec="s", title="t", agent="claude", worktree="id:x")
        self.assertIsNone(got.worktree)

    def test_failed_start_keeps_the_receipt(self):
        o, _ = orca(err("start_failed", result={"failedStage": "launch", "dispatchId": "ctx_9",
                                                 "residualResources": [{"kind": "terminal"}]}))
        with self.assertRaises(OrcaError) as raised:
            o.worker_start(spec="s", title="t", agent="claude", worktree="new-child")
        self.assertEqual(find_key(raised.exception.result, "dispatchId"), "ctx_9")

    def test_workers_follow_pages(self):
        o, sh = orca(ok({"workers": [{"dispatchId": "a"}], "page": {"hasMore": True, "nextCursor": "c1"}}),
                     ok({"workers": [{"dispatchId": "b"}], "page": {"hasMore": False, "nextCursor": None}}))
        self.assertEqual([w["dispatchId"] for w in o.workers("run_1")], ["a", "b"])
        self.assertIn("--cursor", sh.calls[1])

    def test_settled_rows_from_a_live_run(self):
        done = {"workerState": "succeeded", "dispatchStatus": "completed", "terminalState": "released"}
        failed = {"workerState": "failed", "dispatchStatus": "failed"}
        running = {"workerState": "running", "dispatchStatus": "dispatched"}
        retained = {"workerState": "succeeded", "dispatchStatus": "completed", "terminalState": "retained",
                    "projection": {"liveness": {"verdict": "unverifiable"}}}
        self.assertEqual([settled(r) for r in (done, failed, running, retained)],
                         [True, True, False, True])


class Mailbox(unittest.TestCase):
    def test_check_waits_acks_and_filters(self):
        delivery = {"runId": "run_1", "deliveryId": "delivery_1", "count": 1, "timedOut": False,
                    "messages": [{"id": "msg_1", "type": "worker_done",
                                  "payload": json.dumps({"dispatchId": "ctx_1", "outcome": "succeeded"})}]}
        o, sh = orca(ok(delivery))
        res = o.check("run_1", timeout_ms=1000, ack="delivery_0")
        self.assertEqual(res["deliveryId"], "delivery_1")
        argv = sh.calls[0]
        self.assertEqual(argv[argv.index("--ack") + 1], "delivery_0")
        self.assertIn("--wait", argv)
        self.assertEqual(payload(res["messages"][0])["dispatchId"], "ctx_1")

    def test_payload_survives_garbage(self):
        self.assertEqual(payload({"payload": "not json"}), {})
        self.assertEqual(payload({}), {})


class Worktrees(unittest.TestCase):
    def test_set_flags(self):
        o, sh = orca(ok({"worktree": {"id": "w"}}))
        o.worktree_set("repo::/p", comment="c", status="in-review", issue=21, pr=34, unread=True)
        self.assertEqual(sh.calls[0][1:], ["worktree", "set", "--worktree", "id:repo::/p", "--comment", "c",
                                           "--workspace-status", "in-review", "--issue", "21", "--pr", "34",
                                           "--unread", "--json"])

    def test_unread_false_clears_and_none_leaves_it(self):
        o, sh = orca(ok({"worktree": {}}), ok({"worktree": {}}))
        o.worktree_set("w", unread=False)
        o.worktree_set("w", comment="x")
        self.assertIn("--read", sh.calls[0])
        self.assertNotIn("--read", sh.calls[1])
        self.assertNotIn("--unread", sh.calls[1])


if __name__ == "__main__":
    unittest.main()
