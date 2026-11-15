#!/usr/bin/env python3
"""lightspec - emit and validate lightweight feature specs.

    lightspec.py new "Feature name" [--slug SLUG] [--stories N]
    lightspec.py check [PATH ...] [--latest]

`new` writes a fixed skeleton to specs/NNN-slug/spec.md, with `<FILL: ...>`
markers where prose belongs. `check` re-reads a filled spec and reports every
way it drifted from that format, one problem per line, exiting 1 if any.

The skeleton is the only definition of the format; check is the only thing that
enforces it. Keep the two in step - the test suite asserts that a fresh skeleton
fails check for its FILL markers and nothing else.
"""

from __future__ import annotations

import argparse
import datetime
import re
import sys
import unicodedata
from pathlib import Path

REQUIRED_SECTIONS = (
    "Description",
    "Prior Decisions",
    "Out of Scope",
    "Assumptions",
    "User Stories",
    "Tasks",
    "Done When",
)
# Research is optional so specs written before it existed still pass: when
# present it sits straight after Prior Decisions. `new` always writes it, so
# every fresh spec says either what it rests on or `- None`.
RESEARCH_SECTION = "Research"
CROSS_CUTTING = "Cross-cutting"
FILL = "<FILL:"
MAX_SLUG_LEN = 40

# An em dash separates a story title from its priority, and a task from its
# paths. Two hyphens are accepted too - agents drop em dashes, and the
# distinction carries no meaning.
DASH = r"(?:—|--)"

H1_RE = re.compile(r"^# (\S.*)$")
H2_RE = re.compile(r"^## (\S.*)$")
H3_RE = re.compile(r"^### (\S.*)$")
SLUG_LINE_RE = re.compile(r"^\*\*Slug\*\*: `(\d{3}-[a-z0-9][a-z0-9-]*)`$")
CREATED_LINE_RE = re.compile(r"^\*\*Created\*\*: (\d{4}-\d{2}-\d{2})$")
# A spec outlives the writing of it, and Status is the only field that says
# whether the reader is looking at a proposal, a plan to build from, or a record
# of something already shipped. Free text would drift into prose within a month,
# so the vocabulary is closed and the checker holds it.
STATUS_VALUES = ("Draft", "Accepted", "Implemented", "Superseded")
STATUS_LINE_RE = re.compile(
    r"^\*\*Status\*\*: (" + "|".join(STATUS_VALUES) + r")(?:\s+(\S.*))?$"
)
STORY_HEAD_RE = re.compile(r"^US(\d+) " + DASH + r" (\S.*?) \(P([0-4])\)$")
TASK_GROUP_RE = re.compile(r"^US(\d+)$")
TASK_RE = re.compile(
    r"^- \[[ xX]\] T(\d{3}) (\S.*?) " + DASH + r" (`[^`]+`(?:, `[^`]+`)*)$"
)
CHECKBOX_RE = re.compile(r"^- \[[ xX]\] (\S.*)$")
BULLET_RE = re.compile(r"^- (\S.*)$")
STORY_SENTENCE_RE = re.compile(r"^As an? .+, I want .+ so that .+$", re.IGNORECASE)
LINK_RE = re.compile(r"\[[^\]]+\]\([^)]+\)")
LINK_TARGET_RE = re.compile(r"\[[^\]]+\]\(([^)]+)\)")
NO_DECISIONS_RE = re.compile(r"^- None\b", re.IGNORECASE)
DIR_RE = re.compile(r"^(\d{3})-([a-z0-9][a-z0-9-]*)$")
FENCE_RE = re.compile(r"^\s*(```|~~~)")


# ---------------------------------------------------------------- generation


def slugify(name: str) -> str:
    ascii_name = (
        unicodedata.normalize("NFKD", name).encode("ascii", "ignore").decode("ascii")
    )
    slug = re.sub(r"[^a-z0-9]+", "-", ascii_name.lower()).strip("-")
    if len(slug) > MAX_SLUG_LEN:
        slug = slug[:MAX_SLUG_LEN].rsplit("-", 1)[0] or slug[:MAX_SLUG_LEN]
    return slug.strip("-")


def find_repo_root(start: Path) -> Path:
    return repo_root_or_none(start) or start


def repo_root_or_none(start: Path) -> Path | None:
    """The enclosing repository, or None when there is not one.

    Link targets can only be resolved against a repository. Outside one - an
    ad-hoc draft, a spec under test in a temporary directory - there is nothing
    to resolve against, and claiming the target is missing would be a guess.
    """
    for candidate in [start, *start.parents]:
        if (candidate / ".git").exists():
            return candidate
    return None


def resolve_specs_dir(override: str | None) -> Path:
    if override:
        return Path(override)
    return find_repo_root(Path.cwd().resolve()) / "specs"


def next_number(specs_dir: Path) -> int:
    highest = 0
    if specs_dir.is_dir():
        for child in specs_dir.iterdir():
            match = DIR_RE.match(child.name)
            if child.is_dir() and match:
                highest = max(highest, int(match.group(1)))
    return highest + 1


def skeleton(title: str, slug: str, created: str, stories: int) -> str:
    lines = [
        f"# {title}",
        "",
        f"**Slug**: `{slug}`",
        f"**Created**: {created}",
        "**Status**: Draft",
        "",
        "## Description",
        "",
        "<FILL: 2-5 sentences. What this is, who it serves, why it is worth doing"
        " now. No technology choices, no implementation detail.>",
        "",
        "## Prior Decisions",
        "",
        "- <FILL: an accepted decision this feature must respect, as"
        " `[ADR-0007 Prices in cents](docs/adr/0007-prices-in-cents.md) — the"
        " constraint it puts on this work`. Write `- None` if none bear on it.>",
        "",
        "## Research",
        "",
        "- <FILL: a complete research document this feature relies on, as"
        " `[RES-0004 Export failover](docs/research/0004-export-failover.md) — the"
        " finding it leans on`. Write `- None` if there is none.>",
        "",
        "## Out of Scope",
        "",
        "- <FILL: something a reader might reasonably expect here that this feature"
        " deliberately does not do>",
        "",
        "## Assumptions",
        "",
        "- <FILL: a decision made where the request was silent, stated so a reader"
        " can challenge it>",
        "",
        "## User Stories",
        "",
    ]
    for index in range(1, stories + 1):
        priority = "P1" if index == 1 else "P2"
        lines += [
            f"### US{index} — <FILL: short title> ({priority})",
            "",
            "As a <FILL: role>, I want <FILL: capability> so that <FILL: outcome>",
            "",
            "<FILL: 1-2 lines. The flow, the constraint that shapes it, and the edge"
            " case that matters.>",
            "",
        ]

    lines += ["## Tasks", ""]
    task_id = 1
    for index in range(1, stories + 1):
        lines += [
            f"### US{index}",
            "",
            f"- [ ] T{task_id:03d} <FILL: action> — `<FILL: path/to/file>`",
            "",
        ]
        task_id += 1
    lines += [
        f"### {CROSS_CUTTING}",
        "",
        f"- [ ] T{task_id:03d} <FILL: action serving no single story> —"
        " `<FILL: path/to/file>`",
        "",
        "## Done When",
        "",
        "- [ ] <FILL: an observable claim someone else could check without reading"
        " the diff>",
        "- [ ] <FILL: another one>",
        "",
    ]
    return "\n".join(lines)


def cmd_new(args: argparse.Namespace) -> int:
    if args.stories < 1:
        print("lightspec: --stories must be at least 1", file=sys.stderr)
        return 2

    specs_dir = resolve_specs_dir(args.specs_dir)
    slug = slugify(args.slug or args.title)
    if not slug:
        print(f"lightspec: cannot derive a slug from {args.title!r}", file=sys.stderr)
        return 2

    number = args.number if args.number is not None else next_number(specs_dir)
    # A number outside this range yields a directory the rest of the tool cannot
    # see: `next_number`, `--latest` and the slug check all key off `NNN-slug`.
    if not 1 <= number <= 999:
        print(f"lightspec: spec number {number} is outside 001-999", file=sys.stderr)
        return 2
    dirname = f"{number:03d}-{slug}"
    target = specs_dir / dirname / "spec.md"
    if target.exists():
        print(f"lightspec: {target} already exists", file=sys.stderr)
        return 2

    created = args.date or datetime.date.today().isoformat()
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(
        skeleton(args.title, dirname, created, args.stories), encoding="utf-8"
    )
    print(target)
    return 0


# ---------------------------------------------------------------- validation


class Problems:
    def __init__(self, path: Path) -> None:
        self.path = path
        self.items: list[tuple[int, str]] = []

    def add(self, lineno: int, message: str) -> None:
        self.items.append((lineno, message))

    def report(self) -> None:
        for lineno, message in sorted(self.items):
            print(f"{self.path}:{lineno}: {message}")


Block = tuple[str, int, list[tuple[int, str]]]


def fenced_linenos(lines: list[str]) -> frozenset[int]:
    """Line numbers inside a fenced code block, delimiters included.

    A heading is only a heading outside a fence. Specs quote snippets, and a
    `## ` line inside one would otherwise register as a section and derail
    every check that follows it. An unterminated fence swallows the rest of
    the file, which surfaces as missing sections - the right complaint.
    """
    found: set[int] = set()
    opener: str | None = None
    for lineno, line in enumerate(lines, start=1):
        match = FENCE_RE.match(line)
        if opener is None:
            if match:
                opener = match.group(1)
                found.add(lineno)
            continue
        found.add(lineno)
        if match and line.strip().startswith(opener):
            opener = None
    return frozenset(found)


def sections(lines: list[str], fenced: frozenset[int]) -> list[Block]:
    """Split the document into (heading, heading lineno, body) triples by H2."""
    found: list[Block] = []
    for lineno, line in enumerate(lines, start=1):
        match = None if lineno in fenced else H2_RE.match(line)
        if match:
            found.append((match.group(1), lineno, []))
        elif found:
            found[-1][2].append((lineno, line))
    return found


def blocks(
    body: list[tuple[int, str]], fenced: frozenset[int]
) -> tuple[list[tuple[int, str]], list[Block]]:
    """Split a section body into its preamble and its H3 blocks."""
    preamble: list[tuple[int, str]] = []
    found: list[Block] = []
    for lineno, line in body:
        match = None if lineno in fenced else H3_RE.match(line)
        if match:
            found.append((match.group(1), lineno, []))
        elif found:
            found[-1][2].append((lineno, line))
        else:
            preamble.append((lineno, line))
    return preamble, found


def nonblank(body: list[tuple[int, str]]) -> list[tuple[int, str]]:
    return [(lineno, line) for lineno, line in body if line.strip()]


def paragraphs(body: list[tuple[int, str]]) -> list[tuple[int, str]]:
    """Join runs of non-blank lines into (first lineno, text) pairs.

    Prose is checked per paragraph rather than per line so that hard-wrapping a
    sentence does not read as a malformed one.
    """
    found: list[tuple[int, str]] = []
    current: list[str] = []
    start = 0
    for lineno, line in body:
        if line.strip():
            if not current:
                start = lineno
            current.append(line.strip())
        elif current:
            found.append((start, " ".join(current)))
            current = []
    if current:
        found.append((start, " ".join(current)))
    return found


def check_header(lines: list[str], path: Path, problems: Problems) -> None:
    header = [
        (lineno, line) for lineno, line in enumerate(lines, start=1) if line.strip()
    ]
    if not header:
        problems.add(1, "file is empty")
        return

    title_lineno, title_line = header[0]
    if not H1_RE.match(title_line):
        problems.add(
            title_lineno, "first line must be an H1 title, e.g. `# Wine label OCR`"
        )

    meta = (
        ("Slug", SLUG_LINE_RE, "`**Slug**: `NNN-slug``"),
        ("Created", CREATED_LINE_RE, "`**Created**: YYYY-MM-DD`"),
        ("Status", STATUS_LINE_RE, "`**Status**: " + "|".join(STATUS_VALUES) + "`"),
    )
    for offset, (name, pattern, expected) in enumerate(meta, start=1):
        if offset >= len(header):
            problems.add(title_lineno, f"missing `**{name}**:` line under the title")
            continue
        meta_lineno, meta_line = header[offset]
        match = pattern.match(meta_line)
        if not match:
            problems.add(
                meta_lineno,
                f"expected {expected} here, in the order Slug, Created, Status",
            )
            continue
        # A spec that has been replaced is a dead end unless it says by what,
        # the same reason a cited decision has to carry a link.
        if name == "Status":
            if match.group(1) == "Superseded":
                if not (match.group(2) and LINK_RE.search(match.group(2))):
                    problems.add(
                        meta_lineno,
                        "a superseded spec must link what replaced it, e.g."
                        " `**Status**: Superseded by"
                        " [011 Watchlist v2](../011-watchlist-v2/spec.md)`",
                    )
            elif match.group(2):
                # Only Superseded carries a tail. Anywhere else it is the
                # hedging - "Draft (pending review)" - that a closed
                # vocabulary exists to keep out.
                problems.add(
                    meta_lineno,
                    f"`{match.group(1)}` takes nothing after it; only"
                    " `Superseded by [...](...)` carries a tail",
                )
        # Only a spec that lives in a numbered directory has a slug to agree
        # with; the reference example and ad-hoc drafts are checked on shape.
        if (
            name == "Slug"
            and DIR_RE.match(path.parent.name)
            and match.group(1) != path.parent.name
        ):
            problems.add(
                meta_lineno,
                f"slug `{match.group(1)}` does not match its directory"
                f" `{path.parent.name}`",
            )


def unresolved_links(line: str, spec_path: Path) -> list[str]:
    """Local link targets on this line that point at nothing.

    A decision can only be cited once it has been recorded, so unlike a task
    path - which names a file the work has yet to create - a citation that
    resolves to nothing is always wrong. Targets are tried against the repo
    root and against the spec's own directory, since both spellings are in
    common use; external URLs and bare anchors are nobody's to verify.
    """
    root = repo_root_or_none(spec_path.resolve().parent)
    if root is None:
        return []
    missing = []
    for target in LINK_TARGET_RE.findall(line):
        target = target.split("#", 1)[0].strip()
        if not target or "://" in target or target.startswith("mailto:"):
            continue
        if (root / target).exists() or (spec_path.parent / target).exists():
            continue
        missing.append(target)
    return missing


def check_bulleted(
    name: str, body: list[tuple[int, str]], heading_lineno: int, problems: Problems
) -> None:
    content = nonblank(body)
    if not content:
        problems.add(heading_lineno, f"{name} needs at least one `- ` bullet")
    for lineno, line in content:
        # An indented line continues the bullet above it.
        if line.startswith((" ", "\t")):
            continue
        if not BULLET_RE.match(line):
            problems.add(lineno, f"{name} takes only `- ` bullets")


def check_citations(
    name: str,
    noun: str,
    example: str,
    body: list[tuple[int, str]],
    heading_lineno: int,
    path: Path,
    problems: Problems,
) -> None:
    """Every cited decision or research document must be reachable, or the
    section must say None.

    A citation without a link is a claim a reader cannot follow, and the whole
    point of these sections is that the spec can be held against what was
    decided and what was established.
    """
    check_bulleted(name, body, heading_lineno, problems)
    for lineno, line in nonblank(body):
        if line.startswith((" ", "\t")) or not BULLET_RE.match(line):
            continue
        if NO_DECISIONS_RE.match(line) or LINK_RE.search(line):
            # Same carve-out as the slug: only a spec filed under
            # specs/NNN-slug is being held against a real repository. The
            # reference example cites a decision record this repo has never
            # had, and it is a template, not a claim about this codebase.
            if DIR_RE.match(path.parent.name):
                for target in unresolved_links(line, path):
                    problems.add(
                        lineno,
                        f"cited {noun} `{target}` does not exist - a citation"
                        " a reader cannot follow is not a citation",
                    )
            continue
        problems.add(
            lineno,
            f"cite the {noun} as a markdown link, e.g. `{example}`, or write `- None`",
        )


def check_stories(
    body: list[tuple[int, str]],
    heading_lineno: int,
    fenced: frozenset[int],
    problems: Problems,
) -> dict[int, str]:
    preamble, story_blocks = blocks(body, fenced)
    for lineno, _ in nonblank(preamble):
        problems.add(lineno, "User Stories takes only `### US<n> ...` blocks")
    if not story_blocks:
        problems.add(heading_lineno, "needs at least one user story")
        return {}

    titles: dict[int, str] = {}
    for position, (heading, lineno, story_body) in enumerate(story_blocks, start=1):
        match = STORY_HEAD_RE.match(heading)
        if not match:
            problems.add(
                lineno, "story heading must read `### US<n> — <title> (P<0-4>)`"
            )
            continue
        number = int(match.group(1))
        if number != position:
            problems.add(lineno, f"expected `US{position}` here, stories run in order")
        titles[number] = match.group(2)

        paras = paragraphs(story_body)
        if not paras:
            problems.add(lineno, f"US{number} has no body")
            continue
        if not STORY_SENTENCE_RE.match(paras[0][1]):
            problems.add(
                paras[0][0],
                "story must open `As a <role>, I want <capability> so that <outcome>`",
            )
        if len(paras) < 2:
            problems.add(
                lineno, f"US{number} needs a paragraph of detail under the story"
            )
    return titles


def check_tasks(
    body: list[tuple[int, str]],
    heading_lineno: int,
    stories: dict[int, str],
    fenced: frozenset[int],
    problems: Problems,
) -> None:
    preamble, groups = blocks(body, fenced)
    for lineno, _ in nonblank(preamble):
        problems.add(
            lineno, f"Tasks takes only `### US<n>` and `### {CROSS_CUTTING}` groups"
        )

    seen: set[str] = set()
    covered: set[int] = set()
    expected_id = 1
    for heading, lineno, group_body in groups:
        if heading in seen:
            problems.add(lineno, f"duplicate task group `{heading}`")
        seen.add(heading)

        match = TASK_GROUP_RE.match(heading)
        if match:
            number = int(match.group(1))
            if number not in stories:
                problems.add(lineno, f"task group `US{number}` has no matching story")
            covered.add(number)
        elif heading != CROSS_CUTTING:
            problems.add(
                lineno, f"task group must be `### US<n>` or `### {CROSS_CUTTING}`"
            )

        content = nonblank(group_body)
        if not content:
            problems.add(lineno, f"task group `{heading}` has no tasks")
        for task_lineno, line in content:
            task = TASK_RE.match(line)
            if not task:
                problems.add(
                    task_lineno,
                    "task must read `- [ ] T001 <action> — `path/to/file``",
                )
                continue
            if int(task.group(1)) != expected_id:
                problems.add(
                    task_lineno,
                    f"expected `T{expected_id:03d}`, task ids run in order from T001",
                )
            expected_id = int(task.group(1)) + 1

    for number in sorted(set(stories) - covered):
        problems.add(heading_lineno, f"US{number} has no tasks")


def check_done_when(
    body: list[tuple[int, str]], heading_lineno: int, problems: Problems
) -> None:
    content = nonblank(body)
    if not content:
        problems.add(heading_lineno, "needs at least one `- [ ] ` item")
    for lineno, line in content:
        if not CHECKBOX_RE.match(line):
            problems.add(lineno, "Done When takes only `- [ ] ` checklist items")


def check_file(path: Path) -> Problems:
    problems = Problems(path)
    # Trailing whitespace is invisible to a reader but breaks every `$`-anchored
    # pattern below, and the resulting complaint names a heading that looks
    # identical to the one required. Strip it instead of reporting it.
    lines = [line.rstrip() for line in path.read_text(encoding="utf-8").splitlines()]
    fenced = fenced_linenos(lines)

    check_header(lines, path, problems)

    for lineno, line in enumerate(lines, start=1):
        if FILL in line:
            problems.add(lineno, "unfilled `<FILL: ...>` marker")

    found = sections(lines, fenced)
    names = [name for name, _, _ in found]
    expected = list(REQUIRED_SECTIONS)
    if RESEARCH_SECTION in names:
        expected.insert(expected.index("Prior Decisions") + 1, RESEARCH_SECTION)
    if names != expected:
        problems.add(
            found[0][1] if found else 1,
            "sections must be exactly, in order: "
            + ", ".join(expected)
            + (f" (found: {', '.join(names)})" if names else " (found none)"),
        )

    by_name = {name: (lineno, body) for name, lineno, body in found}
    stories: dict[int, str] = {}
    if "Description" in by_name:
        lineno, body = by_name["Description"]
        if not nonblank(body):
            problems.add(lineno, "Description is empty")
    if "Prior Decisions" in by_name:
        lineno, body = by_name["Prior Decisions"]
        check_citations(
            "Prior Decisions",
            "decision",
            "- [ADR-0007 Prices in cents](docs/adr/0007-prices-in-cents.md) — ...",
            body,
            lineno,
            path,
            problems,
        )
    if RESEARCH_SECTION in by_name:
        lineno, body = by_name[RESEARCH_SECTION]
        check_citations(
            RESEARCH_SECTION,
            "research document",
            "- [RES-0004 Export failover](docs/research/0004-export-failover.md) — ...",
            body,
            lineno,
            path,
            problems,
        )
    for name in ("Out of Scope", "Assumptions"):
        if name in by_name:
            lineno, body = by_name[name]
            check_bulleted(name, body, lineno, problems)
    if "User Stories" in by_name:
        lineno, body = by_name["User Stories"]
        stories = check_stories(body, lineno, fenced, problems)
    if "Tasks" in by_name:
        lineno, body = by_name["Tasks"]
        check_tasks(body, lineno, stories, fenced, problems)
    if "Done When" in by_name:
        lineno, body = by_name["Done When"]
        check_done_when(body, lineno, problems)

    return problems


def latest_spec(specs_dir: Path) -> Path | None:
    candidates = []
    if specs_dir.is_dir():
        for child in sorted(specs_dir.iterdir()):
            match = DIR_RE.match(child.name)
            if child.is_dir() and match and (child / "spec.md").is_file():
                candidates.append((int(match.group(1)), child / "spec.md"))
    return max(candidates)[1] if candidates else None


def cmd_check(args: argparse.Namespace) -> int:
    paths = [Path(p) for p in args.paths]
    if args.latest:
        specs_dir = resolve_specs_dir(args.specs_dir)
        newest = latest_spec(specs_dir)
        if newest is None:
            print(f"lightspec: no spec found under {specs_dir}", file=sys.stderr)
            return 2
        paths.append(newest)
    if not paths:
        print("lightspec: pass a path or --latest", file=sys.stderr)
        return 2

    failed = False
    for path in paths:
        if not path.is_file():
            print(f"lightspec: {path} not found", file=sys.stderr)
            failed = True
            continue
        problems = check_file(path)
        if problems.items:
            problems.report()
            failed = True
        else:
            print(f"{path}: ok")
    return 1 if failed else 0


STATUS_PREFIX = "**Status**: "


def cmd_status(args: argparse.Namespace) -> int:
    """Read or move a spec's Status line.

    The status is part of the format, so the script moves it rather than an
    agent editing the header by hand. Moving it is also gated on the spec
    passing check: approving a spec that does not hold together should not be
    possible just because the approver forgot to look.
    """
    path = Path(args.path)
    if not path.is_file():
        print(f"lightspec: {path} not found", file=sys.stderr)
        return 2

    lines = path.read_text(encoding="utf-8").splitlines()
    current = next(
        (line[len(STATUS_PREFIX):] for line in lines if line.startswith(STATUS_PREFIX)),
        None,
    )
    if current is None:
        print(f"lightspec: {path} has no `{STATUS_PREFIX.strip()}` line", file=sys.stderr)
        return 2
    if args.value is None:
        print(current)
        return 0

    if args.value not in STATUS_VALUES:
        print(
            f"lightspec: `{args.value}` is not a status; use one of "
            + ", ".join(STATUS_VALUES),
            file=sys.stderr,
        )
        return 2
    if args.value == "Superseded" and not (args.by and LINK_RE.search(args.by)):
        print(
            "lightspec: Superseded needs --by with a markdown link to the spec"
            " that replaced this one",
            file=sys.stderr,
        )
        return 2

    problems = check_file(path)
    if problems.items:
        problems.report()
        print(
            f"lightspec: refusing to set a status on a spec that does not pass"
            f" check; fix the above first",
            file=sys.stderr,
        )
        return 1

    updated = args.value + (f" by {args.by}" if args.value == "Superseded" else "")
    for index, line in enumerate(lines):
        if line.startswith(STATUS_PREFIX):
            lines[index] = STATUS_PREFIX + updated
            break
    path.write_text("\n".join(lines) + "\n", encoding="utf-8")
    print(f"{path}: {current} -> {updated}")
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="lightspec", description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)

    new = sub.add_parser("new", help="write a spec skeleton")
    new.add_argument("title", help="human-readable feature name")
    new.add_argument("--slug", help="override the slug derived from the title")
    new.add_argument("--stories", type=int, default=3, help="story blocks to stamp")
    new.add_argument("--number", type=int, help="override the auto-detected number")
    new.add_argument("--date", help="override today's date, YYYY-MM-DD")
    new.add_argument("--specs-dir", help="override <repo>/specs")
    new.set_defaults(func=cmd_new)

    check = sub.add_parser("check", help="validate a filled spec")
    check.add_argument("paths", nargs="*", help="spec files to check")
    check.add_argument(
        "--latest",
        action="store_true",
        help="check the highest-numbered spec directory, which may predate lightspec",
    )
    check.add_argument("--specs-dir", help="override <repo>/specs")
    check.set_defaults(func=cmd_check)

    status = sub.add_parser("status", help="read or move a spec's Status")
    status.add_argument("path", help="the spec to read or update")
    status.add_argument(
        "value", nargs="?", help="new status; omit to print the current one"
    )
    status.add_argument(
        "--by", help="for Superseded: a markdown link to the spec that replaced this"
    )
    status.set_defaults(func=cmd_status)

    args = parser.parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
