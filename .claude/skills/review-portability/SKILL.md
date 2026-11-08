---
name: review-portability
description: Review skill files (SKILL.md, REFERENCE.md, shipped scripts) for agent-harness coupling that breaks or degrades them when a different coding agent runs them — slash-command references, $ARGUMENTS, plugin-root paths, machine-specific paths, single-harness tool names and examples. Use when asked to review a skill, check a skill is agent-agnostic or portable, make a skill work across Claude Code, opencode, and codex, audit skills/ for harness assumptions, or before shipping a skill to agents beyond the one it was written in.
---

# Skill Portability Review

Review skill files for coupling to one coding agent — constructs that break, degrade, or teach
the wrong thing when another agent (Claude Code, opencode, codex) runs the skill. This
marketplace serves all three, so "works on the harness I wrote it on" is not done; the goal of
the review is a skill that is agent-agnostic — one text, same behaviour, whichever agent loads
it.

> [!IMPORTANT]
> Consult [REFERENCE.md](REFERENCE.md) for the pattern vocabulary, severity ladder, and what not
> to flag. The not-flagging list is load-bearing: naming harnesses comparatively is how a
> portable skill talks, and flagging that drowns real findings.

## Arguments

Your input may carry flags alongside a path. Strip the flags first; whatever remains is the path
— a skill directory, a plugin's `skills/` directory, or a single file.

- **`--json <path>`** — additionally write a findings file at that path, per
  [FINDINGS.md](FINDINGS.md) with `skill: "portability"`. Off by default.
- **`--inline`** — review in this session instead of delegating to subagents.
- **`--non-interactive`** — ask no questions. Where this skill would otherwise prompt, report
  the outcome and stop.

## Scope

If no path was given, ask which skill to review; under `--non-interactive`, report `no_scope`
and stop. Unlike the other review skills, scope is file-based — no branch discovery, no diff.
Review every file the target ships: frontmatter, prose, fenced code blocks, and script contents.

A review that ran and found nothing, and a review that never ran, both end with zero findings
and mean opposite things — say which happened, in the report and in the findings file's `status`.

Delegate by default: spawn subagents and give each a couple of files plus the pattern
vocabulary, severity ladder, and not-flagging list from [REFERENCE.md](REFERENCE.md). Review in
this session only when `--inline` was given or the harness cannot delegate. Merge what comes
back before collapsing by root cause.

## Method

Work file by file, but collapse findings by root cause, not by occurrence — a template copied
into six skills is one finding with six locations. For each file:

1. **Scan for the mechanical constructs first** — slash-command forms (`/plugin:skill`),
   `$ARGUMENTS`, `${CLAUDE_PLUGIN_ROOT}`, install paths (`.claude/`, `~/.config/opencode/`),
   machine-absolute paths (`/Users/...`, `~/.apm/...`), and paths that climb out of the skill's
   own directory (`../`). These are cheap to grep and mostly P1.
2. **Then read for the judgement calls** — tool names one harness owns, examples only its users
   can follow, claims about what agents read or do that are true of one harness, registration
   instructions aimed at one instructions file. Consult the [REFERENCE.md](REFERENCE.md)
   vocabulary and, critically, its not-flagging list before writing a finding down.
3. **Verify the fix is real.** For every finding, confirm the replacement phrasing actually
   works on the harnesses you are fixing for — "resolve against this skill's own directory"
   only fixes `${CLAUDE_PLUGIN_ROOT}` if the referenced file is inside the skill's own directory,
   because a per-skill installer copies that directory and nothing around it.

Severity follows what happens on the other harness (P1 breaks it, P2 degrades it, P3 is flavour)
— the ladder and boundary cases are in [REFERENCE.md](REFERENCE.md).

## What not to flag

Deliberate multi-harness design is the correct shape, not a finding: a skill with separate
opencode/codex prompt variants, a README explaining how invocation differs per harness, bash
everywhere. The vocabulary's "what not to flag" section is the arbiter — read it before
reporting, and when a construct could be either coupling or deliberate comparison, decide from
whether the skill works on each harness it names.

## Output

Produce a report following the structure in [REFERENCE.md](REFERENCE.md). Each finding must
include:

- **Priority** (P1/P2/P3) in the H3 header
- **Location** (file:line, or just filename)
- **Explanation** — which agent breaks or degrades, and how
- **Fix** — the concrete replacement text or resolution strategy
- **Done when** — verifiable by reading the file. "No file in scope references another skill by
  slash command." NOT: "the skill is portable."

When `--json <path>` was given, write the findings file described in
[FINDINGS.md](FINDINGS.md) as well. `files` must list every file a finding touches — a
template repeated across skills is one finding naming all of them, because that list is what
downstream consumers derive the finding's identity from.
