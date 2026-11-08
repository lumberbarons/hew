Rate-limiting proxy sitting in front of the public API.

- Run tests: `cargo test` (integration tests need Redis on :6379)
- Run locally: `cargo run -- --config dev.toml`
- Never call `Instant::now()` in limiter code — pass a `Clock` so tests can advance time.
- `bucket.rs` is the token-bucket limiter; `window.rs` is the legacy sliding-window one, kept only for the `/v1` routes. New work goes in `bucket.rs`.
