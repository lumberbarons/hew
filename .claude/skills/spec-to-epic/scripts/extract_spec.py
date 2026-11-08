#!/usr/bin/env python3
"""extract_spec - read a lightspec-format spec.md into structured JSON.

    extract_spec.py specs/NNN-slug/spec.md

Prints one JSON object to stdout: title, slug, status, the four framing
sections (description, prior_decisions, out_of_scope, assumptions), each user
story with its id/title/priority/story-sentence/narrative/tasks, any
Cross-cutting tasks, and the epic-level done_when list.

This exists so that turning a spec into hew issues never involves retyping a
path or a task id by hand. A path transcribed by eye is a path that can drift
from the one lightspec's own checker validated; a path read out of this
script's JSON is the same string the spec author wrote. What the JSON does
NOT contain is any judgment call - which done-when item belongs to which
story, how a story's narrative becomes a goal, whether a story is really two.
Those stay a job for whoever reads the JSON, human or agent, because they are
exactly the calls a script should not make silently.

Deliberately not a validator: it trusts the spec is already well-formed
(lightspec check should have passed, and approve-spec should have moved it to
Accepted before this script is ever run) and raises on the specific shapes it
cannot parse rather than guessing. Run lightspec's own `check` first if there
is any doubt.
"""

from __future__ import annotations

import json
import re
import sys
from pathlib import Path

# Mirrors lightspec.py's own DASH: agents drop em dashes for a plain double
# hyphen, and the distinction carries no meaning worth rejecting on here.
DASH = r"(?:—|--)"

H1_RE = re.compile(r"^# (\S.*)$")
H2_RE = re.compile(r"^## (\S.*)$")
H3_RE = re.compile(r"^### (\S.*)$")
SLUG_LINE_RE = re.compile(r"^\*\*Slug\*\*: `([^`]+)`$")
STATUS_LINE_RE = re.compile(r"^\*\*Status\*\*: (\S+)")
STORY_HEAD_RE = re.compile(r"^US(\d+) " + DASH + r" (\S.*?) \(P([0-4])\)$")
TASK_GROUP_RE = re.compile(r"^US(\d+)$")
CROSS_CUTTING = "Cross-cutting"
TASK_RE = re.compile(
    r"^- \[[ xX]\] (T\d{3}) (\S.*?) " + DASH + r" (`[^`]+`(?:, `[^`]+`)*)$"
)
CHECKBOX_RE = re.compile(r"^- \[[ xX]\] (\S.*)$")
BULLET_RE = re.compile(r"^- (\S.*)$")
PATH_RE = re.compile(r"`([^`]+)`")


class SpecFormatError(ValueError):
    """The file does not look like a lightspec spec, in a specific way.

    Raised rather than returning a partial result - a caller piping this into
    an hew plan wants a loud failure over a JSON document quietly missing the
    one story that failed to parse.
    """


def _sections(lines: list[str]) -> dict[str, list[str]]:
    found: dict[str, list[str]] = {}
    current: str | None = None
    for line in lines:
        match = H2_RE.match(line)
        if match:
            current = match.group(1)
            found[current] = []
            continue
        if current is not None:
            found[current].append(line)
    return found


def _blocks(body: list[str]) -> list[tuple[str, list[str]]]:
    found: list[tuple[str, list[str]]] = []
    for line in body:
        match = H3_RE.match(line)
        if match:
            found.append((match.group(1), []))
        elif found:
            found[-1][1].append(line)
    return found


def _bullets(body: list[str]) -> list[str]:
    return [
        BULLET_RE.match(line).group(1)
        for line in body
        if line.strip() and BULLET_RE.match(line)
    ]


def _checklist(body: list[str]) -> list[str]:
    return [
        CHECKBOX_RE.match(line).group(1)
        for line in body
        if line.strip() and CHECKBOX_RE.match(line)
    ]


def _paragraphs(body: list[str]) -> list[str]:
    found: list[str] = []
    current: list[str] = []
    for line in body:
        if line.strip():
            current.append(line.strip())
        elif current:
            found.append(" ".join(current))
            current = []
    if current:
        found.append(" ".join(current))
    return found


def parse(text: str, path: Path) -> dict:
    lines = [line.rstrip() for line in text.splitlines()]

    title_line = next((line for line in lines if line.strip()), None)
    title_match = title_line and H1_RE.match(title_line)
    if not title_match:
        raise SpecFormatError(f"{path}: first non-blank line is not an H1 title")
    title = title_match.group(1)

    slug_match = next(
        (SLUG_LINE_RE.match(line) for line in lines if SLUG_LINE_RE.match(line)), None
    )
    status_match = next(
        (STATUS_LINE_RE.match(line) for line in lines if STATUS_LINE_RE.match(line)),
        None,
    )
    if not status_match:
        raise SpecFormatError(f"{path}: no `**Status**:` line found")

    sections = _sections(lines)
    for required in ("Description", "Prior Decisions", "Out of Scope", "Assumptions",
                      "User Stories", "Tasks", "Done When"):
        if required not in sections:
            raise SpecFormatError(f"{path}: missing `## {required}` section")

    description = " ".join(_paragraphs(sections["Description"]))
    prior_decisions = _bullets(sections["Prior Decisions"])
    out_of_scope = _bullets(sections["Out of Scope"])
    assumptions = _bullets(sections["Assumptions"])
    done_when = _checklist(sections["Done When"])

    stories: dict[int, dict] = {}
    for heading, body in _blocks(sections["User Stories"]):
        match = STORY_HEAD_RE.match(heading)
        if not match:
            raise SpecFormatError(f"{path}: unparseable story heading `{heading}`")
        number = int(match.group(1))
        paras = _paragraphs(body)
        if not paras:
            raise SpecFormatError(f"{path}: US{number} has no body")
        stories[number] = {
            "id": number,
            "title": match.group(2),
            "priority": f"P{match.group(3)}",
            "story": paras[0],
            "narrative": " ".join(paras[1:]),
            "tasks": [],
        }

    cross_cutting_tasks: list[dict] = []
    for heading, body in _blocks(sections["Tasks"]):
        group_match = TASK_GROUP_RE.match(heading)
        target = None
        if group_match:
            number = int(group_match.group(1))
            if number not in stories:
                raise SpecFormatError(
                    f"{path}: task group `US{number}` has no matching story"
                )
            target = stories[number]["tasks"]
        elif heading == CROSS_CUTTING:
            target = cross_cutting_tasks
        else:
            raise SpecFormatError(f"{path}: unrecognised task group `{heading}`")

        for line in body:
            if not line.strip():
                continue
            task_match = TASK_RE.match(line)
            if not task_match:
                raise SpecFormatError(f"{path}: unparseable task line `{line}`")
            paths = PATH_RE.findall(task_match.group(3))
            target.append(
                {"id": task_match.group(1), "action": task_match.group(2), "paths": paths}
            )

    for number, story in stories.items():
        if not story["tasks"]:
            raise SpecFormatError(f"{path}: US{number} has no tasks")

    return {
        "title": title,
        "slug": slug_match.group(1) if slug_match else None,
        "status": status_match.group(1),
        "path": str(path),
        "description": description,
        "prior_decisions": prior_decisions,
        "out_of_scope": out_of_scope,
        "assumptions": assumptions,
        "stories": [stories[number] for number in sorted(stories)],
        "cross_cutting_tasks": cross_cutting_tasks,
        "done_when": done_when,
    }


def main(argv: list[str] | None = None) -> int:
    argv = sys.argv[1:] if argv is None else argv
    if len(argv) != 1:
        print("usage: extract_spec.py <path/to/spec.md>", file=sys.stderr)
        return 2

    path = Path(argv[0])
    if not path.is_file():
        print(f"extract_spec: {path} not found", file=sys.stderr)
        return 2

    try:
        data = parse(path.read_text(encoding="utf-8"), path)
    except SpecFormatError as exc:
        print(f"extract_spec: {exc}", file=sys.stderr)
        return 1

    print(json.dumps(data, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
