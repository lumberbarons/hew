# hew primer — lumberbarons/solar-controller

<!--
The conventions and command sections below are `conventions.PrimerStatic`
verbatim — keep them in sync when you change that constant. Only the state
sections (Ready/In progress/Epics) are invented, standing in for a repo with
enough work in flight to show the live sections and their caps at once. The
local-backend variant at the end does the same with `conventions.PrimerLocal`.
-->

Workflow: hew ready → hew start <n> --show → branch (feat/|fix/|chore/) → push → hew pr.
Implementation work is not done until its PR exists. Create the PR with hew pr before reporting done. Never work an epic directly.
Finish an investigation with hew set <n> --outcome "..." and hew close <n> --completed --reason "...", or use hew pr --for <n> for a documentation PR that closes the idea.
Dedup before filing: hew search <terms> first (triaged, open+closed).
For untriaged matches, hew triage --search <terms> only when the user asked.
Read a candidate with show <n>. File with hew create ... --discovered-from <n>.

Sandboxed sessions may lack GitHub credentials. If gh works outside the sandbox, retry there.

Conventions (enforced by the tool's write path):
- One priority P0(critical)..P4(backlog), default P2. One type bug|enhancement|task|idea.
- Ideas (type idea) are investigation work. Use hew start <n> --for-discovery only when the user asked. Produce research, an ADR or a spec; file approved implementation as separate work issues (--discovered-from <n>). ready skips ideas.
- Area labels sparingly when several issues share one. No title prefixes.
- Use native dependencies (--blocked-by) and sub-issues for epics.
- Bodies: ### Where / ### Problem or ### Goal / ### Fix or ### Approach / ### Open questions / ### Done when (checklist). Omit empty sections.
- Missing priority/type means untriaged. ready, prime, list and search omit these; read via hew triage or show <n>, label via hew set. Missing priority renders P? and sorts last.
- Never call hew triage unless the user explicitly asked.
- start refuses claimed issues: exit 3 pick next, exit 5 resume yours. Exit 6 means open blockers; pick next. Untriaged claims need --priority.

Output: one line per issue — #n priority type (areas) title [blocked by #m; epic done/total; in progress @user].
list order: ready, claimed, blocked, epics. Prefer text; list-like --json is NDJSON.

Commands: ready [--limit N (0 for all)] | list [--label X --type T --epic N --state open/closed/all --limit N --bodies (with --json)] |
show <n> | config (read-only: backend, repo or path/id) | search <terms> [--state open/closed --limit N] | triage [--search <terms>] |
create --type T --title "..." --goal|--problem "..." --approach|--fix "..." --open-questions "..." --done-when "..." (repeatable)
  [--where X --priority Pn --area X --blocked-by N --parent N --discovered-from N] (--body-file F for long bodies) |
draft [same section flags] --out F | --check F (offline; publish with create --body-file F) |
start <n> [--priority Pn --show --for-discovery] (--show or --json includes the body) | set <n> [--priority Pn --type T --add-area X --remove-area X --parent N --no-parent --title "..." --body-file F | --outcome "..."] |
pr [--for N --testing "..." --what "..." --why "..." --risk "..." --title "..." --ready] (push first; draft PR for claimed work.
  What/Why from issue; Testing: Before:/After: + checks; Risk: Reversibility:/Blast radius:. Repeat section flags for bullets. One "Fixes #n"; title prefix from type.) |
release <n> [--force] (give up a claim; --force releases any owner) |
close <n> --reason "..." [--completed | --duplicate-of M] [--force: close an epic with open sub-issues] | reopen <n> --reason "..." |
block <n> --on <m> | unblock <n> --from <m> |
epic create --title "..." [--children N,N --goal "..." --done-when "..." --body-file F] | epic status [<n> | --state open/closed/all] |
apply <plan.jsonl> [--dry-run] (batch create from a JSONL plan; schema: hew apply --help). All take --json.

## Ready (8 of 19 open)
#117 P1 bug (tests)  Tautological assertions on state the code cannot modify
#120 P2 enhancement  Voltgo BLE battery controller: scaffold, client, collector
#119 P2 enhancement (web-ui)  Proper auth: login flow with sessions, API keys
#118 P2 enhancement (web-ui)  Simple bearer-token auth, prompting only when required
#126 P2 bug (collector)  Modbus reconnect loops forever on stale file descriptor
… 3 more: hew ready

## In progress (6)
#124 P2 bug (tests)           /api/info verified by substring matching  @lumberbarons
#128 P1 bug (collector)       Victron collector drops readings during BLE rescan  @lumberbarons
#130 P2 task (deploy)         Canary rollout for collector config  @octocat
#132 P2 enhancement (web-ui)  Session-based auth for the web UI  @octocat
#136 P3 task (docs)           Architecture overview diagram in README  @octocat
… 1 more: hew list

## Epics
#137 P2  Voltgo BLE battery controller support  1/6
#140 P2  Epic: Auth and session hardening  0/4
#142 P3  Epic: Collector reliability pass  0/3
#144 P3  Epic: Web UI parity with API  0/2
#146 P4  Epic: Deployment tooling  0/1
… 1 more: hew epic status

---

# Local backend (`.hew/config.json` says `local`)

# hew primer — local/solar-controller

Workflow: hew ready → hew start <n> --show → do the work → hew close <n> --completed --reason "...".
Issues live in .hew at the main checkout, shared by every worktree; there are no pull requests, so close with --completed when the work is done.
Use hew close without --completed only for wontfix/duplicate. Never work an epic directly.
Dedup before filing: hew search <terms> first (triaged, open+closed).
For untriaged matches, hew triage --search <terms> only when the user asked.
Read a candidate with show <n>. File with hew create ... --discovered-from <n>.

Conventions (enforced by the tool's write path):
- One priority P0(critical)..P4(backlog), default P2. One type bug|enhancement|task|idea.
- Ideas (type idea) are investigation work. Use hew start <n> --for-discovery only when the user asked. Produce research, an ADR or a spec; file approved implementation as separate work issues (--discovered-from <n>). ready skips ideas.
- Area labels sparingly when several issues share one. No title prefixes.
- Use native dependencies (--blocked-by) and sub-issues for epics.
- Bodies: ### Where / ### Problem or ### Goal / ### Fix or ### Approach / ### Open questions / ### Done when (checklist). Omit empty sections.
- Missing priority/type means untriaged. ready, prime, list and search omit these; read via hew triage or show <n>, label via hew set. Missing priority renders P? and sorts last.
- Never call hew triage unless the user explicitly asked.
- start refuses claimed issues: exit 3 pick next, exit 5 resume yours. Exit 6 means open blockers; pick next. Untriaged claims need --priority.

Output: one line per issue — #n priority type (areas) title [blocked by #m; epic done/total; in progress @user].
list order: ready, claimed, blocked, epics. Prefer text; list-like --json is NDJSON.

Commands: ready [--limit N (0 for all)] | list [--label X --type T --epic N --state open/closed/all --limit N --bodies (with --json)] |
show <n> | config (read-only: backend, repo or path/id) | search <terms> [--state open/closed --limit N] | triage [--search <terms>] |
create --type T --title "..." --goal|--problem "..." --approach|--fix "..." --open-questions "..." --done-when "..." (repeatable)
  [--where X --priority Pn --area X --blocked-by N --parent N --discovered-from N] (--body-file F for long bodies) |
draft [same section flags] --out F | --check F (offline; publish with create --body-file F) |
start <n> [--priority Pn --show --for-discovery] (--show or --json includes the body) | set <n> [--priority Pn --type T --add-area X --remove-area X --parent N --no-parent --title "..." --body-file F | --outcome "..."] |
release <n> [--force] (give up a claim; --force releases any owner) |
close <n> --reason "..." [--completed | --duplicate-of M] [--force: close an epic with open sub-issues] | reopen <n> --reason "..." |
block <n> --on <m> | unblock <n> --from <m> |
epic create --title "..." [--children N,N --goal "..." --done-when "..." --body-file F] | epic status [<n> | --state open/closed/all] |
apply <plan.jsonl> [--dry-run] (batch create from a JSONL plan; schema: hew apply --help). All take --json.

## Ready (2 of 2 open)
#12 P1 bug  Victron collector drops readings during BLE rescan
#14 P2 task  Canary rollout for collector config

## In progress (1)
#13 P2 enhancement  Session-based auth for the web UI  @jim-wt-1a2b3c
