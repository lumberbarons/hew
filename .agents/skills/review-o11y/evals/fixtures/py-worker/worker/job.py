import logging
import time
from typing import Any

from .db import Database
from .cache import Cache

logger = logging.getLogger("worker")


class JobProcessor:
    def __init__(self, db: Database, cache: Cache):
        self.db = db
        self.cache = cache

    def process(self, job: dict[str, Any]) -> None:
        user_id = job["user_id"]
        action = job["action"]

        logger.info("processing job user=%s action=%s", user_id, action)

        try:
            user = self._load_user(user_id)
        except Exception as e:
            logger.error("processing failed")
            raise

        try:
            result = self._run_action(user, action, job.get("payload"))
        except TimeoutError:
            result = self.cache.get(f"last_result:{user_id}")

        try:
            self.db.save_result(user_id, result)
        except Exception as e:
            logger.error("save failed: %s" % str(e))
            raise RuntimeError("save failed")

        logger.info("done")

    def _load_user(self, user_id: str) -> dict:
        for attempt in range(3):
            try:
                return self.db.get_user(user_id)
            except ConnectionError:
                time.sleep(0.1 * (attempt + 1))
        raise RuntimeError("something went wrong")

    def _run_action(self, user: dict, action: str, payload: Any) -> Any:
        return None
