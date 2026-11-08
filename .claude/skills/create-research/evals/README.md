# create-research evals

Five evals on the judgement `research.py` cannot make. The script pins the format; whether a
finding is honest, a question is really a question, and an existing document is reused
instead of duplicated is the agent's call.

`fixtures/jobqueue/` is a small Redis-backed job queue with one trap: the README promises
at-least-once delivery, but `dequeue` uses `BLPOP`, which removes the job before the handler
runs, so a worker crash loses it. It also holds an accepted ADR and a complete research
document (`RES-0001`, queued jobs survive a Redis restart) that passes the checker.

| Eval | What it tests |
|------|---------------|
| `codebase-question` | Finds the real answer in the code against the README's claim, numbers the document after 0001, cites only sources that exist |
| `decision-in-disguise` | A "which should we pick" request becomes research on what is true; the choice is left to an ADR and none is written |
| `already-answered` | Reports `RES-0001` and writes no duplicate when the question is already answered and still fresh |
| `stale-supersede` | Supersedes a document whose Revisit When condition has been met, leaving the old record intact apart from its status |
| `topic-not-question` | Asks to narrow "research caching" instead of scaffolding a document |

Each run gets its own copy of the fixture with `git init` run in it, so the script finds a
repository root. `stale-supersede` carries a `setup` step: `appendonly yes` becomes
`appendonly no` in the copy, which is the change the prompt describes.

The tracker-free design means a run needs no network for the repo-bound evals. The SQS eval
may reach for the web; either is fine as long as the document says which it did.
