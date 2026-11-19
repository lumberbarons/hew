---
name: spec-to-epic
description: Turn an Accepted lightspec into a hew epic with one child issue per work item. Copy each item’s goal, paths, approach, and acceptance criteria, and use explicit prerequisites as tracker dependencies. Use when asked to turn a lightspec into issues, file it in hew, or create an epic from an Accepted spec.
argument-hint: The spec to convert — path or number, e.g. 001 or specs/001-deterministic-retirement-engine/spec.md
---

# spec-to-epic

Convert an Accepted spec into a hew epic and its child issues. The spec
holds the approved scope. Issue closure records implementation progress.

Resolve these scripts relative to this skill's own directory:

```bash
EXTRACT="<this skill's directory>/scripts/extract_spec.py"
LIGHTSPEC="<this skill's directory>/scripts/lightspec.py"
```

[REFERENCE.md](REFERENCE.md) describes the JSONL mapping and how to handle a
spec written in the older Tasks format.

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

If the file doesn't parse as a lightspec spec at all, that's a
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

Use a repository-relative path for the search; the plan records it in the
epic's Where. Match an epic among the results. Widen to
`hew list --json --bodies --state all` if the search comes back empty and you
have reason to think an epic exists anyway (a closed one, say).

**Found one:** stop. Report the existing epic and its child count rather than
filing a second tree. If the spec has grown new work items since that epic was
filed — the spec was edited and re-accepted — that's a reconciliation the
user should ask for explicitly (new children off the existing epic), not
something to infer and do silently here.

**Found nothing:** proceed to Step 3.

## Step 3 — Generate the plan

```bash
uv run --no-project "$EXTRACT" --plan specs/NNN-slug/spec.md > plan.jsonl
```

Check the exit status before proceeding. The plan contains the epic and one
child per work item, in spec order. The script copies acceptance criteria and
hard dependencies from the spec. The work item ID and any supported story
IDs appear in the child issue body for traceability.

For an older spec, run the extractor without `--plan`. If it has cross-cutting
tasks, stop and report their IDs. Have the spec author assign the work to
explicit work items and get the revision accepted before filing. For an older
spec with only story-owned tasks, follow the legacy mapping in [REFERENCE.md](REFERENCE.md).

## Step 4 — Check the mapping

Compare the plan with the Accepted spec. Every work item must have one
child. Every task, path, and acceptance criterion must be represented in
that child. Keep the spec's priority and any explicit hard prerequisites.
Raise any scope or sizing concerns with the author before filing. Make
scope changes in the spec so they can be reviewed before implementation.

## Step 5 — Dry-run and apply the plan

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

Report the epic, each work item ID and its child issue number, and any explicit
dependencies. Include each child's path and acceptance-criterion counts so a
reader can see which issues `work-issue --batch` will work alone. A child with
more than three paths or more than five acceptance criteria exceeds the
batch sizing threshold.

If an epic already exists for the spec, report it and its children.
