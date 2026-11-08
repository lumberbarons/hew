"""Read-only repository probes shared by resolve_ready.py and pr_state.py.

Both planners need the same facts — where the primary checkout is, which
branch is the default, which open PRs and pushed branches map to which issue —
and must agree on them, so they read them through one module rather than two
copies that drift.
"""

import json
import os
import re
import subprocess
from urllib.parse import quote

# work-issue's naming rule: <prefix>/<n>-<slug>
BRANCH_ISSUE_RE = re.compile(r"^(?:fix|feat|chore)/(\d+)(?:-|$)")

# gh pr list defaults to 30; a busy repo would silently drop epic PRs past it
PR_LIMIT = "1000"


class CommandError(Exception):
    """A probe command exited non-zero; the message is its stderr."""


def run(cmd):
    p = subprocess.run(cmd, capture_output=True, text=True)
    if p.returncode != 0:
        raise CommandError(p.stderr.strip() or f"{' '.join(cmd)} exited {p.returncode}")
    return p.stdout


def parse_scope(tokens):
    """Issue numbers from positional args: `12`, `#12`, and `12,13` all work.

    Returns None when any token is not a number, so each script can print
    its own usage line. Order is kept and duplicates dropped.
    """
    numbers = []
    for token in tokens:
        for part in token.split(","):
            part = part.strip().lstrip("#")
            if not part.isdigit():
                return None
            if int(part) not in numbers:
                numbers.append(int(part))
    return numbers or None


def scope_issues(numbers, repo=None):
    """The hew rows a run covers: each issue itself, an epic as its children.

    `hew show` and `hew list` share one row schema, so the rows are
    interchangeable. An issue named twice (directly and through its epic)
    appears once, at its first position.
    """
    suffix = ["--repo", repo] if repo else []
    rows, seen = [], set()
    for n in numbers:
        shown = json.loads(run(["hew", "show", str(n), "--json", *suffix]))
        if shown.get("epic"):
            found = [
                json.loads(line)
                for line in run(
                    ["hew", "list", "--epic", str(n), "--json", "--bodies",
                     "--state", "all", *suffix]
                ).splitlines()
                if line.strip()
            ]
        else:
            found = [shown]
        for row in found:
            if row["number"] not in seen:
                seen.add(row["number"])
                rows.append(row)
    return rows


def branch_issue(name):
    """The issue number a conventional branch name carries, or None."""
    m = BRANCH_ISSUE_RE.match(name or "")
    return int(m.group(1)) if m else None


def main_checkout():
    """Absolute path of the repository's primary worktree, or None.

    `git worktree list --porcelain` lists the main worktree first; that is
    the checkout every other worktree hangs off, so it is where the pump
    fetches and fast-forwards the default branch. Orca child worktrees are
    based on origin/<default> explicitly, not on this checkout's HEAD.
    """
    p = subprocess.run(
        ["git", "worktree", "list", "--porcelain"], capture_output=True, text=True
    )
    if p.returncode != 0:
        return None
    for line in p.stdout.splitlines():
        if line.startswith("worktree "):
            return line[len("worktree ") :] or None
    return None


def default_branch(repo=None):
    """The default branch name, or None if undeterminable.

    origin/HEAD first (local, free); it is unset on some clones, so fall back
    to asking GitHub.
    """
    if repo is None:
        p = subprocess.run(
            ["git", "rev-parse", "--abbrev-ref", "origin/HEAD"],
            capture_output=True,
            text=True,
        )
        name = p.stdout.strip()
        if p.returncode == 0 and name.startswith("origin/") and name != "origin/HEAD":
            return name[len("origin/") :]
    p = subprocess.run(
        ["gh", "repo", "view", *([repo] if repo else []),
         "--json", "defaultBranchRef", "--jq", ".defaultBranchRef.name"],
        capture_output=True,
        text=True,
    )
    return (p.stdout.strip() or None) if p.returncode == 0 else None


def local_tracker(repo=None):
    """True when hew keeps this repository's issues in a local `.hew`, not on GitHub.

    hew's own rule: `--repo` always means GitHub; otherwise the main checkout's
    `.hew/config.json` names the backend. Read here rather than asked of hew
    because every hew command that reports it also writes. An unreadable or
    absent config means GitHub, as it does for hew.
    """
    main = None if repo else main_checkout()
    if not main:
        return False
    try:
        with open(os.path.join(main, ".hew", "config.json")) as f:
            return json.load(f).get("backend") == "local"
    except (OSError, ValueError, AttributeError):
        return False


def local_default_branch():
    """The local default branch: origin/HEAD when set, else main, else master, else None.

    The order review-code's discover-files.sh uses, so the pump and the reviewer
    agree on what a branch is compared against.
    """
    p = subprocess.run(["git", "symbolic-ref", "--short", "refs/remotes/origin/HEAD"],
                       capture_output=True, text=True, check=False)
    if p.returncode == 0 and p.stdout.strip().startswith("origin/"):
        return p.stdout.strip()[len("origin/"):]
    for name in ("main", "master"):
        p = subprocess.run(["git", "show-ref", "--verify", "--quiet", f"refs/heads/{name}"], check=False)
        if p.returncode == 0:
            return name
    return None


def unmerged_issue_branches(default):
    """Local branches named for an issue (`<prefix>/<n>-<slug>`) that `default` does not contain.

    Returns {issue number: branch name}; the first branch per issue wins. A branch
    that was squash-merged is not contained, so the pump deletes what it merges.
    """
    out = run(["git", "branch", "--no-merged", default, "--format=%(refname:short)"])
    found = {}
    for name in out.splitlines():
        n = branch_issue(name.strip())
        if n is not None:
            found.setdefault(n, name.strip())
    return found


def branch_head(branch):
    """The commit a local branch points at, or None when there is no such branch."""
    p = subprocess.run(["git", "rev-parse", "--verify", "--quiet", f"refs/heads/{branch}"],
                       capture_output=True, text=True, check=False)
    return (p.stdout.strip() or None) if p.returncode == 0 else None


def commits_behind(default, head):
    """How many commits on `default` the commit `head` lacks, or None if unknown."""
    p = subprocess.run(["git", "rev-list", "--count", f"{head}..{default}"],
                       capture_output=True, text=True, check=False)
    try:
        return int(p.stdout.strip()) if p.returncode == 0 else None
    except ValueError:
        return None


def conflicts_with(default, head):
    """Whether merging `head` into `default` would conflict; None when git cannot say.

    `merge-tree --write-tree` computes the merge without touching any worktree or
    ref. Exit 0 is clean. Exit 1 is both a conflict and a bad ref, so the two are told
    apart by stdout: a conflict leads with the result tree's object id, an error prints
    only to stderr. Anything else — an old git without --write-tree — is unknown, which
    no caller may read as clean.
    """
    p = subprocess.run(["git", "merge-tree", "--write-tree", "--no-messages", default, head],
                       capture_output=True, text=True, check=False)
    if p.returncode == 0:
        return False
    if p.returncode == 1 and re.match(r"^[0-9a-f]{40,64}$", p.stdout.split("\n", 1)[0]):
        return True
    return None


def open_prs(fields, repo=None):
    """Every open PR as gh JSON with the given comma-separated fields."""
    return json.loads(
        run(
            ["gh", "pr", "list", "--state", "open", "--limit", PR_LIMIT,
             "--json", fields, *(["-R", repo] if repo else [])]
        )
    )


def remote_branches(repo=None):
    """Names of every branch on the GitHub remote."""
    out = run(
        ["gh", "api", "--paginate",
         f"repos/{repo or '{owner}/{repo}'}/branches?per_page=100",
         "--jq", ".[].name"]
    )
    return [line for line in out.splitlines() if line.strip()]


DOC_SUFFIXES = (".md", ".mdx", ".rst", ".txt", ".adoc")


def touches_code(paths):
    """Whether a diff changes anything but prose, which is when its tests are worth a review.

    A diff that cannot be listed counts as code: not knowing is never a reason to skip.
    """
    paths = [p for p in paths if p.strip()]
    return not paths or any(not p.lower().endswith(DOC_SUFFIXES) for p in paths)


def behind_by(base, head, repo=None):
    """How many commits on `base` the commit `head` lacks, or None if unknown.

    GitHub's mergeStateStatus says BEHIND only when branch protection requires
    branches to be up to date; without that rule a stale PR reads CLEAN and
    would merge on CI that never saw what landed since. The compare API
    answers regardless of protection.
    """
    if not base or not head:
        return None
    p = subprocess.run(
        ["gh", "api",
         f"repos/{repo or '{owner}/{repo}'}/compare/{quote(base)}...{head}",
         "--jq", ".behind_by"],
        capture_output=True,
        text=True,
    )
    if p.returncode != 0:
        return None
    try:
        return int(p.stdout.strip())
    except ValueError:
        return None
