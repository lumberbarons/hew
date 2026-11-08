#!/usr/bin/env python3
"""Find the already-filed issues each plan line might duplicate.

  dedup_candidates.py <plan.jsonl> <issues.ndjson> [--reviewed-issue <n>]
                      [--out FILE]

`issues.ndjson` is `hew list --json --bodies --state all`. Per plan line this
prints one JSON object:

  line             1-based position in the plan
  key              the plan line's review-key
  candidates       issues that may be this finding: the stored `review-key:`
                   line equals the key exactly (a substring hit, such as an
                   older key sitting inside a longer one, is not a candidate)
                   and, when --reviewed-issue is given, the issue was raised
                   against that same review target. Each carries number,
                   state, title and same_paths (the affected paths, ignoring
                   line numbers, are the same set as the plan line's)
  other_generation issue numbers with the same key raised against a different
                   review target (or none) — findings about earlier code
                   state, never a reason to suppress this one

The key finds candidates; it does not decide identity. Whether a candidate
describes the same problem is the agent's call, made from the body. Without
--reviewed-issue the review target is ignored: a review of product code is
already tracked by any issue that describes the finding.

Never queries hew. Exit: 0 ok · 1 runtime error · 2 usage error.
"""

import json
import re
import sys

USAGE = ("usage: dedup_candidates.py <plan.jsonl> <issues.ndjson> "
         "[--reviewed-issue <n>] [--out FILE]")

KEY_LINE = re.compile(r"^review-key: (\S+)\s*$")
REVIEW_OF_LINE = re.compile(r"^review-of: #(\d+)")
CODE_SPAN = re.compile(r"`([^`]+)`")
LINE_SUFFIX = re.compile(r":\d+(?:-\d+)?$")


def fail(msg, code=1):
    print(f"dedup_candidates: {msg}", file=sys.stderr)
    sys.exit(code)


def key_variants(key):
    """The stored spellings of one key: `skills/` is the legacy `portability/`."""
    skill, _, rest = key.partition("/")
    if skill in ("portability", "skills"):
        return {f"portability/{rest}", f"skills/{rest}"}
    return {key}


def identity_lines(where):
    """(review-key, review-of issue number) from a Where section or body."""
    key = target = None
    for row in where.splitlines():
        m = KEY_LINE.match(row)
        if m and key is None:
            key = m.group(1)
        m = REVIEW_OF_LINE.match(row)
        if m and target is None:
            target = int(m.group(1))
    return key, target


def where_section(body):
    """The text under `### Where`, or the whole string for a plan's `where`."""
    m = re.search(r"^### Where\s*\n(.*?)(?=^### |\Z)", body, re.S | re.M)
    return m.group(1) if m else body


def affected_paths(where):
    """Backticked paths of the first paragraph, without line numbers."""
    first = where.strip().split("\n\n", 1)[0]
    return {LINE_SUFFIX.sub("", span) for span in CODE_SPAN.findall(first)}


def candidates_for(plan_line, issues, reviewed_issue):
    where = plan_line.get("where", "")
    key, _ = identity_lines(where)
    if key is None:
        return None
    variants = key_variants(key)
    paths = affected_paths(where)
    found, other = [], []
    for issue in issues:
        section = where_section(issue.get("body") or "")
        stored, target = identity_lines(section)
        if stored not in variants:
            continue
        if reviewed_issue is not None and target != reviewed_issue:
            other.append(issue["number"])
            continue
        found.append({
            "number": issue["number"],
            "state": issue.get("state"),
            "title": issue.get("title"),
            "same_paths": affected_paths(section) == paths,
        })
    return {"key": key, "candidates": found, "other_generation": other}


def parse_args(args):
    opts = {"reviewed_issue": None, "out": None}
    positional = []
    it = iter(args)
    for a in it:
        if a in ("--reviewed-issue", "--out"):
            v = next(it, None)
            if v is None:
                fail(f"{a} needs a value\n{USAGE}", 2)
            if a == "--out":
                opts["out"] = v
            elif v.isdigit():
                opts["reviewed_issue"] = int(v)
            else:
                fail(f"{a} needs an issue number\n{USAGE}", 2)
        elif a.startswith("--"):
            fail(f"unknown flag {a}\n{USAGE}", 2)
        else:
            positional.append(a)
    if len(positional) != 2:
        fail(USAGE, 2)
    return positional, opts


def read_jsonl(path, what):
    try:
        with open(path, encoding="utf-8") as f:
            return [json.loads(row) for row in f if row.strip()]
    except OSError as e:
        fail(f"cannot read {what}: {e.strerror}")
    except json.JSONDecodeError as e:
        fail(f"{what} is not JSONL: {e}")


def main():
    (plan_path, issues_path), opts = parse_args(sys.argv[1:])
    plan = read_jsonl(plan_path, "plan")
    issues = read_jsonl(issues_path, "issues")
    out = []
    for n, plan_line in enumerate(plan, 1):
        result = candidates_for(plan_line, issues, opts["reviewed_issue"])
        if result is None:
            fail(f"plan line {n} has no review-key")
        out.append(json.dumps(dict(line=n, **result)))
    text = "\n".join(out) + ("\n" if out else "")
    if opts["out"]:
        with open(opts["out"], "w", encoding="utf-8") as f:
            f.write(text)
    else:
        sys.stdout.write(text)


if __name__ == "__main__":
    main()
