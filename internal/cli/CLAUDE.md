# internal/cli

The commands themselves, written against the `gh.Client` interface so `cmd/hew` stays pure wiring and every behavior is testable without hitting GitHub.

| File | What | When to read |
|---|---|---|
| `app.go` | The `App` struct every command hangs off: client, repo, output writers, and the `emit*` helpers that pick text vs `--json` | Adding a command, or changing how output is emitted |
| `read.go` | The read commands: `ready`, `list`, `show`, `search`, `triage`, `prime`, `epic status` — one query, filtered client-side | Changing what a read command returns or how it filters |
| `write.go` | The mutating commands: `create`, `start`, `set`, `close`, `reopen`, `block`, `unblock`, `epic create`, `init`; label swaps never stack, and `start` re-reads after claiming | Changing a mutation or the claim guard |
| `pr.go` | `pr`: infers the claimed issue, composes the body from it, enforces exactly one `Fixes #n`, derives the title from the issue type | Changing PR composition or issue inference |
| `apply.go` | `apply`: walks a parsed `internal/plan` plan, creating issues then wiring dependency edges | Changing `apply` execution (schema and validation live in `internal/plan`) |
| `migrate.go` | `migrate beads`: maps a parsed beads snapshot onto the conventions — priorities, types, deps, epics, in-progress state | Changing the beads migration |
| `batch.go` | Shared machinery for `apply` and `migrate`: the checkpoint state file and its repo/digest binding, the pre-flight verification that every mapped issue carries the provenance marker `internal/conventions` defines, throttled writes, label bootstrapping | Changing resume behavior, checkpoint trust, or write throttling |
| `hooks.go` | `hooks install\|remove`: edits the project's `.claude/settings.json` in place, preserving unknown fields | Changing the SessionStart hook |
| `exit.go` | Maps errors to the exit codes agent loops branch on (`2` usage, `3` claimed by someone else, `4` auth, `5` claimed by you), plus the `--repo` and issue-number argument parsers | Changing an exit code — it is contract |

Tests run against `fakeClient`, never the network. `batch.go` has no test file of its own — it is covered through `apply` and `migrate`; the provenance marker it verifies is unit-tested in `internal/conventions`.

Checkpoint state is trusted local scratch, never a portable input. A state file is bound to the repository and to a digest of its source plan or snapshot, and every issue it maps must carry the provenance marker the batch writer stamped into the body it created — verified in one pass before any write, `--dry-run` included. Tests asserting "no mutation happened" must assert that *only* reads were made (`assertOnlyReads`), not that a list of specific mutating calls was absent: label bootstrapping writes too. See "Checkpoint binding" in DESIGN.md.

| File | What | When to read |
|---|---|---|
| `fake_test.go` | `fakeClient`, a stateful in-memory `gh.Client` where mutations really mutate, so guarded flows (claim, re-read after claim) behave like the real API | Writing a test that needs a new client behavior |
| `read_test.go` | Covers `read.go` except search: filtering, sorting, empty states, and the cycle and truncated-connection warnings | Changing a read command |
| `search_test.go` | Covers `search` in `read.go`: title and body matching, closed issues, best-match order | Changing search behavior |
| `write_test.go` | Covers `write.go`: label swaps, the claim guard, cycle rejection, body composition | Changing a mutation |
| `pr_test.go` | Covers `pr.go`: body composition, `Fixes #n` handling, branch and claim inference | Changing the `pr` command |
| `apply_test.go` | Covers `apply.go` and its half of `batch.go`: plan execution, resume, dependency wiring | Changing `apply` |
| `migrate_test.go` | Covers `migrate.go` and its half of `batch.go`: beads mapping, resume | Changing the beads migration |
| `hooks_test.go` | Covers `hooks.go`: settings merge, idempotent install, removal | Changing the SessionStart hook |
| `exit_test.go` | Covers `exit.go`: the error-to-exit-code mapping and the argument parsers | Changing an exit code |
