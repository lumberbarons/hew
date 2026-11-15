Use the work-issue skill. Work issue #${n} exactly as it specifies, with flags
--non-interactive --json '.work-epic/we-${n}.json'. When it finishes, create `.work-epic/`
if needed and write that file; do not commit it. Then send the worker_done message the
orchestration preamble specifies, with --outcome succeeded only if the file's status is
delivered, otherwise failed, and --report-path '.work-epic/we-${n}.json'. Do not ask
questions and do not interact with the user.
