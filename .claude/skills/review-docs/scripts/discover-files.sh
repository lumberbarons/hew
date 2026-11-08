#!/usr/bin/env bash
# Enumerate files for a critique review.
#
# Usage:
#   discover-files.sh           Scope to current branch (added/modified files vs default branch)
#   discover-files.sh <path>    List tracked + untracked-not-ignored files under <path>
#
# Output: file paths, one per line, on stdout.
#
# Exit codes:
#   0  success (output may be empty if branch has no diff vs default)
#   1  path argument given but does not exist
#   2  no path given and not in a git repo, or default branch indeterminate
#   3  no path given and currently on the default branch
#   4  no path given and HEAD is detached

set -euo pipefail

if [[ $# -gt 0 && -n "${1:-}" ]]; then
  path="$1"
  if [[ ! -e "$path" ]]; then
    echo "Path not found: $path" >&2
    exit 1
  fi
  if [[ -f "$path" ]]; then
    echo "$path"
    exit 0
  fi
  if git -C "$path" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    # The caller's cwd may be outside the repository that holds $path (an absolute
    # path handed in from elsewhere), where a pathspec lookup fails. Fall back to
    # listing from inside $path and prefixing the result, so callers see the same
    # shape either way.
    if ! git ls-files --cached --others --exclude-standard -- "$path" 2>/dev/null; then
      git -C "$path" ls-files --cached --others --exclude-standard |
        while IFS= read -r file; do printf '%s/%s\n' "${path%/}" "$file"; done
    fi
  else
    find "$path" -type f
  fi
  exit 0
fi

if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  echo "Not in a git repository — provide a path to review." >&2
  exit 2
fi

default_branch=""
if git symbolic-ref refs/remotes/origin/HEAD >/dev/null 2>&1; then
  default_branch=$(git symbolic-ref refs/remotes/origin/HEAD | sed 's@^refs/remotes/origin/@@')
elif git show-ref --verify --quiet refs/heads/main; then
  default_branch=main
elif git show-ref --verify --quiet refs/heads/master; then
  default_branch=master
fi

if [[ -z "$default_branch" ]]; then
  echo "Could not detect default branch — provide a path to review." >&2
  exit 2
fi

current_branch=$(git symbolic-ref --short -q HEAD || true)
if [[ -z "$current_branch" ]]; then
  echo "Detached HEAD — provide a path to review." >&2
  exit 4
fi

if [[ "$current_branch" == "$default_branch" ]]; then
  echo "On default branch ($default_branch) — provide a path to review." >&2
  exit 3
fi

diff_target="$default_branch"
if git show-ref --verify --quiet "refs/remotes/origin/$default_branch"; then
  diff_target="origin/$default_branch"
fi

git diff --name-only --diff-filter=AM "$diff_target...HEAD"
exit 0
