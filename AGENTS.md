# AGENTS.md

This file provides guidance to coding agents working with code in this repository.

## What this is

`hew` is an opinionated, agentic-first CLI for tracking work in GitHub Issues — beads-inspired ready-work detection, priority/type/area label conventions, native sub-issues and dependencies, and a `hew prime` command that injects tracker state into an agent session. [DESIGN.md](DESIGN.md) is the authoritative design document: conventions, read-path normalization rules, API strategy, and spike results. Read it before changing behavior.

## Commands

```sh
go build ./...                            # build
go test -race ./...                       # full test suite (what CI runs)
go test ./internal/cli -run TestStart     # single test
go test ./internal/render -update         # rewrite golden files after renderer changes
golangci-lint run                          # lint (CI-blocking)
golangci-lint fmt --diff                   # formatting gate (CI-blocking); drop --diff to fix in place
govulncheck ./...                          # vulnerability scan (CI-blocking)
goreleaser check && goreleaser build --snapshot --clean   # release config dry run (CI-blocking)
actionlint                                 # workflow lint (CI-blocking); also covers examples/auto-triage.yml
cd evals && go test ./...                  # the evals module has its own go.mod
cd evals && golangci-lint run && golangci-lint fmt --diff  # ...and its own lint/format gates
cd consumer && go test -race ./...         # the external-consumer module's contract tests
cd consumer && golangci-lint run && golangci-lint fmt --diff  # ...and its own lint/format gates
cd daemon && go test -race ./...           # the hew-daemon module (REST API + web UI over the public library)
cd daemon && golangci-lint run && golangci-lint fmt --diff  # ...and its own lint/format gates
make web                                   # the daemon's React UI: types, lint, format, unit tests (90% floor), build
cd daemon && go test -run Fixtures -update # regenerate daemon/web/src/fixtures after a daemon response changes
shellcheck install.sh                      # install.sh is linted in CI too
```

Coverage is a blocking CI gate: 90% statement coverage over every package but `cmd/` (excluded as pure wiring — see `.testcoverage.yml`). The profile merges the root module's tests with `consumer/`'s scenarios run under `-coverpkg`, so statements only the consumer exercises count. New logic needs tests or CI fails.

`govulncheck` reports stdlib advisories against the toolchain it runs under, and every CI job resolves its toolchain from the `go` directive in `go.mod`. That directive carries an explicit patch version, so setup-go installs it exactly — which is what makes the scan meaningful, and also means a new stdlib advisory turns the job red until the directive is bumped to the patch release that fixes it. Bumping it is the fix; it also raises the minimum toolchain for building from source. The scan covers the root module only — `evals/` is never shipped, so its dependencies carry no user-facing risk.

Releases are tag-driven (`vX.Y.Z` → goreleaser). Commit messages use `feat:`/`fix:`/`chore:`/`docs:` prefixes — they feed the release changelog.

## Architecture

The layering exists so everything with behavior is testable without hitting GitHub:

| Directory | What | When to read |
|---|---|---|
| `cmd/hew/` | main + urfave/cli v3 flag wiring only — no behavior, excluded from coverage | Adding a command or flag |
| `internal/cli/` | The CLI adapter, written against the `gh.IssueStore` interface, with `gh.PullRequests` for `pr` alone — see [internal/cli/AGENTS.md](internal/cli/AGENTS.md). Flags, input files, the editor, hooks, rendering and exit mapping live here; workflow decisions delegate to public packages per the operation inventory in DESIGN.md ("Tracker library") | Changing command behavior or guarded flows |
| `tracker/` | The public in-process API (`github.com/lumberbarons/hew/tracker`) and the tracker half of the contract in DESIGN.md ("Tracker library"): `Open`/`Detect` pick a backend from an explicit worktree and actor; `Snapshot`/`Get` carry reads, with `Snapshot.List` and `Tracker.Detail`, `Tracker.Search`, `Tracker.Triage` and `Tracker.Primer` carrying the commands' query policy and untriaged boundary; `Start`/`Release`/`Close`/`Edit`/`Reopen`/`Block`/`Unblock` carry writes; `ApplyPlan` carries its `Checkpoint` interface (`MemoryCheckpoint`; `FileCheckpoint` for the CLI's state file) and typed plan model. The CLI delegates over a store it already opened (`Wrap`/`WrapAs`/`WrapWriter`), so tracker must not import `internal/render` or `internal/cli`; typed results and errors, never exit codes; experimental until 1.0, with the exported surface diffed in CI | Changing the library API, the apply/checkpoint engine, or moving an operation out of the CLI |
| `pr/` | The public pull-request workflow (`github.com/lumberbarons/hew/pr`): `Open` resolves the worktree, backend, repository and branch state, then `Create`/`Amend` infer the linked issue(s), compose the body and enforce the exact closing set, and perform the writes through the tracker's transport seam. Warnings and findings ride on typed results; every refusal is a typed error from `pr` or `tracker`. The CLI delegates over `Wrap`, so `pr` must not import `internal/render` or `internal/cli`; experimental until 1.0 | Changing PR inference, composition, guards or amendment |
| `migrate/` | The public migration workflows (`github.com/lumberbarons/hew/migrate`): `Open` resolves the target backend, repository and transport from explicit inputs; `Migrator.Beads` parses a snapshot and maps it onto the conventions; `Migrator.Local` freezes a local tracker, copies it and seals it to GitHub only after every step. Callers that opened a target themselves use `Wrap` with a `Config` naming their checkpoint and repo identity. The engine underneath creates, wires, relinks, comments and closes with provenance verification, checkpointed resume, cycle checks and interruptible throttling. `Result` carries counts, the dry-run plan, the backend and typed warnings; no source parsing, rendering, exit code or expired store type crosses the boundary. The CLI adapts `migrate.Target` over the store it opened and renders what the workflows return | Changing migration resume, provenance checks, wiring, source mapping, the local freeze/seal, or write ordering |
| `internal/gh/` | Thin API layer: the `IssueStore` and `PullRequests` interfaces (`Client` is their union) and the go-gh-backed implementation of both; auth reuses the `gh` CLI's stored credentials (no auth flow of our own), repo detection comes from the git remote | Changing queries, auth, or API calls |
| `internal/model/` | Pure domain logic: readiness, label normalization, cycle detection — no I/O, plain unit tests | Changing readiness or label semantics |
| `internal/render/` | Text and JSON renderers, golden-file tested (`testdata/*.golden`) | Changing output format |
| `internal/conventions/` | The opinions: label set, issue and PR body templates, primer text | Changing labels, a template, or the primer |
| `internal/git/` | Local branch state for `pr` (current branch, the upstream branch name a PR is opened from, has-upstream) and the main checkout's path from any linked worktree (where the local tracker lives); shells out to git behind an injectable runner | Changing what `pr` knows about the checkout |
| `internal/beads/` | Parses a beads (bd) `issues.jsonl` snapshot for `migrate` — pure parsing, no runtime dependency on bd | Changing the beads migration |
| `internal/plan/` | The `apply` plan-file codec: JSONL parsing into tracker's typed plan entries. Local-`id` resolution, dangling-reference and cycle rejection live in `tracker.ValidatePlan`, so all validation still happens before anything is written | Changing the plan file format |
| `internal/local/` | The JSONL-backed `gh.IssueStore` for local-only repos (and the snapshot, freeze and seal `migrate local` uses to move it to GitHub): one `issues.jsonl` in a self-ignoring `.hew/` at the main checkout, every mutation under an exclusive lock and an atomic rename; the default viewer is per worktree and the store's atomic claim backs the guard; `Locate` is the backend switch `buildApp` reads (`--repo` always forces GitHub; see "Local backend" in DESIGN.md); `.hew/` is untracked, so `git clean -fdx` at the main checkout deletes the only copy of the tracker | Changing the local file format, locking, claim identity, backend selection, or local-only behavior |
| `internal/conformance/` | The behavior every `gh.IssueStore` shares, run against `local` and against the `fakeClient` the command tests use — a test helper package, no production code depends on it | Changing what a backend must do, or a fake that drifts from the real client |
| `internal/editor/` | Launches `$EDITOR` seeded with the body template, for `--edit` on `create` and `epic create` | Changing the `--edit` flow |
| `docs/` | Primer mock: `conventions.PrimerStatic` verbatim plus invented state sections — keep it in sync when the primer changes | Revising primer output |
| `site/` | Self-contained landing page for lumberbarons.github.io/hew (no build step, no external assets), published to `gh-pages` by `publish-install` | Changing the landing page |
| `examples/` | Copy-pasteable GitHub Actions recipes (`auto-triage.yml`), documented in README | Changing the auto-triage workflow or its documented guarantees |
| `evals/` | Separate module (keeps the tokenizer out of the CLI's dependency graph): the token-efficiency harness and its committed fixtures — see [evals/README.md](evals/README.md). A renderer change invalidates the fixtures' hew side; re-capture and re-publish the numbers in DESIGN.md | Changing renderer output, or the published token figures |
| `consumer/` | Separate module with a local replace to the checkout: the external-consumer gate that imports only `tracker`, `pr` and `migrate` and runs the contract's workflows against a real local tracker and an isolated GitHub transport. It is why an operation needing an internal type fails CI instead of passing its own tests — see [consumer/README.md](consumer/README.md) | Changing a public API, a workflow, or the consumer scenarios |
| `daemon/` | Separate module with a local replace to the checkout, like `consumer/`: `hew-daemon`, a loopback-only REST API plus an embedded web UI built only on `tracker` and `pr`. It maps typed library errors to HTTP kinds (`errors.go`), re-maps the untagged result types to JSON (`dto.go`), and refuses forgeable requests (`guard` in `server.go`). It has no auth by design, so it must never listen beyond loopback. `daemon/web/` is the UI (Vite, React, TypeScript; npm) built into `daemon/dist/` and embedded; its `web/go.mod` is an empty module that keeps Go tooling out of `node_modules`. `web/src/fixtures/` are the daemon's real responses, generated by the Go tests and consumed by the UI's tests, so a response change fails one side until both agree. Outside the root coverage gate and goreleaser, but held to 90% by `daemon/.testcoverage.yml` (Go, `cmd/` excluded) and Vitest thresholds (UI), in CI's `daemon` and `daemon-ui` jobs. Its `go` directive must match the root's. See [daemon/README.md](daemon/README.md) | Changing the daemon's API or UI, or when a library change breaks it |

The public packages are one API in three parts: `tracker`, `pr` and `migrate`,
including the beads and local-to-GitHub workflows. DESIGN.md's
"Tracker library" is the operation inventory and the boundary: no public package
imports `internal/cli` or `internal/render`, every public signature names public
types, and an operation moves out of the adapter only with its tests and its
contract row. The exported surface of every public package is diffed against the
base commit in CI.

| File | What | When to read |
|---|---|---|
| `DESIGN.md` | The authoritative design document: conventions, normalization rules, API strategy, spike results | Before changing any behavior |
| `README.md` | User-facing overview and install instructions | Changing the CLI surface or install flow |
| `install.sh` | Curl-able installer, shellcheck-linted in CI | Changing the install flow |
| `go.mod` | Module definition and dependencies | Adding or upgrading a dependency |
| `LICENSE` | MIT license | Never |

API strategy: one GraphQL query per command where possible. `ready`/`prime` fetch all open issues (labels, assignees, parent, sub-issues, blockers) in a single paginated query and filter client-side. Nested connections are capped, not paginated — warn when `totalCount` exceeds the cap rather than truncating silently.

## Invariants to preserve

- **Write path enforces, read path normalizes.** Anything the tool writes conforms to the conventions (exactly one priority label, one type label, template body). Issues created outside the tool are normal, not defects: never auto-"repaired", never hidden. Normalization is deterministic (highest priority wins, missing priority renders `P?` and sorts last) and lives pure in `internal/model`.
- **Cycle checks are client-side and mandatory.** GitHub's API rejects self-blocks and 2-cycles but silently accepts longer dependency cycles (verified by spike), so `block`/`create --blocked-by` must run a transitive check before mutating, and the read path must detect cycles too — a cycle silently removes all its members from `ready`.
- **Exit codes are contract.** Agent loops branch on them: `3` = claimed by someone else (pick next ready item), `5` = claimed by you (resume that work), `6` = open blockers (pick the next ready item), `4` = auth failure (`gh auth login`), `2` = usage. Defined in `internal/cli/app.go`.
- **Output is agent-facing.** One compact line per issue, no URLs, no ANSI when not a TTY, stable sort. `--json` is a stable flat schema (deps as number arrays, GraphQL shapes hidden); list-shaped output is NDJSON so truncation degrades gracefully. The `prime` output has a token budget (~1200 tokens) — don't add prose to it casually.
- **Epic-ness = has sub-issues, or carries the `epic` label the tool puts on every epic it creates.** The label is what keeps a childless epic triaged and listed; the `Epic: ` title prefix is cosmetic. `ready` excludes every epic.
