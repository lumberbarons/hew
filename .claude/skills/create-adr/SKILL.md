---
name: create-adr
description: Creates an Architectural Decision Record (ADR) in MADR format, researching the codebase and prior ADRs to fill in real context, alternatives, and consequences. Use when the user asks to record, log, write, capture, or document an architectural decision, design choice, technology selection, trade-off, or ADR — including phrases like "make an ADR", "write this up as a decision", or "capture why we picked X". Also invoke proactively when the conversation is clearly settling a non-trivial architectural question (framework selection, data model choice, protocol trade-off, sequencing of a migration) and no ADR has been drafted yet — in that case, offer to record it before the thread moves on.
---

# Create ADR

## User Input

The user's request describes the decision being recorded (e.g., "use Postgres for the event store", "adopt trunk-based development", "replace custom retry wrapper with Temporal").

If the request does not state a decision, ask the user what decision they want to record — specifically, what was chosen and what alternatives were considered. Do not proceed without at least a decision statement.

## Orientation

ADRs are **standalone policy**. Each one records a constraint and the reasoning behind it; it does not track the downstream specs, plans, or tasks that inherit that constraint. The citation direction runs the other way: a spec that is shaped by an ADR names that ADR in its own frontmatter. The link lives on the churning side (specs get renamed, split, archived), not the stable side (accepted ADRs are append-only history). Once an ADR is accepted you do not revise it when downstream artifacts move around.

The primary reader of an ADR in this project is another AI agent picking up the codebase weeks or months from now. That agent will decide whether a proposed change respects or violates the decision, and whether a driver has shifted enough that the ADR should be revisited. Write for that reader: state the drivers precisely enough that an agent can evaluate "does this still hold?", and name the rejected options so the agent doesn't re-propose them.

Use MADR format with rich frontmatter. Link to other ADRs via `supersedes`, `superseded-by`, and `related`, and to the research the decision rests on via `research` — those edges are stable and directionally correct. Do not embed paths to specs, plans, or task files; those are forward-references that rot.

Research (`create-research`) sits upstream of the ADR: it establishes what is true, and the ADR records what was decided given that. An ADR cites the research documents it relies on by id (`RES-0004`), and the cited document gets this ADR's id added to its own `related`, so the link can be followed from either end. Both ends are append-only records with stable ids, which is what makes a link in each direction safe; that does not extend to specs, which is why the spec side still does the citing.

## Workflow

### Step 1: Determine the ADR directory

Check the target project for an existing ADR directory, in this order:

1. If `docs/adr/` exists, use it
2. If `docs/decisions/` exists, use it
3. If `docs/adrs/` exists, use it
4. If none exist, ask the user where ADRs should live, defaulting to `docs/adr/`

### Step 2: Read existing ADRs

If the directory already contains ADRs, read them before drafting. You need to know:

- **Already-documented decisions** — if an existing ADR already covers the same choice at the same scope, stop and tell the user which ADR it is. Do not create a duplicate. If the user's intent is actually to *change* the prior decision, that's a supersede flow (see below), not a new ADR.
- **Conflict or supersede candidates** — any prior ADR that contradicts or is narrower than the new one. If the new decision replaces an earlier one, you will record that in both the new ADR's `supersedes:` field and the old ADR's `superseded-by:` field + status change to `superseded`.
- **Related decisions** — prior ADRs that the new one depends on or complements. Capture these in `related:`.
- **House style** — deciders convention, tag vocabulary, any project-specific sections.

Then look for research. Find the research directory (`docs/research/` or `research/`) and read the titles and research questions of what is there. A document that investigated this question, or the options on the table, is the strongest source for the Context, Drivers and Consequences you are about to write, so read it before researching the codebase yourself in Step 5.

- **Cite only research whose `status` is `complete`.** A `draft` is unconfirmed; if it is the only research there is, say so in your report rather than resting the decision on it.
- **Do not cite `superseded` research.** Follow its `superseded-by` to the document that replaced it.
- **Weigh the confidence of the findings you lean on.** A driver that rests on a `Low` finding should be written as an assumption, not as fact.
- **Check `Revisit When`.** If a condition in it has been met, the research is stale. Tell the user and suggest `create-research` to refresh it before the ADR leans on it.
- **A decision with no research behind it is fine** when the question is simple or already well understood. Write `research: []`. If the choice hinges on facts nobody has established yet, say that in your report and offer `create-research` first, rather than inventing the evidence.

### Step 3: Generate the slug and title

Derive a kebab-case slug from the decision statement. The slug should name the *choice*, not the problem.

Examples:
- "use Postgres for the event store instead of DynamoDB" → slug `use-postgres-event-store`, title `Use Postgres for the event store`
- "adopt trunk-based development with short-lived feature flags" → slug `adopt-trunk-based-dev`, title `Adopt trunk-based development`
- "replace the custom retry wrapper with Temporal workflows" → slug `replace-retry-wrapper-with-temporal`, title `Replace retry wrapper with Temporal workflows`

Prefer verb-first, imperative. Keep the slug concise; the title can be slightly more descriptive but should still fit in a commit-message line.

### Step 4: Scaffold the ADR

The scaffold script requires `bash` and `jq` on PATH. Check both before running
it; if a dependency is missing, report it before creating the ADR file.

Run the init script to create the file from the template. The script auto-numbers the ADR by scanning the target directory for the highest existing `NNNN-*.md` and incrementing. It sits in `scripts/` beside this file — resolve it once against this skill's own directory rather than hardcoding a path, which breaks the moment the skill is vendored into another repo or installed elsewhere:

```bash
INIT_ADR="<this skill's directory>/scripts/init-adr.sh"
bash "$INIT_ADR" <slug> <adr-dir> "<title>"
```

This outputs JSON with `ADR_FILE`, `ADR_DIR`, `ADR_ID`, and `ADR_NUMBER`. Use `ADR_FILE` for all subsequent edits.

### Step 5: Research the codebase

Before filling in the template, investigate the project to ground the ADR in reality:

- **Code paths the decision affects** — modules, services, or directories that will change
- **Existing specs and plans** — scan `docs/specs/`, `specs/`, spec-kit artifacts, `plan.md`, `tasks.md` for context on how the decision will land in practice, but do **not** record their paths in the ADR; forward-references to specs rot as specs are renamed or archived
- **Configuration and infra** — Dockerfiles, Terraform, CI pipelines, package manifests
- **Prior discussion artifacts** — commit messages, PR descriptions, existing docs on the topic
- **Operational realities** — observability, runbooks, deployment model that constrain viable options

> [!IMPORTANT]
> Decision Drivers must be **specific to this project**, not generic engineering platitudes. "Must be fast" is not a driver. "p99 read latency budget is 30 ms under 10k QPS because of the checkout path in `services/checkout/`" is a driver. If you cannot find specifics from the codebase or user, ask — do not invent numbers.

### Step 6: Fill in the ADR

Edit the scaffolded file, replacing every placeholder with project-specific content:

- **Frontmatter**:
  - `status`: start as `proposed` unless the user explicitly says the decision is already made
  - `deciders`: names/handles of the people deciding, from the user's input — do not guess
  - `supersedes` / `superseded-by` / `related`: ADR IDs you identified in Step 2
  - `research`: the `RES-NNNN` IDs of the research documents this decision rests on, from Step 2. Ids only, not paths
  - `tags`: short nouns (e.g., `storage`, `observability`, `build`) — reuse vocabulary from prior ADRs
- **Context and Problem Statement**: the forces at play, grounded in the codebase research and any cited research documents
- **Decision Drivers**: specific, measurable where possible
- **Considered Options**: include the chosen option plus at least one real alternative; "do nothing" is a valid option if relevant
- **Decision Outcome**: which option won and *which drivers tipped the balance* — a reader should be able to tell whether changing a driver would invalidate this decision
- **Consequences**: honest positives, negatives, and risks; if there are no negatives, you haven't thought hard enough
- **Pros and Cons of the Options**: per-option analysis, including the rejected ones
- **Implementation Notes**: how the decision cascades into code and operational policy — modules or services affected, invariants future changes must respect, and leading indicators that would prompt revisiting.
- **References**: links to discussions, PRs, issues, and prior ADRs, plus a markdown link to each cited research document with a few words on what it established. Where a driver or consequence rests on a specific finding, name it in the text (`RES-0004 F2`) so a later reader can check the claim at its source

> [!NOTE]
> If the request or the conversation does not provide enough material for Decision Drivers or Consequences, stop and ask the user. An ADR with hand-wavy drivers and made-up consequences is worse than no ADR.

### Step 7: Update back-links

**Supersede links.** If the new ADR supersedes an earlier one (captured in its `supersedes:` frontmatter), edit the superseded ADR:

1. Set its `status:` to `superseded`
2. Add the new ADR's ID to its `superseded-by:` list
3. Do **not** rewrite the old ADR's content — the historical record matters

**Research links.** For each id in the new ADR's `research`, edit that research document's frontmatter and add this ADR's id to its `related` list. Change nothing else in it — not its status, findings, or text — since it records what was known when it was written. Superseded research should not have been cited (Step 2).

### Step 8: Update the ADR index (if one exists)

Check if the ADR directory has an index file (e.g., `README.md`, `INDEX.md`). If so, add an entry for the new ADR following the existing format. If the directory has no index and already contains 3+ ADRs, offer to create a `README.md` index listing all ADRs with `id | title | status | date`.

### Step 9: Register the ADR in the project's agent instructions

> [!IMPORTANT]
> This step is critical. ADRs only bind future work if agents and developers can discover them. Register the ADR in the repository instructions that future sessions actually load; filenames and precedence depend on the running agent and the project's configuration.

Identify the repository's active instruction files from the session and project configuration. Read root `AGENTS.md`, `AGENTS.override.md`, and `CLAUDE.md` when present, plus repository instruction files selected by configured paths or globs. An override may replace a default file, and a configured file may be the only entry point; updating an inactive default alone does not register the ADR for that agent.

Update the effective repository entry point for each supported agent. Where several active files already share a repository-owned instruction document through imports or links, update that shared document and preserve the existing discovery paths. Otherwise update each active root entry point, including an override that replaces `AGENTS.md`. Follow actual import semantics rather than inventing them for a plain Markdown file. Do not modify user-global instructions outside the repository. If there is no repository instruction file or configured convention, create a root `AGENTS.md` and register the ADR there; report which files you updated.

In each selected file, look for an existing `## Architectural Decisions` section.

**If the section already exists**, add a new row to the table for the ADR you just created, following the existing format.

**If the section does not exist**, append it to the end using this format (substitute the actual ADR directory):

```markdown
## Architectural Decisions

ADRs live in `<adr-dir>/`. Read the relevant ADRs before proposing architectural changes — they encode constraints and rejected alternatives. When writing or modifying a spec, cite the ADRs that constrained it in the spec itself; ADRs do not track their downstream consumers. An ADR does name the research it rests on (its `research` frontmatter) — read that before overturning a decision.

| ADR | When this applies |
|---|---|
| `<adr-dir>/<NNNN-slug>.md` | <1-line description of the area/situation this decision governs> |
```

The "When this applies" column should describe the *situation* that should make a future agent read the ADR — not just restate the title. Good example: "Any change to event persistence, replay semantics, or write-path latency in `services/ledger/`". Bad example: "When using the event store".

### Step 10: Present for review

Show the user the completed ADR and ask whether the drivers, rejected alternatives, and consequences match their intent. Explicitly flag:

- Any section where you had to make assumptions due to limited information
- Any supersede links you applied to prior ADRs
- Which research documents the decision cites, and any driver that rests on a `Low`-confidence finding or on research still at `draft`

Only change `status:` from `proposed` to `accepted` after the user confirms — do not do it preemptively.
