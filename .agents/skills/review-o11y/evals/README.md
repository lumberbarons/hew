# review-o11y evals

Why this skill is short. Three configurations were run against the fixtures here — the
249-line v1 skill, an 88-line variant carrying only local policy, and no skill at all — two
runs per fixture.

| Config | Lines | Runs | Mean tokens | Findings/report | Reports over the cap | Report structure |
|---|---|---|---|---|---|---|
| v1 | 249 | 8 | 72,920 ± 15,220 | 8.9 (max 15) | 5 of 8 | 8/8 |
| **shipped** | **88** | **8** | **56,168 ± 7,790** | **7.8 (max 10)** | **0 of 8** | **8/8** |
| no skill | — | 7 | 37,655 ± 4,443 | — | 1 of 7 | 0/7 |

"Report structure" counts runs whose report carried the conventions block, `**Location:**`
lines, and per-finding `Done when` criteria. "Findings per report" is not meaningful for the
baseline: those runs invented their own severity scales, so there is no comparable count.

Three results drove the rewrite:

**Length bought nothing.** The lean variant found the same seeded defects as the 249-line
original while spending 23% fewer tokens. The removed material was the Criteria section — six
headings restating that ERROR should not fire on user input, that `"an error occurred"` is
useless, and that secrets do not belong in logs — plus the subagent batching table and the
delimited finding format. None of it changed what the reviews found. This matches the
review-tests result almost exactly, where a 66-line variant scored within noise of a 221-line
original at 22% fewer tokens.

**The missing cap was a real defect.** v1 set no ceiling on report length, and 5 of its 8 runs
filed more than ten findings, one of them fifteen. The other three skills in this plugin cap at
ten; this one now does too, and no run of the shipped version exceeded it.

**But the skill is load-bearing.** With no skill, not one of the seven baseline runs produced
the report structure: no conventions block, no `Location:` lines, no `Done when` criteria, no
severity tags that matched the pipeline. They invented their own scales instead — `P0/P1/P2/P3`
in one run, `Critical/High/Medium/Low` in another, `Medium/Low` in a third — and drifted into
code defects that belong to a code review (an unused variable, a missing config validation, a
missing timeout). Detection was rarely the problem; judgement and format were.

## Fixtures

| Fixture | Seeded conditions |
|---|---|
| `go-payments/` | `r.Header` dumped via `fmt.Printf` on every request, leaking `Authorization`; `card_no` logged as a structured field; `slog.Error` on four 4xx paths; `fmt.Errorf("...: %s", err.Error())` at every wrap site; `withRetry` retrying silently; no correlation id anywhere; `MarkPaid` logging a failure at INFO *and* returning it. Drift traps: `refund.go` uses `"Could not decode refund request."` against a lowercase no-period majority, and `uid` against `user_id` |
| `py-worker/` | `login` logging username, password and api_key at INFO; `verify` logging the raw token at DEBUG; `"processing failed"` discarding the bound exception; a `TimeoutError` branch falling back to cache with no log; `raise RuntimeError("save failed")` with no `from e`; a silent `ConnectionError` retry loop ending in `"something went wrong"`; startup config never recorded |
| `ts-api/` | `console.log` of the full `Authorization` header plus a winston field carrying the bearer token; `console.log` mixed with winston; `logger.error(err); throw err`; `throw new Error("failed")`; ERROR on the 404 and the missing-email path; `userId`/`user_id`/`uid` for one concept. Trap: `cancelOrder` logs WARN before falling back to cache, which is the correct treatment of a degraded branch and should draw nothing |
| `ts-clean/` | Deliberately sound. Correct output is zero findings, or one P3. Traps: pure helpers in `lib/money.ts` that correctly log nothing; `insertInvoice`/`loadInvoice` wrapping and rethrowing without logging because the terminal handler owns the report; a DEBUG success trace; a uniform camelCase/pino house style a reviewer might "correct"; `getInvoice` with no local `try`/`catch` because `route()` forwards to `errorHandler` |

## The clean fixture was not clean

Worth recording, because it is the fixture that justified building one. The first version of
`ts-clean` shipped with four real observability gaps that were not intended: `getInvoice` had no
error owner at all, the access log fired only on `finish` so client-aborted requests vanished,
`chargeId` fell out of scope before the failure log that needed it, and pino's default `err`
serializer does not walk `cause`, so every carefully-attached cause chain was being dropped at
serialization.

Three independent runs — the lean skill twice and the unaided baseline once — found them, and
the P3 on the access log named the consequence precisely: *"any p99 figure computed from these
lines is drawn only from requests that survived, so the dashboard reports the incident as milder
than it is."* The fixture was repaired afterwards. The iteration-2 numbers above therefore
exclude `ts-clean` from any pass-rate claim, since its assertions asked for restraint on a file
that genuinely deserved findings; the token and structure columns include it.

## Known limits of this benchmark

Assertion-level grading did not complete — the session hit its usage limit partway through the
grading pass, so the table above reports objective structural and cost measures rather than a
pass rate. The assertions in `evals.json` are written and unused; re-running the graders is the
first thing to do before trusting any pass-rate claim about this skill.

Two further gaps. `ts-api` has only one baseline run rather than two, after four attempts died
on connection drops. And wall-clock timings were discarded entirely: runs were queued behind a
20-subagent concurrency limit, so several show durations of hours that measure the harness, not
the skill. Token counts are the cost signal to trust here.

## Re-running

`evals.json` holds the prompts and assertions; `{FIXTURES}` is this directory's `fixtures/`.
Point one agent at a fixture with the skill and one without, then compare: whether the seeded
defects were caught, whether the traps were resisted, whether severities stayed anchored, and
whether the report carries the conventions block, `**Location:**` lines and `Done when`
criteria. On `ts-clean`, the question is whether the reviewer can say a codebase is fine.
