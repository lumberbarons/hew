"""Tests for research.py. Run: python3 -m unittest discover -s <this directory>.

The skeleton and the checker define one format in two places. The first group
pins them together; the rest break one rule at a time.
"""

from __future__ import annotations

import contextlib
import io
import tempfile
import unittest
from pathlib import Path

import research

HERE = Path(__file__).resolve().parent
EXAMPLE = HERE.parent / "reference" / "example-research.md"


def run(*argv: str) -> tuple[int, str, str]:
    out, err = io.StringIO(), io.StringIO()
    with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
        try:
            code = research.main(list(argv))
        except SystemExit as exit_:
            code = int(exit_.code or 0)
    return code, out.getvalue(), err.getvalue()


def problems(text: str, name: str = "0004-example.md", root: Path | None = None) -> list[str]:
    return research.check_text(text, Path(name), root)


def example() -> str:
    return EXAMPLE.read_text(encoding="utf-8")


class SkeletonMatchesChecker(unittest.TestCase):
    def test_fresh_skeleton_fails_only_for_fill_markers(self) -> None:
        text = research.skeleton("Is X safe?", 1, "2026-01-02", 3, 3)
        found = problems(text, "0001-is-x-safe.md")
        self.assertTrue(found)
        for line in found:
            self.assertIn("<FILL", line, f"unexpected complaint: {line}")

    def test_every_fill_marker_is_reported(self) -> None:
        text = research.skeleton("Is X safe?", 1, "2026-01-02", 2, 2)
        markers = [i for i, line in enumerate(text.splitlines(), 1) if "<FILL:" in line]
        reported = [line for line in problems(text, "0001-is-x-safe.md") if "unfilled" in line]
        self.assertEqual(len(markers), len(reported))

    def test_example_passes(self) -> None:
        self.assertEqual(problems(example()), [])

    def test_skeleton_honours_counts(self) -> None:
        text = research.skeleton("Is X safe?", 7, "2026-01-02", 4, 2)
        self.assertEqual(text.count("### F"), 4)
        self.assertEqual(text.count("\n- S"), 2)
        self.assertIn("id: RES-0007", text)

    def test_title_with_colon_and_quotes_survives(self) -> None:
        title = 'Redis: is "fast" enough?'
        text = research.skeleton(title, 1, "2026-01-02", 1, 1)
        found = [p for p in problems(text, "0001-redis.md") if "<FILL" not in p]
        self.assertEqual(found, [])


class FrontmatterRules(unittest.TestCase):
    def swap(self, old: str, new: str) -> list[str]:
        text = example()
        self.assertIn(old, text)
        return problems(text.replace(old, new, 1))

    def test_bad_status(self) -> None:
        self.assertTrue(any("status" in p for p in self.swap("status: complete", "status: done")))

    def test_bad_date(self) -> None:
        self.assertTrue(any("date" in p for p in self.swap("date: 2026-03-12", "date: 2026-13-45")))

    def test_id_must_match_filename(self) -> None:
        self.assertTrue(any("does not match" in p for p in self.swap("id: RES-0004", "id: RES-0005")))

    def test_superseded_needs_successor(self) -> None:
        found = self.swap("status: complete", "status: superseded")
        self.assertTrue(any("superseded-by" in p for p in found))

    def test_superseded_with_successor_passes(self) -> None:
        text = example().replace("status: complete", "status: superseded").replace(
            "superseded-by: []", "superseded-by: [RES-0009]"
        )
        self.assertEqual(problems(text), [])

    def test_related_accepts_research_and_adr_ids(self) -> None:
        self.assertEqual(self.swap("related: [RES-0002, ADR-0003]", "related: [RES-0002, ADR-0007]"), [])

    def test_related_rejects_other_ids(self) -> None:
        found = self.swap("related: [RES-0002, ADR-0003]", "related: [RES-0002, SPEC-0003]")
        self.assertTrue(any("RES-0001 or ADR-0001" in p for p in found))

    def test_supersedes_stays_research_only(self) -> None:
        found = self.swap("supersedes: []", "supersedes: [ADR-0002]")
        self.assertTrue(any("RES-0001" in p for p in found))

    def test_related_must_be_a_list(self) -> None:
        self.assertTrue(any("must be a list" in p for p in self.swap("related: [RES-0002, ADR-0003]", "related: RES-0002")))

    def test_missing_key(self) -> None:
        self.assertTrue(any("missing `tags`" in p for p in self.swap("tags: [export, postgres, reliability]\n", "")))

    def test_h1_must_match_frontmatter(self) -> None:
        found = self.swap("# RES-0004: Does", "# RES-0004: Did")
        self.assertTrue(any("H1 must be" in p for p in found))

    def test_no_frontmatter(self) -> None:
        body = example().split("---\n", 2)[2]
        self.assertTrue(any("frontmatter" in p for p in problems(body)))


class StructureRules(unittest.TestCase):
    def swap(self, old: str, new: str) -> list[str]:
        text = example()
        self.assertIn(old, text)
        return problems(text.replace(old, new, 1))

    def test_missing_section(self) -> None:
        found = self.swap("## Revisit When", "## Later")
        self.assertTrue(any("missing section `## Revisit When`" in p for p in found))
        self.assertTrue(any("unexpected section `## Later`" in p for p in found))

    def test_sections_out_of_order(self) -> None:
        text = example()
        head, tail = text.split("## Open Questions", 1)
        open_q, rest = tail.split("## Conclusions", 1)
        swapped = head + "## Conclusions" + rest.split("## Sources", 1)[0] + "## Open Questions" + open_q + "## Sources" + rest.split("## Sources", 1)[1]
        self.assertTrue(any("out of order" in p for p in problems(swapped)))

    def test_question_must_be_a_question(self) -> None:
        found = self.swap("fail visibly?", "fail visibly.")
        self.assertTrue(any("phrased as a question" in p for p in found))

    def test_empty_method(self) -> None:
        text = example()
        start = text.index("## Method")
        end = text.index("## Findings")
        self.assertTrue(any("`## Method` is empty" in p for p in problems(text[:start] + "## Method\n\n" + text[end:])))

    def test_scope_needs_both_blocks(self) -> None:
        found = self.swap("### Out of scope", "### Elsewhere")
        self.assertTrue(any("missing `### Out of scope`" in p for p in found))

    def test_open_questions_need_a_bullet(self) -> None:
        text = example()
        start = text.index("## Open Questions")
        end = text.index("## Conclusions")
        found = problems(text[:start] + "## Open Questions\n\nNone.\n\n" + text[end:])
        self.assertTrue(any("`## Open Questions` needs at least one bullet" in p for p in found))

    def test_none_bullet_is_enough(self) -> None:
        text = example()
        start = text.index("## Open Questions")
        end = text.index("## Conclusions")
        self.assertEqual(problems(text[:start] + "## Open Questions\n\n- None\n\n" + text[end:]), [])

    def test_heading_inside_fence_is_not_a_section(self) -> None:
        text = example().replace("\n### F2", "\n```\n## Not a section\n```\n\n### F2", 1)
        self.assertEqual(problems(text), [])


class FindingRules(unittest.TestCase):
    def swap(self, old: str, new: str) -> list[str]:
        text = example()
        self.assertIn(old, text)
        return problems(text.replace(old, new, 1))

    def test_confidence_is_a_closed_scale(self) -> None:
        found = self.swap("(confidence: Medium)", "(confidence: Probably)")
        self.assertTrue(any("finding heading must be" in p for p in found))

    def test_confidence_is_required(self) -> None:
        found = self.swap(" (confidence: Medium)", "")
        self.assertTrue(any("finding heading must be" in p for p in found))

    def test_findings_are_numbered_in_order(self) -> None:
        found = self.swap("### F3 —", "### F4 —")
        self.assertTrue(any("numbered F4, expected F3" in p for p in found))

    def test_finding_must_cite_a_source(self) -> None:
        text = example()
        start = text.index("### F3")
        end = text.index("## Open Questions")
        bare = text[:start] + text[start:end].replace("[S1]", "").replace("[S2]", "") + text[end:]
        self.assertTrue(any("F3 cites no source" in p for p in problems(bare)))

    def test_dangling_citation(self) -> None:
        found = self.swap("[S3]", "[S9]")
        self.assertTrue(any("cites [S9], which is not in Sources" in p for p in found))

    def test_listed_source_must_be_cited(self) -> None:
        found = self.swap("[S3]", "[S2]")
        self.assertTrue(any("S3 is listed but no finding cites it" in p for p in found))

    def test_text_outside_a_finding(self) -> None:
        found = self.swap("## Findings\n", "## Findings\n\nSome stray prose.\n")
        self.assertTrue(any("must sit under" in p for p in found))


class SourceRules(unittest.TestCase):
    def swap(self, old: str, new: str, root: Path | None = None) -> list[str]:
        text = example()
        self.assertIn(old, text)
        return problems(text.replace(old, new, 1), root=root)

    def test_type_is_a_closed_vocabulary(self) -> None:
        found = self.swap("— primary —", "— blog —")
        self.assertTrue(any("source line must be" in p for p in found))

    def test_accessed_date_is_required(self) -> None:
        found = self.swap(" — accessed 2026-03-10\n- S2", "\n- S2")
        self.assertTrue(any("source line must be" in p for p in found))

    def test_accessed_date_must_be_real(self) -> None:
        found = self.swap("accessed 2026-03-11", "accessed 2026-02-31")
        self.assertTrue(any("not a real date" in p for p in found))

    def test_double_hyphen_is_accepted(self) -> None:
        text = example().replace(" — ", " -- ")
        self.assertEqual(problems(text), [])

    def test_repo_path_must_exist_inside_a_repository(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / ".git").mkdir()
            found = problems(example(), root=root)
            self.assertTrue(any("services/export/reader.go" in p and "does not exist" in p for p in found))
            (root / "services/export").mkdir(parents=True)
            (root / "services/export/reader.go").write_text("package export\n")
            self.assertEqual(problems(example(), root=root), [])

    def test_anchored_path_resolves(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "services/export").mkdir(parents=True)
            (root / "services/export/reader.go").write_text("package export\n")
            text = example().replace("reader.go](services/export/reader.go)", "reader.go](services/export/reader.go#L40)")
            self.assertEqual(problems(text, root=root), [])

    def test_urls_are_never_resolved(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "services/export").mkdir(parents=True)
            (root / "services/export/reader.go").write_text("package export\n")
            self.assertEqual(problems(example(), root=root), [])


class NewCommand(unittest.TestCase):
    def test_numbers_from_the_highest_existing_document(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            target = Path(tmp) / "research"
            target.mkdir()
            (target / "0003-old.md").write_text("x")
            (target / "notes.md").write_text("x")
            code, out, _ = run("new", "Is X safe?", "--dir", str(target))
            self.assertEqual(code, 0)
            self.assertEqual(Path(out.strip()).name, "0004-is-x-safe.md")
            self.assertIn("id: RES-0004", Path(out.strip()).read_text())

    def test_first_document_is_one(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            code, out, _ = run("new", "Is X safe?", "--dir", str(Path(tmp) / "fresh"))
            self.assertEqual(code, 0)
            self.assertEqual(Path(out.strip()).name, "0001-is-x-safe.md")

    def test_slug_override_and_validation(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            code, out, _ = run("new", "Whatever", "--slug", "custom-name", "--dir", tmp)
            self.assertEqual(code, 0)
            self.assertEqual(Path(out.strip()).name, "0001-custom-name.md")
            code, _, err = run("new", "Whatever", "--slug", "Bad Slug", "--dir", tmp)
            self.assertEqual(code, 2)
            self.assertIn("slug", err)

    def test_empty_title_and_zero_counts_are_refused(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            self.assertEqual(run("new", "  ", "--dir", tmp)[0], 2)
            self.assertEqual(run("new", "X?", "--findings", "0", "--dir", tmp)[0], 2)
            self.assertEqual(list(Path(tmp).iterdir()), [])

    def test_prefers_existing_research_directory(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / ".git").mkdir()
            (root / "research").mkdir()
            cwd = Path.cwd()
            import os

            os.chdir(root)
            try:
                code, out, _ = run("new", "Is X safe?")
            finally:
                os.chdir(cwd)
            self.assertEqual(code, 0)
            self.assertEqual(Path(out.strip()).parent.name, "research")
            self.assertNotEqual(Path(out.strip()).parent.parent.name, "docs")

    def test_defaults_to_docs_research(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / ".git").mkdir()
            cwd = Path.cwd()
            import os

            os.chdir(root)
            try:
                code, out, _ = run("new", "Is X safe?")
            finally:
                os.chdir(cwd)
            self.assertEqual(code, 0)
            self.assertEqual(Path(out.strip()).resolve().parent, (root / "docs/research").resolve())


class CheckCommand(unittest.TestCase):
    def test_ok_for_a_good_document_and_nonzero_for_a_bad_one(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            good = Path(tmp) / "0004-example.md"
            good.write_text(example())
            code, out, _ = run("check", str(good))
            self.assertEqual((code, out.strip()), (0, "ok"))

            bad = Path(tmp) / "0005-bad.md"
            bad.write_text(example().replace("status: complete", "status: done"))
            code, out, _ = run("check", str(bad))
            self.assertEqual(code, 1)
            self.assertNotIn("ok", out.split())

    def test_unreadable_path_fails(self) -> None:
        code, out, _ = run("check", "/nonexistent/0001-x.md")
        self.assertEqual(code, 1)
        self.assertIn("cannot read", out)

    def test_fresh_skeleton_from_new_fails_check(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            _, out, _ = run("new", "Is X safe?", "--dir", tmp)
            code, checked, _ = run("check", out.strip())
            self.assertEqual(code, 1)
            self.assertIn("unfilled", checked)


if __name__ == "__main__":
    unittest.main()
