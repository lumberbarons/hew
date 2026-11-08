# review-code evals

Why this skill is short. Four configurations were measured against the fixtures here — the
249-line v1 skill, a 77-line variant carrying only local policy, no skill at all, and the
skill that shipped.

Iteration-2, all three configurations on the final fixtures:

| Config | Lines | Pass rate | Mean tokens | Mean time |
|---|---|---|---|---|
| v1 | 249 | 78.4% ± 19.2 | 48,582 | 174s |
| no skill | — | 66.2% ± 6.3 | 41,178 | 177s |
| **shipped** | **97** | **94.5% ± 3.7** | **45,876** | **161s** |

Iteration-1 ran v1 against a 77-line lean variant to isolate the length question. On the
fixtures as they then stood, lean scored 98.2% ± 3.6 and v1 90.5% ± 7.5, with no skill at
55.4% ± 12.7. Two of those fixtures were corrected afterwards, so the iteration-1 and
iteration-2 numbers are not directly comparable; within each iteration they are.

Four results drove the rewrite:

**Length bought nothing, and cost accuracy.** The lean variant beat the 249-line original in
iteration-1, and the shipped skill beat it again by 16 points in iteration-2 with a fifth of
the variance, on fewer tokens and less wall clock. The removed material was the Design
Criteria section — six headings restating that functions should have one reason to change and
names should reveal intent — plus the parallel-mode batching table, the delimited subagent
output format, and the Prerequisites list. None of it changed what the reviews found.

**The skill is load-bearing, but what it buys is discipline more than detection.** With no
skill the pass rate falls 28 points, and reading the failures, the baseline finds most seeded
defects. On `py-worker` it caught every substantive assertion. It then failed the discipline
assertions wholesale: it invented its own scale (`High`/`Medium`/`Low`,
`Critical`/`Significant`/`Minor`), emitted no `**Location:**` or `**Done when:**` lines, and
filed 14–16 findings against a cap of 10. Nothing downstream — raise-issues, or a human
triaging a backlog — can consume that. Read the gap as "the review is shaped and parseable",
not "the review found more".

**The cap was being treated as a target.** v1 landed on exactly 10 findings on both
non-trivial fixtures in iteration-1; the lean variant stopped at 8 and 9 on the same code. v1
also spent its budget on symptoms and dropped the cause — on `go-checkout` it filed the pricing
duplication, the error typing and the discarded notification error, but never `PlaceOrder`'s
single-responsibility problem, which is what produces them. The shipped skill says the ceiling
is not a target and to keep the structural finding when the cap binds.

**A clean review needs a verdict.** Both skill configs passed the restraint check on `py-clean`
in iteration-1 but neither said the code was sound — v1 emitted a bare count line and a
complaint about missing lint config. The baseline said it well and then filed ~90 lines of
findings on a 203-line package. REFERENCE.md's "no passing rows" rule had been read as "no
verdict". The shipped skill and REFERENCE.md now ask for two sentences naming what carries the
design; in iteration-2 the shipped skill scored 8/8 on that fixture with zero findings and a
stated verdict, against 4/8 for v1.

## An unmeasured change

Iteration-2 exposed one failure the rewrite does not yet fix, replicated across two fixtures:
both skill configurations missed a re-run hazard that the *unskilled* run caught. On
`go-checkout`, the refund `UPDATE` has no `AND refunded = false` guard, so a replay pays out
twice, and an unknown id returns `200 {"refunded_cents": 0}`. On `py-worker`, `charge_customer`
mints a fresh `uuid4()` idempotency key per call, so re-running the nightly batch recharges
every customer — and v1 went further, asserting it "correctly reuses one key across attempts",
a false claim in the output rather than an omission.

The shipped skill adds a short section telling the reviewer not to drop a correctness defect
because the review is framed as design. It has since been measured on `py-store-upgrade` and
`py-store-upgrade-diff`, three runs each: the skill found the unguarded migration, the
existing-database failure, and (diff-scoped) the unchanged `migrations.py` it depended on in
every run. A further paragraph that spelled out upgrade-path checks (create-once, forward-only,
index ownership) was tried on top and made no measurable difference, so it was dropped; the
two fixtures stay as the regression guard. Both are small and carry docstrings that state the
migration contract, so they show the section works, not where it stops working.

## The two-lens rubric

The skill now reads for correctness as well as design (`--lens` runs one), and can repair a
clean development branch (`--fix` / `--report-only`). Measured against the previous skill on
this fixture set and on two harnesses: Claude Code subagents, and `opencode run` on its default
(non-Claude) model, one to three runs per cell.

**What moved.** The new behaviours work on both harnesses: `--lens` is honoured and the excluded
lens stays silent, every finding carries a lens tag, and a repair is reported separately from
unresolved findings with a verification string. On `py-ledger`, the old skill still found every
seeded bug but rated the duplicate credit P2, fell for the `tier_percent` trap and filed
hypothetical-future claims; the new skill did not. The weaker the model, the more the
failing-trace bar bought.

**What did not.** On the original fixtures the two skills tie (about 94% each on Claude Code;
within run-to-run spread on opencode). The new skill costs about a quarter more time and a tenth
more tokens on Claude Code.

**A scare that was noise.** `py-store-upgrade` first looked like a regression: the new skill
dropped the note that the legacy `idx_orders_customer` is superseded. Repeated runs say the old
skill misses it too (2 of 4 runs raise it, against 2 of 6 for the new skill). Do not read
single-run differences on the P3 notes in `py-store-upgrade` as signal.

**Why some assertions changed.** "Every P1 is a security finding, at most two" encoded the
design-only severity scheme; the correctness lens rates data corruption P1, so it is now two
assertions. `parseCents` in `ts-api/src/money.ts` was fixed after a correctness reviewer found
that scaling `"1.005"` as a float rounds down: the restraint trap was not actually clean.
"Nothing drops the superseded index" was never met by any run and now accepts the write-cost
form.

**Harness differences.** Under Claude Code the eval agents are themselves subagents, so every
Claude Code run reviewed inline. Under opencode every run delegated to one subagent. Compare
harnesses on pass rate and finding content, not on time or tokens.

## Fixtures

| Fixture | Seeded conditions |
|---|---|
| `go-checkout/` | `RequireAdmin` deciding admin from a client-supplied `X-Admin` header, gating refunds; `PlaceOrder` doing validation + pricing + persistence + notification; the volume-discount ladder re-implemented in `PlaceOrder` and `Handler.quote` when `pricing.go` owns it, which should collapse to one finding; `Init` required before `PlaceOrder` with nothing enforcing it, so a skipped `Init` silently charges zero tax; `OrderRepo.Query`/`Tx` leaking the driver so the HTTP handler writes SQL; the non-idempotent refund. Traps: `NewOrderRepo(db)` is properly injected, and v1's REFERENCE.md example wrongly claimed such a constructor calls `sql.Open`; `var clock = time.Now` is already a test seam, so not a testability P1; `pricing.go` itself is sound |
| `ts-api/` | User endpoints returning the whole row including `password_hash` and `reset_token`; `new Pool(...)` at module scope in three route files, which should collapse to one finding; `getUser`/`getOrder`/`getSession` returning null vs throwing vs `{ok, data}`; row interfaces exported as the wire type; `getSession`'s bare catch turning a database outage into a 401. Traps: `money.ts` is pure and total; the module-scope pool is P2, not a testability P1, because `vi.mock` is a seam |
| `py-clean/` | Deliberately sound. Correct output is zero findings, or one P3, plus a stated verdict. Traps: a `MappingProxyType` constant that looks like shared state but is read-only and copied on construction; a `Protocol` with one implementation; `__init__` re-exports; a raise-not-return error idiom applied consistently |
| `py-worker/` | An import-time `PaymentClient(os.environ[...])` singleton; `charge_customer` collapsing declines, outages and bugs into `None` while retrying all three; a fresh `uuid4()` key per call, so a batch re-run double-charges; `process_batch` reading + validating + charging + reporting + emailing; `PaymentClient.charge` doubling as a refund by passing a charge id where a customer id belongs; `handle_data`'s name and its `ValueError("bad input")`. Traps: `process_batch` is long and deeply nested, which belongs to a linter; `time.sleep` backoff is not a design defect; the singleton is P2, not P1 |
| `py-store-upgrade/` | A migration run unconditionally on every open with no record that it ran, so the second start raises on `ADD COLUMN` and then on `CREATE INDEX`; a superseded index that `schema.init_db` recreates on every start and nothing drops. Traps: every statement in `schema.init_db` is guarded with `IF NOT EXISTS`, and `OrderRepo` is injected, parameterised and single-purpose |
| `py-store-upgrade-diff/` | A git fixture (`setup.sh` builds it): `main` holds a sound versioned-migration store, and the branch redefines the `orders` table and its index in `schema.init_db` and updates `OrderRepo` to match, but adds no migration and leaves `LATEST` alone. The diff is consistent on its own; the failure (no such column on every already-shipped database) lives in the unchanged `migrations.py` and `app.py`. Traps: the new-database path is correct, and `OrderRepo` is a faithful update |

## Known limits of these assertions

**Format assertions dominate the baseline gap.** Five or six of the twelve-odd assertions per
eval are format and severity-scale checks that move together — a run adopting REFERENCE.md
passes all of them, a run ignoring it fails all of them. This inflates the skill-vs-baseline
delta relative to the difference in review substance. The per-eval breakdowns are the honest
place to look.

**Restraint traps only work on genuinely clean code, and three of them were not.** `money.ts`
returned `0` from `parseCents("")` because `Number("") === 0`; `pricing.go` truncated its
discount while rounding tax half-up; `py-clean` twice leaked a non-`PricingError` past its
documented contract. In each case a configuration that found the real bug was scored as
failing a restraint assertion. The fixtures were fixed rather than the assertions softened —
`money.ts` now validates input shape and magnitude, `pricing.go` rounds consistently, and
`py-clean`'s `_priced` bounds the product rather than only the input. Iteration-1 numbers for
`go-checkout` and `ts-api`, and the `py-clean` `old_skill`/`without_skill` scores in
iteration-2, were measured before the corresponding fix.

**Severity-count assertions were mis-specified twice.** "Reports no more than one P1" is
satisfied trivially by any report declining to use P-levels. Rewording it to "at most one
finding in the highest severity tier" then punished the intended behaviour — a report that
correctly keeps P1 empty has P2 as its top tier, so a second P2 failed it. The current wording
names P1 explicitly and excludes reports that assign no P-levels at all.

**Negative assertions can pass by silence.** "Does NOT claim `NewOrderRepo` builds its own
connection" passes when a report never discusses `NewOrderRepo`. Pairing each with a positive
assertion that the relevant code was examined would give more signal.

## Re-running

`evals.json` holds the prompts and assertions. Point one agent at a fixture with the skill and
one without, then compare: whether the seeded defects were caught, whether the traps were
resisted, whether severities stayed anchored, whether repeated patterns collapsed into one
finding, and whether the report carries the header count, `**Location:**` and `**Done when:**`
lines.
