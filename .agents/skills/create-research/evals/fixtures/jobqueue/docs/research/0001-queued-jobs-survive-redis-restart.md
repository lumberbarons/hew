---
id: RES-0001
title: "Do queued jobs survive a Redis restart?"
status: complete
date: 2026-10-20
authors: [dana]
supersedes: []
superseded-by: []
related: []
tags: [redis, persistence]
---

# RES-0001: Do queued jobs survive a Redis restart?

## Research Question

Under the persistence configuration the deployment uses today, are jobs waiting in the
queue still there after Redis restarts?

## Context

Billing runs invoice jobs through the Redis-backed queue, and Redis is restarted for
patching each month. Nobody had confirmed that waiting jobs are kept across a restart.

## Scope

### In scope

- The persistence settings in `redis.conf` and what they imply for the pending list

### Out of scope

- Jobs that a worker has already taken off the queue
- Redis crashes that corrupt the append-only file

## Method

Read `redis.conf` and the Redis persistence documentation. No restart was performed.

## Findings

### F1 — Pending jobs are written to an append-only file and replayed on restart (confidence: High)

`appendonly yes` with `appendfsync everysec` is set, and RDB snapshots are disabled with
`save ""` [S1]. The documentation states that the append-only file is replayed on startup
and that `everysec` can lose at most about one second of writes [S2].

## Open Questions

- Whether the one-second window has ever lost a job in practice.

## Conclusions

A clean restart keeps pending jobs. A hard crash can lose up to a second of enqueues.
This says nothing about jobs a worker has already taken off the queue.

## Sources

- S1 — [redis.conf](../../redis.conf) — codebase — accessed 2026-10-19
- S2 — [Redis persistence](https://redis.io/docs/latest/operate/oss_and_stack/management/persistence/) — primary — accessed 2026-10-19

## Revisit When

- Any persistence setting in `redis.conf` changes.
