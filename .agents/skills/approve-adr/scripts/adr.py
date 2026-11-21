#!/usr/bin/env python3
"""adr - validate Architectural Decision Records and move them to Accepted.

    adr.py check [--ready] PATH [PATH ...]
    adr.py status PATH [accepted]
    adr.py backlinks PATH

`check` re-reads an ADR and reports every way it departs from the template that
`init-adr.sh` scaffolds, one problem per line, exiting 1 if any. With `--ready` it
also holds the document to what accepting it requires: still `proposed`, every ADR
it supersedes accepted, every research document it cites `complete`.

`status` prints an ADR's status. Given `accepted` it moves a `proposed` ADR there,
and only if it passes `check --ready`, so approving an ADR that does not hold
together cannot happen because the approver forgot to look.

`backlinks` applies the edges acceptance creates on other documents: each ADR this
one supersedes becomes `superseded` and names it in `superseded-by`, and each
research document it cites gains its id in `related`. It is safe to run twice.

The template in assets/adr-template.md is the only definition of the format; check
is the only thing that enforces it. The test suite scaffolds a fresh ADR and asserts
it fails check for its unfilled placeholders and the empty `deciders` list, nothing
else.
"""

from __future__ import annotations

import argparse
import datetime
import json
import re
import sys
from pathlib import Path

SECTIONS = (
    "Context and Problem Statement",
    "Decision Drivers",
    "Considered Options",
    "Decision Outcome",
    "Consequences",
    "Pros and Cons of the Options",
    "Implementation Notes",
    "References",
)

FRONTMATTER_KEYS = (
    "id",
    "title",
    "status",
    "date",
    "deciders",
    "supersedes",
    "superseded-by",
    "related",
    "research",
    "tags",
)
LIST_KEYS = ("deciders", "supersedes", "superseded-by", "related", "research", "tags")
ADR_ID_LIST_KEYS = ("supersedes", "superseded-by", "related")

# proposed: written, not yet policy. accepted: policy, which review-policy holds
# code to. superseded: history, replaced by a later ADR. A closed vocabulary,
# because free text drifts and a reader needs to know at a glance what binds.
STATUS_VALUES = ("proposed", "accepted", "superseded")
SETTABLE_STATUS = "accepted"

ID_RE = re.compile(r"^ADR-(\d{4})$")
RES_ID_RE = re.compile(r"^RES-(\d{4})$")
FILENAME_RE = re.compile(r"^(\d{4})-([a-z0-9][a-z0-9-]*)\.md$")
DATE_RE = re.compile(r"^\d{4}-\d{2}-\d{2}$")
H1_RE = re.compile(r"^# (\S.*)$")
H2_RE = re.compile(r"^## (\S.*)$")
FENCE_RE = re.compile(r"^\s*(```|~~~)")
FRONTMATTER_LINE_RE = re.compile(r"^([a-z][a-z-]*):[ \t]*(.*)$")
INLINE_CODE_RE = re.compile(r"`[^`]*`")
# The template marks every blank as `[Some instruction]` or `[...]`. A bracketed
# span with words in it, or a bare ellipsis, that is neither a link (`[x](y)`), a
# reference link (`[x][y]`) nor a footnote-sized `[1]` is a blank nobody filled.
PLACEHOLDER_RE = re.compile(
    r"(?<![\]\[])\[(?:[^\[\]\n]*[A-Za-z][^\[\]\n]{2,}|\.\.\.|…)\](?![(\[:])"
)
LINK_DEFINITION_RE = re.compile(r"^\s*\[[^\]]+\]:")

RESEARCH_DIRS = ("docs/research", "research")


# ---------------------------------------------------------------- parsing


class Problems:
    def __init__(self, path: Path) -> None:
        self.path = path
        self.items: list[tuple[int, str]] = []

    def add(self, lineno: int, message: str) -> None:
        self.items.append((lineno, message))

    def lines(self) -> list[str]:
        return [f"{self.path}:{n}: {m}" for n, m in sorted(self.items)]


def repo_root_or_none(start: Path) -> Path | None:
    """The enclosing repository, or None when there is not one.

    Research can only be looked up against a repository. Outside one - a document
    under test in a temporary directory - claiming it is missing would be a guess.
    """
    for candidate in [start, *start.parents]:
        if (candidate / ".git").exists():
            return candidate
    return None


def fenced_linenos(lines: list[str]) -> frozenset[int]:
    """Line numbers inside a fenced code block, delimiters included."""
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


def read_meta(path: Path) -> dict[str, str]:
    """Raw frontmatter values of another document; empty if it cannot be read."""
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except OSError:
        return {}
    meta, _ = parse_frontmatter(lines, Problems(path))
    return {key: raw for key, (_, raw) in meta.items()}


def find_doc(directory: Path, ident: str) -> Path | None:
    """The `NNNN-slug.md` in `directory` whose number matches `ident`."""
    number = ident.split("-", 1)[1]
    if not directory.is_dir():
        return None
    for child in sorted(directory.iterdir()):
        match = FILENAME_RE.match(child.name)
        if child.is_file() and match and match.group(1) == number:
            return child
    return None


def research_dir(root: Path | None) -> Path | None:
    if root is None:
        return None
    for candidate in RESEARCH_DIRS:
        if (root / candidate).is_dir():
            return root / candidate
    return None


# ---------------------------------------------------------------- validation


def check_frontmatter(
    meta: dict[str, tuple[int, str]], path: Path, root: Path | None, problems: Problems
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
            problems.add(lineno, f"id must look like `ADR-0001`, got {raw!r}")
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
    else:
        # Links resolve an id to the first file carrying that number, so a second one
        # would make supersedes, backlinks and the --ready gate act on the wrong ADR.
        number = FILENAME_RE.match(path.name).group(1)  # type: ignore[union-attr]
        twins = [
            sibling.name
            for sibling in (sorted(path.parent.iterdir()) if path.parent.is_dir() else [])
            if sibling.name != path.name
            and sibling.is_file()
            and (m := FILENAME_RE.match(sibling.name))
            and m.group(1) == number
        ]
        if twins:
            problems.add(1, f"ADR number {number} is also used by {', '.join(twins)}; numbers must be unique")

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
        if key in ADR_ID_LIST_KEYS:
            for item in items:
                if not ID_RE.match(item):
                    problems.add(lineno, f"`{key}` entries must look like ADR-0001, got {item!r}")
                elif item == ident:
                    problems.add(lineno, f"`{key}` lists the document itself")
                elif find_doc(path.parent, item) is None:
                    problems.add(lineno, f"`{key}` names {item}, which is not in {path.parent}")
        elif key == "research":
            for item in items:
                if not RES_ID_RE.match(item):
                    problems.add(lineno, f"`research` entries must look like RES-0001, got {item!r}")

    if "deciders" in lists and not lists["deciders"]:
        problems.add(
            meta["deciders"][0],
            "`deciders` is empty; name the people deciding (ask - do not guess)",
        )

    if lists.get("research") and root is not None:
        directory = research_dir(root)
        for item in lists["research"]:
            if RES_ID_RE.match(item) and (directory is None or find_doc(directory, item) is None):
                problems.add(meta["research"][0], f"`research` names {item}, which is not in the repository's research directory")

    if status == "superseded" and not lists.get("superseded-by"):
        problems.add(
            meta["status"][0],
            "a superseded ADR must name what replaced it in `superseded-by`",
        )
    if status in ("proposed", "accepted") and lists.get("superseded-by"):
        problems.add(
            meta["superseded-by"][0],
            f"`superseded-by` is set but the status is {status}; it should be superseded",
        )
    return ident, title


def check_placeholders(
    lines: list[str], body_start: int, fenced: frozenset[int], problems: Problems
) -> None:
    for lineno, line in enumerate(lines, start=1):
        if lineno <= body_start or lineno in fenced or LINK_DEFINITION_RE.match(line):
            continue
        match = PLACEHOLDER_RE.search(INLINE_CODE_RE.sub("", line))
        if match:
            problems.add(lineno, f"unfilled template placeholder {match.group(0)}")


def split_sections(
    numbered: list[tuple[int, str]], fenced: frozenset[int]
) -> list[tuple[str, int, list[tuple[int, str]]]]:
    found: list[tuple[str, int, list[tuple[int, str]]]] = []
    for lineno, line in numbered:
        match = None if lineno in fenced else H2_RE.match(line)
        if match:
            found.append((match.group(1), lineno, []))
        elif found:
            found[-1][2].append((lineno, line))
    return found


def check_text(text: str, path: Path, root: Path | None) -> list[str]:
    """Return one line per way `text` departs from the format."""
    problems = Problems(path)
    lines = text.splitlines()
    fenced = fenced_linenos(lines)

    meta, body_start = parse_frontmatter(lines, problems)
    ident, title = check_frontmatter(meta, path, root, problems)
    check_placeholders(lines, body_start, fenced, problems)

    numbered = [(n, line) for n, line in enumerate(lines, start=1) if n > body_start]
    first = next(((n, line) for n, line in numbered if line.strip()), None)
    if first is None or not H1_RE.match(first[1]):
        problems.add(first[0] if first else body_start + 1, "the title must be an H1 right after the frontmatter")
    elif ident and title and first[1] != f"# {ident}: {title}":
        problems.add(first[0], f"H1 must be `# {ident}: {title}` to match the frontmatter")

    sections = split_sections(numbered, fenced)
    names = [name for name, _, _ in sections]
    for name in SECTIONS:
        if name not in names:
            problems.add(1, f"missing section `## {name}`")
        elif names.count(name) > 1:
            problems.add(1, f"section `## {name}` appears more than once")
    # Projects add their own sections (create-adr reads the house style from earlier
    # ADRs), so only the template's own sections are held in order.
    known = [name for name in names if name in SECTIONS and names.count(name) == 1]
    if known != [name for name in SECTIONS if name in known]:
        problems.add(1, "sections are out of order; expected " + ", ".join(SECTIONS))
    for name, lineno, body in sections:
        if name in SECTIONS and names.count(name) == 1 and not any(line.strip() for _, line in body):
            problems.add(lineno, f"`## {name}` is empty")

    return problems.lines()


def check_ready(path: Path, root: Path | None) -> list[str]:
    """Problems that stop an ADR being accepted, beyond a well-formed document."""
    problems = Problems(path)
    meta = read_meta(path)
    status = meta.get("status")
    if status != "proposed":
        problems.add(1, f"only a `proposed` ADR can be accepted; this one is {status!r}")
    for ident in parse_list(meta.get("supersedes", "[]")) or []:
        old = find_doc(path.parent, ident)
        old_status = read_meta(old).get("status") if old else None
        if old_status != "accepted":
            problems.add(1, f"supersedes {ident}, which is {old_status!r}; only an accepted ADR can be superseded")
    directory = research_dir(root)
    for ident in parse_list(meta.get("research", "[]")) or []:
        doc = find_doc(directory, ident) if directory else None
        if doc is None:
            continue  # reported by check_frontmatter, with the repository known
        doc_status = read_meta(doc).get("status")
        if doc_status != "complete":
            problems.add(1, f"cites {ident}, which is {doc_status!r}; only `complete` research can be relied on")
    return problems.lines()


def check_file(path: Path, ready: bool = False) -> list[str]:
    try:
        text = path.read_text(encoding="utf-8")
    except OSError as error:
        return [f"{path}: cannot read: {error.strerror or error}"]
    root = repo_root_or_none(path.resolve().parent)
    found = check_text(text, path, root)
    if ready:
        found += check_ready(path, root)
    return found


def cmd_check(args: argparse.Namespace) -> int:
    failed = False
    for raw in args.paths:
        for line in check_file(Path(raw), args.ready):
            print(line)
            failed = True
    if not failed:
        print("ok")
    return 1 if failed else 0


# ---------------------------------------------------------------- edits


def set_field(text: str, key: str, value: str) -> str:
    """Rewrite one frontmatter line, leaving everything else byte-identical."""
    lines = text.split("\n")
    end = next(i for i in range(1, len(lines)) if lines[i].strip() == "---")
    for index in range(1, end):
        if re.match(rf"^{re.escape(key)}:", lines[index]):
            lines[index] = f"{key}: {value}"
            return "\n".join(lines)
    raise KeyError(key)


def format_list(items: list[str]) -> str:
    return "[" + ", ".join(items) + "]"


def add_to_list(path: Path, key: str, ident: str) -> bool:
    """Append `ident` to a frontmatter list; False if it was already there."""
    text = path.read_text(encoding="utf-8")
    current = parse_list(read_meta(path).get(key, "[]")) or []
    if ident in current:
        return False
    path.write_text(set_field(text, key, format_list([*current, ident])), encoding="utf-8")
    return True


def cmd_status(args: argparse.Namespace) -> int:
    path = Path(args.path)
    if not path.is_file():
        print(f"adr: {path} not found", file=sys.stderr)
        return 2
    current = read_meta(path).get("status")
    if current is None:
        print(f"adr: {path} has no `status` line", file=sys.stderr)
        return 2
    if args.value is None:
        print(current)
        return 0
    if args.value != SETTABLE_STATUS:
        print(
            f"adr: only `{SETTABLE_STATUS}` can be set here; `superseded` is applied to the"
            " old ADR by `backlinks` when the ADR that replaces it is accepted",
            file=sys.stderr,
        )
        return 2

    found = check_file(path, ready=True)
    if found:
        for line in found:
            print(line)
        print(
            "adr: refusing to accept an ADR that does not pass `check --ready`;"
            " fix the above first",
            file=sys.stderr,
        )
        return 1

    today = args.date or datetime.date.today().isoformat()
    text = set_field(path.read_text(encoding="utf-8"), "status", SETTABLE_STATUS)
    path.write_text(set_field(text, "date", today), encoding="utf-8")
    print(f"{path}: {current} -> {SETTABLE_STATUS} ({today})")
    return 0


def cmd_backlinks(args: argparse.Namespace) -> int:
    path = Path(args.path)
    if not path.is_file():
        print(f"adr: {path} not found", file=sys.stderr)
        return 2
    found = check_file(path)
    if found:
        for line in found:
            print(line)
        print("adr: refusing to link from an ADR that does not pass `check`", file=sys.stderr)
        return 1
    meta = read_meta(path)
    if meta.get("status") != "accepted":
        print("adr: backlinks apply only to an accepted ADR", file=sys.stderr)
        return 2
    ident = meta["id"]
    changed: list[str] = []

    for old_id in parse_list(meta.get("supersedes", "[]")) or []:
        old = find_doc(path.parent, old_id)
        if old is None:
            continue
        before = read_meta(old).get("status")
        text = set_field(old.read_text(encoding="utf-8"), "status", "superseded")
        old.write_text(text, encoding="utf-8")
        listed = add_to_list(old, "superseded-by", ident)
        if before != "superseded" or listed:
            changed.append(f"{old}: status {before} -> superseded, superseded-by + {ident}")

    directory = research_dir(repo_root_or_none(path.resolve().parent))
    for res_id in parse_list(meta.get("research", "[]")) or []:
        doc = find_doc(directory, res_id) if directory else None
        if doc is not None and add_to_list(doc, "related", ident):
            changed.append(f"{doc}: related + {ident}")

    print("\n".join(changed) if changed else "nothing to do")
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="adr", description=__doc__.split("\n\n")[0])
    commands = parser.add_subparsers(dest="command", required=True)

    check = commands.add_parser("check", help="validate ADRs")
    check.add_argument("paths", nargs="+", metavar="PATH")
    check.add_argument(
        "--ready",
        action="store_true",
        help="also require what accepting needs: proposed, supersedes accepted, research complete",
    )
    check.set_defaults(func=cmd_check)

    status = commands.add_parser("status", help="read an ADR's status, or accept it")
    status.add_argument("path", help="the ADR to read or update")
    status.add_argument("value", nargs="?", help="`accepted`; omit to print the current status")
    status.add_argument("--date", help=argparse.SUPPRESS)
    status.set_defaults(func=cmd_status)

    backlinks = commands.add_parser(
        "backlinks", help="mark superseded ADRs and link cited research back to this ADR"
    )
    backlinks.add_argument("path", help="the accepted ADR")
    backlinks.set_defaults(func=cmd_backlinks)

    args = parser.parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
