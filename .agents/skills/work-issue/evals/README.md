# work-issue evals

How to run the eval set in `evals.json`. The fixtures themselves live in
`../../work-issue-workspace/fixtures/`, which is gitignored like every other skill workspace in
this repo — this file is the tracked description of what has to be built there.

## Why a stub instead of a fixture repo

`work-issue` is different from the critique skills to evaluate. Those read a code fixture and
emit a report, so a directory of source files is the whole harness. This one *acts*: it claims
issues, pushes branches, and opens pull requests. Running it against a live repository to score
it would create real issues and real PRs, and every rerun would need them cleaned up first —
which also makes the runs non-deterministic, because the queue is different on the second pass.

So the fixture replaces the CLI rather than the repository.

## The hew stub

`hew-stub/hew` is an executable placed ahead of the real binary on `PATH`. It appends every
invocation to `$HEW_LOG` — one command line per row — and serves reads from a small mutable
JSON store seeded per scenario from `$HEW_SCENARIO`.

**The store has to be stateful, not a table of canned responses.** Canned replay keys a response
to a command, which means `start 42` followed by `show 42` needs the post-claim `show`
pre-recorded, and any deviation in command order breaks the fixture. Command order is exactly
what these evals assert on, so a stateless stub fights its own purpose. A store where `start`
actually writes a claim, a later `start` reports that claim back — exit 3 when another user holds
it, exit 5 when it is the runner's own — and `show` reflects both, is robust to whatever order
the skill chooses, which is the point, since the order is the thing under test.

The log is what most assertions read, because the interesting claims about this skill are claims
about *protocol*, and protocol is a sequence:

- did `hew start` come before the first source edit, or after?
- was there a test run between the branch and the implementation, and did it fail?
- did the run end at `hew pr`, or did it call `hew close`?
- when `start` returned exit 3, did the run move on or reach for `--force`?

None of those are visible in a final diff. All of them are visible in an ordered command log,
and none of them require a network.

**Known weakness, and the upstream fix.** This stub is a hand-maintained copy of hew's JSON
schema. When `hew show --json` changes shape, the stub keeps serving the old one and these evals
keep passing against a CLI that no longer exists — silently, until someone runs the real binary.
That is the same drift these skills exist to catch, relocated into the test harness. The durable
fix belongs in hew rather than here: [lumberbarons/hew#74](https://github.com/lumberbarons/hew/issues/74)
proposes a local test backend behind the real command surface, where a contract test can assert
both backends behave identically. Treat this stub as interim, and re-verify it against the real
CLI whenever hew's output shape moves.

## Scenarios

Each scenario is a directory under `../../work-issue-workspace/fixtures/` holding the canned
`ready`/`show`/`list` payloads plus a small real project — real enough that TDD has something to
bite on, since a test that cannot fail proves nothing about a skill that is supposed to watch it
fail.

| Scenario | Shape | What it isolates |
|---|---|---|
| `scenario-ready-bug` | One ready `bug` with a three-item Done-when | The happy path: claim → red → green → verify → PR |
| `scenario-epic` | `#40` is an epic; children open, closed, and blocked, with a P3 listed ahead of a P1 | Descent, the filters, and priority order the listing does not guarantee |
| `scenario-all-ineligible` | Ready queue of one claimed and two untriaged issues, plus an epic with ready children | `not_eligible` vs `no_ready_work`, and not descending into an epic uninvited |
| `scenario-no-done-when` | Issue with Where/Problem/Fix and an empty Done-when | Whether criteria get synthesized or the finish line gets guessed |
| `scenario-gate-fails` | A test in the project fails for a reason outside the issue's scope | Push-but-no-PR, and leaving the claim in place |
| `scenario-resume-own-claim` | Top of the queue is already claimed by the runner, with a partial branch | exit 5 as resume, not collision — the state `scenario-gate-fails` leaves behind |
| `scenario-batch-independent` | Three small ready bugs whose fixes genuinely do not interact, with more ready work behind them | Sizing before claiming, three independent branches, a passing integration pass, a truthful `remaining` |
| `scenario-batch-coupled` | Three ready bugs, two of which change behaviours that contradict each other — in different files | `integration_failed`, and filing the coupling instead of patching a PR |
| `scenario-batch-large-issue` | Top of the queue names six paths in `### Where` and seven `### Done when` items | The size rule lowering the ceiling to one |
| `scenario-local-tracker` | One ready `bug`, a repository with no remote, and a stub whose primer is the local one (workflow ends at `hew close --completed`) and whose `pr` refuses | Work stopping at a committed branch: no push, no `hew pr`, no merge, no close |

`scenario-gate-fails` is the one worth building carefully. The failure has to be genuinely
outside `### Where`, or a capable model will simply fix it and pass the eval for the wrong
reason — the behaviour under test is restraint plus an honest report, not repair.

`scenario-batch-coupled` needs the same care, for the mirror-image reason. **The two fixes must
not touch the same lines.** If they do, git reports a merge conflict and the eval scores the
skill on conflict handling — which git already solves — rather than on the case that motivates
the whole integration pass: two changes that merge perfectly and disagree at runtime. One issue
changing what a caller expects while another changes the callee's contract, in separate files,
is the shape to build. The suite must pass on each branch alone and fail only once both are
merged, or the scenario proves nothing.

## Exit codes the stub must reproduce

| Code | Condition | Why it matters here |
|---|---|---|
| 3 | `start` on an issue claimed by someone else | The concurrency lock. The skill must treat it as "move on", never as an error to force past. |
| 5 | `start` on an issue already claimed by the runner | A resume, not a collision. Conflating it with 3 makes an interrupted run unresumable — it would skip its own unfinished work forever. |
| 4 | any command, unauthenticated | Must surface as `error`, not as an empty backlog. |
| 1 | `pr` in `scenario-local-tracker` | Prints `pr needs the GitHub backend; this repo tracks issues locally`, as the real hew does. A skill that runs it to discover the mode, rather than reading the primer, fails the eval. |
