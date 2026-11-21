---
name: refine-adr
description: Refine a Proposed ADR by interviewing its decider about its rough edges — drivers that are platitudes, rejected options that are strawmen, consequences with no real downside, invariants nobody could check a diff against, claims resting on Low-confidence research — and writing the answers back into the ADR. Asks at most four targeted questions, each with a recommended answer, and leaves the ADR at proposed. Use whenever a drafted ADR should be sharpened before it is accepted — "refine this ADR", "grill me on ADR 7", "what's weak in this decision record", "tighten this up before approval". Not for writing an ADR (create-adr), accepting one (approve-adr), or one already accepted.
argument-hint: The ADR to refine — path or number
---

# refine-adr

Find the places a proposed ADR leaves a future reader guessing, ask the person who
owns the decision to settle them, and write what they decide back into the record.
The questions are the work: an ADR is a set of reasons, and the thin parts are
reasons nobody has given yet — or gave only in the author's head.

The process this serves is **research → ADR → spec**. This skill sits between
writing an ADR and accepting it. It makes the record better; it does not judge
whether the decision is right (that is `approve-adr`, and ultimately the decider)
and it does not move the ADR off `proposed`. Run it because a person asked, not as
an automatic step after `create-adr`.

It matters more here than for most documents: once accepted, an ADR is append-only
policy that agents hold code to. This is the last cheap chance to fix it.

## The script

The format is owned by `adr.py` — the same script create-adr ships, with a copy in
this skill's own `scripts/` directory. Resolve it once:

```bash
ADR="<this skill's directory>/scripts/adr.py"
```

## 1. Find the ADR and gate on it

Take the path or number you were given. If you were given neither, find the ADR
directory (`docs/adr/`, then `docs/decisions/`, then `docs/adrs/`), list each ADR
with its status and ask which — do not guess, and do not reach for the newest.

```bash
uv run --no-project "$ADR" status docs/adr/NNNN-slug.md
uv run --no-project "$ADR" check docs/adr/NNNN-slug.md
```

Two reasons to stop and say so:

- **Not `proposed`.** An accepted ADR is append-only history; editing it quietly
  rewrites policy that code has been reviewed against. Say so, and let the user
  decide whether the change belongs in a new ADR that supersedes it.
- **`check` fails.** An ADR with unfilled placeholders or an empty `deciders` list
  is create-adr's problem. Report the failures instead of interviewing someone
  about a half-filled file.

Read the whole ADR before forming any question.

## 2. Ground yourself

Facts are your job, never the user's. Before asking anything, look up what the
repository can tell you:

- **Other ADRs** — read every accepted one that touches the same area, cited in
  `related` or not. Overlap and conflict hide in the ones the author did not name.
- **The research it cites** — the findings behind each driver, their confidence,
  the document's `status`, and its `Revisit When` conditions.
- **The code the decision governs** — enough to tell whether a claim in Context or
  Consequences is true, and whether the code already does what the ADR decides. A
  few searches, not a survey.

A question the code, an ADR or the research answers is not a question. Either the
ADR already agrees, in which case leave it alone, or it contradicts them, in which
case that is a finding to put to the user, not an ambiguity to ask about.

## 3. Scan for weakness

Read the ADR against these lenses, and note — privately — where each is sound, thin,
or missing. Do not print the map.

| Lens | Ask of the ADR |
|---|---|
| Choice | Is the decision stated as something a change can be checked against, or only as a direction? |
| Drivers | Is each driver specific to this project — a number, a named constraint, a path — or a platitude ("scalable", "maintainable")? If one shifted, could a reader tell? |
| Options | Is each rejected option one a reasonable engineer would propose? Is there a stated reason it lost, tied to a driver? Is "do nothing" missing where it is a real candidate? |
| Outcome | Does it say which drivers tipped the balance, or only restate the choice? |
| Consequences | Are the negatives real costs, and does each risk say how it would be noticed? An ADR with no downside has not been thought through. |
| Invariants | What must future code do, or never do? Could `review-policy` cite a line here to flag a violation? |
| Scope | Which part of the system does it govern, and what would a reader wrongly assume it covers? |
| Conflicts | Does it contradict, duplicate or quietly narrow an accepted ADR — and should it name that one in `supersedes` or `related`? |
| Evidence | Does a driver or consequence rest on a `Low`-confidence finding, a `draft` or `superseded` research document, or a claim nobody checked? |
| Revisit | What would make it stale, and would anyone notice? |

For each thin or missing lens, ask whether settling it would **change what a future
reader decides when judging a change**. If not, it is polish, and polish is not
worth the user's attention. Drop questions about wording, section order and
formatting; those belong to the editor.

Rank what is left by impact times uncertainty: how badly a reader goes wrong if the
gap stays, multiplied by how little the ADR says. Keep the top four.

If nothing survives, say the ADR has no weakness worth an interview and stop. Do not
invent questions to look useful.

## 4. Ask

At most **four questions in total** across the whole session. Work them in rounds: a
round is every question whose answer does not depend on another still-open question.
A question that only makes sense once another is answered waits for a later round.
Where a round holds several questions, put them to the user together, in one
interaction.

Write every question the same way:

- A full interrogative the user can answer from that line alone — never a section
  name standing in for a question.
- One sentence on **why it matters**: what a future reader would get wrong, or what
  two readers would conclude differently, if it stays open.
- A **recommended answer**, with a line of reasoning, stated first. Word the question
  so that "yes" accepts it.
- Either two to four mutually exclusive options, or a short free-form answer. Never
  an essay prompt.

```
Q1 — Does this decision apply to the batch importer, or only to the request path?
Why it matters: the Decision Outcome says "all writes", and the importer writes
200k rows a night; a reader will either exempt it or flag it.
Recommended: Request path only — the 30 ms driver does not apply to the importer.
  A. Request path only; the importer is out of scope
  B. Everything, importer included
  C. Everything, with the importer allowed a documented bulk path
```

Take the user's reply as the answer: "yes" or "recommended" means the recommendation,
a letter means that option, anything else is their own answer. If an answer is
ambiguous, ask once more for the same question — a retry is not a new question. If
the user says "done" or "enough", stop asking at once.

Do not ask a question you could look up, do not ask about a lens whose answer you can
already read in the ADR, and do not reveal questions you have not yet asked.

A user who answers "your call" or "whatever you think" has delegated the choice, not
made it. Use your recommendation, but write it in as a stated assumption — in
Context or Consequences, wherever a reader would otherwise take it for a settled
fact — so the next reader can see it was a guess.

**The decision itself is not yours to open.** If the scan suggests the wrong option
won, say that plainly as your own view and ask whether to change it. Never move the
outcome as a side effect of an answer to a smaller question.

## 5. Write the answers back

After each round, edit the ADR so the answer lives where a reader will look for it.

| The answer settles… | Write it into… |
|---|---|
| What the decision does or does not cover | Context and Problem Statement, or Decision Outcome, in a sentence a reader cannot miss |
| A driver that was vague | Replace it with the number, constraint or path; reword any option or outcome that leaned on the old wording |
| Why an option lost | Considered Options, and that option's Pros and Cons, tied to the driver it failed |
| An option that was missing | Considered Options and Pros and Cons, with a reason it lost — adding it is not re-deciding unless it wins |
| A cost or risk | Consequences, with how it would be noticed |
| A rule future code must follow | Implementation Notes, phrased so a diff can be checked against it |
| A guess that was listed | Remove the hedge and state the fact where it applies; keep it only if still unconfirmed |
| A relationship to another ADR | `related`, or `supersedes` (the old ADR is only marked superseded when this one is accepted) |
| A term | One name, everywhere, replacing the other |

Four rules keep the edit honest:

- **Replace, never accumulate.** If the answer contradicts an earlier line, change
  that line. An ADR that says both the old and the new rule is worse than either.
- **Touch only what an answer settled.** Do not reword for taste, reorder sections or
  tidy things nobody asked about. The user is reviewing a diff caused by their
  answers.
- **If the chosen option changes, change everything that rested on it.** Decision
  Outcome, Consequences, Pros and Cons and Implementation Notes must agree before
  you run `check`; a half-reversed ADR is the worst result of this skill.
- **No forward references.** Do not write spec, plan or task paths into an ADR; specs
  cite ADRs, never the reverse, because specs are renamed and archived.

Do not add sections. The template's sections are the format; a project's own extra
section is the author's to keep, not yours to add.

Run the check after every write:

```bash
uv run --no-project "$ADR" check docs/adr/NNNN-slug.md
```

Fix what it reports before the next round. Never leave the ADR failing the check.
Do not touch `status` or `date` — they stay as they are, and moving them is
`approve-adr`'s job. Do not fill `deciders` from a guess; it is the user's to name.

## 6. Report

Give the path and what changed:

- Each question with the answer that was settled on, one line apiece
- The sections touched
- Anything recorded as an assumption because the user delegated it
- **Deferred**: weaknesses you ranked but did not ask because the limit was reached,
  or because the user stopped early, each with a line on why it matters. The user
  can run the skill again to take them on
- Anything the answers contradicted — an accepted ADR, a cited finding — and what you
  did about it

Say the ADR is still `proposed` and that `approve-adr` is the next step if they want
it reviewed and accepted. Then stop. Do not accept it, do not register it, and do not
start any work the decision implies.

## Anti-patterns

- Asking what another ADR, the research or the code already answers
- Asking about wording, section order or formatting
- More than four questions, or inventing the last ones to use the allowance
- Questions that are labels ("Consequences?") rather than something answerable
- Questions with no recommended answer, or an open essay prompt
- Judging whether the decision is right, or reversing it as a side effect of a smaller
  answer; that is the decider's call, and `approve-adr`'s question
- Adding a section, or a spec or task path, to an ADR
- Leaving the old text beside the new answer
- Rewording, reordering or tightening prose that no answer touched
- Moving the status or date, or running approve-adr as a follow-on step
