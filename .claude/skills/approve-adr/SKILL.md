---
name: approve-adr
description: Review a proposed ADR in a fresh subagent on its substance — is there a real decision, are the drivers specific, are the rejected options real, are the consequences honest, could a diff be checked against its invariants, does it contradict an accepted ADR — then either accept it, which makes it binding policy and registers it where agents will read it, or send it back with blocking objections. Use only when the user explicitly asks to approve or accept an ADR, or to make it binding — "approve ADR 7", "accept this ADR", "make this decision policy". Accepting changes the ADR's status and edits the index and agent instructions, so do not use it to answer "is this ADR any good", to critique a draft, or to sharpen one (that is refine-adr). Reviews ADRs, not code.
argument-hint: The ADR to approve — path or number
---

# approve-adr

Read an ADR the way someone would who has to live under it, and decide whether it
should become policy. This is a judgment call about the decision record, not a lint
pass over the file — the checker already covers shape, and repeating its work here
wastes the one thing you bring that it cannot.

Approving means moving the ADR from `proposed` to `accepted`, and acceptance is what
makes it bind: `review-policy` holds code changes to accepted ADRs and reports a
violation as P1, and the registration this skill performs is how future agents learn
the ADR exists. A weak ADR that gets accepted is therefore expensive in a way a weak
draft is not. Two things follow: you must be able to say no, and you must never run
as an automatic step after writing. An author who approves their own work has not
been reviewed. The calling agent handles validation, status changes and registration;
a fresh subagent does the substance review without the conversation that wrote or
refined the ADR.

Run this only when the user has asked for the ADR to be approved or accepted. A request
for an opinion — "is this any good", "what do you think of ADR 7" — is not that: a
verdict here moves the status and edits other files. Answer it without running this
skill, or offer `refine-adr`, and ask whether they want the ADR accepted.

## The script

The format is owned by `adr.py` — the same script create-adr ships, with a copy in
this skill's own `scripts/` directory. Resolve it once:

```bash
ADR="<this skill's directory>/scripts/adr.py"
```

## 1. Find the ADR

Take the path or number you were given. If you were given neither, find the ADR
directory (`docs/adr/`, then `docs/decisions/`, then `docs/adrs/`), list each ADR with
its status and ask which — do not guess, and do not reach for the newest.

Resolve the ADR to an absolute path. The reviewer will read the whole file.

## 2. Gate on shape and readiness

```bash
uv run --no-project "$ADR" check --ready docs/adr/NNNN-slug.md
```

If it does not print `ok`, stop and report the failures. `--ready` holds the ADR to
what acceptance needs on top of a well-formed file: it is still `proposed`, every ADR
it supersedes is accepted, and every research document it cites is `complete`. An ADR
that fails these is not ready for a judgment call, and reviewing one spends your
attention on problems a script already found.

## Delegate the review

After the gate passes, start one read-only reviewer in a fresh context with no
inherited conversation. Give it only the absolute ADR path, repository root, and this
skill's path with instructions to perform steps 3–5 and return the verdict and
findings described at the end of step 5. It is the reviewer, not another invocation of the calling workflow: it must
not delegate again or run steps 1–2 or 6–7.

Do not pass the writing/refinement conversation, a parent-written summary, suggested
concerns, or an expected verdict. The reviewer discovers relevant ADRs, research and
code itself from the repository. Use the harness's controls to disable conversation
inheritance; spawning a subagent alone does not establish isolation. If the harness
cannot provide that context, stop and report that an isolated review is required. Do
not fall back to reviewing in the calling session.

Retain the ADR's exact contents at dispatch so you can check that the returned verdict
still applies. The reviewer reads files and searches only: it must not edit the ADR,
change its status, or execute project code.

## 3. Ground yourself — reviewer

Read the whole ADR before forming any opinion.

You cannot judge a decision against nothing. Gather first:

- **The accepted ADRs.** Read every one that touches the same area — by tag, by the
  code it governs, by `related` — including any the ADR did *not* name, which is where
  the interesting problems hide. Only `accepted` ones bind.
- **The research it cites.** Read the findings the drivers lean on, their confidence,
  and the document's `Revisit When`.
- **The code the decision governs.** Enough to tell whether the Context is true, whether
  the code already does this, and — the question that matters — whether accepting the
  ADR would put code that exists today in violation of its own policy. A few searches.
  You are testing the ADR's grip on reality, not auditing the codebase.

## 4. The questions — reviewer

One per section, and they are the whole review. Each asks whether the section is doing
its job — not whether it could be worded better.

| Section | The question | A real finding looks like |
|---|---|---|
| Context and Problem Statement | Is there a real decision to make, with forces specific to this project? | A problem asserted rather than evidenced; a choice the code already made, with no forces recorded. |
| Decision Drivers | Could a reader tell when a driver has shifted? | "Must be scalable"; a number nobody can source; a driver that only the chosen option satisfies. |
| Considered Options | Would a reasonable engineer propose each rejected option? | A strawman; the alternative the team is actually weighing missing; "do nothing" absent where it is real. |
| Decision Outcome | Do the drivers tip toward the chosen option, and would a changed driver flip it? | An outcome that restates the choice without saying why; a reason that is not among the drivers. |
| Consequences | Are the negatives real costs, and does each risk say how it would be noticed? | No negatives at all; a risk with no signal; a cost the Context contradicts. |
| Implementation Notes | Could a diff be checked against each invariant? | "Follow best practice"; an invariant no reviewer could cite a line against; existing code that already violates it, undisclosed. |
| Relationships | Does it contradict, duplicate or quietly narrow an accepted ADR? Is `supersedes` right? Does it lean on research that is `Low` confidence, or state a `Low` finding as fact? | An uncited ADR it conflicts with; a `supersedes` that retires more than the new ADR replaces; a driver resting on a `Low` finding written as settled. |

Two rules keep this honest. **A finding you would not hold up the decision for is not a
finding** — you are deciding whether this becomes policy, not polishing prose, and a
review that always finds something is a review nothing can ever pass. And **quote the
line you are objecting to**, so the author can disagree with something specific rather
than with your impression.

You are not asked whether you would have chosen differently. A decision between sound
options is the decider's to make; object only when the record does not support the
choice it records.

## 5. Reach a verdict — reviewer

Exactly one of three. The set is closed for the same reason the status vocabulary is: an
approver allowed to hedge will.

- **Approve** — make it policy. Nothing found that changes how a future reader would
  judge a change.
- **Send back** — one or more specific objections that change what the ADR says,
  including an assumption a future reader needs *in* the ADR: an accepted ADR cannot be
  revised, so a caveat that belongs in it must be written before acceptance. Say
  what must change, not how to word it. A conflict with an accepted ADR is this verdict:
  name the ADR and say whether the fix is to narrow this decision or to supersede that
  one.
- **Needs research first** — the choice hinges on facts nobody has established. Name the
  question and suggest `create-research`.

The last two leave the ADR at `proposed`.

Return the verdict, findings quoting the lines objected to, and what you checked it
against: the ADRs, research and code read. If you could not complete the review, report
that explicitly instead of choosing Approve.

## 6. Record it — calling agent

Wait for the reviewer to finish. A failed or incomplete review is not an approval.
Preserve its verdict and objections; do not substitute your own judgment or repair the
ADR to make it pass.

Before recording an approval, compare the ADR's current contents with what you
dispatched and check that its status is still `proposed`. If either changed, stop
without recording approval: the returned verdict does not approve a different revision.

Only for **Approve**:

```bash
uv run --no-project "$ADR" status docs/adr/NNNN-slug.md accepted
uv run --no-project "$ADR" backlinks docs/adr/NNNN-slug.md
```

`status` refuses on an ADR that fails `check --ready`, so a slip in step 2 cannot become
an approval; if it refuses, believe it. It moves `status` to `accepted` and `date` to
today, the day the ADR became policy, and changes nothing else. `backlinks` then marks
each ADR this one supersedes as `superseded` and names this one in its `superseded-by`,
and adds this ADR's id to the `related` of each research document it cites. It is safe
to run again if it is interrupted.

## 7. Make it discoverable — calling agent

Acceptance without registration binds nothing: an agent that never learns the ADR exists
cannot honour it. Do both of these only after step 6 has succeeded.

**The index.** If the ADR directory has an index (`README.md`, `INDEX.md`), add the new
ADR's entry in the existing format, and update the status of any ADR that `backlinks`
marked superseded. If there is no index and the directory now holds three or more ADRs,
offer to create a `README.md` listing them as `id | title | status | date`.

**The agent instructions.** Identify the repository's active instruction files from the
session and project configuration. Read root `AGENTS.md`, `AGENTS.override.md` and
`CLAUDE.md` when present, plus repository instruction files selected by configured paths
or globs. An override may replace a default file, and a configured file may be the only
entry point; updating an inactive default alone does not register the ADR for that agent.

Update the effective repository entry point for each supported agent. Where several
active files already share a repository-owned instruction document through imports or
links, update that shared document and preserve the existing discovery paths. Otherwise
update each active root entry point, including an override that replaces `AGENTS.md`.
Follow actual import semantics rather than inventing them for a plain Markdown file. Do
not modify user-global instructions outside the repository. If there is no repository
instruction file or configured convention, create a root `AGENTS.md`. Report which files
you updated.

In each selected file, look for an existing `## Architectural Decisions` section.

**If the section exists**, add a row to its table for this ADR in the existing format.
Remove the row of any ADR `backlinks` marked superseded — it no longer binds.

**If it does not exist**, append it to the end (substituting the real ADR directory):

```markdown
## Architectural Decisions

ADRs live in `<adr-dir>/`. Read the relevant ADRs before proposing architectural changes — they encode constraints and rejected alternatives. When writing or modifying a spec, cite the ADRs that constrained it in the spec itself; ADRs do not track their downstream consumers. An ADR does name the research it rests on (its `research` frontmatter) — read that before overturning a decision.

| ADR | When this applies |
|---|---|
| `<adr-dir>/<NNNN-slug>.md` | <1-line description of the area/situation this decision governs> |
```

The "When this applies" column describes the *situation* that should make a future agent
read the ADR, not the title restated. Good: "Any change to event persistence, replay
semantics, or write-path latency in `services/ledger/`". Bad: "When using the event store".

## 8. Report — calling agent

Relay the reviewer's report. Lead with the verdict. Then the findings, each quoting its
line, ordered by how much they change how the ADR would be read. Then what the reviewer
checked it against — which ADRs and research it read, what code it looked at — so the
user can judge whether its grounding was thin. If you recorded approval, say that the
status moved and list the files `backlinks`, the index and the registration touched.

## Follow-up after a rejection — calling agent

Revise the proposed ADR in a separate authoring step only if the user asks for
fixes. If they explicitly ask to fix and retry, run at most one follow-up
approval from step 1 on the revised ADR, even if they ask you to keep going
until it passes. Use a fresh reviewer and keep the earlier findings in the
calling context.

Stop for the user's decision before revising if a blocker requires changing
the decision, narrowing or superseding binding policy, or gathering new research.
If the follow-up still blocks, stop and report which objections were resolved,
which recur, and which are new. State what remains necessary for approval.
Leave the ADR at `proposed` and wait for further direction before making
more edits or dispatching another review.

## Anti-patterns

- Re-reporting what `check` already said; that is the script's job, not yours
- Wording notes, section-ordering notes, "consider adding" notes — none of these change
  how a future reader judges a change
- Objecting because you would have chosen another option; object only when the record
  does not support the choice it records
- Approving with a list of caveats attached; if the caveats belong in the ADR, send it
  back, since it cannot be revised once accepted
- Sending back over something the ADR's own Consequences already disclose — the author
  flagged it, which is the system working
- Editing the ADR's text during a review, cutting a branch, filing issues, or
  starting the work the decision implies
- Registering or indexing an ADR that was not accepted
- Running as a step after create-adr rather than because a person asked
- Reviewing in the writing conversation, or forwarding that conversation to the reviewer
  through inheritance or a summary
- Overriding a reviewer verdict or treating a failed review as approval
