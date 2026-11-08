---
name: review-policy
description: Reviews a code change against the accepted Architectural Decision Records (ADRs) in the project, flagging violations, erosions of invariants, and drift toward rejected alternatives. Use when the user asks to check a change against ADRs, verify a PR respects architectural decisions, audit whether a diff honors policy, or phrases like "does this respect our ADRs", "is this change on-policy", "will this violate ADR-0007", "review this branch against our decisions", "check policy compliance". Also invoke proactively before merging a PR that touches code areas governed by prior ADRs.
---

# Policy Review

Review a code change against the project's accepted ADRs. An ADR is policy: drivers, chosen option, rejected alternatives, and invariants the decision asked future code to respect. This skill reads the change, identifies which ADRs apply, and reports where the change **violates**, **erodes**, or **drifts toward rejected options** — and, secondarily, where a driver may have **shifted** enough that the ADR itself should be revisited.

This skill does **not** grade code quality, test coverage, or style — those belong to `review-code`, `review-tests`, and `review-docs`. It judges the change *against recorded decisions*.

> [!IMPORTANT]
> Consult [REFERENCE.md](REFERENCE.md) for the expected output format and level of detail.

## User Input

The input may carry the change scope (a ref range, PR, or path) and an ADR filter. You **MUST** consider the user input before proceeding (if any was given).

## Argument Parsing

Parse the input for:
- **Change scope** (optional): a git ref range (`main..HEAD`), a PR URL/number, a path, or empty. Default: working diff against the base branch (`main` or `master`, whichever the repo uses).
- **ADR filter** (optional): a specific ADR id like `ADR-0007`, or a tag like `storage`. Default: all accepted ADRs.

If the scope is ambiguous, ask once, then proceed.

## Prerequisites

- The repo has an ADR directory — check `docs/adr/`, `docs/decisions/`, `docs/adrs/` in that order. If none exists, stop and tell the user there are no ADRs to review against.
- The repo is a git repo (the skill reads the diff via git). If not, ask the user to point at a specific path and skip diff-based analysis.

## Philosophy — what counts as a finding

ADRs record decisions. A finding must trace back to a specific claim in a specific accepted ADR — not to the reviewer's taste. If you cannot cite the ADR id and the section of the ADR that the change contradicts, omit the finding.

There are four categories of finding, and they are **not** interchangeable:

1. **Violation** — the change does the thing an ADR explicitly rejected, or fails the invariant the ADR required. The strongest claim; use only when the mismatch is textual (the ADR said X, the change does not-X).
2. **Erosion** — the change weakens an invariant the ADR required without breaking it outright. The ADR said "every service must emit a `request_id`"; the change adds a new handler that does not. The decision still stands, but this change digs a hole under it.
3. **Drift** — the change moves the codebase toward a rejected option without crossing the line. Often a P3 — the author may not realize the path they're on. Example: ADR chose Postgres for the event store and rejected DynamoDB; the change introduces a new DynamoDB-backed store "just for this feature".
4. **Driver shift** — the change reveals that one of the ADR's decision drivers no longer holds. Not a complaint about the change; a signal that the *ADR* is a candidate for revisit. Example: ADR-0003 cited "team has no Kafka experience" as a driver; the change is by a team member who just shipped a Kafka integration. The ADR's premise has eroded.

Only **accepted** ADRs bind. `proposed` ADRs are not yet policy; `superseded` ones are history. Ignore both unless the user explicitly asks.

Be honest about ambiguity. If the ADR is vague on the point the change touches, say so — "ADR-0007 does not speak to this case; flagging as a judgement call, not a violation." Hallucinated ADR intent is worse than no finding.

## Workflow

### Step 1 — Resolve the change scope

Turn the input into a concrete diff and a file list:

- Empty → `git diff <base>...HEAD` where `<base>` is the default branch
- Git ref range (`a..b`, `a...b`) → `git diff <range>`
- PR URL / number → `gh pr diff <n>` and `gh pr view <n> --json files`
- Path → treat as current-state review of that path; note in the report that this is not a diff review
- Bare commit sha → `git show <sha>`

Record:
- The diff itself (file + line ranges + hunk content)
- The list of changed files
- The base ref the diff is relative to

If the diff is empty, stop and tell the user there's nothing to review.

### Step 2 — Load the ADR corpus

Read every ADR file in the ADR directory. For each, extract:

- `id`, `title`, `status`
- `tags`
- `supersedes`, `superseded-by`
- The Decision Outcome (chosen option) and Considered Options (rejected ones)
- The Decision Drivers
- The Consequences (especially "Negative" — these are the invariants the ADR accepted and expects code to respect)

Filter to ADRs with `status: accepted`. Do not bind against `proposed` or `superseded`. If the user passed an ADR filter (specific id or tag), apply it now.

### Step 3 — Score ADR relevance to the change

Not every ADR applies to every change. Spending budget analyzing irrelevant ADRs produces false positives and wastes tokens. Score each accepted ADR against the change using a cheap signal pass:

- **Path overlap** — does the ADR's text (title, problem statement, implementation notes) name directories, modules, services, or filenames that appear in the changed file list?
- **Tag alignment** — does the change touch a domain the ADR's tags cover (e.g., tag `storage` and the diff touches `db/`, `repository/`, `*migrations*`)?
- **Keyword alignment** — does the diff introduce names/APIs/imports the ADR explicitly discussed (libraries, services, config keys)?

Classify each ADR as:
- **Applicable** — at least one strong signal. Review in Step 4.
- **Possibly applicable** — weak signal only. Review in Step 4 but note the weak link.
- **Out of scope** — no signal. Skip; mention count in the report summary.

Record the applicable/possibly-applicable set and *why* each made the list. Include this in the final report's header so the user can sanity-check that nothing relevant was skipped.

### Step 4 — Choose execution strategy

- **1–2 applicable ADRs → Direct mode**: evaluate the change against each ADR inline, then proceed to Synthesis.
- **3+ applicable ADRs → Parallel mode, when delegation is available**: spawn one subagent per ADR (or batch two ADRs per subagent if there are many), collect results, merge. If this session cannot delegate, review each ADR sequentially in direct mode and use the same synthesis and report format. Do not skip ADRs or stop the review because delegation is unavailable.

### Parallel Review Mode

Use this mode when 3 or more applicable ADRs are discovered and this session supports delegation.

#### Spawn subagents

Use the running agent's available delegation capability. Start independent reviewers together where supported so they run in parallel; the exact tool and batching mechanism depend on the harness.

Each subagent prompt MUST include:

1. The ADR file path(s) it is reviewing against — instruct it to read them in full
2. The diff scope (either the actual diff, or paths + a note to obtain the diff via `git diff <base>...HEAD`)
3. The **Finding Categories** section from this skill — copy it verbatim
4. The **Severity** section from this skill — copy it verbatim
5. The explicit instruction: **"You are reviewing the change only against the ADR(s) you were given. Do not flag issues unrelated to these ADRs. Do not grade code quality, tests, or style."**
6. The explicit instruction: **"Every finding must cite: (a) the ADR id, (b) the specific section or claim in the ADR the finding traces to, and (c) the file:line in the change. If you cannot supply all three, omit the finding."**
7. The explicit instruction: **"If the ADR is silent on the point the change touches, say so in the report summary rather than invent a rule."**

Instruct each subagent to return findings in this exact delimited format:

```
---FINDING---
priority: P<1|2|3>
category: <violation|erosion|drift|driver-shift>
adr: <ADR-NNNN>
adr_section: <which section of the ADR this traces to — e.g., "Decision Outcome", "Consequences > Negative", "Decision Drivers > #3">
location: <file:line(s) in the change>
title: <short title>
explanation: <what the change does, what the ADR asked for, why they conflict>
fix: <concrete prescription — revise the change, amend the ADR, or escalate for revisit>
done_when: <verifiable criterion>
---END---
```

If no findings, the subagent returns `---NO-FINDINGS---` plus a one-line note on why (either the ADR is not actually engaged by the change, or the change is on-policy).

#### Collect and merge

1. Parse each subagent's structured findings
2. Combine into a single list, sorted by priority (P1 first), then by category (violation > erosion > drift > driver-shift)
3. Deduplicate: if two findings share the same `adr` *and* the same `location`, keep the higher-priority one
4. Preserve the per-ADR "no findings" notes — the final report should list every applicable ADR with either its findings or an explicit "on-policy" note

#### Error fallback

If a subagent fails or returns unparseable output, review that ADR directly (as in direct mode) and include a note: `Note: ADR-NNNN reviewed directly due to subagent failure.`

### Step 5 — Synthesis

Both direct and parallel modes flow into synthesis before emitting the report.

- **Collapse within an ADR**: multiple hunks that violate the same ADR in the same way should be one finding with a location list, not N findings. Severity is the max of the collapsed set.
- **Do NOT collapse across ADRs**: two findings that happen to touch the same file but trace to different ADRs must stay separate — they carry different remediation paths.
- **Driver-shift findings always stand alone** — they are not about the change but about the ADR's premise. Put them in their own section of the report.

## Severity

- **P1 — Violation of an accepted ADR.** The change does what the ADR explicitly rejected, or removes an invariant the ADR required. These must block merge unless the author supersedes the ADR first. Example: ADR-0004 accepted "all PII fields encrypted at rest"; the change introduces a new PII column with no encryption.

- **P2 — Erosion of an invariant, or significant drift toward a rejected option.** The ADR is not textually violated but the change weakens the decision's effectiveness, or introduces a foothold for the rejected path. Example: ADR-0007 chose Postgres for the event store; the change adds a DynamoDB-backed "side store" for one feature. This is the most useful category in practice — catches architectural drift before it becomes irreversible.

- **P3 — Minor drift, or driver-shift candidate.** Worth flagging but not blocking. For drift: the change nudges the codebase toward a rejected option but is defensible on its own. For driver-shift: an ADR premise has eroded; the ADR is a candidate for revisit via `create-adr` (supersede flow), but this change can ship. Every P3 finding **must** state *why* it matters — "this is the third new service this quarter to skip the shared retry wrapper; the ADR-0003 invariant is fraying."

If you cannot state a concrete consequence grounded in the ADR, omit the finding.

## Output

Produce a report following [REFERENCE.md](REFERENCE.md). The report begins with:

1. **Change scope** — diff range, file count, line count
2. **ADRs considered** — applicable / possibly applicable / out of scope, with counts
3. **Summary line** — "N findings (A P1, B P2, C P3); D ADRs reviewed; E ADRs on-policy"

Then the findings themselves, sorted by priority then category. Each finding MUST include the ADR id, the ADR section it traces to, the file:line in the change, the explanation, the fix, and the done-when criterion.

End with a **Driver Shifts** section if any driver-shift findings were produced — this is a prompt to the user to consider opening a supersede flow on the named ADR, not a complaint about the change.

If no findings: say so plainly. List the ADRs reviewed and state the change is on-policy. Do not invent findings to fill space.
