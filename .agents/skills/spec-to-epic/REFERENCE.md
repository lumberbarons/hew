# spec-to-epic reference

## Work item mapping

`extract_spec.py --plan` emits one hew JSON object per line. The epic
comes first, followed by its work items in spec order.

| Field | Epic | Child |
|---|---|---|
| `id` | `epic` | Work item ID, such as `WI1` |
| `title` | Spec title | Work item title |
| `type` | `epic` | `task` |
| `priority` | Highest priority among the children | Work item priority |
| `parent` | | `epic` |
| `goal` | Description | Goal |
| `where` | Spec path and top-level directories from work item paths | Where, one path per line |
| `approach` | Prior Decisions, Research when present, Out of Scope, and Assumptions | Approach, work item ID, spec path, and any supported story IDs |
| `done-when` | Feature-level Done When bullets | Work item's Done when bullets |
| `blocked-by` | | Explicit Blocked by work item IDs |

hew adds the epic title prefix and renders the acceptance criteria in its
issue-body format. It resolves local IDs in `parent` and `blocked-by`.

See [the watchlist spec](reference/example-spec.md) for a feature with two
stories and three work items. The storage migration supports both stories
and is a prerequisite for the controls and the watchlist page. It files as
one child issue with its own acceptance criteria.

## Legacy Tasks format

The checker and extractor still read older specs. For story-owned tasks,
use the spec's title, description, framing, and acceptance criteria for
the epic. File one child per story with its title, priority, and story
sentence as the goal. Put its narrative and task lines in Approach and
the union of its task paths in Where. Give each child verifiable acceptance
criteria from the epic checks that apply to its scope, adding checks for
deliverables those criteria do not cover. Flag any implementation promised
by an epic criterion that has no owning child before filing.

If the extractor returns `cross_cutting_tasks`, stop before preparing a
plan and report their IDs. Ask the author to revise the spec with explicit
work items and have it reviewed and accepted again.

## Dry run and retry

Run `hew apply plan.jsonl --dry-run` before filing. Resolve warnings about
unmarked paths, commands, or identifiers by wrapping them in Markdown code
spans. Keep the plan and its state file together if filing fails partway
through. Reapplying the same plan resumes from its checkpoint.
