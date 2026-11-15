Repair PR #${pr}. Issue #${n} is the change the PR carries; read it with `hew show ${n}` when its
intent is not clear from the diff. The PR is either in conflict with origin/${default} or has a
failing check; find out which.

This worktree is already on the PR's issue branch. Bring it to the PR head first:
1. `git fetch origin`
2. `git merge --ff-only origin/${branch}`
If the fast-forward fails, stop and write status "error" with the reason; do not reset, rebase,
or force anything.

Then:
3. If the PR conflicts: `git merge origin/${default}`. For each conflicted file, reconcile both
   sides' intent: the PR side is issue #${n}'s change, the incoming side is already-merged work.
   Combine both when they are compatible; when they contradict each other in behaviour — not just
   in text — stop and write status "blocked" with the clashing files and why. Never drop one side
   wholesale, never guess.
4. If a check is failing: read it with `gh pr checks ${pr}` and the failing run's log. Fix the
   cause when this PR caused it. When it did not — a flaky test, an outage, a failure that is
   already on origin/${default} — stop and write status "blocked" and say so; do not paper over it,
   and never skip, weaken, or delete a test or a check.
5. Run every check the project gates on — tests, linters, type checks, builds — as step 6 of
   ${work_issue_dir}/SKILL.md describes (its REFERENCE.md table is only the floor), over the union
   of files you touched. A failing gate is "blocked"; do not push.
6. Commit and `git push origin HEAD:${branch}`. Never --force, never rebase, never push to a ref
   other than this PR's ${branch}.
7. Write '.work-epic/wc-${pr}.json' as JSON:
   { "status": "resolved" | "blocked" | "error", "head": "<sha after the push>",
     "reason": "<why, when not resolved>", "gate": "<command and result>" }
   Do not commit it.
8. Send the worker_done message the orchestration preamble specifies, with --outcome succeeded
   only if status is "resolved", otherwise failed, and --report-path '.work-epic/wc-${pr}.json'.
   Ask nothing and do not interact with the user.
