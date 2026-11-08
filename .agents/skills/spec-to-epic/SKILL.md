---
name: spec-to-epic
description: Turn an Accepted lightspec (specs/NNN-slug/spec.md, in the draft-spec/approve-spec format — Description, Prior Decisions, Out of Scope, Assumptions, prioritised User Stories, Tasks, Done When) into a hew epic — one epic issue carrying the spec's framing, one child issue per user story sized to hew's own conventions, tasks folded into each child's Where/Approach, and Done When items derived per story so work-issue can drive them test-first. Use whenever a spec needs to become trackable work — "turn this spec into issues", "file this spec in hew", "create an epic from spec 001", "get this spec into the tracker" — or as the deliberate step between approve-spec and work-issue once a spec reaches Accepted. Not for a spec still at Draft, not for a spec in some other shape than lightspec's, and not for filing one ad hoc issue — use hew create directly for that.
argument-hint: The spec to convert — path or number, e.g. 001 or specs/001-deterministic-retirement-engine/spec.md
---

# spec-to-epic

The missing link between two pipelines that otherwise don't talk to each
other: **draft-spec → approve-spec** produces an Accepted spec that nothing
has to build from yet, and **hew ready → hew start → hew pr** drives whatever
is already in the tracker. This skill is the one-time translation between
them — read once, filed once, and everything after that is ordinary hew work.

It writes no code and touches no branch. It also does not edit the spec file
— approving a spec moved it to `Implemented` eventually, through the normal
epic-closes-when-built path, not through anything this skill does.

> [!IMPORTANT]
> [REFERENCE.md](REFERENCE.md) carries the JSONL plan schema, the full worked
> example built from this repo's own spec, and the done-when derivation
> table. Read it before Step 4.

## The script

`scripts/extract_spec.py` and a copy of the format checker `scripts/lightspec.py`
ship inside this skill. Resolve both against this skill's own directory:

```bash
EXTRACT="<this skill's directory>/scripts/extract_spec.py"
LIGHTSPEC="<this skill's directory>/scripts/lightspec.py"
```

The extractor parses a lightspec-format `spec.md` into JSON — title, the four framing
sections, every story with its priority and task list, each task's paths,
and the epic-level Done When list. It is stdlib-only, needs no virtualenv,
and exists for one reason: a file path or a task id copied out of this
script's output is the exact string the spec author wrote, and one retyped
by eye is a chance to introduce a path that doesn't exist. What it will
never do is decide which done-when item belongs to which story, or turn a
story's narrative into a `goal` line — that judgment is this skill's job,
not the script's, for the same reason `lightspec.py` draws its own line at
shape rather than substance.

## Prerequisites

`hew` on PATH and authenticated — exit code 4 from any `hew` command means
run `gh auth login`. Run from a checkout of the target repository; `--repo
owner/name` overrides detection.

Confirm which repository that detection landed on before the real (non-dry-run)
apply in Step 5 — `git remote -v`, or just read the repo name back out of
`hew search`'s or `hew apply --dry-run`'s own output. Step 5's dry run is
cheap to re-run; a real `hew apply` against the wrong repository is not
something this skill can undo.

## Step 1 — Resolve and gate the spec

Take the number or path given. `specs/<NNN>-*/spec.md` resolves a bare
number; otherwise take the path as given. If neither was given, list what's
under `specs/` with each one's `**Status**` and ask which.

Run the bundled checker and read the status for the exact resolved path:

```bash
uv run --no-project "$LIGHTSPEC" check specs/NNN-slug/spec.md
uv run --no-project "$LIGHTSPEC" status specs/NNN-slug/spec.md
```

If `check` does not print `ok`, stop and report its failures before building a
filing plan. This gate works even when draft-spec and approve-spec are not
installed; no sibling skill directory is needed.

**Only `Accepted` is fileable.** A `Draft` spec has not been reviewed — filing
its issues is a quiet way of building from something nobody signed off on,
which is exactly what approve-spec's gate exists to prevent one step
upstream. Stop and say the spec needs approve-spec first, rather than
filing anyway because the content looks reasonable.

If the file doesn't parse as a lightspec spec at all (see Step 3), that's a
different problem: this skill only knows the draft-spec/approve-spec shape,
and a spec written some other way needs its own mapping before this skill
can help. Say so rather than improvising one.

## Step 2 — Check for an existing epic first

Filing the same spec twice produces two epics nobody merges and a tracker
that no longer agrees with itself about which one is current. Before writing
anything:

```bash
hew search "<spec path, e.g. specs/001-deterministic-retirement-engine/spec.md>"
```

The spec's own path is what Step 4 puts in the epic's `### Where`, so
searching for it is exact rather than hopeful — unlike title text, a path
cannot have been paraphrased by a previous run. Widen to
`hew list --json --bodies --state all` if the search comes back empty and you
have reason to think an epic exists anyway (a closed one, say).

**Found one:** stop. Report the existing epic and its child count rather than
filing a second tree. If the spec has grown new stories since that epic was
filed — the spec was edited and re-accepted — that's a reconciliation the
user should ask for explicitly (new children off the existing epic), not
something to infer and do silently here.

**Found nothing:** proceed to Step 3.

## Step 3 — Extract the spec mechanically

```bash
uv run --no-project "$EXTRACT" specs/NNN-slug/spec.md
```

A non-zero exit means the file doesn't parse as a lightspec spec — read the
message, which names the specific line or section, and stop rather than
hand-transcribing around the failure. (A spec that fails this should also
fail `lightspec.py check`; if it doesn't, that's worth flagging as a gap in
one of the two scripts, not something to paper over here.)

The JSON that comes back is what Step 4 works from. Nothing past this point
should require rereading the markdown file directly — paths, task ids, and
story text all came out of the JSON, verbatim.

## Step 4 — Map onto hew's shape

hew's body template is `### Where` / `### Problem` or `### Goal` / `### Fix`
or `### Approach` / `### Done when`. The mapping below leans on what each
section is actually *for*, not just what text happens to be lying around —
the same discipline that keeps every other hew issue in the tracker readable
the same way.

### The epic

| Field | From | Why |
|---|---|---|
| `title` | the spec's H1 | `hew apply` prefixes `Epic: ` itself for `type: epic` entries — don't do it yourself |
| `priority` | the highest-priority (lowest number) story | the epic can't outrank the work that makes it up |
| `goal` | `description`, lightly trimmed | this is the spec's own answer to "why does this exist" |
| `where` | the spec path, backticked, then the set of top-level directories across every task's paths | the path is the dedup anchor Step 2 searches on; the directories are a coarse boundary — call `hew search` finds this by path, not prose |
| `approach` | `prior_decisions`, `out_of_scope`, and `assumptions`, each under a bold label, as three short lists | these are exactly what bounds *how* the epic gets built — the constraints, the deliberate exclusions, the guesses a reader can challenge |
| `done-when` | `done_when`, verbatim | the spec's acceptance list already *is* the epic's acceptance list; nothing here is this skill's to rewrite |

Every path and every citation link goes in backticks — hew warns on
unmarked code-shaped text in composed bodies, and a spec path or a file path
rendered as plain prose is exactly the kind of string that should be a code
span. `hew apply --dry-run` will point out anything you missed.

### One child per user story

**Why the story, not the task, is the unit of filing:** a task names one
file; a story is what a reviewer actually judges — a claim about behaviour,
with a handful of tasks under it that exist to make that claim true. A
story's task count usually lands close to hew's own single-issue sizing
(see below), because both are describing the same-sized unit of reviewable
work from two different directions — but that's a description, not a rule
to file by. Filing at task granularity would produce issues too small to
carry a real acceptance claim regardless of size — "T001 exists" is not
something work-issue can write a meaningful test against.

| Field | From | Why |
|---|---|---|
| `title` | the story's title | already short and specific |
| `type` | `task` | every story here is something the codebase doesn't do yet — `enhancement` is for extending something that already exists |
| `priority` | the story's own `(P#)` | the spec's scale and hew's are the same scale on purpose |
| `parent` | the epic's local id in the plan | `hew apply` resolves this within the file |
| `goal` | the story's own sentence, verbatim | it's already written as `As a <role>, I want <X> so that <Y>` — hew's own convention for this field |
| `approach` | the story's narrative paragraph, then the task list as `- T00N: <action>` bullets | the narrative is the prescription; the tasks are the concrete steps that satisfy it |
| `where` | the union of every task's paths under this story | a story whose tasks each name one file ends up with a Where list a reviewer can hold the diff against directly |
| `done-when` | derived — see below | nothing in the spec hands you this verbatim; it has to be built |

### Deriving Done When per story

This is the one part of the mapping that isn't a straight copy, and it's the
part work-issue actually depends on — its whole test-first step reads
`### Done when` as the test list. An issue with a weak one gets weak tests.

1. **Reuse what the spec already proved true for this story.** Walk the
   epic-level `done_when` list and pull in any item whose claim is fully
   checkable within this story's own `where` — nothing outside those paths
   needs to change for the claim to hold. Reword only if the epic-level
   phrasing assumes context (another story, a later state) this story
   doesn't have yet.

   Some epic-level items bundle several stories' claims into one sentence —
   "shows one OAS ceasing, CPP survivor applied under the combined cap,
   single brackets and credits..." is one bullet naming work from three
   different stories. Reuse doesn't have to be all-or-nothing: pull out the
   clause this story actually owns rather than either copying the whole
   compound sentence somewhere it half-applies, or skipping it and deriving
   something unrelated from scratch. The epic keeps the full sentence either
   way (rule below), so nothing is lost by slicing it here.
2. **Write one for any task-level claim that reuse didn't cover.** Read the
   task's `action`. If it names an externally observable effect (a computed
   value, a rejected input, a file format), write a behavioural claim —
   "CPP taken at 60 pays 0.640× the age-65 entitlement." If it's pure
   scaffolding or wiring with nothing to assert yet (a module skeleton, a
   schema with no validation logic behind it), write a structural one —
   "`go build ./cmd/retire` produces a `retire` binary." Either way: a claim
   a reviewer could check by reading the diff or running a command, never a
   restatement of the task ("the schema is defined" is not a done-when
   item).

   Tasks and done-when items don't have to end up 1:1. Two tasks whose
   claims can only be checked together collapse into one item — REFERENCE's
   own US1 example does this for the schema-shape task and the dated-tables
   task, since loading the example household is the one check that exercises
   both. Conversely, split a single task into two items only when its action
   names two claims that a reviewer could find false independently of each
   other — not to pad the list, and not by default. If one check genuinely
   covers everything the task promises, one item is enough.
3. **An item can appear on more than one story** if it's genuinely
   re-verifiable at each — rare, and worth a second look when it happens,
   because it usually means the item actually belongs on the epic alone,
   already covered by step 1's untouched copy there.
4. **Not every epic-level item needs a story to own it, and that's fine.**
   Some claims are integration-level or don't trace to any single task at
   all — the retirement engine spec's own Done When list has one about
   documenting a convention in the README, and no task in any story touches
   `README.md`. Leave it on the epic alone rather than stretching a child's
   `where` to cover a file none of its tasks actually name. The epic's
   `done_when` is the complete record regardless of how the work was split
   into children; a child's list only needs to be complete for *its own*
   scope, not exhaustive over the epic's.

### Sizing

Report each child's path count and done-when count alongside its issue
number — not to decide anything here, but because work-issue's own
`--batch` sizing rule (more than 3 paths, or more than 5 done-when items,
means "work it alone, never batched with others") reads that same count
later, and a human scanning the report should be able to tell without
re-running anything which children that will apply to. A story landing above
that count is not a defect in this skill's output — the spec already decided
how big each story is, back when it was drafted and approved, and
re-splitting it here would break the 1:1 correspondence between spec story
numbers and filed issues for no benefit. If a story's *size* genuinely
bothers you — not its `where`/`done-when` count, but a sense that it is
quietly two stories wearing one heading — that's the same question
approve-spec already asked and answered before this spec reached `Accepted`;
raise it with the user as feedback on the spec, not as a reason to file it
differently than it was written.

**Don't invent a `--blocked-by` chain between stories.** The spec's story
order already carries the build sequence a human would use, and hew's own
tie-break rule does the rest for free: `hew ready` sorts by priority and
breaks ties toward the oldest issue, so filing the children in the order
Step 3 returned them means same-priority stories surface in spec order
without a single dependency edge. A guessed dependency graph is worse than
none — it blocks work that was never actually blocked, and it's wrong more
often than the plain filing order is. If the spec's own prose states a hard
dependency in so many words, say so in the report and let the user decide
whether to add `hew block` after filing; don't add it yourself.

## Step 5 — Write the plan and dry-run it

Assemble one JSONL plan (schema and a full worked example in
[REFERENCE.md](REFERENCE.md)) with the epic first, then its children in spec
order.

```bash
hew apply plan.jsonl --dry-run    # always first
```

Show the dry-run output — every issue it would create, with its priority,
type, and parent — and confirm before applying, unless told not to ask.
Then:

```bash
hew apply plan.jsonl
```

`hew apply` checkpoints creation as it happens, so a run that dies partway
resumes without duplicating anything already filed — the same property
`raise-issues` leans on for the same reason.

## Step 6 — Report

```
Epic #<n>: <title>
  8 stories filed as children (#41-#48), priorities P1×6 / P2×2
  Sizing (paths / done-when): us1 4/4  us2 3/3  us3 2/3  us4 5/5  us5 3/3
    us6 1/2  us7 4/6  us8 2/2 — #47 (US7) exceeds hew's single-issue batch
    size on both counts; work-issue will size it alone, which is expected
  Next: hew ready lists the P1 children now; work-issue to start one
```

Say what you found in Step 2 if you stopped there instead. Flag which
done-when lists leaned on judgment rather than a direct copy from the spec
(most of them will — that's expected, not a defect) so a reviewer knows
where to look first, and name every hard dependency the spec stated that you
did not encode as `--blocked-by`, so the user can act on it without
re-reading the spec themselves.

## Anti-patterns

- Filing a `Draft` spec's issues — that's approve-spec's gate to clear, not
  this skill's to route around
- One issue per task instead of per story — too small to carry a real
  acceptance claim, and 25 issues where 8 would do
- Splitting an oversized story into two child issues on this skill's own
  initiative — the spec's story count is what got approved; raise size as
  feedback on the spec, don't silently refile it differently
- A `where` or `done-when` line copied from the spec's prose without
  checking it's still true in isolation for this one story
- Stretching a child's `where` to cover a file none of its own tasks name,
  just so an orphaned epic-level done-when item has somewhere to live
- A guessed `--blocked-by` chain between stories that the spec never stated
- Editing `spec.md`, starting implementation, or running `hew close` — none
  of those are this skill's job
- Re-filing an epic Step 2 already found
