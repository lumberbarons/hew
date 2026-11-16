# hew-daemon

A local server for the everyday hew workflow: a REST API and a small browser UI over
a tracker, built only on the public `tracker` and `pr` packages. It is the long-running
counterpart to `consumer/`, and what it needed from the library is recorded below.

It is a separate module (`replace github.com/lumberbarons/hew => ../`, like
`consumer/`), so it can name only the public packages. CI runs it as its own `daemon`
job (`make daemon` locally); it is outside the root module's coverage gate and
goreleaser, but has its own 90% statement floor (`cmd/` excluded, as at the root). Its `go` directive must match the root's.

```sh
cd daemon && go run ./cmd/hew-daemon --dir /path/to/a/hew/checkout
# http://127.0.0.1:7777
```

## Security model

There is no authentication. The daemon is for one person on one machine:

- It refuses any listen address that is not loopback.
- Any web page can make a browser send requests to `127.0.0.1`, so the handler also
  rejects a non-loopback `Host` (DNS rebinding), a foreign or `null` `Origin`
  (cross-site writes), and bodies that are not `application/json` (form-style CSRF).
- The frontend renders tracker text with `textContent` only.

## What it covers

Reads: board, ready, list, detail, epics, search, triage, primer, config.
Writes: create, create epic, edit, start, release, close, reopen, block, unblock,
body compose and check, apply (JSON plan), and PR create and amend (GitHub backend).
Library errors map to HTTP statuses with a stable `error` kind: the CLI's exit codes
become `claimed_by_other`, `claimed_by_you` and `blocked` (all 409).

On a local tracker the `X-Hew-Actor` header (the UI's "acting as" box) picks the
claimant per request through `Tracker.As`. On GitHub every request acts as the
authenticated login.

Not covered: `migrate` workflows (long-running, would need a job model), hooks.

## Library gaps this surfaced

Things a consumer had to work around. None blocked a route.

- **Result types are not JSON-tagged.** Only `tracker.Issue` has the `--json` schema.
  Every other result (`CreateResult`, `StartResult`, `IssueDetail`, `PrimerResult`,
  `pr.CreateResult`, warnings, findings) marshals with Go field names and `null` for
  nil slices, so `dto.go` re-maps them.
- **Snapshot reads always carry issue bodies.** `hew list --json` omits them unless
  asked; `Snapshot.Ready`, `List` and the rest include them, so a consumer building a
  list view has to strip them (`lean` in `dto.go`).
- **No untriaged count on a `Snapshot`.** `Primer` reports one, but a board built from
  a snapshot has to count over `Snapshot.Issues()`, the raw read that includes
  untriaged text.
- **No error classification.** The typed errors are enough, but the mapping from them
  to "claimed by you / by someone else / blocked" lives in `internal/cli`, so every
  consumer re-derives it (`errors.go`).
- **`pr.Client` is bound to one checkout's current branch.** It suits a per-worktree
  agent, not a server fronting many branches.
- **Per-request identity is local-only.** `As` is a documented no-op on GitHub.
- **Plan JSONL codec is internal.** `ApplyPlan` takes typed entries, so the daemon
  defines its own JSON plan shape.
