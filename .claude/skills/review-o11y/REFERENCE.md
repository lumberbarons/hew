# Example Output

This reference shows the expected format and level of detail for an observability review report.

---

## Example Report

```
---
Observability Review for internal/payments

11 files reviewed, 5 issues found (1 P1, 3 P2, 1 P3).

**Detected conventions**
- Logger library: `slog` (stdlib) — used in 9/11 files
- Log call shape: structured key-value (majority)
- Field-name casing: snake_case (`user_id`, `order_id`, `request_id`)
- Error-message format: leading lowercase, no trailing punctuation, `"failed to X: <cause>"` verb form
- Error construction: `fmt.Errorf("...: %w", err)` with sentinel errors for domain cases

### 1. [P1] Authorization header logged with every inbound request
**Location:** internal/payments/middleware/logging.go:42 (and all 8 inbound handlers by extension)
**Axis:** prescriptive

`logHTTPRequest` dumps the full `r.Header` map into the request log line. Every request includes the raw `Authorization: Bearer <token>` header in structured field `headers`. These tokens end up in the log aggregator, in the SIEM, and anywhere the logs are shipped — this is a credential leak waiting to be discovered in an audit. Replace with an allowlist of non-sensitive headers (`User-Agent`, `X-Request-Id`, `Content-Type`), or scrub `Authorization`, `Cookie`, and `X-Api-Key` before logging.

### 2. [P2] ERROR used for expected user-input failures
**Location:** internal/payments/handler/checkout.go:78, internal/payments/handler/refund.go:54, internal/payments/handler/subscribe.go:91
**Axis:** prescriptive

Each handler logs at `slog.Error` when a user submits a malformed request (invalid card number, missing address, duplicate subscription). These are expected 4xx responses, not operational failures. They currently page oncall via the ERROR-rate alert. Drop to `slog.Info` (or omit entirely — the access log already records the 4xx) so the ERROR channel only fires on real incidents.

### 3. [P2] Retries on the Stripe client succeed silently
**Location:** internal/payments/stripe/client.go:134
**Axis:** prescriptive

`withRetry` wraps every outbound Stripe call with exponential backoff, but logs nothing on retry — only on final failure. When Stripe is flaking we have no signal until the full retry budget is exhausted and a user sees an error. Emit a WARN on each retry attempt with the attempt number, elapsed time, and the underlying error; keep ERROR for the final give-up.

### 4. [P2] Inbound handlers do not attach request_id to the logger
**Location:** internal/payments/handler/checkout.go:23, internal/payments/handler/refund.go:18, internal/payments/handler/subscribe.go:31, internal/payments/handler/webhook.go:27
**Axis:** prescriptive

All four handlers call `slog.Default().Info(...)` directly instead of pulling a request-scoped logger with `request_id` already bound. A single request that touches checkout → payment → webhook cannot be stitched together in the log aggregator. Add a middleware that puts `slog.With("request_id", reqID)` into the request context and require handlers to obtain the logger via `slog.FromContext`.

### 5. [P3] Error messages split between "failed to" and "could not"
**Location:** internal/payments/repo/orders.go:54, internal/payments/repo/refunds.go:33
**Axis:** descriptive

The dominant verb form in this codebase is `"failed to X: <cause>"` (detected in 27 of 34 sampled error sites). These two files use `"could not X: <cause>"`. An operator alerting on `"failed to charge"` in the log aggregator misses refund and order creation failures entirely. Rewrite these two files to use the dominant form.
---
```

---

## Format Rules

### Start with a detected-conventions block

After the one-line "N files reviewed, M issues found" summary, include a `**Detected conventions**` bullet list showing what Step 2 anchored against. This lets the reader spot-check the baseline before reading descriptive findings — if the baseline is wrong, the descriptive findings are wrong.

### Findings use H3 headers with priority tag, Location, Axis, and explanation

```
### 1. [P2] ERROR used for expected user-input failures
**Location:** internal/payments/handler/checkout.go:78
**Axis:** prescriptive

Explanation of the observability problem and the operational consequence of leaving it.
```

### Location can list multiple sites for collapsed findings

When pattern collapsing merges N per-file findings into one, the Location line lists all affected files (or a representative subset with "and N more" if the list is long).

### Only show issues found — no passing rows

Do not include items that passed review. Start with "N files reviewed, M issues found (severity breakdown)."

The one exception is a short report on a well-instrumented codebase: there, close with a sentence or two naming what was considered and deliberately not raised, so the reader can tell restraint from a shallow pass. This is a closing note, not a section of passing rows.

### No tables in the findings section

The detected-conventions block uses bullets, not tables. Findings are prose, not rows.

---

## Pattern slugs

The `pattern` field of a findings file (see [FINDINGS.md](FINDINGS.md)) names a finding's
root cause. Consumers use it as part of a finding's identity across runs, so it has to be stable
between them — a slug that drifts from one phrasing to another reads as a new finding and gets
filed twice. Pick from this list; use `other-<slug>` when nothing fits, and treat a recurring
`other-` slug as a sign this list needs extending.

```
credential-in-log
double-reported-failure
entry-exit-noise
error-on-user-input
error-wrap-drops-cause
failure-below-alert-level
field-name-drift
message-format-drift
missing-correlation-id
missing-log-io-boundary
mixed-logger-libraries
pii-in-log
silent-fallback
silent-retry
startup-config-unlogged
tautological-message
unstructured-logging
```
