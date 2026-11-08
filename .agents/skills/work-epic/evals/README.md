# work-epic evals

There is no `evals.json` here, and that is deliberate.

## Why there is no behavioral eval set

`work-epic` acts. A run claims children in hew, creates Orca Tasks and worktrees, starts
supervised agents, pushes branches, and merges PRs. A with-skill and a baseline run against a
live epic would produce different work, on different commits, at different times — the queue is
different on the second pass, and the first pass already merged some of it. The results would
not be comparable, and cleaning up between runs is not possible without rewriting history.

`--dry-run` is the only part that can be exercised without side effects, and the decisions it
shows are already covered where they are made.

## What is tested

The policy layer: `resolve_ready.py` (which children, in what order) and `pr_state.py` (which
PRs merge, repair, update, or hold), plus `repo_state.py`'s parsers. `test_*.py`
pins every decision, skip reason, and parser case. Run them from the repo root:

    python3 -m unittest discover -s plugins/work-epic/skills/work-epic/scripts

CI runs the same command. The prose in `SKILL.md` and `REFERENCE.md` is not covered by these
tests, by design: it tells an orchestrator what to do, and the parts that could be checked are
the scripts it is told to run.

## Do not use skill-creator's trigger loop as a substitute

`/skill-creator`'s description optimization (`scripts/run_loop.py`, `scripts/run_eval.py`) looks
like an eval. It is not one for this skill, for two independent reasons.

**It costs a session, not a query.** Every prompt-run is a separate `claude -p` process — a
complete agent, not a classifier call. Launched from the plugin cache, `find_project_root()`
resolves to the operator's home, so each one inherits their global `CLAUDE.md` and every
configured MCP server. The defaults are `--runs-per-query 3`, `--num-workers 10`, and
`--max-iterations 5`: 60 full sessions per iteration, five iterations. Measured on 2026-10-26,
two runs spawned about 360 sessions, ~51 KB (~13K tokens) of system text each before tool
schemas, and spent a five-hour allowance in under five minutes. The loop does not stop at the
limit; the next `claude -p` exits 1, which reads as a crash.

**It cannot see the skill.** `run_eval.py` writes the description to
`<project_root>/.claude/commands/work-epic-skill-<id>.md` and scores a prompt as triggered only
when Claude's first tool call is `Skill` or `Read` naming that file. A command file is not an
installed skill, so the positive prompts mostly never fire. The same runs scored precision
100%, train recall 22% → 11% → 6% → 6%, and held-out recall 0% throughout, while the proposed
descriptions got broader and more explicit. That is the harness failing to trigger, not the
description failing to describe. Do not tune the description against those numbers, and do not
conclude the description is fine from them either.

## If you need a trigger check anyway

One prompt, one run, the cheapest model, and an isolated config — never the stock loop:

    CLAUDE_CONFIG_DIR=$(mktemp -d) claude -p "<one prompt>" --model <cheap-model>

Do not use `--runs-per-query 3`, `--num-workers 10`, or `--max-iterations 5`. Expect one
session. A trigger check says nothing about whether the pump works; it only says whether the
description was selected for one phrasing.

## The simulated harness, and what is still unproven

`scripts/test_pump.py` is the harness this section used to ask for: the real pump loop, the real
decision tables, and a simulated Orca, GitHub, and tracker (`World`), with scenarios for the happy
path, review and fix, holds, repairs, halts, stalls, resume, and the mailbox, asserting over the
command log (one worktree per issue, the reviewer in that same worktree, no merge without a review
round, every Delivery acknowledged). It runs in CI like the other tests.

What it cannot prove is that Orca behaves as the fake does. `test_orca_api.py` pins the shapes
captured from a live Orca 1.4.x, but the `worker-start` receipt and the settlement of real agents
come from past runs, not from exercising this code. The first real epic is the live test; run it with
`--workers 1` on a small, low-risk scope and watch the log.
