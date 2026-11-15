---
name: refine-spec
description: Refine a Draft feature spec by interviewing the author about its rough edges — vague terms, unstated edge cases, stories that disagree with the done-when list, load-bearing assumptions nobody confirmed — and writing the answers back into the spec. Asks at most five targeted questions, each with a recommended answer, and leaves the spec at Draft. Use whenever a drafted spec should be sharpened before review — "refine this spec", "clarify spec 009", "grill me on this spec", "what's unclear in this spec", "tighten this up before approval". Not for writing a spec (draft-spec), approving one (approve-spec), or a spec already Accepted.
argument-hint: The spec to refine — path or number
---

# refine-spec

Find the places a Draft spec leaves a reader guessing, ask the person who owns it
to settle them, and write what they decide back into the spec. The questions are
the work: a spec is a set of decisions, and the unclear parts are decisions
nobody has made yet — or made only in the author's head.

The process this serves is **research → ADR → spec → approve → implement**. This
skill sits between drafting and approving. It makes the spec better; it does not
judge whether the feature is worth building (that is `approve-spec`) and it does
not move the spec off `Draft`. Run it because a person asked, not as an
automatic step after drafting.

## The script

The format is owned by `lightspec.py` — the same script draft-spec uses, with a
copy in this skill's own `scripts/` directory. Resolve it once:

```bash
LIGHTSPEC="<this skill's directory>/scripts/lightspec.py"
```

## 1. Find the spec and gate on it

Take the path or number you were given. If you were given neither, list what is
under `specs/` with each one's status and ask which — do not guess, and do not
reach for the newest.

```bash
uv run --no-project "$LIGHTSPEC" status specs/NNN-slug/spec.md
uv run --no-project "$LIGHTSPEC" check specs/NNN-slug/spec.md
```

Two reasons to stop and say so:

- **Not `Draft`.** An `Accepted` spec has been signed off as written; editing it
  quietly voids the review. Say so, and let the user decide whether it should be
  re-drafted.
- **`check` fails.** A spec that is not yet well-formed is draft-spec's problem.
  Report the failures instead of interviewing someone about a half-filled file.

Read the whole spec before forming any question.

## 2. Ground yourself

Facts are your job, never the user's. Before asking anything, look up what the
repository can tell you:

- **Recorded decisions** — `ls docs/adr docs/decisions docs/adrs 2>/dev/null`.
  Read the accepted ones that bear on the feature, cited or not.
- **Research** — `ls docs/research research 2>/dev/null`, and the findings the
  spec cites.
- **The code the tasks name** — enough to tell whether a question has an answer
  already sitting in the codebase. A few searches, not a survey.

A question the code or an ADR answers is not a question. Either the spec already
agrees with it, in which case leave it alone, or the spec contradicts it, in
which case that is a finding to put to the user, not an ambiguity to ask about.

## 3. Scan for ambiguity

Read the spec against these lenses, and note — privately — where each is clear,
thin, or missing. Do not print the map.

| Lens | Ask of the spec |
|---|---|
| Boundary | Is there something a reader would assume is in scope that Out of Scope neither includes nor excludes? |
| Roles | Is "user" one person or several, and do the stories treat them differently where they should? |
| Edge cases | For each story, what happens on empty, duplicate, concurrent, unauthorised, or failed? Is the one that matters named? |
| Data and lifecycle | What are the entities, what makes one unique, what states can it be in, how much of it is there? |
| Vague words | Which adjectives and adverbs ("fast", "simple", "recent", "large") carry weight without a number or a rule? |
| Assumptions | Which assumption, if wrong, rewrites the stories — and is it a guess the author made or something confirmed? |
| Agreement | Does every story have done-when evidence, every done-when trace to a story, every task serve a story? Do two sections contradict each other? |
| Falsifiability | Could a stranger fail each Done When item without reading the diff? |
| Terms | Is one thing called two names, or two things one? |
| Decisions | Does the spec quietly choose something that deserves a recorded decision, or lean on a `Low`-confidence finding? |

For each thin or missing lens, ask whether settling it would **change what gets
built or how it is tested**. If not, it is polish, and polish is not worth the
user's attention. Drop questions about technology choices, task ordering, and
wording; those belong to the implementer or the editor.

Rank what is left by impact times uncertainty: how much rework a wrong guess
costs, multiplied by how little the spec says. Keep the top five.

If nothing survives, say the spec has no ambiguity worth an interview and stop.
Do not invent questions to look useful.

## 4. Ask

At most **five questions in total** across the whole session. Work them in
rounds: a round is every question whose answer does not depend on another
still-open question. A question that only makes sense once another is answered
waits for a later round. Where a round holds several questions, put them to the
user together, in one interaction.

Write every question the same way:

- A full interrogative the user can answer from that line alone — never a
  section name or a task id standing in for a question.
- One sentence on **why it matters**: what breaks, or what two readers would
  build differently, if it stays open.
- A **recommended answer**, with a line of reasoning, stated first. Word the
  question so that "yes" accepts it.
- Either two to four mutually exclusive options, or a short free-form answer.
  Never an essay prompt.

```
Q1 — Does a user who loses access to a watched product keep it on the list?
Why it matters: US2 sorts the list by price drop, and a product the user can no
longer see has no current price to compare.
Recommended: Yes, shown as unavailable — vanishing silently reads as a bug.
  A. Keep it, marked unavailable
  B. Remove it automatically
  C. Keep it, but hide it from the sorted view
```

Take the user's reply as the answer: "yes" or "recommended" means the
recommendation, a letter means that option, anything else is their own answer.
If an answer is ambiguous, ask once more for the same question — a retry is not
a new question. If the user says "done" or "enough", stop asking at once.

Do not ask a question you could look up, do not ask about a lens whose answer
you can already read in the spec, and do not reveal questions you have not yet
asked.

A user who answers "your call" or "whatever you think" has delegated the choice,
not made it. Use your recommendation, but record it in Assumptions rather than
as a settled fact, so the next reader can see it was a guess.

## 5. Write the answers back

After each round, edit the spec so the answer lives where a reader will look for
it. The format is closed — `check` accepts only the standard sections in order —
so there is no log of questions to append; the answer goes into the body, and
the history is in version control.

| The answer settles… | Write it into… |
|---|---|
| What is or is not included | Out of Scope, or the story's detail paragraph |
| A behaviour or an edge case | The story it belongs to — the detail paragraph carries the flow and the edge case that matters |
| A vague word | Replace it with the number or rule, wherever it appears |
| A guess that was listed | Remove it from Assumptions and put the fact where it applies; keep it only if still unconfirmed |
| How to tell it works | A new or sharpened Done When item a stranger could falsify |
| Work that follows from it | A task naming real paths, under the story it serves |
| A term | One name, everywhere, replacing the other |

Three rules keep the edit honest:

- **Replace, never accumulate.** If the answer contradicts an earlier line,
  change that line. A spec that says both the old and new rule is worse than
  either.
- **Touch only what an answer settled.** Do not reword for taste, reorder
  sections, or tidy things nobody asked about. The user is reviewing a diff
  caused by their answers.
- **Keep identifiers in order.** Story numbers and task ids must run without gaps
  (`US1…`, `T001…`), so adding or dropping either means renumbering what follows,
  including the task groups and any references to them.

An answer may reveal that the spec is missing a whole story, or that one is
really two. Say so and put it to the user rather than adding it silently; new
scope is the user's call, not a side effect of an interview.

Run the check after every write:

```bash
uv run --no-project "$LIGHTSPEC" check specs/NNN-slug/spec.md
```

Fix what it reports before the next round. Never leave the spec failing the
check. Do not touch `**Status**`, and never call `status` with a value — it
stays `Draft`, and moving it is `approve-spec`'s job.

## 6. Report

Give the path and what changed:

- Each question with the answer that was settled on, one line apiece
- The sections touched
- Anything recorded as an Assumption because the user delegated it
- **Deferred**: ambiguities you ranked but did not ask because the limit was
  reached, or because the user stopped early, each with a line on why it matters.
  The user can run the skill again to take them on
- Anything the answers contradicted — an accepted ADR, a cited finding — and
  what you did about it

Say the spec is still at `Draft` and that `approve-spec` is the next step if they
want it reviewed. Then stop. Do not approve it, do not begin implementing, and
do not file the tasks anywhere.

## Anti-patterns

- Asking what an ADR, the research, or the code already answers
- Asking about technology, libraries, or task order — the spec says what, the
  implementer decides how
- More than five questions, or inventing the last ones to use the allowance
- Questions that are labels ("Error handling?") rather than something answerable
- Questions with no recommended answer, or an open essay prompt
- Judging whether the feature is worth building; that is approve-spec's question
- Appending a "Clarifications" section or any other heading — `check` will refuse it
- Leaving the old text beside the new answer
- Rewording, reordering, or tightening prose that no answer touched
- Adding stories or scope the user did not agree to
- Moving the status, or running approve-spec as a follow-on step
