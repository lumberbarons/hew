"""The worker loop: take a job, run it, retry it if the handler raises."""

import logging

from jobqueue import queue

MAX_RETRIES = 3
log = logging.getLogger("jobqueue.worker")


def run_forever(handler) -> None:
    while True:
        job = queue.dequeue()
        if job is None:
            continue
        process(job, handler)


def process(job: dict, handler) -> None:
    attempts = job.get("attempts", 0)
    try:
        handler(job)
    except Exception:
        log.exception("job %s failed (attempt %d)", job.get("id"), attempts + 1)
        if attempts + 1 >= MAX_RETRIES:
            queue.bury(job)
        else:
            queue.enqueue({**job, "attempts": attempts + 1})
