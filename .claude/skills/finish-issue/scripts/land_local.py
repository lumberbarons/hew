"""Squash an explicitly verified local delivery, then close it, with a recovery receipt.

Review and project checks are the caller's responsibility. The immutable head/base pair
binds that evidence to what lands. No push, branch deletion, force, or automatic reset.
"""

import argparse
import fcntl
import json
import os
import re
import subprocess
import sys
from pathlib import Path


class Failure(Exception):
    pass


def run(args, cwd):
    result = subprocess.run(args, cwd=cwd, capture_output=True, text=True, check=False)
    if result.returncode:
        raise Failure(f"{' '.join(args)}: {(result.stderr or result.stdout).strip()}")
    return result.stdout.strip()


def save(path, record):
    temporary = path.with_suffix(".tmp")
    with temporary.open("w") as stream:
        json.dump(record, stream, indent=2)
        stream.write("\n")
        stream.flush()
        os.fsync(stream.fileno())
    os.replace(temporary, path)


def require(condition, reason):
    if not condition:
        raise Failure(reason)


def land(args):
    cwd = Path(args.checkout).resolve()
    config = json.loads(run(["hew", "config", "--json"], cwd))
    require(config.get("backend") == "local", "this helper requires a local tracker")
    require(isinstance(config.get("id"), str) and config["id"], "tracker identity is unavailable")
    require(args.issue > 0, "issue must be positive")
    require(args.branch != args.base, "issue branch and default branch must differ")
    require(args.review.strip() and args.testing.strip(), "review and testing evidence are required")
    for sha in (args.head, args.base_head):
        require(re.fullmatch(r"[0-9a-f]{40}|[0-9a-f]{64}", sha), "use full commit IDs")
    for branch in (args.base, args.branch):
        run(["git", "check-ref-format", f"refs/heads/{branch}"], cwd)
    common = Path(run(["git", "rev-parse", "--git-common-dir"], cwd))
    common = (cwd / common).resolve()
    receipts = common / "finish-issue"
    receipts.mkdir(exist_ok=True)
    # Shared across worktrees and issues: two helpers may not write one checkout at once.
    with (receipts / "land.lock").open("a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            raise Failure("another local landing is in progress") from error
        return land_locked(args, cwd, receipts / f"{args.issue}.json", config)


def land_locked(args, cwd, path, config):
    def git(*argv):
        return run(["git", *argv], cwd)

    identity = {"issue": args.issue, "tracker_id": config.get("id"),
                "branch": args.branch, "base": args.base,
                "head": args.head, "base_head": args.base_head}
    record = None
    if path.exists():
        record = json.loads(path.read_text())
        require(isinstance(record, dict) and all(key in record for key in
                ("review", "testing", "title", "status")), f"invalid receipt; inspect {path}")
        require(all(record.get(key) == value for key, value in identity.items()),
                f"receipt targets a different delivery: {path}")
        require(record["status"] in ("prepared", "closure_pending", "finished")
                and (record["status"] == "prepared" or record.get("commit")),
                f"invalid receipt state; inspect {path}")
    require(git("symbolic-ref", "--quiet", "--short", "HEAD") == args.base,
            f"default branch {args.base} must be checked out here")
    current_base = git("rev-parse", f"refs/heads/{args.base}")
    tree = git("rev-parse", f"{args.head}^{{tree}}")
    marker = f"Hew-Delivery: {args.issue} {args.head} {args.base_head}"

    # A commit can exist even if the process died before recording its SHA. Only
    # accept the first child of the recorded base, with our exact tree and marker.
    if record and not record.get("commit") and current_base != args.base_head:
        commits = git("rev-list", "--reverse", "--ancestry-path",
                      f"{args.base_head}..refs/heads/{args.base}").splitlines()
        if commits:
            commit = commits[0]
            parents = git("show", "-s", "--format=%P", commit)
            body = git("show", "-s", "--format=%B", commit)
            if (parents == args.base_head and marker in body.splitlines()
                    and git("rev-parse", f"{commit}^{{tree}}") == tree):
                record["commit"] = commit
                save(path, record)

    if record and record.get("commit"):
        git("merge-base", "--is-ancestor", record["commit"], f"refs/heads/{args.base}")
        # The merge already happened. A changed/deleted issue branch or a later
        # default-branch commit is no reason to squash again or repeat review.
        if record.get("status") == "finished":
            issue = json.loads(run(["hew", "show", str(args.issue), "--json"], cwd))
            require(issue.get("state") == "closed",
                    "issue was reopened after completion; inspect it rather than closing again")
            return record
    else:
        require(current_base == args.base_head, "default branch changed; integrate and verify again")
        require(git("rev-parse", f"refs/heads/{args.branch}") == args.head,
                "issue head changed; verify again")
        try:
            git("merge-base", "--is-ancestor", args.base_head, args.head)
        except Failure as error:
            raise Failure("issue branch does not contain the current default branch; integrate and verify again") from error
        issue = json.loads(run(["hew", "show", str(args.issue), "--json"], cwd))
        require(issue.get("state") == "open", "issue is already closed; no merge receipt proves completion")
        if record is None:
            require(not git("status", "--porcelain"), "default checkout must be clean")
            require(tree != git("rev-parse", f"{args.base_head}^{{tree}}"),
                    "delivery has no changes; resolve its completion explicitly")
            record = {**identity, "review": args.review, "testing": args.testing,
                      "title": issue["title"], "status": "prepared"}
            save(path, record)
        if git("status", "--porcelain"):
            # Interrupted after squash but before commit. Never add unrelated files
            # or reset anything: only the exact expected index is recoverable.
            require(not git("diff", "--name-only")
                    and not git("ls-files", "--others", "--exclude-standard")
                    and git("write-tree") == tree,
                    "checkout/index differs from the recorded squash; preserve it and inspect the receipt")
        else:
            git("merge", "--squash", args.head)
        prefix = args.branch.split("/")[0]
        if prefix not in ("fix", "feat", "chore"):
            prefix = "chore"
        message = (f"{prefix}: {record['title']} (#{args.issue})\n\n"
                   f"Refs #{args.issue}\n{marker}\n\n"
                   f"Review: {record['review']}\nTesting: {record['testing']}\n")
        git("commit", "-m", message)
        commit = git("rev-parse", "HEAD")
        require(git("rev-parse", f"{commit}^{{tree}}") == tree
                and git("show", "-s", "--format=%P", commit) == args.base_head,
                "commit differs from the verified delivery; do not close, inspect it")
        record["commit"] = commit
        save(path, record)

    try:
        issue = json.loads(run(["hew", "show", str(args.issue), "--json"], cwd))
        require(issue.get("state") in ("open", "closed"), "tracker issue state is unknown")
        if issue.get("state") != "closed":
            run(["hew", "close", str(args.issue), "--completed", "--reason",
                 (f"Squash-merged {record['commit']} into {args.base}. "
                  f"Review: {record['review']}. Testing: {record['testing']}")], cwd)
            issue = json.loads(run(["hew", "show", str(args.issue), "--json"], cwd))
        require(issue.get("state") == "closed", "tracker did not confirm closure")
    except (Failure, ValueError) as error:
        record.update(status="closure_pending", reason=str(error))
        save(path, record)
        return record
    record.pop("reason", None)
    record["status"] = "finished"
    save(path, record)
    return record


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--checkout", required=True, help="clean checkout holding the default branch")
    parser.add_argument("--issue", required=True, type=int)
    parser.add_argument("--branch", required=True)
    parser.add_argument("--base", required=True)
    parser.add_argument("--head", required=True, help="full verified issue commit ID")
    parser.add_argument("--base-head", required=True, help="full default commit ID included in verification")
    parser.add_argument("--review", required=True, help="review evidence")
    parser.add_argument("--testing", required=True, help="commands and results on this head/base pair")
    args = parser.parse_args()
    try:
        result = land(args)
    except (Failure, OSError, ValueError, KeyError) as error:
        result = {"status": "failed", "issue": args.issue, "reason": str(error)}
    print(json.dumps(result, indent=2))
    return 0 if result["status"] == "finished" else 1


if __name__ == "__main__":
    sys.exit(main())
