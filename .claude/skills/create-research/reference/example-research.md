---
id: RES-0004
title: "Does the nightly export lose rows when the database fails over?"
status: complete
date: 2026-03-12
authors: [dana]
supersedes: []
superseded-by: []
related: [RES-0002, ADR-0003]
tags: [export, postgres, reliability]
---

# RES-0004: Does the nightly export lose rows when the database fails over?

## Research Question

When the primary Postgres instance fails over while the nightly export is running,
does the export finish with every row, finish with missing rows, or fail visibly?

## Context

The export in `services/export/` streams the `orders` table to object storage between
01:00 and 01:40 UTC, and finance reconciles against it each morning. The managed
Postgres plan failed over twice last quarter, once during the export window. Nobody
could say whether that night's file was complete, which is what prompted the question.

## Scope

### In scope

- How the export reads rows: its cursor, transaction and retry behaviour
- What the managed Postgres failover does to an open connection and an open cursor

### Out of scope

- Failover during the upload step; the upload is a separate, idempotent PUT
- Other consumers of the `orders` table
- Tuning failover time

## Method

Read `services/export/reader.go` and its tests end to end, then read the provider's
failover documentation for connection behaviour. Reproduced the failure on a staging
instance by forcing a failover (`pg_ctl promote` on the replica) while the export ran
against 2M seeded rows, three runs. Did not test against production data volumes.

## Findings

### F1 — The export reads through one server-side cursor in a single transaction (confidence: High)

`reader.go` opens one `REPEATABLE READ` transaction and declares one cursor over
`SELECT ... FROM orders ORDER BY id`, then fetches in batches of 5,000 [S1]. There is
no resume point: batches are not keyed by `id`, so a restart begins from the first row.

### F2 — A failover kills the cursor, and the export fails rather than continuing (confidence: High)

In all three staging runs the connection dropped with `connection reset by peer`, the
next `FETCH` returned an error, and the export exited non-zero without uploading [S2].
The provider documents that open connections are terminated on promotion and are not
carried to the new primary [S3].

### F3 — Truncated files are possible only through the retry wrapper (confidence: Medium)

The wrapper retries a failed batch up to three times on a new connection [S1]. A new
connection has no cursor, so the retry runs `FETCH` against nothing and returns zero
rows, which the loop reads as end of table. We reproduced this once in three runs, when
the failover finished inside the retry window [S2]. The other two runs exhausted the
retries and failed visibly, so the rate under real failover timing is not established.

## Open Questions

- How often a real failover completes inside the retry window; staging timing was
  chosen by hand.
- Whether the file written on the night of the incident was truncated; the logs from
  that night have expired.

## Conclusions

The export does not silently drop rows in the common case; it fails and uploads
nothing. It can produce a truncated file that looks successful when a failover finishes
inside the retry window (F3). The evidence supports fixing the retry wrapper so that an
empty `FETCH` on a fresh connection is an error, not end-of-table. Whether to also make
the export resumable is a design choice with cost, and belongs in an ADR.

## Sources

- S1 — [services/export/reader.go](services/export/reader.go) — codebase — accessed 2026-03-10
- S2 — [Staging failover runs, three attempts](https://wiki.example.com/export/failover-runs) — experiment — accessed 2026-03-11
- S3 — [Managed Postgres: failover behaviour](https://docs.example.com/postgres/failover) — primary — accessed 2026-03-10

## Revisit When

- The export moves off a single cursor, or the provider changes failover to preserve
  open connections.
- The `orders` table grows enough that the export exceeds its 40 minute window.
