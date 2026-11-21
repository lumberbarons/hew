---
id: ADR-0001
title: Add a discovery issue type
status: accepted
date: 2026-11-19
deciders: []
supersedes: []
superseded-by: []
related: []
research: []
tags: [issues, readiness, claims, epics]
---

# ADR-0001: Add a discovery issue type

## Context and Problem Statement

Hew uses a priority label and a recognized issue type to distinguish triaged issues from incoming reports. Its default ready queue and session primer feed implementation agents, and the PR workflow can infer a closing issue from the caller's claim or branch. Maintainers also need a durable issue from which to explore a possible change, gather evidence, and decide what to pursue. That work benefits from the same issue number, ownership, and history and has a different outcome from implementation. Repositories may use different research, decision, and specification workflows.

## Decision Drivers

- A maintainer can start or resume discovery from a stable hew issue on either backend.
- Existing implementation loops can continue to trust the default ready queue, epic-next selection, and automatic PR inference.
- The CLI, hewd, and library consumers share claim and relationship rules.
- The tracker can serve different spec-driven workflows using ordinary issue references and closure.

## Considered Options

1. **Add a discovery issue type** and give it explicit claim and routing rules.
2. **Use an ordinary task with an `idea` area label** and leave its interpretation to callers.
3. **Keep the idea inbox in repository files** and create tracker issues when implementation is approved.

## Decision Outcome

Choose **Add a discovery issue type**. An idea is an ordinary GitHub issue or local JSONL issue with the `idea` type. When Hew's initial start read identifies an idea, a new claim requires the explicit discovery option. The option declares the caller's intent and uses the ordinary ownership and blocker rules. Changing an issue's type preserves any existing claim.

Hew excludes ideas from its ready queue, the primer's Ready section, and epic-next selection. Automatic PR inference skips ideas both as assigned issues and as branch-number candidates. A caller can explicitly name an idea as the closing target of a documentation PR. Parking or declining uses ordinary closure with a reason that remains available on reopen.

Ideas and epics can reference each other as independent issues. Hew's ordinary create, edit, and batch operations refuse new epic relationships that would put an idea on either end. An idea cannot be added as a child, gain children or an epic marker through those operations, or be produced by retyping an issue that would still be in an epic relationship. If an issue already qualifies as both an idea and an epic, display and handle it as an epic until a maintainer resolves the overlap. Preserve existing relationships on reads and allow explicit repair.

Hew exposes the issue type and ordinary issue operations to callers. Callers own their worker dispatch, discovery steps, artifact references, and completion rules. Any workflow can use the tracker to record an outcome and link an idea to implementation work.

## Consequences

### Positive

- Ideas share the tracker's stable identity, priority, claim, and history.
- Implementation agents can keep taking work from the same queue and using automatic PR inference for implementation issues.
- A repository can hand discovery off to implementation using its existing tools and issue references.

### Negative

- A fourth type changes the recognized vocabulary for scripts and integrations with a closed list of types.
- Existing uses of the literal `idea` label can change meaning after upgrade. An issue with a priority and only that recognized type becomes a triaged idea. If it also has an existing type such as `task`, the existing type wins and the usual multiple-type warning applies.
- Ideas and implementation epics require visible references because a native parent relationship would change epic progress or turn the idea itself into an epic.

### Risks

- A caller that selects issues independently of Hew's ready and epic-next reads may treat a claimed idea as implementation work. Hew exposes the current issue type so the caller can route it according to its workflow.
- Epics and issue labels can be changed outside hew. Surface overlapping classifications and preserve the stored issue so a maintainer can resolve them explicitly.

## Pros and Cons of the Options

### Add a discovery issue type

- Provides a shared signal for browsing, claiming, and excluding ideas from implementation selection.
- Extends the type contract and reserves a label that repositories may already use.

### Use an ordinary task with an area label

- Reuses existing label handling and avoids adding a type for consumers to recognize.
- Keeps ideas eligible for the ordinary task workflow unless each implementation path learns the label's special meaning.

### Keep the idea inbox in repository files

- Gives discovery a low-cost home alongside research and specs with minimal tracker changes.
- Leaves ownership, queueing, and resumption outside hew until an implementation issue is filed.

## Implementation Notes

- Add `idea` after the existing type labels in normalization precedence. Reserve it for new writes and preserve existing labels and relationships on read.
- Put readiness and relationship rules in the shared model and tracker, with command and HTTP adapters passing explicit intent through. The primer should teach the same distinction.
- Apply the idea exclusion consistently to Hew's ready and epic-next selection and to automatic PR inference. Cover explicit documentation closure separately.
- Revisit if hew itself must coordinate discovery stages or enforce a common completion contract across repositories. That would change the reason for leaving those rules with the workflow.

## References

- [Hew design](../../DESIGN.md) documents issue type normalization, readiness, claims, and native epic relationships.
- [Issue model](../../internal/model/issue.go) defines type normalization, epic classification, and readiness.
- [Tracker claim workflow](../../tracker/start.go) applies claim policy for library and adapter callers.
- [PR issue inference](../../pr/infer.go) uses claims and branch names to select a closing target.
