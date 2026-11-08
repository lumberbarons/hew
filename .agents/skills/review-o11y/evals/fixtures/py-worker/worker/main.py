import logging
import os

from .db import Database
from .cache import Cache
from .job import JobProcessor

logging.basicConfig(level=logging.INFO)


def main() -> None:
    db_url = os.environ["DATABASE_URL"]
    cache_url = os.environ["CACHE_URL"]
    batch_size = int(os.environ.get("BATCH_SIZE", "10"))

    db = Database(db_url)
    cache = Cache(cache_url)
    processor = JobProcessor(db, cache)

    while True:
        jobs = db.claim_jobs(batch_size)
        for job in jobs:
            processor.process(job)


if __name__ == "__main__":
    main()
