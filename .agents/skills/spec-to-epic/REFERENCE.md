# spec-to-epic reference

## The plan file

One JSON object per line, epic first, then its children in spec order. This
is `hew apply`'s own format (`hew apply --help` is the source of truth if
this drifts) — the fields that matter here:

| Field | Epic entry | Child entry |
|---|---|---|
| `id` | a short local id (`"epic"`) — never sent to GitHub, just how children reference it | optional; only needed if something else references this entry |
| `title` | plain title — `hew apply` adds the `Epic: ` prefix itself for `type: epic` | the story's title |
| `type` | `"epic"` | `"task"` |
| `priority` | `"P1"`..`"P4"` | `"P1"`..`"P4"` |
| `parent` | — | the epic entry's `id` |
| `goal` | the spec's Description | the story's `As a ... so that ...` sentence |
| `where` | the spec path + top-level directories, each backticked | the story's task paths, each backticked, one per line |
| `approach` | Prior Decisions / Out of Scope / Assumptions, each under a bold label | the narrative paragraph, then `- T00N: <action>` bullets |
| `done-when` | the spec's Done When list, verbatim, as a JSON array | derived per-story items (see below) |

Multi-line content is a normal JSON string with `\n` in it — hew renders it
under the heading as given, so a blank line (`\n\n`) between paragraphs and
`\n- ` for a bullet both work exactly as they would in the markdown you're
already reading.

**Wrap paths and identifiers in backticks.** `hew apply --dry-run` warns on
plain-prose code-shaped text — running it once before the real apply is what
catches a forgotten backtick, same as `raise-issues` relies on for its own
plan files.

## Worked example

Built from this repo's own `specs/001-deterministic-retirement-engine/spec.md`,
epic plus its first and fourth stories. The remaining six follow the same
shape — extend the pattern rather than inventing a new one per story.

### The epic

```json
{"id":"epic","title":"Deterministic Retirement Engine","type":"epic","priority":"P1","goal":"A deterministic retirement projection engine for a two-spouse Ontario household with no children and no bequest motive. From a single household config file it projects both spouses' income, taxes, withdrawals, and account balances year by year, from today through the second death, solving each year so that spending nets a stated target expressed in today's dollars.","where":"`specs/001-deterministic-retirement-engine/spec.md`\n\n`cmd/retire/`\n`internal/`","approach":"**Prior Decisions**\n- The engine works in nominal dollars and deflates once at the reporting layer; every year-specific constant carries its index basis\n- Zero-bequest household: success means terminal real wealth ≈ 0 at second death; CPP child-rearing drop-out, RESP, and bequest heuristics are omitted\n- CPP is approximated from each spouse's stated entitlement scaled by start-age factors, never reconstructed from earnings history\n- A same-year OAS clawback approximation is acceptable if documented; the ESDC ceilings are authoritative\n- The annual order of operations is fixed, and its validation cases within ±$1 are the gate this epic must pass\n\n**Out of Scope**\n- The optimization layer (pension-splitting/CPP-sharing optimizers, RRSP-meltdown, start-age optimization) — the next epic\n- Accumulation-phase splitting vehicles, LIRA/LIF mechanics, stochastic modelling, charitable bequest credits, and the web UI — all later epics\n\n**Assumptions**\n- Written in Go per the standing convention for new backends; this greenfield stack choice deserves its own ADR before implementation starts\n- Household config is a single YAML file; a working example ships with the repo\n- Deterministic returns default to the FP Canada 2026 Projection Assumption Guidelines and are user-overridable","done-when":["`retire project --config household.example.yaml` completes a projection from the current year to the second death and writes a year-by-year table and CSV in both nominal and today's dollars","Every validation case in the governing spec runs as a named test in CI and lands within ±$1, including the gross-vs-net solve, the RRIF minimums, the OAS clawback ceilings, and the spousal-RRIF minimum attributing $0 to the contributor","A TFSA withdrawal in a projection year restores room on January 1 of the following year, shown by a unit test","A run in which a spouse dies shows one OAS ceasing, CPP survivor applied under the combined cap, single brackets and credits, splitting stopped, spending at the survivor factor, and the TFSA merge adding no contribution room","The terminal summary reports second-death tax including the full remaining RRIF inclusion, and terminal real wealth against the $0 estate target","An invalid config fails with an error naming the offending field, and the example household loads clean","No 2026 dollar constant appears outside the dated tables in `internal/constants`, except in tests","The OAS clawback timing approximation and the quarterly-indexation treatment are documented in the README"]}
```

The epic's last done-when item — documenting the OAS clawback and
indexation conventions in the README — has no owning task anywhere in the
spec's Tasks section; no task names `README.md`. It stays here, epic-only.
No child below reuses it, and none should: stretching a story's `where` to
include a file none of its tasks touch just to give an orphaned item a home
would make that `where` a lie.

### US1 — a normal-sized story

Two of the epic's own Done When items are fully checkable inside `internal/config`
and `internal/constants` — reused rather than restated. T001 (scaffolding) and
T002 (schema shape with no validation behind it yet) get freshly derived,
structural items, because nothing in the epic list covers them.

| Item | Source |
|---|---|
| Invalid config fails naming the offending field, and the example loads clean | epic item 6, reused as-is |
| No 2026 dollar constant appears outside `internal/constants`, except in tests | epic item 7, reused as-is |
| `go build ./cmd/retire` produces a `retire` binary | derived for T001 — structural, nothing to assert behaviourally yet |
| The config schema covers spouses, accounts (by type and owner), benefits, pensions, and the spending target — checked by loading the example household | derived for T002+T004 together — the schema and the dated tables it references are one loadable shape |

```json
{"id":"us1","title":"Configure the household","type":"task","priority":"P1","parent":"epic","goal":"As a household planner, I want to describe both spouses, every account, benefit, pension, and our spending target in one config file so that the projection reflects our actual situation rather than a generic example","approach":"The config covers spouses (birth year, death age, CPP entitlement at 65 and start age, OAS start age, DB pension terms), accounts by type and owner (TFSA, RRSP/RRIF with spousal tags and contribution years, non-registered with ACB), and the spending target with its mode. Every dollar constant the engine uses lives in dated tables keyed by year with their index basis — not scattered literals.\n\n- T001: Scaffold the Go module and `retire` CLI entry point\n- T002: Define the household config schema (spouses, accounts, benefits, pensions, spending)\n- T003: Load and validate config with field-level errors; ship a working example household\n- T004: Store every year-specific constant as dated tables keyed by year with index basis, plus the FP Canada 2026 default return/inflation assumptions","where":"`cmd/retire/main.go`\n`internal/config/schema.go`\n`internal/config/load.go`\n`internal/constants/tables.go`","done-when":["An invalid config fails with an error naming the offending field, and the example household loads clean","No 2026 dollar constant appears outside the dated tables in `internal/constants`, except in tests","`go build ./cmd/retire` produces a `retire` binary","The config schema covers spouses, accounts (by type and owner), benefits, pensions, and the spending target, checked by loading the example household"]}
```

### US4 — a story past hew's single-issue size

Five tasks means five paths, past hew's own three-path threshold for a
normal single issue. That's not a reason to split it into two — the spec
already decided US4 was one story when it was drafted and approved, and
refiling it as two issues would break the 1:1 between spec story numbers and
filed issues. It just means the report should say so: work-issue's own
`--batch` sizing rule will size this one alone rather than grouping it with
others, and a reviewer benefits from knowing that in advance rather than
discovering it mid-run.

```json
{"id":"us4","title":"Trust the tax computation","type":"task","priority":"P1","parent":"epic","goal":"As a household planner, I want federal and Ontario taxes computed under the actual 2026 rules — brackets, surtax, health premium, credits, dividends, capital gains — so that I can trust the projection for real decisions","approach":"The Ontario surtax is computed on basic Ontario tax after non-refundable credits (the ordering moves a retiree between surtax bands), and the pension income amount carries per-person, per-income-type eligibility flags (DB qualifies at any age, RRIF at 65+, RRSP withdrawals and CPP/OAS never).\n\n- T011: Implement federal and Ontario bracket engines indexed from the 2026 base\n- T012: Implement the Ontario surtax on basic tax after credits, the health premium, and non-refundable credits with per-type pension-amount eligibility\n- T013: Implement eligible/non-eligible dividend gross-up and DTCs, and 50% capital-gains inclusion on ACB-tracked gains\n- T014: Accept a user-set T1032 pension-split fraction per year (default 0)\n- T015: Port every governing-spec validation case into a named Go test suite, ±$1, wired into CI","where":"`internal/tax/brackets.go`\n`internal/tax/ontario.go`\n`internal/tax/income.go`\n`internal/tax/splitting.go`\n`internal/validate/cases_test.go`","done-when":["Every validation case in the governing spec runs as a named test in CI and lands within ±$1","The Ontario surtax is computed on basic tax after non-refundable credits, not before","The pension income amount applies per-type eligibility: DB at any age, RRIF at 65+, RRSP withdrawals and CPP/OAS never","Eligible and non-eligible dividends apply their own gross-up and dividend tax credit, and capital gains include at 50% against ACB","A user-set T1032 split fraction shifts pension income between spouses; the default fraction of 0 changes nothing"]}
```

Report line for this one: *"US4 has 5 paths / 5 done-when items — past hew's
single-issue size; `work-issue --batch` will size it alone, which is
expected, not a defect."*

## Why `apply` and not `create` in a loop

`hew apply` checkpoints each creation to a state file as it writes, so a run
that dies halfway — network blip, rate limit — resumes without creating the
same issue twice. `hew create` in a loop has no such property: a crash after
issue 5 of 8 either duplicates issue 5 on retry or silently skips it,
depending on which half of the loop you re-run. `raise-issues` leans on the
same guarantee for the same reason.

## What the dry run catches

Running `hew apply plan.jsonl --dry-run` against this repo while drafting
this skill caught one real mistake: a spec path in `where` that wasn't
wrapped in backticks. hew's warning named the exact string:

```
⚠ epic: not in code spans: specs/001-deterministic-retirement-engine/spec.md — wrap them (and any command around them) in backticks
```

It's a warning, not a failure — `apply` still proceeds — but treat it as one
worth fixing before the real run. The whole reason issue bodies get this
treatment is that they're read by a person in a browser rather than an agent
in a terminal, and a bare path dissolves into the sentence around it there
in a way it doesn't in a terminal.
