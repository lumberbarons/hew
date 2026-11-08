"""Exercise real git; only the hew transport is substituted."""

import argparse
import fcntl
import importlib.util
import json
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("land_local", Path(__file__).with_name("land_local.py"))
landing = importlib.util.module_from_spec(SPEC)
if SPEC.loader:
    SPEC.loader.exec_module(landing)


class LandingTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.repo = Path(self.tmp.name)
        self.git("init", "-b", "main")
        self.git("config", "user.name", "Test")
        self.git("config", "user.email", "test@example.test")
        self.git("config", "commit.gpgsign", "false")
        (self.repo / "code").write_text("before\n")
        self.git("add", "code")
        self.git("commit", "-m", "base")
        self.base = self.git("rev-parse", "HEAD")
        self.git("switch", "-c", "fix/42-example")
        (self.repo / "code").write_text("after\n")
        self.git("commit", "-am", "fix: example")
        self.head = self.git("rev-parse", "HEAD")
        self.git("switch", "main")
        self.args = argparse.Namespace(
            checkout=str(self.repo), issue=42, branch="fix/42-example", base="main",
            head=self.head, base_head=self.base, review="Reviewed change; no blockers",
            testing="project-check: passed",
        )
        self.closed = False
        self.close_fails = False
        self.close_calls = []
        self.backend = "local"
        self.tracker_id = "fixture"
        self.real_run = landing.run
        self.mock = patch.object(landing, "run", side_effect=self.transport)
        self.mock.start()
        self.addCleanup(self.mock.stop)

    def git(self, *args):
        return subprocess.run(["git", *args], cwd=self.repo, check=True,
                              capture_output=True, text=True).stdout.strip()

    def transport(self, args, cwd):
        if args[0] != "hew":
            return self.real_run(args, cwd)
        if args[1] == "config":
            return json.dumps({"backend": self.backend, "id": self.tracker_id})
        if args[1] == "show":
            return json.dumps({"number": 42, "title": "Example", "state": "closed" if self.closed else "open"})
        if args[1] == "close":
            # Closing must happen after a committed, clean squash on the default branch.
            self.assertEqual(self.git("branch", "--show-current"), self.args.base)
            self.assertEqual(self.git("status", "--porcelain"), "")
            self.assertEqual((self.repo / "code").read_text(), "after\n")
            self.assertNotEqual(self.git("rev-parse", "HEAD"), self.base)
            self.close_calls.append(args)
            if self.close_fails:
                raise landing.Failure("tracker unavailable")
            self.closed = True
            return "closed"
        self.fail(f"unexpected command: {args}")

    def receipt(self):
        return self.repo / ".git" / "finish-issue" / "42.json"

    def test_squashes_exact_head_then_closes_with_commit_evidence(self):
        result = landing.land(self.args)
        self.assertEqual(result["status"], "finished")
        self.assertEqual(self.git("rev-list", "--count", "main"), "2")
        self.assertEqual(self.git("rev-parse", "main^{tree}"), self.git("rev-parse", f"{self.head}^{{tree}}"))
        self.assertIn("--completed", self.close_calls[0])
        self.assertIn(result["commit"], self.close_calls[0][-1])
        self.assertIn("project-check: passed", self.git("log", "-1", "--format=%B"))
        self.assertTrue(self.git("rev-parse", "refs/heads/fix/42-example"))

    def test_close_failure_resumes_without_another_squash(self):
        self.close_fails = True
        first = landing.land(self.args)
        self.assertEqual(first["status"], "closure_pending")
        commit = self.git("rev-parse", "main")
        self.close_fails = False
        second = landing.land(self.args)
        self.assertEqual(second["status"], "finished")
        self.assertEqual(second["commit"], commit)
        self.assertEqual(self.git("rev-list", "--count", "main"), "2")
        self.assertEqual(len(self.close_calls), 2)

    def test_completed_retry_does_not_close_twice(self):
        first = landing.land(self.args)
        self.assertEqual(landing.land(self.args)["commit"], first["commit"])
        self.assertEqual(len(self.close_calls), 1)

    def test_completed_receipt_does_not_hide_reopened_issue(self):
        landing.land(self.args)
        self.closed = False
        self.assert_refused("reopened")
        self.assertFalse(self.closed)

    def test_receipt_cannot_close_same_number_in_reinitialized_tracker(self):
        self.close_fails = True
        landing.land(self.args)
        self.tracker_id = "replacement-tracker"
        self.assert_refused("receipt")

    def test_close_retry_survives_later_default_commit_and_deleted_branch(self):
        self.close_fails = True
        first = landing.land(self.args)
        self.git("commit", "--allow-empty", "-m", "later default commit")
        self.git("branch", "-D", self.args.branch)
        self.close_fails = False
        self.assertEqual(landing.land(self.args)["commit"], first["commit"])
        self.assertEqual(self.git("rev-list", "--count", "main"), "3")

    def test_failed_commit_hook_does_not_close_and_retry_can_commit(self):
        hook = self.repo / ".git" / "hooks" / "pre-commit"
        hook.write_text("#!/bin/sh\nexit 1\n")
        hook.chmod(0o755)
        self.assert_refused("git commit")
        self.assertEqual(self.git("rev-parse", "main"), self.base)
        self.assertEqual((self.repo / "code").read_text(), "after\n")
        hook.unlink()
        self.assertEqual(landing.land(self.args)["status"], "finished")

    def test_can_land_from_linked_default_worktree(self):
        self.git("switch", self.args.branch)
        default_checkout = self.repo / "default-checkout"
        self.git("worktree", "add", str(default_checkout), "main")
        self.args.checkout = str(default_checkout)
        # Assertions in the hew stub should inspect the landing checkout too.
        self.repo = default_checkout
        self.assertEqual(landing.land(self.args)["status"], "finished")

    def test_recovers_commit_written_before_receipt_update(self):
        save = landing.save

        def crash_on_merge(path, record):
            if record.get("commit"):
                raise KeyboardInterrupt("power lost after commit")
            return save(path, record)

        with patch.object(landing, "save", side_effect=crash_on_merge), self.assertRaises(KeyboardInterrupt):
            landing.land(self.args)
        commit = self.git("rev-parse", "main")
        self.assertNotIn("commit", json.loads(self.receipt().read_text()))
        self.assertEqual(landing.land(self.args)["commit"], commit)
        self.assertEqual(self.git("rev-list", "--count", "main"), "2")

    def test_recovers_staged_squash_after_interruption(self):
        real = self.real_run

        def interrupt_commit(args, cwd):
            if args[:2] == ["git", "commit"]:
                raise KeyboardInterrupt("power lost before commit")
            return real(args, cwd)

        self.real_run = interrupt_commit
        with self.assertRaises(KeyboardInterrupt):
            landing.land(self.args)
        self.real_run = real
        self.assertNotEqual(self.git("diff", "--cached", "--name-only"), "")
        self.assertEqual(landing.land(self.args)["status"], "finished")

    def assert_refused(self, message):
        calls = len(self.close_calls)
        with self.assertRaisesRegex(landing.Failure, message):
            landing.land(self.args)
        self.assertEqual(len(self.close_calls), calls)

    def test_refuses_changed_issue_head(self):
        self.git("switch", "fix/42-example")
        (self.repo / "other").write_text("unverified\n")
        self.git("add", "other")
        self.git("commit", "-m", "unverified")
        self.git("switch", "main")
        self.assert_refused("head changed")

    def test_refuses_changed_default_head(self):
        self.git("commit", "--allow-empty", "-m", "new base")
        self.assert_refused("default branch changed")

    def test_refuses_branch_missing_current_default(self):
        self.git("commit", "--allow-empty", "-m", "new base")
        self.args.base_head = self.git("rev-parse", "HEAD")
        self.assert_refused("does not contain")

    def test_refuses_dirty_checkout_without_touching_it(self):
        (self.repo / "code").write_text("user edit\n")
        self.assert_refused("clean")
        self.assertEqual((self.repo / "code").read_text(), "user edit\n")

    def test_refuses_wrong_checkout_branch(self):
        self.git("switch", "fix/42-example")
        self.assert_refused("checked out")

    def test_other_landing_holds_repository_lock(self):
        receipts = self.receipt().parent
        receipts.mkdir()
        with (receipts / "land.lock").open("a") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            self.assert_refused("another local landing")
        self.assertEqual(self.git("rev-parse", "main"), self.base)

    def test_honours_non_main_default_branch(self):
        self.git("branch", "-m", "trunk")
        self.args.base = "trunk"
        self.assertEqual(landing.land(self.args)["status"], "finished")

    def test_commit_hook_cannot_change_verified_tree_and_close(self):
        hook = self.repo / ".git" / "hooks" / "pre-commit"
        hook.write_text("#!/bin/sh\nprintf 'unverified\\n' > other\ngit add other\n")
        hook.chmod(0o755)
        self.assert_refused("commit differs")

    def test_refuses_github_backend(self):
        self.backend = "github"
        self.assert_refused("local tracker")

    def test_corrupt_receipt_cannot_start_a_squash(self):
        self.receipt().parent.mkdir()
        self.receipt().write_text("{}")
        self.assert_refused("invalid receipt")
        self.assertEqual(self.git("status", "--porcelain"), "")
        self.assertEqual(self.git("rev-parse", "main"), self.base)

    def test_refuses_closed_issue_without_own_merge_receipt(self):
        self.closed = True
        self.assert_refused("already closed")

    def test_refuses_different_target_on_retry(self):
        landing.land(self.args)
        self.args.branch = "fix/42-different"
        self.assert_refused("receipt")

    def test_refuses_resuming_unrelated_staged_changes(self):
        self.close_fails = True
        landing.land(self.args)
        record = json.loads(self.receipt().read_text())
        record.pop("commit")
        record["status"] = "prepared"
        landing.save(self.receipt(), record)
        self.git("reset", "--hard", self.base)
        (self.repo / "code").write_text("unrelated\n")
        self.git("add", "code")
        self.assert_refused("index")


if __name__ == "__main__":
    unittest.main()
