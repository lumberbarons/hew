"""Regression tests for instruction-file discovery and import semantics."""

import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("validate-claude-md.py")
BROKEN_NAVIGATION = (
    "| File | What | When to read |\n"
    "| --- | --- | --- |\n"
    "| `missing.py` | Code | Editing code |\n"
)


class Validator(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)

    def write(self, name, content):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content)
        return path

    def run_validator(self, *args, target=None):
        result = subprocess.run(
            [sys.executable, str(SCRIPT), str(target or self.root), "--json", *args],
            capture_output=True, text=True,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        return json.loads(result.stdout)

    def test_override_is_reviewed(self):
        path = self.write("AGENTS.override.md", BROKEN_NAVIGATION)
        results = self.run_validator()
        self.assertEqual([r["file"] for r in results], [str(path)])
        self.assertEqual(results[0]["issues"][0]["type"], "missing_reference")

    def test_configured_instruction_path_is_reviewed(self):
        path = self.write("docs/conventions.md", BROKEN_NAVIGATION)
        results = self.run_validator("--instructions", "docs/conventions.md")
        self.assertEqual([r["file"] for r in results], [str(path)])
        self.assertEqual(results[0]["issues"][0]["type"], "missing_reference")

    def test_fallback_filename_glob_covers_nested_directories(self):
        paths = [self.write(name, BROKEN_NAVIGATION) for name in
                 ("TEAM_GUIDE.md", "backend/TEAM_GUIDE.md")]
        results = self.run_validator("--instructions", "**/TEAM_GUIDE.md")
        self.assertEqual({r["file"] for r in results}, {str(p) for p in paths})

    def test_explicit_custom_file_is_reviewed(self):
        path = self.write("conventions.md", BROKEN_NAVIGATION)
        results = self.run_validator(target=path)
        self.assertEqual(results[0]["issues"][0]["type"], "missing_reference")

    def test_agents_prose_has_no_implicit_import_semantics(self):
        self.write("AGENTS.md", "Install @acme/widget. Also mention @docs/missing.md.\n")
        results = self.run_validator()
        self.assertEqual(results[0]["issues"], [])

    def test_claude_missing_import_is_still_reported(self):
        self.write("CLAUDE.md", "@docs/missing.md\n")
        results = self.run_validator()
        self.assertEqual(results[0]["issues"][0]["type"], "broken_import")

    def test_manual_import_convention_can_be_enabled(self):
        self.write("AGENTS.md", "@docs/missing.md\n")
        results = self.run_validator("--imports", "AGENTS.md")
        self.assertEqual(results[0]["issues"][0]["type"], "broken_import")

    def test_scoped_package_is_not_a_claude_import(self):
        self.write("CLAUDE.md", "Install @acme/widget before building.\n")
        self.assertEqual(self.run_validator()[0]["issues"], [])

    def test_explicit_relative_extensionless_import_is_validated(self):
        for ref in ("@docs/setup", "@./setup", "@./docs/setup", "@../setup"):
            with self.subTest(ref=ref):
                self.write("CLAUDE.md", ref + "\n")
                self.assertEqual(self.run_validator()[0]["issues"][0]["type"], "broken_import")

    def test_existing_import_is_not_reported(self):
        self.write("CLAUDE.md", "@docs/setup.md\n")
        self.write("docs/setup.md", "Setup instructions.\n")
        self.assertEqual(self.run_validator()[0]["issues"], [])

    def test_missing_configured_file_is_reported(self):
        results = self.run_validator("--instructions", "docs/missing.md")
        self.assertFalse(results[0]["exists"])

    def test_instruction_paths_are_deduplicated_and_fixtures_excluded(self):
        path = self.write("AGENTS.md", BROKEN_NAVIGATION)
        self.write("fixtures/AGENTS.md", BROKEN_NAVIGATION)
        results = self.run_validator("--instructions", "**/AGENTS.md")
        self.assertEqual([r["file"] for r in results], [str(path)])


if __name__ == "__main__":
    unittest.main()
