# Local delivery

The hew backend is `local` even if the repository has a GitHub remote. Do not push or open a PR.
Find the local default branch from repository instructions/configuration; use `main` or `master`
only when unambiguous. An unborn default branch or ambiguous issue branches need a decision.

Inspect `git worktree list --porcelain`. If the default branch is already checked out, use that
checkout for landing. Otherwise switch a clean suitable checkout to it. Do not displace another
worktree's branch, stash user changes, or move a ref behind an active checkout. The issue's
branch remains the place to review, integrate the default branch, and run the project gates.

Once the branch contains the current default and its review/checks are complete, record both
full commit IDs. Use the helper **from this skill's own directory**, not a marketplace or
another installed skill's path:

```bash
python3 "<this skill's directory>/scripts/land_local.py" \
  --checkout "<checkout holding the default branch>" \
  --issue <n> --branch "<issue branch>" --base "<default branch>" \
  --head "<full verified issue head>" --base-head "<full verified base head>" \
  --review "<review evidence>" --testing "<commands and results>"
```

The helper uses Python 3 on macOS/Linux, git, and the installed hew. It verifies the backend,
clean default checkout, exact branch heads, and that the issue branch contains the verified
base. It squashes that exact head, commits with review/testing evidence, then closes the issue
with `hew close <n> --completed --reason ...` naming the commit. It preserves commit hooks and
signing configuration. It does not decide whether a review or test result is sufficient: the
calling skill supplies that judgement before invoking it. Do not invoke it under `--dry-run`.

## Interrupted landing

Receipts live at `<git-common-dir>/finish-issue/<n>.json`, outside the tracked tree and shared
across worktrees. Preserve that directory until the work is complete. A per-repository advisory
lock serializes helper runs; it does not lock out unrelated git commands, so leave the landing
checkout alone while the helper runs.

Before retrying, inspect the receipt and git/hew state. Repeat the original arguments (including
the original head/base IDs), rather than substituting today's default head:

- `finished`: the receipt names a commit still contained in the default branch; no new squash
  or close is needed.
- `closure_pending`: retry closure of the existing commit. The helper never squashes again.
- `prepared`: the helper can recover an exact staged squash, or recognize its committed squash
  from its recorded base, tree, and `Hew-Delivery` trailer after interruption. Unexpected index
  contents, a moved base without that commit, or a different delivery stop for inspection.

If an unrelated edit or a failing commit hook leaves the checkout changed, preserve it and
report the failure. Do not delete a receipt, reset the checkout, or start a second squash to get
past it. A merged commit with an open issue is `closure_pending`, never `finished`. The helper's
JSON is the local merge/closure evidence; include it in the skill's outcome rather than treating
its exit status alone as proof.
