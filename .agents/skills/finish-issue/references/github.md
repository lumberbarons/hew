# GitHub delivery

Use the repository and PR linked to the target issue. Do not assume the issue number is its PR
number. Inspect the PR's state, head repository, head SHA, base branch, closing issue references,
draft flag, review/merge state, and checks. Stop for an unexpected base or a PR that closes
additional issues outside the requested scope. A PR closed without merging is blocked, not done.

If the PR is already merged, skip repairs and merging: read its merge commit and confirm the
issue is closed. A missing closure is `closure_pending`; do not call `hew close` to conceal a
missing/wrong closing reference. Report the mismatch for the maintainer.

For an open PR:

1. Fetch the PR's base and head from the correct repository. Use the PR's actual head checkout
   and preserve unrelated changes. Fork heads need their actual remote; do not assume `origin`
   can push them. If it cannot be updated with available access, report the blocker.
2. Review, integrate the current base, repair, and verify as the entrypoint describes. Push any
   resulting commits to the PR head branch. Existing review evidence may be a human review,
   a completed agent review report, or work-epic's review marker; read what it records rather
   than treating a marker as a waiver for unresolved findings.
3. Mark a draft ready once review/repairs and local checks are complete (`gh pr ready <pr>`).
   Some CI and approval rules only run when ready. Wait for CI on the resulting full head SHA.
   Inspect both all reported checks and required-check state; no checks reported is unknown,
   not green. Do not mistake an empty required-check list for a pass. If the repository truly
   has no CI, establish that from its configuration and use its local project gates, explicitly
   recording that fact. Skipped jobs are acceptable only where the repository intentionally
   makes them inapplicable to this change; cancellations and missing required results block.
4. Refresh the PR and base immediately before merging. Confirm the head is the verified one,
   the branch contains the current base, checks have passed, review requirements are satisfied,
   and mergeability is known. If an update is needed, repeat integration and verification.
   Do not use `--admin` or relax protection. Never merge on a locally green result while CI is
   red, even if the failing check is also red on the default branch.
5. Request the squash with the exact verified head guard:

   ```bash
   gh pr merge <pr> --squash --match-head-commit <full-verified-head>
   ```

   Add the consistent repository selector where needed. A merge queue may enqueue or schedule
   the PR rather than merge it immediately. Poll observable state within the waiting budget;
   report `pending` if it has not merged. A successful command alone is not completion.
6. Read the merged PR's merge commit and read the issue through hew. Only an observed merged PR
   and closed issue produce `finished`. Allow closure propagation within the waiting budget,
   then report `closure_pending` with the merge commit if still open. GitHub closure comes from
   the PR's closing reference, not an extra `hew close`.

Keep the working branches unless cleanup is part of the request or the repository convention.
When updating a local default checkout afterwards, fetch and fast-forward it only if clean and
already on the default branch. A cleanup/sync problem does not undo a successful merge: report
it separately rather than attempting another merge.
