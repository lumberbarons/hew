"""Scope-resolution tests for repo_state.parse_scope and scope_issues.

Run with `python3 -m unittest discover plugins/work-epic/skills/work-epic/scripts`
— stdlib only, no network: `hew` is patched out.
"""

import json
import unittest
from unittest.mock import patch

from repo_state import CommandError, parse_scope, scope_issues, touches_code


class ParseScope(unittest.TestCase):
    def test_numbers_hashes_and_lists(self):
        self.assertEqual(parse_scope(["12", "#13", "14,15"]), [12, 13, 14, 15])

    def test_duplicates_collapse_in_first_position(self):
        self.assertEqual(parse_scope(["12", "13", "12"]), [12, 13])

    def test_anything_else_is_not_a_scope(self):
        self.assertIsNone(parse_scope(["12", "--resume"]))
        self.assertIsNone(parse_scope(["fix-the-bugs"]))
        self.assertIsNone(parse_scope([]))


def row(n, **kw):
    return dict({"number": n, "title": f"issue {n}", "state": "open", "epic": False}, **kw)


def fake_hew(shows, children):
    """`hew show <n>` answers from `shows`; `hew list --epic <n>` from `children`."""

    def run(argv):
        if argv[:2] == ["hew", "show"]:
            n = int(argv[2])
            if n not in shows:
                raise CommandError(f"issue #{n} not found")
            return json.dumps(shows[n])
        if argv[:2] == ["hew", "list"] and "--epic" in argv:
            epic = int(argv[argv.index("--epic") + 1])
            return "".join(json.dumps(c) + "\n" for c in children[epic])
        raise AssertionError(f"unexpected command {argv}")

    return run


class TouchesCode(unittest.TestCase):
    def test_prose_only_diffs_are_not_code(self):
        self.assertFalse(touches_code(["README.md", "docs/Guide.RST", "notes.txt"]))

    def test_one_non_prose_file_makes_it_code(self):
        self.assertTrue(touches_code(["README.md", "svc/a.go"]))
        self.assertTrue(touches_code(["Makefile"]))

    def test_a_diff_that_cannot_be_listed_counts_as_code(self):
        self.assertTrue(touches_code([]))
        self.assertTrue(touches_code(["", "  "]))


class ScopeIssues(unittest.TestCase):
    def resolve(self, numbers, shows, children=None):
        with patch("repo_state.run", side_effect=fake_hew(shows, children or {})):
            return [r["number"] for r in scope_issues(numbers)]

    def test_issues_stand_for_themselves(self):
        shows = {21: row(21), 22: row(22)}
        self.assertEqual(self.resolve([22, 21], shows), [22, 21])

    def test_an_epic_expands_to_its_children_including_closed(self):
        shows = {12: row(12, epic=True)}
        children = {12: [row(21), row(22, state="closed")]}
        self.assertEqual(self.resolve([12], shows, children), [21, 22])

    def test_an_issue_named_directly_and_through_its_epic_appears_once(self):
        shows = {12: row(12, epic=True), 22: row(22)}
        children = {12: [row(21), row(22)]}
        self.assertEqual(self.resolve([22, 12], shows, children), [22, 21])

    def test_an_unreadable_issue_fails_the_run(self):
        with self.assertRaises(CommandError):
            self.resolve([99], {})

    def test_repo_is_passed_to_hew(self):
        seen = []

        def run(argv):
            seen.append(argv)
            return json.dumps(row(21))

        with patch("repo_state.run", side_effect=run):
            scope_issues([21], "o/r")
        self.assertEqual(seen[0][-2:], ["--repo", "o/r"])


if __name__ == "__main__":
    unittest.main()
