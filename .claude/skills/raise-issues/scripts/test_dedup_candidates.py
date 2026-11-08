"""Tests for dedup_candidates.

Run with `python3 -m unittest discover plugins/hew/skills/raise-issues/scripts`
— stdlib only; the CLI cases run the script as a subprocess.
"""

import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

from dedup_candidates import candidates_for

SCRIPT = Path(__file__).with_name("dedup_candidates.py")
KEY = "code/leaky-abstraction/internal/store"


def plan_line(key=KEY, paths=("internal/store/a.go:10",), review_of=None):
    where = ", ".join(f"`{p}`" for p in paths) + f"\n\nreview-key: {key}"
    if review_of:
        where += f"\nreview-of: #{review_of} (PR #99)"
    return {"title": "t", "where": where}


def issue(number, key=KEY, paths=("internal/store/a.go:3",), review_of=None,
          state="open"):
    where = ", ".join(f"`{p}`" for p in paths) + f"\n\nreview-key: {key}"
    if review_of:
        where += f"\nreview-of: #{review_of} (PR #50)"
    body = f"### Where\n\n{where}\n\n### Problem\n\nSomething.\n"
    return {"number": number, "title": f"issue {number}", "state": state,
            "body": body}


def numbers(result):
    return [c["number"] for c in result["candidates"]]


class Candidates(unittest.TestCase):
    def test_exact_key_is_a_candidate(self):
        r = candidates_for(plan_line(), [issue(1)], None)
        self.assertEqual(numbers(r), [1])

    def test_longer_key_containing_this_one_is_not(self):
        r = candidates_for(plan_line(), [issue(1, key=KEY + "/sub")], None)
        self.assertEqual(numbers(r), [])

    def test_shorter_key_that_prefixes_this_one_is_not(self):
        r = candidates_for(plan_line(), [issue(1, key="code/leaky-abstraction")],
                           None)
        self.assertEqual(numbers(r), [])

    def test_several_issues_sharing_a_key_are_all_candidates(self):
        r = candidates_for(plan_line(), [issue(1), issue(2), issue(3)], None)
        self.assertEqual(numbers(r), [1, 2, 3])

    def test_closed_issues_are_candidates(self):
        r = candidates_for(plan_line(), [issue(1, state="closed")], None)
        self.assertEqual(r["candidates"][0]["state"], "closed")

    def test_other_key_is_ignored(self):
        r = candidates_for(plan_line(), [issue(1, key="code/god-function/x")], None)
        self.assertEqual(r, {"key": KEY, "candidates": [], "other_generation": []})

    def test_same_paths_ignores_line_numbers(self):
        r = candidates_for(plan_line(paths=("internal/store/a.go:10",)),
                           [issue(1, paths=("internal/store/a.go:99",))], None)
        self.assertTrue(r["candidates"][0]["same_paths"])

    def test_different_paths_are_reported_not_filtered(self):
        r = candidates_for(plan_line(),
                           [issue(1, paths=("internal/store/b.go",))], None)
        self.assertEqual(numbers(r), [1])
        self.assertFalse(r["candidates"][0]["same_paths"])

    def test_portability_key_matches_the_legacy_skills_spelling(self):
        key = "portability/single-harness-tool/skills/x"
        r = candidates_for(plan_line(key=key),
                           [issue(1, key="skills/single-harness-tool/skills/x")],
                           None)
        self.assertEqual(numbers(r), [1])


class Generations(unittest.TestCase):
    """The spiral: a finding born of reviewing a fix shares the fix's key."""

    def test_the_issue_under_review_does_not_suppress_its_own_review(self):
        r = candidates_for(plan_line(review_of=7), [issue(7)], 7)
        self.assertEqual(numbers(r), [])
        self.assertEqual(r["other_generation"], [7])

    def test_rerunning_the_same_review_finds_its_earlier_filing(self):
        r = candidates_for(plan_line(review_of=7), [issue(7), issue(8, review_of=7)], 7)
        self.assertEqual(numbers(r), [8])
        self.assertEqual(r["other_generation"], [7])

    def test_a_different_review_target_is_another_generation(self):
        r = candidates_for(plan_line(review_of=9), [issue(8, review_of=7)], 9)
        self.assertEqual(numbers(r), [])
        self.assertEqual(r["other_generation"], [8])

    def test_a_retry_pr_for_the_same_issue_still_matches(self):
        # review-of compares the issue, not the PR: PR #50 vs #99 is one target.
        r = candidates_for(plan_line(review_of=7), [issue(8, review_of=7)], 7)
        self.assertEqual(numbers(r), [8])

    def test_a_product_review_ignores_the_review_target(self):
        r = candidates_for(plan_line(), [issue(8, review_of=7)], None)
        self.assertEqual(numbers(r), [8])


def cli(*args, plan, issues):
    with tempfile.TemporaryDirectory() as d:
        p, i = Path(d, "plan.jsonl"), Path(d, "issues.ndjson")
        p.write_text("\n".join(json.dumps(x) for x in plan))
        i.write_text("\n".join(json.dumps(x) for x in issues))
        return subprocess.run([sys.executable, str(SCRIPT), str(p), str(i), *args],
                              capture_output=True, text=True)


class Cli(unittest.TestCase):
    def test_one_result_per_plan_line(self):
        p = cli("--reviewed-issue", "7",
                plan=[plan_line(review_of=7), plan_line(key="code/other/x")],
                issues=[issue(7), issue(8, review_of=7)])
        self.assertEqual(p.returncode, 0, p.stderr)
        rows = [json.loads(r) for r in p.stdout.splitlines()]
        self.assertEqual([r["line"] for r in rows], [1, 2])
        self.assertEqual(numbers(rows[0]), [8])
        self.assertEqual(numbers(rows[1]), [])

    def test_plan_line_without_a_key_is_a_runtime_error(self):
        p = cli(plan=[{"title": "t", "where": "`a.go`"}], issues=[])
        self.assertEqual(p.returncode, 1)
        self.assertNotIn("Traceback", p.stderr)

    def test_bad_flag_value_is_usage_error(self):
        p = cli("--reviewed-issue", "abc", plan=[plan_line()], issues=[])
        self.assertEqual(p.returncode, 2)
        self.assertNotIn("Traceback", p.stderr)

    def test_missing_input_file_is_a_clean_error(self):
        p = subprocess.run([sys.executable, str(SCRIPT), "/nope", "/nope"],
                           capture_output=True, text=True)
        self.assertEqual(p.returncode, 1)
        self.assertNotIn("Traceback", p.stderr)


if __name__ == "__main__":
    unittest.main()
