# Reference

Outcome file, quality gates, branch and commit conventions, and a worked example for
`work-issue`.

## Outcome file

What `--json <path>` writes. One object per invocation; `issues` holds one entry per issue the
run touched, which is a single entry unless `--batch` was given.

```json
{
  "skill": "work-issue",
  "status": "delivered",
  "batch": { "ceiling": 3, "delivered": 3, "remaining": 7 },
  "integration": {
    "branch": "integration/batch-42-51",
    "merged": ["fix/42-write-commands-guard-closed", "fix/47-...", "fix/51-..."],
    "gate": "go build ./... && go test ./... — passed",
    "result": "passed"
  },
  "issues": [
    {
      "number": 42,
      "type": "bug",
      "title": "Write commands guard against closed targets",
      "outcome": "delivered",
      "branch": "fix/42-write-commands-guard-closed",
      "pr": 57,
      "done_when": { "total": 3, "verified": 3 },
      "tests_added": 3,
      "tdd": "applied",
      "gate": "go build ./... && go test ./... — passed",
      "discovered": [58]
    }
  ],
  "skipped": { "claimed": 1, "untriaged": 2, "blocked": 0 }
}
```

| Field | Notes |
|---|---|
| `status` | Run-level: `delivered`, `integration_failed`, `failed`, `no_ready_work`, `not_eligible`, or `error`. Under `--batch`, `delivered` if every issue taken was delivered and Step 8 passed. |
| `reason` | **Required when `status` is not `delivered`.** The stderr message, which eligibility rule emptied the queue, or the specific pair Step 8 caught. |
| `batch` | Present only under `--batch`. `ceiling` is what sizing allowed, which may be below the requested `n`; `remaining` is the ready count left afterwards. `delivered` short of `ceiling` with `remaining: 0` is a drained queue, not a truncated run. |
| `integration` | Present only when two or more issues were delivered. `result` is `passed`, `failed`, or `conflict` — `conflict` meaning the branches would not even merge. The branch is local and unpushed; it never becomes a PR. |
| `issues` | Empty array when nothing was worked. |
| `outcome` | Per issue: `delivered` or `failed`. |
| `pr` | The PR number, or `null` on a `failed` issue — a failed issue has a pushed branch and no PR. Also `null` on a delivered issue when the tracker is local: there `branch` alone is the delivery, committed and not pushed. |
| `done_when` | Checklist items total and verified. `verified < total` on a delivered issue is a contradiction; do not emit it. |
| `tdd` | `applied`, or `not_applicable` with the reason in `gate` (config, docs, migration, wiring). |
| `skipped` | Why candidates were passed over. A run reporting `no_ready_work` with a non-zero `claimed` count is describing a stall, not a drained backlog. |

`status` exists because four different situations produce zero PRs and mean opposite things.
See the table in SKILL.md; the failure mode it guards against is a scheduled run reporting a
finished backlog when the CLI has actually been unauthenticated for a week.

`integration_failed` is the one status that carries open PRs, so a caller must not treat it as a
run to retry. Nothing about re-running changes the result — two issues filed as independent are
coupled, and only a human reading the pair resolves that.

## Quality gates

Step 6 runs every check the project defines — tests, linters, formatters, type checkers, builds —
found in its CI workflows, task runner targets, package scripts, pre-commit config and contributing
docs. This table is the floor: what to run for a language when the project names nothing, and the
minimum even when it does. Detect from the files the branch changed
(`git diff <default-branch>...HEAD --name-only`) and run every row that applies — a change touching
Go and Terraform runs both.

| Changed | Gate |
|---|---|
| `*.go` | `go build ./...` and `go test ./...` |
| `*.py` | `uv run pytest` (scope to the relevant module where possible) |
| `*.ts` `*.tsx` `*.js` `*.jsx` | `npm test` — or `pnpm`/`yarn`, matching the lockfile present |
| `*.rs` | `cargo test` |
| `*.java` `*.kt` | `./mvnw test` or `./gradlew test`, whichever wrapper is present |
| `*.rb` | `bundle exec rspec`, or `bundle exec rake test` with no `spec/` |
| `*.cs` | `dotnet test` |
| `*.swift` | `swift test` |
| `*.php` | `./vendor/bin/phpunit` |
| `*.tf` `*.tfvars` | `terraform validate` in the module directory |
| Shell, config, docs only | Nothing from this table — the `### Done when` items are the verification, plus whatever the project checks such files with (shellcheck, a markdown or link lint, a schema validator) |

When the repo's own command for a row differs from the table, run the repo's: a Makefile target or
a CI workflow naming the real command is better evidence than a language guess. The table never
excuses skipping a check the project defines because no row mentions it.

Step 8 runs the same table against the *union* of the files the batch changed, which can pull in
a gate no single issue triggered — three Go-only fixes plus one touching Terraform means the
integration pass runs both.

## Branch and commit conventions

Branch: `<prefix>/<n>-<slug>`, where the slug is the issue title lowercased and hyphenated,
truncated to something readable. This is the **upstream** name; an existing local branch
dedicated to the issue can keep its name. Attach detached HEAD to a local issue branch before
pushing, preserving any commits from an earlier attempt. Then
`git push -u origin HEAD:<prefix>/<n>-<slug>` establishes tracking for either case. Checkout
and worktree management follow the user's and repository's conventions. On a local tracker
nothing is pushed: the local branch keeps the same `<prefix>/<n>-<slug>` name, and it is the
delivery.

| Issue type | Branch prefix | Commit prefix | `hew pr` title default |
|---|---|---|---|
| `bug` | `fix/` | `fix:` | `fix: <title>` |
| `enhancement` | `feat/` | `feat:` | `feat: <title>` |
| `task` | `chore/` | `chore:` | `chore: <title>` |

The issue number in the branch is load-bearing: `hew pr` uses it to break ties when inferring
which claimed issue the PR is for, and it checks the prefix against the issue type. Both read
the name `hew` resolves from the upstream ref, so a dedicated local branch can have a different
name without losing issue inference or the prefix check.

Commit body explains why the change is what it is, then:

```
Refs #42
```

`Refs`, not `Fixes`. The PR body composed by `hew pr` already carries exactly one `Fixes #42`,
which is what closes the issue on merge. A second closing keyword in a commit message closes it
by a path the reviewer never saw, and does so even if the PR is closed unmerged. A local tracker
has no PR body; keep `Refs` there too, because the issue is closed by `hew close --completed`
after the merge, not by a keyword.

Sign commits however the repository already does — read the recent log rather than assuming a
trailer. Hardcoding a specific model name in a co-author trailer ages out within a release.

## Worked example

Issue #39 in `lumberbarons/hew`, an `enhancement`, is at the top of `hew ready`.

```bash
$ hew ready
#39 P3 enhancement  Write commands guard against closed targets
#40 P3 enhancement  Reopen command symmetric with close
```

Claiming #39, using the command surface from the primer, succeeds (exit 0):

```
started #39: Write commands guard against closed targets
```

Reuse the body if selection or the claim already returned it; otherwise retrieve the missing
body with the read the primer lists. The example doesn't depend on which form the CLI offers.

The body carries the four sections, and `### Done when` has three items:

```
- [ ] set/block/unblock on a closed issue fail with the close state in the message, without mutating
- [ ] An explicit override flag allows the edit
- [ ] close on an already-closed issue reports the existing state instead of silently re-closing
```

Sorted by kind: all three are **behavioural** — each is a claim about what a command does — so
all three become tests. `### Where` names `internal/cli/write.go` and the pre-mutation read the
commands already perform, which bounds the diff.

Red first. Three tests, named for their scenarios, failing because the guard does not exist:

```
--- FAIL: TestSet_RefusesClosedIssue
--- FAIL: TestSet_ClosedOverrideFlagAllowsEdit
--- FAIL: TestClose_ReportsAlreadyClosed
```

They fail on missing behaviour, not on a compile error — that distinction is the whole value of
the red step. Green, then refactor. Verify: three tests pass, `git diff main...HEAD --name-only`
includes `internal/cli/write.go` as `### Where` promised, and `go build ./... && go test ./...`
passes.

```bash
$ git push -u origin HEAD:feat/39-write-commands-guard-closed
$ hew pr --testing '3 new tests in `internal/cli/write_test.go` covering refusal, override, and re-close; `go test ./...` passes'
opened draft PR #63 → Fixes #39
```

The issue stays open. The merge closes it, which is the point — the work went through review
instead of around it.

### The same run, one tick later

```bash
$ hew ready
#40 P3 enhancement  Reopen command symmetric with close
```

Claiming #40 fails with exit 3 because @someone-else already holds it:

```
#40 already claimed (assigned to @someone-else); pick the next ready item or --force
```

Not an error condition — the lock did its job. Move to the next candidate. With nothing else
ready, the run reports `not_eligible` with `skipped.claimed: 1`, rather than `no_ready_work`.
The distinction matters: one says the backlog is finished, the other says something else is
holding it, and only one of them should make anyone comfortable.

### A batch of three

`work-issue --batch` against a queue of ten. Sizing the first three candidates: #42 names
two paths and three Done-when items, #47 one path and two items, #51 two paths and four items.
None is large, so the ceiling stays at 3.

Each is worked exactly as above — branched from `origin/main`, gated alone, pushed, PR opened.
Then Step 8:

```bash
$ git checkout -B integration/batch-42-51 origin/main
$ git merge --no-ff fix/42-write-commands-guard-closed
$ git merge --no-ff fix/47-close-reports-state
$ git merge --no-ff fix/51-start-honours-priority

$ go build ./... && go test ./...
--- FAIL: TestStart_UntriagedRequiresPriority
```

Nothing conflicted — the three fixes touch different files. #51 made `start` read priority before
claiming; #42 made the write path refuse closed issues *before* any read. Each is correct against
its own `### Done when`, and the order they compose in is not something either issue specified.

The three PRs stay open. The coupling is filed fresh:

```bash
$ hew create --type bug --title "start on a closed issue fails before the priority check" \
    --discovered-from 42 --discovered-from 51 --priority P2 ...
```

Status is `integration_failed`, `reason` names the pair and the failing test. Not `failed` — all
three issues were delivered, and re-running the batch would reproduce this exactly.
