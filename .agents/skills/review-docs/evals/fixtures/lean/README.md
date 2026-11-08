# gatekeep

A rate-limiting reverse proxy for the public API.

## Requirements

- Rust 1.75+
- Redis 7+ (`brew install redis` on macOS, `apt install redis-server` on Debian)

## Quick start

```bash
redis-server --daemonize yes
cargo run -- --config dev.toml
```

You should see:

```
gatekeep 0.4.1 listening on 127.0.0.1:9000 (backend: redis://localhost:6379)
```

Verify it is limiting:

```bash
for i in $(seq 1 12); do curl -s -o /dev/null -w "%{http_code} " localhost:9000/ping; done
```

Expect nine `200`s then `429`s — the dev config allows 9 requests per 10s window.

## Troubleshooting

- `Connection refused (os error 61)` — Redis isn't running. Start it with `redis-server`.
- All requests return `429` immediately — a previous run left counters in Redis. Clear with `redis-cli FLUSHDB`.

## Configuration

See `dev.toml`. Defaults: 9 requests per 10s, burst 3, 30s ban after repeated violations.
