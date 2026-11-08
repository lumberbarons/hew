# review-docs evals

Why this skill is short. Three configurations were run against the fixtures here — the
329-line v1.7.0 skill, a 49-line variant carrying only local policy, and no skill at all —
two runs per fixture, 16 runs total.

| Config | Runs | Pass rate | Mean findings | Total P1 | MEMORY.md violations | Mean tokens |
|---|---|---|---|---|---|---|
| v1.7.0 (329 lines) | 4 | 90% ± 12 | 4.0 | 0 | 0 | 42,107 |
| lean (49 lines) | 6 | **100% ± 0** | 4.8 | 0 | 0 | 38,632 |
| no skill | 6 | 70% ± 24 | 8.7 | 6 | 5 | 29,966 |

Two results drove v2.0.0:

**Long instructions suppressed detection.** On `legacy/`, v1.7.0 reported 2 findings in both
runs and missed that `express` is never declared in `package.json` — so the documented
`npm install && npm run dev` cannot run. The lean variant caught it in both runs; so did the
no-skill baseline. Attention spent restating review criteria came out of reading the docs.

**But the skill is not optional.** With no skill, the baseline filed 5 findings against
`MEMORY.md` (out of scope entirely), 6 P1s across fixtures that contain no security defect,
and drifted into code defects rather than doc defects. Both skill variants: zero of each.
Its variance is in judgement, not detection — it finds things reliably and misjudges them
reliably.

So the shipped skill keeps severity anchors, the do-not-flag list, the migration rule, scope
handling, and the output contract, and drops every restatement of how to review.

## Known flaw in the "zero P1" assertion

The benchmark asserted zero P1 findings on the grounds that no fixture contains a security
defect. That is wrong for `messy/`: `src/control.js` requires a `CONTROL_TOKEN` bearer header
on `POST /jobs`, the README documents neither the header nor the endpoint, and it advises
setting the token to "any random string". Under this skill's own P1 clause — a missing auth
step — that is a defensible P1, and a confirmation run against the shipped skill raised it as
one.

The assertion should be "no P1 unless it cites a missing auth step, an exposed secret, or an
unwarned destructive command." The no-skill P1s counted against the baseline are unaffected:
all six were a broken link, a missing npm script, an incomplete service list, and a missing
CLAUDE.md invariant — none security-related.

## Fixtures

| Fixture | Seeded conditions |
|---|---|
| `legacy/` | Two pre-v1.6 exhaustive CLAUDE.md index tables (all rows derivable); an undeclared `express` dependency; a promised startup line no code prints; an inert `PORT`; an integer-cents invariant living only in a source comment |
| `enumeration/` | README documents 3 of 6 env vars in `.env.example` and 3 of 5 scripts in `package.json` |
| `lean/` | Deliberately good docs — table-free CLAUDE.md with commands, an invariant, and a real disambiguation; README with prerequisites, expected output, verification, troubleshooting. False-positive check: correct output is zero findings |
| `messy/` | Real defects (`npm run setup` absent, broken `docs/DEPLOY.md` link, 2 of 4 services documented) mixed with traps: a `MEMORY.md` that must be skipped, an unfinished Dockerfile whose *promise* is the docs defect, and an invariant correctly placed in `docs/ARCHITECTURE.md` |

## Re-running

`evals.json` holds the prompts. Point one agent at a fixture with the skill and one without,
then compare: findings count, severity mix, whether `MEMORY.md` was touched, and whether the
seeded broken commands were caught.
