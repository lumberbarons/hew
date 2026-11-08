---
id: ADR-0001
title: Use Redis for the job queue
status: accepted
date: 2026-02-03
deciders: [dana]
supersedes: []
superseded-by: []
related: []
tags: [queue, redis]
---

# ADR-0001: Use Redis for the job queue

## Context and Problem Statement

Invoice jobs need a queue. The team already runs Redis for sessions and has no broker experience.

## Decision Drivers

- No new infrastructure to operate
- Invoice jobs must not be lost

## Considered Options

1. **Redis lists** — reuse the existing instance
2. **RabbitMQ** — a dedicated broker

## Decision Outcome

Chosen option: **Redis lists**, because it adds no new infrastructure.

## Consequences

### Positive

- One less system to run

### Negative

- Delivery guarantees are ours to build

### Risks

- Job loss if the queue semantics are misunderstood

## Pros and Cons of the Options

### Redis lists

- Good: already operated
- Bad: no built-in acknowledgement

### RabbitMQ

- Good: acknowledgements built in
- Bad: new system to run

## Implementation Notes

Queue code lives in `jobqueue/`.

## References

- None
