# Example Output

This reference shows the expected format and level of detail for a tests review report.

---

## Example Report

```
---
Tests Review for tests/order

3 files reviewed, 4 issues found (1 P1, 2 P2, 1 P3).

### 1. [P1] Shared mutable state between test cases
**Location:** tests/order/service_test.go:34

A package-level `testDB` variable is opened in TestMain and reused across all tests without resetting state. Tests pass individually but fail when run together because earlier tests leave rows that affect later assertions. Use per-test setup with t.Cleanup to guarantee isolation.

### 2. [P2] Assertion checks error only, ignores result
**Location:** tests/order/service_test.go:78

TestPlaceOrder calls PlaceOrder and asserts `err == nil` but never inspects the returned order. The test claims to verify order placement, but if PlaceOrder were changed to `return Order{}, nil` (silently returning a zero-value order), this test would still incorrectly pass — the central contract is unverified. Assert on expected fields (ID, total, status) to catch real regressions.

### 3. [P2] Test name doesn't communicate intent
**Location:** tests/order/handler_test.go:12

"TestHandler" gives no indication of what scenario is being verified. When this test fails in CI, the developer must read the test body to understand what broke. Rename to TestCheckout_AppliesVolumeDiscount to make failures self-describing.

### 4. [P3] Missing edge case for duplicate order
**Location:** tests/order/repo_test.go:55

Only the happy path (create new order) is tested. There is no test for what happens when an order with the same ID already exists. Add a test that inserts a duplicate and asserts the expected conflict error.
---
```

---

## Format Rules

### Findings use H3 headers with priority tag, Location, and explanation

```
### 1. [P1] Shared mutable state between test cases
**Location:** tests/order/service_test.go:34

Explanation of the test quality problem and rationale for the fix.
```

### Only show issues found — no passing rows

Do not include items that passed review. Start with "N files reviewed, M issues found (severity breakdown)."

### No tables

Do not include summary tables or issue tables. Findings are the only output.

### Truncation footer when the cap kicks in

When findings exceed the reporting cap (see SKILL.md → Reporting Cap), end the report with a single-line footer:

```
Note: 7 additional findings omitted (4 P2, 3 P3) — re-run after addressing these to surface what remains.
```

The footer is omitted when all findings fit under the cap. The header count reflects the *reported* findings; the footer count reflects the *omitted* tail.

---

## Pattern slugs

The `pattern` field of a findings file (see [FINDINGS.md](FINDINGS.md)) names a finding's
root cause. Consumers use it as part of a finding's identity across runs, so it has to be stable
between them — a slug that drifts from one phrasing to another reads as a new finding and gets
filed twice. Pick from this list; use `other-<slug>` when nothing fits, and treat a recurring
`other-` slug as a sign this list needs extending.

```
dead-expectation
isolation-hazard
loose-matching
missing-coverage
missing-edge-case
permanently-skipped-test
reimplemented-logic
shared-mutable-state
tautological-assertion
unclear-test-name
```
