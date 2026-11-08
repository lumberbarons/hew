Bring the branch `${branch}` up to date with `${default}` and prove it passes the project's checks.
Issue #${n} is the change the branch carries; read it with `hew show ${n}` when its intent is not
clear from the diff. Why you were asked: ${why}. This repository tracks issues locally: there is
no remote and no pull request, so nothing is fetched or pushed, and `${default}` is the local
branch.

This worktree is where the work was done. Confirm that first:
1. `git branch --show-current` must print `${branch}` and `git status --porcelain` must be empty.
   If either is not so, stop and write status "error" with the reason; do not switch, reset, or
   rebase anything.

Then:
2. Merge the default branch in with `git merge ${default}` (nothing to do when
   `git merge-base --is-ancestor ${default} HEAD` already succeeds). For each conflicted file,
   reconcile both sides' intent: the branch side is issue #${n}'s change, the incoming side is
   already-merged work. Combine both when they are compatible; when they contradict each other in
   behaviour — not just in text — stop and write status "blocked" with the clashing files and why.
   Never drop one side wholesale, never guess.
3. Run every check the project gates on — tests, linters, type checks, builds — as step 6 of
   ${work_issue_dir}/SKILL.md describes (its REFERENCE.md table is only the floor), on the merged
   result. This is the only gate that will see it: nothing else runs these checks before the
   merge. Fix the cause when this branch caused a failure. When it did not — a flaky test, a
   failure already on ${default} — stop and write status "blocked" and say so; do not paper over
   it, and never skip, weaken, or delete a test or a check.
4. Commit what you changed. Never push, never rebase, never force anything.
5. Write '.work-epic/wc-${n}.json' as JSON:
   { "status": "resolved" | "blocked" | "error", "head": "<`git rev-parse HEAD`>",
     "reason": "<why, when not resolved>", "gate": "<command and result>" }
   "resolved" means the checks passed on exactly that head. Do not commit the file.
6. Send the worker_done message the orchestration preamble specifies, with --outcome succeeded
   only if status is "resolved", otherwise failed, and --report-path '.work-epic/wc-${n}.json'.
   Ask nothing and do not interact with the user.
