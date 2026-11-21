package conventions

// The primer is assembled from shared and backend-specific pieces so the two
// variants cannot drift apart on the conventions and command surface they have
// in common. PrimerStatic and PrimerLocal are what ship; the pieces are not
// used on their own.
const (
	primerGitHubWorkflow = `Workflow: hew ready → hew start <n> --show → branch (feat/|fix/|chore/) → push → hew pr.
Implementation work is not done until its PR exists. Create the PR with hew pr before reporting done. Never work an epic directly.
Finish an investigation with hew set <n> --outcome "..." and hew close <n> --completed --reason "...", or use hew pr --for <n> for a documentation PR that closes the idea.`

	primerLocalWorkflow = `Workflow: hew ready → hew start <n> --show → do the work → hew close <n> --completed --reason "...".
Issues live in .hew at the main checkout, shared by every worktree; there are no pull requests, so close with --completed when the work is done.
Use hew close without --completed only for wontfix/duplicate. Never work an epic directly.`

	primerDedup = `Dedup before filing: hew search <terms> first (triaged, open+closed).
For untriaged matches, hew triage --search <terms> only when the user asked.
Read a candidate with show <n>. File with hew create ... --discovered-from <n>.`

	primerSandbox = `Sandboxed sessions may lack GitHub credentials. If gh works outside the sandbox, retry there.`

	// primerConventions runs from the conventions heading through the command
	// list up to the pr entry, which only exists on the GitHub backend.
	primerConventions = `Conventions (enforced by the tool's write path):
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
start <n> [--priority Pn --show --for-discovery] (--show or --json includes the body) | set <n> [--priority Pn --type T --add-area X --remove-area X --parent N --no-parent --title "..." --body-file F | --outcome "..."] |`

	primerPR = `pr [--for N --testing "..." --what "..." --why "..." --risk "..." --title "..." --ready] (push first; draft PR for claimed work.
  What/Why from issue; Testing: Before:/After: + checks; Risk: Reversibility:/Blast radius:. Repeat section flags for bullets. One "Fixes #n"; title prefix from type.) |`

	primerTail = `release <n> [--force] (give up a claim; --force releases any owner) |
close <n> --reason "..." [--completed | --duplicate-of M] [--force: close an epic with open sub-issues] | reopen <n> --reason "..." |
block <n> --on <m> | unblock <n> --from <m> |
epic create --title "..." [--children N,N --goal "..." --done-when "..." --body-file F] | epic status [<n> | --state open/closed/all] |
apply <plan.jsonl> [--dry-run] (batch create from a JSONL plan; schema: hew apply --help). All take --json.`
)

// PrimerStatic is the conventions-and-cheatsheet half of `hew prime`,
// kept deliberately terse: the whole primer targets ~1200 tokens (measured,
// see DESIGN.md's token-efficiency section).
const PrimerStatic = primerGitHubWorkflow + "\n" + primerDedup + "\n\n" + primerSandbox + "\n\n" +
	primerConventions + "\n" + primerPR + "\n" + primerTail

// PrimerLocal is the primer for a repo that tracks issues in a local `.hew`
// directory. It drops the pull-request step and the GitHub-only sandbox note,
// and completes work with `hew close --completed`.
const PrimerLocal = primerLocalWorkflow + "\n" + primerDedup + "\n\n" +
	primerConventions + "\n" + primerTail

// ClaudeSnippet is what `hew init` prints for the repo's CLAUDE.md: the
// point of prime is that the snippet stays this short.
const ClaudeSnippet = `## Issue tracking

Work is tracked in GitHub Issues via the ` + "`hew`" + ` CLI.
Run ` + "`hew prime`" + ` at session start — it prints the conventions,
the ready work, and current state. When in doubt: ` + "`hew ready`" + `.`

// ClaudeSnippetLocal is the same for a repo that tracks issues locally: the
// issues live in `.hew`, not GitHub, and the primer is still the entry point.
const ClaudeSnippetLocal = `## Issue tracking

Work is tracked locally in ` + "`.hew`" + ` via the ` + "`hew`" + ` CLI; there is no GitHub tracker.
Run ` + "`hew prime`" + ` at session start — it prints the conventions,
the ready work, and current state. When in doubt: ` + "`hew ready`" + `.`
