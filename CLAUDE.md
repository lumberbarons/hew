# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

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
shellcheck install.sh                      # install.sh is linted in CI too
```

Coverage is a blocking CI gate: 90% statement coverage over the `internal/` packages (`cmd/` is excluded as pure wiring — see `.testcoverage.yml`). New logic needs tests or CI fails.

`govulncheck` reports stdlib advisories against the toolchain it runs under, and every CI job resolves its toolchain from the `go` directive in `go.mod`. That directive carries an explicit patch version, so setup-go installs it exactly — which is what makes the scan meaningful, and also means a new stdlib advisory turns the job red until the directive is bumped to the patch release that fixes it. Bumping it is the fix; it also raises the minimum toolchain for building from source. The scan covers the root module only — `evals/` is never shipped, so its dependencies carry no user-facing risk.

Releases are tag-driven (`vX.Y.Z` → goreleaser). Commit messages use `feat:`/`fix:`/`chore:`/`docs:` prefixes — they feed the release changelog.

## Architecture

The layering exists so everything with behavior is testable without hitting GitHub:

| Directory | What | When to read |
|---|---|---|
| `cmd/hew/` | main + urfave/cli v3 flag wiring only — no behavior, excluded from coverage | Adding a command or flag |
| `internal/cli/` | The commands, written against the `gh.Client` interface — see [internal/cli/CLAUDE.md](internal/cli/CLAUDE.md) | Changing command behavior or guarded flows |
| `internal/gh/` | Thin API layer: the `Client` interface and its go-gh-backed implementation; auth reuses the `gh` CLI's stored credentials (no auth flow of our own), repo detection comes from the git remote | Changing queries, auth, or API calls |
| `internal/model/` | Pure domain logic: readiness, label normalization, cycle detection — no I/O, plain unit tests | Changing readiness or label semantics |
| `internal/render/` | Text and JSON renderers, golden-file tested (`testdata/*.golden`) | Changing output format |
| `internal/conventions/` | The opinions: label set, issue and PR body templates, primer text | Changing labels, a template, or the primer |
| `internal/git/` | Local branch state for `pr` (current branch, the upstream branch name a PR is opened from, has-upstream); shells out to git behind an injectable runner | Changing what `pr` knows about the checkout |
| `internal/beads/` | Parses a beads (bd) `issues.jsonl` snapshot for `migrate` — pure parsing, no runtime dependency on bd | Changing the beads migration |
| `internal/plan/` | The `apply` plan-file schema: JSONL parsing, local-`id` resolution, dangling-reference and cycle rejection — all validation happens before anything is written | Changing the plan schema or `apply` validation |
| `internal/editor/` | Launches `$EDITOR` seeded with the body template, for `--edit` on `create` and `epic create` | Changing the `--edit` flow |
| `docs/` | Primer mock: `conventions.PrimerStatic` verbatim plus invented state sections — keep it in sync when the primer changes | Revising primer output |
| `site/` | Self-contained landing page for lumberbarons.github.io/hew (no build step, no external assets), published to `gh-pages` by `publish-install` | Changing the landing page |
| `examples/` | Copy-pasteable GitHub Actions recipes (`auto-triage.yml`), documented in README | Changing the auto-triage workflow or its documented guarantees |
| `evals/` | Separate module (keeps the tokenizer out of the CLI's dependency graph): the token-efficiency harness and its committed fixtures — see [evals/README.md](evals/README.md). A renderer change invalidates the fixtures' hew side; re-capture and re-publish the numbers in DESIGN.md | Changing renderer output, or the published token figures |

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
- **Exit codes are contract.** Agent loops branch on them: `3` = claimed by someone else (pick next ready item), `5` = claimed by you (resume that work), `4` = auth failure (`gh auth login`), `2` = usage. Defined in `internal/cli/app.go`.
- **Output is agent-facing.** One compact line per issue, no URLs, no ANSI when not a TTY, stable sort. `--json` is a stable flat schema (deps as number arrays, GraphQL shapes hidden); list-shaped output is NDJSON so truncation degrades gracefully. The `prime` output has a token budget (~1200 tokens) — don't add prose to it casually.
- **Epic-ness = has sub-issues.** The `Epic: ` title prefix is cosmetic; `ready` excludes any issue with sub-issues.
