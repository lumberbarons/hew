"""File a reviewer's findings as issues, close the fixed ones, and word the round comment.

The reviewer's outcome file is review-code's findings file plus `fixed`/`fixedIn`
per finding. Filing runs raise-issues' converter and dedup helper, then
`hew apply`; every finding the reviewer fixed is closed `--completed` with its
commit, so the tracker keeps the paper trail and only an unfixed finding stays
open. Findings are filed with no parent: they are never part of the scope.

Dedup is deterministic here. dedup_candidates.py only finds leads; the skill's
agent flow reads each body. A driver has no reader, so a plan line is dropped
only when a candidate has the same review key, was raised against the same
review target, and covers the same paths (`same_paths`) — anything less is filed.
"""

import json
import os
import re

from pr_state import REVIEW_MARKER

FIXED_IN_RE = re.compile(r"(?m)^fixed-in: ([0-9a-f]+)")
BLOCKING_PRIORITIES = ("P0", "P1")


class FilingError(Exception):
    """Filing failed. No round comment is posted over it."""


def read_outcome(path):
    """The reviewer's findings file, or None when it is missing or unreadable."""
    try:
        with open(path) as f:
            data = json.load(f)
    except (OSError, ValueError):
        return None
    return data if isinstance(data, dict) else None


def unfixed_blocker(findings):
    """The first finding at P1 or above the reviewer did not fix, or None."""
    for f in findings:
        if f.get("priority") in BLOCKING_PRIORITIES and not f.get("fixed"):
            return f
    return None


class Filer:
    def __init__(self, sh, *, cwd, raise_dir, outdir, repo=None):
        self.sh, self.cwd, self.outdir = sh, cwd, outdir
        self.raise_dir = raise_dir
        self.hew_suffix = ["--repo", repo] if repo else []

    def _hew(self, *args, check=True):
        proc = self.sh(["hew", *args, *self.hew_suffix], cwd=self.cwd)
        if check and proc.rc != 0:
            raise FilingError(f"hew {args[0]} failed: {proc.err.strip()[:200]}")
        return proc

    def _script(self, name, *args):
        return self.sh(["uv", "run", "--no-project",
                        os.path.join(self.raise_dir, "scripts", name), *args], cwd=self.cwd)

    def _issues(self):
        """Every issue, open and closed, as hew rows."""
        out = self._hew("list", "--json", "--bodies", "--state", "all").out
        return [json.loads(line) for line in out.splitlines() if line.strip()]

    def file(self, *, pr, issue, outcome_path):
        """File, close, and return one record per finding for the round comment.

        `pr` is None on a local tracker, where there is no PR to name in the marker.
        """
        data = read_outcome(outcome_path)
        if data is None or data.get("status") != "reviewed":
            raise FilingError("findings file is missing or not `reviewed`")
        records = [dict(f, number=None) for f in data.get("findings") or []]
        marker = f"review-of: #{issue}" + (f" (PR #{pr})" if pr is not None else "")
        # without a PR suffix the marker for #21 is also the start of the one for #210
        marker_re = re.compile(re.escape(marker) + (r"\b" if pr is None else ""))
        key = pr if pr is not None else issue  # a local delivery is named for its issue
        plan = os.path.join(self.outdir, f"wr-{key}.plan.jsonl")

        converted = self._script(
            "findings_to_plan.py", outcome_path, "--reviewed-issue", str(issue),
            *(["--reviewed-pr", str(pr)] if pr is not None else []),
            "--at-or-above", "P2", "--out", plan)
        if converted.rc != 0:
            raise FilingError(f"findings_to_plan exited {converted.rc}: "
                              f"{converted.err.strip()[:200]}")
        with open(plan) as f:
            lines = [line for line in f.read().splitlines() if line.strip()]

        if lines:
            known = self._issues()
            with open(os.path.join(self.outdir, f"wr-{key}.issues.ndjson"), "w") as f:
                f.write("".join(json.dumps(k) + "\n" for k in known))
            dedup = self._script(
                "dedup_candidates.py", plan, os.path.join(self.outdir, f"wr-{key}.issues.ndjson"),
                "--reviewed-issue", str(issue))
            if dedup.rc != 0:
                raise FilingError(f"dedup_candidates exited {dedup.rc}")
            duplicates = {}
            for row in (json.loads(l) for l in dedup.out.splitlines() if l.strip()):
                same = [c for c in row.get("candidates") or [] if c.get("same_paths")]
                if same:
                    duplicates[row["line"]] = same[0]["number"]
            titles = [json.loads(l).get("title") for l in lines]
            kept = [l for i, l in enumerate(lines, 1) if i not in duplicates]
            for i, number in duplicates.items():
                for rec in records:
                    if rec.get("title") == titles[i - 1] and rec["number"] is None:
                        rec["number"] = number
            with open(plan, "w") as f:
                f.write("".join(l + "\n" for l in kept))
            if kept:
                self._hew("apply", plan, "--dry-run")
                self._hew("apply", plan)

        # what hew now holds for this review: number by title, and the fixed ones to close
        for row in self._issues():
            body = row.get("body") or ""
            if not marker_re.search(body):
                continue
            for rec in records:
                if rec.get("title") == row.get("title") and rec["number"] is None:
                    rec["number"] = row["number"]
            fixed = FIXED_IN_RE.search(body)
            if fixed and row.get("state") == "open":
                self._hew("close", str(row["number"]), "--completed", "--reason",
                          f"Fixed in {fixed.group(1)}"
                          + (f" on PR #{pr}" if pr is not None else "") + " before merge")
        return records


def _sentence(text):
    text = text.strip()
    return text if text.endswith((".", "!", "?")) else text + "."


def round_comment(records, gate=None):
    """The one review-round comment per PR. Its first line is what pr_state reads."""
    out = [f"{REVIEW_MARKER}:", ""]
    if not records:
        out.append("Clean review: no findings.")
    for f in records:
        where = (f.get("locations") or f.get("files") or ["?"])[0]
        number = f.get("number")
        filed = (f"Filed as #{number}" + (" and closed" if f.get("fixed") else "")) if number else "Not filed"
        if f.get("fixed"):
            status = f"Fixed in {f.get('fixedIn', '?')}. {filed}."
        elif f.get("priority") in ("P0", "P1", "P2"):
            status = f"Not fixed: {(f.get('unfixedReason') or 'no reason given').strip().rstrip('.')}. {filed}."
        else:
            status = f"{_sentence('Fix: ' + str(f.get('fix', '?')))} {filed}."
        why = _sentence((f.get("explanation") or "").strip().split("\n")[0][:240]) if f.get("explanation") else ""
        out += [f"### {f.get('priority', '?')} — {f.get('title', 'finding')}",
                f"`{where}` — {why} {status}".replace("  ", " "), ""]
    if gate:
        out.append(f"gate: {gate}")
    return "\n".join(out).rstrip() + "\n"
