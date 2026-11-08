# Reference

Findings vocabulary for `review-portability`: the pattern slugs, severity ladder, and the distinction
between coupling that breaks a skill elsewhere and harness flavour that does not.

## Scope

The target is one or more skill directories. Review every file a skill ships: `SKILL.md`,
`REFERENCE.md`, `README.md` files, and scripts (`scripts/*`, `assets/*`). Frontmatter,
prose, code blocks, and script contents are all in scope — a hardcoded `/Users/jim/...` in a
bash block breaks as surely as one in prose.

The review is file-based, not diff-based: run it against a path, not a branch range.

## Severity ladder

- **P1** — the skill breaks or silently misbehaves when another agent runs it: a path that only
  resolves on one machine or under one harness's install layout, a script invocation the agent
  cannot resolve, instructions that register work in a file the running agent never reads.
- **P2** — the skill degrades: references to an invocation form or tool the agent does not have,
  examples the agent cannot follow, guidance that assumes one harness's behaviour where another's
  differs. The skill still runs, but worse, or teaches the agent to do something wrong elsewhere.
- **P3** — harness flavour with no behavioural consequence: a nonstandard frontmatter field a
  harness ignores, a harness name used purely as an example where any would do.

Severity follows what happens on the *other* harness, not how wrong the text looks. A
`${CLAUDE_PLUGIN_ROOT}` in a skill nobody runs outside Claude Code is still P1 by this rule —
the marketplace serves three agents — but judge the blast radius honestly when reporting.

## Pattern slugs

The `pattern` field names the root cause. Pick from this list; use `other-<slug>` when nothing
fits, and treat a recurring `other-` slug as the signal to extend this vocabulary.

- **`slash-command-ref`** — the skill refers to another skill by its slash-command form
  (`/plugin:skill`). Slash prefixes are one harness's invocation syntax; agents that resolve
  skills by name see a string that names nothing. Fix: "the `<skill>` skill". A one-line note
  explaining the harness's prefix convention is the correct replacement, not deletion.
- **`arguments-variable`** — `$ARGUMENTS` (or any harness's equivalent placeholder) appears as
  though it is always populated or has universal meaning. Other agents pass the same text as
  ordinary input. Fix: "your input", "the request", "the path you were given".
- **`plugin-root-variable`** — `${CLAUDE_PLUGIN_ROOT}` or a hardcoded install path
  (`.claude/skills/...`, `~/.config/opencode/skills/...`) reaches a plugin file. Every harness
  lays plugins out differently. Fix: resolve against the skill's own directory
  (`"<this skill's directory>/..."`), which is true everywhere the skill runs — provided the file
  is inside that directory; if it is not, the finding is `outside-skill-path` as well.
- **`outside-skill-path`** — a path that climbs out of the skill's own directory to reach a file
  the plugin keeps beside its skills (`<this skill's directory>/../../scripts/x.sh`, a
  `[FINDINGS.md](../../../apm_modules/lumberbarons/lumber-mart/plugins/critique/FINDINGS.md)` link, "the other skill's directory"). Per-skill installers
  (APM, anything following the Agent Skills spec) copy the skill directory and nothing around it,
  so the file is simply not there. Fix: ship a copy inside the skill directory and resolve it from
  there, keeping one source and syncing the copies rather than editing them by hand. Symlinks are
  not a fix — installers filter them.
- **`absolute-user-path`** — a machine-specific absolute path (`/Users/<name>/...`,
  `~/.apm/...`, `/home/<name>/...`). Breaks everywhere but one machine. Fix: resolve against the
  skill directory or the repo.
- **`single-harness-tool`** — a tool name only one agent provides (`AskUserQuestion`,
  `TodoWrite`, `worktree_checkout`) used as the instruction. Fix: describe the capability
  ("ask in a single round, recommended option first"); per-harness tool names may follow in
  parentheses.
- **`single-harness-example`** — an example that only one harness's users can follow and the
  instruction depends on it: branch-naming conventions from one worktree implementation, one
  harness's session hook, one harness's compaction behaviour. Fix: name two harnesses, or make
  the example generic. Distinct from the deliberate case below.
- **`single-file-registration`** — instructions to register project state (ADRs, conventions)
  in exactly one agent-instructions file. Fix: cover `AGENTS.md` and `CLAUDE.md`, or say "the
  project's root agent-instructions file".
- **`harness-exclusive-claim`** — a factual claim about what agents read, compact, or inject
  that is only true of one ("Claude Code always reads CLAUDE.md", "Claude Code compacts rather
  than failing"). Fix: state the general behaviour ("an agent compacts its context...").
- **`harness-hook-mechanism`** — session-start/context-injection instructions wired to one
  harness's hook mechanism as if it were the only one. Fix: name the capability and the
  per-harness mechanisms, or defer to a tool that manages it (`hew hooks`).

## What not to flag

- **Deliberate multi-harness skills.** A skill that switches behaviour *by harness* — prompt
  templates with separate opencode and codex variants, an argument that selects a kind — is the
  correct shape, not a finding. The test is whether the skill *works* on each harness it names;
  naming several comparatively is how a portable skill talks. Only flag harness references that
  are exclusive by accident.
- **Harness notes in READMEs that explain the difference** ("Claude Code prefixes the plugin
  name") — that is documentation of a real difference, phrased neutrally.
- **Bash in scripts and code blocks.** Bash is the one universal shell assumption across these
  agents; `#!/usr/bin/env bash` is not coupling.
- **`argument-hint` and other extra frontmatter.** Harnesses that do not know a field ignore
  it. That is P3-flavour at most, and usually nothing.
- **Markdown callouts (`> [!IMPORTANT]`)** — rendered or readable in every harness that matters
  here. Not coupling.
- **Fixtures, evals, and workspace directories** — deliberately broken or benchmark-specific
  input; their harness references are the point. Skip anything under `fixtures/`, `evals/`,
  `*workspace*/`.
- **Skill text telling the agent to ask the user a question in prose.** That is portable. Only
  the named-tool form is coupling.

## Report format

Same structure as the other critique skills: header line (`N files reviewed, M issues found`),
then one finding per issue — H3 with priority tag, location, explanation, fix, and a done-when
criterion verifiable by reading the file ("No file in scope references another skill by slash
command" — not "the skill is portable"). Cap at about ten findings; group one systemic problem
(one template repeated across six skills, say) as one finding listing every location, never one
finding per copy.

When `--json <path>` was given, write the findings file per [FINDINGS.md](FINDINGS.md)
with `skill: "portability"`. `files` lists every file the finding touches — for a template repeated
across skills, every file carrying it, because consumers derive the finding's identity from that
list.
