"""Enqueue and dequeue jobs against Redis."""

import json

import redis

QUEUE_KEY = "jobs:pending"
DEAD_KEY = "jobs:dead"

client = redis.Redis()


def enqueue(job: dict) -> None:
    client.rpush(QUEUE_KEY, json.dumps(job))


def dequeue(timeout: int = 5) -> dict | None:
    """Take the next job off the queue, or None if the timeout expires."""
    item = client.blpop(QUEUE_KEY, timeout=timeout)
    if item is None:
        return None
    _key, raw = item
    return json.loads(raw)


def bury(job: dict) -> None:
    client.rpush(DEAD_KEY, json.dumps(job))
