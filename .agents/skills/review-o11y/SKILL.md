---
name: review-o11y
description: Reviews observability — logging consistency, log level appropriateness, log value, missing logs at I/O boundaries, and error-message quality. Use when asked to review logging, observability, log quality, error messages, or to audit how a codebase logs and reports failures. Also invoke when a user says things like "check the logging", "are our logs any good", "do we log the right things", "are error messages consistent", or asks why operational visibility is poor in a service.
---

# Observability Review

Review the logging and error messages in scope.

> [!IMPORTANT]
> Consult [REFERENCE.md](REFERENCE.md) for the expected output format and level of detail.

You already know what bad observability looks like — a bearer token in a request log, ERROR firing on a user's typo, `"something went wrong"` as the only clue an operator gets, a retry that hides a flaking dependency, a wrap that throws away the cause. Apply that judgement directly and thoroughly; reading the log and error sites closely is the bulk of the value here.

What follows is only the local policy you could not infer. Prescribing the review itself bought nothing measurable: a 249-line version of this skill found the same defects as this one across four fixtures while spending 23% more tokens, and because it set no ceiling on report length it filed up to fifteen findings in a single report.

## Arguments

Your input may carry flags alongside a path. Strip the flags first; whatever remains is the path.

- **`--json <path>`** — additionally write a findings file at that path, per [FINDINGS.md](FINDINGS.md). Off by default; the markdown report is unchanged either way.
- **`--inline`** — review in this session instead of delegating to subagents.
- **`--non-interactive`** — ask no questions. Where this skill would otherwise prompt, report the outcome and stop.

## Scope

Determine the review scope before discovering files. The discovery script ships in this skill's own `scripts/` directory — resolve it against this skill's directory rather than hardcoding an install path, which only holds for one harness's layout:

```bash
DISCOVER="<this skill's directory>/scripts/discover-files.sh"
```

- If a path was given, treat it as a path (file or directory) and run:
  ```bash
  bash "$DISCOVER" <path>
  ```
- If no path was given, scope to files added or modified on the current branch relative to the default branch:
  ```bash
  bash "$DISCOVER"
  ```

Handle the script's exit codes:
- **0 with output** — review the listed paths.
- **0 with empty output** — nothing is in scope. Interactively, say so and ask which path to review; under `--non-interactive`, report `no_scope` and stop.
- **non-zero** — the script explains itself on stderr (path not found, not a git repo, on the default branch with no path, detached HEAD, or default branch indeterminate). Relay that message, then ask which path to review, or under `--non-interactive` report `error` with the message and stop.

A review that ran and found nothing, and a review that never ran, both end with zero findings and mean opposite things — so say which happened, in the report and in the findings file's `status`. A caller that cannot tell them apart reports a broken pipeline as a clean codebase.

The script returns paths language-blind. Filter to source files, excluding tests, generated code, and vendored dependencies.

Delegate by default: detect the conventions below first, then spawn subagents and give each a
couple of files, the severity rules, and those conventions. Review in this session only when
`--inline` was given or the harness cannot delegate. Keep delegated reviews to static reading:
use available file-reading/search tools, including terminal commands when needed, but do not
execute project code. Merge what comes back before collapsing patterns.

## Judge semantics prescriptively, syntax descriptively

This is the split that keeps the review honest, in both directions.

**Semantics are prescriptive.** Whether a token reaches a log, whether ERROR means "act on this", whether a retry is visible, whether a wrap keeps the cause — these follow from how oncall actually works, so flag them even when the codebase is consistently wrong. Consistency is not a defence.

**Syntax is descriptive.** Logger library, field-name casing, whether the correlation id is `request_id` or `traceId`, error-message capitalization and verb form — reasonable shops differ, and a shop's consistent choice is its right. Detect the dominant convention first, then flag drift *against that baseline*, never against your own taste.

So before flagging anything on the syntactic axis, sample the codebase and work out what normal looks like here: logger library, call shape (structured vs string-formatted), field casing, correlation field names, error-message format and construction pattern. Open the report with the **Detected conventions** block REFERENCE.md shows, so a reader can catch you anchoring wrong.

When one pattern clearly dominates, minority sites are outliers. When the codebase is genuinely split down the middle, no side is an outlier — raise one finding that the codebase runs two conventions and asks readers to know both, and let the maintainers pick.

## Severity, anchored on operational consequence

These findings feed a work-tracking pipeline, so an inflated severity becomes false urgency in someone's backlog and teaches people to ignore the reviewer. The driving question: **what does an operator lose at 3am because of this?**

- **P1** — a secret, token, credential, or PII reaches a log or an error message, at any level (DEBUG included: log levels are configuration, and the payload is already written). Also: a failure swallowed with neither a log nor a propagation, so it is invisible everywhere.
- **P2** — real operational blindness or real alert damage. ERROR on expected user-input rejections, which pages someone for a typo; an operationally critical failure logged below the alerting threshold; missing logs at an I/O boundary (outbound call, inbound handler, silent retry, silent fallback); a wrap that drops the cause chain so `errors.Is` / `except X from e` / `err.cause` stops working; codebase-wide unstructured logging, which no aggregator can filter on; no correlation id at inbound handlers, so a single request cannot be stitched together.
- **P3** — field-name drift, format inconsistency, entry/exit noise, an unactionable message on a rare path. **A P3 must name the mistake it causes**: *"an operator alerting on `failed to charge` misses every refund failure, because this file says `could not`"*. If you cannot state the specific operational mistake, the finding is below the bar — drop it.

P1 is narrow, and it is the severity that drifts. It is for data that should never have been written, not for data that is hard to find. A missing correlation id makes debugging slow; a logged bearer token makes it an incident. If a report carries more than two P1s, be suspicious of the third.

## What not to flag

Each of these spends the reader's attention on something they cannot act on, or argues against a decision they already made:

- **A missing log anywhere that is not an I/O boundary.** "This pure function should log" is the single most common way an observability review turns into noise. The boundaries that earn a finding are: outbound calls, inbound handlers, retries, fallbacks and degraded-mode branches, and startup configuration. Everywhere else, silence is the correct default.
- **The same failure logged again at every layer.** One owner per failure — usually the handler at the top — with inner layers wrapping and returning. A repo function that returns a wrapped error without logging is doing it right, not missing a log.
- **A code defect rather than an observability defect.** A missing timeout, a swallowed retry budget, a race — real observations, wrong review. They belong to `review-code`; note them in passing at most. This skill judges the artifacts: the log lines emitted and the error messages constructed.
- **A consistent house style you would have chosen differently.** Capitalized error messages, `traceId` over `trace_id`, a logger that is not the one you like. If it is uniform, it is not a finding.
- **Anything under `fixtures/`, `testdata/`, or `__fixtures__/`.** Those files are test input; their defects are usually the point.

## Collapse patterns, then cap at ten

Observability problems are uniform in a way most defects are not: if a codebase logs unstructured strings it does so everywhere, and if one handler is missing a correlation id they all are. N findings for N instances of one pattern is noise that hides the two findings that matter. Collapse them into **one** finding that names the pattern, lists the affected locations, and prescribes the codebase-wide fix, at the highest severity among them.

Then cap the report at ten. It is a ceiling, not a target: four real findings means a four-finding report. Include every P1, then fill with P2s and P3s by impact. If you cut any, end with `Note: N additional findings omitted (X P2, Y P3) — re-run after addressing these to surface what remains.`

Batches of ten are what people actually apply in one pass; larger reports age out or get half-applied. The cap also works against the "asked to find things, so finds things" reflex — and on a codebase that is genuinely well instrumented, the right report is a short one that says so.

When a report comes out short, close it with a sentence or two naming what you looked at and deliberately did not raise — the pure helpers that correctly stay silent, the inner layer that wraps without logging because the handler owns the report. Silence reads as a shallow review otherwise, and the maintainer cannot tell a considered pass from a skim.

## Output

Produce a report following the structure in [REFERENCE.md](REFERENCE.md). Each finding must include:

- **Priority** (P1/P2/P3) in the H3 header
- **Location** (file:line, or a list of locations if the finding was collapsed)
- **Axis** (prescriptive or descriptive) — so the reader can tell an enforced rule from a consistency call
- **Explanation** of the problem and the operational consequence
- **Fix** — concrete prescription. For a consistency finding, reference the detected baseline ("the dominant form here is lowercase `failed to X: <cause>`"). For a prescriptive one, reference the rule.
- **Done when** — a criterion verifiable by reading the diff. "Every handler in `internal/http/` obtains its logger from `ctx` with `request_id` already bound." NOT "correlation context is added."

When `--json <path>` was given, write the findings file described in [FINDINGS.md](FINDINGS.md) as well. Two fields need care because nothing downstream can recover them if they are wrong: `files` must list every path a finding touches, and `pattern` must be the slug that names its root cause, taken from the vocabulary in [REFERENCE.md](REFERENCE.md). Findings you collapsed together share a root cause, so they share a slug — that pairing is what lets a later run recognise this finding as one it has already reported.
