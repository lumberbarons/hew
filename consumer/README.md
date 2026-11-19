# consumer

The external-consumer gate. This is a Go module **outside**
`github.com/lumberbarons/hew`: it replaces the library with the checkout beside
it and imports only its public packages — `tracker`, `pr` and `migrate`. A
workflow that needs an internal type, or an operation that exists only in
`internal/cli`, fails to compile here instead of passing the library's own unit
tests.

```sh
cd consumer
go test -race ./...
```

Nothing here builds or runs the `hew` binary, reads a `gh` credential, or
reaches the network. The GitHub-backed scenarios serve an `httptest` transport
through `Config.Transport`, which is the public test seam; the local scenarios
run against a real `.hew` tracker created with `tracker.Init`.

## What it covers

- **Initialization and metadata** — `tracker.Init` on both backends,
  `tracker.Detect`, `Open`'s backend and identity.
- **Reads** — `Snapshot` (`Ready`, `List`, `Epics`), `Detail`/`Get`, `Search`,
  `Triage`, `Primer`, the untriaged boundary, text findings and warnings.
- **Writes** — creation and epics, editing and reopening, claims and releases,
  closure, `Block`/`Unblock`, `ApplyPlan` with in-memory and file checkpoints.
- **Pull requests** — `pr.Open`, `Create`, `Amend`, branch inference, body
  composition and the typed refusals.
- **Migrations** — the source-neutral engine, `Beads`, and `Local` with its
  freeze, resume and seal.
- **Errors** — every refusal category matches a public type: not-found, auth,
  invalid input, claims and ownership, blockers and cycles, closed targets,
  partial writes, migration and lock timeout. Cancellation stays matchable with
  `errors.Is`.
- **Concurrency** — concurrent local actors and concurrent operations on one
  GitHub client, under the race detector.
- **Boundaries** — the public production dependency graph excludes
  `internal/cli`, `internal/render` and `cmd/hew`; this module imports nothing
  internal; the names DESIGN.md promises are exported; and the
  module declares the same `go` directive as the root.

## Adding a scenario

Write it against the public API, the way a program outside this repository
would. If it cannot be written without an internal import, that is the finding
— the operation is not part of the contract yet.
