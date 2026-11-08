# Example Output

This reference shows the expected format and level of detail for a documentation review report.

---

## Example Report

```
---
Documentation Review for .

9 files reviewed, 8 issues found (1 P1, 2 P2, 4 P3, 1 P4).

### 1. [P1] Quick start seed command is destructive
**Location:** README.md:45

The quick start instructs users to run `make seed`, which drops all tables before recreating them. A new contributor following these steps on a shared dev database would destroy existing test data. Either add a prominent warning or change to `make seed-safe` which inserts only missing rows.

### 2. [P2] API example references deleted endpoint
**Location:** docs/api.md:23

The curl example calls `POST /v1/users` which was removed in the v2 migration. Running the example as-is returns a 404. Update to `POST /v2/users` and include the required `Authorization: Bearer` header that v2 requires.

### 3. [P3] CLAUDE.md index missing migrations directory
**Location:** backend/CLAUDE.md

The backend/migrations/ directory contains 12 migration files but has no entry in the CLAUDE.md index table. Add a row: `migrations/` | SQL migration files | Changing database schema.

### 4. [P3] No expected output after main setup step
**Location:** README.md:12

After `docker compose up`, the reader has no way to verify success. Add a "You should see:" block showing the expected startup log lines so newcomers can distinguish success from a silent failure.

### 5. [P4] Inconsistent terminology for setup section
**Location:** frontend/README.md:8

The frontend README uses "Installation" in its heading but the root README links to it as "Setup". Standardize on "Setup" to match the root README's terminology.

### 6. [P2] Broken import target in root CLAUDE.md
**Location:** CLAUDE.md:14

The import `@docs/setup-guide.md` points to a file that was renamed to `docs/getting-started.md` in commit abc1234. Any tool resolving this import will fail silently. Update the import path to `@docs/getting-started.md`.

### 7. [P3] Personal sandbox URL in shared CLAUDE.md
**Location:** CLAUDE.md:31

The line `API_BASE=http://localhost:3001` appears to be a developer-specific sandbox URL. Other contributors may use a different port or remote endpoint. Move this to `CLAUDE.local.md` or replace with a placeholder like `http://localhost:<port>`.

### 8. [P3] Stale setup command in AGENTS.md
**Location:** AGENTS.md:22

The setup section instructs contributors to run `make bootstrap`, which was replaced by `make setup` when the Makefile was reorganized for v2. Anyone — human or agent — following it stops at the first step with "no rule to make target 'bootstrap'". Update the command and add the required Go toolchain prerequisite beside it.
---
```

---

## Format Rules

### Findings use H3 headers with priority tag, Location, and explanation

```
### 1. [P1] Quick start seed command is destructive
**Location:** README.md:45

Explanation of the documentation problem and rationale for the fix.
```

### Only show issues found — no passing rows

Do not include items that passed review. Start with "N files reviewed, M issues found (severity breakdown)."

### No tables

Do not include summary tables or issue tables. Findings are the only output.

---

## Pattern slugs

The `pattern` field of a findings file (see [FINDINGS.md](FINDINGS.md)) names a finding's
root cause. Consumers use it as part of a finding's identity across runs, so it has to be stable
between them — a slug that drifts from one phrasing to another reads as a new finding and gets
filed twice. Pick from this list; use `other-<slug>` when nothing fits, and treat a recurring
`other-` slug as a sign this list needs extending.

```
broken-reference
derivable-content
drifted-enumeration
failing-quickstart
hardcoded-local-path
missing-expected-output
missing-prerequisite
oversized-claude-md
stale-command
```
