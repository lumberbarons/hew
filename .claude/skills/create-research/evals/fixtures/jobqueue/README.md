# jobqueue

A small Redis-backed job queue used by the billing service to run invoice jobs.

## Guarantees

- **At-least-once delivery.** A job that is accepted is run at least once, even if a worker
  dies while running it.
- Jobs that raise are retried up to `MAX_RETRIES` times, then dropped to the dead-letter list.
- Queued jobs survive a Redis restart (see `redis.conf`).

## Layout

- `jobqueue/queue.py` — enqueue and dequeue against Redis
- `jobqueue/worker.py` — the worker loop and retry handling
- `redis.conf` — the Redis configuration the deployment uses
- `docs/adr/` — architecture decisions
- `docs/research/` — research records
