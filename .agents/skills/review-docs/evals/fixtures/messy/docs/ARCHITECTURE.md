# Architecture

The control API in `src/control.js` owns all writes to Redis. Services never write
job state directly — they call the control API. This is the invariant that keeps
the reaper from racing the workers.
