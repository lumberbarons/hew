# hew primer — lumberbarons/solar-controller

<!--
The conventions and command sections below are `conventions.PrimerStatic`
verbatim — keep them in sync when you change that constant. Only the state
sections (Ready/In progress/Blocked/Epics) are invented, standing in for a
repo with enough work in flight to show every section at once.
-->

Workflow: hew ready → hew start <n> → branch (feat/|fix/|chore/) → push → hew pr.
Close via PR; hew close is for wontfix/duplicate only. Never work an epic directly.
Dedup before filing: hew search <terms> (open+closed) first; hew list --json --bodies --state all when
exhaustiveness matters or search may be stale; show <n> only to read a specific candidate. Then
hew create ... --discovered-from <n>.

Conventions (enforced by the tool's write path):
- One priority label P0(critical)..P4(backlog), default P2; one type label bug|enhancement|task.
- Area labels sparingly — only once several issues would share one. No title prefixes; labels carry the metadata.
- Dependencies are native (--blocked-by), never body text. Epics are sub-issue trees.
- Bodies: ### Where / ### Problem or ### Goal / ### Fix or ### Approach / ### Done when (checklist). Omit empty sections.
- Missing priority renders P? and sorts last; issues missing priority/type are untriaged, not broken — triage them via hew set.
- start refuses claimed issues: exit 3 pick the next ready item, exit 5 the claim is yours — resume it. Untriaged issues need start --priority.

Output: one line per issue — #n priority type (areas) title [blocked by #m; epic done/total; in progress @user].
list sorts ready work first, then claimed, blocked, epics. Prefer text output; --json on list commands emits NDJSON.

Commands: ready [--limit N (0 for all)] | list [--label X --epic N --state open/closed/all --bodies (with --json)] |
show <n> | search <terms> | triage |
create --type T --title "..." --goal|--problem "..." --approach|--fix "..." --done-when "..." (repeatable)
  [--where X --priority Pn --area X --blocked-by N --parent N --discovered-from N] (--body-file F for long bodies) |
start <n> [--priority Pn] | set <n> [--priority Pn --type T --add-area X --remove-area X --parent N --no-parent --title "..." --body-file F] |
pr [--for N --testing "..." --what "..." --why "..." --title "..." --ready] (draft PR for the claimed issue; body
  composed from the issue, exactly one "Fixes #n"; title defaults to feat:|fix:|chore: from the type —
  push the branch first) |
close <n> --reason "..." [--completed | --duplicate-of M] | block <n> --on <m> | unblock <n> --from <m> |
epic create --title "..." [--children N,N --goal "..." --done-when "..." --body-file F] | epic status [<n>] |
apply <plan.jsonl> [--dry-run] (batch create from a JSONL plan; schema: hew apply --help). All take --json.

## Ready (8 of 19 open)
#117 P1 bug (tests)  Tautological assertions on state the code cannot modify
#120 P2 enhancement  Voltgo BLE battery controller: scaffold, client, collector
#119 P2 enhancement (web-ui)  Proper auth: login flow with sessions, API keys
#118 P2 enhancement (web-ui)  Simple bearer-token auth, prompting only when required
#126 P2 bug (collector)  Modbus reconnect loops forever on stale file descriptor
#131 P3 task (tests)  Golden-file tests for renderer output
#133 P3 task  Extract shared retry/backoff helper from collectors
#135 P4 enhancement (docs)  Architecture overview diagram in README

## In progress (2)
#124 P2 bug (tests)  /api/info verified by substring matching  @lumberbarons
#128 P1 bug (collector)  Victron collector drops readings during BLE rescan  @lumberbarons

## Blocked (4)
#121 P2 enhancement  Voltgo poller wiring  ← blocked by #120
#122 P2 enhancement  Voltgo web-ui cards  ← blocked by #120 #121
#127 P2 task (deploy)  Canary rollout for collector config  ← blocked by #126
#134 P3 task  Remove legacy /api/v0 endpoints  ← blocked by #119

## Epics
#137 Voltgo BLE battery controller support  1/6
#140 Epic: Auth and session hardening  0/4
