---
name: review-docs
description: Review documentation (README.md, agent-instructions files — CLAUDE.md, AGENTS.md, or the harness's equivalent — and .claude/rules/) for accuracy, drift against the codebase, and context cost. Use whenever the user asks to review or audit docs, check documentation, validate a README, or check whether agent instructions (CLAUDE.md, AGENTS.md) are still accurate — including when they just say "review the docs" without naming a file.
---

# Documentation Review

Review the documentation (README.md, the agent-instructions files — CLAUDE.md, AGENTS.md, or the harness's equivalent — and `.claude/rules/`) in scope.

> [!IMPORTANT]
> Consult [REFERENCE.md](REFERENCE.md) for the expected output format and level of detail.

You already know how to find bad documentation — stale references, missing prerequisites, a quick start that cannot work, a list that has drifted from the code it describes, magic values left unexplained. Apply that judgement directly and thoroughly; it is the bulk of the value here.

What follows is only the local policy you could not infer. Prescribing the review itself made reviews measurably worse: a longer version of this skill missed a quick start whose `npm install` could never succeed, because attention went to restating criteria instead of reading the docs.

## Arguments

Your input may carry flags alongside a path. Strip the flags first; whatever remains is the path.

- **`--json <path>`** — additionally write a findings file at that path, per [FINDINGS.md](FINDINGS.md). Off by default; the markdown report is unchanged either way.
- **`--inline`** — review in this session instead of delegating to subagents.
- **`--non-interactive`** — ask no questions. Where this skill would otherwise prompt, report the outcome and stop.

## Scope

Determine the review scope before discovering files. The discovery script ships in this skill's own `scripts/` directory — resolve it against this skill's directory rather than hardcoding an install path, which only holds for one harness's layout:

```bash
DISCOVER="<this skill's directory>/scripts/discover-files.sh"
```

- If a path was given, treat it as a path (file or directory) and run:
  ```bash
  bash "$DISCOVER" <path>
  ```
- If no path was given, scope to files added or modified on the current branch relative to the default branch:
  ```bash
  bash "$DISCOVER"
  ```

Handle the script's exit codes:
- **0 with output** — review the listed paths as input to the review.
- **0 with empty output** — nothing is in scope. Interactively, say so and ask which path to review; under `--non-interactive`, report `no_scope` and stop.
- **non-zero** — the script explains itself on stderr (path not found, not a git repo, on the default branch with no path, detached HEAD, or default branch indeterminate). Relay that message, then ask which path to review, or under `--non-interactive` report `error` with the message and stop.

A review that ran and found nothing, and a review that never ran, both end with zero findings and mean opposite things — so say which happened, in the report and in the findings file's `status`. A caller that cannot tell them apart reports a broken pipeline as a clean codebase.

The script returns paths language-blind. Keep README.md, CLAUDE.md, AGENTS.md,
AGENTS.override.md, `.claude/rules/` markdown, and the project's configured instruction
files. Identify configured sources from project configuration and the active session's
instruction sources: for example, Codex's `project_doc_fallback_filenames` and OpenCode's
`instructions` paths/globs in `opencode.json` or `opencode.jsonc`. Review configured files
in the requested scope even when their names differ from the conventional ones. Exclude
personal and auto-managed sources as described below.

Delegate by default: spawn subagents and give each a couple of files plus the severity and
"What not to flag" sections below. Review in this session only when `--inline` was given or
the harness cannot delegate. Run the validator below yourself, repo-wide, and fold its output
into the merged findings. Merge what comes back before collapsing patterns.

## Run the validator

Always repo-wide, regardless of branch scope — a broken reference outside the diff still breaks. The validator sits in this skill's own directory:

```bash
python3 "<this skill's directory>/scripts/validate-claude-md.py" . --json
```

Pass each additional shared instruction path/glob identified above with a repeatable
`--instructions` argument, resolved from the repository root. For a fallback filename, use
`--instructions '**/TEAM_GUIDE.md'` to include its nested instances. Missing configured files
are findings, not an empty review. Remote instruction URLs are reviewed as documentation
references rather than passed to the local validator.

The validator covers CLAUDE.md, AGENTS.md, and AGENTS.override.md by default, plus the
configured files you supply. It catches broken references, files over the 200-line target,
bad `paths` globs in `.claude/rules/`, and hardcoded local paths. `@path` imports are validated
in CLAUDE.md; elsewhere they are ordinary prose unless the project explicitly defines an
import convention. For that case, pass the file/glob with repeatable `--imports` instead of
`--instructions`. Explicit package/install wording distinguishes `@scope/package` mentions
from imports; standalone extensionless imports remain valid. Fold the validator's output
into your findings.

## Severity, and why it is strict here

These findings feed a work-tracking pipeline, so an inflated severity becomes false urgency in someone's backlog and teaches people to ignore the reviewer. Reviews without this anchor reliably drift upward — calling a broken link or a missing npm script P1.

- **P1** — security only: a missing auth step, a secret exposed in an example, a destructive command presented without warning.
- **P2** — broken: a command that errors, a path that does not exist, a quick start that fails on copy-paste, a reference or import pointing at a missing file.
- **P3** — stale, incomplete, or wasting context: drifted enumerations in either direction, missing prerequisites, no expected output, derivable agent-instructions content, an agent-instructions file over 200 lines.
- **P4** — polish.

A quick start that fails is P2, not P1. An agent-instructions file full of filler is P3, not P2. Cap the report at about ten findings; if you cut any, say how many.

## What not to flag

Each of these is a deliberate decision here, so flagging it spends the reader's attention arguing against their own convention:

- An agent-instructions file (CLAUDE.md, AGENTS.md) with no index table. Commands, invariants, and gotchas with no table is the preferred shape.
- A file that exists but is absent from an agent-instructions index. Indexes are curated pointers, not directory listings.
- A directory with no agent-instructions file, or a nested subdirectory with no README.
- A README that references a component whose agent-instructions file does not exist.
- Personal or auto-managed files — `MEMORY.md`, `CLAUDE.local.md`, anything under `~/.claude/` or another harness's personal-memory location. Do not review their content or suggest changes to them.
- A code defect rather than a documentation defect. An unfinished Dockerfile is not a docs finding; a README promising it works is.
- Anything under a `fixtures/`, `__fixtures__/`, or `testdata/` directory. Those documents are deliberately broken test input; their defects are the point.

## Agent-instructions files

The project's agent-instructions file — `CLAUDE.md`, `AGENTS.md`, or the harness's equivalent — loads into context on every session that touches its directory on the harnesses that read it, so ask of each line: **would removing it cause a mistake?** Keep commands that can't be guessed, conventions differing from tool defaults, invariants, and gotchas. A row that restates its own filename (`` `tests/` `` — "Test files") costs context on every load and prevents nothing: that is P3.

Some agent-instructions files here were written under an earlier convention that required an exhaustive index table, and they were correct when written. Report a systemically derivable index as **one** finding for the file — never one per row. Say that the convention changed so a maintainer does not read it as sloppiness, name the rows worth keeping because they disambiguate rather than enumerate, and trim the table rather than deleting the file.

## Output

Produce a report following the structure in [REFERENCE.md](REFERENCE.md). Each finding must include:

- **Priority** (P1/P2/P3/P4) in the H3 header
- **Location** (file:line, or just filename if no specific line)
- **Explanation** of the problem and why it matters
- **Fix** — concrete prescription of exactly what to change or add
- **Done when** — a criterion verifiable by reading the file. For errors: "The link at README.md:45 resolves to an existing file." For missing content: "The Quick Start section lists the minimum required Docker version with a link to the install page." NOT: "The docs are accurate."

When `--json <path>` was given, write the findings file described in [FINDINGS.md](FINDINGS.md) as well. Two fields need care because nothing downstream can recover them if they are wrong: `files` must list every path a finding touches, and `pattern` must be the slug that names its root cause, taken from the vocabulary in [REFERENCE.md](REFERENCE.md). Findings you collapsed together share a root cause, so they share a slug — that pairing is what lets a later run recognise this finding as one it has already reported.
