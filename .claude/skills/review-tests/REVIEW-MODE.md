# Review mode and repair

Shared policy for `review-code` and `review-tests`. Decide the mode before writing findings
files or making changes. The review itself remains a static, read-only pass; the calling
agent owns any subsequent repair.

## Decide the target, then the mode

Translate the request into one script scope; do not pass natural-language text to the script:

| Scope | When |
| --- | --- |
| `pr` | A PR link, number or explicit PR target was supplied, even if its branch is checked out |
| `branch` | Reviewing the current local branch's changes against the default branch, explicitly or through the skill's no-path default |
| `audit` | Reviewing a repository, directory or file generally, including a general review on the default branch |
| `unknown` | The target or intent is ambiguous |

A path alone selects an audit. If the request explicitly reviews branch changes within a path,
use the branch diff restricted to that path, not every existing file under it. A PR target is
not a filesystem path: read its base, head and diff with the available forge tools, and review
that target's code. Do not silently substitute the current checkout's diff. If the target
cannot be resolved, report `error` rather than a clean review. Do not switch or reset the user's
checkout to obtain a PR's code.

Resolve the script inside this skill's directory:

```bash
python3 "<this skill's directory>/scripts/review-mode.py" --scope branch
```

Replace `branch` with the scope above. Add `--fix` for an explicit request to fix (including
"and fix"), or `--report-only` for "don't fix"/"report only". Conflicting explicit instructions
are ambiguous: use report-only unless clarified. A caller that specifies its own later
repair/commit/report phase owns that phase: pass `--report-only` for this skill invocation
and return the original findings for the caller to handle. This preserves existing automated
review-and-fix workflows without performing repairs twice.

The script emits JSON with `mode` (`report` or `fix`), `reason`, `scope` and `automatic`.
Explicit intent overrides defaults. Otherwise only `branch` can fix, and all these must hold:

- The current branch has a nonempty `feat/`, `fix/` or `chore/` prefix and is not the default.
- The default branch is recorded by origin HEAD, or a local-only repo has exactly one of
  `main` and `master`. A remote without recorded default metadata is uncertain.
- The working tree has no staged, unstaged or untracked changes.
- There is one merge base and a nonempty added/modified-file diff against that default.

All other cases report only. `--json` and `--non-interactive` do not authorize fixes or change
the selected mode. If the script fails, its output cannot be read, or Git state is uncertain,
report only. State the chosen mode and reason in one short sentence before reviewing.

For automatic fixing, retain the script's `root`, `branch`, `head`, `base` and `base_commit`.
Immediately before repair, rerun it: it must still allow automatic fixing and those fields
must still match. Otherwise leave the findings unresolved and explain the changed state.
Do not make the tree appear clean by stashing, deleting, committing or ignoring user files.

## One bounded repair pass

Finish and merge the independent review before editing. In report mode, return the existing
findings format and stop. In fix mode, the calling agent makes one repair pass over confirmed
findings, followed by relevant verification:

- For automatic mode, fix only problems introduced by the reviewed branch, within its changed
  files and their direct tests. A file the branch left unchanged may be edited only when the
  branch's change is incomplete without it (a migration list, an export, a schema version), and
  the repair summary names it. Existing unrelated problems remain findings. For an explicit
  fix request, stay within its authorized target; confirm the checkout matches the reviewed
  PR head before editing a PR's code. An unresolved or mismatched target remains report-only.
- Make the smallest correction that preserves intended behavior. Leave broad redesigns,
  behavior decisions, conflicting prescriptions or fixes outside the authorized scope
  unresolved, with a reason. A test review repairs tests; report production defects for a
  code review rather than changing production behavior to satisfy a test.
- Preserve pre-existing edits when an explicit fix request allows a dirty tree. Run the
  project's required checks relevant to the repair; never weaken or delete assertions just
  to obtain a pass. If verification fails or is unavailable, do not claim the repair succeeded.
  A project with no checks is not by itself unavailable: when the failing trace can be
  reproduced, show it failing before the edit and passing after, name that reproduction as the
  verification and say the project has no checks. If the trace cannot be reproduced, leave the
  finding unresolved.
  Undo only that attempted repair's edits, preserving user work and earlier verified repairs,
  and report the finding unresolved with the failure or limitation.
- Do not start a repeated review/fix loop. Do not commit, push, publish PR comments or file
  issues unless the caller explicitly requested those actions. A separate workflow may do
  those steps after this skill returns.

## Report unresolved work separately

In fix mode, the normal H3 findings and their count describe only unresolved findings, ready
for `raise-issues`. Give a separate brief repair summary and verification results; do not put
repaired items into the unresolved findings list. Say when every finding was repaired rather
than implying the initial review found nothing. Do not call a still-broken codebase sound.

When `--json <path>` was requested, write the final file after repair. Preserve the schema in
`FINDINGS.md`: `findings` holds unresolved items, `repaired` holds the original repaired findings
with verification evidence, and `mode`/`mode_reason` record the decision. In report mode all
findings remain in `findings`. Existing callers that own the repair phase receive report mode
and may add their existing `fixed`/`fixedIn` metadata afterwards; do not replace their format.
