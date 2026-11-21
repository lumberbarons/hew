# Idea Issues

**Slug**: `001-idea-issues`
**Created**: 2026-11-19
**Status**: Accepted

## Description

A maintainer can capture a possible improvement as an idea and return to it when they are ready to decide whether to pursue it. The issue gives the maintainer and their agents a stable starting point for gathering evidence, recording decisions, and developing a spec. A maintainer decides when to investigate and can link the idea to any resulting documents or implementation issues.

## Prior Decisions

- [ADR-0001 Add a discovery issue type](../../docs/adr/0001-add-discovery-issue-type.md) — Ideas require explicit discovery claims, stay outside implementation selection and automatic PR inference, and use references to associate with epics. Hew preserves existing relationships on reads and leaves completion policy to each workflow.

## Research

- None

## Out of Scope

- Orchestrating discovery, checking spec acceptance, or automating epic filing and recovery. Those steps belong to the workflow using hew.
- Additional tracker states for ideas, a separate idea store, or automatic promotion based on priority.
- A dedicated `hew idea` command. Capture and browsing use the existing `create` and `list` commands.
- Tracker-wide enforcement of spec workflow conventions. Completion and the number of specs and epics associated with an idea are up to the workflow using hew.

## Assumptions

- `idea` uses the existing issue number, priority, assignee, history, and open and closed states on both backends.
- Existing type labels keep their normalization precedence. If a repo already used `idea` as a label, an issue carrying both `idea` and `task` still reads as a task and surfaces the usual multiple-type warning. A priority-labeled issue with `idea` as its only type reads as an idea. Upgrades leave stored labels intact.
- A maintainer asks for investigation by naming the idea or by explicitly authorizing an investigation workflow to select one. An explicit claim option records the caller's intent. The caller is responsible for having that authorization.
- Research and ADRs are useful when evidence is needed or a decision warrants a record.
- Repository document paths and ordinary issue references carry traceability. A GitHub documentation PR can close the idea when its outcome is recorded in the repo. The local tracker can record completion with `hew close --completed` and a reason.

## User Stories

### US1 — Capture an idea quickly

As a maintainer, I want to capture a possible improvement in hew so that I can revisit it before committing to a design.

I can file it with a title and a short problem or opportunity statement. I can add questions when I already know what needs investigation.

### US2 — Authorize an investigation

As a maintainer, I want an agent starting a new claim on an idea to declare discovery intent so that routine implementation runs leave ideas alone.

The agent sees what an idea means in the primer. I can direct it to a specific idea for research, an ADR, or a spec, and it uses the ordinary ownership and blocker checks when claiming or resuming. I can change an issue's type while keeping its current assignment. Queues and workers use the issue's current type.

### US3 — Trace the decision and handoff

As a maintainer, I want to see what an investigation concluded and where it led so that I or another agent can resume from the original issue.

The issue records the outcome and references the evidence or decisions that informed it. I can add document paths and issue references to the idea, and I can reference the idea from an epic or another issue. My workflow determines which artifacts to produce and when the idea is complete.

I park or decline an idea by closing it with a reason. If I revisit a parked idea, reopening it restores it to the Open view with its original number and history. Epic associations use issue references, so the original idea stays outside the epic's implementation progress.

### US4 — Review ideas in hewd

As a maintainer, I want a dedicated Ideas page so that I can capture and browse ideas away from the implementation queue.

I can switch between open and closed ideas, open one in the existing detail view, and follow its references or start an investigation deliberately.

## Work Items

### WI1 — Recognize idea as an issue type (P1)

**Goal** Give ideas the same stable tracker identity as other issue types.

**Supports** US1, US2, US4

**Where** `internal/model/issue.go`, `internal/conventions/labels.go`, `tracker/create.go`, `tracker/edit.go`, `tracker/init.go`, `tracker/plan.go`, `tracker/apply.go`, `internal/plan/plan.go`, `internal/cli/apply.go`, `migrate/local.go`, `consumer`

**Approach**
- Extend the shared type vocabulary and label bootstrap. Keep the existing type precedence and treat `idea` as a reserved convention label on writes.
- Cover create, edit, apply, and local-to-GitHub migration so the type survives ordinary tracker operations.
- Let an unmodified plan resume past entries already created with `idea` as an area label when their checkpoint mappings pass the existing repository, source, and issue provenance checks. Defer the new area reservation check until the apply path can distinguish verified existing entries from pending creates. Enforce the reservation on all pending creates and on fresh plans.

**Done when**
- On GitHub and local, an issue created or edited to type `idea` reads back with exactly one type and one priority.
- A dry-run and apply accept `idea`, and migrating a local idea preserves its type on GitHub.
- An existing `task` plus `idea` reads as `task` with a multiple-type warning and retains both stored labels. An issue with only `idea` and a priority reads as `idea`.
- A pre-upgrade checkpoint for a task created with `areas: ["idea"]` can resume with the original plan in `hew apply` and the tracker API. It keeps the task's issue number and labels, creates only pending valid issues, and finishes pending valid relationships. Starting the same plan with no checkpoint rejects the reserved area before writing anything. A mismatched checkpoint fails the existing binding checks.
- An unchanged API-built plan with ordinary task entries and no open questions resumes from a pre-upgrade checkpoint and its existing issue provenance. The test uses the tracker's derived digest, with no caller-supplied or checkpoint-bound digest, to verify that the optional questions field preserves the digest of existing plans.

### WI2 — Capture and filter ideas through existing commands (P1)

**Goal** Make the shortest useful idea easy to file and find from the CLI.

**Supports** US1, US4

**Blocked by** WI1

**Where** `cmd/hew/main.go`, `internal/cli/write.go`, `internal/cli/read.go`, `internal/cli/draft.go`, `internal/conventions/template.go`, `tracker/body.go`, `tracker/query.go`, `daemon/handlers_read.go`, `daemon/handlers_write.go`

**Approach**
- Support quick capture with `hew create --type idea --title … --problem …`. Update type help for create and edit. Add optional `--open-questions` to the shared body composer so create, draft, and hewd can emit `### Open questions` only when supplied. Keep `--body-file` available for author-written Markdown.
- Add `hew list --type idea` over a shared normalized-type filter and expose the filter through `GET /api/issues?type=idea`. Keep the existing label filter available for literal label queries.

**Done when**
- Quick capture writes the supplied problem or opportunity under `### Problem` with the default priority. Adding questions produces the optional heading without requiring an approach or a checklist.
- `hew draft` can compose the same body offline and `hew create --body-file` can publish it.
- The CLI in text and NDJSON and the daemon list endpoint return triaged, non-epic issues whose normalized type is `idea`. They honor open and closed state filters.
- A legacy task with a literal `idea` label appears in a label query and is absent from an idea type query.

### WI3 — Require explicit intent to claim an idea (P1)

**Goal** Give authorized investigators a claim path while ordinary claim callers leave ideas untouched.

**Supports** US2, US4

**Blocked by** WI1

**Where** `tracker/start.go`, `tracker/options.go`, `tracker/errors.go`, `tracker/create.go`, `tracker/edit.go`, `cmd/hew/main.go`, `internal/cli/write.go`, `internal/cli/exit.go`, `daemon/handlers_write.go`, `daemon/errors.go`, `consumer`

**Approach**
- Add `hew start <n> --for-discovery` and a corresponding tracker option and REST request field. Map the typed refusal to a CLI usage error and a stable REST error that explains how to claim for investigation.
- Check the issue type returned by the existing start read before any claim or priority write, including forced starts. Reject assignees on idea creation through the tracker so capture cannot implicitly claim the issue.
- Treat a type edit as a classification change. Preserve any current assignee and `in-progress` label. Maintainers can use the existing release mechanism when they want to end a claim before or after retyping.

**Done when**
- If `hew start` reads the issue as an `idea`, it requires explicit discovery intent before writing a claim, including when forced. A refusal leaves the assignee, priority, and labels untouched and is also available through the public tracker and REST API.
- An explicit discovery start claims an eligible idea. It reports open blockers and existing claims using the ordinary claim outcomes when no force is requested.
- An idea create request carrying an assignee fails before writing an issue.
- Retyping an otherwise eligible claimed task to `idea` and retyping a claimed idea to `task` both preserve the assignee and `in-progress` label. Default work selection follows the new type and the usual rules for claimed issues.

### WI4 — Keep ideas out of implementation readiness and teach the workflow (P1)

**Goal** Keep the ready queue safe for ordinary implementation and teach agents when to investigate ideas.

**Supports** US2

**Blocked by** WI3

**Where** `internal/model/issue.go`, `tracker/query.go`, `tracker/snapshot.go`, `tracker/primer.go`, `internal/conventions/primer.go`, `docs/primer-mock.md`, `daemon/handlers_read.go`, `daemon/fixtures_test.go`, `daemon/web/src/fixtures`, `DESIGN.md`, `README.md`

**Approach**
- Apply idea exclusion to the shared default readiness policy and to the shared epic-next selector used by tracker detail. Keep idea children attached outside hew visible in epic detail.
- Add a compact rule to both primers. Claim ideas only on request, use them for research, ADRs, or specs, and hand approved implementation off to separate work issues. Document the invocation and keep the primer within its existing budget.

**Done when**
- With an unclaimed, unblocked idea and an equivalent task, only the task appears in `hew ready`, the primer's Ready section, and hewd's Ready queue.
- When an epic has an eligible idea child attached outside hew and an eligible task child, tracker detail and hewd show both children and select the task as next. If the idea is the only child, no next child is selected.
- Both primer variants identify the idea type, explain the explicit claim rule, and direct implementation to separate work issues.

### WI5 — Document how to record and link idea outcomes (P1)

**Goal** Show maintainers how to record an idea's outcome using ordinary tracker operations.

**Supports** US3

**Blocked by** WI1

**Where** `README.md`

**Approach**
- Show how to keep the original problem and add outcome notes, document paths, and issue references to the existing issue body. Use an epic as an example of an implementation issue that can refer back to the idea.
- Document how to close an idea with a reason and how to reopen it.

**Done when**
- Using the documented operations, a maintainer can record a decision and a reference to an epic on the idea and add the original idea number to the epic body.
- A maintainer can close an idea with a reason, find the reason and artifact references on the closed issue, and reopen the same issue.

### WI6 — Add the Ideas page to hewd (P1)

**Goal** Give maintainers a place to capture ideas, browse their outcomes, and deliberately begin an investigation.

**Supports** US1, US2, US3, US4

**Blocked by** WI2, WI3

**Where** `daemon/web/src/App.tsx`, `daemon/web/src/router.ts`, `daemon/web/src/views/NewForm.tsx`, `daemon/web/src/views/IssueView.tsx`, `daemon/web/src/types.ts`, `daemon/web/src/fixtures`, `daemon/README.md`

**Approach**
- Add an Ideas navigation entry with Open and Closed views, backed by the shared type filter. Link to the existing New form preselected to Idea and reuse issue detail for reading and editing.
- Label the idea detail action as investigation and pass explicit claim intent. Reuse the Markdown renderer for outcomes and issue references.

**Done when**
- From Ideas, a user can file an idea with a title and a short statement. It appears in Open after reload and in Closed after closure.
- The page shows only ideas. Opening one shows the original statement, any decision, and navigable issue references.
- The idea detail offers an explicit investigation action. Opening the page or reading the detail leaves the issue unassigned.

### WI7 — Keep ideas independent of epic hierarchy (P1)

**Goal** Keep epic child relationships for implementation work and associate ideas through issue references.

**Supports** US3

**Blocked by** WI1

**Where** `internal/model/issue.go`, `tracker/create.go`, `tracker/edit.go`, `tracker/plan.go`, `tracker/apply.go`, `tracker/issue.go`, `consumer`

**Approach**
- Validate the resulting type and both ends of a relationship through create, edit, epic creation, and apply before the first write. Refuse changes that would put an idea on either end of a new parent relationship, give an idea an epic marker, or retype an issue that would retain a parent, children, or an epic marker as `idea`.
- On apply resume, validate pending parent relationships using the normalized types of both endpoints. Skip relationships recorded as wired in the verified checkpoint.
- Preserve existing relationships on reads. Where an issue with the idea type also has children or an epic marker, give epic classification precedence for reads and claims until a maintainer explicitly repairs the overlap. Allow maintainers to detach relationships or change the type to resolve existing overlaps.

**Done when**
- Create, edit, epic creation, and apply reject supported operations that would introduce an idea as either a parent or a child before any write.
- A resumed plan leaves a recorded relationship in place and only validates pending parent relationships. A pending attachment involving an idea fails before any write.
- Retyping an existing child, an issue with children, or a marked epic as `idea` fails while the conflicting relationship or marker would remain. An idea write that would add an epic marker is also refused.
- An issue that already has an idea type and meets the epic rule is displayed as an epic and remains ineligible even for an explicit discovery claim. Its stored relationships remain visible. An externally attached idea child can be detached. An issue reference between a standalone idea and an epic creates no child relationship.

### WI8 — Require explicit PR closure for ideas (P1)

**Goal** Let maintainers close an idea with a documentation PR while ordinary PR inference selects implementation work.

**Supports** US2, US3

**Blocked by** WI1

**Where** `pr/infer.go`, `pr/infer_test.go`, `pr/create_test.go`, `consumer`

**Approach**
- Exclude ideas from automatic issue inference through both the caller's claims and the branch number. Apply the same eligibility rule to the branch conflict check. Preserve explicit closing-target selection for an idea.

**Done when**
- A claimed idea and an idea named in a branch each fail to produce an automatic closing target on their own. A branch naming an idea does not veto inference of an otherwise eligible task claim.
- A documentation PR that explicitly names an eligible idea with `hew pr --for <n>` can include it as a closing target.

## Done When

- On a local tracker and a GitHub-backed tracker, create an idea and a comparable task. Find the idea through the idea list and Ideas page, and verify that only the task enters the default Ready queue.
- Verify that a plain claim leaves the idea unassigned and that an explicit investigation claim succeeds. Verify that the discovery claim leaves the idea out of default implementation selection.
- With a discovery claim and an implementation task claim, verify that automatic PR inference selects the task even when the branch names the idea. Verify that explicitly naming the idea on a documentation PR selects it.
- Using ordinary issue updates, record a decision and a document path on the idea and add references between it and an epic. Follow the issue references in hewd and verify that the idea stays outside the epic's child list.
- Park an idea by closing it with a reason. Verify that it leaves Open, that the reason is visible from Closed, and that reopening restores it to Open with the same number and history.
- Complete a separate idea using a different workflow's completion note. Verify that hew accepts closure without an epic and retains the supplied artifact references.
