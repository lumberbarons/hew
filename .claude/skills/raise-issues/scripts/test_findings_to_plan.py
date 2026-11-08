"""Tests for findings_to_plan.

Run with `python3 -m unittest discover plugins/hew/skills/raise-issues/scripts`
— stdlib only; the CLI cases run the script as a subprocess.
"""

import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

from findings_to_plan import convert

SCRIPT = Path(__file__).with_name("findings_to_plan.py")


def finding(priority="P2", **kw):
    f = {"skill": "code", "priority": priority, "pattern": "god-function",
         "title": "Handler does everything", "files": ["internal/http/a.go"],
         "fix": "Split it.", "explanation": "Too much.", "done_when": "Split."}
    f.update(kw)
    return f


def cli(*args, findings=None, status="reviewed"):
    with tempfile.TemporaryDirectory() as d:
        path = Path(d, "findings.json")
        path.write_text(json.dumps(
            {"skill": "code", "status": status, "findings": findings or []}))
        return subprocess.run([sys.executable, str(SCRIPT), str(path), *args],
                              capture_output=True, text=True)


class Convert(unittest.TestCase):
    def test_legacy_portability_identity_emits_the_canonical_key(self):
        plans = [convert(finding(skill=skill, pattern="single-harness-tool"),
                         None, None, None, "bug")[0]
                 for skill in ("skills", "portability")]
        self.assertEqual(plans[0], plans[1])
        self.assertIn("review-key: portability/single-harness-tool/", plans[0]["where"])

    def test_pump_wiring(self):
        line, _ = convert(finding(), 12, 21, 34, "bug")
        self.assertEqual((line["parent"], line["blocked-by"], line["discovered-from"]),
                         (12, [21], 21))
        self.assertIn("review-key: code/god-function/internal/http/a.go", line["where"])
        self.assertIn("review-of: #21 (PR #34)", line["where"])

    def test_fixed_finding_carries_its_commit(self):
        line, _ = convert(finding(fixedIn="9f2c1ab"), None, 21, 34, "bug")
        self.assertTrue(line["where"].endswith("fixed-in: 9f2c1ab"))

    def test_unfixed_finding_has_no_fixed_in_line(self):
        line, _ = convert(finding(), None, 21, 34, "bug")
        self.assertNotIn("fixed-in:", line["where"])

    def test_p0_files_as_p1(self):
        line, _ = convert(finding("P0"), None, None, None, "bug")
        self.assertEqual(line["priority"], "P1")

    def test_at_or_above_skips_less_severe(self):
        line, reason = convert(finding("P3"), None, None, None, "bug", at_or_above=2)
        self.assertIsNone(line)
        self.assertIn("below", reason)
        line, _ = convert(finding("P0"), None, None, None, "bug", at_or_above=2)
        self.assertEqual(line["priority"], "P1")

    def test_scope_is_common_directory(self):
        f = finding(files=["internal/http/a.go", "internal/http/b/c.go"])
        line, _ = convert(f, None, None, None, "bug")
        self.assertIn("review-key: code/god-function/internal/http\n", line["where"] + "\n")

    def test_missing_pattern_skipped(self):
        line, reason = convert(finding(pattern=None), None, None, None, "bug")
        self.assertIsNone(line)
        self.assertEqual(reason, "missing pattern")


class Cli(unittest.TestCase):
    def test_filters_by_severity(self):
        p = cli("--at-or-above", "P2", findings=[finding("P1"), finding("P3")])
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual([json.loads(l)["priority"] for l in p.stdout.splitlines()], ["P1"])

    def test_a_findings_own_skill_wins_over_the_files(self):
        # the work-epic reviewer merges a tests-lens review into the code-lens file
        p = cli(findings=[finding(title="a"),
                          finding(title="b", skill="tests", pattern="tautological-assertion",
                                  files=["a_test.go"])])
        self.assertEqual(p.returncode, 0, p.stderr)
        keys = [json.loads(l)["where"] for l in p.stdout.splitlines()]
        self.assertIn("review-key: code/god-function/", keys[0])
        self.assertIn("review-key: tests/tautological-assertion/a_test.go", keys[1])

    def test_unreviewed_status_exits_3(self):
        self.assertEqual(cli(status="error").returncode, 3)

    def test_missing_flag_value_is_usage_error(self):
        p = cli("--parent")
        self.assertEqual(p.returncode, 2)
        self.assertNotIn("Traceback", p.stderr)

    def test_non_numeric_flag_value_is_usage_error(self):
        self.assertEqual(cli("--reviewed-issue", "abc").returncode, 2)

    def test_unreadable_file_is_runtime_error(self):
        p = subprocess.run([sys.executable, str(SCRIPT), "/nonexistent.json"],
                           capture_output=True, text=True)
        self.assertEqual(p.returncode, 1)
        self.assertNotIn("Traceback", p.stderr)


if __name__ == "__main__":
    unittest.main()
