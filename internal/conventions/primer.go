package conventions

// PrimerStatic is the conventions-and-cheatsheet half of `hew prime`,
// kept deliberately terse: the whole primer targets ~600 tokens.
const PrimerStatic = `Workflow: hew ready → hew start <n> → branch (feat/|fix/|chore/) → push → hew pr.
Close via PR; hew close is for wontfix/duplicate only. Never work an epic directly.
Dedup before filing: hew search <terms> (open+closed) first; hew list --json --bodies --state all when
exhaustiveness matters or search may be stale; show <n> only to read a specific candidate. Then
hew create ... --discovered-from <n>.

Sandboxed sessions may not access keychain-backed GitHub credentials. If writes report auth failure but gh works elsewhere, retry outside the sandbox.

Conventions (enforced by the tool's write path):
- One priority label P0(critical)..P4(backlog), default P2; one type label bug|enhancement|task.
- Area labels sparingly — only once several issues would share one. No title prefixes; labels carry the metadata.
- Dependencies are native (--blocked-by), never body text. Epics are sub-issue trees.
- Bodies: ### Where / ### Problem or ### Goal / ### Fix or ### Approach / ### Done when (checklist). Omit empty sections.
- Issues missing priority/type are untriaged, not broken: excluded from ready and prime as unvetted input, still shown by hew triage and hew list — label them via hew set. Missing priority renders P? and sorts last.
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
close <n> --reason "..." [--completed | --duplicate-of M] | reopen <n> --reason "..." |
block <n> --on <m> | unblock <n> --from <m> |
epic create --title "..." [--children N,N --goal "..." --done-when "..." --body-file F] | epic status [<n>] |
apply <plan.jsonl> [--dry-run] (batch create from a JSONL plan; schema: hew apply --help). All take --json.`

// ClaudeSnippet is what `hew init` prints for the repo's CLAUDE.md: the
// point of prime is that the snippet stays this short.
const ClaudeSnippet = `## Issue tracking

Work is tracked in GitHub Issues via the ` + "`hew`" + ` CLI.
Run ` + "`hew prime`" + ` at session start — it prints the conventions,
the ready work, and current state. When in doubt: ` + "`hew ready`" + `.`
