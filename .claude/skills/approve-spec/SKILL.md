---
name: approve-spec
description: Review a drafted feature spec on its substance — is it worth doing now, is the scope honest, are the stories real, could a stranger falsify the done-when list, does it contradict a recorded decision — then either move it to Accepted or send it back with blocking objections. Use whenever a spec needs approving, accepting, reviewing or sanity-checking before the work starts — "approve this spec", "review spec 009", "is this spec any good", "can we build from this", "sign this off". Reviews specs, not code.
argument-hint: The spec to approve — path or number
---

# approve-spec

Read a spec the way someone would who has to live with the thing it describes,
and decide whether the work should start. This is a judgment call about the
feature, not a lint pass over the file — the checker already covers shape, and
repeating its work here wastes the one thing you bring that it cannot.

Approving means moving the spec from `Draft` to `Accepted`, which is a claim
that the feature is worth building as written. You can make that claim because
a person invoked you deliberately to make it. Two things follow: you must be
able to say no, and you must never run as an automatic step after drafting. An
author who approves their own work has not been reviewed.

## The script

The format is owned by `lightspec.py` — the same script draft-spec uses, with a
copy in this skill's own `scripts/` directory. Resolve it once:

```bash
LIGHTSPEC="<this skill's directory>/scripts/lightspec.py"
```

## 1. Find the spec

Take the path or number you were given. If you were given neither, list what is
under `specs/` with each one's current status and ask which — do not guess, and
do not reach for the newest.

Read the whole spec before forming any opinion.

## 2. Gate on shape

```bash
uv run --no-project "$LIGHTSPEC" check specs/NNN-slug/spec.md
```

If it does not print `ok`, stop and report the failures. A spec that is not yet
well-formed is not ready for a judgment call, and reviewing one spends your
attention on problems a script already found.

Check the status while you are there — `Draft` is the only thing you can
approve:

```bash
uv run --no-project "$LIGHTSPEC" status specs/NNN-slug/spec.md
```

An `Accepted` spec has already been through this. Say so and stop, rather than
re-approving something to look busy.

## 3. Ground yourself

You cannot judge a spec against nothing. Two things to gather first:

- **The recorded decisions.** `ls docs/adr docs/decisions docs/adrs 2>/dev/null`,
  or `find . -ipath "*adr*" -name "*.md"`. Read the accepted ones bearing on
  this feature — including any the spec did *not* cite, which is where the
  interesting problems hide.
- **The code the spec claims to touch.** Enough to tell whether the tasks are
  plausible and whether something obvious is missing. A few searches. You are
  testing the spec's grip on reality, not auditing the codebase.

## 4. The questions

One per section, and they are the whole review. Each asks whether the section is
doing its job — not whether it could be worded better.

| Section | The question | A real finding looks like |
|---|---|---|
| Description | Is this worth doing **now**? | The stated reason is decoration, or the problem is asserted rather than evidenced. |
| Prior Decisions | Does it contradict an accepted decision, or quietly make one nobody recorded? | An ADR that constrains this and went uncited; a choice whose consequences outlive this feature, made in passing. |
| Research | Does a story rest on a claim the cited research does not support, or rates `Low`? | A cited finding that says less than the spec assumes; a `draft` or `superseded` document; a factual premise in the Description that no research backs. `- None` is fine unless the spec turns on a fact nobody established. |
| Out of Scope | Is the boundary defensible, or drawn to make the spec easy? | Something excluded that the feature does not work without. |
| Assumptions | Which assumption, if wrong, invalidates the spec — and is it written down? | The load-bearing guess is missing while three harmless ones are listed. |
| User Stories | Is P1 actually what makes this worth shipping? Is a story secretly three? | A P1 nobody would ship alone; a story whose "and" hides a second feature. |
| Done When | Could a stranger falsify each line without reading the diff? | An item that restates a task, or that only its author could evaluate. |

Two rules keep this honest. **A finding you would not hold up the work for is
not a finding** — you are deciding whether to start, not polishing prose, and a
review that always finds something is a review nothing can ever pass. And
**quote the line you are objecting to**, so the author can disagree with
something specific rather than with your impression.

## 5. Reach a verdict

Exactly one of four. The set is closed for the same reason the status vocabulary
is: an approver allowed to hedge will.

- **Approve** — build from it. Nothing found that changes what gets built.
- **Approve, assumptions on record** — worth building, but name the assumptions
  the author is taking on. For when the risk is real but the author's to carry.
- **Send back** — one or more specific objections that change what gets built.
  Say what must change, not how to word it.
- **Needs an ADR first** — the spec is quietly deciding something whose
  consequences outlive it. Name the decision and suggest recording it before the
  spec is worth finishing; the create-adr skill (decisions plugin) if available.

The last two leave the spec at `Draft`.

## 6. Record it

Only for the two approving verdicts:

```bash
uv run --no-project "$LIGHTSPEC" status specs/NNN-slug/spec.md Accepted
```

The script refuses this on a spec that fails `check`, so a slip in step 2 cannot
become an approval. If it refuses, believe it.

Do not edit the spec as part of approving. Fixing the objections you just raised
makes you the author again, and then nobody has reviewed the result. If the user
wants those changes made, that is a separate ask, and re-approving is a fresh
invocation of this skill.

## 7. Report

Lead with the verdict. Then the findings, each quoting its line, ordered by how
much they change what gets built. Then what you checked it against — which ADRs
you read, what code you looked at — so the user can judge whether your grounding
was thin. If you approved, say the status moved.

## Anti-patterns

- Re-reporting what `check` already said; that is the script's job, not yours
- Wording notes, section-ordering notes, "consider adding" notes — none of these
  change what gets built
- Approving with a list of caveats attached; if the caveats matter, send it back
- Sending back over something the Assumptions section already discloses — the
  author flagged it, which is the system working
- Editing the spec, cutting a branch, filing issues, or starting the work
- Running as a step after draft-spec rather than because a person asked
