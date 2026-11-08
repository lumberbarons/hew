from __future__ import annotations

import json
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

from extract_spec import SpecFormatError, parse

MINIMAL = """\
# Widget Export

**Slug**: `009-widget-export`
**Created**: 2026-01-01
**Status**: Accepted

## Description

Export widgets as CSV.

## Prior Decisions

- None

## Out of Scope

- Import

## Assumptions

- CSV only for now

## User Stories

### US1 — Export widgets (P1)

As a user, I want to export widgets so that I can share them

Exports the current filtered view.

## Tasks

### US1

- [ ] T001 Add the export button — `internal/ui/export.go`
- [ ] T002 Stream rows as CSV — `internal/csv/writer.go`, `internal/csv/rows.go`

### Cross-cutting

- [ ] T003 Wire the CLI flag — `cmd/widget/main.go`

## Done When

- [ ] Exporting 0 rows writes a header-only file
- [ ] Exporting 10,000 rows completes in under a second
"""


class ParseTests(unittest.TestCase):
    def test_minimal_spec_round_trips(self) -> None:
        data = parse(MINIMAL, Path("specs/009-widget-export/spec.md"))
        self.assertEqual(data["title"], "Widget Export")
        self.assertEqual(data["slug"], "009-widget-export")
        self.assertEqual(data["status"], "Accepted")
        self.assertEqual(data["description"], "Export widgets as CSV.")
        self.assertEqual(data["prior_decisions"], ["None"])
        self.assertEqual(data["out_of_scope"], ["Import"])
        self.assertEqual(data["done_when"], [
            "Exporting 0 rows writes a header-only file",
            "Exporting 10,000 rows completes in under a second",
        ])

    def test_story_carries_its_own_tasks_and_paths(self) -> None:
        data = parse(MINIMAL, Path("spec.md"))
        self.assertEqual(len(data["stories"]), 1)
        story = data["stories"][0]
        self.assertEqual(story["id"], 1)
        self.assertEqual(story["priority"], "P1")
        self.assertEqual(story["story"], "As a user, I want to export widgets so that I can share them")
        self.assertEqual(story["narrative"], "Exports the current filtered view.")
        self.assertEqual([task["id"] for task in story["tasks"]], ["T001", "T002"])
        # A task can name more than one path — both must survive, not just the first.
        self.assertEqual(story["tasks"][1]["paths"], ["internal/csv/writer.go", "internal/csv/rows.go"])

    def test_cross_cutting_tasks_are_kept_out_of_every_story(self) -> None:
        data = parse(MINIMAL, Path("spec.md"))
        self.assertEqual([task["id"] for task in data["cross_cutting_tasks"]], ["T003"])
        for story in data["stories"]:
            self.assertNotIn("T003", [task["id"] for task in story["tasks"]])

    def test_story_with_no_tasks_is_rejected(self) -> None:
        broken = MINIMAL.replace(
            "### US1\n\n- [ ] T001 Add the export button — `internal/ui/export.go`\n"
            "- [ ] T002 Stream rows as CSV — `internal/csv/writer.go`, `internal/csv/rows.go`\n",
            "### US1\n\n",
        )
        with self.assertRaises(SpecFormatError):
            parse(broken, Path("spec.md"))

    def test_task_group_without_matching_story_is_rejected(self) -> None:
        broken = MINIMAL.replace("### US1\n\n- [ ] T001", "### US2\n\n- [ ] T001")
        with self.assertRaises(SpecFormatError):
            parse(broken, Path("spec.md"))

    def test_missing_status_line_is_rejected(self) -> None:
        broken = MINIMAL.replace("**Status**: Accepted\n", "")
        with self.assertRaises(SpecFormatError):
            parse(broken, Path("spec.md"))

    def test_bundled_example_parses(self) -> None:
        example = Path(__file__).resolve().parents[1] / "reference" / "example-spec.md"
        data = parse(example.read_text(encoding="utf-8"), example)
        self.assertEqual(len(data["stories"]), 2)
        self.assertEqual(sum(len(s["tasks"]) for s in data["stories"]), 6)
        self.assertEqual(len(data["cross_cutting_tasks"]), 2)
        self.assertEqual(len(data["done_when"]), 6)


class IsolatedInstallTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        root = Path(self.tmp.name)
        # A per-skill installer copies only this directory, independently of the
        # target checkout. Spaces exercise the quoted script paths in the skill.
        self.skill = root / "installed skills" / "spec-to-epic"
        shutil.copytree(
            Path(__file__).resolve().parents[1], self.skill,
            ignore=shutil.ignore_patterns("__pycache__"),
        )
        self.repo = root / "target repo"
        self.repo.mkdir()
        (self.repo / ".git").mkdir()
        self.spec = self.repo / "specs" / "009-widget-export" / "spec.md"
        self.spec.parent.mkdir(parents=True)
        self.spec.write_text(MINIMAL, encoding="utf-8")

    def run_script(self, script: str, *args: str) -> subprocess.CompletedProcess:
        return subprocess.run(
            [sys.executable, str(self.skill / "scripts" / script), *args],
            cwd=self.repo, text=True, capture_output=True, check=False,
        )

    def test_can_check_status_and_extract_without_other_skills(self) -> None:
        check = self.run_script("lightspec.py", "check", str(self.spec))
        self.assertEqual(check.returncode, 0, check.stdout + check.stderr)
        status = self.run_script("lightspec.py", "status", str(self.spec))
        self.assertEqual(status.returncode, 0, status.stdout + status.stderr)
        self.assertEqual(status.stdout.strip(), "Accepted")
        extract = self.run_script("extract_spec.py", str(self.spec))
        self.assertEqual(extract.returncode, 0, extract.stdout + extract.stderr)
        data = json.loads(extract.stdout)
        self.assertEqual(data["path"], str(self.spec))
        self.assertEqual(data["status"], "Accepted")
        self.assertEqual(data["stories"][0]["tasks"][1]["paths"],
                         ["internal/csv/writer.go", "internal/csv/rows.go"])

    def test_gate_rejects_malformed_accepted_spec(self) -> None:
        # The extractor deliberately does not validate task numbering. The
        # local checker must reject this before an agent builds a filing plan.
        self.spec.write_text(MINIMAL.replace("T002", "T004"), encoding="utf-8")
        check = self.run_script("lightspec.py", "check", str(self.spec))
        self.assertEqual(check.returncode, 1, check.stdout + check.stderr)
        self.assertIn("expected `T002`", check.stdout)

    def test_citations_resolve_in_target_checkout_and_worktree(self) -> None:
        self.spec.write_text(MINIMAL.replace(
            "- None", "- [ADR-0001 Storage](docs/adr/0001-storage.md) — CSV storage",
        ), encoding="utf-8")
        for worktree in (False, True):
            with self.subTest(worktree=worktree):
                if worktree:
                    (self.repo / ".git").rmdir()
                    (self.repo / ".git").write_text("gitdir: elsewhere\n", encoding="utf-8")
                missing = self.run_script("lightspec.py", "check", str(self.spec))
                self.assertEqual(missing.returncode, 1, missing.stdout + missing.stderr)
                self.assertIn("cited decision `docs/adr/0001-storage.md` does not exist", missing.stdout)
                adr = self.repo / "docs" / "adr" / "0001-storage.md"
                adr.parent.mkdir(parents=True, exist_ok=True)
                adr.write_text("# Storage\n", encoding="utf-8")
                present = self.run_script("lightspec.py", "check", str(self.spec))
                self.assertEqual(present.returncode, 0, present.stdout + present.stderr)
                adr.unlink()


if __name__ == "__main__":
    unittest.main()
