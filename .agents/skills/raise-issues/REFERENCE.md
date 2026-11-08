# Reference

Slug handling, file shapes, and a worked example for `raise-issues`.

## Review identities

The canonical `skill` values are `code`, `tests`, `docs`, and `portability`. The retired `o11y`
review no longer ships; its keys on existing issues still match and are passed through unchanged.
The older `skills` value aliases `portability`. Normalize that alias in new plans, and search
both `portability/<pattern>/<scope>` and `skills/<pattern>/<scope>` before filing a portability
finding. Existing stored review keys stay unchanged, including when refreshing issue bodies.
Apply the normal open/completed/declined match rules to either spelling.

## Pattern slugs

The `pattern` component of a review key is not defined here. Each review skill owns the
vocabulary for its own domain, in its `REFERENCE.md` — `review-portability` names harness-coupling
patterns, `review-tests` names test-quality ones, and so on. The reviewer picks the slug, because the
reviewer is what knows which root cause it just identified.

That leaves this skill with one rule rather than a list: **pass the slug through unchanged.**
Normalising, re-spelling, or "tidying" a slug here breaks identity for every issue already filed
under the original, which is the one failure this whole scheme exists to prevent.

A finding that arrives with no slug — from a hand-written list, or a review that predates the
findings file — needs one before it can be filed. Read the relevant skill's `REFERENCE.md` and
pick from there; fall back to `other-<slug>` only when nothing fits.

## Findings file

What `--findings <path>` expects. `files` drives the mechanically-derived scope, so it must
list every affected path — for a pattern-collapsed finding, all of them.

```json
{
  "skill": "o11y",
  "scope_reviewed": "internal/http",
  "findings": [
    {
      "priority": "P1",
      "pattern": "credential-in-log",
      "title": "Authorization header written to logs on every request",
      "files": ["internal/http/middleware/logging.go"],
      "locations": ["internal/http/middleware/logging.go:13"],
      "explanation": "LogRequests prints the full r.Header map, so every bearer token reaches stdout and the log aggregator.",
      "fix": "Log an allowlist of headers (User-Agent, Content-Type, X-Request-Id) and drop the raw header map.",
      "done_when": "No call site passes r.Header or any http.Header value into a log field."
    }
  ]
}
```

`priority`, `files`, and `fix` are required; a finding missing any of them is skipped and
counted. `pattern` may be omitted, in which case pick one from the vocabulary above.

## Plan file

One JSON object per line, consumed by `hew apply`. `where` carries the locations and the review
key; `done-when` is a list, one checklist item per entry.

```jsonl
{"title":"Logging: Authorization header written to logs on every request","type":"bug","priority":"P1","where":"`internal/http/middleware/logging.go:13`\n\nreview-key: o11y/credential-in-log/internal/http/middleware/logging.go","problem":"`LogRequests` prints the full `r.Header` map, so every bearer token reaches stdout and the log aggregator.","fix":"Log an allowlist of headers (`User-Agent`, `Content-Type`, `X-Request-Id`) and drop the raw header map.","done-when":["No call site passes `r.Header` or any `http.Header` value into a log field."]}
{"title":"Logging: error wraps drop the cause chain","type":"bug","priority":"P2","where":"`internal/http/repo/orders.go:25`, `internal/http/repo/orders.go:34`, `internal/http/stripe/client.go:41`\n\nreview-key: o11y/error-wrap-drops-cause/internal/http","problem":"Every wrap site uses `fmt.Errorf` with `%s` and `err.Error()`, so `Unwrap` returns nil and `errors.Is` stops working.","fix":"Replace each with `fmt.Errorf(\"...: %w\", err)`.","done-when":["No `fmt.Errorf` call in `internal/http` formats an error with `%s` or `.Error()`."]}
```

Add `"discovered-from": <n>` when re-filing a regression against a closed issue. `"parent"`
attaches the issue as an epic child and `"blocked-by"` defers it behind other issues — both
are how pump-driven filing wires findings into an epic (a finding's fix waits for the PR it
was found in to merge).

## Converter script

`scripts/findings_to_plan.py` turns a findings file into a plan deterministically — the pump
path, where no agent hand-composes plan lines:

```bash
findings_to_plan.py <findings.json> [--parent <epic>] [--reviewed-issue <n>]
                    [--reviewed-pr <n>] [--at-or-above P1..P4] [--type bug|task]
                    [--out FILE]
```

Per finding it emits the plan shape above, deriving `review-key: <skill>/<pattern>/<scope>`
mechanically (scope per the table below) and appending `review-of: #<n> (PR #<pr>)` to
`where` when `--reviewed-issue` is given. The `review-of:` marker is what the work-epic merge
pass greps to attribute an open finding child to the PR it holds — treat it like
`review-key:`: bare, exact, never re-rendered. A finding that carries `fixedIn: <sha>` (the
work-epic reviewer fixes what it can in the session that found it) also gets a bare
`fixed-in: <sha>` line, which the pump greps to close the filed issue as completed. Priority passes straight through, clamped to
`P1..P4` (a P0 finding files as P1 — P0 is a human's declaration). Dedup is *not* the
converter's job: run Step 3 against the emitted plan yourself before `hew apply`, with
`dedup_candidates.py` (below) narrowing the candidates.
The converter emits `portability/` for both `portability` and legacy `skills` input; deduplication
must still search for already-filed `skills/` keys.

A finding is skipped and counted when it lacks priority, files, fix, or pattern — the pump
files from critique findings files, which always carry a pattern; hand-written lists go
through the agent flow instead. `--at-or-above P2` also skips findings less severe than P2,
for callers (the work-epic pump) that relay those elsewhere rather than track them; the
threshold compares the finding's own priority, so a P0 still passes and files as P1. The whole file is rejected when `skill` is missing (a finding may carry its own `skill`, which wins over the file's: the work-epic reviewer merges its tests-lens review into the code-lens file that way), and a
file whose `status` is `no_scope` or `error` exits 3 — nothing was reviewed, which is a
different outcome from "reviewed, nothing found" (exit 0, empty plan).

## Dedup candidates script

`scripts/dedup_candidates.py` applies the mechanical half of Step 3 to a plan:

```bash
dedup_candidates.py <plan.jsonl> <issues.ndjson> [--reviewed-issue <n>] [--out FILE]
```

`issues.ndjson` is `hew list --json --bodies --state all`. Per plan line it prints
`{line, key, candidates, other_generation}`. A candidate is an issue whose stored `review-key:`
line equals the plan line's key exactly (the `portability/` and legacy `skills/` spellings count
as one) and, when `--reviewed-issue` is given, whose `review-of:` names that issue. Each carries
`same_paths`, true when its affected paths — line numbers ignored — are the same set as the plan
line's; a false value is information (the finding may have widened), not a filter. Issues with
the same key raised against any other review target are listed in `other_generation` and never
suppress the finding. Whether a candidate describes the same problem stays with the agent.

## Deriving scope

The scope component is the deepest directory containing every file in `files`, or the file
itself when there is one:

| `files` | scope |
|---|---|
| `["internal/http/middleware/logging.go"]` | `internal/http/middleware/logging.go` |
| `["internal/http/repo/orders.go", "internal/http/stripe/client.go"]` | `internal/http` |
| `["cmd/api/main.go", "internal/worker/job.go"]` | *(repo root)* — use the skill name alone: `o11y/silent-retry/.` |

A finding spanning the whole repository is usually a sign the collapse went too far; prefer
splitting it per top-level area over anchoring at the root.

## Worked example

A review reports two findings. The first has been filed before and its issue is still open; the
second matches an issue that was closed as completed.

```
$ hew search "review-key: o11y/credential-in-log/internal/http/middleware/logging.go"
#118 P1 bug  Logging: Authorization header written to logs on every request [open]

$ hew search "review-key: o11y/error-wrap-drops-cause/internal/http"
#96  P2 bug  Logging: error wraps drop the cause chain [closed]

$ hew show 96          # closed how?
... closed as completed by #97 ...
```

The first is skipped — already tracked. The second is a regression: the pattern came back after
a fix shipped, so it is filed fresh with `"discovered-from": 96` linking the history, not
silently re-raised as if it were new.

A finding about a fix works the other way. Suppose #118 was filed from a review of product code
and its fix PR is now reviewed; the review reports the same pattern in the same directory.

```
$ dedup_candidates.py plan.jsonl issues.ndjson --reviewed-issue 118
{"line": 1, "key": "o11y/credential-in-log/internal/http/middleware", "candidates": [], "other_generation": [118]}
```

#118 carries the same key but no `review-of: #118`, so it is the finding the PR fixed, not this
one. The new finding is filed (with `blocked-by` and `discovered-from` pointing at #118, as the
converter already emits). Re-running that review later lists the new issue under `candidates`,
because it carries `review-of: #118`, and the finding is skipped.

Had #96 been closed as not-planned, the finding would be suppressed instead — permanently. A
declined issue is a decision, and re-filing it every cycle is how a review pipeline gets muted.
