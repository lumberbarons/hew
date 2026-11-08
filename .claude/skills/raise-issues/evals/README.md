# raise-issues evals

One eval, on the judgement the scripts cannot make. `dedup_candidates.py` narrows a plan line to
the issues that share its exact key and review target; whether a candidate describes the same
finding is the agent's call, and a wrong call either files a duplicate or silently drops a real
finding.

`fixtures/dedup-judgement/` is a tracker snapshot (`issues.ndjson`, `closures.txt`) and two plans.
Plan 1 is a review of the PR that fixes #118: the finding about the fix shares #118's key and must
still be filed, a re-run of the same review must be skipped, and a finding with the same key but a
different file and problem must be filed. Plan 2 is a review of product code: an open duplicate
that carries someone else's `review-of` is skipped, a closed-as-completed match is filed as a
regression, a closed-as-not-planned match is suppressed, and a longer key that merely contains the
shorter one is not a match.

The tracker is a file so the run needs no `hew` and files nothing. Compare the skill under test
against the previous version of `raise-issues`; the old Step 3 treated "same key" as "same
finding".

## Result

Three runs per side, the previous `raise-issues` against the review-target rule. The previous
Step 3 passed 6 of 8 assertions in every run: it skipped the finding about the fix because #118
shares its key (the review spiral), and skipped the same-key finding on a different file and
problem, suggesting a body refresh of #118 instead. The current Step 3 passed all 8 in every
run. The regression, declined-issue and longer-key cases passed on both, so they guard against
regressing, not against the old rule.
