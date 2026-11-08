#!/usr/bin/env python3
"""Choose report/fix from explicit intent and locally verifiable Git state.

No natural-language parsing, writes, fetches or network calls. The skill supplies
the scope; an omitted/uncertain scope always reports. Exit 2 is invalid input,
never permission to fix. Git discovery failures produce a report decision.
"""

import argparse
import json
import re
import subprocess


def git(*args):
    result = subprocess.run(
        ["git", *args], capture_output=True, text=True, check=False
    )
    if result.returncode:
        raise ValueError("cannot verify Git state")
    return result.stdout.strip()


def default_base():
    """Prefer recorded origin HEAD; never guess a remote's default branch."""
    remotes = git("remote").splitlines()
    if remotes:
        if "origin" not in remotes:
            raise ValueError("default branch is unknown: no origin remote")
        ref = git("symbolic-ref", "refs/remotes/origin/HEAD")
        prefix = "refs/remotes/origin/"
        if not ref.startswith(prefix):
            raise ValueError("default branch is unknown")
        git("rev-parse", "--verify", ref + "^{commit}")
        return ref[len(prefix):], ref

    # Local-only repos have no remote HEAD. One conventional default is
    # sufficient evidence; two candidates are ambiguous.
    candidates = []
    for name in ("main", "master"):
        result = subprocess.run(
            ["git", "show-ref", "--verify", "--quiet", "refs/heads/" + name],
            capture_output=True, check=False,
        )
        if result.returncode == 0:
            candidates.append(name)
        elif result.returncode != 1:
            raise ValueError("cannot verify default branch")
    if len(candidates) != 1:
        raise ValueError("default branch is unknown or ambiguous")
    name = candidates[0]
    return name, "refs/heads/" + name


def decide(scope, fix=False, report_only=False):
    result = {"mode": "report", "automatic": False, "scope": scope}
    if report_only:
        return dict(result, reason="explicit report-only request")
    if fix:
        return dict(result, mode="fix", reason="explicit fix request")
    if scope != "branch":
        return dict(result, reason="only a current-branch change review can auto-fix")

    try:
        root = git("rev-parse", "--show-toplevel")
        branch = git("symbolic-ref", "--quiet", "--short", "HEAD")
        default, base = default_base()
        result.update(
            root=root, branch=branch, default_branch=default, base=base,
            head=git("rev-parse", "HEAD"),
            base_commit=git("rev-parse", base + "^{commit}"),
        )
        if branch == default:
            return dict(result, reason="on the default branch")
        if not re.fullmatch(r"(?:feat|fix|chore)/.+", branch):
            return dict(result, reason="branch is not clearly a development branch")
        if git("status", "--porcelain", "--untracked-files=all"):
            return dict(result, reason="working tree has staged, unstaged or untracked changes")
        bases = git("merge-base", "--all", base, "HEAD").splitlines()
        if len(bases) != 1:
            return dict(result, reason="branch comparison has no unique merge base")
        changed = git("diff", "--name-only", "--diff-filter=AM", base + "...HEAD", "--")
        if not changed:
            return dict(result, reason="no added or modified files in the branch diff")
        return dict(result, mode="fix", automatic=True, reason="clean development-branch change review")
    except (OSError, ValueError) as error:
        return dict(result, reason=str(error))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--scope", choices=("branch", "pr", "audit", "unknown"), default="unknown")
    intent = parser.add_mutually_exclusive_group()
    intent.add_argument("--fix", action="store_true")
    intent.add_argument("--report-only", action="store_true")
    args = parser.parse_args()
    print(json.dumps(decide(args.scope, args.fix, args.report_only)))


if __name__ == "__main__":
    main()
