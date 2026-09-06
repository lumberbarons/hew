# evals

Measurements that keep `hew`'s claims honest. Its own module, so the tokenizer's
embedded vocabularies never reach the CLI's dependency graph — `go install
.../cmd/hew@latest` still pulls go-gh and urfave/cli and nothing else.

## Token efficiency

`cmd/tokens` compares what a read command costs an agent's context against the
raw `gh` and GraphQL output an agent would otherwise have to ingest to answer the
same question. Two steps, because only the first needs GitHub:

```sh
go build -o /tmp/hew ../cmd/hew

# capture: reads a live repo, records both sides' raw output into a fixture
go run ./cmd/tokens capture \
  --repo lumberbarons/solar-controller --show 123 --epic 137 \
  --hew /tmp/hew --out fixtures/solar-controller

# report: tokenizes the committed fixture — offline, deterministic
go run ./cmd/tokens report fixtures/solar-controller fixtures/hew
go run ./cmd/tokens report --format markdown fixtures/solar-controller  # for DESIGN.md
go run ./cmd/tokens report --format json fixtures/solar-controller
```

`--epic` is optional: omit it for a repo with no open epic (the `hew` fixture has
none). Published figures live in [DESIGN.md](../DESIGN.md#token-efficiency-measured-2026-09-06).

### What a fixture is

A directory of raw command output plus `capture.json`, which records the repo,
the capture date, the measured binary's version, the open-issue count, and the
exact argv behind every file. That makes the numbers reproducible without
network access and disputable without trusting the harness: re-run any recorded
command and compare.

Fixtures are snapshots. **A renderer change invalidates the hew side of every
fixture** — re-capture and re-publish rather than assuming the ratios held.

Capture from a quiet repo. The commands run seconds apart against live state, so
issues filed mid-capture land on one side of the comparison and not the other.
The open-issue count in `capture.json` is the check: if it doesn't match the repo
you meant to measure, re-capture.

### Method

- **Tokenizer**: tiktoken `o200k_base`, compiled into the binary, so counting
  needs no network and cannot drift between runs. Claude's tokenizer is not
  published; `o200k_base` is the documented stand-in and reads slightly low, so
  the absolute figures are conservative and the ratios are the durable part.
  `count_test.go` pins the encoding with counts that `cl100k_base` gets wrong.
- **Charitable baselines**: the GraphQL queries in `cmd/tokens/queries/` request
  the least an agent could ask for and still answer the question — no bodies, no
  timestamps, no URLs. Inflating the baseline would be the easy way to win.
- **Partial baselines are priced, not hidden**: `gh issue list --json` cannot
  return `blockedBy`, `parent`, or `subIssues`, so it cannot answer readiness at
  all. It is reported with a † because it is what an agent reaches for first,
  and its cost is worth knowing — but it is excluded from the headline ratio
  range, which only compares against answers that are answers.
- **No totals across commands**: `ready`, `list`, and `prime` all read the same
  tracker state, so summing their costs would count that state three times. The
  report gives the range and median instead.
- **Zero model calls**, by design — this harness answers the token question. The
  accuracy question (does an agent get the right answer more often?) is #35.

## Layout

| Path | What |
|---|---|
| `cmd/tokens/main.go` | The `capture` and `report` subcommands |
| `cmd/tokens/spec.go` | The comparison itself: which commands, which baselines, and the manifest schema |
| `cmd/tokens/capture.go` | Runs the commands and writes the fixture; shared baselines run once |
| `cmd/tokens/report.go` | Tokenizes a fixture and renders text, markdown, or JSON |
| `cmd/tokens/count.go` | The tokenizer wrapper and the encoding the project measures with |
| `cmd/tokens/queries/` | The hand-rolled GraphQL baselines, one file per question |
| `fixtures/` | Committed captures — the published numbers |
