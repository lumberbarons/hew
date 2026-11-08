---
name: review-code
description: Reviews code for design issues and correctness bugs that static analysis misses — single responsibility, abstraction levels, testability, meaningful naming, API design, error-handling strategy, and defects you can trace to a failing input or state. Use when asked to review code, audit a module, check architecture or design, find design problems or bugs, improve code quality, or look at testability/coupling/SRP. Also invoke when a user says things like "review this code", "is this well-structured", "audit src/X for design problems", "what's wrong with this module", "check the architecture here", "look for SRP violations", "is this testable", "any bugs in this?", "will this break on retry?", or asks for a design-level (not lint-level) read of a file or directory.
---

# Code Review

Review the code in scope through two lenses: **design** (how the code will fare under change) and **correctness** (what it does wrong today). They are separate reads with separate severity rules; the report merges them.

> [!IMPORTANT]
> Consult [REFERENCE.md](REFERENCE.md) for the expected output format and level of detail.

You already know what bad design looks like — a function with two unrelated reasons to change, business rules interleaved with transport parsing, a constructor that reaches out and builds its own dependency, an API that must be called in an order nothing enforces, a name that describes a category instead of an action, an error path that collapses three different failures into one. Apply that judgement directly and thoroughly; reading the code closely is the bulk of the value here.

What follows is only the local policy you could not infer. In an earlier design-review benchmark, a 249-line version scored 78.4% across four fixtures against 94.5% for a 97-line version, with five times the variance. Those measurements predate the two-lens rubric; they support keeping instructions focused on local policy rather than restating review criteria.

## Arguments

Your input may carry flags alongside a path, branch-review request or PR target. Strip the
flags before resolving that target.

- **`--json <path>`** — additionally write a findings file at that path, per [FINDINGS.md](FINDINGS.md). Off by default; the markdown report is unchanged either way.
- **`--lens design|correctness`** — run one lens only. Without it, both run.
- **`--inline`** — review in this session instead of delegating to subagents.
- **`--fix`** — explicitly request a review followed by one repair pass.
- **`--report-only`** — return findings without editing, overriding automatic fixing.
- **`--non-interactive`** — ask no questions. Where this skill would otherwise prompt, report the outcome and stop.

## Mode and target

Follow [REVIEW-MODE.md](REVIEW-MODE.md) before discovering scope or writing output. Run this
skill's `scripts/review-mode.py` with the resolved scope and explicit intent. Only a clean,
clearly identified local development-branch change review fixes automatically; PR targets,
general audits and uncertain cases report only. Explicit intent overrides those defaults.
The review subagent stays read-only; the calling agent handles the bounded repair and
verification phase when the selected mode permits it.

## Scope

Determine the review scope before discovering files. The discovery script ships in this skill's own `scripts/` directory — resolve it against this skill's directory rather than hardcoding an install path, which only holds for one harness's layout:

For a PR target, use the PR's base/head diff as described in `REVIEW-MODE.md` instead of
passing the PR to the path-discovery script below.

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

The script returns paths language-blind. Filter to source files, dropping tests, vendored and generated code, and pure configuration.

Delegate by default, choosing the review shape from the user's intent:

- **Branch or focused change review** — one subagent gets the complete diff in scope and access
  to surrounding code so it can trace interactions across files. Do not split the change by
  file, directory or lens, including through further delegation.
- **Exploratory audit** — when looking for existing issues across a codebase or directory,
  multiple subagents may review independent subsystems. Divide by coherent responsibility,
  not arbitrary file counts; each reviewer applies both lenses to its entire subsystem.

A path selects scope, not review mode: reviewing a change within a directory still uses one
reviewer. Give each reviewer both lens sections below. Make two explicit passes: correctness
first, then design. Apply each lens's own reporting bar and severity rules; neither pass needs
to produce a finding. Merge overlapping root causes into one report after both passes.
With `--lens`, run only that lens; the other lens is out of scope.

Review in this session only when `--inline` was given or the harness cannot delegate, using
the same two-pass method.
Keep delegated reviews to static reading: use available file-reading/search tools, including
terminal commands when needed, but do not execute project code.

## Design lens

This lens reads for how the code will fare under change. A defect that is wrong today belongs to
the correctness lens; do not hunt for one here.

### Severity, anchored on a named consequence

These findings feed a work-tracking pipeline, so an inflated severity becomes false urgency in someone's backlog and teaches people to ignore the reviewer. The driving question for every finding: **name a specific change someone is likely to make that this design would predictably break.** State it concretely — *"adding a CLI entry point would force re-implementing the discount tiers currently inlined in the HTTP handler."* A finding you cannot put in that form is below the bar; drop it, and do not demote it to P3 because it sounds mild. P1 is the exception: its consequence is implicit.

- **P1** — a security flaw in the design, or code with no test seam at all. Authorization decided from caller-controlled input, a privileged operation with no check, a secret or credential that escapes into a response or a log. Or: no way to substitute a dependency, even with the language's standard mocking tools.
- **P2** — a change someone is likely to make goes wrong. An SRP violation where you can name the ripple ("a pricing rule change lands in the same function as a validation rule change"). Mixed abstractions where you can name the entry point the mixing blocks. Temporal coupling that nothing enforces, with the misuse named. Divergent shapes across sibling APIs, where you name the consumer that cannot share code. Testability friction whose cost you can state as a test that cannot be written cleanly.
- **P3** — the same shape, but the named consequence is narrower: it misleads a reader, or costs readability rather than future change. Naming and small API asymmetries usually live here.

P1 is narrower than it looks, and it is the severity that drifts. Where the language has monkeypatching — Python, TypeScript — a module-scope singleton is almost never a testability P1: `monkeypatch.setattr` or `vi.mock` is the seam, so it is friction, not a wall. Reserve it for the design where you can name no seam at all, and for security flaws you can trace from an attacker-controlled input to the privilege it buys. If a report carries more than one P1, be suspicious of the second.

The P2/P3 line: does the named consequence hit a change someone is **likely to make**, or only a **reader's first impression**? A design that forces a future refactor to land in many places is P2; one that merely reads awkwardly is P3.

### What not to flag

Each of these spends the reader's attention on something they cannot act on, or on a decision they already made:

- **Anything the toolchain already prints.** Linters, formatters, type checkers and security scanners run alongside this review, and they own function length, nesting depth, cyclomatic complexity, dead code, formatting, type errors, injection and hardcoded secrets. If a linter would flag it, it is not a design finding. Where those tools appear to be missing, say so once in the report rather than filing their findings by hand.
- **A test defect rather than a code defect.** A weak assertion or a missing case is a real observation and the wrong review; it belongs to a test review.
- **Anything under `fixtures/`, `testdata/`, `__fixtures__/`, or `golden/`.** Those files are deliberately shaped input, and their defects are usually the point.
- **A missing abstraction nothing yet needs.** An interface with one implementation, a config value not yet configurable, a layer someone might want later — speculative generality costs more than it saves, and asking for it lands on code that is currently right.
- **An alternative idiom the codebase has already chosen.** Raising versus returning an error, constructor injection versus a factory, a protocol with a single implementation: a defensible convention consistently applied is not a defect. Flag inconsistency, not preference.

## Correctness lens

You already know what a bug looks like — a cache keyed on fewer fields than its value depends
on, an error swallowed so the caller carries on with stale data, a rounding step that drifts a
total by a cent per line, an operation that is not idempotent in a world that will retry it, a
change that fits a fresh database and breaks every existing one. Read for those. The cost is false positives: a suspected bug that is not one is worse than a missed
design note, because it sends someone chasing.

**The bar is a failing trace.** Every finding names a concrete input or state, the path the code
takes on it, and the wrong result: *"`prices(user, currency)` caches under
`user` alone at catalog.py:41, so a user's first EUR request is answered from their earlier
USD entry and every later one repeats it."* A finding
you cannot put in that form is a suspicion; drop it. Trace into callers and unchanged
dependencies before filing — the failing path often lives in code the diff did not touch, and a
guard you did not look for may exist there.

- **P1** — security flaw traceable from attacker-controlled input to the privilege it buys; data
  loss or corruption; money moved wrongly or twice; a crash on startup or on existing data.
- **P2** — wrong result or failure on input or state that normal operation reaches.
- **P3** — wrong only at an edge value or in a rare, recoverable state.

Do not flag:

- Anything the toolchain already prints — type errors, unused values, and what injection and
  secret scanners report.
- A hazard that requires a caller to break the documented contract, or a state the types or an
  upstream guard rule out.
- Speculative failures: "could be nil", "might overflow" with no path that gets there.
- Performance, style and structure. A bug whose only fix is a redesign is still filed here, as a
  bug; the redesign itself belongs to the design lens.
- Defects in tests, and anything under `fixtures/`, `testdata/`, `__fixtures__/` or `golden/`.

In a findings file, re-run, retry and upgrade-over-existing-data hazards take the
`unsafe-state-transition` slug.

## Merging the lenses

Findings from both passes go into one list, each finding tagged with its lens. When both lenses report the
same root cause — a forged header the design reviewer called a trust-boundary flaw and the
correctness reviewer traced to a refund — keep one finding at the higher severity and keep the
failing trace.

## Collapse patterns, then cap at ten

N findings for N instances of one anti-pattern is noise. When several findings share a root cause — three route modules each constructing their own connection pool at import scope, business rules inlined at every transport boundary, every repository leaking its driver's row type — collapse them into **one** finding that names the pattern, lists the affected locations, and prescribes the codebase-wide fix, at the highest severity among them. Keep findings separate when they merely share a category.

Then cap the report at ten. It is a ceiling, not a target: four real findings means a four-finding report, and landing on exactly ten more than once is a sign of padding rather than a coincidence. Include every P1 — they never belong in a truncated tail — then fill with P2s and P3s by impact, favouring the ones whose consequence reaches more callers. If you cut any, end with `Note: N additional findings omitted (X P2, Y P3) — re-run after addressing these to surface what remains.`

When the cap binds, keep the finding that names the largest structural problem even if several smaller ones are easier to write up. A unit doing four unrelated jobs is worth more to the reader than three separate notes about its consequences, and it is the finding most easily crowded out — symptoms are more concrete than their cause, so they win the budget unless you watch for it.

Design changes have wider blast radius than test fixes, so churn matters more here, not less. A thirty-finding review is unmergeable as one PR; it ages out or gets half-applied. Batches of ten are what people actually do, and re-running after fixes surfaces what only became visible once the pressing things were gone — often one cross-cutting refactor dissolves several adjacent findings. The cap also works against the "asked to find things, so finds things" reflex.

## When the code is sound, say so

A review that finds little should read as a verdict, not as a shrug. Reporting only issues is the right default, but a bare count line leaves the reader unable to tell a careful review from a cursory one. State what the selected lens or lenses established: that the design holds up, that no failing trace turned up, or both. Name the evidence carrying that verdict — the injected dependencies, the single-purpose modules, the error type that gives callers something to branch on. With `--lens`, make no claim about the excluded lens. Two sentences is enough. Then file the one or two real findings if there are any, and stop; do not reach for filler to make the report look substantial.

## Output

Produce a report following the structure in [REFERENCE.md](REFERENCE.md). In fix mode,
report unresolved findings in that format and summarize verified repairs separately per
`REVIEW-MODE.md`. Each finding must include:

- **Priority** (P1/P2/P3) and **lens** (`design` or `correctness`) in the H3 header
- **Location** (file:line)
- **Explanation** of the problem and why it matters — the named consequence for a design finding, the failing trace for a correctness finding
- **Fix** — concrete prescription. For an API design issue, give the exact shape: parameter names, types, signatures, not just the general approach.
- **Done when** — a criterion verifiable by reading the diff, referencing specific functions, files or observable behaviour. "Both parseSkillFrontmatter and parseAgentFrontmatter delegate to a shared parseFrontmatterRaw; no duplicated delimiter-scanning code remains." NOT: "the duplication is removed." For a correctness finding, name the guard or fix and the case that now behaves correctly: "`prices` caches under `(user, currency)`; a test requesting USD then EUR for one user gets EUR prices the second time."

When `--json <path>` was given, write the findings file described in [FINDINGS.md](FINDINGS.md) as well. Three fields need care: `files` must list every path a finding touches, `lens` must say which lens raised it, and `pattern` must be the slug that names its root cause, taken from the vocabulary in [REFERENCE.md](REFERENCE.md). Findings you collapsed together share a root cause, so they share a slug — that pairing is what lets a later run recognise this finding as one it has already reported.
