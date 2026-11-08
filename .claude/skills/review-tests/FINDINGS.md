# Findings File

The machine-readable output every review skill writes when given `--json <path>`. The markdown
report is unchanged and remains the default; this file is additive, for pipelines that would
otherwise have to parse prose.

## Shape

```json
{
  "skill": "code",
  "status": "reviewed",
  "scope": "internal/http",
  "files_reviewed": 11,
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
  ],
  "omitted": { "count": 0, "P2": 0, "P3": 0 }
}
```

## Fields

| Field | Notes |
|---|---|
| `skill` | `code`, `tests`, `docs`, or `portability` |
| `status` | `reviewed`, `no_scope`, or `error` — see below |
| `reason` | Required when `status` is not `reviewed`; the stderr message or why nothing was in scope |
| `scope` | The path or branch range actually reviewed |
| `files_reviewed` | Count matching the markdown header line |
| `findings` | Empty array when the review ran and found nothing |
| `omitted` | The truncated tail when the ten-finding cap applied; zeros otherwise |
| `mode` | `report` or `fix` for code/test reviews; other skills may omit it |
| `mode_reason` | The classifier's reason, or why a requested repair could not safely proceed |
| `repaired` | In fix mode, original findings successfully repaired, each with a `verification` string naming checks and results; never issue-filing input |

For code/test reviews in fix mode, `findings` contains only unresolved findings, and the markdown
header count matches that list. Put verified repairs in `repaired`, even when none remain
unresolved. In report mode, keep the original full findings list. Consumers such as
`raise-issues` read `findings`, not `repaired`, so successful repairs are not filed as new work.
Do not claim a clean initial review when all its findings were repaired.

Per finding, `priority`, `files`, and `fix` are what consumers depend on. `review-code` also sets
`lens` to `design` or `correctness`, saying which read raised the finding. It is informational:
a finding's identity is its skill, pattern and `files`, not its lens, so a defect keeps the same
key whichever lens finds it. Other skills omit it. A finding missing any
of them is unusable downstream, so fill them or drop the finding.

`portability` is the canonical identity for `review-portability`. Older reports used `skills`;
consumers accept that alias as `portability` and search both identity prefixes when deduplicating
against existing tracker issues. Preserve existing issues' stored review keys.

**`files` must list every affected path.** For a pattern-collapsed finding that means all of
them, not a representative sample. Downstream consumers derive a stable identity for the finding
from this list — `hew:raise-issues` computes the deepest common directory from it — so a partial
list silently changes what the finding *is* between runs, and the same finding gets filed twice.

**`pattern`** is a slug naming the underlying anti-pattern, drawn from the vocabulary in each
skill's `REFERENCE.md`. Two findings sharing a root cause must share a slug — that is the same
judgement the collapse step already makes, written down. Use `other-<slug>` when nothing fits;
recurring `other-` slugs are the signal to extend the vocabulary rather than to keep improvising.

## Status, and why it is not optional

`reviewed` with an empty `findings` array and `no_scope` both mean zero findings, and they mean
opposite things:

- **`reviewed`** — the review ran. Zero findings is a result.
- **`no_scope`** — nothing was in scope to review. The branch had no diff, or the path was empty.
- **`error`** — the review could not run. Not a git repository, a path that does not exist, a
  detached HEAD, an indeterminate default branch.

An automated caller that treats all three as "clean" will report a broken pipeline as a healthy
codebase, and will keep doing so until someone notices the absence of findings is permanent.
That failure is silent by construction, which is exactly why the status field carries it
explicitly rather than leaving it to be inferred from an empty array.
