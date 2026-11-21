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

Research (`create-research`) sits upstream of the ADR: it establishes what is true, and the ADR records what was decided given that. An ADR cites the research documents it relies on by id (`RES-0004`), and once the ADR is accepted the cited document gets this ADR's id added to its own `related`, so the link can be followed from either end. Both ends are append-only records with stable ids, which is what makes a link in each direction safe; that does not extend to specs, which is why the spec side still does the citing.

This skill writes a **`proposed`** ADR and stops. A proposed ADR is not policy: `review-policy` ignores it, it is not registered in the agent instructions, and nothing else is edited on its account. Acceptance is a separate act by a person, through `approve-adr`, and it is what applies the supersede and research back-links, updates the index and registers the ADR. `refine-adr` sharpens a proposed ADR before then. Keeping the two apart means an ADR that is never accepted leaves nothing behind.

The format is held by `adr.py`, which sits in this skill's `scripts/` directory: `check` validates an ADR against the template, and it is what `refine-adr` and `approve-adr` gate on. Resolve it once:

```bash
ADR="<this skill's directory>/scripts/adr.py"
```

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
- **Conflict or supersede candidates** — any prior ADR that contradicts or is narrower than the new one. If the new decision replaces an earlier one, record it in the new ADR's `supersedes:` field only. Leave the old ADR exactly as it is: it stays `accepted`, and so stays binding, until the new one is accepted, at which point `approve-adr` marks it `superseded`.
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
  - `status`: always `proposed`, even if the user says the decision is already made — acceptance goes through `approve-adr`, which is also what registers it
  - `deciders`: names/handles of the people deciding, from the user's input — do not guess. If you do not have them, ask; `check` rejects an empty list
  - `supersedes` / `related`: ADR IDs you identified in Step 2. Leave `superseded-by` empty — it is filled in when a later ADR is accepted
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

### Step 7: Check the ADR

```bash
uv run --no-project "$ADR" check <ADR_FILE>
```

Fix whatever it reports — an unfilled placeholder, an empty `deciders` list, a cited research document that does not exist — before showing the ADR to anyone. Do not edit any other file: no back-links, no index entry, no change to the agent instructions. Those belong to acceptance (`approve-adr`).

### Step 8: Present for review

Show the user the completed ADR and ask whether the drivers, rejected alternatives, and consequences match their intent. Explicitly flag:

- Any section where you had to make assumptions due to limited information
- Any `supersedes` link, and that the older ADR stays binding until this one is accepted
- Which research documents the decision cites, and any driver that rests on a `Low`-confidence finding or on research still at `draft`

Say that the ADR is `proposed` and what comes next: `refine-adr` to sharpen it with a short interview, `approve-adr` to review and accept it. Do not run either unprompted, and do not change `status` yourself.
