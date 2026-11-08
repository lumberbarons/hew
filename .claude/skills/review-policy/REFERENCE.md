# Example Output

This reference shows the expected format and level of detail for a policy review report.

---

## Example Report

```
---
Policy Review for feature/stripe-refunds (12 files, +387 / -54 lines)

**Change scope**
- Diff: `main...HEAD`
- Files: 12 changed (10 in `services/payments/`, 2 in `services/ledger/`)

**ADRs considered**
- Applicable (3): ADR-0004 (PII encryption), ADR-0007 (Postgres event store), ADR-0012 (retry policy)
- Possibly applicable (1): ADR-0011 (shared logging middleware) — weak signal, change touches one handler
- Out of scope (9): not engaged by the change

**Summary:** 4 findings (1 P1, 2 P2, 1 P3); 4 ADRs reviewed; 0 ADRs on-policy; 1 driver-shift candidate flagged.

### 1. [P1] [violation] Refund payloads written to audit log without PII redaction
**ADR:** ADR-0004 (PII encryption)
**ADR section:** Decision Outcome — "all PII fields must be encrypted at rest or scrubbed before persistence in logs, audit tables, or error reporters."
**Location:** services/payments/refund/audit.go:87–95

The new `logRefundOutcome` function serializes the full `RefundRequest` struct — including `customer_email` and `billing_address` — into the `refund_audit` table. ADR-0004 explicitly required PII fields to be encrypted or scrubbed before reaching audit storage. The audit table is the exact persistence sink ADR-0004 called out. This is a textual violation, not a judgement call.

**Fix:** run the payload through the existing `pii.Scrub` helper before insert, or encrypt the PII fields with the per-tenant KMS key used elsewhere in this service. Do not ship without one of the two.
**Done when:** `refund_audit` inserts do not contain plaintext values for any field listed in `internal/pii/fields.go`.

### 2. [P2] [erosion] New handler bypasses shared retry wrapper
**ADR:** ADR-0012 (retry policy)
**ADR section:** Consequences > Negative — "any outbound call that does not use `retry.Wrap` will silently skip the exponential backoff and circuit-breaker logic."
**Location:** services/payments/stripe/refund_client.go:42

ADR-0012 accepted a shared `retry.Wrap` helper as the single retry path for outbound calls; the invariant is that every outbound call goes through it. This change calls `http.Do` directly on the Stripe refund endpoint. The ADR is not textually violated (there's no rule saying "never call http.Do"), but this is the first new external-call site in six months to skip the wrapper, and the invariant erodes every time it's skipped.

**Fix:** route through `retry.Wrap` as every other Stripe call in this package does.
**Done when:** `grep 'http\.Do\|http\.Client{' services/payments/` shows zero direct call sites outside `internal/retry/`.

### 3. [P2] [drift] New DynamoDB-backed store for idempotency keys
**ADR:** ADR-0007 (Postgres event store)
**ADR section:** Considered Options — DynamoDB rejected because "operational cost of a second stateful system outweighs read-latency advantage for our QPS."
**Location:** services/payments/idempotency/dynamo_store.go (new file, 142 lines), services/payments/idempotency/config.go:18–24

ADR-0007 chose Postgres as the event store and explicitly rejected DynamoDB. This change introduces a DynamoDB-backed store for idempotency keys — technically a different data domain, but it reintroduces the exact operational cost the ADR rejected (a second stateful system to monitor, back up, and pay for). If idempotency keys genuinely need DynamoDB semantics, that's a supersede conversation on ADR-0007, not a silent addition.

**Fix:** either (a) implement idempotency keys on Postgres (the table already exists; a unique constraint on `(tenant_id, key)` gets you the same semantics), or (b) open a supersede flow on ADR-0007 to justify introducing DynamoDB for this use case.
**Done when:** `services/payments/idempotency/` contains only a Postgres-backed implementation, OR a new ADR accepted that amends ADR-0007 to permit DynamoDB for narrowly-scoped stores.

### 4. [P3] [driver-shift] ADR-0012 cited "team has no expertise with circuit breakers"
**ADR:** ADR-0012 (retry policy)
**ADR section:** Decision Drivers > #2
**Location:** PR metadata, not a code finding

ADR-0012 chose the simple retry-only approach over a circuit-breaker library, citing "team has no expertise with circuit breakers" as a driver. This PR's author shipped a circuit breaker in `services/inventory/` last quarter. The driver as written no longer holds — this doesn't make the current change wrong, but the next time someone proposes a circuit breaker they should not be blocked by a driver that's stale. Candidate for revisit.

**Fix:** no action required on this PR. Consider running the create-adr skill (supersede flow) on ADR-0012 to update the driver list, or add a note to the ADR's implementation notes.
**Done when:** ADR-0012 is either explicitly revalidated or superseded.

### On-policy ADRs

(none — all three applicable ADRs had findings)

### Driver Shifts

- **ADR-0012** — driver "team has no expertise with circuit breakers" contradicted by recent codebase evidence. See finding #4.
---
```

---

## Format Rules

### Start with a scope + ADR-triage header

Before any findings, show (1) the change scope (diff range, file count), (2) the ADRs triaged into applicable / possibly applicable / out of scope buckets with counts, and (3) the summary line. This lets the reader catch a miscategorized ADR before reading the findings.

### Every finding cites ADR id + ADR section + file:line

If a finding cannot cite all three, omit it. `[P2] Bad retry handling` with no ADR anchor is not a policy review finding — it's a code review finding, and belongs in a different review.

### Tag every finding with its category

`[violation]`, `[erosion]`, `[drift]`, or `[driver-shift]`. These are not interchangeable. Violations are textual and should block; erosions weaken invariants; drift is directional; driver-shifts are signals for ADR revisit, not complaints about the change.

### Sort by priority, then by category

P1 first, then P2, then P3. Within a priority, sort violation > erosion > drift > driver-shift — this keeps the strongest claims at the top of each tier.

### Driver-shift findings get their own closing section

They are not about the change; they are about the ADR. Repeating them in a separate "Driver Shifts" closing section (even if they also appear in the main findings list as P3) flags to the user: "consider opening a supersede flow."

### On-policy ADRs are named, not hidden

If an applicable ADR produced no findings, list it under "On-policy ADRs" so the user knows it was reviewed and cleared. Silence is ambiguous; "ADR-0004 reviewed, no findings" is not.

### No findings means no findings — don't invent

If the change is on-policy against every applicable ADR, say so plainly. List the ADRs reviewed and state the change respects them. Padding a clean review with P3 nitpicks undermines trust in the P1s and P2s.
