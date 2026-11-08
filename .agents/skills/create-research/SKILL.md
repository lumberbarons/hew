---
name: create-research
description: Write up a piece of research — a question investigated, the findings with their confidence and sources, what is still unknown, and when it goes stale — into docs/research/NNNN-slug.md in a fixed format enforced by a checker script. Use whenever something should be investigated and recorded before anything is built or decided — "research X", "look into whether Y", "investigate Z and write it up", "compare these options before we pick", "what do we actually know about…", "do a spike on…", or findings from the conversation that should be kept. This is the step before an ADR or a spec, and the evidence they cite. Not for recording a decision (create-adr) or scoping a feature (draft-spec).
argument-hint: The question to investigate
---

# create-research

Turn a question into one reviewable file: what was asked, how it was looked into,
what was found and how far to trust each finding, what is still unknown, and when
the answer stops being true. A script owns the format so every research document
comes out identical; you supply the investigation and the judgment.

This is the first step of **research → ADR → spec**. Research establishes what is
true. An ADR then records what was decided given that, and a spec says what to build.
Keeping the three apart matters: a research document that quietly makes the decision
hides the choice from the person who should make it, and a decision with no research
behind it gets re-litigated by the next agent who wonders why.

Research is the **upstream** document: ADRs and specs cite it, and it does not depend on
them. The links still run both ways where both ends are stable. An ADR cites the research
that informed it, and `create-adr` then adds that ADR's id to the research document's
`related`, so a reader of either can find the other. Specs are different: they get
renamed, split and archived, so a spec cites research from its own side and research never
links to one. Do not guess at a consumer when you write — an ADR id appears in `related`
once that ADR exists.

## The script

`scripts/research.py` sits beside this file. Resolve it once against this skill's own
directory and reuse it through the steps below:

```bash
RESEARCH="<this skill's directory>/scripts/research.py"
```

A hardcoded `.claude/skills/...` path works only while the skill lives in a repo
checkout, and breaks once it is installed elsewhere or shipped in a plugin. The script
is stdlib only and needs no virtualenv; run it with `uv run --no-project "$RESEARCH" ...`.

## 1. Decide whether this is research

The request should be a question that looking can answer: "does the export lose rows
when the database fails over?", "what are the real differences between A and B for our
workload?". Three things that look like research are not:

- **A decision.** "Should we use Postgres or DynamoDB?" asks for a choice. The research
  is the part underneath it — what each can and cannot do for this project — and the
  choice belongs in an ADR. Write the research as the evidence, and say in your report
  that the choice itself is a `create-adr` job.
- **A feature.** "Figure out what the watchlist should do" is scoping; use `draft-spec`.
- **A topic.** "Research caching" has no answer. Ask what the person needs to find out
  or decide — at most three questions in one round, each with a recommended option
  first — and do not scaffold until the question is narrow enough to be answered. A
  sprawling document answering no question is worse than no document.

Do not ask what the codebase or the conversation can tell you. When the request is
already a concrete question, go straight on.

## 2. See what already exists

Find the research directory: `docs/research/` or `research/` if either exists, otherwise
`docs/research/` is created for you. Read the titles and research questions of what is
there.

- **Same question already answered, and still fresh.** Stop and tell the user which
  document it is. Do not write a duplicate.
- **Same question, but the answer has gone stale** (a `Revisit When` condition has been
  met, or it is old enough to doubt). Write a new document that lists the old one in
  `supersedes`, then edit the old one: `status: superseded`, add the new id to its
  `superseded-by`, and leave the rest of it alone — it records what was known then.
- **Related but different.** Put the old id in `related`. `related` also takes ADR ids
  (`ADR-0007`): list any accepted ADR that already governs the area under investigation,
  since the question may exist because of it.

## 3. Scaffold

```bash
uv run --no-project "$RESEARCH" new "Does the nightly export lose rows on failover?" --findings 3 --sources 4
```

The script numbers the document from the highest existing `NNNN-`, derives the slug
from the title, stamps today's date, refuses to overwrite, and prints the path it wrote.
Title it with the question or the topic, not the answer — you do not know the answer yet.
Keep the printed path; step 6 checks it.

The counts are only starting slots. Add or delete finding and source blocks as the work
demands; the checker needs the numbering contiguous (`F1, F2, F3`; `S1, S2, S3`), not
any particular count.

## 4. Investigate, writing as you go

Work the question with whatever is available — read the code and its history, read
documentation, search the web if you can, run an experiment. Record each source in
`## Sources` the moment you use it, with the date you accessed it, rather than
reconstructing the list at the end. Sources decay and move, and "accessed" is what lets
a later reader decide how much to trust a link.

Be honest about where a claim comes from, because the whole value of this document is
that someone can tell what was seen from what was assumed:

- A source is something you actually opened or ran. A fact you remember is not a source;
  if it matters, go and find the page, and if you cannot reach one, say so in Method and
  give that finding `Low` confidence. Never write a plausible URL or file path from memory.
- An experiment is a source too. Record what you ran and what it printed, in the finding,
  so it can be repeated.
- If a tool you needed was unavailable (no network, no access to the system under
  question), say that in Method. A reader who knows the web was not searched reads the
  document differently from one who assumes it was.

## 5. Fill it in

Replace every `<FILL: ...>` marker; the marker text is the instruction for that slot.
`reference/example-research.md` is a filled document written to be imitated.

| Section | What belongs there |
|---|---|
| Research Question | One question, answerable by looking. Keep the one you were given unless you narrowed it in step 1. |
| Context | What prompted it and what turns on the answer — without forecasting the ADRs or specs that will build on it. |
| Scope | What was investigated, and what a reader might assume was covered and was not. |
| Method | What was read, searched, run — and what was deliberately skipped. Enough to repeat it. |
| Findings | One claim each, `### F1 — claim (confidence: High)`, with the evidence and `[S1]`-style citations. |
| Open Questions | What you could not settle. `- None` only if that is true. |
| Conclusions | What the evidence supports and what it does not. A recommendation is fine; a decision is not. |
| Sources | `- S1 — [title](url or repo path) — primary / secondary / experiment / codebase — accessed 2026-03-10`. |
| Revisit When | What would make this stale: a version, a scale, a policy change. |

**Confidence is a promise about how far to lean on a finding.** `High` is directly
observed or stated by the authoritative source, and you would expect a rerun to agree.
`Medium` rests on one source, or on observation plus a reasonable inference. `Low` is
inference, a secondary source, or something you could not verify. When a finding mixes
observation and inference, say which part is which in the text — and give it the
confidence of the weaker part. Calling everything `High` makes the scale useless.

**Findings are claims, not topics.** "F2 — A failover kills the cursor and the export
fails" can be wrong; "F2 — Failover behaviour" cannot, so it tells the reader nothing.
Order findings so the ones that answer the question come first.

**Conclusions stop at the evidence.** Say what follows from the findings and how
confident the whole picture is. When the honest answer is "it depends" or "we could not
tell", write that, and put the missing piece in Open Questions. Where a choice has to be
made between options, name that and leave the choice to an ADR.

## 6. Check

Check the exact path step 3 printed:

```bash
uv run --no-project "$RESEARCH" check docs/research/0003-nightly-export-failover.md
```

Fix what it reports and run it again until it prints `ok`. Never report the document
finished while the check fails.

The checker sees shape: sections present and ordered, every finding carrying a confidence
and citing a source that exists, every listed source cited by something, dates real,
repository paths resolving. It cannot tell whether a citation supports the claim next to
it, or whether a confidence is earned. Re-read the Findings once with the checker green
and ask of each: does the cited source actually say this, and would I stake the stated
confidence on it?

## 7. Report

Give the path, the number of findings with their confidences, the open questions, and
anything the user should overturn — an assumption about scope, a source you could not
reach. If the question turned out to hide a decision, say so and name `create-adr` as
the next step, which will cite this document by id; if it was feeding a feature,
`draft-spec`, which cites it in the spec's Research section.

The document is left at `status: draft`. Change it to `complete` only when the user
confirms it. Then stop: do not write the ADR or the spec, and do not make the change the
research points at.

## Anti-patterns

- A research question that is a decision or a topic in disguise
- Sources recalled from memory, or links never opened
- A confidence applied to the whole document instead of to each claim
- Findings that restate the question, or topics where a claim belongs
- Conclusions that choose, or that go beyond what the findings support
- An empty Open Questions when something was clearly left unsettled
- Forecasting the ADRs or specs that will build on this document, or linking a spec
  (add ADR ids to `related` only for ADRs that exist)

## Maintaining the script

```bash
python3 -m unittest discover "$(dirname "$RESEARCH")"
```

The skeleton and the checker define the same format in two places. The tests pin them
together: a fresh skeleton must fail the check for its `<FILL: ...>` markers and nothing
else, and `reference/example-research.md` must pass. Change the format in
`scripts/research.py` and those two tests tell you what else moved.
