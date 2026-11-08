---
name: raise-issues
description: File review findings as GitHub issues using the hew CLI, deduplicated by a stable review key so re-running a review does not re-file tracked work. Use after any critique review skill (review-code, review-tests, review-docs, review-o11y, review-portability) or with a findings file, and whenever the user says "raise these", "file these findings", "turn this review into issues", "create issues for these", or runs a review inside an automated loop.
---

# Raise Issues

Turn structured review findings into hew issues, deduplicated by a stable key so the same
finding does not get filed twice when a review runs again.

> [!IMPORTANT]
> [REFERENCE.md](REFERENCE.md) carries slug handling, the findings and plan-file shapes, the
> converter script, and a worked example. Read it before computing keys.

This skill does not run a review. If the findings are stale, re-run the review first.

## Input

Parse the input for:

- **`--findings <path>`** — a JSON findings file. Preferred, and the only form that behaves
  deterministically unattended.
- **`--dry-run`** — build the plan and run `hew apply --dry-run`, then stop.
- **`--non-interactive`** — never ask anything; skip the confirmation step and report the
  outcome instead.
- **anything else** — filtering guidance, e.g. "only P1 and P2", "skip the naming findings".

Findings come from `--findings` when given, otherwise from the review report in conversation
context (a `N files reviewed, M issues found` header followed by `[P1]`/`[P2]`/`[P3]` findings
with `**Location:**`, an explanation, a fix, and a done-when criterion). Formatting variation is
fine; priority, location and fix are what matter.

When neither is available: ask which findings to file if a human is present, and if
`--non-interactive` is set, report that no findings were supplied and exit without filing.

**Say which of the two nothing-happened cases you are in.** "Reviewed and found nothing" and
"never received findings" both file zero issues, and a loop that cannot tell them apart reports
a broken pipeline as a clean codebase.

## Prerequisites

`hew` on PATH and authenticated — exit code 4 from any `hew` command means run `gh auth login`.
Run from a checkout of the target repository; `--repo owner/name` overrides detection.

## Step 1 — Normalize each finding

Extract priority, title, location(s), explanation, fix, done-when criterion, and which review
skill produced it. Derive a done-when from the fix if it is missing. Skip any finding with no
priority, no location, or no fix, and count the skips in the final report rather than guessing.

## Step 2 — Compute the review key

The key is what makes a second run recognise its own earlier work:

```
review-key: <skill>/<pattern>/<scope>
```

- **`skill`** — `o11y`, `tests`, `docs`, `code`, or `portability`. Normalize the legacy
  `skills` identity to `portability` for new keys; the dedup step also searches legacy keys.
- **`pattern`** — comes from the review itself when the findings file carries one, and must be
  passed through unchanged; re-spelling a slug here orphans every issue already filed under the
  original. Only when a finding arrives without one, pick from the vocabulary in the relevant
  critique skill's `REFERENCE.md`.
- **`scope`** — **derive this mechanically**: the deepest directory that contains every
  affected file, or the file path itself when there is exactly one. Do not choose it by
  judgement. Two runs that anchor the same finding at `internal/http` and
  `internal/http/handler` file it twice, and that single inconsistency is what turns a loop
  into a duplicate factory.

Severity is deliberately **not** part of the key. It legitimately moves between runs — after a
recalibration or a pattern collapse — and identity has to survive that.

## Step 3 — Deduplicate

Follow the read order the primer prescribes rather than inventing one — `hew search` first, then
`hew list --json --bodies --state all` when exhaustiveness matters, and `hew show <n>` only to
read a candidate those surfaced. Search on the key itself: `hew search "review-key: <key>"`.

For portability findings, search both `hew search "review-key: portability/<pattern>/<scope>"`
and `hew search "review-key: skills/<pattern>/<scope>"`. This applies to conversation reports
and converter-produced plans, including old plans that still use `skills/`. Treat a match under
either prefix by the same table below. When refreshing an existing issue, preserve its stored
review key. Before applying a surviving old plan, normalize its new-issue key to `portability/`.
Do not bulk-rewrite stored keys or treat an identity spelling change as a regression.

**The key finds candidates; it does not decide identity.** Many findings in one package
legitimately share a skill, pattern and directory, so a key hit is a lead, not a verdict. A hit
is *this finding, already filed* only when all three hold:

1. its stored `review-key:` line equals the key exactly — a substring hit, such as an older key
   sitting inside a longer one, is not a match;
2. its body names the same affected paths and describes the same problem;
3. it was raised against the same review target. When this run reviews an issue's PR
   (`--reviewed-issue <n>`), the hit's `review-of:` line must name `#<n>`. A hit with no
   `review-of:`, or one naming another issue, describes earlier code — including the issue
   under review itself — so it does not suppress a finding about the fix. Without a review
   target (a review of product code), ignore `review-of:`.

Test 3 is what stops a review spiral from deduplicating onto itself: a finding about the fix for
#n shares the key of the finding #n fixed, and only `review-of:` tells them apart. It compares
the issue, not the PR, so a retry PR for the same issue still matches.

When no hit passes all three, file the finding. Several old issues sharing one key are not a
reason to stop and ask — compare their bodies and file if none describes this finding. For
converter plans, `dedup_candidates.py` applies tests 1 and 3 mechanically, leaving you test 2:

```bash
hew list --json --bodies --state all > issues.ndjson
uv run --no-project <this skill's directory>/scripts/dedup_candidates.py plan.jsonl issues.ndjson \
  [--reviewed-issue <n>]
```

It prints one object per plan line: `candidates` (read these; `same_paths` says whether the
affected paths are unchanged) and `other_generation` (same key, different review target — never
a reason to skip). It never queries hew.

The part to hold on to is `--state all`. Every step of that order spans closed issues as well as
open ones, and that is load-bearing rather than incidental: a dedup pass reading only open issues
re-files every finding the moment its fix merges, which is the failure this whole scheme exists
to prevent.

Then act on what came back:

| Match | Action |
|---|---|
| Open issue, same finding | Already tracked — skip. If the scope widened (more files now affected), refresh the body with `hew set <n> --body-file <f>` rather than filing a second issue. |
| Closed as completed, same finding | The pattern came back. File a new issue with `--discovered-from <n>` — this is a regression, and it is the most valuable thing the key buys you. |
| Closed as not-planned or duplicate | Suppress permanently. Re-filing something a human explicitly declined is the fastest way to get this pipeline muted. |
| No matching issue | Before filing, check the open review issues for the same problem under a different slug, and reuse the existing issue if you find one. This is the backstop for vocabulary drift. |

## Step 4 — Map onto hew fields

- **Priority maps straight across**: P1→`P1`, P2→`P2`, P3→`P3`, P4→`P4`. **Never assign P0.**
  It stays reserved for a human declaring an emergency, which is also what hew's own CI triage
  agent does.
- **Type**: `bug` for something broken or wrong; `task` for something missing — an absent test,
  an uncovered error path, a missing prerequisite section.
- **Body sections**: `where` ← the location list, then a blank line, then the `review-key:` line.
  `problem` ← the explanation. `fix` ← the prescription. `done-when` ← the criterion, as one or
  more checklist items.

The key lives in `where` because hew renders bodies as-is, which keeps it greppable by both
`hew search` and `hew list --bodies` without needing a field the tracker does not have.

**Write code-shaped text as code spans.** Paths, symbols, flags, commands, and error strings all
go in backticks — `` `internal/http/middleware/logging.go:13` ``, `` `r.Header` ``,
`` `fmt.Errorf("...: %w", err)` ``. Wrap the whole command rather than the flag inside it. hew
checks composed bodies and warns about unmarked code text, but the reason is not the warning:
review findings are made almost entirely of these strings, and an issue body is the one artifact
here written for a human in a browser rather than an agent in a terminal. Rendered as
proportional prose, the characters carrying the meaning — slashes, dots, double dashes — are
exactly the ones that dissolve into the sentence around them.

**The `review-key:` line is the exception — leave it bare.** It is an identity token that
`hew search` matches as a substring, not prose anyone reads, and its format is load-bearing:
re-rendering it orphans every issue already filed under the old spelling. Accept the warning on
that line.

## Step 5 — Write a plan, then apply it

When the input is a findings *file* — the unattended case, and the one pump-driven filing
uses — produce the plan with the converter rather than by hand:

```bash
uv run --no-project <this skill's directory>/scripts/findings_to_plan.py <findings.json> \
  [--parent <epic>] [--reviewed-issue <n>] [--reviewed-pr <n>] [--at-or-above Pn] --out plan.jsonl
```

It derives the review keys and scopes mechanically, emits the plan shape below, stamps
`review-of: #<n>` (the marker downstream merge-pass tooling greps for), and skips malformed
findings with a count. Deduplication stays yours — run Step 3 against the emitted plan
(`dedup_candidates.py` narrows the candidates) and delete suppressed lines before applying.

Otherwise write every surviving finding to a JSONL plan (shape in REFERENCE.md) and apply it:

```bash
hew apply findings-plan.jsonl --dry-run    # always first
hew apply findings-plan.jsonl
```

Use a plan rather than a loop of `hew create` calls: creation is checkpointed to a state file as
it happens, so a run that dies halfway resumes without creating duplicates. That property is the
whole reason this is safe to run on a schedule.

Show the plan and confirm before applying, unless `--dry-run` or `--non-interactive` is set.

## Step 6 — Report

Close with counts that let the next run — or a human reading a job log — tell what happened:
issues filed, findings already tracked, findings suppressed against a declined issue,
regressions re-filed, and findings skipped as malformed.
