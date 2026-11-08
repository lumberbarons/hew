---
name: draft-spec
description: Draft a lightweight feature spec — description, prior decisions, prioritised user stories, a task list, and a done-when checklist — into specs/NNN-slug/spec.md in a fixed format enforced by a checker script. Use whenever a feature should be specified before it is built — "spec this out", "write a spec for X", "scope this feature", "plan this feature", "turn this into a task list", or a feature description handed over for planning. Produces a spec at status Draft; approving one is the separate approve-spec skill. Not for a single bug fix or a one-file change.
argument-hint: Describe the feature to specify
---

# draft-spec

Turn a feature request into one reviewable file: what it is, the decisions it
must respect, prioritised user stories, the tasks, and how anyone can tell it is
done. A script owns the format so every spec comes out identical; you supply the
judgment.

The process this serves is **research → ADR → spec → approve → implement**. This
skill is the drafting step. It reads decisions and research already recorded, and
leaves something a person or an agent can implement from. It writes no code, cuts no branches,
files no issues, and does not approve its own output — a spec leaves here at
`Draft`, and `approve-spec` is what moves it.

## The script

`scripts/lightspec.py` sits beside this file. Resolve it once against this
skill's own directory and reuse it through the steps below:

```bash
LIGHTSPEC="<this skill's directory>/scripts/lightspec.py"
```

A hardcoded `.claude/skills/...` path works only while the skill lives in a
repo checkout, and breaks the moment it is installed under `~/.claude/skills/`
or shipped in a plugin. It needs no virtualenv — the script is stdlib only.

## 1. Understand the request

Read what you were given. Ask only where a wrong guess would change the shape of
the work — scope boundaries, who the feature is for, what counts as done. At
most three questions, asked in a single round (one interaction, not one per
question), each with a recommended option first.

Do not ask what the codebase can tell you, and do not ask at all when the
request is already concrete. A spec built on one clarifying question and four
stated assumptions beats one built on five questions.

## 2. Collect the prior decisions

Find the recorded architecture decisions before writing anything:

```bash
ls docs/adr docs/decisions docs/adrs 2>/dev/null
find . -ipath "*adr*" -name "*.md" -not -path "./node_modules/*" | head
```

Read the ones that plausibly bear on this feature and check each one's status.
Cite the **accepted** ones; ignore superseded ones. For each citation write the
link and one line on the constraint it places on *this* feature — not a summary
of the ADR, which the reader can follow the link for.

`check` resolves every citation link against the repository, so a plausible-
looking path to an ADR that does not exist is caught rather than believed. Cite
what you actually found.

Three cases worth handling deliberately:

- **Nothing applies.** Write `- None`. Do not pad the section.
- **The feature would contradict an accepted decision.** Say so in the bullet,
  plainly, and raise it in your report. Diverging quietly is the failure mode
  this section exists to prevent; the user may want a new ADR before the spec is
  worth finishing.
- **The feature forces a decision nobody has recorded.** Put it in Assumptions
  and note it deserves an ADR of its own.

## 3. Collect the research

Research documents record what was found out before anything was decided or built.
Look in `docs/research/` or `research/`, read the titles and research questions, and
read the ones that bear on this feature:

```bash
ls docs/research research 2>/dev/null
```

Cite a document only if it is `status: complete` and not `superseded`; for a superseded
one, follow its `superseded-by` to what replaced it. A `draft` is not yet confirmed, so
rely on it only by saying so in Assumptions. For each citation write the link and the
specific finding the spec leans on — `RES-0004 F2`, not a summary of the document —
because the finding's stated confidence tells a reviewer how much weight it can bear. If
a story rests on a `Low` finding, put that in Assumptions too.

Where an ADR has since been written on the strength of the research, cite the ADR in
Prior Decisions and leave the research out unless it adds something the ADR does not. If
a `Revisit When` condition in cited research has been met, say so in your report; the
research may need refreshing before the spec is worth approving.

Write `- None` when no research bears on it. That is the common case for a small feature,
and no reason to commission research. If the feature plainly turns on a fact nobody has
established, say so in your report and offer `create-research` first.

## 4. Ground it in the code

Skim enough of the repo to name real files in the task list. You are locating
the seams the work will touch, not auditing them — a few searches, not a survey.

## 5. Generate the skeleton

```bash
uv run --no-project "$LIGHTSPEC" new "Price Drop Watchlist" --stories 2
```

Decide the story count from step 1. The script auto-numbers from the highest
existing `specs/NNN-`, derives the slug from the title, refuses to overwrite,
and prints the path it wrote. Keep that path — step 7 checks it.

## 6. Fill it in

Replace every `<FILL: ...>` marker. The marker text is the instruction for that
slot — delete it as you go. `reference/example-spec.md` is a filled spec written
to be imitated.

| Section | What belongs there |
|---|---|
| Description | What this is, who it serves, why it is worth doing now. No technology choices. |
| Prior Decisions | Step 2's citations, each with the constraint it imposes here. |
| Research | Step 3's citations, each with the finding the spec leans on. `- None` if there is none. |
| Out of Scope | What a reader would otherwise assume is included. This is where scope creep dies. |
| Assumptions | Every place you guessed, stated so a reader can disagree with a specific line. |
| User Stories | `As a <role>, I want <capability> so that <outcome>`, then 1-2 lines: the flow, the constraint that shapes it, the edge case that matters. |
| Tasks | One line each, grouped under the story it serves, `Cross-cutting` for the rest, each naming real paths. |
| Done When | Observable claims someone else could check without reading the diff. |

The header above the sections is stamped for you. `**Status**` is the one field
with a life after this skill: `new` writes `Draft`, and draft-spec never moves
it off `Draft`. You are writing a proposal, not ratifying one — advancing it is the
`approve-spec` skill, invoked deliberately by a person, and the checker holds
the vocabulary closed so it cannot decay into free text.

| Status | Means |
|---|---|
| `Draft` | Written, not yet agreed. Everything `new` produces starts here. |
| `Accepted` | Agreed. Build from it. Set by `approve-spec`, never here. |
| `Implemented` | Built and merged. Kept as the record of what was intended. |
| `Superseded` | Replaced — and it must link what replaced it, for the same reason a cited decision carries a link. |

Nothing else passes the check, and only `Superseded` may carry text after the
word: `**Status**: Superseded by [011 Watchlist v2](../011-watchlist-v2/spec.md)`.

Priorities carry meaning: **P1** is what makes the feature worth shipping at
all, **P2/P3** what makes it good. Order stories by priority, and write them at
the altitude of the person using the software — "As a shopper, I want…", never
"As a developer, I want a repository interface".

Done-when items are the acceptance test. "Clicking watch twice leaves exactly
one row and reports success both times" — not "the watchlist works". Write them
so a stranger could run them.

## 7. Check

Check the exact path step 5 printed:

```bash
uv run --no-project "$LIGHTSPEC" check specs/009-price-drop-watchlist/spec.md
```

Fix what it reports and run it again until it prints `ok`. Never report the spec
finished while the check fails.

Do not reach for `--latest`. It resolves to the highest-numbered directory under
`specs/`, which in a repo that used another spec tool is a spec in a format
lightspec knows nothing about. Pointed at one of those, the checker reports
failures for a file this feature never touched — and the instruction above,
"fix what it reports", would then have you rewriting somebody else's spec.

The checker sees shape only. It cannot tell you a done-when item is unfalsifiable
or that a story is really three stories. That judgment stays yours — re-read the
Done When section once with the checker green and ask whether each line could
actually be verified by someone who did not write the code.

## 8. Report

Give the path, the story and task counts, and anything you assumed that the user
should overturn. Say the spec is at `Draft` and that approve-spec is the next
step if they want it reviewed and accepted. Then stop. Do not begin implementing,
do not approve it yourself, and do not file the tasks anywhere unless asked.

## Portability

Nothing in the format is tied to a tracker. The vocabulary lines up with common
issue-body conventions: one task is about one issue, `Done When` items drop
straight into a done-when checklist, and P0–P4 matches the usual priority
labels. If this repo uses an issue tracker, filing is the user's next step and
their call, not something this skill does.

## Anti-patterns

- Technology in the Description — it belongs in the tasks, or in an ADR
- Done-when items that restate tasks; a task is work, a done-when is evidence
- Empty Out of Scope or Assumptions when the request was genuinely ambiguous
- Padding Prior Decisions with ADRs that do not actually constrain this feature
- Citing research that is a draft, superseded, or unread, or citing a whole document
  where one finding is what the spec relies on
- More than about eight stories or twenty-five tasks — that is two features
- Cutting a branch, filing issues, or writing code from this skill

## Maintaining the script

```bash
python3 -m unittest discover "$(dirname "$LIGHTSPEC")"
```

The skeleton and the checker define the same format in two places. The tests
pin them together: a fresh skeleton must fail the check for its `<FILL: ...>`
markers and nothing else, and `reference/example-spec.md` must pass. Change the
format in `scripts/lightspec.py` and those two tests tell you what else moved.
