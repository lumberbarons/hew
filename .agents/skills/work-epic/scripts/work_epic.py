#!/usr/bin/env python3
"""Burn through a hew epic or a set of issues: the work-epic pump.

  work_epic.py <issue-number>... [--repo owner/name] [--workers N] [--resume] [--dry-run]

The scope is issue numbers; an epic number stands for its children. Runs the
whole loop — resolve, spawn, review-and-fix, repair, merge — inside the current
Orca-managed worktree and prints a report. Run it from the coordinator's
terminal: the Run, the mailbox, and the escalation card all belong to it.

When hew keeps the issues in a local `.hew` (and no --repo is given) there is no
GitHub: each issue's branch is reviewed, gated, and squashed into the local
default branch, and the issue is closed by the merge.

Exit: 0 drained · 10 stopped with decisions parked for a human · 3 halted on an
outage · 1 could not start · 2 usage error.
"""

import glob
import json
import os
import sys
import time
from pathlib import Path

import findings
import local_state
import pr_state
import pump
import resolve_ready
from orca_api import Orca, OrcaError, real_sh
from repo_state import (
    default_branch,
    local_default_branch,
    local_tracker,
    main_checkout,
    parse_scope,
)

USAGE = ("usage: work_epic.py <issue-number>... [--repo owner/name] [--workers N] "
         "[--resume] [--dry-run]")


def fail(msg, code=1):
    print(f"work_epic: {msg}", file=sys.stderr)
    sys.exit(code)


def parse_args(args):
    args = list(args)
    repo, workers, resume, dry, positional = None, 2, False, False, []
    while args:
        a = args.pop(0)
        if a == "--repo":
            if not args:
                fail("--repo needs owner/name", 2)
            repo = args.pop(0)
        elif a == "--workers":
            if not args or not args[0].isdigit() or int(args[0]) < 1:
                fail("--workers needs a positive integer", 2)
            workers = int(args.pop(0))
        elif a == "--resume":
            resume = True
        elif a == "--dry-run":
            dry = True
        else:
            positional.append(a)
    scope = parse_scope(positional)
    if scope is None:
        fail(USAGE, 2)
    return scope, repo, workers, resume, dry


def find_skill_dir(name, env_var):
    """An installed sibling skill's directory: the env override, the repo layout, or the plugin cache."""
    if os.environ.get(env_var):
        return os.environ[env_var]
    plugins = Path(__file__).resolve().parents[4]
    home = Path.home()
    candidates = [plugins / "hew" / "skills" / name]
    cache = sorted(glob.glob(str(home / ".claude/plugins/cache/*/hew/*/skills" / name)), reverse=True)
    candidates += [Path(c) for c in cache]
    candidates += [home / ".claude/skills" / name, home / ".agents/skills" / name]
    for c in candidates:
        if (c / "SKILL.md").exists():
            return str(c)
    fail(f"cannot find the {name} skill; set {env_var} to its directory", 1)


def dry_run(scope, repo, workers, resume):
    local = local_tracker(repo)
    if local:  # nothing is tracked yet, so nothing is delivered and no branch awaits a merge
        plan, prs = local_state.resolve(scope, {}, workers), local_state.plan(scope, {})
    else:
        plan = resolve_ready.resolve(scope, repo, resume, workers)
        prs = pr_state.plan(scope, repo)
    print(f"Run: work-epic {' '.join('#%d' % n for n in scope)}  (dry run: nothing created)"
          + ("  [local tracker]" if local else ""))
    slots = min(workers, plan["spawnBudget"])
    print(f"Would start up to {slots} of {len(plan['eligible'])} eligible ({workers} workers assumed free):")
    for e in plan["eligible"][:slots]:
        print(f"  we-{e['number']} · {pump.IMPLEMENTER}  #{e['number']} {e['title']}"
              f"{'  [large]' if e['large'] else ''}")
    for s in plan["skipped"]:
        print(f"  skipped #{s['number']}: {s['reason']} ({s['detail']})")
    print("Merge plan for delivered branches:" if local else "Merge plan for open PRs:")
    for p in prs["prs"]:
        print(f"  PR #{p['number']} (#{p['issue']}): {p['action']}" + (f" — {p['note']}" if p.get("note") else ""))
    if not prs["prs"]:
        print("  none")
    return 0


def exclude_scratch(coord_path):
    proc = real_sh(["git", "-C", coord_path, "rev-parse", "--path-format=absolute",
                    "--git-path", "info/exclude"])
    path = proc.out.strip()
    if proc.rc != 0 or not path:
        return
    os.makedirs(os.path.dirname(path), exist_ok=True)
    existing = Path(path).read_text().splitlines() if os.path.exists(path) else []
    if ".work-epic/" not in existing:
        with open(path, "a") as f:
            f.write(".work-epic/\n")


def main(argv):
    scope, repo, workers, resume, dry = parse_args(argv)
    if dry:
        return dry_run(scope, repo, workers, resume)

    orca = Orca()
    try:
        if not orca.runtime_ready():
            fail("the Orca runtime is not ready; open Orca first")
        coord = orca.current_worktree()
    except OrcaError as e:
        fail(f"this is not an Orca-managed worktree or Orca is unreachable: {e}")
    outdir = os.path.join(coord["path"], ".work-epic", "coordinator")
    os.makedirs(outdir, exist_ok=True)
    try:
        # prove the escalation surface is writable before spawning anything
        orca.worktree_set(coord["id"], comment="work-epic · starting", status="in-progress")
    except OrcaError as e:
        fail(f"cannot write the coordinator's worktree card: {e}")
    exclude_scratch(coord["path"])

    state_path = os.path.join(outdir, "state.json")
    if os.path.exists(state_path) and not resume:
        os.replace(state_path, os.path.join(outdir, f"state.{int(time.time())}.json"))

    work_issue = find_skill_dir("work-issue", "WORK_EPIC_WORK_ISSUE_DIR")
    raise_issues = find_skill_dir("raise-issues", "WORK_EPIC_RAISE_ISSUES_DIR")
    local = local_tracker(repo)
    state = pump.State(state_path)
    cfg = pump.Config(scope=scope, coord=coord, outdir=outdir, work_issue_dir=work_issue,
                      workers=workers, repo=repo, resume=resume, main=main_checkout(),
                      default=(local_default_branch() if local else default_branch(repo)) or "main",
                      local=local)
    filer = findings.Filer(real_sh, cwd=coord["path"], raise_dir=raise_issues, outdir=outdir, repo=repo)
    resolve_fn, plan_fn = ((local_state.resolver(state), local_state.planner(state)) if local
                           else (resolve_ready.resolve, pr_state.plan))
    p = pump.Pump(cfg, orca, real_sh, resolve_fn, plan_fn, filer, state)
    try:
        summary = p.run()
    except pump.StopRun as e:
        fail(str(e))
    p.stop_card(summary)
    with open(os.path.join(outdir, "report.json"), "w") as f:
        json.dump(summary, f, indent=2)
    print(pump.render_report(summary))
    return pump.EXIT_CODES[summary["status"]]


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
