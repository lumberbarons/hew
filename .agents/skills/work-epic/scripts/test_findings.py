"""Tests for findings: filing, the deterministic dedup rule, closing the fixed, the round comment.

Run with `python3 -m unittest discover plugins/work-epic/skills/work-epic/scripts`
— stdlib only; the converter, dedup helper and hew are a scripted fake.
"""

import json
import os
import tempfile
import unittest

from findings import (FilingError, Filer, round_comment, read_outcome, unfixed_blocker)
from orca_api import Proc
from pr_state import REVIEW_MARKER


def finding(title, priority, **kw):
    f = {"title": title, "priority": priority, "files": ["a.go"], "locations": ["a.go:10"],
         "explanation": "It breaks.", "fix": "Fix it.", "pattern": "p"}
    f.update(kw)
    return f


class FakeTools:
    """Plays the converter, dedup helper and hew for one PR #34 reviewing issue #21."""

    def __init__(self, outdir, outcome, *, converter_rc=0, dedup=None, tracker=None):
        self.outdir, self.outcome = outdir, outcome
        self.converter_rc, self.dedup, self.tracker = converter_rc, dedup or [], tracker or []
        self.calls = []

    def __call__(self, argv, cwd=None, input=None):
        self.calls.append(argv)
        if argv[0] == "uv":
            script = os.path.basename(argv[3])
            if script == "findings_to_plan.py":
                out = argv[argv.index("--out") + 1]
                lines = [json.dumps({"title": f["title"]}) for f in self.outcome["findings"]
                         if f["priority"] in ("P0", "P1", "P2")]
                with open(out, "w") as f:
                    f.write("".join(l + "\n" for l in lines))
                return Proc(self.converter_rc, "", "converter said no" if self.converter_rc else "")
            return Proc(0, "".join(json.dumps(d) + "\n" for d in self.dedup), "")
        if argv[:2] == ["hew", "list"]:
            return Proc(0, "".join(json.dumps(r) + "\n" for r in self.tracker), "")
        return Proc(0, "", "")


class Filing(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.outcome = {"status": "reviewed", "skill": "code", "findings": [
            finding("Leaky handler", "P1", fixed=True, fixedIn="9f2c1ab"),
            finding("Dead branch", "P2", fixed=False, unfixedReason="needs a schema decision"),
            finding("Naming", "P3"),
        ]}
        self.path = os.path.join(self.tmp.name, "wr-34.json")
        with open(self.path, "w") as f:
            json.dump(self.outcome, f)

    def filer(self, tools):
        return Filer(tools, cwd=self.tmp.name, raise_dir="/skills/raise-issues", outdir=self.tmp.name)

    def tracker_after_apply(self):
        """What hew holds once the plan is applied: the P1 (fixed-in, open) and the P2."""
        marker = "review-of: #21 (PR #34)"
        return [
            {"number": 41, "state": "open", "title": "Leaky handler",
             "body": f"review-key: code/p/a.go\n{marker}\nfixed-in: 9f2c1ab\n"},
            {"number": 42, "state": "open", "title": "Dead branch",
             "body": f"review-key: code/p/a.go\n{marker}\n"},
        ]

    def test_files_closes_the_fixed_and_numbers_every_filed_finding(self):
        tools = FakeTools(self.tmp.name, self.outcome, tracker=self.tracker_after_apply())
        records = self.filer(tools).file(pr=34, issue=21, outcome_path=self.path)
        self.assertEqual([(r["title"], r["number"]) for r in records],
                         [("Leaky handler", 41), ("Dead branch", 42), ("Naming", None)])
        closes = [c for c in tools.calls if c[:2] == ["hew", "close"]]
        self.assertEqual(len(closes), 1)  # the unfixed P2 stays open, the P3 was never filed
        self.assertEqual(closes[0][2], "41")
        self.assertIn("--completed", closes[0])
        self.assertIn("9f2c1ab", closes[0][closes[0].index("--reason") + 1])

    def test_applies_after_a_dry_run(self):
        tools = FakeTools(self.tmp.name, self.outcome, tracker=self.tracker_after_apply())
        self.filer(tools).file(pr=34, issue=21, outcome_path=self.path)
        applies = [c for c in tools.calls if c[:2] == ["hew", "apply"]]
        self.assertEqual(["--dry-run" in c for c in applies], [True, False])

    def test_same_key_target_and_paths_is_a_duplicate_not_refiled(self):
        # plan line 2 ("Dead branch") already exists as #9 against the same target and paths
        dedup = [{"line": 1, "candidates": []},
                 {"line": 2, "candidates": [{"number": 9, "state": "open", "same_paths": True}]}]
        tools = FakeTools(self.tmp.name, self.outcome, dedup=dedup, tracker=self.tracker_after_apply()[:1])
        records = self.filer(tools).file(pr=34, issue=21, outcome_path=self.path)
        self.assertEqual(records[1]["number"], 9)
        with open(os.path.join(self.tmp.name, "wr-34.plan.jsonl")) as f:
            self.assertEqual([json.loads(l)["title"] for l in f], ["Leaky handler"])

    def test_a_candidate_with_different_paths_does_not_suppress(self):
        dedup = [{"line": 2, "candidates": [{"number": 9, "state": "open", "same_paths": False}]}]
        tools = FakeTools(self.tmp.name, self.outcome, dedup=dedup, tracker=self.tracker_after_apply())
        records = self.filer(tools).file(pr=34, issue=21, outcome_path=self.path)
        self.assertEqual(records[1]["number"], 42)

    def test_already_closed_issue_is_not_closed_again(self):
        tracker = self.tracker_after_apply()
        tracker[0]["state"] = "closed"
        tools = FakeTools(self.tmp.name, self.outcome, tracker=tracker)
        self.filer(tools).file(pr=34, issue=21, outcome_path=self.path)
        self.assertFalse([c for c in tools.calls if c[:2] == ["hew", "close"]])

    def test_another_prs_findings_are_left_alone(self):
        tracker = self.tracker_after_apply()
        tracker.append({"number": 50, "state": "open", "title": "Other",
                        "body": "review-of: #22 (PR #35)\nfixed-in: abc1234\n"})
        tools = FakeTools(self.tmp.name, self.outcome, tracker=tracker)
        self.filer(tools).file(pr=34, issue=21, outcome_path=self.path)
        closed = [c[2] for c in tools.calls if c[:2] == ["hew", "close"]]
        self.assertEqual(closed, ["41"])

    def test_without_a_pr_findings_are_marked_by_the_review_target_alone(self):
        marker = "review-of: #21"
        tracker = [
            {"number": 41, "state": "open", "title": "Leaky handler",
             "body": f"review-key: code/p/a.go\n{marker}\nfixed-in: 9f2c1ab\n"},
            {"number": 42, "state": "open", "title": "Dead branch",
             "body": f"review-key: code/p/a.go\n{marker}\n"},
            # issue #210's finding shares the digits, not the review target
            {"number": 60, "state": "open", "title": "Leaky handler",
             "body": "review-of: #210\nfixed-in: abc1234\n"},
        ]
        tools = FakeTools(self.tmp.name, self.outcome, tracker=tracker)
        records = self.filer(tools).file(pr=None, issue=21, outcome_path=self.path)
        self.assertEqual([(r["title"], r["number"]) for r in records],
                         [("Leaky handler", 41), ("Dead branch", 42), ("Naming", None)])
        (convert,) = [c for c in tools.calls if c[:1] == ["uv"] and "findings_to_plan.py" in c[3]]
        self.assertNotIn("--reviewed-pr", convert)
        closes = [c for c in tools.calls if c[:2] == ["hew", "close"]]
        self.assertEqual([c[2] for c in closes], ["41"])
        self.assertEqual(closes[0][closes[0].index("--reason") + 1], "Fixed in 9f2c1ab before merge")

    def test_scratch_files_are_named_for_the_issue_when_there_is_no_pr(self):
        tools = FakeTools(self.tmp.name, self.outcome, tracker=[])
        self.filer(tools).file(pr=None, issue=21, outcome_path=self.path)
        names = os.listdir(self.tmp.name)
        self.assertIn("wr-21.plan.jsonl", names)
        self.assertNotIn("wr-None.plan.jsonl", names)

    def test_converter_failure_is_a_filing_error(self):
        tools = FakeTools(self.tmp.name, self.outcome, converter_rc=3)
        with self.assertRaises(FilingError):
            self.filer(tools).file(pr=34, issue=21, outcome_path=self.path)

    def test_a_review_that_did_not_run_is_not_filed(self):
        with open(self.path, "w") as f:
            json.dump({"status": "no_scope", "reason": "empty diff", "findings": []}, f)
        tools = FakeTools(self.tmp.name, self.outcome)
        with self.assertRaises(FilingError):
            self.filer(tools).file(pr=34, issue=21, outcome_path=self.path)
        self.assertFalse(tools.calls)

    def test_a_clean_review_files_nothing(self):
        self.outcome["findings"] = []
        with open(self.path, "w") as f:
            json.dump(self.outcome, f)
        tools = FakeTools(self.tmp.name, self.outcome)
        self.assertEqual(self.filer(tools).file(pr=34, issue=21, outcome_path=self.path), [])
        self.assertFalse([c for c in tools.calls if c[:2] == ["hew", "apply"]])


class Outcome(unittest.TestCase):
    def test_missing_or_garbled_file_reads_as_none(self):
        self.assertIsNone(read_outcome("/no/such/file"))
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as f:
            f.write("{not json")
        self.addCleanup(os.unlink, f.name)
        self.assertIsNone(read_outcome(f.name))

    def test_unfixed_blocker_is_p1_or_above_and_unfixed(self):
        self.assertIsNone(unfixed_blocker([finding("a", "P1", fixed=True), finding("b", "P2", fixed=False),
                                           finding("c", "P3")]))
        self.assertEqual(unfixed_blocker([finding("a", "P2"), finding("b", "P1", fixed=False)])["title"], "b")
        self.assertEqual(unfixed_blocker([finding("z", "P0")])["title"], "z")


class Comment(unittest.TestCase):
    def test_first_line_is_the_marker_pr_state_reads(self):
        self.assertEqual(round_comment([]).splitlines()[0], f"{REVIEW_MARKER}:")

    def test_clean_review_says_so(self):
        self.assertIn("no findings", round_comment([]))

    def test_each_finding_states_what_happened(self):
        records = [dict(finding("Leaky", "P1", fixed=True, fixedIn="9f2c1ab"), number=41),
                   dict(finding("Dead", "P2", fixed=False, unfixedReason="needs a decision"), number=42),
                   dict(finding("Naming", "P3"), number=None)]
        text = round_comment(records, gate="go test ./... — passed")
        self.assertIn("### P1 — Leaky", text)
        self.assertIn("Fixed in 9f2c1ab. Filed as #41 and closed.", text)
        self.assertIn("Not fixed: needs a decision. Filed as #42.", text)
        self.assertIn("Fix: Fix it. Not filed.", text)
        self.assertTrue(text.rstrip().endswith("gate: go test ./... — passed"))


if __name__ == "__main__":
    unittest.main()
