# review-tests evals

Why this skill is short. Four configurations were run against the fixtures here — the
221-line v1 skill, a 66-line variant carrying only local policy, no skill at all, and the
80-line skill that shipped — two runs per fixture.

| Config | Lines | Runs | Pass rate | Mean tokens | Mean time |
|---|---|---|---|---|---|
| v1 | 221 | 8 | 91.0% ± 11.3 | 50,748 | 193s |
| lean | 66 | 8 | 91.2% ± 11.3 | 41,703 | 145s |
| no skill | — | 8 | 68.2% ± 9.8 | 35,444 | 116s |
| **shipped** | **80** | **8** | **94.9% ± 7.7** | **44,487** | **184s** |

Three results drove the rewrite:

**Length bought nothing.** The 66-line variant scored within noise of the 221-line original
(91.2% vs 91.0%) while spending 22% fewer tokens and a third less wall clock. The removed
material was the Quality Criteria section — seven headings restating that tests should cover
edge cases, assert on results, and be readable — plus the parallel-mode batching table and the
delimited subagent output format. None of it changed what the reviews found.

**But the skill is load-bearing.** With no skill the pass rate falls 23 points, and the failure
is judgement rather than detection. Baseline runs found the same seeded defects, then invented
their own severity scale (`## High` / `## Medium` / `## Minor`), emitted no `**Location:**` or
`**Done when:**` lines, drifted into production defects that belong to a code review (`errors.Is`
idiom, slice aliasing), and on `py-clean` wrote "this suite is healthy" in prose before filing
five findings anyway. On `ts-api` a baseline run rated the tautological test P2 — the severity
anchor is the single most valuable thing in the file.

**Two failures were universal, and both were fixable.** Every iteration-1 run — all three
configs — filed a finding against `testdata/broken_invoice.json`, which is deliberately
malformed input rather than a test. And every `py-mixed` run reported two P1s, inflating
`assert inv is not None` to "cannot fail" when that test *can* fail; it merely checks too
little. The shipped skill adds a "what not to flag" list and a sharper P1 boundary. In
iteration-2, no run flagged the fixture file and all eight filed exactly one P1.

## Fixtures

| Fixture | Seeded conditions |
|---|---|
| `go-orders/` | A package-level `testDB` making `TestSaveOrder_Duplicate` depend on `TestSaveOrder`; `TestPlaceOrder` asserting only `err == nil`; a `fakeNotifier` recording `reasons`/`lastID` nothing reads; exported `CancelOrder` with no test; a vague `TestHandler`. Trap: `ApplyDiscount` has no direct test but is reached through `TestPlaceOrder_AppliesVolumeDiscount`, so calling it uncovered is a false positive |
| `ts-api/` | `globalThis.fetch` replaced at module scope in three files, which should collapse to one finding; `expect(mockFetch).toBeDefined()`; a body assertion that substring-matches one hardcoded constant; exported `refreshSession` with no test. Trap: `format.test.ts` is parametrized across its boundaries and should draw nothing |
| `py-clean/` | Deliberately sound. False-positive check: correct output is zero findings, or one P3. Traps: a session-scoped fixture over a `MappingProxyType` that looks like shared state but cannot be written |
| `py-mixed/` | A module-level `_cache` causing real order dependence; `refund`'s only test permanently skipped; `test_apply_tax_accepts_valid_rates` asserting against `int(1000 * rate)`, reimplementing the production formula; `FakeTransport.last_subject`/`last_body` nothing reads; `test_1`. Traps: a `time.sleep(1)` test that is slow but not a falsifiability defect, and `testdata/broken_invoice.json` |

## Known limits of these assertions

`ts-api` asserts that `format.test.ts` draws no finding. One shipped-skill run flagged it
anyway, observing that `truncate`'s `max <= 0` guard is only exercised at `0`, so narrowing it
to `max === 0` would stay green. That is a true observation scored as a failure, because the
fixture's purpose is to measure restraint on an already-well-covered file. Read that assertion
as "did it resist padding," not "is the file perfect."

Pattern collapsing turned out not to discriminate: all six iteration-1 runs, baseline included,
collapsed the three-file fetch-mock pattern into one finding without being asked. The assertion
stays because a regression there would matter, but it does not separate configurations.

Residual variance sits in two places — whether the sound suite draws zero findings or one or
two P3s, and which finding earns the P1 on `py-mixed` (one run promoted the skipped `refund`
test over the `_cache` order dependence, though the skill puts coverage gaps at P2).

## Re-running

`evals.json` holds the prompts and assertions. Point one agent at a fixture with the skill and
one without, then compare: severity mix, whether the seeded falsifiability defects were caught,
whether the traps were resisted, and whether the report carries `**Location:**` and
`**Done when:**` lines.
