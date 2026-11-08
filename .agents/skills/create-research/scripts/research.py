#!/usr/bin/env python3
"""research - emit and validate research documents.

    research.py new "Title" [--slug SLUG] [--dir DIR] [--findings N] [--sources N]
    research.py check PATH [PATH ...]

`new` writes a fixed skeleton to <research-dir>/NNNN-slug.md, with `<FILL: ...>`
markers where prose belongs. `check` re-reads a filled document and reports every
way it drifted from that format, one problem per line, exiting 1 if any.

The skeleton is the only definition of the format; check is the only thing that
enforces it. Keep the two in step - the test suite asserts that a fresh skeleton
fails check for its FILL markers and nothing else.
"""

from __future__ import annotations

import argparse
import datetime
import json
import re
import sys
import unicodedata
from pathlib import Path

SECTIONS = (
    "Research Question",
    "Context",
    "Scope",
    "Method",
    "Findings",
    "Open Questions",
    "Conclusions",
    "Sources",
    "Revisit When",
)
PROSE_SECTIONS = ("Research Question", "Context", "Method", "Conclusions")
BULLET_SECTIONS = ("Open Questions", "Revisit When")
SCOPE_BLOCKS = ("In scope", "Out of scope")

FRONTMATTER_KEYS = (
    "id",
    "title",
    "status",
    "date",
    "authors",
    "supersedes",
    "superseded-by",
    "related",
    "tags",
)
LIST_KEYS = ("authors", "supersedes", "superseded-by", "related", "tags")
# `related` also takes ADR ids: create-adr back-links the ADRs that cite a document.
ID_LIST_KEYS = ("supersedes", "superseded-by", "related")

# Research is a snapshot of what was known on a date. The status says whether
# the reader is looking at work in progress, a finished record, or a record that
# something newer has replaced. Free text would drift into prose, so the
# vocabulary is closed and the checker holds it.
STATUS_VALUES = ("draft", "complete", "superseded")
# How far a reader may lean on a finding. A closed scale keeps "High" meaning
# the same thing in every document.
CONFIDENCE_VALUES = ("High", "Medium", "Low")
# primary: the thing itself (official docs, a spec, upstream source, a vendor).
# secondary: someone's account of it (blog, forum thread, summary).
# experiment: something run for this research, with the result recorded here.
# codebase: this repository.
SOURCE_TYPES = ("primary", "secondary", "experiment", "codebase")

FILL = "<FILL:"
MAX_SLUG_LEN = 40
DEFAULT_DIRS = ("docs/research", "research")

# An em dash separates a finding's number from its claim and a source's parts.
# Two hyphens are accepted too - agents drop em dashes, and the distinction
# carries no meaning.
DASH = r"(?:—|--)"

H1_RE = re.compile(r"^# (\S.*)$")
H2_RE = re.compile(r"^## (\S.*)$")
H3_RE = re.compile(r"^### (\S.*)$")
FENCE_RE = re.compile(r"^\s*(```|~~~)")
FRONTMATTER_LINE_RE = re.compile(r"^([a-z][a-z-]*):[ \t]*(.*)$")
ID_RE = re.compile(r"^RES-(\d{4})$")
RELATED_ID_RE = re.compile(r"^(?:RES|ADR)-\d{4}$")
FILENAME_RE = re.compile(r"^(\d{4})-([a-z0-9][a-z0-9-]*)\.md$")
SLUG_RE = re.compile(r"^[a-z0-9]+(?:-[a-z0-9]+)*$")
DATE_RE = re.compile(r"^\d{4}-\d{2}-\d{2}$")
FINDING_RE = re.compile(
    r"^F(\d+) " + DASH + r" (\S.*?) \(confidence: (" + "|".join(CONFIDENCE_VALUES) + r")\)$"
)
SOURCE_RE = re.compile(
    r"^- S(\d+) " + DASH + r" \[([^\]]+)\]\(([^)\s]+)\) " + DASH + r" ("
    + "|".join(SOURCE_TYPES) + r") " + DASH + r" accessed (\d{4}-\d{2}-\d{2})$"
)
CITATION_RE = re.compile(r"\[S(\d+)\]")
BULLET_RE = re.compile(r"^- (\S.*)$")
URL_RE = re.compile(r"^(?:[a-z][a-z0-9+.-]*:|#)", re.IGNORECASE)
FILL_SPAN_RE = re.compile(r"<FILL:[^>]*>")
FINDING_NUMBER_RE = re.compile(r"^F(\d+) ")
SOURCE_NUMBER_RE = re.compile(r"^- S(\d+) ")


# ---------------------------------------------------------------- generation


def slugify(name: str) -> str:
    ascii_name = (
        unicodedata.normalize("NFKD", name).encode("ascii", "ignore").decode("ascii")
    )
    slug = re.sub(r"[^a-z0-9]+", "-", ascii_name.lower()).strip("-")
    if len(slug) > MAX_SLUG_LEN:
        slug = slug[:MAX_SLUG_LEN].rsplit("-", 1)[0] or slug[:MAX_SLUG_LEN]
    return slug.strip("-")


def repo_root_or_none(start: Path) -> Path | None:
    """The enclosing repository, or None when there is not one.

    Link targets can only be resolved against a repository. Outside one - an
    ad-hoc draft, a document under test in a temporary directory - there is
    nothing to resolve against, and claiming the target is missing would be a
    guess.
    """
    for candidate in [start, *start.parents]:
        if (candidate / ".git").exists():
            return candidate
    return None


def resolve_research_dir(override: str | None) -> Path:
    if override:
        return Path(override)
    root = repo_root_or_none(Path.cwd().resolve()) or Path.cwd().resolve()
    for candidate in DEFAULT_DIRS:
        if (root / candidate).is_dir():
            return root / candidate
    return root / DEFAULT_DIRS[0]


def next_number(research_dir: Path) -> int:
    highest = 0
    if research_dir.is_dir():
        for child in research_dir.iterdir():
            match = FILENAME_RE.match(child.name)
            if child.is_file() and match:
                highest = max(highest, int(match.group(1)))
    return highest + 1


def skeleton(title: str, number: int, today: str, findings: int, sources: int) -> str:
    ident = f"RES-{number:04d}"
    lines = [
        "---",
        f"id: {ident}",
        f"title: {json.dumps(title, ensure_ascii=False)}",
        "status: draft",
        f"date: {today}",
        "authors: []",
        "supersedes: []",
        "superseded-by: []",
        "related: []",
        "tags: []",
        "---",
        "",
        f"# {ident}: {title}",
        "",
        "## Research Question",
        "",
        "<FILL: One question, phrased so the findings can answer it. If it cannot"
        " be answered by looking, it is a decision (an ADR) or a feature (a spec),"
        " not research.>",
        "",
        "## Context",
        "",
        "<FILL: 2-5 sentences. What prompted the question and what turns on the"
        " answer. Do not forecast the ADRs or specs that will build on this -"
        " they cite it, and create-adr adds the ADR ids to `related` afterwards.>",
        "",
        "## Scope",
        "",
        "### In scope",
        "",
        "- <FILL: what was investigated>",
        "",
        "### Out of scope",
        "",
        "- <FILL: what a reader might assume was covered and was not>",
        "",
        "## Method",
        "",
        "<FILL: How the question was investigated: what was read, searched or run,"
        " and what was deliberately not looked at. Enough for someone to repeat it.>",
        "",
        "## Findings",
        "",
    ]
    for index in range(1, findings + 1):
        lines += [
            f"### F{index} — <FILL: the claim, as a short sentence>"
            f" (confidence: <FILL: High | Medium | Low>)",
            "",
            "<FILL: What was observed, and what separates it from what is inferred."
            " Cite the sources it rests on as [S1].>",
            "",
        ]
    lines += [
        "## Open Questions",
        "",
        "- <FILL: something this research could not settle, or `None`>",
        "",
        "## Conclusions",
        "",
        "<FILL: What the evidence supports, and what it does not. State a"
        " recommendation if the findings warrant one, but choosing is a decision"
        " and belongs in an ADR.>",
        "",
        "## Sources",
        "",
    ]
    for index in range(1, sources + 1):
        lines.append(
            f"- S{index} — [<FILL: title>](<FILL: url or repo path>) —"
            f" <FILL: {' | '.join(SOURCE_TYPES)}> — accessed <FILL: YYYY-MM-DD>"
        )
    lines += [
        "",
        "## Revisit When",
        "",
        "- <FILL: a change that would make these findings stale, or `None`>",
        "",
    ]
    return "\n".join(lines)


def cmd_new(args: argparse.Namespace) -> int:
    if args.findings < 1 or args.sources < 1:
        print("research: --findings and --sources must be at least 1", file=sys.stderr)
        return 2
    title = args.title.strip()
    if not title:
        print("research: title must not be empty", file=sys.stderr)
        return 2
    if args.slug is not None and not (
        SLUG_RE.match(args.slug) and len(args.slug) <= MAX_SLUG_LEN
    ):
        print(
            f"research: slug {args.slug!r} must be lowercase letters, digits and"
            f" single hyphens, at most {MAX_SLUG_LEN} characters",
            file=sys.stderr,
        )
        return 2
    slug = args.slug or slugify(title)
    if not slug:
        print(f"research: cannot derive a slug from {title!r}", file=sys.stderr)
        return 2

    research_dir = resolve_research_dir(args.dir)
    number = next_number(research_dir)
    if number > 9999:
        print("research: no research numbers left below 10000", file=sys.stderr)
        return 2
    target = research_dir / f"{number:04d}-{slug}.md"

    today = args.date or datetime.date.today().isoformat()
    research_dir.mkdir(parents=True, exist_ok=True)
    try:
        # "x" refuses to overwrite, and fails atomically if two runs race.
        with open(target, "x", encoding="utf-8") as handle:
            handle.write(skeleton(title, number, today, args.findings, args.sources))
    except FileExistsError:
        print(f"research: {target} already exists", file=sys.stderr)
        return 2
    print(target)
    return 0


# ---------------------------------------------------------------- validation


class Problems:
    def __init__(self, path: Path) -> None:
        self.path = path
        self.items: list[tuple[int, str]] = []

    def add(self, lineno: int, message: str) -> None:
        self.items.append((lineno, message))

    def lines(self) -> list[str]:
        return [f"{self.path}:{n}: {m}" for n, m in sorted(self.items)]


Block = tuple[str, int, list[tuple[int, str]]]


def fenced_linenos(lines: list[str]) -> frozenset[int]:
    """Line numbers inside a fenced code block, delimiters included.

    A heading is only a heading outside a fence. Findings quote snippets, and a
    `## ` line inside one would otherwise register as a section and derail every
    check that follows it. An unterminated fence swallows the rest of the file,
    which surfaces as missing sections - the right complaint.
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


def split_blocks(
    lines: list[tuple[int, str]], heading_re: re.Pattern[str], fenced: frozenset[int]
) -> tuple[list[tuple[int, str]], list[Block]]:
    """Split numbered lines into a preamble and (heading, lineno, body) blocks."""
    preamble: list[tuple[int, str]] = []
    found: list[Block] = []
    for lineno, line in lines:
        match = None if lineno in fenced else heading_re.match(line)
        if match:
            found.append((match.group(1), lineno, []))
        elif found:
            found[-1][2].append((lineno, line))
        else:
            preamble.append((lineno, line))
    return preamble, found


def nonblank(body: list[tuple[int, str]]) -> list[tuple[int, str]]:
    return [(n, line) for n, line in body if line.strip()]


def parse_frontmatter(
    lines: list[str], problems: Problems
) -> tuple[dict[str, tuple[int, str]], int]:
    """Return ({key: (lineno, raw value)}, index of the first body line)."""
    if not lines or lines[0].strip() != "---":
        problems.add(1, "file must open with a `---` frontmatter block")
        return {}, 0
    end = next((i for i in range(1, len(lines)) if lines[i].strip() == "---"), None)
    if end is None:
        problems.add(1, "frontmatter is never closed with `---`")
        return {}, 0
    found: dict[str, tuple[int, str]] = {}
    for index in range(1, end):
        line = lines[index]
        if not line.strip():
            continue
        match = FRONTMATTER_LINE_RE.match(line)
        if not match:
            problems.add(index + 1, f"frontmatter line is not `key: value`: {line!r}")
            continue
        key = match.group(1)
        if key in found:
            problems.add(index + 1, f"frontmatter key {key!r} appears twice")
            continue
        found[key] = (index + 1, match.group(2).strip())
    return found, end + 1


def parse_list(raw: str) -> list[str] | None:
    """Parse a flow-style list (`[a, b]`); None when it is not one."""
    if not (raw.startswith("[") and raw.endswith("]")):
        return None
    inner = raw[1:-1].strip()
    if not inner:
        return []
    return [item.strip().strip("'\"") for item in inner.split(",")]


def parse_string(raw: str) -> str:
    if raw.startswith('"'):
        try:
            value = json.loads(raw)
        except json.JSONDecodeError:
            return raw
        return value if isinstance(value, str) else raw
    return raw.strip("'")


def check_frontmatter(
    meta: dict[str, tuple[int, str]], path: Path, problems: Problems
) -> tuple[str | None, str | None]:
    """Validate the frontmatter; return (id, title) for the H1 cross-check."""
    for key in FRONTMATTER_KEYS:
        if key not in meta:
            problems.add(1, f"frontmatter is missing `{key}`")
    for key in meta:
        if key not in FRONTMATTER_KEYS:
            problems.add(meta[key][0], f"unknown frontmatter key `{key}`")

    ident: str | None = None
    title: str | None = None

    if "id" in meta:
        lineno, raw = meta["id"]
        match = ID_RE.match(raw)
        if not match:
            problems.add(lineno, f"id must look like `RES-0001`, got {raw!r}")
        else:
            ident = raw
            file_match = FILENAME_RE.match(path.name)
            if file_match and file_match.group(1) != match.group(1):
                problems.add(
                    lineno,
                    f"id {raw} does not match the file name's number {file_match.group(1)}",
                )
    if not FILENAME_RE.match(path.name):
        problems.add(1, f"file name {path.name!r} must look like `0001-some-slug.md`")

    if "title" in meta:
        lineno, raw = meta["title"]
        title = parse_string(raw)
        if not title.strip():
            problems.add(lineno, "title must not be empty")

    status = None
    if "status" in meta:
        lineno, raw = meta["status"]
        status = raw
        if raw not in STATUS_VALUES:
            problems.add(
                lineno, f"status must be one of {', '.join(STATUS_VALUES)}, got {raw!r}"
            )

    if "date" in meta:
        lineno, raw = meta["date"]
        try:
            if not DATE_RE.match(raw):
                raise ValueError
            datetime.date.fromisoformat(raw)
        except ValueError:
            problems.add(lineno, f"date must be a real YYYY-MM-DD date, got {raw!r}")

    lists: dict[str, list[str]] = {}
    for key in LIST_KEYS:
        if key not in meta:
            continue
        lineno, raw = meta[key]
        items = parse_list(raw)
        if items is None:
            problems.add(lineno, f"`{key}` must be a list such as `[]` or `[a, b]`")
            continue
        lists[key] = items
        if key in ID_LIST_KEYS:
            for item in items:
                if key == "related":
                    if not RELATED_ID_RE.match(item):
                        problems.add(
                            lineno,
                            f"`related` entries must look like RES-0001 or ADR-0001, got {item!r}",
                        )
                    elif item == ident:
                        problems.add(lineno, "`related` lists the document itself")
                elif not ID_RE.match(item):
                    problems.add(lineno, f"`{key}` entries must look like RES-0001, got {item!r}")
                elif item == ident:
                    problems.add(lineno, f"`{key}` lists the document itself")

    if status == "superseded" and not lists.get("superseded-by"):
        problems.add(
            meta["status"][0],
            "a superseded document must name what replaced it in `superseded-by`",
        )
    return ident, title


def link_exists(target: str, path: Path, root: Path) -> bool:
    bare = target.split("#", 1)[0]
    if not bare:
        return True
    return (root / bare).exists() or (path.parent / bare).exists()


def check_findings(
    body: list[tuple[int, str]], fenced: frozenset[int], problems: Problems
) -> tuple[dict[int, int], set[int]]:
    """Check findings; return ({finding number: lineno}, source ids cited)."""
    preamble, blocks = split_blocks(body, H3_RE, fenced)
    for lineno, _ in nonblank(preamble):
        problems.add(lineno, "Findings text must sit under an `### F<n> — claim (confidence: ...)` heading")
        break
    if not blocks:
        problems.add(body[0][0] - 1 if body else 1, "Findings has no `### F1 — ...` entries")

    cited: set[int] = set()
    cited_at: dict[int, int] = {}
    expected = 1
    for heading, lineno, text in blocks:
        # A heading or body that still carries a FILL marker is reported once,
        # as unfilled. Judging its shape too would complain about the
        # instruction text itself, and the skeleton's instructions mention
        # `[S1]` as an example, not as a citation.
        heading_open = FILL in heading
        body_open = any(FILL in line for _, line in text)
        match = FINDING_RE.match(heading)
        if not match and not heading_open:
            problems.add(
                lineno,
                "finding heading must be `### F<n> — <claim> (confidence: High|Medium|Low)`",
            )
        number_match = match or FINDING_NUMBER_RE.match(heading)
        if number_match is None:
            expected += 1
            continue
        number = int(number_match.group(1))
        if number != expected:
            problems.add(lineno, f"finding is numbered F{number}, expected F{expected}")
        expected = number + 1
        refs = {
            int(m.group(1))
            for line_no, line in text
            if line_no not in fenced
            for m in CITATION_RE.finditer(FILL_SPAN_RE.sub("", line))
        }
        if not nonblank(text):
            problems.add(lineno, f"F{number} has no body")
        elif not refs and not body_open:
            problems.add(
                lineno, f"F{number} cites no source - add at least one [S<n>] reference"
            )
        for ref in refs:
            cited.add(ref)
            cited_at.setdefault(ref, lineno)
    return cited_at, cited


def check_sources(
    body: list[tuple[int, str]],
    path: Path,
    root: Path | None,
    problems: Problems,
    unfilled: set[int],
) -> dict[int, int]:
    """Check source lines; return {source number: lineno}.

    Numbers of lines still carrying a FILL marker go into `unfilled`: they are
    reported as unfilled, and not again as uncited.
    """
    found: dict[int, int] = {}
    expected = 1
    for lineno, line in nonblank(body):
        match = SOURCE_RE.match(line)
        if not match:
            number_match = SOURCE_NUMBER_RE.match(line)
            if FILL in line and number_match:
                found[int(number_match.group(1))] = lineno
                unfilled.add(int(number_match.group(1)))
            else:
                problems.add(
                    lineno,
                    "source line must be `- S<n> — [title](url or path) — "
                    f"{'|'.join(SOURCE_TYPES)} — accessed YYYY-MM-DD`",
                )
            expected += 1
            continue
        number, _title, target, _kind, accessed = match.groups()
        number = int(number)
        if number != expected:
            problems.add(lineno, f"source is numbered S{number}, expected S{expected}")
        expected = number + 1
        found[number] = lineno
        try:
            datetime.date.fromisoformat(accessed)
        except ValueError:
            problems.add(lineno, f"S{number} accessed date {accessed!r} is not a real date")
        if root is not None and not URL_RE.match(target) and not link_exists(target, path, root):
            problems.add(lineno, f"S{number} links to {target!r}, which does not exist")
    if not nonblank(body):
        problems.add(body[0][0] - 1 if body else 1, "Sources lists nothing")
    return found


def check_text(text: str, path: Path, root: Path | None) -> list[str]:
    """Return one line per way `text` departs from the format."""
    problems = Problems(path)
    lines = text.splitlines()

    for lineno, line in enumerate(lines, start=1):
        if FILL in line:
            problems.add(lineno, "unfilled `<FILL: ...>` marker")

    meta, body_start = parse_frontmatter(lines, problems)
    ident, title = check_frontmatter(meta, path, problems)

    numbered = [(n, line) for n, line in enumerate(lines, start=1) if n > body_start]
    fenced = fenced_linenos(lines)

    first = nonblank(numbered)
    if not first or not H1_RE.match(first[0][1]):
        problems.add(first[0][0] if first else body_start + 1, "the title must be an H1 right after the frontmatter")
    elif ident and title and first[0][1] != f"# {ident}: {title}":
        problems.add(first[0][0], f"H1 must be `# {ident}: {title}` to match the frontmatter")

    _, sections = split_blocks(numbered, H2_RE, fenced)
    names = [name for name, _, _ in sections]
    for name in SECTIONS:
        if name not in names:
            problems.add(1, f"missing section `## {name}`")
    for name, lineno, _ in sections:
        if name not in SECTIONS:
            problems.add(lineno, f"unexpected section `## {name}`")
    for name in set(names):
        if names.count(name) > 1:
            problems.add(1, f"section `## {name}` appears more than once")
    known = [name for name in names if name in SECTIONS and names.count(name) == 1]
    if known != [name for name in SECTIONS if name in known]:
        problems.add(1, "sections are out of order; expected " + ", ".join(SECTIONS))

    by_name = {name: (lineno, body) for name, lineno, body in sections if names.count(name) == 1}

    for name in PROSE_SECTIONS:
        if name in by_name and not nonblank(by_name[name][1]):
            problems.add(by_name[name][0], f"`## {name}` is empty")

    if "Research Question" in by_name:
        question = " ".join(line for _, line in nonblank(by_name["Research Question"][1]))
        if question and FILL not in question and "?" not in question:
            problems.add(
                by_name["Research Question"][0],
                "the research question must be phrased as a question",
            )

    for name in BULLET_SECTIONS:
        if name in by_name:
            lineno, body = by_name[name]
            if not any(BULLET_RE.match(line) for _, line in nonblank(body)):
                problems.add(lineno, f"`## {name}` needs at least one bullet (`- None` is fine)")

    if "Scope" in by_name:
        lineno, body = by_name["Scope"]
        _, blocks = split_blocks(body, H3_RE, fenced)
        titles = [t for t, _, _ in blocks]
        for wanted in SCOPE_BLOCKS:
            if wanted not in titles:
                problems.add(lineno, f"`## Scope` is missing `### {wanted}`")
        for t, block_lineno, text in blocks:
            if t not in SCOPE_BLOCKS:
                problems.add(block_lineno, f"unexpected `### {t}` under Scope")
            elif not any(BULLET_RE.match(line) for _, line in nonblank(text)):
                problems.add(block_lineno, f"`### {t}` needs at least one bullet")

    cited_at: dict[int, int] = {}
    if "Findings" in by_name:
        cited_at, _ = check_findings(by_name["Findings"][1], fenced, problems)

    source_lines: dict[int, int] = {}
    if "Sources" in by_name:
        unfilled_sources: set[int] = set()
        source_lines = check_sources(
            by_name["Sources"][1], path, root, problems, unfilled_sources
        )
        if source_lines or cited_at:
            for number, lineno in cited_at.items():
                if number not in source_lines:
                    problems.add(lineno, f"cites [S{number}], which is not in Sources")
            for number, lineno in source_lines.items():
                if number not in cited_at and number not in unfilled_sources:
                    problems.add(lineno, f"S{number} is listed but no finding cites it")

    return problems.lines()


def cmd_check(args: argparse.Namespace) -> int:
    failed = False
    for raw in args.paths:
        path = Path(raw)
        try:
            text = path.read_text(encoding="utf-8")
        except OSError as error:
            print(f"{path}: cannot read: {error.strerror or error}")
            failed = True
            continue
        root = repo_root_or_none(path.resolve().parent)
        for line in check_text(text, path, root):
            print(line)
            failed = True
    if not failed:
        print("ok")
    return 1 if failed else 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        prog="research", description="Emit and validate research documents."
    )
    commands = parser.add_subparsers(dest="command", required=True)

    new = commands.add_parser("new", help="write a skeleton research document")
    new.add_argument("title", help="what the document is called")
    new.add_argument("--slug", help="file name slug (default: derived from the title)")
    new.add_argument(
        "--dir",
        help="research directory (default: docs/research or research/ if present,"
        " else docs/research)",
    )
    new.add_argument("--findings", type=int, default=3, help="finding slots (default 3)")
    new.add_argument("--sources", type=int, default=3, help="source slots (default 3)")
    new.add_argument("--date", help=argparse.SUPPRESS)
    new.set_defaults(func=cmd_new)

    check = commands.add_parser("check", help="validate research documents")
    check.add_argument("paths", nargs="+", metavar="PATH")
    check.set_defaults(func=cmd_check)

    args = parser.parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
