#!/usr/bin/env python3
"""Read a lightspec spec or generate a hew JSONL plan for Accepted work items."""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

import lightspec


class SpecFormatError(ValueError):
    """The spec cannot be read or filed in the requested format."""


def parse_legacy_tasks(
    tasks: list[tuple[int, str]],
    fenced: frozenset[int],
    stories: dict[int, dict],
    path: Path,
) -> list[dict]:
    cross_cutting_tasks: list[dict] = []
    for heading, _, body in lightspec.blocks(tasks, fenced)[1]:
        group_match = lightspec.TASK_GROUP_RE.match(heading)
        target = None
        if group_match:
            number = int(group_match.group(1))
            if number not in stories:
                raise SpecFormatError(
                    f"{path}: task group `US{number}` has no matching story"
                )
            target = stories[number]["tasks"]
        elif heading == lightspec.CROSS_CUTTING:
            target = cross_cutting_tasks
        else:
            raise SpecFormatError(f"{path}: unrecognised task group `{heading}`")

        for _, line in lightspec.nonblank(body):
            task_match = lightspec.TASK_RE.match(line)
            if not task_match:
                raise SpecFormatError(f"{path}: unparseable task line `{line}`")
            paths = lightspec.PATH_RE.findall(task_match.group(3))
            target.append(
                {"id": f"T{task_match.group(1)}", "action": task_match.group(2), "paths": paths}
            )

    for number, story in stories.items():
        if not story["tasks"]:
            raise SpecFormatError(f"{path}: US{number} has no tasks")

    return cross_cutting_tasks


def parse(text: str, path: Path) -> dict:
    lines = [line.rstrip() for line in text.splitlines()]
    title_line = next((line for line in lines if line.strip()), "")
    title = lightspec.H1_RE.fullmatch(title_line)
    if not title:
        raise SpecFormatError(f"{path}: first non-blank line is not an H1 title")

    slug = next((m for line in lines if (m := lightspec.SLUG_LINE_RE.match(line))), None)
    status = next((m for line in lines if (m := lightspec.STATUS_LINE_RE.match(line))), None)
    if not status:
        raise SpecFormatError(f"{path}: no valid `**Status**:` line found")

    fenced = lightspec.fenced_linenos(lines)
    sections = {name: (lineno, body) for name, lineno, body in lightspec.sections(lines, fenced)}
    legacy = "Tasks" in sections and "Work Items" not in sections
    work_section = "Tasks" if legacy else "Work Items"
    for name in lightspec.REQUIRED_SECTIONS:
        name = work_section if name == "Work Items" else name
        if name not in sections:
            raise SpecFormatError(f"{path}: missing `## {name}` section")
    if not legacy and "Tasks" in sections:
        raise SpecFormatError(f"{path}: use either `## Tasks` or `## Work Items`")

    story_pattern = lightspec.LEGACY_STORY_HEAD_RE if legacy else lightspec.STORY_HEAD_RE
    stories: dict[int, dict] = {}
    for heading, _, body in lightspec.blocks(sections["User Stories"][1], fenced)[1]:
        match = story_pattern.fullmatch(heading)
        if not match:
            raise SpecFormatError(f"{path}: unparseable story heading `{heading}`")
        number = int(match.group(1))
        paras = [paragraph for _, paragraph in lightspec.paragraphs(body)]
        if not paras:
            raise SpecFormatError(f"{path}: US{number} has no body")
        story = {
            "id": number, "title": match.group(2), "story": paras[0],
            "narrative": " ".join(paras[1:]),
        }
        if legacy:
            story.update(priority=f"P{match.group(3)}", tasks=[])
        stories[number] = story

    problems = lightspec.Problems(path)
    work_items = []
    cross_cutting_tasks = []
    done_lineno, done_body = sections["Done When"]
    if legacy:
        cross_cutting_tasks = parse_legacy_tasks(sections["Tasks"][1], fenced, stories, path)
        done_when = [
            match.group(1) for _, line in done_body
            if (match := lightspec.CHECKBOX_RE.fullmatch(line))
        ]
    else:
        work_lineno, work_body = sections["Work Items"]
        work_items = lightspec.parse_work_items(
            work_body, work_lineno, {n: s["title"] for n, s in stories.items()}, fenced, problems,
        )
        done_when = lightspec.parse_bullets(done_body, done_lineno, "Done When", problems)

    framing = {}
    for heading in ("Prior Decisions", "Research", "Out of Scope", "Assumptions"):
        if heading in sections:
            lineno, body = sections[heading]
            framing[heading.lower().replace(" ", "_")] = lightspec.parse_bullets(
                body, lineno, heading, problems,
            )
    if problems.items:
        raise SpecFormatError("\n".join(
            f"{path}:{lineno}: {message}" for lineno, message in sorted(problems.items)
        ))

    return {
        "title": title.group(1),
        "slug": slug.group(1) if slug else None,
        "status": status.group(1),
        "path": str(path),
        "description": " ".join(text for _, text in lightspec.paragraphs(sections["Description"][1])),
        **framing,
        "stories": [stories[number] for number in sorted(stories)],
        "work_items": work_items,
        "cross_cutting_tasks": cross_cutting_tasks,
        "done_when": done_when,
    }


def build_plan(data: dict) -> list[dict]:
    if data["status"] != "Accepted":
        raise SpecFormatError("only Accepted specs can be filed")
    if data["cross_cutting_tasks"]:
        ids = ", ".join(task["id"] for task in data["cross_cutting_tasks"])
        raise SpecFormatError(
            f"legacy cross-cutting tasks need owning work items before filing: {ids}"
        )
    items = data["work_items"]
    if not items:
        raise SpecFormatError("legacy story-only spec needs the mapping in REFERENCE.md")

    path = Path(data["path"])
    root = lightspec.repo_root_or_none(path.resolve().parent)
    if root is not None:
        path = path.resolve().relative_to(root)
    directories = sorted({
        f"{Path(path).parts[0]}/"
        for item in items for path in item["paths"] if len(Path(path).parts) > 1
    })
    where = "\n\n".join(
        "\n".join(f"`{entry}`" for entry in group)
        for group in ([str(path)], directories) if group
    )
    framing = [
        ("Prior Decisions", "prior_decisions"), ("Research", "research"),
        ("Out of Scope", "out_of_scope"), ("Assumptions", "assumptions"),
    ]
    plan = [{
        "id": "epic", "title": data["title"], "type": "epic",
        "priority": min(item["priority"] for item in items),
        "goal": data["description"], "where": where,
        "approach": "\n\n".join(
            f"**{label}**\n" + "\n".join(f"- {line}" for line in data[key])
            for label, key in framing if key in data
        ),
        "done-when": data["done_when"],
    }]
    for item in items:
        source = f"Work item `{item['id']}` in `{path}`."
        if item["supports"]:
            source += " Supports " + ", ".join(f"`{ref}`" for ref in item["supports"]) + "."
        child = {
            "id": item["id"], "title": item["title"], "type": "task",
            "priority": item["priority"], "parent": "epic", "goal": item["goal"],
            "where": "\n".join(f"`{path}`" for path in dict.fromkeys(item["paths"])),
            "approach": f"{item['approach']}\n\n{source}",
            "done-when": item["done_when"],
        }
        if item["blocked_by"]:
            child["blocked-by"] = item["blocked_by"]
        plan.append(child)
    return plan


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("path", type=Path)
    parser.add_argument("--plan", action="store_true", help="emit a hew JSONL plan for work items")
    args = parser.parse_args()
    try:
        if args.plan:
            problems = lightspec.check_file(args.path)
            if problems.items:
                raise SpecFormatError("\n".join(
                    f"{args.path}:{lineno}: {message}"
                    for lineno, message in sorted(problems.items)
                ))
        data = parse(args.path.read_text(encoding="utf-8"), args.path)
        if args.plan:
            for entry in build_plan(data):
                print(json.dumps(entry))
        else:
            print(json.dumps(data, indent=2))
    except (OSError, SpecFormatError) as exc:
        print(f"extract_spec: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
