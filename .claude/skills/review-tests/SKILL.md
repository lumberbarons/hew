---
name: review-tests
description: Reviews tests for issues that coverage tools miss — falsifiability, isolation hazards, dead expectations, tautological assertions, missing edge cases, and unclear test names. Use when asked to review tests, audit a test suite, check test quality, validate test isolation, or check whether tests actually catch regressions. Also invoke when a user says things like "review these tests", "audit tests/", "are these tests any good", "do these tests catch real bugs", "check test isolation", "is this tested", or asks for a quality-level (not coverage-percentage) read of unit, integration, or e2e tests.
---

# Tests Review

Review the tests in scope for quality issues.

> [!IMPORTANT]
> Consult [REFERENCE.md](REFERENCE.md) for the expected output format and level of detail.

You already know what a bad test looks like — assertions that cannot fail, shared state that leaks between cases, a spy that records a value nothing ever inspects, a name that tells a CI reader nothing, an error path nobody exercised. Apply that judgement directly and thoroughly; reading the tests closely is the bulk of the value here.

What follows is only the local policy you could not infer. Prescribing the review itself made reviews no better and measurably slower: a 221-line version of this skill scored identically to this one across four fixtures while spending 22% more tokens, because attention went to restating criteria instead of reading the tests.

## Scope

Determine the review scope before discovering files:

- If `$ARGUMENTS` is non-empty, treat it as a path (file or directory) and run:
  ```bash
  .claude/skills/review-tests/scripts/discover-files.sh "$ARGUMENTS"
  ```
- If `$ARGUMENTS` is empty, scope to files added or modified on the current branch relative to the default branch:
  ```bash
  .claude/skills/review-tests/scripts/discover-files.sh
  ```

Handle the script's exit codes:
- **0 with output** — use the listed paths.
- **0 with empty output** — branch has no diff vs the default branch. Tell the user and ask which path to review.
- **non-zero** — script prints a message to stderr (path not found, not a git repo, on the default branch with no path, detached HEAD, or default branch indeterminate). Relay the message and ask the user which path to review.

The script returns paths language-blind. Filter to test files; keep the production sources in view too, since a gap is only visible against the code nothing tests.

For three or more test files, fan out: give each subagent a couple of files, the severity rules below, and the falsifiability requirement, and tell it not to use Bash — this is static reading, so shell access only adds latency and risk. Merge what comes back before collapsing patterns.

## Severity, anchored on falsifiability

These findings feed a work-tracking pipeline, so an inflated severity becomes false urgency in someone's backlog and teaches people to ignore the reviewer. The driving question for every finding: **what fraction of meaningful regressions in the behavior this test claims to cover would still slip past it?** If the answer is "none," there is no finding.

State the claim concretely — *"if `<specific production change>` were made, this test would still incorrectly pass."* A finding you cannot put in that form is below the bar; drop it. Two exceptions: a P1 tautology (the claim is implicit — nothing can fail it) and an unclear test name (judged on CI readability, not falsifiability).

- **P1** — the test cannot fail, or it masks a known bug: assertions against a freshly-constructed mock; truthiness on an always-truthy value; a simulated event the runtime would dispatch differently; shared state that *actually* causes interference today. Fragile-but-currently-passing shared state is P2.
- **P2** — the test would still pass after deleting a *central* production behavior it claims to verify. Dead expectations (a fake records a value no assertion reads). Loose matching that defeats the point — substring-matching a JSON body where decoding and comparing is what verifies the contract. Real isolation hazards — leaked goroutines or listeners under `t.Parallel`, fragile state that breaks the moment a sibling test lands. Zero coverage on exported/public code in scope. Unclear test names.
- **P3** — the test catches the central regression but a *peripheral* one slips past. Most "could assert more" findings live here: some fields of a struct asserted but not all, one uncovered error branch beside a covered happy path, a nameable missing edge case, zero coverage on internal helpers.

P1 is narrower than it looks, and it is the severity that drifts. **"Cannot fail" is literal: no change to the production code could turn this test red.** A weak assertion is not a tautology. `assert result is not None` against a function that constructs an object is P2 — it checks far too little, but returning `None` would still fail it, so the test does hold one thin claim. Reserve P1 for the test where you can find no production edit at all that breaks it, and for shared state you can show interferes *today* by naming the two tests and the order that makes one fail. If a report carries more than one P1, be suspicious of the second.

The P2/P3 line: does the gap hit the **primary contract** of the thing under test, or a **secondary** aspect a separate test could reasonably own? If the test's name says it verifies X and X is not actually verified, that is P2 — the test is misleading. If X is verified and adjacent Y is not, P3.

On coverage gaps, use the asymmetry: **zero coverage is a strong signal of a real gap; high coverage is a weak signal of quality.** Prefer file- and module-level gaps ("nothing imports or exercises this file") over symbol-level grep, which is too noisy in layered code — a function is not a gap if a higher-level test flows through it. If a real coverage tool has been run, trust it over static heuristics.

## What not to flag

Each of these spends the reader's attention on something they cannot act on, or on a decision they already made:

- **Anything under `testdata/`, `fixtures/`, `__fixtures__/`, or `golden/`.** Those files are input to tests, not tests. A malformed JSON there is usually the point of the case that loads it, and "this fixture is invalid and unreferenced" is a finding every reviewer files and no maintainer wants.
- **A production defect rather than a test defect.** A sentinel compared with `!=` instead of `errors.Is`, a slice aliased where it should be copied — real observations, wrong review. Note them in passing at most; they belong to a code review.
- **One more input for a table that already covers its boundaries.** A parametrized test spanning zero, one, the limit, past the limit, and a negative has done its job. Asking for a further case there is padding, and it lands on the file whose author did the most work.
- **A test being slow.** `sleep` in a test is a real annoyance but not a falsifiability defect. If you raise it at all it is P3, and only alongside something that is.

## Collapse patterns, then cap at ten

N findings for N instances of one anti-pattern is noise. When several findings share a root cause — module-scope mocks across four files, fetch stubbed at import time instead of in `beforeEach`, assertions everywhere that check the call happened but not what came back — collapse them into **one** finding that names the pattern, lists the affected locations, and prescribes the codebase-wide fix, at the highest severity among them. Keep findings separate when they merely share a category.

Then cap the report at ten. It is a ceiling, not a target: four real findings means a four-finding report. Include every P1 — they never belong in a truncated tail — then fill with P2s and P3s by impact. If you cut any, end with `Note: N additional findings omitted (X P2, Y P3) — re-run after addressing these to surface what remains.`

Large reports rarely land as a single PR; they age out or get half-applied. Batches of ten are what people actually do, and re-running after fixes surfaces what only became visible once the pressing things were gone. The cap also works against the "asked to find things, so finds things" reflex — a finding that wouldn't make the top ten probably isn't worth the reader's attention.

## Output

Produce a report following the structure in [REFERENCE.md](REFERENCE.md). Each finding must include:

- **Priority** (P1/P2/P3) in the H3 header
- **Location** (file:line)
- **Explanation** of the problem and why it matters
- **Fix** — concrete prescription. For a quality bug, exactly what changes ("replace the package-level `db` var with a per-test instance built in each test's setup"). For a gap, exactly which scenario or assertion is missing ("add a case where the input slice is nil; the table has only empty and non-empty").
- **Done when** — a criterion verifiable by reading the test file. "TestFoo has no package-level mutable state; all state is initialized inside t.Run or TestFoo itself." NOT "the test is properly isolated."
