"""Tests for lightspec.

Run with `python3 -m unittest discover -s "<this skill's directory>/scripts"` -
stdlib only, no test dependency to install.

The two tests that matter most are the round trip (a fresh skeleton fails only
for its FILL markers) and the reference example (the file the skill tells the
agent to imitate still passes). Together they catch the skeleton and the checker
drifting apart, which is the only way this tool can quietly stop working.
"""

from __future__ import annotations

import contextlib
import io
import tempfile
import unittest
from pathlib import Path

import lightspec

VALID = """# Thing

**Slug**: `001-thing`
**Created**: 2026-01-01
**Status**: Draft

## Description

A thing that does something for someone.

## Prior Decisions

- None

## Out of Scope

- The other thing

## Assumptions

- The caller is authenticated

## User Stories

### US1 — Do the thing (P1)

As a user, I want to do the thing so that the outcome happens

The flow runs once per request, and a second call is a no-op.

## Tasks

### US1

- [ ] T001 Build the thing — `api/internal/thing.go`

## Done When

- [ ] The thing can be done twice with one result
"""


def problems_for(text: str, name: str = "spec.md", parent: str = "001-thing"):
    with tempfile.TemporaryDirectory() as tmp:
        path = Path(tmp) / parent / name
        path.parent.mkdir(parents=True)
        path.write_text(text, encoding="utf-8")
        return [message for _, message in lightspec.check_file(path).items]


class SkeletonRoundTrip(unittest.TestCase):
    def test_fresh_skeleton_fails_only_for_fill_markers(self):
        text = lightspec.skeleton("Thing", "001-thing", "2026-01-01", stories=2)
        messages = problems_for(text)
        self.assertTrue(messages, "a skeleton should not pass check")
        self.assertEqual({"unfilled `<FILL: ...>` marker"}, set(messages))

    def test_one_story_skeleton_is_structurally_sound(self):
        text = lightspec.skeleton("Thing", "001-thing", "2026-01-01", stories=1)
        self.assertEqual({"unfilled `<FILL: ...>` marker"}, set(problems_for(text)))


class ReferenceExample(unittest.TestCase):
    def test_example_spec_passes(self):
        example = Path(__file__).resolve().parents[1] / "reference" / "example-spec.md"
        problems = lightspec.check_file(example)
        self.assertEqual([], problems.items)


class ValidBaseline(unittest.TestCase):
    def test_minimal_spec_passes(self):
        self.assertEqual([], problems_for(VALID))

    def test_cross_cutting_group_is_optional_and_allowed(self):
        text = VALID.replace(
            "## Done When",
            "### Cross-cutting\n\n"
            "- [ ] T002 Wire the nav — `webui/src/App.tsx`\n\n"
            "## Done When",
        )
        self.assertEqual([], problems_for(text))


class Structure(unittest.TestCase):
    def test_missing_section_is_reported(self):
        text = VALID.replace("## Prior Decisions\n\n- None\n\n", "")
        self.assertTrue(any("sections must be exactly" in m for m in problems_for(text)))

    def test_reordered_sections_are_reported(self):
        text = VALID.replace("## Description", "## Assumptions", 1).replace(
            "## Assumptions\n\n- The caller is authenticated", "## Description\n\nProse."
        )
        self.assertTrue(any("sections must be exactly" in m for m in problems_for(text)))

    def test_slug_must_match_its_directory(self):
        messages = problems_for(VALID, parent="002-other")
        self.assertTrue(any("does not match its directory" in m for m in messages))

    def test_slug_is_not_checked_outside_a_numbered_directory(self):
        self.assertEqual([], problems_for(VALID, parent="reference"))


class Decisions(unittest.TestCase):
    def test_citation_without_a_link_is_reported(self):
        text = VALID.replace("- None", "- ADR 7 says prices are cents")
        self.assertTrue(any("markdown link" in m for m in problems_for(text)))

    def test_linked_citation_passes(self):
        text = VALID.replace(
            "- None", "- [ADR-0007 Cents](docs/adr/0007-cents.md) — prices stay integers"
        )
        self.assertEqual([], problems_for(text))

    def test_empty_section_is_reported(self):
        text = VALID.replace("- None\n", "")
        self.assertTrue(any("at least one `- ` bullet" in m for m in problems_for(text)))


class Stories(unittest.TestCase):
    def test_missing_priority_is_reported(self):
        text = VALID.replace("### US1 — Do the thing (P1)", "### US1 — Do the thing")
        self.assertTrue(any("story heading must read" in m for m in problems_for(text)))

    def test_out_of_order_numbering_is_reported(self):
        text = VALID.replace("### US1 — Do the thing (P1)", "### US2 — Do the thing (P1)")
        self.assertTrue(any("expected `US1` here" in m for m in problems_for(text)))

    def test_missing_role_sentence_is_reported(self):
        text = VALID.replace(
            "As a user, I want to do the thing so that the outcome happens",
            "The user does the thing.",
        )
        self.assertTrue(any("story must open" in m for m in problems_for(text)))

    def test_wrapped_role_sentence_passes(self):
        text = VALID.replace(
            "As a user, I want to do the thing so that the outcome happens",
            "As a user, I want to do the thing\nso that the outcome happens",
        )
        self.assertEqual([], problems_for(text))

    def test_story_without_detail_is_reported(self):
        text = VALID.replace(
            "\nThe flow runs once per request, and a second call is a no-op.\n", ""
        )
        self.assertTrue(any("needs a paragraph of detail" in m for m in problems_for(text)))


class Tasks(unittest.TestCase):
    def test_task_without_a_path_is_reported(self):
        text = VALID.replace(
            "- [ ] T001 Build the thing — `api/internal/thing.go`",
            "- [ ] T001 Build the thing",
        )
        self.assertTrue(any("task must read" in m for m in problems_for(text)))

    def test_double_hyphen_separator_is_accepted(self):
        text = VALID.replace(
            "- [ ] T001 Build the thing — `api/internal/thing.go`",
            "- [ ] T001 Build the thing -- `api/internal/thing.go`",
        )
        self.assertEqual([], problems_for(text))

    def test_multiple_paths_are_accepted(self):
        text = VALID.replace(
            "`api/internal/thing.go`", "`api/internal/thing.go`, `api/graph/schema.graphqls`"
        )
        self.assertEqual([], problems_for(text))

    def test_non_contiguous_ids_are_reported(self):
        text = VALID.replace("T001", "T004")
        self.assertTrue(any("expected `T001`" in m for m in problems_for(text)))

    def test_story_without_tasks_is_reported(self):
        text = VALID.replace(
            "### US1\n\n- [ ] T001 Build the thing — `api/internal/thing.go`\n",
            "### Cross-cutting\n\n- [ ] T001 Build the thing — `api/internal/thing.go`\n",
        )
        self.assertTrue(any("US1 has no tasks" in m for m in problems_for(text)))

    def test_group_without_a_story_is_reported(self):
        text = VALID.replace(
            "### US1\n\n- [ ] T001", "### US1\n\n- [ ] T001 Build — `a.go`\n\n### US9\n\n- [ ] T002"
        )
        self.assertTrue(
            any("has no matching story" in m for m in problems_for(text))
        )

    def test_unknown_group_heading_is_reported(self):
        text = VALID.replace(
            "### US1\n\n- [ ] T001 Build the thing — `api/internal/thing.go`",
            "### US1\n\n- [ ] T001 Build the thing — `api/internal/thing.go`\n\n"
            "### Polish\n\n- [ ] T002 Tidy up — `api/internal/thing.go`",
        )
        self.assertTrue(any("task group must be" in m for m in problems_for(text)))


class DoneWhen(unittest.TestCase):
    def test_plain_bullets_are_reported(self):
        text = VALID.replace(
            "- [ ] The thing can be done twice with one result",
            "- The thing can be done twice with one result",
        )
        self.assertTrue(any("checklist items" in m for m in problems_for(text)))

    def test_empty_section_is_reported(self):
        text = VALID.replace("- [ ] The thing can be done twice with one result\n", "")
        self.assertTrue(any("at least one `- [ ] ` item" in m for m in problems_for(text)))


class Naming(unittest.TestCase):
    def test_slugify(self):
        self.assertEqual("price-drop-watchlist", lightspec.slugify("Price Drop Watchlist"))
        self.assertEqual("saq-lcbo-sync", lightspec.slugify("SAQ / LCBO sync!"))
        self.assertEqual("creme-brulee", lightspec.slugify("Crème brûlée"))

    def test_next_number_follows_the_highest_existing(self):
        with tempfile.TemporaryDirectory() as tmp:
            specs = Path(tmp)
            self.assertEqual(1, lightspec.next_number(specs))
            (specs / "003-alpha").mkdir()
            (specs / "007-beta").mkdir()
            (specs / "notes").mkdir()
            self.assertEqual(8, lightspec.next_number(specs))


class Robustness(unittest.TestCase):
    """The formatting a real spec picks up on the way through an editor."""

    def test_trailing_whitespace_on_a_heading_is_tolerated(self):
        # The complaint it used to raise named a heading that read identically
        # to the required one, which is the least actionable message possible.
        self.assertEqual([], problems_for(VALID.replace("## Description", "## Description  ")))

    def test_trailing_whitespace_on_a_meta_line_is_tolerated(self):
        self.assertEqual([], problems_for(VALID.replace("**Status**: Draft", "**Status**: Draft ")))

    def test_headings_inside_a_fence_are_not_sections(self):
        text = VALID.replace(
            "A thing that does something for someone.",
            "A thing that does something for someone.\n\n"
            "```graphql\n## not a section\n### US9 - not a story (P1)\ntype T { id: ID! }\n```",
        )
        self.assertEqual([], problems_for(text))

    def test_headings_inside_a_fence_in_a_story_body_are_not_stories(self):
        text = VALID.replace(
            "The flow runs once per request, and a second call is a no-op.",
            "```sql\n### US9 - not a story (P1)\nSELECT 1;\n```\n\n"
            "The flow runs once per request, and a second call is a no-op.",
        )
        self.assertEqual([], problems_for(text))

    def test_a_tilde_fence_is_not_closed_by_a_backtick_fence(self):
        text = VALID.replace(
            "A thing that does something for someone.",
            "A thing that does something for someone.\n\n"
            "~~~\n```\n## still inside the fence\n~~~",
        )
        self.assertEqual([], problems_for(text))

    def test_an_unterminated_fence_reports_missing_sections(self):
        text = VALID.replace("## Prior Decisions", "```\nnever closed\n\n## Prior Decisions")
        self.assertTrue(any("sections must be exactly" in m for m in problems_for(text)))


class Status(unittest.TestCase):
    """The one header field with a life after the skill that wrote it."""

    def _with(self, status: str) -> list[str]:
        return problems_for(VALID.replace("**Status**: Draft", f"**Status**: {status}"))

    def test_every_value_in_the_vocabulary_is_accepted(self):
        for value in ("Draft", "Accepted", "Implemented"):
            with self.subTest(value=value):
                self.assertEqual([], self._with(value))

    def test_a_value_outside_the_vocabulary_is_reported(self):
        self.assertTrue(any("**Status**" in m for m in self._with("In Progress")))

    def test_the_vocabulary_is_case_sensitive(self):
        self.assertTrue(any("**Status**" in m for m in self._with("draft")))

    def test_prose_after_a_status_is_reported(self):
        # "Draft (pending review)" is how a closed vocabulary turns back into
        # free text one hedge at a time.
        self.assertTrue(any("takes nothing after it" in m for m in self._with("Draft (pending review)")))

    def test_superseded_without_a_link_is_reported(self):
        self.assertTrue(any("must link what replaced it" in m for m in self._with("Superseded")))
        self.assertTrue(
            any("must link what replaced it" in m for m in self._with("Superseded by 011"))
        )

    def test_superseded_with_a_link_passes(self):
        self.assertEqual(
            [], self._with("Superseded by [011 Watchlist v2](../011-watchlist-v2/spec.md)")
        )

    def test_the_skeleton_starts_as_a_draft(self):
        # `new` writes a proposal; nothing in the skill ratifies one.
        text = lightspec.skeleton("Thing", "001-thing", "2026-01-01", stories=1)
        self.assertIn("**Status**: Draft\n", text)


class CitedDecisionsResolve(unittest.TestCase):
    """A decision can only be cited once recorded, so a dead link is always wrong."""

    def _repo(self, tmp: str, citation: str, adrs: tuple[str, ...] = ()) -> list[str]:
        root = Path(tmp)
        (root / ".git").mkdir()
        for adr in adrs:
            (root / adr).parent.mkdir(parents=True, exist_ok=True)
            (root / adr).write_text("# a decision", encoding="utf-8")
        spec = root / "specs" / "001-thing" / "spec.md"
        spec.parent.mkdir(parents=True)
        spec.write_text(VALID.replace("- None", citation), encoding="utf-8")
        return [message for _, message in lightspec.check_file(spec).items]

    def test_a_citation_pointing_at_nothing_is_reported(self):
        with tempfile.TemporaryDirectory() as tmp:
            messages = self._repo(tmp, "- [ADR-0007 Cents](docs/adr/0007-cents.md) — cents")
            self.assertTrue(any("does not exist" in m for m in messages), messages)

    def test_a_citation_pointing_at_a_real_file_passes(self):
        with tempfile.TemporaryDirectory() as tmp:
            messages = self._repo(
                tmp,
                "- [ADR-0007 Cents](docs/adr/0007-cents.md) — cents",
                adrs=("docs/adr/0007-cents.md",),
            )
            self.assertEqual([], messages)

    def test_an_external_url_is_nobody_s_to_verify(self):
        with tempfile.TemporaryDirectory() as tmp:
            messages = self._repo(
                tmp, "- [RFC 7231](https://www.rfc-editor.org/rfc/rfc7231) — semantics"
            )
            self.assertEqual([], messages)

    def test_links_are_not_resolved_outside_a_repository(self):
        # An ad-hoc draft has nothing to resolve against; claiming the target
        # is missing would be a guess.
        self.assertEqual(
            [],
            problems_for(VALID.replace("- None", "- [ADR-0007](docs/adr/0007.md) — x")),
        )


class Research(unittest.TestCase):
    """Research is optional, so older specs pass, but checked when present."""

    WITH = VALID.replace(
        "## Out of Scope", "## Research\n\n- None\n\n## Out of Scope", 1
    )

    def test_a_spec_without_the_section_still_passes(self):
        self.assertEqual([], problems_for(VALID))

    def test_none_passes(self):
        self.assertEqual([], problems_for(self.WITH))

    def test_a_linked_citation_passes(self):
        text = self.WITH.replace(
            "## Research\n\n- None",
            "## Research\n\n- [RES-0004 Failover](docs/research/0004-failover.md) — rows survive",
        )
        self.assertEqual([], problems_for(text))

    def test_a_citation_without_a_link_is_reported(self):
        text = self.WITH.replace("## Research\n\n- None", "## Research\n\n- RES-0004 says so")
        self.assertTrue(any("research document as a markdown link" in m for m in problems_for(text)))

    def test_an_empty_section_is_reported(self):
        text = self.WITH.replace("## Research\n\n- None\n\n", "## Research\n\n")
        self.assertTrue(any("Research needs at least one" in m for m in problems_for(text)))

    def test_the_section_must_follow_prior_decisions(self):
        text = VALID.replace("## Assumptions", "## Research\n\n- None\n\n## Assumptions", 1)
        self.assertTrue(any("sections must be exactly" in m for m in problems_for(text)))

    def test_a_dead_link_is_reported_inside_a_repository(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / ".git").mkdir()
            spec = root / "specs" / "001-thing" / "spec.md"
            spec.parent.mkdir(parents=True)
            spec.write_text(
                self.WITH.replace(
                    "## Research\n\n- None",
                    "## Research\n\n- [RES-0004 X](docs/research/0004-x.md) — x",
                ),
                encoding="utf-8",
            )
            messages = [m for _, m in lightspec.check_file(spec).items]
            self.assertTrue(any("research document" in m and "does not exist" in m for m in messages), messages)

    def test_a_fresh_skeleton_carries_the_section(self):
        text = lightspec.skeleton("Thing", "001-thing", "2026-01-01", stories=1)
        self.assertLess(text.index("## Prior Decisions"), text.index("## Research"))
        self.assertLess(text.index("## Research"), text.index("## Out of Scope"))


class StatusCommand(unittest.TestCase):
    def _spec(self, tmp: str, text: str = VALID) -> Path:
        spec = Path(tmp) / "001-thing" / "spec.md"
        spec.parent.mkdir(parents=True)
        spec.write_text(text, encoding="utf-8")
        return spec

    def _run(self, *argv: str) -> tuple[int, str]:
        quiet = io.StringIO()
        with contextlib.redirect_stdout(quiet), contextlib.redirect_stderr(quiet):
            code = lightspec.main(["status", *argv])
        return code, quiet.getvalue()

    def test_reads_the_current_status(self):
        with tempfile.TemporaryDirectory() as tmp:
            code, out = self._run(str(self._spec(tmp)))
            self.assertEqual(0, code)
            self.assertEqual("Draft", out.strip())

    def test_moves_a_passing_spec_to_accepted(self):
        with tempfile.TemporaryDirectory() as tmp:
            spec = self._spec(tmp)
            self.assertEqual(0, self._run(str(spec), "Accepted")[0])
            self.assertIn("**Status**: Accepted\n", spec.read_text())
            self.assertEqual([], [m for _, m in lightspec.check_file(spec).items])

    def test_refuses_a_status_outside_the_vocabulary(self):
        with tempfile.TemporaryDirectory() as tmp:
            spec = self._spec(tmp)
            self.assertEqual(2, self._run(str(spec), "Shipped")[0])
            self.assertIn("**Status**: Draft\n", spec.read_text())

    def test_refuses_to_approve_a_spec_that_fails_check(self):
        # The gate is the point: an approver who forgets to run check cannot
        # accidentally ratify a spec that does not hold together.
        with tempfile.TemporaryDirectory() as tmp:
            spec = self._spec(tmp, VALID.replace("- [ ] The thing can be done twice with one result\n", ""))
            code, out = self._run(str(spec), "Accepted")
            self.assertEqual(1, code)
            self.assertIn("refusing to set a status", out)
            self.assertIn("**Status**: Draft\n", spec.read_text())

    def test_superseded_requires_a_link(self):
        with tempfile.TemporaryDirectory() as tmp:
            spec = self._spec(tmp)
            self.assertEqual(2, self._run(str(spec), "Superseded")[0])
            self.assertEqual(0, self._run(str(spec), "Superseded", "--by", "[011](../011-x/spec.md)")[0])
            self.assertIn("Superseded by [011](../011-x/spec.md)", spec.read_text())
            self.assertEqual([], [m for _, m in lightspec.check_file(spec).items])


class SpecNumber(unittest.TestCase):
    """A number outside 001-999 names a directory the rest of the tool cannot see."""

    def _new(self, tmp: str, number: int) -> int:
        # main() is the surface under test here, so its output is captured
        # rather than left to scribble over the test runner's.
        quiet = io.StringIO()
        with contextlib.redirect_stdout(quiet), contextlib.redirect_stderr(quiet):
            return lightspec.main(
                ["new", "Thing", "--number", str(number), "--specs-dir", tmp, "--date", "2026-01-01"]
            )

    def test_number_below_the_range_is_refused(self):
        with tempfile.TemporaryDirectory() as tmp:
            self.assertEqual(2, self._new(tmp, -1))
            self.assertEqual([], list(Path(tmp).iterdir()))

    def test_number_above_the_range_is_refused(self):
        with tempfile.TemporaryDirectory() as tmp:
            self.assertEqual(2, self._new(tmp, 1000))
            self.assertEqual([], list(Path(tmp).iterdir()))

    def test_number_inside_the_range_is_written(self):
        with tempfile.TemporaryDirectory() as tmp:
            self.assertEqual(0, self._new(tmp, 8))
            self.assertTrue((Path(tmp) / "008-thing" / "spec.md").is_file())


if __name__ == "__main__":
    unittest.main()
